// Slipstream CL (dex "cl") support: encoding vectors cross-checked against `cast calldata`, config defaulting, the X1 / X3 flows through the fake
// RPC world, and the rejection of the other DEX's selector. Mirrors go/bridgesdk/cl_test.go.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Wallet, getAddress, toBeHex } from 'ethers';
import {
  AddressNotConfiguredError, BridgeClient, CL_EXACT_INPUT_SINGLE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR, INT24_MAX, INT24_MIN, InvalidOptionError, NetworkMismatchError, Route,
  UnsupportedRouteError, dexOf, encodeSwapBridge, encodeX1ExtraData, getNetwork, getNetworksData,
} from '../src/index.js';
import type { NetworksData } from '../src/index.js';
import { clQuoterIface, swapBridgeRouterCLIface, swapBridgeRouterIface } from '../src/encoding.js';
import { checkX1, decodeX1Struct } from '../src/extra.js';
import { E18, RECIPIENT, TOKEN_A, World, bridgeI } from './world.js';
import { x1World, x3req } from './helpers.js';

// Expected calldata produced with foundry `cast calldata` (A = 0x11..11, B = 0x22..22, C = 0x33..33, deadline 1800000000, amountIn 100e18,
// amountOutMinimum 99e18, quote amountIn 1e18, toChainID 56). Same constants as the Go test; do not regenerate them with the SDK encoders.
const CAST = {
  clExactInputSingle200: '0xa026383e0000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000000c80000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000',
  clExactInputSingleMin: '0xa026383e00000000000000000000000011111111111111111111111111111111111111110000000000000000000000002222222222222222222222222222222222222222ffffffffffffffffffffffffffffffffffffffffffffffffffffffffff8000000000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000',
  clExactInputSingleMax: '0xa026383e0000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000007fffff0000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000',
  v3ExactInputSingle500: '0x414bf3890000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000001f40000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000',
  clQuote200: '0x9e7defe6000000000000000000000000111111111111111111111111111111111111111100000000000000000000000022222222222222222222222222222222222222220000000000000000000000000000000000000000000000000de0b6b3a764000000000000000000000000000000000000000000000000000000000000000000c80000000000000000000000000000000000000000000000000000000000000000',
  clQuoteNeg1: '0x9e7defe6000000000000000000000000111111111111111111111111111111111111111100000000000000000000000022222222222222222222222222222222222222220000000000000000000000000000000000000000000000000de0b6b3a7640000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff0000000000000000000000000000000000000000000000000000000000000000',
  sbrCL200: '0x49d9a7f70000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000000c80000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000038000000000000000000000000333333333333333333333333333333333333333300000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000000',
  sbrCLETHNeg100: '0xa26337a60000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000006b49d20000000000000000000000000011111111111111111111111111111111111111110000000000000000000000002222222222222222222222222222222222222222ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff9c0000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000038000000000000000000000000333333333333333333333333333333333333333300000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000000',
  getAmountOut200: '0xdf7da91c00000000000000000000000000000000000000000000000000000000000000380000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000000c80000000000000000000000000000000000000000000000000de0b6b3a7640000',
  getAmountOutNeg100: '0xdf7da91c000000000000000000000000000000000000000000000000000000000000003800000000000000000000000011111111111111111111111111111111111111110000000000000000000000002222222222222222222222222222222222222222ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff9c0000000000000000000000000000000000000000000000000de0b6b3a7640000',
};
const A = getAddress('0x1111111111111111111111111111111111111111');
const B = getAddress('0x2222222222222222222222222222222222222222');
const C = getAddress('0x3333333333333333333333333333333333333333');
const fake = (h: string) => getAddress('0x' + h.padStart(40, '0'));

test('CL selectors and ABI: router / quoter / wrapper selectors; SwapBridge event identical in both wrappers', () => {
  assert.equal(EXACT_INPUT_SINGLE_SELECTOR, '0x414bf389');
  assert.equal(CL_EXACT_INPUT_SINGLE_SELECTOR, '0xa026383e');
  assert.equal(clQuoterIface.getFunction('quoteExactInputSingle')!.selector, '0x9e7defe6');
  assert.equal(swapBridgeRouterCLIface.getFunction('swapBridgeExactInputSingle')!.selector, '0x49d9a7f7');
  assert.equal(swapBridgeRouterCLIface.getFunction('swapBridgeExactInputSingleETH')!.selector, '0xa26337a6');
  assert.equal(swapBridgeRouterCLIface.getFunction('getAmountSwapBridgeOut')!.selector, '0xdf7da91c');
  assert.equal(swapBridgeRouterCLIface.getFunction('getExpectedBridgeAmount')!.selector, swapBridgeRouterIface.getFunction('getExpectedBridgeAmount')!.selector);
  assert.equal(swapBridgeRouterCLIface.getEvent('SwapBridge')!.topicHash, swapBridgeRouterIface.getEvent('SwapBridge')!.topicHash);
});

test('CL encoding vectors match `cast calldata` (incl. negative / int24 bounds); the int24 range is enforced, never truncated', () => {
  const x1 = (o: object) => {
    const ed = encodeX1ExtraData({ router: C, tokenIn: A, tokenOut: B, fee: 0, recipient: C, deadline: 1_800_000_000, amountIn: 100n * E18, amountOutMinimum: 99n * E18, ...o });
    assert.equal(ed.slice(0, 42).toLowerCase(), C.toLowerCase());
    return '0x' + ed.slice(42);
  };
  assert.equal(x1({ dex: 'cl', tickSpacing: 200 }), CAST.clExactInputSingle200);
  assert.equal(x1({ dex: 'cl', tickSpacing: INT24_MIN }), CAST.clExactInputSingleMin);
  assert.equal(x1({ dex: 'cl', tickSpacing: INT24_MAX }), CAST.clExactInputSingleMax);
  assert.equal(x1({ dex: 'v3', fee: 500 }), CAST.v3ExactInputSingle500);
  assert.equal(x1({ fee: 500 }), CAST.v3ExactInputSingle500, 'default dex is V3');
  assert.equal(clQuoterIface.encodeFunctionData('quoteExactInputSingle', [[A, B, E18, 200, 0]]), CAST.clQuote200);
  assert.equal(clQuoterIface.encodeFunctionData('quoteExactInputSingle', [[A, B, E18, -1, 0]]), CAST.clQuoteNeg1);
  const sb = (native: boolean, tickSpacing: number) => encodeSwapBridge({ tokenIn: A, tokenOut: B, fee: 0, amountIn: 100n * E18, amountOutMinimum: 99n * E18, toChainId: 56, recipient: C, extraData: '0x', deadline: 1_800_000_000, native, dex: 'cl', tickSpacing });
  assert.equal(sb(false, 200), CAST.sbrCL200);
  assert.equal(sb(true, -100), CAST.sbrCLETHNeg100);
  assert.equal(swapBridgeRouterCLIface.encodeFunctionData('getAmountSwapBridgeOut', [56, A, B, 200, E18]), CAST.getAmountOut200);
  assert.equal(swapBridgeRouterCLIface.encodeFunctionData('getAmountSwapBridgeOut', [56, A, B, -100, E18]), CAST.getAmountOutNeg100);
  for (const ts of [INT24_MAX + 1, INT24_MIN - 1, 1.5]) {
    assert.throws(() => x1({ dex: 'cl', tickSpacing: ts }), RangeError, `X1 tickSpacing ${ts}`);
    assert.throws(() => sb(false, ts), RangeError, `X3 tickSpacing ${ts}`);
  }
  assert.throws(() => x1({ dex: 'v4' }), RangeError);
  // structural decoding: a negative tickSpacing is not a real pool; a valid CL call decodes with dex + tickSpacing
  assert.throws(() => decodeX1Struct(C + CAST.clExactInputSingleMin.slice(2)), (e: any) => e instanceof UnsupportedRouteError && /tickSpacing/.test(e.message));
  const x = decodeX1Struct(C + CAST.clExactInputSingle200.slice(2));
  assert.deepEqual([x.dex, x.tickSpacing, x.fee], ['cl', 200, 0]);
  assert.equal(decodeX1Struct(C + CAST.v3ExactInputSingle500.slice(2)).dex, undefined);
});

test('dex config: absent = v3 (mainnet), testnet stays v3 with a verified cl block and a pending CL wrapper; unknown dex refused', () => {
  const main = getNetwork('mainnet'), testnet = getNetwork('testnet');
  assert.equal(dexOf(main), 'v3');
  assert.equal(main.chains.one.swap!.dex, undefined);
  assert.equal(main.chains.one.swap!.cl, undefined);
  assert.equal(dexOf(testnet), 'v3');
  const cl = testnet.chains.one.swap!.cl!;
  assert.ok(cl.swapRouter && cl.quoterV2 && cl.factory);
  assert.equal(cl.swapBridgeRouter, null);
  assert.equal(cl.x1Pool!.tickSpacing, 200);
  assert.match(testnet.chains.one.todo['swap.cl.swapBridgeRouter'], /TODO\(PENDING\)/);
  const bad = structuredClone(getNetworksData()) as NetworksData;
  (bad.networks.mainnet.chains.one.swap as any).dex = 'v4';
  assert.throws(() => dexOf(bad.networks.mainnet), RangeError);
});

/** networks data with mainnet (or testnet) switched to dex "cl" */
function clData(network: 'mainnet' | 'testnet', sbr: string | null): NetworksData {
  const d = structuredClone(getNetworksData()) as NetworksData;
  const sw = d.networks[network].chains.one.swap!;
  sw.dex = 'cl';
  if (network === 'mainnet') sw.cl = { swapRouter: fake('c1a0001'), quoterV2: fake('c1a0002'), factory: fake('c1a0003'), swapBridgeRouter: sbr, x1Pool: { tokenIn: 'USDTx', tokenOut: 'ONEUSD', tickSpacing: 200, pool: fake('c1a0005') } };
  else sw.cl!.swapBridgeRouter = sbr;
  return d;
}

function clX1World() {
  const w = new World('mainnet', clData('mainnet', fake('c1a0004')));
  const c = w.net.chains;
  const usdt = c.bsc.tokens.USDT!.address, usdtx = c.one.tokens.USDTx!.address, router = c.one.swap!.cl!.swapRouter!;
  w.setPair('bsc', c.one.chainId, usdt, usdtx, true);
  w.setFee('bsc', c.one.chainId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`one:${router.toLowerCase()}`);
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', usdt, wallet.address, 1000n * E18);
  w.x1Out = 100n * E18 + 3_511_957n;
  return { w, c, wallet, usdtx, router, client: new BridgeClient(w.options({ quoteTtlMs: 60_000 })) };
}
const x1Req = (sender: string) => ({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender, recipient: RECIPIENT });
const extraOf = (data: string) => String(bridgeI.parseTransaction({ data })!.args[6]);

test('testnet: the cl block changes no route (X1 closed, X3 on the V3 wrapper); switched to dex cl, X3 needs the pending CL wrapper', async () => {
  const w = World.testnet();
  const c = w.net.chains;
  const client = new BridgeClient(w.options());
  w.setBal('one', TOKEN_A, RECIPIENT, 1000n * E18);
  w.setFee('one', c.bsc.chainId, c.one.tokens.USDTx!.address, { min: 1n, nf: 0n, rate: 0n });
  await assert.rejects(() => client.quote(x1Req(RECIPIENT)), (e: any) => e instanceof AddressNotConfiguredError && e.field === 'one.tokens.ONEUSD');
  const q = await client.quote(x3req(RECIPIENT, c.one.tokens.USDTx!.address));
  assert.equal(q.tx.to.toLowerCase(), c.one.swap!.swapBridgeRouter!.toLowerCase());
  assert.equal(q.tx.data.slice(0, 10), swapBridgeRouterIface.getFunction('swapBridgeExactInputSingle')!.selector);
  assert.equal((q.request.x3 as any).tickSpacing, undefined);
  const fw = World.testnet(clData('testnet', null));
  const fc = new BridgeClient(fw.options());
  const req = x3req(RECIPIENT, c.one.tokens.USDTx!.address);
  await assert.rejects(() => fc.quote({ ...req, x3: { ...req.x3, tickSpacing: 200 } }),
    (e: any) => e instanceof AddressNotConfiguredError && e.field === 'one.swap.cl.swapBridgeRouter' && /TODO\(PENDING\)/.test(e.todo));
  await assert.rejects(() => fc.quote(x1Req(RECIPIENT)), (e: any) => e instanceof AddressNotConfiguredError && e.field === 'one.tokens.ONEUSD');
});

test('CL X1: quote (CL quoter, ts 200) -> CL router || 0xa026383e, preflight (whitelist, CL factory), send, handle, tracking by selector', async () => {
  const x = clX1World();
  const q = await x.client.quote(x1Req(x.wallet.address));
  const ed = extraOf(q.tx.data);
  assert.equal(ed.slice(0, 42).toLowerCase(), x.router.toLowerCase());
  assert.equal('0x' + ed.slice(42, 50), CL_EXACT_INPUT_SINGLE_SELECTOR);
  assert.deepEqual(x.w.clTicks, [200]);
  const pf = await x.client.preflight(q);
  assert.ok(pf.ok, JSON.stringify(pf.issues));
  assert.equal(x.w.clTicks[x.w.clTicks.length - 1], 200, 'preflight calls CL factory.getPool with tickSpacing 200');
  x.w.execWhitelist.delete(`one:${x.router.toLowerCase()}`);
  assert.ok((await x.client.preflight(q)).issues.some((i) => i.code === 'EXECUTOR_NOT_WHITELISTED'));
  x.w.execWhitelist.add(`one:${x.router.toLowerCase()}`);
  x.w.poolMissing = true;
  assert.ok((await x.client.preflight(q)).issues.some((i) => i.code === 'POOL_NOT_FOUND'));
  x.w.poolMissing = false;

  const sent = await x.client.send(x.wallet, q);
  const handle = await sent.wait();
  assert.equal(handle.route, Route.BSC_USDT_TO_ONEUSD);
  assert.deepEqual([handle.expect.x1!.dex, handle.expect.x1!.tickSpacing], ['cl', 200]);
  assert.deepEqual(JSON.parse(JSON.stringify(handle)).expect.x1.tickSpacing, 200);

  const hub = x.c.one.bridge!;
  const idx = Number(handle.hop1.index);
  const EXEC = getAddress('0x00000000000000000000000000000000000000e1');
  const ev = (name: string, values: any[]) => { const e = bridgeI.encodeEventLog(name, values); return { address: hub, topics: e.topics as string[], data: e.data }; };
  const finalize = (selector: string) => {
    x.w.finalizeNext.set(`one:${x.c.bsc.chainId}`, BigInt(idx) + 1n);
    x.w.chains.one.mineLogs(RECIPIENT, hub, bridgeI.encodeFunctionData('finalizeBridgeBatch', [[], [], [], []]), [
      ev('ExtraCallExecuted', [x.c.bsc.chainId, idx, x.router, selector, true, 100n * E18, '0x' + toBeHex(99n * E18, 32).slice(2)]),
      ev('BridgeFinalized', [x.c.bsc.chainId, idx, x.usdtx, EXEC, 100n * E18, 1n]),
    ]);
  };
  finalize(CL_EXACT_INPUT_SINGLE_SELECTOR);
  const r = await x.client.trackOnce(handle);
  assert.equal(r.state, 'DELIVERED');
  assert.equal(r.hops[0].amount, (99n * E18).toString());

  // V3 handles keep their JSON shape
  const v = x1World();
  const vh = await (await v.client.send(v.wallet, await v.client.quote(x1Req(v.wallet.address)))).wait();
  assert.equal('dex' in vh.expect.x1!, false);
  assert.equal('tickSpacing' in vh.expect.x1!, false);
});

test('CL X1: the other DEX selector is refused (decoders, inference, client binding)', async () => {
  const cl = clX1World();
  const v3 = x1World();
  const clQ = await cl.client.quote(x1Req(cl.wallet.address));
  const v3Q = await v3.client.quote(x1Req(v3.wallet.address));
  const clED = extraOf(clQ.tx.data), v3ED = extraOf(v3Q.tx.data);
  assert.throws(() => checkX1(cl.w.net, v3ED, 100n * E18), (e: any) => e instanceof UnsupportedRouteError && /configured dex is "cl"/.test(e.message));
  assert.throws(() => checkX1(v3.w.net, clED, 100n * E18), (e: any) => e instanceof UnsupportedRouteError && /configured dex is "v3"/.test(e.message));
  // a CL call re-targeted at the V3 router is still refused (selector / dex mismatch), also at receipt inference
  const retarget = v3.router + clED.slice(42);
  assert.throws(() => checkX1(v3.w.net, retarget, 100n * E18), (e: any) => e instanceof UnsupportedRouteError && /dex/.test(e.message));
  const e = bridgeI.encodeEventLog('BridgeInitiated', [v3.c.one.chainId, 0, v3.c.bsc.tokens.USDT!.address, v3.usdtx, v3.wallet.address, RECIPIENT, 100n * E18, 0n, 0n, retarget, 1n]);
  const h = v3.w.chains.bsc.mineLogs(v3.wallet.address, v3.c.bsc.bridge!, '0x', [{ address: v3.c.bsc.bridge!, topics: e.topics as string[], data: e.data }]);
  await assert.rejects(() => v3.client.handleFromTx('bsc', h), (err: any) => err instanceof UnsupportedRouteError && /dex/.test(err.message));
  // client binding: a CL quote handed to a V3 client and vice versa
  await assert.rejects(() => v3.client.send(v3.wallet, clQ), NetworkMismatchError);
  await assert.rejects(() => cl.client.send(cl.wallet, v3Q), NetworkMismatchError);
});

test('CL X3: SwapBridgeRouterCL with int24 tickSpacing; request validated against the dex; seal carries tickSpacing', async () => {
  const w = World.testnet(clData('testnet', fake('c1a0004')));
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  w.setFee('one', c.bsc.chainId, c.one.tokens.USDTx!.address, { min: 1n, nf: 0n, rate: 0n });
  const client = new BridgeClient(w.options());
  const sbrCL = c.one.swap!.cl!.swapBridgeRouter!;
  const base = x3req(wallet.address, c.one.tokens.USDTx!.address);
  const q = await client.quote({ ...base, x3: { ...base.x3, tickSpacing: 200 } });
  assert.equal(q.tx.to.toLowerCase(), sbrCL.toLowerCase());
  assert.equal(q.tx.data.slice(0, 10), '0x49d9a7f7');
  assert.equal(Number(swapBridgeRouterCLIface.parseTransaction({ data: q.tx.data })!.args[0][2]), 200);
  assert.equal(w.clTicks[0], 200);
  assert.equal(q.request.x3!.tickSpacing, 200);
  assert.ok((await client.preflight(q)).ok);
  const sent = await client.send(wallet, q);
  const h = await sent.wait();
  assert.equal(h.route, Route.SWAP_BRIDGE);

  await assert.rejects(() => client.quote(base), (e: any) => e instanceof InvalidOptionError && /tickSpacing is required/.test(e.message));
  for (const bad of [0, -200, INT24_MAX + 1]) await assert.rejects(() => client.quote({ ...base, x3: { ...base.x3, tickSpacing: bad } }), InvalidOptionError, `ts ${bad}`);
  await assert.rejects(() => client.quote({ ...base, x3: { ...base.x3, tickSpacing: 200, poolFee: 500 } }), (e: any) => e instanceof InvalidOptionError && /poolFee must be 0/.test(e.message));
  const vw = World.testnet();
  const vc = new BridgeClient(vw.options());
  await assert.rejects(() => vc.quote({ ...base, x3: { ...base.x3, tickSpacing: 200 } }), (e: any) => e instanceof InvalidOptionError && /only valid on a network whose dex is "cl"/.test(e.message));
  // a CL X3 quote handed to a V3 client is a configuration mismatch
  await assert.rejects(() => vc.send(wallet, q), NetworkMismatchError);
});
