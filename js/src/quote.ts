import { Interface, getAddress, hexlify, randomBytes } from 'ethers';
import {
  bridgeAbi,
  erc20Abi,
  quoterV2Abi,
  swapBridgeRouterAbi,
  swapBridgeRouterCLAbi,
  verifierAbi,
  v3PoolAbi,
  hyperTokenAbi,
  clQuoterV2Abi,
} from './abi/index.js';
import type { Ctx } from './ctx.js';
import {
  checkPoolTickSpacing,
  encodeBridgeToken,
  encodeCoreExtraData,
  encodeForwardExtraData,
  encodeSwapBridge,
  encodeX1ExtraData,
  hashExtraData,
} from './encoding.js';
import { snapshot } from './freeze.js';
import { sealQuote } from './seal.js';
import { resolveRecipients, specFor } from './spec.js';
export { specFor } from './spec.js';
export type { Spec } from './spec.js';
import { InvalidOptionError, LimitViolationError, PreflightError, RequoteRequiredError } from './errors.js';
import { coreAmounts, floorBps, multihopAmounts } from './math.js';
import { NATIVE_TOKEN, configValue, requireConfigured, chainOf, dexOf, dexPath, swapView } from './networks.js';
import type { ChainKey, DexKind } from './networks-types.js';
import { readContract } from './rpc.js';
import { Route } from './types.js';
import type { FeeTriple, HopKind, Limits, MatchSpec, Quote, QuoteRequest } from './types.js';

export const bridgeI = new Interface(bridgeAbi as any);
export const verifierI = new Interface(verifierAbi as any);
export const erc20I = new Interface(erc20Abi as any);
export const quoterI = new Interface(quoterV2Abi as any);
export const sbrI = new Interface(swapBridgeRouterAbi as any);
/** SwapBridgeRouterCL (int24 tickSpacing); its SwapBridge event and fee helpers are identical to sbrI */
export const sbrCLI = new Interface(swapBridgeRouterCLAbi as any);
export const clQuoterI = new Interface(clQuoterV2Abi as any);
export const poolI = new Interface(v3PoolAbi as any);
export const hyperTokenI = new Interface(hyperTokenAbi as any);

export const QUOTE_STATUS = ['Invalid', 'Success', 'NoPair', 'InsufficientForFee', 'InsufficientValue', 'InvalidSwap'];

export interface Frozen {
  oldQuote: Quote;
  limits: Limits;
  deadline?: number;
  amountOutMinimum?: bigint;
  minBridgeValue?: bigint;
}

/** slippageBps in [0, 10000) (undefined = 50); deadlineSec a positive integer (undefined = 1800). 0 bps is a valid explicit value. */
function swapOptions(req: QuoteRequest): { slippageBps: number; deadlineSec: number } {
  const bps = req.swap?.slippageBps ?? 50;
  const dl = req.swap?.deadlineSec ?? 1800;
  if (!Number.isInteger(bps) || bps < 0 || bps >= 10_000) throw new InvalidOptionError(`swap.slippageBps must be an integer in [0, 10000), got ${bps}`);
  if (!Number.isInteger(dl) || dl <= 0) throw new InvalidOptionError(`swap.deadlineSec must be a positive integer (a deadline of "now" always fails), got ${dl}`);
  return { slippageBps: bps, deadlineSec: dl };
}

const rand = () => hexlify(randomBytes(8));

/** The X3 pool key against the network's dex: poolFee for 'v3' (tickSpacing must be absent); tickSpacing (int24, > 0) with poolFee 0 for 'cl'. */
function checkX3Pool(dex: DexKind, x3: NonNullable<QuoteRequest['x3']>): void {
  if (dex === 'cl') {
    if (x3.tickSpacing === undefined) throw new InvalidOptionError('x3.tickSpacing is required on a network whose dex is "cl" (Slipstream pools are keyed by tick spacing, not fee)');
    try { checkPoolTickSpacing(x3.tickSpacing); } catch (e) { throw new InvalidOptionError(`x3.tickSpacing: ${(e as Error).message}`); }
    if (x3.poolFee !== 0) throw new InvalidOptionError('x3.poolFee must be 0 on a network whose dex is "cl" (set x3.tickSpacing)');
    return;
  }
  if (x3.tickSpacing !== undefined) throw new InvalidOptionError(`x3.tickSpacing is only valid on a network whose dex is "cl" (this network uses "${dex}": set x3.poolFee)`);
}

async function readFee(ctx: Ctx, key: ChainKey, verifier: string, toChainId: number, token: string, value: bigint): Promise<FeeTriple> {
  const r = await readContract<any>(ctx.rpc(key), verifierI, verifier, 'calculateFee', [toChainId, token, value]);
  return { minimumValue: r[0], networkFee: r[1], exFee: r[2] };
}

async function readPair(ctx: Ctx, key: ChainKey, bridge: string, toChainId: number, token: string) {
  const p = await readContract<any>(ctx.rpc(key), bridgeI, bridge, 'getTokenPair', [toChainId, token]);
  return { localToken: String(p[0]), remoteToken: String(p[1]), isOrigin: Boolean(p[2]), paused: Boolean(p[3]), minted: BigInt(p[6]) };
}

export async function maxExtraData(ctx: Ctx, key: ChainKey, bridge: string): Promise<bigint> {
  return readContract<bigint>(ctx.rpc(key), bridgeI, bridge, 'maxExtraDataLength', []);
}

export async function buildQuote(ctx: Ctx, request: QuoteRequest, frozen?: Frozen): Promise<Quote> {
  // the request is snapshotted (structuredClone) so later mutation of the caller's object can never change the quote or a requote
  const req: QuoteRequest = { ...structuredClone(request), sender: getAddress(request.sender), recipient: getAddress(request.recipient) };
  if ((req.x3 as { extraData?: string } | undefined)?.extraData !== undefined) throw new InvalidOptionError('x3.extraData is not supported (X3 supports extraData = 0x only)');
  const route = req.route;
  const spec = specFor(route, req);
  // the swap routes follow the network's dex: their required config paths are those of the active DEX (one.swap.* for V3, one.swap.cl.* for CL)
  const dex: DexKind = spec.kind === 'x1' || spec.kind === 'x3' ? dexOf(ctx.net) : 'v3';
  requireConfigured(route, ctx.net, spec.required.map((p) => dexPath(dex, p)));
  if (spec.kind === 'x3') checkX3Pool(dex, req.x3!);
  if (req.amount <= 0n) throw new RangeError('amount must be > 0');
  const chains = ctx.net.chains;
  const src = chainOf(ctx.net, spec.src);
  const ttlMs = req.quoteTtlMs ?? ctx.opts.quoteTtlMs ?? 30_000;
  const rec = resolveRecipients(req);
  const violated: string[] = [];

  await ctx.assertChain(spec.src);
  if (spec.hub && spec.hub !== spec.src) await ctx.assertChain(spec.hub);

  // ---------------------------------------------------------------------------------------- X3
  if (spec.kind === 'x3') {
    const x3 = req.x3!;
    const router = getAddress(swapView(ctx.net).sbr!);
    const cl = dex === 'cl';
    const routerI = cl ? sbrCLI : sbrI;
    const poolKey = cl ? x3.tickSpacing! : x3.poolFee;
    const toChainId = chains[spec.dest].chainId;
    const native = x3.tokenIn === 'native';
    const tokenIn = native ? getAddress(chains.one.tokens.WCROSS!.address) : getAddress(x3.tokenIn);
    const tokenOut = getAddress(x3.tokenOut);
    const { slippageBps, deadlineSec } = swapOptions(req);
    const deadline = frozen?.deadline ?? Math.floor(ctx.now() / 1000) + deadlineSec;
    const extraData = '0x'; // X3 supports extraData = 0x only

    const r: any = await readContract(ctx.rpc('one'), routerI, router, 'getAmountSwapBridgeOut', [toChainId, tokenIn, tokenOut, poolKey, req.amount], { from: req.sender });
    const status = Number(r[0]);
    if (status !== 1) throw new PreflightError([{ level: 'error', code: `X3_QUOTE_${QUOTE_STATUS[status] ?? status}`, message: `SwapBridgeRouter quote status ${QUOTE_STATUS[status] ?? status}`, hint: 'NoPair: tokenOut has no hub pair to the destination; InsufficientForFee/InsufficientValue: amount too small; InvalidSwap: no pool / liquidity.' }]);
    // the EXPECTED destination token: the hub pair's remoteToken (read on the chain-verified hub RPC); a missing pair fails the quote
    const xpair = await readPair(ctx, 'one', getAddress(chains.one.bridge!), toChainId, tokenOut);
    if (xpair.localToken.toLowerCase() !== tokenOut.toLowerCase()) {
      throw new PreflightError([{ level: 'error', code: 'PAIR_NOT_REGISTERED', message: `tokenOut ${tokenOut} has no hub pair to chain ${toChainId}`, hint: 'The token pair is not registered (RegistryNotExistToken).' }]);
    }
    const swapAmountOut: bigint = r[1];
    const expectedBridge: bigint = r[2];
    const hop1 = { minimumValue: 0n, networkFee: BigInt(r[3]), exFee: BigInt(r[4]) };

    const amountOutMinimum = frozen?.amountOutMinimum ?? floorBps(swapAmountOut, slippageBps);
    const eb: any = await readContract(ctx.rpc('one'), routerI, router, 'getExpectedBridgeAmount', [toChainId, tokenOut, amountOutMinimum]);
    const ebStatus = Number(eb[0]);
    const deliveryAtFloor: bigint = eb[1];
    if (frozen) {
      if (swapAmountOut < amountOutMinimum) violated.push('swapOutputBelowFloor');
      if (ebStatus !== 1 || deliveryAtFloor < (frozen.minBridgeValue ?? 0n)) violated.push('bridgeDeliveryFloor');
    } else if (ebStatus !== 1) {
      throw new PreflightError([{ level: 'error', code: `X3_FLOOR_${QUOTE_STATUS[ebStatus] ?? ebStatus}`, message: `the delivery floor cannot be bridged (status ${QUOTE_STATUS[ebStatus] ?? ebStatus}); lower slippage or raise the amount` }]);
    }
    const minBridgeValue = frozen?.minBridgeValue ?? deliveryAtFloor;
    const data = encodeSwapBridge({ tokenIn, tokenOut, fee: x3.poolFee, amountIn: req.amount, amountOutMinimum, toChainId, recipient: rec.dest, extraData, deadline, native, ...(cl ? { dex: 'cl' as const, tickSpacing: x3.tickSpacing! } : {}) });
    const match: MatchSpec = { kind: 'swap', tokenIn, tokenOut, amountIn: req.amount, toChainID: toChainId, toToken: getAddress(xpair.remoteToken), recipient: rec.dest, extraDataHash: hashExtraData(extraData) };
    const limits: Limits = frozen?.limits ?? {
      maxTotalSpend: req.limits?.maxTotalSpend ?? req.amount,
      minDelivered: req.limits?.minDelivered ?? minBridgeValue,
      amountOutMinimum,
      minBridgeValue,
    };
    const quote: Omit<Quote, 'seal'> = {
      id: rand(), network: ctx.network, route, request: req, kind: 'x3', sourceChainKey: 'one', sourceChainId: chains.one.chainId,
      sourceContract: router, sourceBridge: getAddress(chains.one.bridge!), sender: req.sender, createdAt: ctx.now(), ttlMs,
      verifiers: {}, fees: { hop1 }, value: expectedBridge, totalSpend: req.amount,
      tx: { to: router, data, value: native ? req.amount : 0n },
      approval: native ? null : { token: tokenIn, spender: router, amount: req.amount },
      limits, deadline, match,
      details: { hop1ToChainId: toChainId, hop1ToToken: tokenOut, destChainKey: spec.dest, destChainId: toChainId, hop1Kind: 'plain', extraData, recipients: { dest: rec.dest }, swapAmountOut },
    };
    if (req.amount > limits.maxTotalSpend) violated.push('maxTotalSpend');
    if (minBridgeValue < limits.minDelivered) violated.push('minDelivered');
    return finish(quote, violated, frozen);
  }

  // ---------------------------------------------------------------------------------------- bridge routes
  const srcBridge = getAddress(src.bridge!);
  const native = spec.srcToken === 'native';
  const srcTokenAddr = native ? NATIVE_TOKEN : getAddress(src.tokens[spec.srcToken]!.address);
  const hubKey: ChainKey | undefined = spec.hub;
  const toChainId = spec.kind === 'multihop' || spec.kind === 'x1' ? chains.one.chainId : chains[spec.dest].chainId;
  const srcVerifier = await readContract<string>(ctx.rpc(spec.src), bridgeI, srcBridge, 'bridgeVerifier', []);

  const pair = await readPair(ctx, spec.src, srcBridge, toChainId, srcTokenAddr);
  if (pair.localToken.toLowerCase() !== srcTokenAddr.toLowerCase()) {
    throw new PreflightError([{ level: 'error', code: 'PAIR_NOT_REGISTERED', message: `token ${srcTokenAddr} has no registered pair to chain ${toChainId} on ${spec.src}`, hint: 'The token pair is not registered (RegistryNotExistToken).' }]);
  }

  let value = req.amount; // hop-1 value
  let extraData = '0x';
  let hop2: FeeTriple | undefined;
  let ef2Declared: bigint | undefined;
  let v2: bigint | undefined;
  let hubVerifier: string | undefined;
  let hop1Kind: HopKind = 'plain';
  let hop2Kind: HopKind | undefined;
  let deadline: number | undefined;
  let amountOutMinimum: bigint | undefined;
  let expectedSwapOut: bigint | undefined;
  let coreAmount: bigint | undefined;
  let coreDust: bigint | undefined;
  let destToChainId = chains[spec.dest].chainId;
  const destTokenOne = chains.hyperevm.tokens.ONE?.address; // only used for X2

  if (spec.kind === 'single' && spec.core) {
    // X2 on R3: ONE -> HyperEVM, HyperCore deposit as extraData
    const heTok = getAddress(destTokenOne!);
    extraData = encodeCoreExtraData({ token: heTok, coreRecipient: rec.core, value });
    hop1Kind = 'x2';
    try {
      await ctx.assertChain('hyperevm');
      const unit = await readContract<bigint>(ctx.rpc('hyperevm'), hyperTokenI, heTok, 'coreUnit', []);
      ({ coreAmount, dust: coreDust } = coreAmounts(value, unit));
    } catch { /* reported by preflight */ }
  } else if (spec.kind === 'multihop') {
    const hub = getAddress(chains.one.bridge!);
    hubVerifier = await readContract<string>(ctx.rpc('one'), bridgeI, hub, 'bridgeVerifier', []);
    const hubFee = await readFee(ctx, 'one', hubVerifier, destToChainId, NATIVE_TOKEN, req.amount);
    v2 = req.amount;
    if (v2 < hubFee.minimumValue) throw new PreflightError([{ level: 'error', code: 'BELOW_MINIMUM_HOP2', message: `amount ${v2} is below the hub minimumValue ${hubFee.minimumValue} for chain ${destToChainId}` }]);
    hop2 = hubFee;
    const buf = req.hop2FeeBuffer ?? 0n;
    const m = multihopAmounts(v2, hubFee, buf);
    ef2Declared = m.ef2Declared;
    value = m.v1;
    let inner = '0x';
    hop2Kind = 'plain';
    if (spec.core) {
      const heTok = getAddress(destTokenOne!);
      inner = encodeCoreExtraData({ token: heTok, coreRecipient: rec.core, value: v2 });
      hop2Kind = 'x2';
      try {
        await ctx.assertChain('hyperevm');
        const unit = await readContract<bigint>(ctx.rpc('hyperevm'), hyperTokenI, heTok, 'coreUnit', []);
        ({ coreAmount, dust: coreDust } = coreAmounts(v2, unit));
      } catch { /* reported by preflight */ }
    }
    extraData = encodeForwardExtraData({ hub, destChainId: destToChainId, toDest: rec.dest, value2: v2, networkFee2: hubFee.networkFee, exFee2Declared: m.ef2Declared, inner });
    hop1Kind = 'hubForward';
  } else if (spec.kind === 'x1') {
    const oneChain = chains.one;
    const sv = swapView(ctx.net);
    const router = getAddress(sv.router!);
    if (sv.dex === 'cl') {
      try { checkPoolTickSpacing(sv.tickSpacing); } catch (e) { throw new RangeError(`one.swap.cl.x1Pool: ${(e as Error).message}`); }
    }
    const { slippageBps, deadlineSec } = swapOptions(req);
    deadline = frozen?.deadline ?? Math.floor(ctx.now() / 1000) + deadlineSec;
    const usdtx = getAddress(oneChain.tokens.USDTx!.address);
    const oneusd = getAddress(oneChain.tokens.ONEUSD!.address);
    await ctx.assertChain('one');
    const q = sv.dex === 'cl'
      ? await readContract<any>(ctx.rpc('one'), clQuoterI, getAddress(sv.quoter!), 'quoteExactInputSingle', [[usdtx, oneusd, value, sv.tickSpacing, 0]])
      : await readContract<any>(ctx.rpc('one'), quoterI, getAddress(sv.quoter!), 'quoteExactInputSingle', [[usdtx, oneusd, value, sv.fee, 0]]);
    expectedSwapOut = BigInt(q[0]);
    amountOutMinimum = frozen?.amountOutMinimum ?? floorBps(expectedSwapOut, slippageBps);
    if (frozen && expectedSwapOut < amountOutMinimum) violated.push('swapOutputBelowFloor');
    extraData = encodeX1ExtraData({ router, tokenIn: usdtx, tokenOut: oneusd, fee: sv.fee, recipient: rec.dest, deadline, amountIn: value, amountOutMinimum, ...(sv.dex === 'cl' ? { dex: 'cl' as const, tickSpacing: sv.tickSpacing } : {}) });
    hop1Kind = 'x1';
    destToChainId = chains.one.chainId;
  }

  const fee1 = await readFee(ctx, spec.src, srcVerifier, toChainId, srcTokenAddr, value);
  if (value < fee1.minimumValue) {
    throw new PreflightError([{ level: 'error', code: 'BELOW_MINIMUM', message: `value ${value} is below minimumValue ${fee1.minimumValue} of the source verifier`, hint: 'Quote live: minimums move with the price feed.' }]);
  }
  const totalSpend = value + fee1.networkFee + fee1.exFee;
  const hop1To = spec.kind === 'multihop' ? rec.onHub : rec.dest;
  const data = encodeBridgeToken({ toChainId, fromToken: srcTokenAddr, to: hop1To, value, networkFee: fee1.networkFee, exFee: fee1.exFee, extraData });
  const minDelivered = spec.kind === 'multihop' ? v2! : value;
  const limits: Limits =
    frozen?.limits ??
    ({
      maxTotalSpend: req.limits?.maxTotalSpend ?? totalSpend,
      minDelivered: req.limits?.minDelivered ?? minDelivered,
      ...(amountOutMinimum !== undefined ? { amountOutMinimum } : {}),
    } as Limits);
  if (totalSpend > limits.maxTotalSpend) violated.push('maxTotalSpend');
  if (minDelivered < limits.minDelivered) violated.push('minDelivered');

  const match: MatchSpec = { kind: 'bridge', toChainID: toChainId, fromToken: srcTokenAddr, toToken: getAddress(pair.remoteToken), recipient: hop1To, value, networkFee: fee1.networkFee, exFee: fee1.exFee, extraDataHash: hashExtraData(extraData) };
  const quote: Omit<Quote, 'seal'> = {
    id: rand(), network: ctx.network, route, request: req, kind: spec.kind, sourceChainKey: spec.src, sourceChainId: src.chainId,
    sourceContract: srcBridge, sourceBridge: srcBridge, sender: req.sender, createdAt: ctx.now(), ttlMs,
    verifiers: { source: srcVerifier, ...(hubVerifier ? { hub: hubVerifier } : {}) },
    fees: { hop1: fee1, ...(hop2 ? { hop2, hop2ExFeeDeclared: ef2Declared } : {}) },
    value, ...(v2 !== undefined ? { v2 } : {}), totalSpend,
    tx: { to: srcBridge, data, value: native ? totalSpend : 0n },
    approval: native ? null : { token: srcTokenAddr, spender: srcBridge, amount: totalSpend },
    limits, ...(deadline !== undefined ? { deadline } : {}), match,
    details: {
      hop1ToChainId: toChainId, hop1ToToken: getAddress(pair.remoteToken), ...(hubKey ? { hubChainKey: hubKey } : {}),
      destChainKey: spec.dest, destChainId: destToChainId, hop1Kind, ...(hop2Kind ? { hop2Kind } : {}), extraData,
      recipients: { onHub: rec.onHub, dest: rec.dest, core: rec.core },
      ...(expectedSwapOut !== undefined ? { expectedSwapOut } : {}),
      ...(coreAmount !== undefined ? { coreAmount, coreDust } : {}),
    },
  };
  return finish(quote, violated, frozen);
}

function finish(quote0: Omit<Quote, 'seal'>, violated: string[], frozen?: Frozen): Quote {
  const quote = snapshot(sealQuote(quote0)); // sealed: the canonical payload of the WHOLE quote + keccak256 digest
  if (violated.length) {
    if (frozen) throw new RequoteRequiredError(frozen.oldQuote, quote, violated);
    throw new LimitViolationError(violated, quote);
  }
  return quote;
}

export function frozenFrom(q: Quote): Frozen {
  return { oldQuote: q, limits: q.limits, deadline: q.deadline, amountOutMinimum: q.limits.amountOutMinimum, minBridgeValue: q.limits.minBridgeValue };
}

export { configValue };
