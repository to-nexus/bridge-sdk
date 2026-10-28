import { BaseWallet, MaxUint256, NonceManager, Transaction, getAddress, keccak256 } from 'ethers';
import { assertQuoteBelongs } from './binding.js';
import { snapshot } from './freeze.js';
import { blockingPreflight } from './preflight.js';
import { PreflightError } from './errors.js';
import type { Ctx } from './ctx.js';
import { buildHandle, quoteHandleInput, type RawReceipt } from './handle.js';
import {
  ApproveFailedError, BroadcastOutcomeUnknownError, NetworkMismatchError, QuoteExpiredError, SenderMismatchError,
  RpcTimeoutError, SignerMisbehaviorError, SignerRejectedError, SigningError, SimulationRevertError, SourceTxFailedError, WaitTimeoutError, decodeBridgeError, extractRevertData,
} from './errors.js';
import type { ChainKey } from './networks-types.js';
import { buildQuote, erc20I, frozenFrom } from './quote.js';
import { ethCall, fromHexQty, readContract, toHexQty, withTimeout } from './rpc.js';
import { openQuote } from './seal.js';
import type { Checkpoint, Quote, SendOptions, SentTx, SignedInfo, SubmittedIntent, TransferHandle } from './types.js';

export interface SignerLike {
  getAddress(): Promise<string>;
  provider?: { getNetwork(): Promise<{ chainId: bigint | number }> } | null;
  signTransaction?(tx: any): Promise<string>;
  sendTransaction?(tx: any): Promise<{ hash: string }>;
}

/** BSC testnet needs >= 1 gwei or the tx sits in the mempool (see GUIDE FAQ). */
const MIN_GAS_PRICE_WEI: Record<number, bigint> = { 97: 1_000_000_000n };

/** Mode is decided by capability only, BEFORE any signing. There is no error-driven fallback. */
export function pickMode(signer: SignerLike, opts: SendOptions = {}): 'L' | 'W' {
  const isLocal = (s: any): boolean => {
    if (s instanceof BaseWallet) return true; // Wallet, HDNodeWallet
    if (s instanceof NonceManager) return isLocal((s as any).signer);
    return false;
  };
  if (opts.signerMode === 'local') {
    if (typeof signer.signTransaction !== 'function') throw new TypeError('signerMode "local" requires a signer that implements signTransaction');
    return 'L';
  }
  return isLocal(signer) ? 'L' : 'W';
}

export async function assertSigner(ctx: Ctx, signer: SignerLike, q: Quote, mode: 'L' | 'W'): Promise<void> {
  // network reads (NOT wallet prompts) are time-bounded like every other RPC call: a hung signer provider => RpcTimeoutError, nothing approved / signed / sent
  const callTimeout = ctx.opts.callTimeoutMs ?? 15_000;
  const addr = getAddress(await withTimeout(Promise.resolve().then(() => signer.getAddress()), callTimeout, 'signer.getAddress'));
  if (addr.toLowerCase() !== q.sender.toLowerCase()) throw new SenderMismatchError(q.sender, addr);
  if (signer.provider) {
    const prov = signer.provider;
    const n = await withTimeout(Promise.resolve().then(() => prov.getNetwork()), callTimeout, 'signer.provider.getNetwork');
    if (BigInt(n.chainId) !== BigInt(q.sourceChainId)) throw new NetworkMismatchError('signer provider', q.sourceChainId, String(n.chainId));
  } else if (mode === 'W') {
    throw new NetworkMismatchError('signer provider', q.sourceChainId, 'unknown (the signer has no provider to verify)');
  }
  await ctx.assertChain(q.sourceChainKey);
}

const isRejection = (e: any): boolean => e?.code === 4001 || e?.code === 'ACTION_REJECTED' || e?.info?.error?.code === 4001 || e?.error?.code === 4001;

/** Wallet prompts (Mode W) are NOT timed out by default: the user may take time and a timeout cannot prove the wallet did not send. Opt in with walletTimeoutMs. */
function withWalletTimeout<T>(p: Promise<T>, ms: number | undefined): Promise<T> {
  if (ms === undefined) return p;
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`wallet did not answer within ${ms} ms`)), ms);
    p.then((v) => { clearTimeout(t); resolve(v); }, (e) => { clearTimeout(t); reject(e); });
  });
}

/**
 * Mode L: the signed tx is parsed and verified BEFORE any broadcast: replay-protected (EIP-155 / typed: chainId != 0), chainId == the quote's,
 * recovered sender == the quote's sender, and every field equals the unsigned request. Violation => SignerMisbehaviorError, nothing broadcast.
 */
export function verifySignedTx(raw: string, want: { chainId: number; from: string; nonce: number; gasPrice: bigint; gasLimit: bigint; to: string; data: string; value: bigint }): void {
  let t: Transaction;
  try { t = Transaction.from(raw); } catch (e) { throw new SignerMisbehaviorError('raw', `unparseable signed transaction (${(e as Error).message})`); }
  if (t.chainId === 0n) throw new SignerMisbehaviorError('protected', 'the signature is not replay-protected (pre-EIP-155 legacy)');
  if (t.chainId !== BigInt(want.chainId)) throw new SignerMisbehaviorError('chainId', `signed for chain ${t.chainId}, expected ${want.chainId}`);
  let from: string;
  try { from = t.from ?? ''; } catch { from = ''; }
  if (!from || from.toLowerCase() !== want.from.toLowerCase()) throw new SignerMisbehaviorError('from', `recovered sender ${from || '(none)'} != ${want.from}`);
  const eq = (field: string, a: unknown, b: unknown) => { if (a !== b) throw new SignerMisbehaviorError(field, `signed ${String(a)} != requested ${String(b)}`); };
  eq('to', (t.to ?? '').toLowerCase(), want.to.toLowerCase());
  eq('value', t.value, want.value);
  eq('data', t.data.toLowerCase(), want.data.toLowerCase());
  eq('nonce', t.nonce, want.nonce);
  eq('gasLimit', t.gasLimit, want.gasLimit);
  eq('gasPrice', t.gasPrice, want.gasPrice);
}

async function gasPriceFor(ctx: Ctx, key: ChainKey, chainId: number): Promise<bigint> {
  const gp = fromHexQty(await ctx.rpc(key).send('eth_gasPrice', []));
  const min = MIN_GAS_PRICE_WEI[chainId] ?? 0n;
  return gp > min ? gp : min;
}

export async function waitReceipt(ctx: Ctx, key: ChainKey, hash: string, o: { timeoutMs?: number; pollMs?: number } = {}): Promise<RawReceipt> {
  const timeout = o.timeoutMs ?? 10 * 60_000;
  const poll = o.pollMs ?? 3_000;
  const start = ctx.now();
  for (;;) {
    const r = await ctx.rpc(key).send('eth_getTransactionReceipt', [hash]);
    if (r) return r as RawReceipt;
    if (ctx.now() - start >= timeout) throw new WaitTimeoutError(hash);
    await ctx.sleep(poll);
  }
}

interface BuiltTx { to: string; data: string; value: bigint }

/** Sign+send a non-bridge tx (approve). Returns the hash once known; failures here mean nothing of value was at risk. */
async function submitSimple(ctx: Ctx, signer: SignerLike, mode: 'L' | 'W', key: ChainKey, chainId: number, from: string, tx: BuiltTx, walletTimeoutMs: number | undefined): Promise<string> {
  if (mode === 'L') {
    const nonce = Number(fromHexQty(await ctx.rpc(key).send('eth_getTransactionCount', [from, 'pending'])));
    const gasPrice = await gasPriceFor(ctx, key, chainId);
    const est = fromHexQty(await ctx.rpc(key).send('eth_estimateGas', [{ from, to: tx.to, data: tx.data, value: toHexQty(tx.value) }]));
    let raw: string;
    const gasLimit = (est * 12n) / 10n;
    try {
      raw = await signer.signTransaction!({ type: 0, chainId, nonce, gasPrice, gasLimit, to: tx.to, data: tx.data, value: tx.value });
    } catch (e) { throw new SigningError(e); }
    verifySignedTx(raw, { chainId, from, nonce, gasPrice, gasLimit, to: tx.to, data: tx.data, value: tx.value });
    try {
      return (await ctx.rpc(key).send('eth_sendRawTransaction', [raw])) as string;
    } catch (e) {
      // the request left and no answer came: the approve may or may not be on chain
      if (e instanceof RpcTimeoutError) throw new ApproveFailedError(`approve broadcast timed out; it may or may not be on chain (hash ${keccak256(raw)}): re-check the allowance before retrying`, keccak256(raw), e);
      throw e;
    }
  }
  try {
    const r = await withWalletTimeout(signer.sendTransaction!({ to: tx.to, data: tx.data, value: tx.value, chainId }), walletTimeoutMs);
    return r.hash;
  } catch (e) {
    if (isRejection(e)) throw new SignerRejectedError(e);
    throw e;
  }
}

export async function ensureAllowance(ctx: Ctx, signer: SignerLike, quoteIn: Quote, opts: SendOptions = {}): Promise<{ txs: string[] }> {
  const q = openQuote(quoteIn); // sealed quotes only; everything below uses the restored private copy
  assertQuoteBelongs(ctx, q); // client binding check against this client's config, before anything else (also for a quote that needs no approval)
  if (!q.approval) return { txs: [] };
  const mode = pickMode(signer, opts);
  await assertSigner(ctx, signer, q, mode);
  return doApprove(ctx, signer, q, opts, mode);
}

async function doApprove(ctx: Ctx, signer: SignerLike, q: Quote, opts: SendOptions, mode: 'L' | 'W'): Promise<{ txs: string[] }> {
  if (!q.approval) return { txs: [] }; // native source (R2/R3/X2) and native-in X3: nothing to approve
  const a = q.approval;
  const key = q.sourceChainKey;
  const txs: string[] = [];
  const cur = await readContract<bigint>(ctx.rpc(key), erc20I, a.token, 'allowance', [q.sender, a.spender]);
  if (cur >= a.amount) return { txs };
  const wt = opts.walletTimeoutMs;
  const step = async (amount: bigint, label: string) => {
    let hash: string;
    try {
      hash = await submitSimple(ctx, signer, mode, key, q.sourceChainId, q.sender, { to: a.token, data: erc20I.encodeFunctionData('approve', [a.spender, amount]), value: 0n }, wt);
    } catch (e) {
      if (e instanceof SignerRejectedError || e instanceof SigningError || e instanceof SignerMisbehaviorError || e instanceof ApproveFailedError) throw e;
      throw new ApproveFailedError(`${label} could not be submitted: ${(e as Error).message}`, undefined, e);
    }
    txs.push(hash);
    const r = await waitReceipt(ctx, key, hash);
    if (BigInt(r.status) !== 1n) throw new ApproveFailedError(`${label} transaction ${hash} reverted (status 0)`, hash);
  };
  const zf = opts.zeroFirst ?? 'auto';
  if (zf === 'always' || (zf === 'auto' && cur > 0n)) await step(0n, 'approve(0)');
  await step(opts.approveAmount === 'max' ? MaxUint256 : a.amount, 'approve');
  return { txs };
}

async function recordDestBlocks(ctx: Ctx, q: Quote): Promise<Partial<Record<ChainKey, number>>> {
  const keys = new Set<ChainKey>([q.details.destChainKey]);
  if (q.details.hubChainKey) keys.add(q.details.hubChainKey);
  if (q.kind === 'single' || q.kind === 'x3') keys.add(q.details.destChainKey);
  if (q.kind === 'x1') keys.add('one');
  const out: Partial<Record<ChainKey, number>> = {};
  for (const k of keys) {
    if (k === q.sourceChainKey && q.kind !== 'x3' && q.kind !== 'x1') continue;
    try { out[k] = Number(fromHexQty(await ctx.rpc(k).send('eth_blockNumber', []))); } catch { /* best effort: tracking falls back to lookback */ }
  }
  return out;
}

export async function send(ctx: Ctx, signer: SignerLike, quoteIn: Quote, opts: SendOptions = {}): Promise<SentTx> {
  // verify the seal FIRST (synchronously, nothing awaited): shape -> version/sdk -> digest -> parse+schema -> public fields vs payload -> restore.
  // From here on ONLY q0 (a new private deep-frozen object restored from the payload) is used; the caller's object is never read again.
  const q0 = openQuote(quoteIn);
  assertQuoteBelongs(ctx, q0); // (0) the quote must belong to THIS client (network + config-owned addresses), before anything else
  const age = ctx.now() - q0.createdAt;
  if (age > q0.ttlMs) throw new QuoteExpiredError(age, q0.ttlMs);
  const mode = pickMode(signer, opts); // capability only; decided before any signing
  await assertSigner(ctx, signer, q0, mode); // (1) network + sender, before any approve
  await blockingPreflight(ctx, q0, 'pre-approve'); // (1b) required preflight (blocking subset); PreflightError => nothing approved / signed / sent

  await doApprove(ctx, signer, q0, opts, mode); // (2) approve if needed, receipt status==1
  let adopted: Quote;
  try {
    adopted = await buildQuote(ctx, q0.request, frozenFrom(q0)); // (3) re-quote under immutable limits (throws RequoteRequiredError)
  } catch (e) {
    if (e instanceof PreflightError && !e.stage) throw new PreflightError(e.issues, 'requote'); // an error while building the adopted quote (e.g. X3 quote status)
    throw e;
  }
  assertQuoteBelongs(ctx, adopted);
  if (adopted.approval && (!q0.approval || adopted.approval.amount > q0.approval.amount)) await doApprove(ctx, signer, adopted, opts, mode);
  await blockingPreflight(ctx, adopted, 'post-approve'); // (3b) same checks on the adopted quote, allowance now blocking

  const from = adopted.sender;
  const key = adopted.sourceChainKey;
  const rpc = ctx.rpc(key);
  if (!opts.skipSimulation) { // (4) eth_call of the exact final tx
    try {
      await ethCall(rpc, { from, to: adopted.tx.to, data: adopted.tx.data, value: adopted.tx.value });
    } catch (e) {
      if (e instanceof RpcTimeoutError) throw e;
      const rd = extractRevertData(e);
      throw new SimulationRevertError(decodeBridgeError(rd), rd, e);
    }
  }

  const checkpoint: Checkpoint = snapshot({
    from, chainId: adopted.sourceChainId,
    pendingNonceBefore: Number(fromHexQty(await rpc.send('eth_getTransactionCount', [from, 'pending']))),
    blockBefore: Number(fromHexQty(await rpc.send('eth_blockNumber', []))),
    srcBridge: adopted.sourceContract,
    destBlocks: await recordDestBlocks(ctx, adopted),
  });
  const intent: SubmittedIntent = snapshot({
    chainId: adopted.sourceChainId, from, to: adopted.tx.to, data: adopted.tx.data, value: adopted.tx.value,
    quoteId: adopted.id, adoptedQuote: adopted, match: adopted.match,
  });
  // callbacks always receive fresh frozen clones: mutating one can never change what the SDK signs or reports
  if (opts.onSubmitting) await opts.onSubmitting(snapshot(intent), snapshot(checkpoint)); // a throw aborts: nothing is sent

  let hash: string;
  let nonce: number | undefined;
  let raw: string | undefined;
  if (mode === 'L') {
    const gasPrice = await gasPriceFor(ctx, key, adopted.sourceChainId);
    let est: bigint;
    try {
      est = fromHexQty(await rpc.send('eth_estimateGas', [{ from, to: adopted.tx.to, data: adopted.tx.data, value: toHexQty(adopted.tx.value) }]));
    } catch (e) {
      if (e instanceof RpcTimeoutError) throw e;
      const rd = extractRevertData(e);
      throw new SimulationRevertError(decodeBridgeError(rd), rd, e);
    }
    nonce = checkpoint.pendingNonceBefore;
    const gasLimit = (est * 12n) / 10n;
    try {
      raw = await signer.signTransaction!({ type: 0, chainId: adopted.sourceChainId, nonce, gasPrice, gasLimit, to: adopted.tx.to, data: adopted.tx.data, value: adopted.tx.value });
    } catch (e) { throw new SigningError(e); }
    verifySignedTx(raw, { chainId: adopted.sourceChainId, from, nonce, gasPrice, gasLimit, to: adopted.tx.to, data: adopted.tx.data, value: adopted.tx.value });
    hash = keccak256(raw);
    const info: SignedInfo = { raw, hash, nonce };
    if (opts.onSigned) await opts.onSigned(snapshot(info), snapshot(intent)); // awaited BEFORE broadcast; a throw aborts (nothing is broadcast)
    try {
      const h = (await rpc.send('eth_sendRawTransaction', [raw])) as string;
      if (h && h.toLowerCase() !== hash.toLowerCase()) hash = h;
    } catch (e) {
      // includes RpcTimeoutError: after the request left, the outcome is unknown and the hash is known. NEVER resend.
      if (!/already known|known transaction|already imported/i.test(String((e as Error).message))) throw new BroadcastOutcomeUnknownError('L', intent, checkpoint, { hash, nonce, raw }, e);
    }
  } else {
    try {
      const r = await withWalletTimeout(signer.sendTransaction!({ to: adopted.tx.to, data: adopted.tx.data, value: adopted.tx.value, chainId: adopted.sourceChainId }), opts.walletTimeoutMs);
      hash = r.hash;
    } catch (e) {
      if (isRejection(e)) throw new SignerRejectedError(e);
      throw new BroadcastOutcomeUnknownError('W', intent, checkpoint, {}, e);
    }
  }

  // The SentTx exists BEFORE the callback runs: once broadcast, the call must never look like a pre-send failure (a retry = duplicate send).
  const sent: SentTx = {
    hash, chainId: adopted.sourceChainId, mode, ...(nonce !== undefined ? { nonce } : {}), ...(raw ? { raw } : {}), checkpoint, intent, quote: adopted,
    wait: (o) => waitForHandle(ctx, sent, o),
  };
  if (opts.onBroadcast) {
    try { await opts.onBroadcast(hash); } catch (e) {
      const err = e as { name?: string; message?: string };
      sent.callbackError = { name: String(err?.name ?? 'Error'), message: String(err?.message ?? e) };
    }
  }
  return sent;
}

export async function waitForHandle(ctx: Ctx, sent: SentTx, o: { timeoutMs?: number; pollMs?: number } = {}): Promise<TransferHandle> {
  await ctx.assertChain(sent.quote.sourceChainKey); // the chain of the RPC is verified before ANY evidence read (also for a SentTx restored after a restart)
  const r = await waitReceipt(ctx, sent.quote.sourceChainKey, sent.hash, o);
  if (BigInt(r.status) !== 1n) throw new SourceTxFailedError(sent.hash);
  return buildHandle(ctx, quoteHandleInput(sent.quote, r, sent.checkpoint.destBlocks));
}

/** Mode L: re-broadcast the SAME signed tx. Idempotent - it cannot create a second transfer. */
export async function rebroadcast(ctx: Ctx, key: ChainKey, raw: string): Promise<string> {
  await ctx.assertChain(key); // never push a raw tx (or read its receipt) through an RPC whose chain is unverified
  try {
    return (await ctx.rpc(key).send('eth_sendRawTransaction', [raw])) as string;
  } catch (e) {
    const hash = keccak256(raw);
    if (/already known|known transaction|already imported/i.test(String((e as Error).message))) return hash;
    // e.g. "nonce too low": only treat as success if THIS tx is actually on chain
    const rec = await ctx.rpc(key).send('eth_getTransactionReceipt', [hash]).catch(() => null);
    if (rec) return hash;
    throw e;
  }
}

/** eth_call of the exact tx the SDK would send. Never sends anything. */
export async function simulate(ctx: Ctx, q: Quote): Promise<{ ok: true } | { ok: false; decoded: import('./errors.js').DecodedError | null; data: string | null; message: string }> {
  try {
    await ethCall(ctx.rpc(q.sourceChainKey), { from: q.sender, to: q.tx.to, data: q.tx.data, value: q.tx.value });
    return { ok: true };
  } catch (e) {
    const rd = extractRevertData(e);
    return { ok: false, decoded: decodeBridgeError(rd), data: rd, message: String((e as Error).message) };
  }
}
