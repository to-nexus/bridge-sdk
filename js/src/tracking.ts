import { AbiCoder, getAddress, keccak256 } from 'ethers';
import { backoffOf, type Ctx } from './ctx.js';
import { NetworkMismatchError, RpcTimeoutError } from './errors.js';
import { parseInitiated, type RawReceipt } from './handle.js';
import { chainOf } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { bridgeI, hyperTokenI } from './quote.js';
import { fromHexQty, readContract, withBackoff } from './rpc.js';
import { headBlock, hex32, scanLogs } from './scan.js';
import { TERMINAL_STATES } from './types.js';
import { CORE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR } from './encoding.js';
import type { HopKind, HopStatus, RawLog, ScanState, TrackResult, TransferHandle, TransferState, X1Expect, X2Expect } from './types.js';

export const FINALIZE_STATUS = [
  'None', 'Success', 'TokenPaused', 'VerificationAmountThresholdExceeded', 'PeriodTotalValueThresholdExceeded', 'TransferFailed',
  'MintFailed', 'TokenScoreOverflow', 'TokenCurrentVolumeOverflow', 'CrossSupplyLimitExceeded', 'TokenPriceUnavailable', 'InsufficientLiquidity', 'InvalidRecipient',
];
export const FORWARD_FAILURE_CODE = ['None', 'ForwardNotAttempted', 'ExecutorCallReverted', 'ForwardCallNotConsumed'];

const T = (n: string) => bridgeI.getEvent(n)!.topicHash.toLowerCase();
const TOPIC = {
  finalized: T('BridgeFinalized'), pending: T('BridgePending'), extra: T('ExtraCallExecuted'),
  fwdInit: T('ForwardInitiated'), fwdFail: T('ForwardFailed'), manual: T('ManualReleased'), removed: T('PendingRemoved'),
};
const KEYED_TOPICS = Object.values(TOPIC);
const FN_SELECTOR = (n: string) => bridgeI.getFunction(n)!.selector.toLowerCase();
const RELEASE_SELECTORS = new Set([FN_SELECTOR('releasePending'), FN_SELECTOR('manualReleasePending'), FN_SELECTOR('manualReleasePendingWithRecipient')]);
const FINALIZE_BATCH_SELECTOR = FN_SELECTOR('finalizeBridgeBatch');

class Unknown extends Error {}

export interface Evidence {
  /** keyed (fromChainID, index) logs for the hop item, in chain order */
  logs: RawLog[];
}

/** Pure per-hop classification (explicit resolution events first, then the requested hop kind). Everything it needs is passed in. */
export interface ClassifyInput {
  kind: HopKind;
  logs: RawLog[];
  /** tx input selector by tx hash (needed for BridgeFinalized / release detection) */
  selectorOf: (txHash: string) => Promise<string | null>;
  /** receipt of the tx holding ForwardInitiated (hub multihop) */
  receiptOf: (txHash: string) => Promise<RawReceipt | null>;
  hubAddress: string;
  /** token addresses for X1 labels */
  x1OneUsd?: string;
  /** the decoded extraData call stored in the handle. Without it an x1 / x2 hop can never be reported as success (PROCESSED_UNVERIFIED). */
  x1?: X1Expect;
  x2?: X2Expect;
}

export interface Classified {
  state: TransferState;
  token?: string;
  amount?: string | 'unknown';
  recipient?: string;
  reason?: string;
  code?: string;
  txHash?: string;
  detail?: Record<string, unknown>;
  hop2?: { toChainID: string; index: string };
}

const sameAddr = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

export async function classifyHop(c: ClassifyInput): Promise<Classified> {
  const by = (t: string) => c.logs.filter((l) => l.topics[0]?.toLowerCase() === t);
  const finalized = by(TOPIC.finalized);
  const removed = by(TOPIC.removed);
  const manual = by(TOPIC.manual);
  const extra = by(TOPIC.extra);
  const fwdInit = by(TOPIC.fwdInit);
  const fwdFail = by(TOPIC.fwdFail);

  const fin = finalized[0];
  const finDec = fin ? bridgeI.parseLog({ topics: fin.topics, data: fin.data })! : null;
  const toToken = fin ? getAddress('0x' + fin.topics[3]!.slice(26)) : undefined;

  // step 4: explicit resolution events first
  if (removed.length) return { state: 'PENDING_REMOVED', txHash: removed[0].transactionHash, reason: 'removed by an operator without being processed' };
  if (fin) {
    const sel = await c.selectorOf(fin.transactionHash);
    if ((sel && RELEASE_SELECTORS.has(sel.toLowerCase())) || manual.length) {
      return {
        state: 'RELEASED_FROM_PENDING', token: toToken, amount: String(finDec!.args[4]), recipient: finDec!.args[3], txHash: fin.transactionHash,
        reason: 'released from pending: the payout is the plain token to the release recipient (extraData is not re-executed)',
        detail: { manual: manual.length > 0, selector: sel },
      };
    }
  }
  // step 5
  if (!fin) return { state: 'PROCESSED_UNVERIFIED', reason: 'index advanced and is not pending, but no keyed BridgeFinalized was found (logs unavailable, pruned or removed)' };

  // step 6: only for a finalizeBridgeBatch tx
  const sel = await c.selectorOf(fin.transactionHash);
  if (!sel || sel.toLowerCase() !== FINALIZE_BATCH_SELECTOR) {
    return { state: 'PROCESSED_UNVERIFIED', txHash: fin.transactionHash, reason: `BridgeFinalized was emitted by an unrecognised transaction (selector ${sel ?? 'unknown'})` };
  }
  const value = String(finDec!.args[4]);
  const to = String(finDec!.args[3]);

  if (c.kind === 'plain') return { state: 'DELIVERED', token: toToken, amount: value, recipient: to, txHash: fin.transactionHash };

  if (c.kind === 'hubForward') {
    if (fwdFail.length) {
      const d = bridgeI.parseLog({ topics: fwdFail[0].topics, data: fwdFail[0].data });
      const code = FORWARD_FAILURE_CODE[Number(d?.args[2] ?? 0)] ?? String(d?.args[2]);
      return { state: 'HUB_FALLBACK', token: toToken, amount: value, recipient: to, code, txHash: fwdFail[0].transactionHash, reason: 'hop 2 could not be started: native ONE was paid to the hop-1 recipient (toOnHub)', detail: { reasonHash: d?.args[3] } };
    }
    if (fwdInit.length === 1) {
      const fi = fwdInit[0];
      const fd = bridgeI.parseLog({ topics: fi.topics, data: fi.data })!;
      const rec = await c.receiptOf(fi.transactionHash);
      if (!rec) throw new Unknown(`receipt of ${fi.transactionHash} unavailable`);
      const cands: { index: bigint }[] = [];
      for (const l of rec.logs) {
        if (!sameAddr(l.address, c.hubAddress) || Number(l.logIndex) >= fi.logIndex) continue;
        const e = parseInitiated({ ...l, logIndex: l.logIndex });
        if (!e) continue;
        if (e.toChainID !== fd.args[2] || !sameAddr(e.to, fd.args[4]) || e.value !== fd.args[5] || e.networkFee !== fd.args[6] || e.exFee !== fd.args[7] || keccak256(e.extraData).toLowerCase() !== String(fd.args[8]).toLowerCase()) continue;
        // no other ForwardInitiated between the candidate and ours
        const between = rec.logs.some((o) => sameAddr(o.address, c.hubAddress) && o.topics[0]?.toLowerCase() === TOPIC.fwdInit && Number(o.logIndex) > Number(l.logIndex) && Number(o.logIndex) < fi.logIndex);
        if (between) continue;
        cands.push({ index: e.index });
      }
      if (cands.length === 1) {
        return { state: 'FORWARDED', txHash: fi.transactionHash, amount: String(fd.args[5]), token: toToken, recipient: String(fd.args[4]), hop2: { toChainID: String(fd.args[2]), index: cands[0].index.toString() }, detail: { hop2Value: String(fd.args[5]), hop2NetworkFee: String(fd.args[6]), hop2ExFee: String(fd.args[7]) } };
      }
      return { state: 'PROCESSED_UNVERIFIED', txHash: fi.transactionHash, reason: `${cands.length} candidate hop-2 BridgeInitiated events matched ForwardInitiated (expected exactly 1); not guessing` };
    }
    return { state: 'PROCESSED_UNVERIFIED', txHash: fin.transactionHash, reason: 'hub hop 1 finalized, but neither ForwardInitiated nor ForwardFailed was found for this item' };
  }

  // x1 / x2: the requested extraData kind decides; absence of the event is NOT inferred as plain delivery
  const ec = extra[0];
  const ecDec = ec ? bridgeI.parseLog({ topics: ec.topics, data: ec.data })! : null;
  const success = ecDec ? Boolean(ecDec.args[4]) : null;
  const unsupported = (why: string): Classified => ({ state: 'PROCESSED_UNVERIFIED', txHash: fin.transactionHash, reason: `unsupported extraData: ${why}` });
  const callIs = (target: string, selector: string) => !!ecDec && sameAddr(String(ecDec.args[2]), target) && String(ecDec.args[3]).toLowerCase() === selector;
  if (c.kind === 'x2') {
    if (!c.x2) return unsupported('the handle carries no decoded transferToCoreFor call');
    if (success === true) {
      if (!callIs(c.x2.token, CORE_SELECTOR)) return unsupported(`ExtraCallExecuted target/method (${ecDec!.args[2]}, ${ecDec!.args[3]}) is not the expected transferToCoreFor on ${c.x2.token}`);
      const consumed = BigInt(ecDec!.args[5]);
      // returnData = abi.encode(uint sent), sent = coreTransferableAmount(encodedAmount) (floored to coreUnit); consumed == sent for this token
      let sent = consumed;
      try { const rd = String(ecDec!.args[6]); if (rd.length >= 66) sent = AbiCoder.defaultAbiCoder().decode(['uint256'], rd.slice(0, 66))[0]; } catch { /* keep consumed */ }
      const returned = BigInt(value) > consumed ? BigInt(value) - consumed : 0n;
      return {
        state: 'CORE_DEPOSIT_SUBMITTED', token: toToken, amount: sent.toString(), recipient: c.x2.coreRecipient, txHash: ec.transactionHash,
        detail: {
          coreRecipient: c.x2.coreRecipient, coreAmount: sent.toString(), encodedAmount: c.x2.encodedAmount, finalizedValue: value, consumedEvm: consumed.toString(),
          returnedToDest: returned.toString(), returnedTo: to,
          note: 'coreAmount is the EVM-wei amount sent to the HyperCore system address (floored to coreUnit); the surplus (returnedToDest) was paid to BridgeFinalized.to on the destination. The HyperCore credit is asynchronous - confirm via the HyperCore info API',
        },
      };
    }
    return { state: 'DELIVERED_FALLBACK', token: toToken, amount: value, recipient: to, txHash: fin.transactionHash, reason: success === false ? 'extraCall-failed' : 'extraCall-skipped' };
  }
  // x1
  if (!c.x1) return unsupported('the handle carries no decoded exactInputSingle call');
  if (BigInt(value) !== BigInt(c.x1.amountIn)) return unsupported(`finalized value ${value} != the decoded exactInputSingle amountIn ${c.x1.amountIn}`);
  if (success === true) {
    if (!callIs(c.x1.router, EXACT_INPUT_SINGLE_SELECTOR)) return unsupported(`ExtraCallExecuted target/method (${ecDec!.args[2]}, ${ecDec!.args[3]}) is not exactInputSingle on ${c.x1.router}`);
    let amount: string | 'unknown' = 'unknown';
    try {
      const rd = String(ecDec!.args[6]);
      if (rd.length >= 66) amount = AbiCoder.defaultAbiCoder().decode(['uint256'], rd.slice(0, 66))[0].toString();
    } catch { /* undecodable -> unknown */ }
    // the swap output goes to the decoded exactInputSingle recipient (NOT BridgeFinalized.to, which is the executor path)
    return {
      state: 'DELIVERED', token: c.x1.tokenOut, amount, recipient: c.x1.recipient, txHash: ec.transactionHash,
      detail: { usdtxDelivered: value, amountOutMinimum: c.x1.amountOutMinimum, bridgeFinalizedTo: to, fee: c.x1.fee },
    };
  }
  return { state: 'DELIVERED_FALLBACK', token: toToken, amount: value, recipient: to, txHash: fin.transactionHash, reason: success === false ? 'extraCall-failed' : 'extraCall-skipped', detail: { note: 'the swap did not run (slippage / deadline / pool); plain USDTx was paid, no loss' } };
}

// ------------------------------------------------------------------------------------------------ RPC-backed tracker
/** UNKNOWN reason when eth_chainId could not be verified: a timeout keeps the "timeout:" prefix, every other error is "chain-unverified:". */
function chainUnverifiedReason(e: unknown): string {
  const m = (e as Error)?.message ?? String(e);
  return e instanceof RpcTimeoutError ? `timeout: the RPC chain id could not be verified in time: ${m}.` : `chain-unverified: ${m}.`;
}

function scanKey(chain: ChainKey, fromChainId: number, index: string) { return `${chain}:${fromChainId}:${index}`; }

async function evalHop(ctx: Ctx, h: TransferHandle, which: 1 | 2): Promise<HopStatus & { hop2?: { toChainID: string; index: string } }> {
  const ref = which === 1 ? h.hop1 : { ...h.hop2!, index: h.hop2!.index! };
  const chain = ref.destChainKey;
  const cfg = chainOf(ctx.net, chain);
  const bridge = getAddress(cfg.bridge!);
  const fromChainId = which === 1 ? h.srcChainId : ctx.net.chains.cross.chainId;
  const index = BigInt(ref.index);
  const base: HopStatus = { chainKey: chain, chainId: cfg.chainId, hop: which, index: ref.index, state: 'UNKNOWN' };
  const rpc = ctx.rpc(chain);
  try {
    await ctx.assertChain(chain); // EVERY error stops all evidence reads on this chain: the catch below returns UNKNOWN, nothing is read after it
  } catch (e) {
    if (e instanceof NetworkMismatchError) throw e;
    return { ...base, state: 'UNKNOWN', reason: `${chainUnverifiedReason(e)} Recovery: check the bridge events on ${chain} for (fromChainID=${fromChainId}, index=${ref.index}) manually, then ask support.` };
  }
  try {
    // step 2: currently pending is authoritative
    const pend: bigint[] = await withBackoff(() => readContract<bigint[]>(rpc, bridgeI, bridge, 'allPendingIndex', [fromChainId]), backoffOf(ctx));
    if (pend.some((p) => BigInt(p) === index)) {
      let reason: string | undefined;
      try {
        const pd = await readContract<any>(rpc, bridgeI, bridge, 'getPendingArguments', [fromChainId, index]);
        reason = FINALIZE_STATUS[Number(pd[1])] ?? String(pd[1]);
      } catch { /* reason is best effort */ }
      return { ...base, state: 'PENDING_ON_DEST', reason };
    }
    // step 3
    const next: bigint = await withBackoff(() => readContract<bigint>(rpc, bridgeI, bridge, 'getNextFinalizeIndex', [fromChainId]), backoffOf(ctx));
    if (BigInt(next) <= index) return { ...base, state: 'IN_FLIGHT' };

    // chunked, resumable, deduped scan for the keyed events of this item
    const key = scanKey(chain, fromChainId, ref.index);
    const st: ScanState = h.scans[key] ?? { lastScanned: -1, logs: [] };
    const head = await headBlock(ctx, chain);
    const start = st.lastScanned >= 0 ? st.lastScanned + 1 : (h.scanFrom[chain] ?? Math.max(0, head - (ctx.opts.scanLookbackBlocks ?? 20_000)));
    if (start <= head) {
      const fresh = await scanLogs(ctx, chain, { address: bridge, topics: [KEYED_TOPICS, hex32(fromChainId), hex32(index)] }, start, head);
      const have = new Set(st.logs.map((l) => `${l.transactionHash}:${l.logIndex}`));
      for (const l of fresh) if (!have.has(`${l.transactionHash}:${l.logIndex}`)) st.logs.push(l);
      st.lastScanned = head;
    }
    h.scans[key] = st;

    const kind = ref.kind;
    const selCache = new Map<string, string | null>();
    const selectorOf = async (hash: string) => {
      if (selCache.has(hash)) return selCache.get(hash)!;
      const tx = await withBackoff(() => rpc.send('eth_getTransactionByHash', [hash]), backoffOf(ctx));
      const sel = tx?.input ? String(tx.input).slice(0, 10).toLowerCase() : null;
      selCache.set(hash, sel);
      return sel;
    };
    const receiptOf = (hash: string) => withBackoff(() => rpc.send('eth_getTransactionReceipt', [hash]), backoffOf(ctx)) as Promise<RawReceipt | null>;
    const cls = await classifyHop({ kind, logs: st.logs, selectorOf, receiptOf, hubAddress: bridge, x1OneUsd: ctx.net.chains.cross.tokens.ONEUSD?.address, ...(h.expect.x1 ? { x1: h.expect.x1 } : {}), ...(h.expect.x2 ? { x2: h.expect.x2 } : {}) });
    const { hop2, ...rest } = cls;
    return { ...base, ...rest, ...(hop2 ? { hop2 } : {}) };
  } catch (e) {
    if (e instanceof NetworkMismatchError) throw e;
    const to = e instanceof RpcTimeoutError ? 'timeout: ' : '';
    return { ...base, state: 'UNKNOWN', reason: `${to}RPC/log evidence unavailable: ${(e as Error).message}. Recovery: check the bridge events on ${chain} for (fromChainID=${fromChainId}, index=${ref.index}) manually, then ask support.` };
  }
}

const NOTES: Partial<Record<TransferState, string>> = {
  SOURCE_SUBMITTED: 'source transaction not mined yet',
  SOURCE_FAILED: 'the source transaction failed; no transfer was created',
  IN_FLIGHT: 'waiting for the destination finalize (normally seconds to minutes; arrival times are observations, not guarantees)',
  PENDING_ON_DEST: 'held in the destination pending queue (e.g. verification threshold); it is released automatically or by the operator',
  DELIVERED_FALLBACK: 'delivered as the plain token instead of the requested action (see reason); funds are safe',
  RELEASED_FROM_PENDING: 'released from pending as the plain token',
  CORE_DEPOSIT_SUBMITTED: 'EVM-side transfer to HyperCore done; the Core credit is asynchronous',
  FORWARDED: 'hop 1 finished on the hub and hop 2 was started; tracking hop 2',
  HUB_FALLBACK: 'hop 2 could not start; native ONE is on the hub at the hop-1 recipient (toOnHub). Bridge it onward manually (R2/R3).',
  PENDING_REMOVED: 'the pending item was removed by an operator; contact support',
  PROCESSED_UNVERIFIED: 'processed on chain but the evidence is missing/ambiguous; NOT reported as success. Check the explorer.',
  UNKNOWN: 'could not reach evidence; see reason for manual recovery',
};

/** one evaluation pass; the returned handle carries the resumable scan state */
export async function trackOnce(ctx: Ctx, handleIn: TransferHandle): Promise<TrackResult> {
  const h: TransferHandle = structuredClone(handleIn);
  // a hop whose evidence call timed out (per-call or overall deadline) => UNKNOWN{reason: 'timeout: ...'} with timedOut: true
  const done = (state: TransferState, hops: HopStatus[], extra: Partial<TrackResult> = {}): TrackResult => ({
    state, terminal: TERMINAL_STATES.has(state), hops, handle: h, note: NOTES[state],
    ...(state === 'UNKNOWN' && hops.some((x) => x.reason?.startsWith('timeout:')) ? { timedOut: true } : {}), ...extra,
  });
  const src: HopStatus = { chainKey: h.srcChainKey, chainId: h.srcChainId, hop: 1, index: h.hop1.index, state: 'SOURCE_SUBMITTED', txHash: h.srcTxHash };

  try {
    await ctx.assertChain(h.srcChainKey); // EVERY error stops all evidence reads: the receipt below is never read on an unverified chain
  } catch (e) {
    if (e instanceof NetworkMismatchError) throw e;
    return done('UNKNOWN', [{ ...src, state: 'UNKNOWN', reason: chainUnverifiedReason(e) }]);
  }
  let rec: RawReceipt | null;
  try {
    rec = (await withBackoff(() => ctx.rpc(h.srcChainKey).send('eth_getTransactionReceipt', [h.srcTxHash]), backoffOf(ctx))) as RawReceipt | null;
  } catch (e) {
    if (e instanceof NetworkMismatchError) throw e;
    return done('UNKNOWN', [{ ...src, state: 'UNKNOWN', reason: `${e instanceof RpcTimeoutError ? 'timeout: ' : ''}source receipt unavailable: ${(e as Error).message}` }]);
  }
  if (!rec) return done('SOURCE_SUBMITTED', [src]);
  if (BigInt(rec.status) !== 1n) return done('SOURCE_FAILED', [{ ...src, state: 'SOURCE_FAILED' }]);

  const hop1 = await evalHop(ctx, h, 1);
  if (hop1.state !== 'FORWARDED') return done(hop1.state, [hop1]);
  h.hop2 = { ...h.hop2!, index: hop1.hop2!.index };
  const hop2 = await evalHop(ctx, h, 2);
  const state: TransferState = hop2.state === 'IN_FLIGHT' ? 'FORWARDED' : hop2.state;
  return done(state, [hop1, hop2]);
}

export async function track(ctx0: Ctx, handle: TransferHandle, o: { timeoutMs?: number; pollMs?: number; onUpdate?: (r: TrackResult) => void } = {}): Promise<TrackResult> {
  const timeout = o.timeoutMs ?? 10 * 60_000;
  const poll = o.pollMs ?? 5_000;
  const start = ctx0.now();
  const deadline = start + timeout;
  // every RPC call of this track() is bounded by min(callTimeoutMs, time left): the call returns within timeoutMs + epsilon even on a hung provider
  const ctx = ctx0.withDeadline(deadline);
  let h = handle;
  let last: TrackResult | undefined; // the last established (non-terminal) state
  for (;;) {
    const r = await trackOnce(ctx, h);
    h = r.handle;
    // evidence calls timed out AFTER a state was established: report that state, not a clobbering UNKNOWN
    if (last && r.state === 'UNKNOWN' && r.timedOut) { const out = { ...last, handle: r.handle, timedOut: true }; o.onUpdate?.(out); return out; }
    o.onUpdate?.(r);
    if (r.terminal) return r;
    last = r;
    const left = deadline - ctx.now();
    if (left <= 0) return { ...r, timedOut: true };
    await ctx.sleep(Math.min(poll, left));
    if (ctx.now() >= deadline) return { ...r, timedOut: true }; // no extra pass with a zero budget
  }
}

/** Optional, best-effort HyperCore balance check via the public info API (HyperCore credit is asynchronous). */
export async function checkCoreBalance(ctx: Ctx, coreRecipient: string, fetchImpl: typeof fetch = fetch): Promise<{ ok: boolean; balances?: unknown; error?: string }> {
  const api = ctx.net.chains.hyperevm.coreInfoApi;
  if (!api) return { ok: false, error: `HyperCore info API is not configured for this network (${ctx.net.chains.hyperevm.todo.coreInfoApi ?? 'null'})` };
  try {
    const res = await fetchImpl(api, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ type: 'spotClearinghouseState', user: coreRecipient }) });
    if (!res.ok) return { ok: false, error: `HTTP ${res.status}` };
    return { ok: true, balances: await res.json() };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

void fromHexQty; void hyperTokenI;
