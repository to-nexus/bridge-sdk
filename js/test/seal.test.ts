// The quote seal: an unchanged quote (copy, structuredClone, JSON round trip) is accepted, any edit is refused with the offending path, and every public API
// that consumes a quote verifies the seal first.
import assert from 'node:assert/strict';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { test } from 'node:test';
import { Transaction, keccak256, toUtf8Bytes } from 'ethers';
import { BridgeClient, LimitViolationError, Route, jsonParse, jsonStringify } from '../src/index.js';
import type { Quote } from '../src/index.js';
import { canonicalJSON, sealPayloadOf } from '../src/seal.js';
import { E18, RECIPIENT, World, bridgeI } from './world.js';
import { OTHER, amount, nothingSent, r1, rpcCalls, sealForTest, setup, x3req } from './helpers.js';

const sealErr = (reason: string, path?: string) => (e: any) => e instanceof LimitViolationError && e.field === 'seal' && e.reason === reason && (path === undefined || e.path === path);
const withPayload = (q: Quote, payload: string, over: object = {}): Quote => ({ ...q, seal: { v: 1, sdk: 'js', payload, digest: keccak256(toUtf8Bytes(payload)), ...over } } as Quote);

test('an unchanged quote is accepted in every copy style (spread, structuredClone, JSON round trip); an edited recipient is refused as modified (path named) with 0 RPC and nothing sent', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  assert.ok(Object.keys(q).includes('seal'), 'the seal is an enumerable public property');
  assert.throws(() => { (q.seal as any).payload = 'x'; }, TypeError, 'the seal is frozen');
  const copies: Record<string, (x: Quote) => any> = {
    spread: (x) => ({ ...x, request: { ...x.request } }),
    structuredClone: (x) => structuredClone(x),
    'JSON round trip': (x) => jsonParse(jsonStringify(x)),
  };
  for (const [style, copy] of Object.entries(copies)) {
    const bad = copy(q);
    bad.request.recipient = OTHER;
    const before = rpcCalls(w);
    await assert.rejects(() => client.send(wallet, bad), sealErr('modified', 'request.recipient'), style);
    assert.equal(rpcCalls(w), before, `${style}: 0 RPC`);
    nothingSent(w);
  }
  for (const [style, copy] of Object.entries(copies)) {
    const t = setup();
    const q2 = await t.client.quote(r1(t.wallet.address));
    const sent = await t.client.send(t.wallet, copy(q2));
    assert.equal(sent.intent.data, q2.tx.data, style);
  }
});

test('edits made while the approve is pending (request, limits, X3 tokens) have no effect: calldata and limits stay the sealed originals', async () => {
  for (const kind of ['r1', 'x3'] as const) {
    const { w, c, wallet, client } = setup();
    const usdtx = c.cross.tokens.USDTx!.address;
    const q = await client.quote(kind === 'r1' ? r1(wallet.address) : x3req(wallet.address, usdtx));
    const copy: any = { ...q, request: { ...q.request, ...(q.request.x3 ? { x3: { ...q.request.x3 } } : {}) }, limits: { ...q.limits }, tx: { ...q.tx } };
    w.onApprove = () => {
      copy.request.recipient = OTHER; copy.limits.maxTotalSpend = 1n; copy.limits.minDelivered = 0n; copy.tx.data = '0x00';
      if (copy.request.x3) { copy.request.x3.tokenIn = OTHER; copy.request.x3.tokenOut = OTHER; }
    };
    const sent = await client.send(wallet, copy);
    assert.equal(sent.intent.data, q.tx.data, kind);
    assert.equal(Transaction.from(sent.raw!).data, q.tx.data, kind);
    assert.deepEqual(sent.quote.limits, q.limits, kind);
  }
});

test('seal failures: missing / wrong shape / tampered digest / v=2 / sdk go - each with 0 RPC and nothing sent', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const { seal: _s, ...noSeal } = q;
  const before = rpcCalls(w);
  const cases: [string, any, (e: any) => boolean][] = [
    ['no seal', noSeal, sealErr('missing')],
    ['null seal', { ...q, seal: null }, sealErr('missing')],
    ['payload not a string', { ...q, seal: { ...q.seal, payload: 5 } }, sealErr('missing')],
    ['digest missing', { ...q, seal: { v: 1, sdk: 'js', payload: q.seal.payload } }, sealErr('missing')],
    ['not an object', null, sealErr('missing')],
    ['tampered digest', { ...q, seal: { ...q.seal, digest: '0x' + '11'.repeat(32) } }, sealErr('corrupt')],
    ['tampered payload', { ...q, seal: { ...q.seal, payload: q.seal.payload + ' ' } }, sealErr('corrupt')],
    ['v=2', { ...q, seal: { ...q.seal, v: 2 } }, sealErr('unsupported-version')],
    ['sdk go', { ...q, seal: { ...q.seal, sdk: 'go' } }, sealErr('unsupported-sdk')],
  ];
  for (const [name, bad, check] of cases) await assert.rejects(() => client.send(wallet, bad), check, name);
  assert.equal(rpcCalls(w), before, '0 RPC');
  nothingSent(w);
});

test('invalid payloads WITH a correct digest are caught by the schema check only: invalid-payload + path, never a generic exception, 0 RPC / approve / sign / broadcast', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const p = JSON.parse(q.seal.payload);
  const mut = (f: (o: any) => void) => { const o = JSON.parse(JSON.stringify(p)); f(o); return canonicalJSON(o); };
  const cases: [string, string, string | undefined][] = [
    ['malformed JSON', '{"a":', '$'],
    ['not an object', '[]', '$'],
    ['missing required field', mut((o) => { delete o.sourceBridge; }), 'sourceBridge'],
    ['missing nested required field', mut((o) => { delete o.tx.data; }), 'tx.data'],
    ['non-numeric amount', mut((o) => { o.value = '12x'; }), 'value'],
    ['negative amount', mut((o) => { o.totalSpend = '-5'; }), 'totalSpend'],
    ['leading zero amount', mut((o) => { o.value = '007'; }), 'value'],
    ['wrong type (number instead of decimal string)', mut((o) => { o.ttlMs = 30000; }), 'ttlMs'],
    ['malformed address', mut((o) => { o.sender = '0x12'; }), 'sender'],
    ['uppercase (non canonical) address', mut((o) => { o.sender = o.sender.toUpperCase().replace('0X', '0x'); }), 'sender'],
    ['malformed hex', mut((o) => { o.tx.data = '0xabc'; }), 'tx.data'],
    ['unknown network', mut((o) => { o.network = 'devnet'; }), 'network'],
    ['unknown extra field', mut((o) => { o.extra = '1'; }), 'extra'],
  ];
  const before = rpcCalls(w);
  for (const [name, payload, path] of cases) await assert.rejects(() => client.send(wallet, withPayload(q, payload)), sealErr('invalid-payload', path), name);
  assert.equal(rpcCalls(w), before, '0 RPC');
  nothingSent(w);
});

test('a public field that cannot be encoded is "modified" with its path (wrong type / missing)', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  await assert.rejects(() => client.send(wallet, { ...q, ttlMs: 'soon' } as any), sealErr('modified', 'ttlMs'));
  await assert.rejects(() => client.send(wallet, { ...q, tx: { ...q.tx, value: -1n } } as any), sealErr('modified', 'tx.value'));
  const { sourceBridge: _x, ...partial } = q;
  await assert.rejects(() => client.send(wallet, partial as any), sealErr('modified', 'sourceBridge'));
  nothingSent(w);
});

test('seal vectors: each route family reproduces its payload and digest byte for byte; encoding twice is stable', () => {
  const file = new URL('../../test/seal.vectors.json', import.meta.url);
  const vec = JSON.parse(readFileSync(file, 'utf8')) as { family: string; quote: string; payload: string; digest: string }[];
  assert.ok(vec.length >= 4);
  for (const v of vec) {
    const q = jsonParse<Omit<Quote, 'seal'>>(v.quote);
    const a = sealPayloadOf(q), b = sealPayloadOf(q);
    assert.equal(a.payload, b.payload, `${v.family}: stable`);
    assert.equal(a.payload, v.payload, `${v.family}: payload`);
    assert.equal(a.digest, v.digest, `${v.family}: digest`);
    assert.equal(keccak256(toUtf8Bytes(v.payload)), v.digest, `${v.family}: digest recomputed`);
    assert.equal(sealForTest(q).seal.payload, v.payload);
  }
});

test('every public API that consumes a Quote verifies the seal first: buildTx / preflight / simulate / ensureAllowance refuse a modified quote (nothing sent, no approve) and accept an unchanged copy', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const bad = { ...q, tx: { ...q.tx, data: '0x00' } }; // modified calldata
  const bad2 = { ...q, request: { ...q.request, recipient: OTHER } };
  const before = rpcCalls(w);
  assert.throws(() => client.buildTx(bad as any), sealErr('modified', 'tx.data'));
  assert.throws(() => client.buildTx(bad2 as any), sealErr('modified', 'request.recipient'));
  await assert.rejects(() => client.preflight(bad as any), sealErr('modified', 'tx.data'));
  await assert.rejects(() => client.simulate(bad as any), sealErr('modified', 'tx.data'));
  await assert.rejects(() => client.ensureAllowance(wallet, bad as any), sealErr('modified', 'tx.data'));
  await assert.rejects(() => client.ensureAllowance(wallet, bad2 as any), sealErr('modified', 'request.recipient'));
  assert.throws(() => client.buildTx({ ...q, seal: undefined } as any), sealErr('missing'));
  assert.equal(rpcCalls(w), before, '0 RPC');
  nothingSent(w);
  // unchanged copies work
  const copy = { ...q, request: { ...q.request } };
  assert.equal(client.buildTx(copy).data, q.tx.data);
  assert.equal((await client.preflight(copy)).ok, true);
  assert.ok('ok' in (await client.simulate(copy)), 'simulate ran on the unchanged copy');
  assert.equal(w.approveCalls.length, 0, 'preflight / simulate never approve');
  const r = await client.ensureAllowance(wallet, copy);
  assert.equal(r.txs.length, 1);
  assert.equal(w.approveCalls.length, 1);
  assert.equal(w.chains.bsc.logs.length, 0, 'no bridge tx');
});

// generator for the vectors (run once with UPDATE_SEAL_VECTORS=1; the checked-in file is what the test above reproduces)
test('seal vectors generator (only with UPDATE_SEAL_VECTORS=1)', { skip: !process.env.UPDATE_SEAL_VECTORS }, async () => {
  const out: { family: string; quote: string; payload: string; digest: string }[] = [];
  const add = async (family: string, q0: Quote) => {
    const { seal: _s, ...rest } = q0;
    const fixed = { ...rest, id: '0x00000000000000aa' } as Omit<Quote, 'seal'>;
    const { payload, digest } = sealPayloadOf(fixed);
    out.push({ family, quote: jsonStringify(fixed), payload, digest });
  };
  const { c, wallet, client } = setup();
  await add('single R1 (erc20 source, approval)', await client.quote(r1(wallet.address)));
  await add('single R2 (native source, no approval)', await client.quote({ route: Route.ONE_TO_BSC, amount, sender: wallet.address, recipient: RECIPIENT }));
  await add('multihop R5 + X2 (hubForward, core)', await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: wallet.address, recipient: RECIPIENT, hypercore: true }));
  await add('x3 (swap match, erc20 in)', await client.quote(x3req(wallet.address, c.cross.tokens.USDTx!.address)));
  const m = new World('mainnet');
  const mc = m.net.chains;
  const usdt = mc.bsc.tokens.USDT!.address, usdtx = mc.cross.tokens.USDTx!.address;
  m.setPair('bsc', mc.cross.chainId, usdt, usdtx, true);
  m.setFee('bsc', mc.cross.chainId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  m.x1Out = 100n * E18 + 3_511_957n;
  await add('x1 (swap deadline, amountOutMinimum)', await new BridgeClient(m.options({ quoteTtlMs: 60_000 })).quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: wallet.address, recipient: RECIPIENT }));
  writeFileSync(new URL('../../test/seal.vectors.json', import.meta.url), JSON.stringify(out, null, 2) + '\n');
  assert.ok(existsSync(new URL('../../test/seal.vectors.json', import.meta.url)));
});
void bridgeI;
