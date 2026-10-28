import { getAddress } from 'ethers';
import type { Ctx } from './ctx.js';
import { checkX1, checkX2 } from './extra.js';
import { CORE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR, FORWARD_SELECTOR } from './encoding.js';
import { AmbiguousSourceError, RouteMismatchError, SourceTransferNotFoundError, UnsupportedRouteError, type SourceCandidateInfo } from './errors.js';
import { decodeForward, linkSwapTransfer, parseInitiated, TOPIC_BRIDGE_INITIATED, type ParsedInitiated, type RawReceipt } from './handle.js';
import { chainKeyById } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { sbrI } from './quote.js';
import { Route } from './types.js';

export interface InferOptions {
  /** optional: verified against the route decoded from the receipt (contradiction => RouteMismatchError) */
  route?: Route;
  /** select the candidate whose BridgeInitiated `index` equals this */
  index?: bigint | number;
  /** select the candidate by the log index of its BridgeInitiated */
  logIndex?: number;
}

export interface Inferred {
  route: Route;
  hypercore: boolean;
  x3ToChain?: 'bsc' | 'hyperevm';
  /** the selected BridgeInitiated (X3: the bridge log paired with the router's SwapBridge) */
  logIndex: number;
  event: ParsedInitiated;
}

interface Candidate { x3: boolean; event: ParsedInitiated }

const sameAddr = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/**
 * Deterministic route inference from the SOURCE receipt only (never from absent events).
 * Trust rule: only logs emitted by the configured source bridge (and, for X3, the configured SwapBridgeRouter) of `srcKey` are
 * candidates - a log with identical topics from any other address is ignored. X3 additionally needs the router's SwapBridge log and
 * the bridge's BridgeInitiated (from = router) in the same receipt, consistent with each other.
 */
export function inferFromReceipt(ctx: Ctx, srcKey: ChainKey, receipt: RawReceipt, opts: InferOptions = {}): Inferred {
  const net = ctx.net;
  const src = net.chains[srcKey];
  if (!src.bridge) throw new UnsupportedRouteError(`the source bridge of ${srcKey} is not configured`);
  const srcBridge = getAddress(src.bridge);
  const routerCfg = srcKey === 'cross' ? net.chains.cross.swap?.swapBridgeRouter : null;
  const router = routerCfg ? getAddress(routerCfg) : null;

  const bridgeLogs: ParsedInitiated[] = [];
  for (const l of receipt.logs) {
    if (sameAddr(l.address, srcBridge) && l.topics[0]?.toLowerCase() === TOPIC_BRIDGE_INITIATED.toLowerCase()) {
      const p = parseInitiated({ ...l, logIndex: l.logIndex });
      if (p) bridgeLogs.push(p);
    }
  }

  // X3 candidates come from the structural, intent-free link (linkSwapTransfer); handleFromTx has NO intent, so nothing is compared with a quote
  const cands: Candidate[] = [];
  const paired = new Set<number>();
  for (const s of linkSwapTransfer(receipt, { router, bridge: srcBridge }).linked) { paired.add(s.bi.logIndex); cands.push({ x3: true, event: s.bi }); }
  for (const e of bridgeLogs) {
    if (paired.has(e.logIndex)) continue;
    if (router && sameAddr(e.from, router)) continue; // router-origin transfer without a consistent SwapBridge: not a candidate
    cands.push({ x3: false, event: e });
  }
  cands.sort((a, b) => a.event.logIndex - b.event.logIndex);
  if (cands.length === 0) throw new SourceTransferNotFoundError(`no BridgeInitiated emitted by the configured source bridge ${srcBridge} (${srcKey}) with a consistent X3 pairing in the receipt`);

  const info = (c: Candidate): SourceCandidateInfo => ({ toChainID: c.event.toChainID, index: c.event.index, logIndex: c.event.logIndex });
  let pool = cands;
  if (opts.index !== undefined) pool = pool.filter((c) => c.event.index === BigInt(opts.index!));
  if (opts.logIndex !== undefined) pool = pool.filter((c) => c.event.logIndex === opts.logIndex);
  if (pool.length === 0) throw new SourceTransferNotFoundError(`the selector (index ${opts.index ?? '-'}, logIndex ${opts.logIndex ?? '-'}) matches none of the candidates: ${cands.map((c) => `(${c.event.toChainID}, ${c.event.index}, ${c.event.logIndex})`).join(' ')}`);
  if (pool.length > 1) throw new AmbiguousSourceError('several candidate BridgeInitiated events in the receipt; refusing to pick one. Pass opts.index or opts.logIndex.', pool.map(info));
  const chosen = pool[0];
  const e = chosen.event;
  const destKey = chainKeyById(net, Number(e.toChainID));

  const unsupported = (why: string, target?: string, selector?: string) => new UnsupportedRouteError(`${why}${target ? ` (target ${target}, selector ${selector})` : ''}`, target, selector);
  let route: Route;
  let hypercore = false;
  let x3ToChain: 'bsc' | 'hyperevm' | undefined;

  if (chosen.x3) {
    if (destKey !== 'bsc' && destKey !== 'hyperevm') throw unsupported(`X3 destination chain ${e.toChainID} is not a configured route destination`);
    if (e.extraData !== '0x' && e.extraData.length > 2) throw unsupported('X3 with non-empty extraData is not supported by this SDK');
    route = Route.SWAP_BRIDGE;
    x3ToChain = destKey;
  } else if (e.extraData === '0x' || e.extraData.length === 2) {
    const plain: Record<string, Route> = { 'bsc>cross': Route.BSC_TO_ONE, 'cross>bsc': Route.ONE_TO_BSC, 'cross>hyperevm': Route.ONE_TO_HYPEREVM, 'hyperevm>cross': Route.HYPEREVM_TO_ONE };
    const r = destKey ? plain[`${srcKey}>${destKey}`] : undefined;
    if (!r) throw unsupported(`no plain route from ${srcKey} to chain ${e.toChainID}`);
    route = r;
  } else {
    if (e.extraData.length < 2 + 48) throw unsupported('extraData is shorter than target(20 B) + selector(4 B)');
    const target = getAddress('0x' + e.extraData.slice(2, 42));
    const selector = ('0x' + e.extraData.slice(42, 50)).toLowerCase();
    const hub = net.chains.cross.bridge ? getAddress(net.chains.cross.bridge) : null;
    const v3 = net.chains.cross.swap?.v3SwapRouter ? getAddress(net.chains.cross.swap.v3SwapRouter) : null;
    const heTok = net.chains.hyperevm.tokens.ONE ? getAddress(net.chains.hyperevm.tokens.ONE.address) : null;
    if (hub && sameAddr(target, hub) && selector === FORWARD_SELECTOR) {
      if (destKey !== 'cross') throw unsupported('a hub forward must be sent to the hub chain', target, selector);
      let fwd;
      try { fwd = decodeForward(e.extraData); } catch (err) { throw unsupported(`undecodable bridgeTokenForwarded call: ${(err as Error).message}`, target, selector); }
      const dest = chainKeyById(net, Number(fwd.toChainID));
      const multi: Record<string, Route> = { 'bsc>hyperevm': Route.BSC_TO_ONE_TO_HYPEREVM, 'hyperevm>bsc': Route.HYPEREVM_TO_ONE_TO_BSC };
      const r = dest ? multi[`${srcKey}>${dest}`] : undefined;
      if (!r) throw unsupported(`hop-2 destination chain ${fwd.toChainID} is not a configured route destination for a multihop from ${srcKey}`, target, selector);
      route = r;
      if (fwd.inner !== '0x' && fwd.inner.length > 2) {
        const it = fwd.inner.length >= 2 + 48 ? getAddress('0x' + fwd.inner.slice(2, 42)) : null;
        const isel = ('0x' + fwd.inner.slice(42, 50)).toLowerCase();
        if (r === Route.BSC_TO_ONE_TO_HYPEREVM && heTok && it && sameAddr(it, heTok) && isel === CORE_SELECTOR) { checkX2(net, fwd.inner, fwd.value); hypercore = true; }
        else throw unsupported('unsupported nested hop-2 extraData', it ?? undefined, isel);
      }
    } else if (v3 && sameAddr(target, v3) && selector === EXACT_INPUT_SINGLE_SELECTOR && srcKey === 'bsc' && destKey === 'cross') {
      checkX1(net, e.extraData, e.value);
      route = Route.BSC_USDT_TO_ONEUSD;
    } else if (heTok && sameAddr(target, heTok) && selector === CORE_SELECTOR && srcKey === 'cross' && destKey === 'hyperevm') {
      checkX2(net, e.extraData, e.value);
      route = Route.ONE_TO_HYPERCORE;
    } else {
      throw unsupported('the extraData target/selector maps to no supported route', target, selector);
    }
  }
  if (opts.route !== undefined && opts.route !== route) throw new RouteMismatchError(opts.route, route);
  return { route, hypercore, ...(x3ToChain ? { x3ToChain } : {}), logIndex: e.logIndex, event: e };
}
