// extraData is decoded, not trusted: delivery is reported to the DECODED recipient and token, and a call that differs from what the SDK builds is unsupported.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { getAddress, toBeHex } from 'ethers';
import { InvalidOptionError, Route, UnsupportedRouteError, encodeCoreExtraData, encodeForwardExtraData, encodeX1ExtraData } from '../src/index.js';
import { E18, NATIVE, RECIPIENT, SENDER, TOKEN_A, bridgeI, sbrI } from './world.js';
import { OTHER, amount, setup, x1World } from './helpers.js';

const ev = (name: string, values: any[], address: string) => { const e = bridgeI.encodeEventLog(name, values); return { address, topics: e.topics as string[], data: e.data }; };
const BI = (to: number, idx: number, from: string, extraData: string, address: string, value = amount, rcpt = RECIPIENT) => ev('BridgeInitiated', [to, idx, NATIVE, NATIVE, from, rcpt, value, 0n, 0n, extraData, 1n], address);
test('X1: delivered is reported to the DECODED recipient and tokenOut (not BridgeFinalized.to)', async () => {
  const x = x1World();
  const q = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: OTHER });
  const sent = await x.client.send(x.wallet, q);
  const handle = await sent.wait();
  assert.equal(handle.expect.x1!.recipient, OTHER);
  assert.equal(handle.expect.x1!.tokenOut.toLowerCase(), x.c.cross.tokens.ONEUSD!.address.toLowerCase());
  assert.equal(handle.expect.x1!.amountIn, (100n * E18).toString());
  const hub = x.c.cross.bridge!;
  const idx = Number(handle.hop1.index);
  const EXEC = getAddress('0x00000000000000000000000000000000000000e1');
  x.w.finalizeNext.set(`cross:${x.c.bsc.chainId}`, BigInt(idx) + 1n);
  x.w.chains.cross.mineLogs(SENDER, hub, bridgeI.encodeFunctionData('finalizeBridgeBatch', [[], [], [], []]), [
    ev('ExtraCallExecuted', [x.c.bsc.chainId, idx, x.router, '0x414bf389', true, 100n * E18, '0x' + toBeHex(99n * E18, 32).slice(2)], hub),
    ev('BridgeFinalized', [x.c.bsc.chainId, idx, x.usdtx, EXEC, 100n * E18, 1n], hub),
  ]);
  const r = await x.client.trackOnce(handle);
  assert.equal(r.state, 'DELIVERED');
  assert.equal(r.hops[0].recipient, OTHER);
  assert.equal(r.hops[0].token!.toLowerCase(), x.c.cross.tokens.ONEUSD!.address.toLowerCase());
  assert.equal(r.hops[0].amount, (99n * E18).toString());
  assert.equal((r.hops[0].detail as any).bridgeFinalizedTo, EXEC);
});

test('X1 inference: another tokenOut / a wrong amountIn / a wrong fee / sqrtPriceLimit != 0 / trailing bytes => UnsupportedRouteError; the SDK-built call is accepted', async () => {
  const x = x1World();
  const hub = x.c.cross.chainId;
  const bsc = x.w.chains.bsc;
  const fee = x.c.cross.swap!.x1Pool!.fee;
  const good = { router: x.router, tokenIn: x.usdtx, tokenOut: x.c.cross.tokens.ONEUSD!.address, fee, recipient: RECIPIENT, deadline: 2_000_000_000, amountIn: 100n * E18, amountOutMinimum: 1n };
  const at = (extra: string, i: number) => bsc.mineLogs(x.wallet.address, x.c.bsc.bridge!, '0x', [BI(hub, i, x.wallet.address, extra, x.c.bsc.bridge!, 100n * E18)]);
  const okHash = at(encodeX1ExtraData(good), 0);
  assert.equal((await x.client.handleFromTx('bsc', okHash)).route, Route.BSC_USDT_TO_ONEUSD);
  const cases: [string, string][] = [
    ['tokenOut', encodeX1ExtraData({ ...good, tokenOut: OTHER })],
    ['tokenIn', encodeX1ExtraData({ ...good, tokenIn: OTHER })],
    ['amountIn', encodeX1ExtraData({ ...good, amountIn: 99n * E18 })],
    ['fee', encodeX1ExtraData({ ...good, fee: fee + 1 })],
    ['trailing', encodeX1ExtraData(good) + 'ab'],
  ];
  let i = 1;
  for (const [name, ed] of cases) {
    await assert.rejects(() => x.client.handleFromTx('bsc', at(ed, i++)), (e: any) => e instanceof UnsupportedRouteError && /unsupported X1 extraData/.test(e.message), name);
  }
});

test('X2 inference: encoded amount != committed amount => unsupported (direct R3 and nested R5); a matching call yields the decoded recipient', async () => {
  const { w, c, wallet, client } = setup();
  const he = c.hyperevm.tokens.ONE!.address;
  const hubBridge = c.cross.bridge!;
  const direct = (value: bigint, committed = amount) => w.chains.cross.mineLogs(SENDER, hubBridge, '0x', [BI(c.hyperevm.chainId, 0, SENDER, encodeCoreExtraData({ token: he, coreRecipient: OTHER, value }), hubBridge, committed)]);
  const ok = await client.handleFromTx('cross', direct(amount));
  assert.equal(ok.route, Route.ONE_TO_HYPERCORE);
  assert.deepEqual(ok.expect.x2, { token: getAddress(he), coreRecipient: OTHER, encodedAmount: amount.toString() });
  await assert.rejects(() => client.handleFromTx('cross', direct(amount + 1n)), (e: any) => e instanceof UnsupportedRouteError && /unsupported X2 extraData/.test(e.message));
  // nested (R5): the committed amount is the forward payload value
  const fwd = (inner: string) => encodeForwardExtraData({ hub: hubBridge, destChainId: c.hyperevm.chainId, toDest: RECIPIENT, value2: amount, networkFee2: 0n, exFee2Declared: 0n, inner });
  const nested = (value: bigint) => w.chains.bsc.mineLogs(wallet.address, c.bsc.bridge!, '0x', [BI(c.cross.chainId, 0, wallet.address, fwd(encodeCoreExtraData({ token: he, coreRecipient: OTHER, value })), c.bsc.bridge!, amount + 1n)]);
  const h5 = await client.handleFromTx('bsc', nested(amount));
  assert.equal(h5.route, Route.BSC_TO_ONE_TO_HYPEREVM);
  assert.equal(h5.expect.x2!.coreRecipient, OTHER);
  await assert.rejects(() => client.handleFromTx('bsc', nested(amount + 7n)), (e: any) => e instanceof UnsupportedRouteError && /unsupported X2 extraData/.test(e.message));
});

test('X3: non-empty extraData is rejected at quote time and at inference (SwapBridge + BridgeInitiated pair)', async () => {
  const { w, c, wallet, client } = setup();
  const usdtx = c.cross.tokens.USDTx!.address;
  const req: any = { route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc', extraData: '0x1234' } };
  await assert.rejects(() => client.quote(req), (e: any) => e instanceof InvalidOptionError && /extraData/.test(e.message));
  const router = c.cross.swap!.swapBridgeRouter!;
  const pair = (idx: number, extraData: string) => w.chains.cross.mineLogs(SENDER, router, '0x', [
    { address: router, ...(() => { const e = sbrI.encodeEventLog('SwapBridge', [SENDER, TOKEN_A, usdtx, 50n * E18, 49n * E18, c.bsc.chainId, idx, 48n * E18, 0n, 0n]); return { topics: e.topics as string[], data: e.data }; })() },
    { address: c.cross.bridge!, ...(() => { const e = bridgeI.encodeEventLog('BridgeInitiated', [c.bsc.chainId, idx, usdtx, usdtx, router, RECIPIENT, 48n * E18, 0n, 0n, extraData, 1n]); return { topics: e.topics as string[], data: e.data }; })() },
  ]);
  assert.equal((await client.handleFromTx('cross', pair(0, '0x'))).route, Route.SWAP_BRIDGE);
  await assert.rejects(() => client.handleFromTx('cross', pair(1, '0x1234')), (e: any) => e instanceof UnsupportedRouteError && /X3 with non-empty extraData/.test(e.message));
});
