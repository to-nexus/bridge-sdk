import { Interface, getAddress } from 'ethers';
import { executorAbi } from './abi/index.js';
import type { Ctx } from './ctx.js';
import { CORE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR, FORWARD_SELECTOR } from './encoding.js';
import { NetworkMismatchError, PreflightError } from './errors.js';
import { NATIVE_TOKEN, chainOf } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { decodeForward } from './handle.js';
import { QUOTE_STATUS, bridgeI, erc20I, hyperTokenI, maxExtraData, poolI, sbrI } from './quote.js';
import { v3FactoryAbi } from './abi/index.js';
import { readContract } from './rpc.js';
import { Route } from './types.js';
import type { PreflightIssue, PreflightReport, Quote } from './types.js';

const executorI = new Interface(executorAbi as any);
const v3FactoryI = new Interface(v3FactoryAbi as any);

export type PreflightStage = 'pre-approve' | 'post-approve';

/**
 * Structured issues + hints; never throws for a failed check (only returns it).
 * Every `error` issue is BLOCKING (send() enforces them twice, see blockingPreflight). An RPC error/timeout during a check is itself a
 * blocking `PREFLIGHT_INCOMPLETE` issue: an unverified check never counts as passed. Allowance is a warning, except at stage
 * 'post-approve' (after approve + re-quote) where it is blocking.
 */
export async function preflight(ctx: Ctx, q: Quote, stage?: PreflightStage): Promise<PreflightReport> {
  const issues: PreflightIssue[] = [];
  const checks: PreflightReport['checks'] = [];
  const add = (name: string, ok: boolean, detail?: string, issue?: PreflightIssue) => {
    checks.push({ name, ok, detail });
    if (!ok && issue) issues.push(issue);
  };
  const guard = async (name: string, fn: () => Promise<void>) => {
    try {
      await fn();
    } catch (e) {
      add(name, false, String((e as Error).message), { level: 'error', code: 'PREFLIGHT_INCOMPLETE', message: `${name}: could not be verified: ${(e as Error).message}`, hint: 'RPC read failed or timed out; an unverified check is never treated as passed. Retry or use another RPC.', check: name, error: String((e as Error).message) });
    }
  };
  const chains = ctx.net.chains;
  const hubKey: ChainKey | undefined = q.details.hubChainKey;
  const srcKey = q.sourceChainKey;
  const toChainId = q.details.hop1ToChainId;
  const srcToken = q.match.kind === 'bridge' ? q.match.fromToken : NATIVE_TOKEN;

  // 1. chain ids
  for (const k of new Set<ChainKey>([srcKey, ...(hubKey ? [hubKey] : []), ...(q.route === Route.ONE_TO_HYPERCORE || q.request.hypercore ? (['hyperevm'] as ChainKey[]) : [])])) {
    try {
      await ctx.assertChain(k);
      add(`chainId(${k})`, true, String(chains[k].chainId));
    } catch (e) {
      const m = e instanceof NetworkMismatchError ? e.message : String((e as Error).message);
      if (e instanceof NetworkMismatchError) add(`chainId(${k})`, false, m, { level: 'error', code: 'NETWORK_MISMATCH', message: m, hint: 'Point the RPC at the right network.' });
      else add(`chainId(${k})`, false, m, { level: 'error', code: 'PREFLIGHT_INCOMPLETE', message: `chainId(${k}): could not be verified: ${m}`, hint: 'RPC read failed or timed out; an unverified check is never treated as passed. Retry or use another RPC.', check: `chainId(${k})`, error: m });
    }
  }

  // a chain id that is wrong or could not be verified: report it and read NOTHING further from these RPCs (every issue is blocking anyway)
  if (issues.length > 0) return { ok: false, issues, checks };

  // 2. source bridge paused? (X3: the hub bridge the router calls)
  const srcBridge = q.sourceBridge;
  await guard('source bridge paused()', async () => {
    const p = await readContract<boolean>(ctx.rpc(srcKey), bridgeI, srcBridge, 'paused', []);
    add('source bridge paused()', !p, String(p), { level: 'error', code: 'BRIDGE_PAUSED', message: 'the source bridge is paused', hint: 'EnforcedPause: bridge under maintenance.' });
  });

  // 3. pair registered / not paused / minted stock
  if (q.match.kind === 'bridge') {
    const m = q.match;
    await guard('pair', async () => {
      const p = await readContract<any>(ctx.rpc(srcKey), bridgeI, srcBridge, 'getTokenPair', [toChainId, srcToken]);
      const registered = String(p[0]).toLowerCase() === srcToken.toLowerCase();
      add('pair registered', registered, undefined, { level: 'error', code: 'PAIR_NOT_REGISTERED', message: `no pair for ${srcToken} -> ${toChainId}`, hint: 'RegistryNotExistToken' });
      if (registered) {
        add('pair not paused', !p[3], String(p[3]), { level: 'error', code: 'PAIR_PAUSED', message: 'the token pair is paused', hint: 'RegistryTokenPaused' });
        const isOrigin = Boolean(p[2]);
        const minted = BigInt(p[6]);
        if (!isOrigin) add('wrapped stock (minted >= value)', minted >= m.value, `minted=${minted}`, { level: 'error', code: 'INSUFFICIENT_MINTED', message: `minted ${minted} < value ${m.value}`, hint: 'BaseBridgeInvalidValue: the wrapped-token stock on this pair is too low.' });
      }
    });
    await guard('source extraData length', async () => {
      const max = await maxExtraData(ctx, srcKey, srcBridge);
      const len = BigInt((q.details.extraData.length - 2) / 2);
      add('extraData length (source)', max === 0n || len <= max, `${len}/${max}`, { level: 'error', code: 'EXTRADATA_TOO_LONG_SOURCE', message: `extraData ${len} B > the source bridge max ${max} B`, hint: 'BaseBridgeExtraDataTooLong' });
    });
  }

  // 3b. X3: router.bridge() == hub, tokenOut pair on the hub
  if (q.kind === 'x3' && q.match.kind === 'swap') {
    const m = q.match;
    // fresh quote status through the timed RPC: a status other than Success (1) blocks at every stage, before any approve
    await guard('x3 quote status', async () => {
      const r: any = await readContract(ctx.rpc('one'), sbrI, q.sourceContract, 'getAmountSwapBridgeOut', [m.toChainID, m.tokenIn, m.tokenOut, q.request.x3!.poolFee, m.amountIn], { from: q.sender });
      const status = Number(r[0]);
      const name = QUOTE_STATUS[status] ?? String(status);
      add('router quote status == Success', status === 1, name, { level: 'error', code: `X3_QUOTE_${name}`, message: `SwapBridgeRouter quote status ${name}`, hint: 'NoPair: tokenOut has no hub pair to the destination; InsufficientForFee/InsufficientValue: amount too small; InvalidSwap: no pool / liquidity.' });
    });
    await guard('x3 router.bridge()', async () => {
      const b = await readContract<string>(ctx.rpc('one'), sbrI, q.sourceContract, 'bridge', []);
      add('router.bridge() == hub', b.toLowerCase() === q.sourceBridge.toLowerCase(), b, { level: 'error', code: 'ROUTER_BRIDGE_MISMATCH', message: `SwapBridgeRouter.bridge() is ${b}, expected the hub ${q.sourceBridge}` });
    });
    await guard('x3 tokenOut pair', async () => {
      const p = await readContract<any>(ctx.rpc('one'), bridgeI, q.sourceBridge, 'getTokenPair', [m.toChainID, m.tokenOut]);
      const reg = String(p[0]).toLowerCase() === m.tokenOut.toLowerCase();
      add('pair registered (tokenOut)', reg, undefined, { level: 'error', code: 'PAIR_NOT_REGISTERED', message: `tokenOut ${m.tokenOut} has no hub pair to chain ${m.toChainID}`, hint: 'RegistryNotExistToken' });
      if (reg) add('pair not paused (tokenOut)', !p[3], String(p[3]), { level: 'error', code: 'PAIR_PAUSED', message: 'the tokenOut pair is paused', hint: 'RegistryTokenPaused' });
    });
  }

  // 4. multihop / X1: hub
  if (q.kind === 'multihop' || q.kind === 'x1') {
    const hub = getAddress(chains.one.bridge!);
    let executor = '';
    await guard('hub paused()', async () => {
      const p = await readContract<boolean>(ctx.rpc('one'), bridgeI, hub, 'paused', []);
      add('hub paused()', !p, String(p), { level: 'error', code: 'HUB_PAUSED', message: 'the hub bridge is paused' });
    });
    await guard('hub executor', async () => {
      executor = await readContract<string>(ctx.rpc('one'), bridgeI, hub, 'bridgeExecutor', []);
      add('hub bridgeExecutor() != 0', executor !== '0x0000000000000000000000000000000000000000', executor, { level: 'error', code: 'HUB_EXECUTOR_UNSET', message: 'hub executor is not set', hint: 'extraData cannot be executed on the hub.' });
    });
    if (q.kind === 'multihop') {
      await guard('hub pair(dest, NATIVE)', async () => {
        const p = await readContract<any>(ctx.rpc('one'), bridgeI, hub, 'getTokenPair', [q.details.destChainId, NATIVE_TOKEN]);
        const reg = String(p[0]).toLowerCase() === NATIVE_TOKEN;
        add('hub pair (dest, NATIVE) registered', reg, undefined, { level: 'error', code: 'HUB_PAIR_NOT_REGISTERED', message: `hub has no NATIVE pair to chain ${q.details.destChainId}` });
        if (reg) add('hub pair not paused', !p[3], String(p[3]), { level: 'error', code: 'HUB_PAIR_PAUSED', message: 'hub pair is paused' });
      });
      await guard('hub inner extraData length', async () => {
        // the HUB limit applies to the decoded INNER extraData (what the hub forwards in hop 2: 88 B X2 payload, 0 B without core), not to the outer payload
        const max = await maxExtraData(ctx, 'one', hub);
        const inner = decodeForward(q.details.extraData).inner;
        const len = BigInt((inner.length - 2) / 2);
        add('inner extraData length (hub)', max === 0n || len <= max, `${len}/${max}`, { level: 'error', code: 'EXTRADATA_TOO_LONG_HUB', message: `inner extraData ${len} B > hub max ${max} B` });
      });
    }
    if (executor && executor !== '0x0000000000000000000000000000000000000000') {
      const whitelist = async (label: string, target: string, selector: string) => {
        const wl = await readContract<boolean>(ctx.rpc('one'), executorI, executor, 'isWhitelistedTarget', [target]);
        const mc = await readContract<boolean>(ctx.rpc('one'), executorI, executor, 'isMethodCheckEnabled', [target]);
        const wm = mc ? await readContract<boolean>(ctx.rpc('one'), executorI, executor, 'isWhitelistedMethod', [target, selector]) : true;
        add(`executor whitelists ${label}`, wl && wm, `target=${wl} methodCheck=${mc} method=${wm}`, { level: 'error', code: 'EXECUTOR_NOT_WHITELISTED', message: `hub executor does not whitelist ${label}`, hint: 'BETargetNotWhitelisted / BEMethodNotWhitelisted - contact the operator; the transfer would fall back to a plain payout.' });
      };
      if (q.kind === 'multihop') await guard('executor whitelist (hub forward)', () => whitelist('hub + bridgeTokenForwarded', hub, FORWARD_SELECTOR));
      if (q.kind === 'x1') {
        const router = getAddress(chains.one.swap!.v3SwapRouter!);
        await guard('executor whitelist (V3 router)', () => whitelist('V3 SwapRouter + exactInputSingle', router, EXACT_INPUT_SINGLE_SELECTOR));
        await guard('x1 pool exists', async () => {
          const cfgPool = chains.one.swap!.x1Pool!;
          const factory = chains.one.swap!.v3Factory;
          if (factory) {
            const got = await readContract<string>(ctx.rpc('one'), v3FactoryI, getAddress(factory), 'getPool', [chains.one.tokens.USDTx!.address, chains.one.tokens.ONEUSD!.address, cfgPool.fee]);
            add('factory.getPool(USDTx, ONEUSD, fee) == configured pool', got.toLowerCase() === cfgPool.pool.toLowerCase(), got, { level: 'error', code: 'POOL_NOT_FOUND', message: `factory has no pool ${cfgPool.pool} for (USDTx, ONEUSD, fee ${cfgPool.fee})` });
          }
        });
        await guard('x1 pool liquidity', async () => {
          const pool = getAddress(chains.one.swap!.x1Pool!.pool);
          const liq = await readContract<bigint>(ctx.rpc('one'), poolI, pool, 'liquidity', []);
          add('USDTx/ONEUSD pool liquidity > 0', liq > 0n, String(liq), { level: 'error', code: 'POOL_NO_LIQUIDITY', message: 'the swap pool has no liquidity' });
        });
      }
    }
  }

  // 4b. the DESTINATION bridge (every bridge the route touches after the source): paused()==false on a chain-verified RPC. R1-R4, X2, X3 (bridge leg) and the final
  // destination of R5/R6. For X1 the destination IS the hub (HUB_PAUSED above). The destination PAIR check is deliberately not extended here (it needs a per-hop
  // (query chain, counterpart chainId, local token, expected remote token) table); only the pause is required. Any RPC error / timeout / chain mismatch is blocking.
  const destKey = q.details.destChainKey as ChainKey;
  if (q.kind !== 'x1' && destKey !== srcKey) {
    await guard('DEST_BRIDGE_PAUSED', async () => {
      await ctx.assertChain(destKey);
      const destBridge = getAddress(chains[destKey].bridge!);
      const p = await readContract<boolean>(ctx.rpc(destKey), bridgeI, destBridge, 'paused', []);
      add('destination bridge paused()', !p, `${destKey}: ${p}`, { level: 'error', code: 'DEST_BRIDGE_PAUSED', message: `the destination bridge on ${destKey} is paused`, hint: 'EnforcedPause on the destination: the source transfer would be locked/burned and then wait in the pending queue. Wait for the maintenance to end.' });
    });
  }

  // 5. X2 (HyperCore) checks on HyperEVM
  const core = q.details.hop1Kind === 'x2' || q.details.hop2Kind === 'x2';
  if (core) {
    await guard('hypercore', async () => {
      const he = chains.hyperevm;
      const token = getAddress(he.tokens.ONE!.address);
      await ctx.assertChain('hyperevm');
      const set = await readContract<boolean>(ctx.rpc('hyperevm'), hyperTokenI, token, 'isCoreTokenIndexSet', []);
      add('token.isCoreTokenIndexSet()', set, String(set), { level: 'error', code: 'CORE_NOT_LINKED', message: 'the HyperEVM token is not linked to HyperCore (isCoreTokenIndexSet=false)', hint: 'HyperMintableERC20CoreTokenIndexNotSet - X2 is unavailable until the link is set.' });
      const destBridge = getAddress(he.bridge!);
      const exec = await readContract<string>(ctx.rpc('hyperevm'), bridgeI, destBridge, 'bridgeExecutor', []);
      const wl = await readContract<boolean>(ctx.rpc('hyperevm'), executorI, exec, 'isWhitelistedTarget', [token]);
      const mc = await readContract<boolean>(ctx.rpc('hyperevm'), executorI, exec, 'isMethodCheckEnabled', [token]);
      const wm = mc ? await readContract<boolean>(ctx.rpc('hyperevm'), executorI, exec, 'isWhitelistedMethod', [token, CORE_SELECTOR]) : true;
      add('HyperEVM executor whitelists token (+ transferToCoreFor when method check is on)', wl && wm, `target=${wl} methodCheck=${mc} method=${wm}`, { level: 'error', code: 'EXECUTOR_NOT_WHITELISTED', message: 'HyperEVM executor does not whitelist the ONE token / transferToCoreFor', hint: 'The deposit would fall back to plain ONE on HyperEVM.' });
      const amt = q.v2 ?? q.value;
      const t = await readContract<bigint>(ctx.rpc('hyperevm'), hyperTokenI, token, 'coreTransferableAmount', [amt]);
      add('coreTransferableAmount(value) > 0', t > 0n, String(t), { level: 'error', code: 'CORE_AMOUNT_ZERO', message: 'amount is below 1 Core wei after flooring', hint: 'CoreAmountBelowOneCoreWei' });
    });
  }

  // 6. balance / allowance
  await guard('balance', async () => {
    if (q.approval) {
      const bal = await readContract<bigint>(ctx.rpc(srcKey), erc20I, q.approval.token, 'balanceOf', [q.sender]);
      add('token balance >= total', bal >= q.approval.amount, `${bal} / ${q.approval.amount}`, { level: 'error', code: 'INSUFFICIENT_BALANCE', message: `balance ${bal} < required ${q.approval.amount}`, hint: 'ERC20InsufficientBalance' });
      const allowance = await readContract<bigint>(ctx.rpc(srcKey), erc20I, q.approval.token, 'allowance', [q.sender, q.approval.spender]);
      const blockingAllowance = stage === 'post-approve';
      add('allowance >= total', allowance >= q.approval.amount, `${allowance} / ${q.approval.amount}`, blockingAllowance
        ? { level: 'error', code: 'ALLOWANCE_INSUFFICIENT', message: `allowance ${allowance} < ${q.approval.amount} after the approve step`, hint: `approve(spender=${q.approval.spender}, amount=${q.approval.amount})` }
        : { level: 'warn', code: 'APPROVE_NEEDED', message: `allowance ${allowance} < ${q.approval.amount}: send() will approve first`, hint: `approve(spender=${q.approval.spender}, amount=${q.approval.amount})` });
    } else {
      const bal = BigInt(await ctx.rpc(srcKey).send('eth_getBalance', [q.sender, 'latest']));
      add('native balance >= value', bal >= q.tx.value, `${bal} / ${q.tx.value}`, { level: 'error', code: 'INSUFFICIENT_BALANCE', message: `native balance ${bal} < required ${q.tx.value} (gas not included)` });
    }
  });

  return { ok: !issues.some((i) => i.level === 'error'), issues, checks };
}

/**
 * The BLOCKING subset used by send(): every `error` issue of preflight() at the given stage (warnings are ignored).
 * Throws PreflightError{issues, stage}; nothing further is signed or broadcast.
 */
export async function blockingPreflight(ctx: Ctx, q: Quote, stage: PreflightStage): Promise<void> {
  const rep = await preflight(ctx, q, stage);
  const blocking = rep.issues.filter((i) => i.level === 'error');
  if (blocking.length) throw new PreflightError(blocking, stage);
}
