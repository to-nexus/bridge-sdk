import { Interface, getAddress, keccak256 } from 'ethers';
import type { Ctx } from './ctx.js';
import { FORWARD_SELECTOR } from './encoding.js';
import { checkX1, checkX2, decodeForward } from './extra.js';
export { decodeForward } from './extra.js';
import { AmbiguousSourceError, BridgeSdkError, UnsupportedRouteError } from './errors.js';
import { chainOf } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { bridgeI, sbrI } from './quote.js';
import { Route } from './types.js';
import type { BridgeMatch, HopKind, HopRef, MatchSpec, Quote, SwapMatch, TransferHandle } from './types.js';

export const TOPIC_BRIDGE_INITIATED = bridgeI.getEvent('BridgeInitiated')!.topicHash;
export const TOPIC_SWAP_BRIDGE = sbrI.getEvent('SwapBridge')!.topicHash;

export interface RouteShape {
  src: ChainKey;
  hub?: ChainKey;
  dest: ChainKey; // final destination
  hop1Dest: ChainKey; // first hop destination (the hub for multihop / X1's hub)
  hop1Kind: HopKind;
  hop2Kind?: HopKind;
  swap: boolean; // X3
}

export function routeShape(route: Route, opts: { hypercore?: boolean; x3ToChain?: 'bsc' | 'hyperevm' } = {}): RouteShape {
  switch (route) {
    case Route.BSC_TO_ONE: return { src: 'bsc', dest: 'cross', hop1Dest: 'cross', hop1Kind: 'plain', swap: false };
    case Route.ONE_TO_BSC: return { src: 'cross', dest: 'bsc', hop1Dest: 'bsc', hop1Kind: 'plain', swap: false };
    case Route.ONE_TO_HYPEREVM: return { src: 'cross', dest: 'hyperevm', hop1Dest: 'hyperevm', hop1Kind: 'plain', swap: false };
    case Route.ONE_TO_HYPERCORE: return { src: 'cross', dest: 'hyperevm', hop1Dest: 'hyperevm', hop1Kind: 'x2', swap: false };
    case Route.HYPEREVM_TO_ONE: return { src: 'hyperevm', dest: 'cross', hop1Dest: 'cross', hop1Kind: 'plain', swap: false };
    case Route.BSC_TO_ONE_TO_HYPEREVM: return { src: 'bsc', hub: 'cross', dest: 'hyperevm', hop1Dest: 'cross', hop1Kind: 'hubForward', hop2Kind: opts.hypercore ? 'x2' : 'plain', swap: false };
    case Route.HYPEREVM_TO_ONE_TO_BSC: return { src: 'hyperevm', hub: 'cross', dest: 'bsc', hop1Dest: 'cross', hop1Kind: 'hubForward', hop2Kind: 'plain', swap: false };
    case Route.BSC_USDT_TO_ONEUSD: return { src: 'bsc', hub: 'cross', dest: 'cross', hop1Dest: 'cross', hop1Kind: 'x1', swap: false };
    case Route.SWAP_BRIDGE: return { src: 'cross', dest: opts.x3ToChain ?? 'bsc', hop1Dest: opts.x3ToChain ?? 'bsc', hop1Kind: 'plain', swap: true };
    default: throw new RangeError(`unknown route ${String(route)}`);
  }
}

export interface ParsedInitiated {
  toChainID: bigint;
  index: bigint;
  fromToken: string;
  toToken: string;
  from: string;
  to: string;
  value: bigint;
  networkFee: bigint;
  exFee: bigint;
  extraData: string;
  logIndex: number;
  address: string;
}

export function parseInitiated(log: { address: string; topics: string[]; data: string; logIndex: number | string }): ParsedInitiated | null {
  if (log.topics[0]?.toLowerCase() !== TOPIC_BRIDGE_INITIATED.toLowerCase()) return null;
  const d = bridgeI.parseLog({ topics: log.topics as string[], data: log.data });
  if (!d) return null;
  return {
    toChainID: d.args[0], index: d.args[1], fromToken: d.args[2], toToken: d.args[3], from: d.args[4], to: d.args[5],
    value: d.args[6], networkFee: d.args[7], exFee: d.args[8], extraData: d.args[9], logIndex: Number(log.logIndex), address: getAddress(log.address),
  };
}

export interface RawReceipt {
  status: string;
  blockNumber: string;
  transactionHash: string;
  logs: { address: string; topics: string[]; data: string; logIndex: string; transactionHash?: string; blockNumber?: string }[];
}

/**
 * Does a BridgeInitiated event correspond to the (adopted) bridge intent?
 * Every non-fee field must match exactly: toChainID, fromToken, toToken, from (when given), to, value and keccak(extraData).
 * Fees match when `event.fee <= intent.fee` for BOTH networkFee and exFee. Reason (BaseBridge invariant): `bridgeToken` only
 * accepts a tx when `networkFee >= calc && exFee >= calc` (`_checkInitiateAmount`, src/BaseBridge.sol) and then emits the verifier's
 * CALCULATED values, so for any successful transfer the event fees are always <= the calldata fees (they are lower when fees
 * dropped between quote/adoption and inclusion). Strict equality would miss such a transfer and invite a duplicate send.
 * (Hop-2 / ForwardInitiated matching is a different rule - the hub re-settles those fees.)
 */
export function matchesBridgeIntent(m: BridgeMatch, e: ParsedInitiated, from?: string): boolean {
  return (
    e.toChainID === BigInt(m.toChainID) &&
    e.fromToken.toLowerCase() === m.fromToken.toLowerCase() &&
    e.toToken.toLowerCase() === m.toToken.toLowerCase() &&
    (from === undefined || e.from.toLowerCase() === from.toLowerCase()) &&
    e.to.toLowerCase() === m.recipient.toLowerCase() &&
    e.value === m.value &&
    e.networkFee <= m.networkFee && e.exFee <= m.exFee &&
    keccak256(e.extraData) === m.extraDataHash
  );
}

const sameA = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** one X3 transfer, decoded from the router's SwapBridge log and the bridge's BridgeInitiated log it is linked to (structural, intent-free) */
export interface SwapTransfer {
  user: string;
  tokenIn: string;
  tokenOut: string;
  amountIn: bigint;
  amountOut: bigint;
  toChainID: bigint;
  /** BridgeInitiated.toToken (the destination token the BRIDGE emitted) */
  toToken: string;
  /** BridgeInitiated.to */
  recipient: string;
  /** keccak256(BridgeInitiated.extraData) */
  extraDataHash: string;
  index: bigint;
  value: bigint;
  swapLogIndex: number;
  swapEmitter: string;
  bi: ParsedInitiated;
}

export interface SwapLinkResult {
  /** SwapBridge logs of the configured router that link consistently to a BridgeInitiated of the configured bridge */
  linked: SwapTransfer[];
  /** logIndex of every SwapBridge log (any emitter) that is NOT in `linked` */
  unlinkedSwapLogs: { logIndex: number; emitter: string }[];
  /** logIndex of every BridgeInitiated consumed by a link (these are not separate transfers) */
  consumedInitiated: Set<number>;
}

/**
 * STRUCTURAL, INTENT-FREE link of the router's SwapBridge logs to the bridge's BridgeInitiated logs of ONE receipt. A SwapBridge log (topic0, emitted by
 * the configured router) is linked when the same receipt holds a BridgeInitiated emitted by the configured bridge with from == router, indexed
 * index == SwapBridge.initiateIndex, toChainID == SwapBridge.toChainID, fromToken == SwapBridge.tokenOut, value == SwapBridge.bridgeValue and
 * value + networkFee + exFee <= SwapBridge.amountOut (the router refunds floor dust). It compares NOTHING against a quote: the actual fees and output
 * are decided at inclusion and may legitimately be higher than quoted. A SwapBridge log without a consistent link is `unlinked` (unattributed evidence).
 */
export function linkSwapTransfer(receipt: RawReceipt, cfg: { router: string | null; bridge: string }): SwapLinkResult {
  const linked: SwapTransfer[] = [];
  const unlinkedSwapLogs: SwapLinkResult['unlinkedSwapLogs'] = [];
  const consumed = new Set<number>();
  const initiated: ParsedInitiated[] = [];
  for (const l of receipt.logs) {
    if (sameA(l.address, cfg.bridge) && l.topics[0]?.toLowerCase() === TOPIC_BRIDGE_INITIATED.toLowerCase()) {
      const p = parseInitiated({ ...l, logIndex: l.logIndex });
      if (p) initiated.push(p);
    }
  }
  for (const l of receipt.logs) {
    if (l.topics[0]?.toLowerCase() !== TOPIC_SWAP_BRIDGE.toLowerCase()) continue;
    const logIndex = Number(l.logIndex);
    let d: ReturnType<typeof sbrI.parseLog> = null;
    try { d = sbrI.parseLog({ topics: l.topics, data: l.data }); } catch { d = null; }
    if (!cfg.router || !sameA(l.address, cfg.router) || !d) { unlinkedSwapLogs.push({ logIndex, emitter: l.address }); continue; }
    const [user, tokenIn, tokenOut, amountIn, amountOut, toChainID, initiateIndex, bridgeValue] = d.args as unknown as [string, string, string, bigint, bigint, bigint, bigint, bigint];
    const b = initiated.find((e) => !consumed.has(e.logIndex) && e.index === initiateIndex && sameA(e.from, cfg.router!) && e.toChainID === toChainID && sameA(e.fromToken, tokenOut)
      && e.value === bridgeValue && e.value + e.networkFee + e.exFee <= amountOut);
    if (!b) { unlinkedSwapLogs.push({ logIndex, emitter: l.address }); continue; }
    consumed.add(b.logIndex);
    linked.push({ user: getAddress(user), tokenIn: getAddress(tokenIn), tokenOut: getAddress(tokenOut), amountIn, amountOut, toChainID, toToken: b.toToken, recipient: b.to, extraDataHash: keccak256(b.extraData), index: b.index, value: b.value, swapLogIndex: logIndex, swapEmitter: getAddress(l.address), bi: b });
  }
  return { linked, unlinkedSwapLogs, consumedInitiated: consumed };
}

/**
 * Does a LINKED X3 transfer correspond to the (adopted) intent? user == sender; tokenIn, tokenOut, amountIn and toChainID equal; recipient equals the intent's
 * resolved destination recipient; the linked extraData hash is keccak256(0x) (X3 user extraData is unsupported); the BridgeInitiated.toToken equals the STORED
 * expected destination token (`match.toToken`, from the hub pair at quote time - never the hub tokenOut and never the event's own value); and amountOut >=
 * amountOutMinimum when it is known (consistency only; the chain enforces it). NO upper bound on fees or output.
 */
export function matchesSwapIntent(t: SwapTransfer, m: SwapMatch, from: string | undefined, amountOutMinimum?: bigint): boolean {
  return (
    (from === undefined || sameA(t.user, from)) &&
    sameA(t.tokenIn, m.tokenIn) && sameA(t.tokenOut, m.tokenOut) && t.amountIn === m.amountIn && t.toChainID === BigInt(m.toChainID) &&
    sameA(t.recipient, m.recipient) &&
    t.extraDataHash === keccak256('0x') &&
    sameA(t.toToken, m.toToken) &&
    (amountOutMinimum === undefined || t.amountOut >= amountOutMinimum)
  );
}

function matches(m: MatchSpec | undefined, e: ParsedInitiated): boolean {
  if (!m || m.kind !== 'bridge') return true;
  return matchesBridgeIntent(m, e);
}

export interface HandleInput {
  route: Route;
  srcKey: ChainKey;
  receipt: RawReceipt;
  hypercore?: boolean;
  x3ToChain?: 'bsc' | 'hyperevm';
  /** the transfer's sender (bridge `from`; for X3 the SwapBridge `user`) */
  sender?: string;
  match?: MatchSpec;
  /** X3 intent consistency (amountOut >= amountOutMinimum), from the adopted quote's limits */
  amountOutMinimum?: bigint;
  /** when the receipt holds several transfers of the sender, pick this BridgeInitiated index */
  initiateIndex?: bigint | number;
  /** select exactly one BridgeInitiated log (set by route inference; X3: the bridge log paired with the SwapBridge event) */
  logIndex?: number;
  scanFrom?: Partial<Record<ChainKey, number>>;
  /** X3 router, for `from` filtering */
}

/** Build a TransferHandle from the source receipt (never from absence of events). */
export function buildHandle(ctx: Ctx, h: HandleInput): TransferHandle {
  const shape = routeShape(h.route, { hypercore: h.hypercore, x3ToChain: h.x3ToChain });
  const src = chainOf(ctx.net, h.srcKey);
  if (h.srcKey !== shape.src) throw new BridgeSdkError(`route ${h.route} starts on ${shape.src}, not ${h.srcKey}`);
  const srcBridge = getAddress(src.bridge!);
  const router = ctx.net.chains.cross.swap?.swapBridgeRouter ? getAddress(ctx.net.chains.cross.swap.swapBridgeRouter) : null;
  const srcContract = shape.swap ? router! : srcBridge;
  const bridgeAddr = srcBridge;

  const initiated: ParsedInitiated[] = [];
  for (const l of h.receipt.logs) {
    if (l.address.toLowerCase() === bridgeAddr.toLowerCase() && l.topics[0]?.toLowerCase() === TOPIC_BRIDGE_INITIATED.toLowerCase()) {
      const p = parseInitiated({ ...l, logIndex: l.logIndex });
      if (p) initiated.push(p);
    }
  }
  const swapLinked = shape.swap ? linkSwapTransfer(h.receipt, { router, bridge: bridgeAddr }).linked : [];

  let chosen: ParsedInitiated | undefined;
  if (h.logIndex !== undefined) {
    chosen = initiated.find((i) => i.logIndex === h.logIndex);
    if (!chosen) throw new BridgeSdkError(`no BridgeInitiated with logIndex ${h.logIndex} emitted by the source bridge in the receipt`);
  } else if (shape.swap) {
    // X3: ONLY the structurally LINKED transfers are candidates (linkSwapTransfer); with an adopted intent they must also match it (matchesSwapIntent)
    const m = h.match?.kind === 'swap' ? h.match : undefined;
    const cands = swapLinked.filter((s) => (!h.sender || sameA(s.user, h.sender)) && (!m || matchesSwapIntent(s, m, h.sender, h.amountOutMinimum)));
    if (cands.length === 0) throw new BridgeSdkError('no linked SwapBridge + BridgeInitiated pair in the receipt matches the intent');
    if (cands.length > 1 && h.initiateIndex === undefined) throw new AmbiguousSourceError('several SwapBridge events match; pass initiateIndex', cands.map((c) => ({ toChainID: c.toChainID, index: c.index, logIndex: c.bi.logIndex })));
    const sb = h.initiateIndex !== undefined ? cands.find((c) => c.index === BigInt(h.initiateIndex!)) : cands[0];
    if (!sb) throw new BridgeSdkError('initiateIndex not found among SwapBridge events');
    chosen = sb.bi;
  } else {
    const cands = initiated.filter((i) => (!h.sender || i.from.toLowerCase() === h.sender.toLowerCase()) && matches(h.match, i));
    if (cands.length === 0) throw new BridgeSdkError('no matching BridgeInitiated event emitted by the source bridge in the receipt');
    if (cands.length > 1 && h.initiateIndex === undefined) throw new AmbiguousSourceError('several BridgeInitiated events match; pass initiateIndex', cands.map((c) => ({ toChainID: c.toChainID, index: c.index, logIndex: c.logIndex })));
    chosen = h.initiateIndex !== undefined ? cands.find((c) => c.index === BigInt(h.initiateIndex!)) : cands[0];
    if (!chosen) throw new BridgeSdkError('initiateIndex not found among BridgeInitiated events');
  }

  const hop1Dest = chainOf(ctx.net, shape.hop1Dest);
  const hop1: HopRef = { destChainKey: shape.hop1Dest, destChainId: hop1Dest.chainId, index: chosen.index.toString(), kind: shape.hop1Kind };
  const expect: TransferHandle['expect'] = { value: chosen.value.toString(), token: chosen.toToken };
  let hop2: TransferHandle['hop2'];
  if (shape.hop1Kind === 'hubForward') {
    const fwd = decodeForward(chosen.extraData);
    hop2 = { destChainKey: shape.dest, destChainId: ctx.net.chains[shape.dest].chainId, kind: shape.hop2Kind ?? 'plain' };
    expect.v2 = fwd.value.toString();
    if (Number(fwd.toChainID) !== hop2.destChainId) throw new BridgeSdkError(`forward extraData targets chain ${fwd.toChainID}, route expects ${hop2.destChainId}`);
    if (hop2.kind === 'x2') expect.x2 = checkX2(ctx.net, fwd.inner, fwd.value); // nested X2: the amount committed in the forward payload
  } else if (shape.hop1Kind === 'x1') {
    expect.x1 = checkX1(ctx.net, chosen.extraData, chosen.value);
  } else if (shape.hop1Kind === 'x2') {
    expect.x2 = checkX2(ctx.net, chosen.extraData, chosen.value);
  } else if (shape.swap && chosen.extraData !== '0x' && chosen.extraData.length > 2) {
    throw new UnsupportedRouteError('X3 with non-empty extraData is not supported by this SDK');
  }
  return {
    v: 1, network: ctx.network, route: h.route, srcChainKey: h.srcKey, srcChainId: src.chainId, srcContract, srcBridge,
    srcTxHash: h.receipt.transactionHash, srcBlock: Number(BigInt(h.receipt.blockNumber)),
    hop1, ...(hop2 ? { hop2 } : {}), expect, scanFrom: { ...(h.scanFrom ?? {}) }, scans: {},
  };
}

export function quoteHandleInput(q: Quote, receipt: RawReceipt, scanFrom?: Partial<Record<ChainKey, number>>): HandleInput {
  return {
    route: q.route, srcKey: q.sourceChainKey, receipt, hypercore: q.request.hypercore, x3ToChain: q.request.x3?.toChain,
    sender: q.sender, match: q.match, amountOutMinimum: q.limits.amountOutMinimum, scanFrom,
  };
}
