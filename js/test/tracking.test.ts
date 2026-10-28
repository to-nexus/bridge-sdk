// Status tracking: keyed correlation on batch receipts, determination order, resumable chunked scan.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { AbiCoder, Wallet, getAddress, keccak256, zeroPadValue, toBeHex } from 'ethers';
import { BridgeClient, Route, classifyHop } from '../src/index.js';
import type { RawLog } from '../src/index.js';
import { FakeChain } from './fake.js';
import { E18, NATIVE, RECIPIENT, SENDER, World, bridgeI } from './world.js';

const HUB = '0xb81d6e000000000000000000000000000000C0de';
const ONEUSD = '0xff42c13d927a6b9265236619161accb227b6d452';
const USDTX = '0x1c37d4F44ed3eC86d96868001CfA89e97112a85C';
let txn = 0;
const txh = () => '0x' + (++txn).toString(16).padStart(64, '0');
const finalizeSel = bridgeI.getFunction('finalizeBridgeBatch')!.selector;
const releaseSel = bridgeI.getFunction('releasePending')!.selector;
const manualSel = bridgeI.getFunction('manualReleasePendingWithRecipient')!.selector;

function mk(ev: string, values: any[], tx: string, logIndex: number, address = HUB): RawLog {
  const e = bridgeI.encodeEventLog(ev, values);
  return { address, topics: e.topics as string[], data: e.data, transactionHash: tx, logIndex, blockNumber: 2000 };
}
const finalized = (from: number, idx: number, tx: string, li: number, value = 10n * E18, token = NATIVE, to = RECIPIENT) => mk('BridgeFinalized', [from, idx, token, to, value, 1n], tx, li);
const extra = (from: number, idx: number, ok: boolean, consumed: bigint, ret: string, tx: string, li: number, target = HUB, selector = '0x55fd0776') => mk('ExtraCallExecuted', [from, idx, target, selector, ok, consumed, ret], tx, li);
// decoded extraData expectations (what the handle stores) for the pure classifier tests
const V3 = '0x6666666666666666666666666666666666666666';
const HE_TOKEN = '0x00000000000000000000000000000000000000AA';
const X1_RECIPIENT = '0x3333333333333333333333333333333333333333';
const CORE_RECIPIENT = '0x4444444444444444444444444444444444444444';
const X1E = { router: V3, tokenIn: USDTX, tokenOut: ONEUSD, fee: 500, recipient: X1_RECIPIENT, deadline: '1900000000', amountIn: (10n * E18).toString(), amountOutMinimum: '1' };
const X2E = { token: HE_TOKEN, coreRecipient: CORE_RECIPIENT, encodedAmount: '5000000000000000042' };
const exX1 = (l: RawLog[]) => l; void exX1;
const initiated = (to: number, idx: number, value: bigint, nf: bigint, ex: bigint, extraData: string, tx: string, li: number) => mk('BridgeInitiated', [to, idx, NATIVE, NATIVE, HUB, RECIPIENT, value, nf, ex, extraData, 1n], tx, li);
const fwdInit = (from: number, idx: number, to: number, value: bigint, nf: bigint, ex: bigint, extraData: string, tx: string, li: number) => mk('ForwardInitiated', [from, idx, to, NATIVE, RECIPIENT, value, nf, ex, keccak256(extraData)], tx, li);

const sel = (m: Record<string, string>) => async (h: string) => m[h] ?? null;
const noReceipt = async () => null;
const cls = (kind: any, logs: RawLog[], selectors: Record<string, string>, receipts: Record<string, any> = {}, exp: { x1?: typeof X1E | null; x2?: typeof X2E | null } = {}) =>
  classifyHop({
    kind, logs, selectorOf: sel(selectors), receiptOf: async (h) => receipts[h] ?? null, hubAddress: HUB, x1OneUsd: ONEUSD,
    ...((exp.x1 === undefined ? kind === 'x1' : exp.x1) ? { x1: exp.x1 ?? X1E } : {}), ...((exp.x2 === undefined ? kind === 'x2' : exp.x2) ? { x2: exp.x2 ?? X2E } : {}),
  });
const xe1 = (from: number, idx: number, ok: boolean, consumed: bigint, ret: string, tx: string, li: number) => extra(from, idx, ok, consumed, ret, tx, li, V3, '0x414bf389');
const xe2 = (from: number, idx: number, ok: boolean, consumed: bigint, ret: string, tx: string, li: number) => extra(from, idx, ok, consumed, ret, tx, li, HE_TOKEN, '0x9438568d');
const abi = AbiCoder.defaultAbiCoder();

test('determination order: plain hop without ExtraCallExecuted is DELIVERED (not fallback); recipient/amount from the keyed event', async () => {
  const t = txh();
  const r = await cls('plain', [finalized(97, 0, t, 0, 10n * E18)], { [t]: finalizeSel });
  assert.equal(r.state, 'DELIVERED');
  assert.equal(r.amount, (10n * E18).toString());
});

test('batch receipt: two items, one success + one extraCall failure -> per-item correlation; foreign keyed logs in the same tx are ignored', async () => {
  const t = txh();
  const logs = [
    finalized(97, 0, t, 0, 10n * E18, USDTX), xe1(97, 0, true, 10n * E18, abi.encode(['uint256'], [123n]), t, 1),
    finalized(97, 1, t, 2, 10n * E18, USDTX), xe1(97, 1, false, 0n, '0x', t, 3),
  ];
  const keyed = (idx: number) => logs.filter((l) => l.topics[2] === zeroPadValue(toBeHex(idx), 32)); // the tracker's eth_getLogs topic filter
  const s = { [t]: finalizeSel };
  const a = await cls('x1', keyed(0), s);
  const b = await cls('x1', keyed(1), s);
  assert.equal(a.state, 'DELIVERED'); assert.equal(a.amount, '123'); assert.equal(a.token, ONEUSD);
  assert.equal(b.state, 'DELIVERED_FALLBACK'); assert.equal(b.token, USDTX); assert.equal(b.reason, 'extraCall-failed');
});

test('X1: two successful items to the same recipient in one batch - each amount from its OWN returnData; undecodable -> unknown; absent -> USDTx fallback', async () => {
  const t = txh();
  const logs = [
    finalized(97, 0, t, 0, 10n * E18, USDTX), xe1(97, 0, true, 10n * E18, abi.encode(['uint256'], [111n]), t, 1),
    finalized(97, 1, t, 2, 10n * E18, USDTX), xe1(97, 1, true, 10n * E18, abi.encode(['uint256'], [222n]), t, 3),
    finalized(97, 2, t, 4, 10n * E18, USDTX), xe1(97, 2, true, 10n * E18, '0x', t, 5),
    finalized(97, 3, t, 6, 10n * E18, USDTX),
  ];
  const keyed = (idx: number) => logs.filter((l) => l.topics[2] === zeroPadValue(toBeHex(idx), 32));
  const s = { [t]: finalizeSel };
  assert.equal((await cls('x1', keyed(0), s)).amount, '111');
  assert.equal((await cls('x1', keyed(1), s)).amount, '222');
  assert.equal((await cls('x1', keyed(2), s)).amount, 'unknown');
  const d = await cls('x1', keyed(3), s);
  assert.equal(d.state, 'DELIVERED_FALLBACK'); assert.equal(d.reason, 'extraCall-skipped'); assert.equal(d.token, USDTX);
});

test('X2: ExtraCallExecuted(true) -> CORE_DEPOSIT_SUBMITTED (never DELIVERED) with coreRecipient / sent / surplus; (false) / absent -> DELIVERED_FALLBACK', async () => {
  const t = txh();
  const s = { [t]: finalizeSel };
  // finalized 5000000000000000042, the token floors to coreUnit and sends 5e18; the executor returns the 42 surplus to BridgeFinalized.to
  const ok = await cls('x2', [finalized(612044, 0, t, 0, 5000000000000000042n), xe2(612044, 0, true, 5000000000000000000n, abi.encode(['uint256'], [5000000000000000000n]), t, 1)], s);
  assert.equal(ok.state, 'CORE_DEPOSIT_SUBMITTED');
  assert.equal(ok.recipient, CORE_RECIPIENT);
  const d = ok.detail as any;
  assert.deepEqual([d.coreRecipient, d.coreAmount, d.returnedToDest, d.finalizedValue, d.returnedTo], [CORE_RECIPIENT, '5000000000000000000', '42', '5000000000000000042', RECIPIENT]);
  const bad = await cls('x2', [finalized(612044, 1, t, 2), xe2(612044, 1, false, 0n, '0x', t, 3)], s);
  assert.deepEqual([bad.state, bad.reason], ['DELIVERED_FALLBACK', 'extraCall-failed']);
  const none = await cls('x2', [finalized(612044, 2, t, 4)], s);
  assert.deepEqual([none.state, none.reason], ['DELIVERED_FALLBACK', 'extraCall-skipped']);
});

test('X2: surplus when the hop-2 finalized value exceeds the committed amount (fee drop); coreUnit rounding dust; encodedAmount > finalized -> fallback; wrong target / no decoded call -> PROCESSED_UNVERIFIED', async () => {
  const t = txh();
  const s = { [t]: finalizeSel };
  // fee drop: committed 5e18, actually finalized 5e18 + 7e17 -> the token still sends only encodedAmount (floored), everything else returns
  const x2e = { ...X2E, encodedAmount: '5000000000000000000' };
  const surplus = await cls('x2', [finalized(612044, 0, t, 0, 5700000000000000000n), xe2(612044, 0, true, 5000000000000000000n, abi.encode(['uint256'], [5000000000000000000n]), t, 1)], s, {}, { x2: x2e });
  assert.equal(surplus.state, 'CORE_DEPOSIT_SUBMITTED');
  assert.equal((surplus.detail as any).returnedToDest, '700000000000000000');
  // rounding dust: encoded 1.000000000123e18 -> token sends the floored 1e18; 123 wei returns with the surplus
  const dust = await cls('x2', [finalized(612044, 1, t, 2, 1000000000123n), xe2(612044, 1, true, 1000000000000n, abi.encode(['uint256'], [1000000000000n]), t, 3)], s, {}, { x2: { ...X2E, encodedAmount: '1000000000123' } });
  assert.equal((dust.detail as any).returnedToDest, '123'); assert.equal((dust.detail as any).coreAmount, '1000000000000');
  // encodedAmount > finalized: the executor call reverts -> ExtraCallExecuted(false) -> plain fallback, never success
  const over = await cls('x2', [finalized(612044, 2, t, 4, 4n * E18), xe2(612044, 2, false, 0n, '0x', t, 5)], s, {}, { x2: x2e });
  assert.deepEqual([over.state, over.reason, over.amount], ['DELIVERED_FALLBACK', 'extraCall-failed', (4n * E18).toString()]);
  // the executed call is not transferToCoreFor on the expected token
  const wrong = await cls('x2', [finalized(612044, 3, t, 6, 5n * E18), extra(612044, 3, true, 5n * E18, '0x', t, 7)], s, {}, { x2: x2e });
  assert.equal(wrong.state, 'PROCESSED_UNVERIFIED'); assert.match(wrong.reason!, /unsupported extraData/);
  // a handle without the decoded call can never be reported as success
  const nodec = await cls('x2', [finalized(612044, 4, t, 8, 5n * E18), xe2(612044, 4, true, 5n * E18, '0x', t, 9)], s, {}, { x2: null });
  assert.equal(nodec.state, 'PROCESSED_UNVERIFIED'); assert.match(nodec.reason!, /unsupported extraData/);
});

test('X1: DELIVERED is reported to the DECODED recipient/tokenOut (not BridgeFinalized.to); wrong amountIn / wrong call / missing decode -> PROCESSED_UNVERIFIED; fallback still pays USDTx to .to', async () => {
  const t = txh();
  const s = { [t]: finalizeSel };
  const ok = await cls('x1', [finalized(97, 0, t, 0, 10n * E18, USDTX), xe1(97, 0, true, 10n * E18, abi.encode(['uint256'], [9n * E18]), t, 1)], s);
  assert.deepEqual([ok.state, ok.recipient, ok.token, ok.amount], ['DELIVERED', X1_RECIPIENT, ONEUSD, (9n * E18).toString()]);
  assert.equal((ok.detail as any).bridgeFinalizedTo, RECIPIENT);
  const amt = await cls('x1', [finalized(97, 1, t, 2, 11n * E18, USDTX), xe1(97, 1, true, 11n * E18, '0x', t, 3)], s);
  assert.equal(amt.state, 'PROCESSED_UNVERIFIED'); assert.match(amt.reason!, /amountIn/);
  const wrong = await cls('x1', [finalized(97, 2, t, 4, 10n * E18, USDTX), extra(97, 2, true, 10n * E18, '0x', t, 5)], s);
  assert.equal(wrong.state, 'PROCESSED_UNVERIFIED');
  const nodec = await cls('x1', [finalized(97, 3, t, 6, 10n * E18, USDTX), xe1(97, 3, true, 10n * E18, '0x', t, 7)], s, {}, { x1: null });
  assert.equal(nodec.state, 'PROCESSED_UNVERIFIED');
  const fb = await cls('x1', [finalized(97, 4, t, 8, 10n * E18, USDTX), xe1(97, 4, false, 0n, '0x', t, 9)], s);
  assert.deepEqual([fb.state, fb.recipient, fb.token], ['DELIVERED_FALLBACK', RECIPIENT, USDTX]);
});

test('multihop hop 1: forward events win over the hub ExtraCallExecuted; hop-2 index via the strict match rule; ambiguity -> PROCESSED_UNVERIFIED', async () => {
  const inner = '0x';
  const t = txh();
  // two forwards to the same dest in one finalize batch. Real emission order per item: BridgeInitiated(hop2), ForwardInitiated, ExtraCallExecuted, BridgeFinalized
  const logs: RawLog[] = [
    initiated(998, 7, 10n * E18, 18n * 10n ** 16n, 0n, inner, t, 0), fwdInit(97, 0, 998, 10n * E18, 18n * 10n ** 16n, 0n, inner, t, 1), extra(97, 0, true, 10n * E18, '0x', t, 2), finalized(97, 0, t, 3, 10n * E18 + 18n * 10n ** 16n),
    initiated(998, 8, 10n * E18, 18n * 10n ** 16n, 0n, inner, t, 4), fwdInit(97, 1, 998, 10n * E18, 18n * 10n ** 16n, 0n, inner, t, 5), extra(97, 1, true, 10n * E18, '0x', t, 6), finalized(97, 1, t, 7, 10n * E18 + 18n * 10n ** 16n),
  ];
  const receipt = { status: '0x1', blockNumber: '0x7d0', transactionHash: t, logs: logs.map((l) => ({ ...l, logIndex: '0x' + l.logIndex.toString(16), blockNumber: '0x7d0' })) };
  const keyed = (idx: number) => logs.filter((l) => l.topics[2] === zeroPadValue(toBeHex(idx), 32) && !l.topics[0]!.startsWith(bridgeI.getEvent('BridgeInitiated')!.topicHash));
  const s = { [t]: finalizeSel }; const r = { [t]: receipt };
  const a = await cls('hubForward', keyed(0), s, r);
  const b = await cls('hubForward', keyed(1), s, r);
  assert.equal(a.state, 'FORWARDED'); assert.equal(a.hop2!.index, '7');
  assert.equal(b.state, 'FORWARDED'); assert.equal(b.hop2!.index, '8');

  // identical duplicate BridgeInitiated before the same ForwardInitiated -> not unique -> never guess
  const t2 = txh();
  const dup: RawLog[] = [initiated(998, 1, 10n * E18, 0n, 0n, inner, t2, 0), initiated(998, 2, 10n * E18, 0n, 0n, inner, t2, 1), fwdInit(97, 5, 998, 10n * E18, 0n, 0n, inner, t2, 2), finalized(97, 5, t2, 3)];
  const rec2 = { status: '0x1', blockNumber: '0x7d0', transactionHash: t2, logs: dup.map((l) => ({ ...l, logIndex: '0x' + l.logIndex.toString(16) })) };
  const amb = await cls('hubForward', dup.filter((l) => l.topics[1] === zeroPadValue(toBeHex(97), 32) && !l.topics[0]!.startsWith(bridgeI.getEvent('BridgeInitiated')!.topicHash)), { [t2]: finalizeSel }, { [t2]: rec2 });
  assert.equal(amb.state, 'PROCESSED_UNVERIFIED');
});

test('ForwardFailed -> HUB_FALLBACK with the failure code (native ONE paid to toOnHub)', async () => {
  const t = txh();
  const ff = mk('ForwardFailed', [97, 0, 2, keccak256('0x1234')], t, 1);
  const r = await cls('hubForward', [ff, finalized(97, 0, t, 2)], { [t]: finalizeSel });
  assert.equal(r.state, 'HUB_FALLBACK'); assert.equal(r.code, 'ExecutorCallReverted');
});

test('release resolution (step 4) is evaluated BEFORE extraData rules: public releasePending (only BridgeFinalized) and manualReleasePendingWithRecipient', async () => {
  for (const kind of ['plain', 'x1', 'x2', 'hubForward'] as const) {
    const t = txh();
    // public releasePending emits ONLY BridgeFinalized (no ManualReleased, no ExtraCallExecuted, no forward events)
    const rel = await cls(kind, [finalized(97, 0, t, 0, 10n * E18, kind === 'x1' ? USDTX : NATIVE)], { [t]: releaseSel });
    assert.equal(rel.state, 'RELEASED_FROM_PENDING', kind);
    assert.equal(rel.recipient, RECIPIENT);
    // manual release with a custom recipient: keyed ManualReleased exists
    const t2 = txh();
    const man = await cls(kind, [finalized(97, 1, t2, 0, 10n * E18, NATIVE, SENDER), mk('ManualReleased', [97, 1], t2, 1)], { [t2]: manualSel });
    assert.equal(man.state, 'RELEASED_FROM_PENDING', kind);
    assert.equal(man.recipient, SENDER);
    // even if forward/extraCall events from an earlier attempt are present, a release tx is a release
    const t3 = txh();
    const mix = await cls(kind, [extra(97, 2, true, 1n, '0x', t3, 0), finalized(97, 2, t3, 1)], { [t3]: releaseSel });
    assert.equal(mix.state, 'RELEASED_FROM_PENDING', kind);
  }
});

test('PendingRemoved -> PENDING_REMOVED; stale BridgePending with no resolution -> PROCESSED_UNVERIFIED (never success)', async () => {
  const t = txh();
  assert.equal((await cls('plain', [mk('PendingRemoved', [97, 0], t, 0)], {})).state, 'PENDING_REMOVED');
  const stale = await cls('plain', [mk('BridgePending', [97, 0, NATIVE, RECIPIENT, 1n, 1n, 2], t, 0)], {});
  assert.equal(stale.state, 'PROCESSED_UNVERIFIED');
  const none = await cls('plain', [], {});
  assert.equal(none.state, 'PROCESSED_UNVERIFIED');
});

// ------------------------------------------------------------------------------------------------ RPC-backed tracker
async function sentR1() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  const client = new BridgeClient(w.options({ scanChunkBlocks: 500 }));
  const q = await client.quote({ route: Route.BSC_TO_ONE, amount: 10n * E18, sender: wallet.address, recipient: RECIPIENT });
  const sent = await client.send(wallet, q);
  const handle = await sent.wait();
  return { w, c, client, handle, wallet };
}
const finalizeOnHub = (w: World, logsFn: (tx: string) => RawLog[]) => {
  const t = '0x' + 'ab'.repeat(32);
  const hubChain = w.chains.one;
  const mined = hubChain.mineLogs(SENDER, HUB, bridgeI.encodeFunctionData('finalizeBridgeBatch', [[], [], [], []]), logsFn(t).map((l) => ({ address: l.address, topics: l.topics, data: l.data })));
  return mined;
};

test('tracker (RPC-backed): SOURCE ok -> IN_FLIGHT -> DELIVERED; PENDING_ON_DEST has precedence over a (stale) success log', async () => {
  const { w, c, client, handle } = await sentR1();
  assert.equal(handle.scanFrom.one !== undefined, true, 'destination block recorded at send time');
  let r = await client.trackOnce(handle);
  assert.equal(r.state, 'IN_FLIGHT');
  w.pending.set(`one:${c.bsc.chainId}`, [0n]);
  r = await client.trackOnce(handle);
  assert.equal(r.state, 'PENDING_ON_DEST'); assert.equal(r.hops[0].reason, 'VerificationAmountThresholdExceeded');
  w.pending.clear();
  w.finalizeNext.set(`one:${c.bsc.chainId}`, 1n);
  // not finalized yet in logs: processed but no evidence -> never success
  r = await client.trackOnce(handle);
  assert.equal(r.state, 'PROCESSED_UNVERIFIED');
  finalizeOnHub(w, (t) => [finalized(c.bsc.chainId, 0, t, 0, 10n * E18)]);
  r = await client.trackOnce(handle);
  assert.equal(r.state, 'DELIVERED'); assert.equal(r.terminal, true);
});

test('tracker: chunked scan, dedupe, backoff, UNKNOWN on exhaustion, and resume from a SERIALIZED handle', async () => {
  const { w, c, client, handle } = await sentR1();
  w.finalizeNext.set(`one:${c.bsc.chainId}`, 1n);
  finalizeOnHub(w, (t) => [finalized(c.bsc.chainId, 0, t, 0, 10n * E18)]);
  const hub = w.chains.one;
  hub.block = handle.scanFrom.one! + 2600; // forces several 500-block chunks
  hub.calls.length = 0;
  hub.failLogs = 2; // two failed attempts are retried with backoff
  const t0 = w.clock;
  let r = await client.trackOnce(handle);
  assert.equal(r.state, 'DELIVERED');
  const getLogs = hub.calls.filter((x) => x.method === 'eth_getLogs');
  assert.ok(getLogs.length >= 6, `chunked: ${getLogs.length} calls`);
  assert.ok(w.clock - t0 >= 3000, 'slept 1s + 2s between retries');
  // resume: serialize, restart, only NEW blocks are scanned
  const saved = JSON.parse(JSON.stringify(r.handle));
  const key = Object.keys(saved.scans)[0];
  assert.equal(saved.scans[key].lastScanned, hub.block);
  const client2 = new BridgeClient(w.options({ scanChunkBlocks: 500 }));
  hub.calls.length = 0; hub.block += 10;
  r = await client2.trackOnce(saved);
  assert.equal(r.state, 'DELIVERED');
  const logs2 = hub.calls.filter((x) => x.method === 'eth_getLogs');
  assert.equal(logs2.length, 1);
  assert.equal(Number(BigInt(logs2[0].params[0].fromBlock)), saved.scans[key].lastScanned + 1);
  assert.equal(saved.scans[key].logs.length, 1, 'the finalized log is persisted exactly once (deduped across chunks/resume)');

  // backoff exhaustion -> UNKNOWN with recovery instructions (not an on-chain failure, not success)
  const f = await sentR1();
  f.w.finalizeNext.set(`one:${f.c.bsc.chainId}`, 1n);
  f.w.chains.one.unavailableLogs = true;
  const u = await f.client.trackOnce(f.handle);
  assert.equal(u.state, 'UNKNOWN');
  assert.match(u.hops[0].reason!, /manually/);
});

test('tracker: timeout returns the current NON-terminal state (distinct from an on-chain failure)', async () => {
  const { client, handle } = await sentR1();
  const r = await client.track(handle, { timeoutMs: 20_000, pollMs: 5_000 });
  assert.equal(r.state, 'IN_FLIGHT');
  assert.equal(r.terminal, false);
  assert.equal(r.timedOut, true);
});

test('tracker: R5 end to end - hub forward found, hop 2 delivered on the final destination', async () => {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  const client = new BridgeClient(w.options());
  const q = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: 10n * E18, sender: wallet.address, recipient: RECIPIENT });
  assert.equal(q.value, 10n * E18 + 18n * 10n ** 16n);
  const sent = await client.send(wallet, q);
  const handle = await sent.wait();
  assert.equal(handle.hop1.kind, 'hubForward');
  assert.equal(handle.hop2!.destChainId, c.hyperevm.chainId);
  // hub finalizes hop 1 and forwards: BridgeInitiated(hop2) -> ForwardInitiated -> ExtraCallExecuted -> BridgeFinalized
  w.finalizeNext.set(`one:${c.bsc.chainId}`, 1n);
  const ed = '0x';
  const nf = 18n * 10n ** 16n;
  w.chains.one.mineLogs(SENDER, HUB, bridgeI.encodeFunctionData('finalizeBridgeBatch', [[], [], [], []]), [
    initiated(c.hyperevm.chainId, 0, 10n * E18, nf, 0n, ed, '0x0', 0), fwdInit(c.bsc.chainId, 0, c.hyperevm.chainId, 10n * E18, nf, 0n, ed, '0x0', 1),
    extra(c.bsc.chainId, 0, true, 10n * E18, '0x', '0x0', 2), finalized(c.bsc.chainId, 0, '0x0', 3, 10n * E18 + nf),
  ].map((l) => ({ address: l.address, topics: l.topics, data: l.data })));
  let r = await client.trackOnce(handle);
  assert.equal(r.state, 'FORWARDED');
  assert.equal(r.handle.hop2!.index, '0');
  // hop 2 on HyperEVM
  w.finalizeNext.set(`hyperevm:${c.one.chainId}`, 1n);
  w.chains.hyperevm.mineLogs(SENDER, c.hyperevm.bridge!, bridgeI.encodeFunctionData('finalizeBridgeBatch', [[], [], [], []]), [
    finalized(c.one.chainId, 0, '0x0', 0, 10n * E18 + 5n, c.hyperevm.tokens.ONE!.address),
  ].map((l) => ({ address: c.hyperevm.bridge!, topics: l.topics, data: l.data })));
  r = await client.trackOnce(r.handle);
  assert.equal(r.state, 'DELIVERED');
  assert.equal(r.hops[1].amount, (10n * E18 + 5n).toString(), 'surplus above v2 is visible');
  void NATIVE; void getAddress; void FakeChain; void noReceipt;
});
