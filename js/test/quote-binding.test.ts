// A quote belongs to the client that issued it: network, chains and every config-owned address must equal THIS client's configuration, while values the
// caller supplied (X3 tokens, recipients, amount) must equal the request and the built calldata. Nothing is signed or sent for a quote that does not belong.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Transaction, Wallet, getAddress, keccak256 } from 'ethers';
import { BridgeClient, LimitViolationError, NetworkMismatchError, Route, SenderMismatchError, encodeBridgeToken, encodeX1ExtraData, getNetworksData } from '../src/index.js';
import type { Quote } from '../src/index.js';
import { bridgeIface } from '../src/encoding.js';
import type { NetworksData } from '../src/networks-types.js';
import { openQuote } from '../src/seal.js';
import { FakeChain, FakeWalletSigner } from './fake.js';
import { E18, NATIVE, RECIPIENT, SENDER, TOKEN_A, World, bridgeI, sbrI } from './world.js';
import { OTHER, amount, r1, sealForTest, setup, x1World } from './helpers.js';

const A = (n: string) => getAddress('0x' + n.padStart(40, '0')); // fake (dummy-pattern) addresses of the override configuration

function overrideNetworks(): NetworksData {
  const nd: NetworksData = structuredClone(getNetworksData());
  const t = nd.networks.testnet.chains;
  t.cross.bridge = A('a1000000001');
  t.cross.swap!.swapBridgeRouter = A('a1000000002');
  t.cross.swap!.v3SwapRouter = A('a1000000003');
  t.cross.swap!.quoterV2 = A('a1000000004');
  t.cross.swap!.v3Factory = A('a1000000005');
  t.cross.tokens.ONEUSD = { address: A('a1000000006'), symbol: 'ONEUSD', decimals: 18, kind: 'erc20' };
  t.cross.swap!.x1Pool = { tokenIn: t.cross.tokens.USDTx!.address, tokenOut: A('a1000000006'), fee: 500, pool: A('a1000000007') };
  t.hyperevm.tokens.ONE!.address = A('a1000000008');
  return nd;
}

function overrideWorld(nd: NetworksData = overrideNetworks()) {
  const w = World.testnet(nd);
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  const usdt = c.bsc.tokens.USDT!.address, usdtx = c.cross.tokens.USDTx!.address;
  w.setPair('bsc', c.cross.chainId, usdt, usdtx, true);
  w.setFee('bsc', c.cross.chainId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`cross:${c.cross.swap!.v3SwapRouter!.toLowerCase()}`);
  w.setBal('bsc', usdt, wallet.address, 1000n * E18);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('cross', TOKEN_A, wallet.address, 1000n * E18);
  w.setFee('cross', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n * E18;
  w.x1Out = 100n * E18 + 3_511_957n;
  return { nd, w, c, wallet, usdtx, client: new BridgeClient(w.options({ quoteTtlMs: 60_000 })), defaultClient: new BridgeClient(w.options({ networksData: undefined, quoteTtlMs: 60_000 })) };
}

async function overrideQuotes(o: ReturnType<typeof overrideWorld>) {
  const base = { sender: o.wallet.address, recipient: RECIPIENT };
  return {
    X1: await o.client.quote({ ...base, route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18 }),
    X2: await o.client.quote({ ...base, route: Route.ONE_TO_HYPERCORE, amount }),
    R5: await o.client.quote({ ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM, amount }),
    'R5+core': await o.client.quote({ ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, hypercore: true }),
    X3: await o.client.quote({ ...base, route: Route.SWAP_BRIDGE, amount: 50n * E18, x3: { tokenIn: TOKEN_A, tokenOut: o.usdtx, poolFee: 0, toChain: 'bsc' } }),
  } as Record<string, Quote>;
}

test('a quote of another network / config is rejected before any approve, signature or broadcast (Wallet without provider, both directions)', async () => {
  const main = World.testnet(); void main;
  const t = setup();
  const m = x1World();
  const mainClient = new BridgeClient(m.w.options());
  // mainnet quote into the testnet client
  m.w.setBal('bsc', m.c.bsc.tokens.ONE!.address, t.wallet.address, 1000n * E18);
  m.w.setFee('bsc', m.c.cross.chainId, m.c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 0n, rate: 0n });
  m.w.setPair('bsc', m.c.cross.chainId, m.c.bsc.tokens.ONE!.address, NATIVE, true);
  const qMain = await mainClient.quote(r1(t.wallet.address));
  assert.equal(qMain.network, 'mainnet');
  await assert.rejects(() => t.client.send(t.wallet, qMain), (e: any) => e instanceof NetworkMismatchError && /network/.test(e.message));
  // testnet quote into the mainnet client
  const qTest = await t.client.quote(r1(t.wallet.address));
  await assert.rejects(() => mainClient.send(t.wallet, qTest), (e: any) => e instanceof NetworkMismatchError);
  for (const w of [t.w, m.w]) { assert.equal(w.approveCalls.length, 0); assert.equal(w.chains.bsc.rawSent.length, 0); assert.equal(w.chains.cross.rawSent.length, 0); }
});

test('tampered quotes: a config-owned address => NetworkMismatchError; a calldata that no longer encodes the request => LimitViolationError; nothing is sent', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const bad1 = sealForTest({ ...q, sourceContract: OTHER, tx: { ...q.tx, to: OTHER } }); // route rules need tx.to == sourceContract; both retargeted // retargeted bridge address
  await assert.rejects(() => client.send(wallet, bad1), (e: any) => e instanceof NetworkMismatchError && /sourceContract|tx\.to/.test(e.message));
  const bad2 = sealForTest({ ...q, approval: { ...q.approval!, spender: OTHER } });
  await assert.rejects(() => client.send(wallet, bad2), (e: any) => e instanceof NetworkMismatchError && /spender/.test(e.message));
  const bad3 = sealForTest({ ...q, sourceChainId: 1 });
  await assert.rejects(() => client.send(wallet, bad3), (e: any) => e instanceof NetworkMismatchError);
  // user-supplied recipient swapped inside the calldata only
  const data = bridgeI.encodeFunctionData('bridgeToken', [c.cross.chainId, c.bsc.tokens.ONE!.address, OTHER, q.value, 0n, 0n, '0x']);
  await assert.rejects(() => client.send(wallet, sealForTest({ ...q, tx: { ...q.tx, data } })), (e: any) => e instanceof LimitViolationError);
  assert.equal(w.approveCalls.length, 0); assert.equal(w.chains.bsc.rawSent.length, 0);
});

test('X3 with a tokenIn that is NOT in config passes the binding check, and the broadcast calldata equals the quote calldata', async () => {
  const { w, c, wallet, client } = setup();
  const usdtx = c.cross.tokens.USDTx!.address;
  const q = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } });
  const sent = await client.send(wallet, q);
  assert.equal(sent.intent.data, q.tx.data);
  assert.equal(Transaction.from(sent.raw!).data, q.tx.data);
  // but a quote whose calldata encodes another tokenOut than the request is internal tampering
  const other = sbrI.encodeFunctionData('swapBridgeExactInputSingle', [[TOKEN_A, OTHER, 0, 50n * E18, 1n, 0, [c.bsc.chainId, RECIPIENT, '0x']], 2_000_000_000]);
  await assert.rejects(() => client.send(wallet, sealForTest({ ...q, tx: { ...q.tx, data: other } })), (e: any) => e instanceof LimitViolationError);
  void w;
});

test('network binding: wrong RPC chainId, wrong signer network, wrong sender - all rejected before any approve or signature', async () => {
  const { w, c, wallet } = setup();
  // (a) RPC points at the wrong chain
  const bad = new BridgeClient({ ...w.options(), providers: { ...w.chains, bsc: new FakeChain(1) } });
  await assert.rejects(() => bad.quote(r1(wallet.address)), (e: any) => e instanceof NetworkMismatchError && e.expected === 97 && e.actual === 1);
  const client = new BridgeClient(w.options());
  const q = await client.quote(r1(wallet.address));
  // (b) signer attached to another network
  const alien = new FakeWalletSigner(wallet.address, new FakeChain(56));
  await assert.rejects(() => client.send(alien, q), (e: any) => e instanceof NetworkMismatchError);
  assert.equal(alien.sent.length, 0);
  // (c) signer is not the quote sender
  const other = new FakeWalletSigner(SENDER, w.chains.bsc);
  await assert.rejects(() => client.send(other, q), (e: any) => e instanceof SenderMismatchError);
  assert.equal(other.sent.length, 0);
  assert.equal(w.approveCalls.length, 0);
  assert.equal(w.chains.bsc.rawSent.length, 0);
  void c;
});

test('override client: its own X1 / X2 / R5 / R5+core / X3 quotes pass openQuote (structural validation needs no config) and that client\'s buildTx / preflight / simulate binding (the client binding check uses ITS config); the built-in networks data is untouched', async () => {
  const before = JSON.stringify(getNetworksData());
  const o = overrideWorld();
  assert.notEqual(o.nd.networks.testnet.chains.cross.bridge, getNetworksData().networks.testnet.chains.cross.bridge, 'the override really differs');
  const quotes = await overrideQuotes(o);
  for (const [name, q] of Object.entries(quotes)) {
    assert.ok(openQuote(q), `${name}: openQuote`);
    assert.equal(o.client.buildTx(q).data, q.tx.data, `${name}: buildTx`);
    assert.ok(Array.isArray((await o.client.preflight(q)).issues), `${name}: preflight`);
    assert.ok('ok' in (await o.client.simulate(q)), `${name}: simulate`);
  }
  assert.equal(JSON.stringify(getNetworksData()), before, 'built-in networks data deep-equal before and after');
});

test('cross-client: an override-config quote given to a DEFAULT-config client is refused with NetworkMismatchError by the client binding check (not invalid-payload); a default-config quote given to the override client likewise', async () => {
  const o = overrideWorld();
  const quotes = await overrideQuotes(o);
  const wrongClient = (e: any) => e instanceof NetworkMismatchError && !(e instanceof LimitViolationError);
  for (const [name, q] of Object.entries(quotes)) {
    assert.ok(openQuote(q), `${name}: the quote itself is structurally valid`);
    assert.throws(() => o.defaultClient.buildTx(q), wrongClient, `${name}: buildTx`);
    await assert.rejects(() => o.defaultClient.preflight(q), wrongClient, `${name}: preflight`);
    await assert.rejects(() => o.defaultClient.simulate(q), wrongClient, `${name}: simulate`);
    await assert.rejects(() => o.defaultClient.ensureAllowance(o.wallet, q), wrongClient, `${name}: ensureAllowance`);
    await assert.rejects(() => o.defaultClient.send(o.wallet, q), wrongClient, `${name}: send`);
  }
  // reverse direction: default-config quotes into the override client
  const d = World.testnet();
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  d.setBal('bsc', d.net.chains.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  d.setBal('cross', TOKEN_A, wallet.address, 1000n * E18);
  d.setFee('cross', d.net.chains.bsc.chainId, d.net.chains.cross.tokens.USDTx!.address, { min: 1n, nf: 0n, rate: 0n });
  d.swapOut = 100n * E18;
  const dc = new BridgeClient(d.options());
  const base = { sender: wallet.address, recipient: RECIPIENT };
  const defaults: Quote[] = [
    await dc.quote({ ...base, route: Route.ONE_TO_HYPERCORE, amount }),
    await dc.quote({ ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM, amount }),
    await dc.quote({ ...base, route: Route.SWAP_BRIDGE, amount: 50n * E18, x3: { tokenIn: TOKEN_A, tokenOut: d.net.chains.cross.tokens.USDTx!.address, poolFee: 0, toChain: 'bsc' } }),
  ];
  for (const q of defaults) {
    assert.ok(openQuote(q));
    assert.throws(() => o.client.buildTx(q), wrongClient);
    await assert.rejects(() => o.client.preflight(q), wrongClient);
  }
});

test('Layer split: a well-formed X1 tokenOut / fee that is not the client\'s ONEUSD / pool fee passes structural validation but fails the client binding check (NetworkMismatchError); a zero tokenOut or an out-of-range fee fails structural validation (invalid-payload)', async () => {
  const o = overrideWorld();
  const q = (await overrideQuotes(o)).X1;
  const sw = o.c.cross.swap!;
  const x1 = (tokenOut: string, fee: number) => encodeX1ExtraData({ router: sw.v3SwapRouter!, tokenIn: o.usdtx, tokenOut, fee, recipient: RECIPIENT, deadline: q.deadline!, amountIn: q.value, amountOutMinimum: q.limits.amountOutMinimum! });
  const withExtra = (extraData: string): Quote => {
    const a = bridgeIface.decodeFunctionData('bridgeToken', q.tx.data);
    const data = encodeBridgeToken({ toChainId: a[0], fromToken: a[1], to: a[2], value: a[3], networkFee: a[4], exFee: a[5], extraData });
    return sealForTest({ ...q, tx: { ...q.tx, data }, details: { ...q.details, extraData }, match: { ...q.match, extraDataHash: keccak256(extraData) } as Quote['match'] });
  };
  const badOut = withExtra(x1(OTHER, 500));
  assert.ok(openQuote(badOut), 'structural validation accepts a well-formed tokenOut');
  assert.throws(() => o.client.buildTx(badOut), (e: any) => e instanceof NetworkMismatchError && /tokenOut/.test(e.scope));
  const badFee = withExtra(x1(o.c.cross.tokens.ONEUSD!.address, 3000));
  assert.ok(openQuote(badFee), 'structural validation accepts a valid uint24 fee');
  assert.throws(() => o.client.buildTx(badFee), (e: any) => e instanceof NetworkMismatchError && /fee/.test(e.scope));
  const zeroOut = withExtra(x1('0x0000000000000000000000000000000000000000', 500));
  assert.throws(() => openQuote(zeroOut), (e: any) => e instanceof LimitViolationError && e.reason === 'invalid-payload' && e.path === 'details.extraData');
  // the exact wire paths of the structural X1 checks: tokenIn == details.hop1ToToken, amountIn == value
  const badIn = sealForTest({ ...withExtra(encodeX1ExtraData({ router: sw.v3SwapRouter!, tokenIn: OTHER, tokenOut: o.c.cross.tokens.ONEUSD!.address, fee: 500, recipient: RECIPIENT, deadline: q.deadline!, amountIn: q.value, amountOutMinimum: q.limits.amountOutMinimum! })) });
  assert.throws(() => openQuote(badIn), (e: any) => e instanceof LimitViolationError && e.reason === 'invalid-payload' && e.path === 'details.extraData');
});

const mismatchAt = (re: RegExp) => (e: any) => e instanceof NetworkMismatchError && !(e instanceof TypeError) && re.test(e.scope);
test('an X1 quote from a client that kept the testnet router and filled in only ONEUSD + the X1 pool, given to a default testnet client (no ONEUSD, no pool), is NetworkMismatchError naming ONEUSD - not a TypeError', async () => {
  const nd: NetworksData = structuredClone(getNetworksData());
  const t = nd.networks.testnet.chains;
  t.cross.tokens.ONEUSD = { address: A('a1000000006'), symbol: 'ONEUSD', decimals: 18, kind: 'erc20' };
  t.cross.swap!.x1Pool = { tokenIn: t.cross.tokens.USDTx!.address, tokenOut: A('a1000000006'), fee: 500, pool: A('a1000000007') };
  const o = overrideWorld(nd);
  const q = (await overrideQuotes(o)).X1;
  assert.equal(o.c.cross.swap!.v3SwapRouter, getNetworksData().networks.testnet.chains.cross.swap!.v3SwapRouter, 'the existing router was kept');
  assert.ok(openQuote(q));
  assert.equal(o.client.buildTx(q).data, q.tx.data, 'the issuing client accepts it');
  assert.throws(() => o.defaultClient.buildTx(q), mismatchAt(/ONEUSD/));
  await assert.rejects(() => o.defaultClient.preflight(q), mismatchAt(/ONEUSD/));
  await assert.rejects(() => o.defaultClient.simulate(q), mismatchAt(/ONEUSD/));
  await assert.rejects(() => o.defaultClient.ensureAllowance(o.wallet, q), mismatchAt(/ONEUSD/));
  await assert.rejects(() => o.defaultClient.send(o.wallet, q), mismatchAt(/ONEUSD/));
});

test('one variant per missing component: ONEUSD / X1 pool / USDTx / X2 dest token / X3 router / multihop hub / WCROSS => NetworkMismatchError naming that path, no TypeError', async () => {
  const o = overrideWorld();
  const quotes = await overrideQuotes(o);
  const nat = await o.client.quote({ sender: o.wallet.address, recipient: RECIPIENT, route: Route.SWAP_BRIDGE, amount: 50n * E18, x3: { tokenIn: 'native', tokenOut: o.usdtx, poolFee: 0, toChain: 'bsc' } });
  const variants: [string, Quote, (c: any) => void, RegExp][] = [
    ['ONEUSD missing', quotes.X1, (c) => { c.cross.tokens.ONEUSD = null; }, /chains\.cross\.tokens\.ONEUSD/],
    ['X1 pool missing', quotes.X1, (c) => { c.cross.swap.x1Pool = null; }, /chains\.cross\.swap\.x1Pool/],
    ['USDTx missing', quotes.X1, (c) => { c.cross.tokens.USDTx = null; }, /chains\.cross\.tokens\.USDTx/],
    ['X1 router missing', quotes.X1, (c) => { c.cross.swap.v3SwapRouter = null; }, /chains\.cross\.swap\.v3SwapRouter/],
    ['X2 dest token missing', quotes.X2, (c) => { c.hyperevm.tokens.ONE = null; }, /chains\.hyperevm\.tokens\.ONE/],
    ['R5+core inner dest token missing', quotes['R5+core'], (c) => { c.hyperevm.tokens.ONE = null; }, /chains\.hyperevm\.tokens\.ONE/],
    ['X3 router missing', quotes.X3, (c) => { c.cross.swap.swapBridgeRouter = null; }, /chains\.cross\.swap\.swapBridgeRouter/],
    ['X3 native WCROSS missing', nat, (c) => { c.cross.tokens.WCROSS = null; }, /chains\.cross\.tokens\.WCROSS/],
    ['multihop hub missing', quotes.R5, (c) => { c.cross.bridge = null; }, /chains\.cross\.bridge/],
    ['whole chain missing', quotes.X2, (c) => { delete c.hyperevm; }, /chains\.hyperevm/],
  ];
  for (const [name, q, edit, re] of variants) {
    const nd2: NetworksData = structuredClone(o.nd);
    edit(nd2.networks.testnet.chains);
    const c2 = new BridgeClient(o.w.options({ networksData: nd2 }));
    assert.throws(() => c2.buildTx(q), mismatchAt(re), `${name}: buildTx`);
    await assert.rejects(() => c2.preflight(q), mismatchAt(re), `${name}: preflight`);
    await assert.rejects(() => c2.send(o.wallet, q), mismatchAt(re), `${name}: send`);
  }
});
