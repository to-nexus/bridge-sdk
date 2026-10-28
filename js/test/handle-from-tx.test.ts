// handleFromTx: the route is inferred from the SOURCE receipt alone; only logs of the configured bridge / router count; ambiguity is never resolved silently.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Wallet, getAddress, hexlify, randomBytes } from 'ethers';
import { AmbiguousSourceError, BridgeClient, Route, RouteMismatchError, SourceTransferNotFoundError, SourceTxFailedError, UnsupportedRouteError, encodeForwardExtraData } from '../src/index.js';
import { E18, RECIPIENT, TOKEN_A, World, bridgeI } from './world.js';

const amount = 10n * E18;

async function x1WorldAsync() {
  const w = new World('mainnet');
  const c = w.net.chains;
  const hubId = c.one.chainId;
  const usdt = c.bsc.tokens.USDT!.address, usdtx = c.one.tokens.USDTx!.address, router = c.one.swap!.v3SwapRouter!;
  w.setPair('bsc', hubId, usdt, usdtx, true);
  w.setFee('bsc', hubId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`one:${router.toLowerCase()}`);
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', usdt, wallet.address, 1000n * E18);
  w.x1Out = 100n * E18 + 3_511_957n;
  return { w, c, wallet, client: new BridgeClient(w.options({ quoteTtlMs: 60_000 })) };
}
const x1Req = (sender: string, swap?: { slippageBps?: number; deadlineSec?: number }) => ({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender, recipient: RECIPIENT, ...(swap ? { swap } : {}) });
async function x3WorldAsync() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  const usdtx = c.one.tokens.USDTx!.address;
  w.setFee('one', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n * E18;
  return { w, c, wallet, usdtx, client: new BridgeClient(w.options()) };
}
const x3Req = (sender: string, usdtx: string, swap?: { slippageBps?: number; deadlineSec?: number }) => ({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender, recipient: RECIPIENT, ...(swap ? { swap } : {}), x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' as const } });

function setup() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('hyperevm', c.hyperevm.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  w.setFee('one', c.bsc.chainId, c.one.tokens.USDTx!.address, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n * E18;
  return { w, c, wallet, client: new BridgeClient(w.options()) };
}
test('handleFromTx infers the route from the SOURCE receipt for all 9 routes (route omitted) and matches the SDK-built handle', async () => {
  const { w, c, wallet, client } = setup();
  const usdtx = c.one.tokens.USDTx!.address;
  const base = { amount, sender: wallet.address, recipient: RECIPIENT };
  const reqs: any[] = [
    { ...base, route: Route.BSC_TO_ONE },
    { ...base, route: Route.ONE_TO_BSC },
    { ...base, route: Route.ONE_TO_HYPEREVM },
    { ...base, route: Route.HYPEREVM_TO_ONE },
    { ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM },
    { ...base, route: Route.BSC_TO_ONE_TO_HYPEREVM, hypercore: true },
    { ...base, route: Route.HYPEREVM_TO_ONE_TO_BSC },
    { ...base, route: Route.ONE_TO_HYPERCORE },
    { ...base, amount: 50n * E18, route: Route.SWAP_BRIDGE, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } },
  ];
  for (const r of reqs) {
    const q = await client.quote(r);
    const sent = await client.send(wallet, q);
    const built = await sent.wait();
    const inferred = await client.handleFromTx(q.sourceChainId, sent.hash);
    assert.equal(inferred.route, q.route, `route of ${q.route}${r.hypercore ? '+core' : ''}`);
    assert.equal(inferred.hop1.kind, q.details.hop1Kind);
    assert.equal(inferred.hop2?.kind, q.details.hop2Kind);
    assert.deepEqual({ ...inferred, scanFrom: {} }, { ...built, scanFrom: {} });
    assert.equal((await client.handleFromTx(q.sourceChainKey, sent.hash, { route: q.route })).route, q.route, 'matching explicit route is accepted');
  }
  // X1 (mainnet world)
  const x = await x1WorldAsync();
  const q = await x.client.quote(x1Req(x.wallet.address));
  const sent = await x.client.send(x.wallet, q);
  const h = await x.client.handleFromTx(56, sent.hash);
  assert.equal(h.route, Route.BSC_USDT_TO_ONEUSD);
  assert.equal(h.hop1.kind, 'x1');
});

const BI = (to: number, idx: number, from: string, extra: string, address: string, value = amount) => {
  const e = bridgeI.encodeEventLog('BridgeInitiated', [to, idx, '0x26ca2A92ea9659e073621bb85b9117Dc5944Cff1', '0x0000000000000000000000000000000000000001', from, RECIPIENT, value, 0n, 0n, extra, 1n]);
  return { address, topics: e.topics as string[], data: e.data };
};
test('handleFromTx trust + ambiguity: a forged emitter with identical topics is ignored; two real candidates => AmbiguousSourceError, resolved by opts.index / logIndex', async () => {
  const { w, c, wallet, client } = setup();
  const bsc = w.chains.bsc;
  const bridge = c.bsc.bridge!;
  const forger = getAddress(hexlify(randomBytes(20)));
  const hubId = c.one.chainId;
  // only a forged log: nothing to build a handle from
  const t1 = bsc.mineLogs(wallet.address, forger, '0x', [BI(hubId, 0, wallet.address, '0x', forger)]);
  await assert.rejects(() => client.handleFromTx(97, t1), (e: any) => e instanceof SourceTransferNotFoundError);
  // forged + real: the real one is used, the forgery (even with a different index) is ignored
  const t2 = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 99, wallet.address, '0x', forger), BI(hubId, 7, wallet.address, '0x', bridge)]);
  const h = await client.handleFromTx(97, t2);
  assert.equal(h.hop1.index, '7');
  assert.equal(h.route, Route.BSC_TO_ONE);
  // two real candidates
  const t3 = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 5, wallet.address, '0x', bridge), BI(hubId, 6, wallet.address, '0x', bridge)]);
  await assert.rejects(() => client.handleFromTx(97, t3), (e: any) => e instanceof AmbiguousSourceError && e.candidates.length === 2 && e.candidates[0].index === 5n && e.candidates[1].logIndex === 1);
  assert.equal((await client.handleFromTx(97, t3, { index: 6 })).hop1.index, '6');
  assert.equal((await client.handleFromTx(97, t3, { logIndex: 0 })).hop1.index, '5');
  await assert.rejects(() => client.handleFromTx(97, t3, { index: 42 }), (e: any) => e instanceof SourceTransferNotFoundError, 'a selector matching none is an error');
  // same index on two different destinations: index alone stays ambiguous, logIndex resolves
  const t4 = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 3, wallet.address, '0x', bridge), BI(hubId, 3, wallet.address, '0x', bridge, amount + 1n)]);
  await assert.rejects(() => client.handleFromTx(97, t4, { index: 3 }), (e: any) => e instanceof AmbiguousSourceError);
  assert.equal((await client.handleFromTx(97, t4, { index: 3, logIndex: 1 })).expect.value, (amount + 1n).toString());
});

test('handleFromTx: bad nested hop-2 destination => UnsupportedRouteError; unknown target/selector => UnsupportedRouteError; route override mismatch => RouteMismatchError; failed source => SourceTxFailedError', async () => {
  const { w, c, wallet, client } = setup();
  const bsc = w.chains.bsc;
  const bridge = c.bsc.bridge!;
  const hubId = c.one.chainId;
  const hub = c.one.bridge!;
  const fwd = (destChainId: number, inner = '0x') => encodeForwardExtraData({ hub, destChainId, toDest: RECIPIENT, value2: amount, networkFee2: 0n, exFee2Declared: 0n, inner });
  const bad = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 0, wallet.address, fwd(1), bridge)]);
  await assert.rejects(() => client.handleFromTx(97, bad), (e: any) => e instanceof UnsupportedRouteError && /hop-2 destination chain 1/.test(e.message));
  const sameChain = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 1, wallet.address, fwd(c.bsc.chainId), bridge)]);
  await assert.rejects(() => client.handleFromTx(97, sameChain), UnsupportedRouteError, 'BSC -> hub -> BSC is not a route');
  const nestedBad = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 2, wallet.address, fwd(c.hyperevm.chainId, '0x' + 'ab'.repeat(30)), bridge)]);
  await assert.rejects(() => client.handleFromTx(97, nestedBad), (e: any) => e instanceof UnsupportedRouteError && /nested/.test(e.message));
  const unknown = bsc.mineLogs(wallet.address, bridge, '0x', [BI(hubId, 3, wallet.address, '0x' + '12'.repeat(20) + 'deadbeef' + '00'.repeat(8), bridge)]);
  await assert.rejects(() => client.handleFromTx(97, unknown), (e: any) => e instanceof UnsupportedRouteError && e.selector === '0xdeadbeef');
  // route override that contradicts the receipt
  const q = await client.quote({ route: Route.BSC_TO_ONE, amount, sender: wallet.address, recipient: RECIPIENT });
  const sent = await client.send(wallet, q);
  await assert.rejects(() => client.handleFromTx(97, sent.hash, { route: Route.ONE_TO_BSC }), (e: any) => e instanceof RouteMismatchError && e.given === 'R2' && e.inferred === 'R1');
  // failed source tx (status 0)
  const failed = bsc.applyTx({ from: wallet.address, to: bridge, data: bridgeI.encodeFunctionData('bridgeToken', [hubId, c.bsc.tokens.ONE!.address, RECIPIENT, 1n, 0n, 0n, '0x']), value: 0n });
  assert.equal(bsc.receipts.get(failed.hash)!.status, '0x0');
  await assert.rejects(() => client.handleFromTx(97, failed.hash), (e: any) => e instanceof SourceTxFailedError);
});
