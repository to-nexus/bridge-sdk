// Structural validation of a sealed quote payload: route-specific required fields, decoded-calldata consistency and time bounds. Every negative case carries a
// CORRECT digest, so only the schema / route rules can catch it.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Wallet, getAddress, keccak256, toUtf8Bytes } from 'ethers';
import { BridgeClient, LimitViolationError, Route, encodeBridgeToken, encodeCoreExtraData, encodeX1ExtraData, jsonParse, jsonStringify } from '../src/index.js';
import type { Quote } from '../src/index.js';
import { bridgeIface } from '../src/encoding.js';
import { canonicalJSON, openQuote } from '../src/seal.js';
import { E18, RECIPIENT, TOKEN_A, World } from './world.js';
import { OTHER, amount, rpcCalls, sealForTest } from './helpers.js';

const CORE = getAddress('0x4444444444444444444444444444444444444444');
const DEST = getAddress('0x3333333333333333333333333333333333333333');

function testnet() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('hyperevm', c.hyperevm.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('cross', TOKEN_A, wallet.address, 1000n * E18);
  w.setFee('cross', c.bsc.chainId, c.cross.tokens.USDTx!.address, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n * E18;
  return { w, c, wallet, client: new BridgeClient(w.options()) };
}
function mainnetX1(x1Out = 100n * E18 + 3_511_957n) {
  const w = new World('mainnet');
  const c = w.net.chains;
  const usdt = c.bsc.tokens.USDT!.address, usdtx = c.cross.tokens.USDTx!.address;
  w.setPair('bsc', c.cross.chainId, usdt, usdtx, true);
  w.setFee('bsc', c.cross.chainId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`cross:${c.cross.swap!.v3SwapRouter!.toLowerCase()}`);
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', usdt, wallet.address, 1000n * E18);
  w.x1Out = x1Out;
  return { w, c, wallet, client: new BridgeClient(w.options({ quoteTtlMs: 60_000 })) };
}
const sealErr = (path?: string) => (e: any) => e instanceof LimitViolationError && e.field === 'seal' && e.reason === 'invalid-payload' && (path === undefined || e.path === path);
const mut = (q: Quote, f: (o: any) => void): Quote => {
  const o = JSON.parse(q.seal.payload);
  f(o);
  const payload = canonicalJSON(o);
  return { ...q, seal: { v: 1, sdk: 'js', payload, digest: keccak256(toUtf8Bytes(payload)) } } as Quote;
};
const accepted = (q: Quote, label: string) => {
  assert.equal(openQuote(q).seal.digest, q.seal.digest, `${label}: the quote`);
  assert.ok(openQuote({ ...q, request: { ...q.request } }), `${label}: spread copy`);
  assert.ok(openQuote(jsonParse(jsonStringify(q))), `${label}: JSON round-trip`);
  assert.ok(openQuote(structuredClone(q)), `${label}: structuredClone`);
};
/** re-encode the sealed bridgeToken call with a different extraData (keeps every other argument) */
const withExtra = (q: Quote, extraData: string): Omit<Quote, 'seal'> => {
  const a = bridgeIface.decodeFunctionData('bridgeToken', q.tx.data);
  const data = encodeBridgeToken({ toChainId: a[0], fromToken: a[1], to: a[2], value: a[3], networkFee: a[4], exFee: a[5], extraData });
  return { ...q, tx: { ...q.tx, data }, details: { ...q.details, extraData }, match: { ...q.match, extraDataHash: keccak256(extraData) } as Quote['match'] };
};

test('positive controls: every route, an X1 quote with a floor of 0 and the recipient overrides pass openQuote in every copy style (spread, structuredClone, JSON round trip)', async () => {
  {
  const { c, wallet, client } = testnet();
  const base = { amount, sender: wallet.address, recipient: RECIPIENT };
  const reqs: any[] = [
    { ...base, route: Route.BSC_TO_ONE }, { ...base, route: Route.ONE_TO_BSC }, { ...base, route: Route.ONE_TO_HYPEREVM }, { ...base, route: Route.HYPEREVM_TO_ONE },
    { ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM }, { ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM, hypercore: true }, { ...base, route: Route.HYPEREVM_TO_ONE_TO_BSC },
    { ...base, route: Route.ONE_TO_HYPERCORE },
    { ...base, amount: 50n * E18, route: Route.SWAP_BRIDGE, x3: { tokenIn: TOKEN_A, tokenOut: c.cross.tokens.USDTx!.address, poolFee: 0, toChain: 'bsc' } },
    { ...base, amount: 50n * E18, route: Route.SWAP_BRIDGE, x3: { tokenIn: 'native', tokenOut: c.cross.tokens.USDTx!.address, poolFee: 0, toChain: 'hyperevm' } },
  ];
  for (const r of reqs) accepted(await client.quote(r), `${r.route}${r.hypercore ? '+core' : ''}${r.x3?.tokenIn === 'native' ? ' native' : ''}`);
  const x = mainnetX1();
  accepted(await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT }), 'X1');
  }
  {
  const x = mainnetX1(1n); // floor(1 * 9950 / 10000) = 0
  const q0 = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT });
  assert.equal(q0.limits.amountOutMinimum, 0n);
  accepted(q0, 'X1 floor 0');
  const qd = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT, recipients: { dest: DEST } });
  assert.notEqual(DEST, RECIPIENT);
  accepted(qd, 'X1 recipients.dest');
  const { wallet, client } = testnet();
  accepted(await client.quote({ route: Route.ONE_TO_HYPERCORE, amount, sender: wallet.address, recipient: RECIPIENT, recipients: { core: CORE } }), 'X2 R3 + recipients.core');
  accepted(await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: wallet.address, recipient: RECIPIENT, hypercore: true, recipients: { core: CORE, dest: DEST, onHub: OTHER } }), 'X2 R5 + recipients.core/dest/onHub');
  }
});

test('X1: missing limits.amountOutMinimum / deadlineSec (or a deadline in the past) => invalid-payload at the wire path; 0 RPC, nothing sent;(payload AND public fields both omit the floor) is refused at openQuote', async () => {
  const x = mainnetX1();
  const q = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT });
  const before = rpcCalls(x.w);
  const cases: [string, Quote, string][] = [
    ['no amountOutMinimum', mut(q, (o) => { delete o.limits.amountOutMinimum; }), 'limits.amountOutMinimum'],
    ['no deadlineSec', mut(q, (o) => { delete o.deadlineSec; }), 'deadlineSec'],
    ['deadline not after createdAt', mut(q, (o) => { o.deadlineSec = '1'; }), 'deadlineSec'],
    ['public fields AND payload omit the floor', sealForTest({ ...q, limits: { maxTotalSpend: q.limits.maxTotalSpend, minDelivered: q.limits.minDelivered } }), 'limits.amountOutMinimum'],
    ['public fields AND payload omit the deadline', sealForTest({ ...q, deadline: undefined }), 'deadlineSec'],
  ];
  for (const [name, bad, path] of cases) {
    assert.throws(() => openQuote(bad), sealErr(path), name);
    await assert.rejects(() => x.client.send(x.wallet, bad), sealErr(path), name);
  }
  assert.equal(rpcCalls(x.w), before, '0 RPC');
  assert.deepEqual([x.w.approveCalls.length, x.w.chains.bsc.rawSent.length], [0, 0]);
});

test('X1: the decoded exactInputSingle recipient must equal the RESOLVED destination recipient (recipients.dest override honoured, a different recipient refused)', async () => {
  const x = mainnetX1();
  const sw = x.c.cross.swap!;
  const newX1 = (q: Quote, recipient: string) => encodeX1ExtraData({ router: sw.v3SwapRouter!, tokenIn: x.c.cross.tokens.USDTx!.address, tokenOut: x.c.cross.tokens.ONEUSD!.address, fee: sw.x1Pool!.fee, recipient, deadline: q.deadline!, amountIn: q.value, amountOutMinimum: q.limits.amountOutMinimum! });
  const q = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT });
  assert.throws(() => openQuote(sealForTest(withExtra(q, newX1(q, OTHER)) as Quote)), sealErr('details.extraData'));
  // with the override the SAME recipient is now the wrong one, and the override recipient is the right one
  const qd = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT, recipients: { dest: DEST } });
  assert.throws(() => openQuote(sealForTest(withExtra(qd, newX1(qd, RECIPIENT)) as Quote)), sealErr('details.extraData'));
  assert.ok(openQuote(sealForTest(withExtra(qd, newX1(qd, DEST)) as Quote)));
  // a floor that contradicts the calldata
  assert.throws(() => openQuote(mut(q, (o) => { o.limits.amountOutMinimum = String(BigInt(o.limits.amountOutMinimum) + 1n); })), sealErr('details.extraData'));
});

test('X3: missing limits.minBridgeValue / limits.amountOutMinimum / deadlineSec; calldata that contradicts the request => invalid-payload; 0 RPC', async () => {
  const { w, c, wallet, client } = testnet();
  const q = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, x3: { tokenIn: TOKEN_A, tokenOut: c.cross.tokens.USDTx!.address, poolFee: 0, toChain: 'bsc' } });
  const before = rpcCalls(w);
  const cases: [string, Quote, string][] = [
    ['no minBridgeValue', mut(q, (o) => { delete o.limits.minBridgeValue; }), 'limits.minBridgeValue'],
    ['no amountOutMinimum', mut(q, (o) => { delete o.limits.amountOutMinimum; }), 'limits.amountOutMinimum'],
    ['no deadlineSec', mut(q, (o) => { delete o.deadlineSec; }), 'deadlineSec'],
    ['recipient of the request differs from the calldata', mut(q, (o) => { o.request.recipient = OTHER.toLowerCase(); }), 'tx.data'],
    ['public fields AND payload omit minBridgeValue', sealForTest({ ...q, limits: { ...q.limits, minBridgeValue: undefined } }), 'limits.minBridgeValue'],
  ];
  for (const [name, bad, path] of cases) {
    assert.throws(() => openQuote(bad), sealErr(path), name);
    await assert.rejects(() => client.send(wallet, bad), sealErr(path), name);
  }
  assert.equal(rpcCalls(w), before, '0 RPC');
  assert.deepEqual([w.approveCalls.length, w.chains.cross.rawSent.length], [0, 0]);
});

test('multihop: missing v2 / fees.hop2 / fees.hop2ExFeeDeclared / verifiers.hub / details.hop2Kind => invalid-payload; hop-2 fields must agree with the sealed bridgeTokenForwarded payload', async () => {
  const { w, wallet, client } = testnet();
  const q = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: wallet.address, recipient: RECIPIENT });
  const before = rpcCalls(w);
  const cases: [string, Quote, string][] = [
    ['no v2', mut(q, (o) => { delete o.v2; }), 'v2'],
    ['no fees.hop2', mut(q, (o) => { delete o.fees.hop2; }), 'fees.hop2'],
    ['no fees.hop2ExFeeDeclared', mut(q, (o) => { delete o.fees.hop2ExFeeDeclared; }), 'fees.hop2ExFeeDeclared'],
    ['no verifiers.hub', mut(q, (o) => { delete o.verifiers.hub; }), 'verifiers.hub'],
    ['no details.hop2Kind', mut(q, (o) => { delete o.details.hop2Kind; }), 'details.hop2Kind'],
    ['v2 contradicts the forward payload', mut(q, (o) => { o.v2 = String(BigInt(o.v2) + 1n); }), 'details.extraData'],
    ['public fields AND payload omit v2', sealForTest({ ...q, v2: undefined }), 'v2'],
  ];
  for (const [name, bad, path] of cases) {
    assert.throws(() => openQuote(bad), sealErr(path), name);
    await assert.rejects(() => client.send(wallet, bad), sealErr(path), name);
  }
  assert.equal(rpcCalls(w), before, '0 RPC');
  assert.deepEqual([w.approveCalls.length, w.chains.bsc.rawSent.length], [0, 0]);
});

test('X2 (R3 + core, R5 + core): a zero coreRecipient, a wrong amount or a coreRecipient that is not the resolved core recipient in the decoded extraData => invalid-payload', async () => {
  const { w, c, wallet, client } = testnet();
  const he = c.hyperevm.tokens.ONE!.address;
  const r3 = await client.quote({ route: Route.ONE_TO_HYPERCORE, amount, sender: wallet.address, recipient: RECIPIENT, recipients: { core: CORE } });
  const zero = '0x0000000000000000000000000000000000000000';
  const before = rpcCalls(w);
  const cases: [string, Quote][] = [
    ['R3 zero coreRecipient', sealForTest(withExtra(r3, encodeCoreExtraData({ token: he, coreRecipient: zero, value: r3.value })) as Quote)],
    ['R3 wrong amount', sealForTest(withExtra(r3, encodeCoreExtraData({ token: he, coreRecipient: CORE, value: r3.value + 1n })) as Quote)],
    ['R3 coreRecipient != resolved core recipient', sealForTest(withExtra(r3, encodeCoreExtraData({ token: he, coreRecipient: RECIPIENT, value: r3.value })) as Quote)],
  ];
  for (const [name, bad] of cases) {
    assert.throws(() => openQuote(bad), sealErr('details.extraData'), name);
    await assert.rejects(() => client.send(wallet, bad), sealErr('details.extraData'), name);
  }
  assert.equal(rpcCalls(w), before, '0 RPC');
  assert.deepEqual([w.approveCalls.length, w.chains.cross.rawSent.length], [0, 0]);
  // R5 + core: the inner payload is decoded from the sealed forward call
  const r5 = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: wallet.address, recipient: RECIPIENT, hypercore: true, recipients: { core: CORE } });
  const mutInner = (coreRecipient: string, value: bigint) => {
    const forwardEd = r5.details.extraData;
    const decoded = bridgeIface.decodeFunctionData('bridgeTokenForwarded', '0x' + forwardEd.slice(42));
    const call = bridgeIface.encodeFunctionData('bridgeTokenForwarded', [decoded[0], decoded[1], decoded[2], decoded[3], decoded[4], encodeCoreExtraData({ token: he, coreRecipient, value })]);
    return forwardEd.slice(0, 42) + call.slice(2);
  };
  for (const [name, ed] of [['R5 zero coreRecipient', mutInner(zero, r5.v2!)], ['R5 wrong amount', mutInner(CORE, r5.v2! + 1n)], ['R5 coreRecipient != resolved', mutInner(RECIPIENT, r5.v2!)]] as const) {
    assert.throws(() => openQuote(sealForTest(withExtra(r5, ed) as Quote)), sealErr('details.extraData'), name);
  }
});

test('ms values: ttlMs / createdAtMs / request.quoteTtlMs must be positive safe integers (0, negative and 2^53 are refused; the bound is accepted by the validator)', async () => {
  const { wallet, client } = testnet();
  const q = await client.quote({ route: Route.BSC_TO_ONE, amount, sender: wallet.address, recipient: RECIPIENT, quoteTtlMs: 60_000 });
  const SAFE = '9007199254740991';
  for (const [field, path] of [['ttlMs', 'ttlMs'], ['createdAtMs', 'createdAtMs']] as const) {
    for (const v of ['0', '-1', '9007199254740992', '9007199254740993', '99999999999999999999']) assert.throws(() => openQuote(mut(q, (o) => { o[field] = v; })), sealErr(path), `${field}=${v}`);
    // the bound itself passes the validator (it then differs from the public fields, i.e. "modified", not "invalid-payload")
    assert.throws(() => openQuote(mut(q, (o) => { o[field] = SAFE; })), (e: any) => e instanceof LimitViolationError && e.reason === 'modified', `${field} bound`);
  }
  for (const v of ['0', '-1', '9007199254740992']) assert.throws(() => openQuote(mut(q, (o) => { o.request.quoteTtlMs = v; })), sealErr('request.quoteTtlMs'), `quoteTtlMs=${v}`);
  assert.throws(() => openQuote(mut(q, (o) => { o.request.quoteTtlMs = SAFE; })), (e: any) => e instanceof LimitViolationError && e.reason === 'modified');
});
