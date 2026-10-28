import { getAddress } from 'ethers';
import type { Ctx } from './ctx.js';
import { CORE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR, FORWARD_SELECTOR, v3RouterIface } from './encoding.js';
import { LimitViolationError, NetworkMismatchError } from './errors.js';
import { decodeForward } from './handle.js';
import { NATIVE_TOKEN } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { bridgeI, sbrI, specFor } from './quote.js';
import type { Quote } from './types.js';

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/**
 * Client binding check: config equality of a quote against the CLIENT's own networks config (`ctx.net`).
 * Quote -> Client binding (runs FIRST in send(), before signer checks and before any approve; a requote re-derives from THIS client's
 * config, so it could otherwise silently re-target another network).
 *  - quote.network / sourceChainId / sourceChainKey must equal this client's config;
 *  - every CONFIG-OWNED address embedded in the quote (source bridge / router, hub, fixed route tokens, hop and executor targets, approve
 *    spender) must equal this client's config: mismatch => NetworkMismatchError;
 *  - USER-SUPPLIED values (X3 tokens, recipients, amount) are not checked against config; they must equal the frozen request snapshot and the
 *    built calldata must encode exactly those values: mismatch => LimitViolationError (internal tampering). Calldata is never re-targeted.
 */
export function assertQuoteBelongs(ctx: Ctx, q: Quote): void {
  const cfgMismatch = (field: string, client: string | number, quote: string | number): never => {
    throw new NetworkMismatchError(`quote.${field}`, client, quote);
  };
  const tamper = (what: string): never => {
    throw new LimitViolationError([`tampered:${what}`], q);
  };
  /**
   * The ONE way the binding check reads a config-owned value: a dotted path into THIS client's networks config (`ctx.net`). A missing component raises
   * NetworkMismatchError naming the path (expected: null = this client does not have it, actual: what the quote relies on), never a TypeError:
   * a quote that relies on a component the client does not have does not belong to that client. No non-null assertion on config anywhere in this file.
   */
  const requireCfg = <T extends string | number>(path: string, quoteValue: string | number): T => {
    let cur: unknown = ctx.net;
    for (const seg of path.split('.')) {
      if (cur === null || cur === undefined || typeof cur !== 'object') { cur = undefined; break; }
      cur = (cur as Record<string, unknown>)[seg];
    }
    if (cur === null || cur === undefined || cur === '') throw new NetworkMismatchError(path, 'null (not configured by this client)', quoteValue);
    return cur as T;
  };
  if (q.network !== ctx.network) cfgMismatch('network', ctx.network, q.network);
  const srcKey = q.sourceChainKey as ChainKey;
  const srcChainId = requireCfg<number>(`chains.${srcKey}.chainId`, q.sourceChainId);
  if (q.sourceChainId !== srcChainId) cfgMismatch('sourceChainId', srcChainId, q.sourceChainId);
  const spec = specFor(q.route, q.request);
  if (spec.src !== q.sourceChainKey) cfgMismatch('sourceChainKey', spec.src, q.sourceChainKey);
  if (spec.kind !== q.kind) tamper('kind');
  const wantBridge = requireCfg<string>(`chains.${srcKey}.bridge`, q.sourceBridge);
  const wantContract = q.kind === 'x3' ? requireCfg<string>('chains.one.swap.swapBridgeRouter', q.sourceContract) : wantBridge;
  if (!same(q.sourceBridge, wantBridge)) cfgMismatch('sourceBridge', wantBridge, q.sourceBridge);
  if (!same(q.sourceContract, wantContract)) cfgMismatch('sourceContract', wantContract, q.sourceContract);
  if (!same(q.tx.to, wantContract)) cfgMismatch('tx.to', wantContract, q.tx.to);
  if (q.approval && !same(q.approval.spender, wantContract)) cfgMismatch('approval.spender', wantContract, q.approval.spender);

  const req = q.request;
  const recDest = getAddress(req.recipients?.dest ?? req.recipient);
  const recHub = getAddress(req.recipients?.onHub ?? req.recipient);
  const destChainId = requireCfg<number>(`chains.${spec.dest}.chainId`, q.details.destChainId);
  const hubId = requireCfg<number>('chains.one.chainId', q.details.destChainId);
  // the chain ids the payload carries must be the CLIENT's chain ids for this route (structural validation only checks them against the calldata)
  if (q.details.destChainId !== (spec.kind === 'x1' ? hubId : destChainId)) cfgMismatch('details.destChainId', spec.kind === 'x1' ? hubId : destChainId, q.details.destChainId);

  if (q.kind === 'x3') {
    const x3 = req.x3;
    if (!x3) return tamper('x3 request');
    const native = x3.tokenIn === 'native';
    let d;
    try {
      d = sbrI.parseTransaction({ data: q.tx.data, value: q.tx.value });
    } catch { return tamper('calldata is not a SwapBridgeRouter call'); }
    if (!d || d.name !== (native ? 'swapBridgeExactInputSingleETH' : 'swapBridgeExactInputSingle')) return tamper('swap function');
    const p = d.args[0];
    if (native) { const wcross = requireCfg<string>('chains.one.tokens.WCROSS.address', p[0]); if (!same(p[0], wcross)) cfgMismatch('tokenIn(WCROSS)', wcross, p[0]); }
    if (!native && !same(p[0], x3.tokenIn)) return tamper('tokenIn');
    if (!same(p[1], x3.tokenOut)) return tamper('tokenOut');
    if (Number(p[2]) !== x3.poolFee) return tamper('poolFee');
    if (p[3] !== req.amount) return tamper('amountIn');
    if (Number(p[6][0]) !== destChainId) cfgMismatch('toChainID', destChainId, Number(p[6][0]));
    if (!same(p[6][1], recDest)) return tamper('recipient');
    if (p[6][2] !== '0x') return tamper('extraData');
    if (q.approval && (!same(q.approval.token, x3.tokenIn) || q.approval.amount !== req.amount)) return tamper('approval');
    if (native && q.tx.value !== req.amount) return tamper('msg.value');
    return;
  }

  // bridge routes: bridgeToken(toChainId, fromToken, to, value, nf, ef, extraData)
  let args;
  try { args = bridgeI.decodeFunctionData('bridgeToken', q.tx.data); } catch { return tamper('calldata is not a bridgeToken call'); }
  const wantToken = spec.srcToken === 'native' ? NATIVE_TOKEN : requireCfg<string>(`chains.${srcKey}.tokens.${spec.srcToken}.address`, args[1]);
  if (!same(args[1], wantToken)) cfgMismatch('fromToken', wantToken, args[1]);
  if (q.approval && !same(q.approval.token, wantToken)) cfgMismatch('approval.token', wantToken, q.approval.token);
  const hop1ToChain = spec.kind === 'multihop' || spec.kind === 'x1' ? hubId : destChainId;
  if (Number(args[0]) !== hop1ToChain) cfgMismatch('toChainID', hop1ToChain, Number(args[0]));
  if (!same(args[2], spec.kind === 'multihop' ? recHub : recDest)) return tamper('recipient');
  if (args[3] !== q.value) return tamper('value');
  const ed: string = args[6];
  const target = ed.length >= 42 ? getAddress('0x' + ed.slice(2, 42)) : '';
  const sel = ed.length >= 50 ? ('0x' + ed.slice(42, 50)).toLowerCase() : '';
  if (spec.kind === 'multihop') {
    const hub = requireCfg<string>('chains.one.bridge', target || '(empty)');
    if (!same(target, hub) || sel !== FORWARD_SELECTOR) return cfgMismatch('extraData.target(hub bridge)', hub, target || '(empty)');
    const f = decodeForward(ed);
    if (Number(f.toChainID) !== destChainId) cfgMismatch('hop2 toChainID', destChainId, Number(f.toChainID));
    if (!same(f.to, recDest)) return tamper('hop2 recipient');
    if (f.inner !== '0x') {
      const innerTarget = '0x' + f.inner.slice(2, 42);
      const he = requireCfg<string>('chains.hyperevm.tokens.ONE.address', innerTarget);
      if (!same(innerTarget, he) || ('0x' + f.inner.slice(42, 50)).toLowerCase() !== CORE_SELECTOR) cfgMismatch('extraData.inner.target(HyperEVM ONE)', he, innerTarget);
    }
  } else if (spec.kind === 'x1') {
    const v3 = requireCfg<string>('chains.one.swap.v3SwapRouter', target || '(empty)');
    if (!same(target, v3) || sel !== EXACT_INPUT_SINGLE_SELECTOR) return cfgMismatch('extraData.target(V3 router)', v3, target || '(empty)');
    const sw = v3RouterIface.decodeFunctionData('exactInputSingle', '0x' + ed.slice(42))[0];
    const usdtx = requireCfg<string>('chains.one.tokens.USDTx.address', sw[0]);
    if (!same(sw[0], usdtx)) cfgMismatch('x1.tokenIn(USDTx)', usdtx, sw[0]);
    if (!same(q.details.hop1ToToken, usdtx)) cfgMismatch('details.hop1ToToken(USDTx)', usdtx, q.details.hop1ToToken);
    const oneusd = requireCfg<string>('chains.one.tokens.ONEUSD.address', sw[1]);
    if (!same(sw[1], oneusd)) cfgMismatch('x1.tokenOut(ONEUSD)', oneusd, sw[1]);
    const poolFee = requireCfg<number>('chains.one.swap.x1Pool.fee', Number(sw[2]));
    if (Number(sw[2]) !== poolFee) cfgMismatch('x1.fee', poolFee, Number(sw[2]));
    if (!same(sw[3], recDest)) return tamper('x1 swap recipient');
  } else if (spec.core) {
    const he = requireCfg<string>('chains.hyperevm.tokens.ONE.address', target || '(empty)');
    if (!same(target, he) || sel !== CORE_SELECTOR) return cfgMismatch('extraData.target(HyperEVM ONE)', he, target || '(empty)');
  } else if (ed !== '0x') {
    return tamper('extraData');
  }
}
