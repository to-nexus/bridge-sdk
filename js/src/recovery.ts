import { getAddress } from 'ethers';
import { backoffOf, type Ctx } from './ctx.js';
import { buildHandle, linkSwapTransfer, matchesBridgeIntent, matchesSwapIntent, parseInitiated, quoteHandleInput, TOPIC_BRIDGE_INITIATED, TOPIC_SWAP_BRIDGE, type RawReceipt } from './handle.js';
import { AmbiguousSourceError, NetworkMismatchError } from './errors.js';
import { chainKeyById } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { sbrI } from './quote.js';
import { fromHexQty, withBackoff } from './rpc.js';
import { hex32, headBlock, scanLogs } from './scan.js';
import type { Checkpoint, FindSourceTransferResult, MatchSpec, ReplacementVerdict, SubmittedIntent, TransferEvidence } from './types.js';

/**
 * The source chain key of an intent, verified against THIS client's configuration BEFORE any read: the intent's chain id must be a configured chain of
 * this network and the checkpoint must agree. A mismatch is a NetworkMismatchError (no verdict, no evidence read).
 */
function sourceKeyOf(ctx: Ctx, intent: SubmittedIntent, checkpoint: Checkpoint): ChainKey {
  const key = chainKeyById(ctx.net, intent.chainId);
  if (!key) throw new NetworkMismatchError('intent.chainId', 'a configured chain of this network', intent.chainId);
  if (checkpoint.chainId !== intent.chainId) throw new NetworkMismatchError('checkpoint.chainId', intent.chainId, checkpoint.chainId);
  return key;
}

function handleInputFor(intent: SubmittedIntent, receipt: RawReceipt, checkpoint: Checkpoint) {
  return quoteHandleInput(intent.adoptedQuote, receipt, checkpoint.destBlocks);
}

/** the CONFIGURED SwapBridgeRouter (cross only) and source bridge of a chain: the only emitters whose logs can be attributed */
function linkCfg(ctx: Ctx, key: ChainKey): { router: string | null; bridge: string } {
  const c = ctx.net.chains[key];
  const router = key === 'cross' && ctx.net.chains.cross.swap?.swapBridgeRouter ? getAddress(ctx.net.chains.cross.swap.swapBridgeRouter) : null;
  return { router, bridge: getAddress(c.bridge!) };
}

const getReceipt = (ctx: Ctx, key: any, hash: string) =>
  withBackoff(() => ctx.rpc(key).send('eth_getTransactionReceipt', [hash]), backoffOf(ctx)) as Promise<RawReceipt | null>;

/** Search the source chain for the transfer described by the adopted SubmittedIntent - works WITHOUT a tx hash. */
export async function findSourceTransfer(ctx: Ctx, intent: SubmittedIntent, checkpoint: Checkpoint): Promise<FindSourceTransferResult> {
  const key = sourceKeyOf(ctx, intent, checkpoint);
  await ctx.assertChain(key); // before ANY evidence read (logs, receipts, nonce)
  const head = await headBlock(ctx, key);
  const m = intent.match;
  const found: { txHash: string; logIndex: number }[] = [];

  if (m.kind === 'bridge') {
    const logs = await scanLogs(ctx, key, { address: checkpoint.srcBridge, topics: [TOPIC_BRIDGE_INITIATED, hex32(m.toChainID), null, hex32(intent.from)] }, checkpoint.blockBefore, head);
    for (const l of logs) {
      const e = parseInitiated(l);
      if (!e) continue;
      if (matchesBridgeIntent(m, e, intent.from)) found.push({ txHash: l.transactionHash, logIndex: l.logIndex });
    }
  } else {
    // X3: the bridge-level `from` is the ROUTER. Search SwapBridge(user, tokenIn, tokenOut), then STRUCTURALLY link it to the bridge event of the same tx
    // (linkSwapTransfer, intent-free) and compare the linked transfer with the intent (matchesSwapIntent: recipient, extraData hash, destination token, ...).
    const router = checkpoint.srcBridge;
    const cfg = linkCfg(ctx, key);
    const logs = await scanLogs(ctx, key, { address: router, topics: [TOPIC_SWAP_BRIDGE, hex32(intent.from), hex32(m.tokenIn), hex32(m.tokenOut)] }, checkpoint.blockBefore, head);
    for (const l of logs) {
      const rec = await getReceipt(ctx, key, l.transactionHash);
      if (!rec) continue;
      const t = linkSwapTransfer(rec, cfg).linked.find((x) => x.swapLogIndex === Number(l.logIndex));
      if (t && matchesSwapIntent(t, m, intent.from, intent.adoptedQuote.limits.amountOutMinimum)) found.push({ txHash: l.transactionHash, logIndex: l.logIndex });
    }
  }

  const minedNonce = Number(fromHexQty(await ctx.rpc(key).send('eth_getTransactionCount', [intent.from, 'latest'])));
  const nonceInfo = { minedNonce, pendingNonceBefore: checkpoint.pendingNonceBefore };
  if (found.length === 0) return { result: { status: 'NOT_FOUND_YET' }, nonceInfo };
  if (found.length > 1) return { result: { status: 'AMBIGUOUS', candidates: found }, nonceInfo };
  const rec = await getReceipt(ctx, key, found[0].txHash);
  if (!rec) return { result: { status: 'NOT_FOUND_YET' }, nonceInfo };
  const handle = buildHandle(ctx, { ...handleInputFor(intent, rec, checkpoint), initiateIndex: undefined });
  return { result: { status: 'FOUND', handle, txHash: found[0].txHash }, nonceInfo };
}

/**
 * Mode L replacement analysis. A new tx is allowed ONLY when the replacement's receipt proves it did not
 * perform a bridge transfer. A differing hash alone is never evidence; nonce advancement alone is never evidence.
 */
export async function analyzeReplacement(
  ctx: Ctx,
  p: { intent: SubmittedIntent; checkpoint: Checkpoint; original: { hash: string; nonce: number }; replacementHash?: string; maxScanBlocks?: number },
): Promise<ReplacementVerdict> {
  const { intent, checkpoint, original } = p;
  const key = sourceKeyOf(ctx, intent, checkpoint);
  await ctx.assertChain(key); // before ANY evidence read: an RPC on another chain can hold a same-account, same-nonce "cancel" tx that proves nothing here
  const rpc = ctx.rpc(key);
  const origRec = (await rpc.send('eth_getTransactionReceipt', [original.hash])) as RawReceipt | null;
  if (origRec) return { verdict: 'ORIGINAL_MINED', txHash: original.hash };
  const mined = Number(fromHexQty(await rpc.send('eth_getTransactionCount', [intent.from, 'latest'])));
  if (mined <= original.nonce) return { verdict: 'ORIGINAL_STILL_POSSIBLE', reason: 'the nonce is not consumed yet: the original may still be mined' };

  let repHash = p.replacementHash;
  if (!repHash) {
    const head = await headBlock(ctx, key);
    const from = checkpoint.blockBefore;
    const to = Math.min(head, from + (p.maxScanBlocks ?? 500));
    for (let n = from; n <= to && !repHash; n++) {
      const blk = await rpc.send('eth_getBlockByNumber', ['0x' + n.toString(16), true]);
      for (const t of blk?.transactions ?? []) {
        if (String(t.from).toLowerCase() === intent.from.toLowerCase() && Number(BigInt(t.nonce)) === original.nonce) { repHash = t.hash; break; }
      }
    }
  }
  if (!repHash) return { verdict: 'ORIGINAL_STILL_POSSIBLE', reason: 'could not locate the tx mined at that nonce; pass replacementHash (wallet history / explorer)' };
  if (repHash.toLowerCase() === original.hash.toLowerCase()) return { verdict: 'ORIGINAL_MINED', txHash: repHash };
  const tx = await rpc.send('eth_getTransactionByHash', [repHash]);
  const rec = tx ? ((await rpc.send('eth_getTransactionReceipt', [repHash])) as RawReceipt | null) : null;
  return settleReplacement(ctx, key, p, repHash, tx, rec);
}

/**
 * THE ONLY FUNCTION THAT CAN RETURN `NEW_TX_ALLOWED` (grep: the literal appears only inside this function). It asserts, in this one place, every fact that
 * makes a replacement receipt valid evidence that the original can never be mined as a transfer:
 *   1. the chain of the RPC is verified (ctx.assertChain: the cached eth_chainId check),
 *   2. the replacement tx carries an EIP-155 chain id equal to the intent's source chain id (a pre-EIP-155 tx has none and is not accepted),
 *   3. its sender is the intent's sender and its nonce is the original's nonce,
 *   4. its receipt is known and EITHER failed (status 0) OR holds NO transfer-shaped log AT ALL: not one BridgeInitiated and not one SwapBridge log,
 *      from ANY emitter, whatever the kind of the original intent. Any transfer-shaped evidence blocks a new transaction (the conservative direction).
 * A successful receipt WITH such logs is classified (M = user transfers that fully match the intent, D = user transfers that do not, U = evidence that
 * cannot be attributed: unknown emitter, from/user that is not the sender and not explained by an X3 link, an unlinked SwapBridge). First rule that applies:
 *   U non-empty -> REPLACED_UNVERIFIED | |M| > 1 -> AmbiguousSourceError | |M| = 1 and D empty -> FOUND | |M| = 1 and D non-empty -> REPLACED_UNVERIFIED
 *   | M empty and D non-empty -> REPLACED_BY_DIFFERENT_TRANSFER | all empty -> (step 4) NEW_TX_ALLOWED.
 * Anything else is `ORIGINAL_STILL_POSSIBLE` (never safe).
 */
async function settleReplacement(
  ctx: Ctx, key: ChainKey,
  p: { intent: SubmittedIntent; checkpoint: Checkpoint; original: { hash: string; nonce: number } },
  repHash: string, tx: any, rec: RawReceipt | null,
): Promise<ReplacementVerdict> {
  const { intent, checkpoint, original } = p;
  await ctx.assertChain(key); // fact 1
  if (!tx || String(tx.from).toLowerCase() !== intent.from.toLowerCase() || Number(BigInt(tx.nonce)) !== original.nonce) { // fact 3
    return { verdict: 'ORIGINAL_STILL_POSSIBLE', reason: 'the given hash is not a tx of this sender at the original nonce' };
  }
  if (tx.chainId === undefined || tx.chainId === null || BigInt(tx.chainId) !== BigInt(intent.chainId)) { // fact 2
    return { verdict: 'ORIGINAL_STILL_POSSIBLE', reason: 'the replacement tx has no EIP-155 chain id or carries another chain id: it is not evidence for this chain' };
  }
  if (!rec) return { verdict: 'ORIGINAL_STILL_POSSIBLE', reason: 'the replacement has no receipt yet' };
  if (BigInt(rec.status) !== 1n) return { verdict: 'NEW_TX_ALLOWED', reason: 'replacement-failed', replacementHash: repHash }; // fact 4

  // 1. collect EVERY transfer-shaped log of the receipt, from any emitter, whatever the intent kind
  const isT = (l: RawReceipt['logs'][number], topic: string) => l.topics[0]?.toLowerCase() === topic.toLowerCase();
  const biLogs = rec.logs.filter((l) => isT(l, TOPIC_BRIDGE_INITIATED));
  const sbLogs = rec.logs.filter((l) => isT(l, TOPIC_SWAP_BRIDGE));
  if (biLogs.length === 0 && sbLogs.length === 0) return { verdict: 'NEW_TX_ALLOWED', reason: 'replacement-not-a-transfer', replacementHash: repHash }; // fact 4

  // 3. classify each collected log
  const cfg = linkCfg(ctx, key);
  const m: MatchSpec = intent.match;
  const minOut = intent.adoptedQuote.limits.amountOutMinimum;
  const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();
  const link = linkSwapTransfer(rec, cfg);
  const M: (TransferEvidence & { index: bigint; toChainID: bigint; biLogIndex: number })[] = [];
  const D: TransferEvidence[] = [];
  const U: TransferEvidence[] = [];
  for (const t of link.linked) {
    const ev: TransferEvidence = { kind: 'swap', emitter: t.swapEmitter, logIndex: t.swapLogIndex };
    if (!same(t.user, intent.from)) { U.push({ ...ev, detail: 'SwapBridge user is not the sender' }); continue; }
    if (m.kind === 'swap' && matchesSwapIntent(t, m, intent.from, minOut)) M.push({ ...ev, index: t.index, toChainID: t.toChainID, biLogIndex: t.bi.logIndex });
    else D.push({ ...ev, detail: 'a linked X3 transfer of the sender that does not match the intent' });
  }
  for (const u of link.unlinkedSwapLogs) U.push({ kind: 'swap', emitter: u.emitter, logIndex: u.logIndex, detail: 'SwapBridge log from an unconfigured emitter or without a consistent BridgeInitiated link' });
  for (const l of biLogs) {
    const idx = Number(l.logIndex);
    const configured = same(l.address, cfg.bridge);
    if (configured && link.consumedInitiated.has(idx)) continue; // consumed by an X3 link: not a separate transfer
    const e = configured ? parseInitiated({ ...l, logIndex: l.logIndex }) : null;
    if (e && same(e.from, intent.from)) {
      if (m.kind === 'bridge' && matchesBridgeIntent(m, e, intent.from)) M.push({ kind: 'bridge', emitter: e.address, logIndex: idx, index: e.index, toChainID: e.toChainID, biLogIndex: idx });
      else D.push({ kind: 'bridge', emitter: e.address, logIndex: idx, detail: 'a direct transfer of the sender that does not match the intent' });
    } else U.push({ kind: 'bridge', emitter: l.address, logIndex: idx, detail: configured ? 'BridgeInitiated whose from is not the sender and is not explained by an X3 link' : 'BridgeInitiated from an unconfigured emitter' });
  }
  const strip = (x: TransferEvidence): TransferEvidence => ({ kind: x.kind, emitter: x.emitter, logIndex: x.logIndex, ...(x.detail ? { detail: x.detail } : {}) });
  // 4. the verdict: the first rule that applies
  if (U.length > 0) return { verdict: 'REPLACED_UNVERIFIED', replacementHash: repHash, matched: M.map(strip), different: D, unattributed: U };
  if (M.length > 1) throw new AmbiguousSourceError('several transfers of the sender in the replacement receipt match the intent; refusing to pick one', M.map((x) => ({ toChainID: x.toChainID, index: x.index, logIndex: x.biLogIndex })));
  if (M.length === 1 && D.length === 0) {
    const handle = buildHandle(ctx, { ...handleInputFor(intent, rec, checkpoint), logIndex: M[0].biLogIndex });
    return { verdict: 'FOUND', handle, txHash: repHash };
  }
  if (M.length === 1) return { verdict: 'REPLACED_UNVERIFIED', replacementHash: repHash, matched: M.map(strip), different: D, unattributed: [] };
  if (D.length > 0) return { verdict: 'REPLACED_BY_DIFFERENT_TRANSFER', replacementHash: repHash, different: D };
  return { verdict: 'ORIGINAL_STILL_POSSIBLE', reason: 'internal: transfer-shaped logs were collected but none could be classified; treated as not safe' }; // unreachable in practice; never a new transaction
}

void getAddress;
