// Classification of a replacement transaction at the same nonce: any transfer-shaped log blocks a new transaction, X3 matching is split into a structural
// link and an intent comparison (recipient, extraData hash, expected destination token).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Wallet, keccak256, toUtf8Bytes } from 'ethers';
import { AmbiguousSourceError, BridgeClient, LimitViolationError, Route, jsonParse, jsonStringify } from '../src/index.js';
import type { Quote } from '../src/index.js';
import { buildHandle, linkSwapTransfer, quoteHandleInput } from '../src/handle.js';
import type { RawReceipt } from '../src/handle.js';
import { canonicalJSON, openQuote } from '../src/seal.js';
import type { LogInput } from './fake.js';
import { E18, NATIVE, RECIPIENT, TOKEN_A, World, sbrI } from './world.js';

const OTHER = '0x5555555555555555555555555555555555555555';
const STRANGER = '0x00000000000000000000000000000000dEaD0001';

async function setupReplacement(x3: boolean) {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  const usdtx = c.cross.tokens.USDTx!.address;
  w.setBal('cross', TOKEN_A, wallet.address, 1000n * E18);
  w.setFee('cross', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  const router = c.cross.swap!.swapBridgeRouter!;
  w.setAllow('cross', TOKEN_A, wallet.address, router, 10n ** 30n);
  const client = new BridgeClient(w.options());
  const q = await client.quote(x3
    ? { route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' as const } }
    : { route: Route.ONE_TO_BSC, amount: 10n * E18, sender: wallet.address, recipient: RECIPIENT });
  w.chains.cross.sendMode = 'pending'; // accepted by the node, never mined: the nonce stays open for a replacement
  const sent = await client.send(wallet, q);
  w.chains.cross.sendMode = 'mine';
  return { w, c, wallet, client, sent, router, hub: c.cross.bridge!, usdtx, bscUSDT: c.bsc.tokens.USDT!.address, bscONE: c.bsc.tokens.ONE!.address };
}
type E = Awaited<ReturnType<typeof setupReplacement>>;

interface X3o { m?: any; user?: string; recipient?: string; toToken?: string; emitterRouter?: string; emitterBridge?: string; amountOut?: bigint; value?: bigint; nf?: bigint; ef?: bigint; extra?: string; badIndex?: boolean; noBI?: boolean }
/** [SwapBridge, BridgeInitiated(from = router)] that MATCH the sent quote unless an option overrides a field */
function x3Logs(e: E, o: X3o = {}): LogInput[] {
  const m: any = o.m ?? e.sent.quote.match;
  const minOut: bigint = e.sent.quote.limits.amountOutMinimum ?? 40n * E18;
  const out = o.amountOut ?? minOut + E18, val = o.value ?? minOut, nf = o.nf ?? 0n, ef = o.ef ?? 0n;
  const bi = e.w.initiatedLog('cross', o.emitterBridge ?? e.hub, Number(m.toChainID), m.tokenOut, o.toToken ?? m.toToken, e.router, o.recipient ?? m.recipient, val, nf, ef, o.extra ?? '0x');
  const idx = e.w.initIndex.get(`cross:${Number(m.toChainID)}`)! - 1n;
  const ev = sbrI.encodeEventLog('SwapBridge', [o.user ?? e.wallet.address, m.tokenIn, m.tokenOut, m.amountIn, out, m.toChainID, o.badIndex ? idx + 7n : idx, val, nf, ef]);
  const sb: LogInput = { address: o.emitterRouter ?? e.router, topics: ev.topics as string[], data: ev.data };
  return o.noBI ? [sb] : [sb, bi];
}
const direct = (e: E, from: string, rcpt: string, v: bigint, emitter?: string): LogInput => e.w.initiatedLog('cross', emitter ?? e.hub, e.c.bsc.chainId, NATIVE, e.bscONE, from, rcpt, v, 0n, 0n, '0x');

async function verdict(e: E, ...logs: LogInput[]): Promise<any> {
  const hash = e.w.chains.cross.mineSignedLogs({ from: e.wallet.address, nonce: e.sent.nonce!, to: e.router, logs });
  return e.client.analyzeReplacement({ intent: e.sent.intent, checkpoint: e.sent.checkpoint, original: { hash: e.sent.hash, nonce: e.sent.nonce! }, replacementHash: hash });
}
const receiptOf = (e: E, hash: string) => e.w.chains.cross.receipts.get(hash.toLowerCase()) as unknown as RawReceipt;

test('the kind of the ORIGINAL intent does not decide: X3 original + a direct R2 replacement, and a direct original + an X3 replacement => REPLACED_BY_DIFFERENT_TRANSFER, never NEW_TX_ALLOWED', async () => {
  const e = await setupReplacement(true);
  let v = await verdict(e, direct(e, e.wallet.address, OTHER, 10n * E18));
  assert.equal(v.verdict, 'REPLACED_BY_DIFFERENT_TRANSFER');
  const d = await setupReplacement(false);
  const xm = { tokenIn: TOKEN_A, tokenOut: d.usdtx, amountIn: 50n * E18, toChainID: d.c.bsc.chainId, toToken: d.bscUSDT, recipient: RECIPIENT };
  v = await verdict(d, ...x3Logs(d, { m: xm }));
  assert.equal(v.verdict, 'REPLACED_BY_DIFFERENT_TRANSFER');
});

test('unattributed transfer evidence is never a new tx: a BridgeInitiated from an unknown emitter, a SwapBridge from an unknown router => REPLACED_UNVERIFIED; a plain cancel => NEW_TX_ALLOWED', async () => {
  let e = await setupReplacement(true);
  let v = await verdict(e, e.w.initiatedLog('cross', STRANGER, e.c.bsc.chainId, NATIVE, e.bscONE, e.wallet.address, RECIPIENT, 10n * E18, 0n, 0n, '0x'));
  assert.equal(v.verdict, 'REPLACED_UNVERIFIED');
  assert.equal(v.unattributed.length, 1);
  e = await setupReplacement(true);
  v = await verdict(e, ...x3Logs(e, { emitterRouter: STRANGER }));
  assert.equal(v.verdict, 'REPLACED_UNVERIFIED');
  e = await setupReplacement(true);
  v = await verdict(e);
  assert.deepEqual([v.verdict, v.reason], ['NEW_TX_ALLOWED', 'replacement-not-a-transfer']);
});

test('combinations: match + unattributed => UNVERIFIED; two matches => AmbiguousSourceError; match + different => UNVERIFIED; different alone => DIFFERENT; match alone => FOUND', async () => {
  let e = await setupReplacement(true);
  const stray = e.w.initiatedLog('cross', STRANGER, e.c.bsc.chainId, NATIVE, e.bscONE, e.wallet.address, RECIPIENT, E18, 0n, 0n, '0x');
  let v = await verdict(e, ...x3Logs(e), stray);
  assert.equal(v.verdict, 'REPLACED_UNVERIFIED');
  assert.equal(v.matched.length, 1); assert.equal(v.unattributed.length, 1);
  e = await setupReplacement(true);
  await assert.rejects(() => verdict(e, ...x3Logs(e), ...x3Logs(e)), (err: any) => err instanceof AmbiguousSourceError && err.candidates.length === 2);
  e = await setupReplacement(true);
  v = await verdict(e, ...x3Logs(e), direct(e, e.wallet.address, OTHER, 3n * E18));
  assert.equal(v.verdict, 'REPLACED_UNVERIFIED');
  assert.deepEqual([v.matched.length, v.different.length, v.unattributed.length], [1, 1, 0]);
  e = await setupReplacement(true);
  v = await verdict(e, direct(e, e.wallet.address, OTHER, 3n * E18));
  assert.equal(v.verdict, 'REPLACED_BY_DIFFERENT_TRANSFER');
  e = await setupReplacement(true);
  assert.equal((await verdict(e, ...x3Logs(e))).verdict, 'FOUND');
});

test('recipient, linked extraData hash and destination token are intent fields: another value => not matched by findSourceTransfer / settleReplacement / quote-based buildHandle; handleFromTx still restores the receipt', async () => {
  const cases: Record<string, X3o> = { 'another recipient': { recipient: OTHER }, 'another linked extra data': { extra: '0x1234' }, 'another destination token': { toToken: OTHER } };
  for (const [name, o] of Object.entries(cases)) {
    const e = await setupReplacement(true);
    const logs = x3Logs(e, o);
    const v = await verdict(e, ...logs);
    assert.equal(v.verdict, 'REPLACED_BY_DIFFERENT_TRANSFER', name);
    const f = await e.client.findSourceTransfer(e.sent.intent, e.sent.checkpoint);
    assert.equal(f.result.status, 'NOT_FOUND_YET', name);
    assert.throws(() => buildHandle((e.client as any).ctx, quoteHandleInput(e.sent.quote, receiptOf(e, v.replacementHash))), /no linked SwapBridge/, name);
  }
  const e = await setupReplacement(true);
  const hash = e.w.chains.cross.mineLogs(e.wallet.address, e.router, '0x', x3Logs(e, { recipient: OTHER }));
  const h = await e.client.handleFromTx('cross', hash);
  assert.equal(h.route, Route.SWAP_BRIDGE);
});

test('a broken link is unattributed (REPLACED_UNVERIFIED) and handleFromTx errors', async () => {
  let e = await setupReplacement(true);
  assert.equal((await verdict(e, ...x3Logs(e, { badIndex: true }))).verdict, 'REPLACED_UNVERIFIED');
  e = await setupReplacement(true);
  assert.equal((await verdict(e, ...x3Logs(e, { noBI: true }))).verdict, 'REPLACED_UNVERIFIED');
  e = await setupReplacement(true);
  const hash = e.w.chains.cross.mineLogs(e.wallet.address, e.router, '0x', x3Logs(e, { badIndex: true }));
  await assert.rejects(() => e.client.handleFromTx('cross', hash));
  const l = linkSwapTransfer(receiptOf(e, hash), { router: e.router, bridge: e.hub });
  assert.equal(l.linked.length, 0); assert.equal(l.unlinkedSwapLogs.length, 1);
});

test('an X3 that succeeded with fees AND output HIGHER than quoted is still FOUND (no upper bound); the linked transfer is decoded from the events', async () => {
  const e = await setupReplacement(true);
  const minOut = e.sent.quote.limits.amountOutMinimum!;
  const logs = x3Logs(e, { amountOut: minOut + 20n * E18, value: minOut + 5n * E18, nf: 4n * E18, ef: 5n * E18 });
  assert.equal((await verdict(e, ...logs)).verdict, 'FOUND');
  const f = await e.client.findSourceTransfer(e.sent.intent, e.sent.checkpoint);
  assert.equal(f.result.status, 'FOUND');
});

test('the expected destination token is stored (hub pair remote token, NOT the hub tokenOut), sealed at match.toToken (required for X3) and survives a JSON round trip', async () => {
  const e = await setupReplacement(true);
  const q: any = e.sent.quote;
  assert.equal(q.match.toToken.toLowerCase(), e.bscUSDT.toLowerCase());
  assert.notEqual(q.match.toToken.toLowerCase(), q.details.hop1ToToken.toLowerCase());
  const payload = JSON.parse(q.seal.payload);
  assert.equal(String(payload.match.toToken).toLowerCase(), e.bscUSDT.toLowerCase());
  // omitting it from the wire (digest recomputed, so only the schema can catch it) => invalid-payload at match.toToken
  delete payload.match.toToken;
  const pl = canonicalJSON(payload);
  const bad = { ...q, seal: { v: 1, sdk: 'js', payload: pl, digest: keccak256(toUtf8Bytes(pl)) } } as Quote;
  assert.throws(() => openQuote(bad), (err: any) => err instanceof LimitViolationError && err.field === 'seal' && err.reason === 'invalid-payload' && err.path === 'match.toToken');
  // the JSON round trip keeps it and the copy is accepted
  const rt = jsonParse(jsonStringify(q)) as Quote;
  assert.equal((rt.match as any).toToken.toLowerCase(), e.bscUSDT.toLowerCase());
  assert.ok(openQuote(rt));
  // the hub tokenOut differs from the destination token yet the transfer is FOUND
  assert.equal((await verdict(e, ...x3Logs(e))).verdict, 'FOUND');
});
