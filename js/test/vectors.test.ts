// Pure unit tests against the shared golden vectors (tools/vectors/vectors.json).
// The Go SDK runs the SAME file; both must reproduce it byte for byte.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import {
  AddressNotConfiguredError, BridgeClient, Route, coreAmounts, encodeBridgeToken, encodeCoreExtraData, encodeForwardExtraData, encodeSwapBridge,
  encodeX1ExtraData, expectedBridgeAmount, floorBps, multihopAmounts, resolveForwardAmounts,
} from '../src/index.js';

const V = JSON.parse(readFileSync(new URL('../../test/vectors.json', import.meta.url), 'utf8'));
const big = (s: string) => BigInt(s);

const bridgeIn = (i: any) => ({ toChainId: i.toChainId, fromToken: i.fromToken, to: i.to, value: big(i.value), networkFee: big(i.networkFee), exFee: big(i.exFee), extraData: i.extraData });
const fwdIn = (f: any) => ({ hub: f.hub, destChainId: f.destChainId, toDest: f.toDest, value2: big(f.value2), networkFee2: big(f.networkFee2), exFee2Declared: big(f.exFee2Declared), inner: f.inner });
const coreIn = (c: any) => ({ token: c.token, coreRecipient: c.coreRecipient, value: big(c.value) });

test('encoding vectors: every route is byte-identical to cast', () => {
  for (const v of V.encoding) {
    switch (v.kind) {
      case 'bridgeToken':
        assert.equal(encodeBridgeToken(bridgeIn(v.inputs)), v.expected.calldata, v.name);
        break;
      case 'multihop': {
        let inner = '0x';
        if (v.inputs.core) {
          inner = encodeCoreExtraData(coreIn(v.inputs.core));
          assert.equal(inner, v.expected.extraData2, `${v.name} extraData2`);
          assert.equal((inner.length - 2) / 2, v.expected.extraData2Bytes);
        }
        const ed1 = encodeForwardExtraData({ ...fwdIn(v.inputs.forward), inner });
        assert.equal(ed1, v.expected.extraData1, `${v.name} extraData1`);
        assert.equal((ed1.length - 2) / 2, v.expected.extraData1Bytes);
        assert.equal(encodeBridgeToken({ ...bridgeIn(v.inputs.call), extraData: ed1 }), v.expected.calldata, `${v.name} calldata`);
        break;
      }
      case 'core': {
        const ed = encodeCoreExtraData(coreIn(v.inputs.core));
        assert.equal(ed, v.expected.extraData);
        assert.equal(encodeBridgeToken({ ...bridgeIn(v.inputs.call), extraData: ed }), v.expected.calldata);
        break;
      }
      case 'x1': {
        const s = v.inputs.swap;
        const ed = encodeX1ExtraData({ router: s.router, tokenIn: s.tokenIn, tokenOut: s.tokenOut, fee: s.fee, recipient: s.recipient, deadline: s.deadline, amountIn: big(s.amountIn), amountOutMinimum: big(s.amountOutMinimum) });
        assert.equal(ed, v.expected.extraData);
        assert.ok(ed.toLowerCase().includes('414bf389'));
        assert.equal(encodeBridgeToken({ ...bridgeIn(v.inputs.call), extraData: ed }), v.expected.calldata);
        break;
      }
      case 'x3':
      case 'x3eth': {
        const i = v.inputs;
        assert.equal(encodeSwapBridge({ tokenIn: i.tokenIn, tokenOut: i.tokenOut, fee: i.fee, amountIn: big(i.amountIn), amountOutMinimum: big(i.amountOutMinimum), toChainId: i.toChainId, recipient: i.recipient, extraData: i.extraData, deadline: i.deadline, native: v.kind === 'x3eth' }), v.expected.calldata, v.name);
        break;
      }
      default: assert.fail(`unknown vector kind ${v.kind}`);
    }
  }
});

test('extraData reference lengths: extraData2 = 88 B, extraData1 = 344 B', () => {
  const v = V.encoding.find((e: any) => e.name === 'R5-X2-testnet-reference-lengths');
  assert.equal(v.expected.extraData2Bytes, 88);
  assert.equal(v.expected.extraData1Bytes, 344);
});

test('CrossBridge._resolveForwardAmounts mirror reproduces the Solidity-generated raw vectors', () => {
  for (const v of V.forwardMath) {
    const r = resolveForwardAmounts(big(v.ctxValue), big(v.quotedValue), big(v.networkFee), big(v.rate), big(v.denominator));
    assert.equal(r.reverted, v.reverted, v.name);
    if (!v.reverted && !r.reverted) {
      assert.equal(r.value.toString(), v.value, `${v.name} value`);
      assert.equal(r.exFee.toString(), v.exFee, `${v.name} exFee`);
    } else {
      assert.equal(v.selector, '0x8399ac8d'); // BaseBridgeForwardStagedInsufficient
    }
  }
});

test('multihop (R5/R6): quote-side numbers and hub re-settlement reproduce the Solidity vectors', () => {
  assert.ok(V.multihop.length >= 16);
  for (const v of V.multihop) {
    const hubFee = { minimumValue: big(v.hubQuote.minimumValue), networkFee: big(v.hubQuote.networkFee), exFee: big(v.hubQuote.exFee) };
    const m = multihopAmounts(big(v.v2), hubFee, big(v.buffer));
    assert.equal(m.ef2Declared.toString(), v.ef2Declared, `${v.name} ef2'`);
    assert.equal(m.v1.toString(), v.v1, `${v.name} v1`);
    // source side: total = v1 + nf1 + ef1 (fee triple from the source verifier, as generated)
    assert.equal((m.v1 + big(v.srcQuote.networkFee) + big(v.srcQuote.exFee)).toString(), v.totalSpend, `${v.name} total`);
    // hub execution time
    const r = resolveForwardAmounts(m.v1, big(v.v2), big(v.exec.networkFee), big(v.exec.rate), big(v.exec.denominator));
    assert.equal(r.reverted, v.settle.reverted, v.name);
    if (!r.reverted) {
      assert.equal(r.value.toString(), v.settle.value, `${v.name} settled value`);
      assert.equal(r.exFee.toString(), v.settle.exFee, `${v.name} settled exFee`);
      // the hub's own post-check (verifier recomputation) as the SDK documents it
      const exCalc = (r.value * big(v.exec.rate)) / big(v.exec.denominator);
      const ok = r.value >= big(v.verifierCheck.minimumValue) && big(v.exec.networkFee) >= big(v.verifierCheck.networkFee) && r.exFee >= exCalc;
      assert.equal(ok, v.verifierCheck.ok, `${v.name} verifier check`);
      assert.equal(exCalc.toString(), v.verifierCheck.exFee, `${v.name} exFee recomputed`);
      assert.ok(r.value >= big(v.v2), 'v2 is a floor: delivered >= v2');
    } else {
      assert.equal(v.settle.selector, '0x8399ac8d');
    }
  }
});

test('multihop: the buffer lives in the declared exFee, never in value', () => {
  const m = multihopAmounts(10n * 10n ** 18n, { minimumValue: 5n * 10n ** 18n, networkFee: 18n * 10n ** 16n, exFee: 0n }, 5n * 10n ** 16n);
  assert.equal(m.ef2Declared, 5n * 10n ** 16n);
  assert.equal(m.v1, 10n * 10n ** 18n + 18n * 10n ** 16n + 5n * 10n ** 16n);
  assert.throws(() => multihopAmounts(1n, { minimumValue: 5n, networkFee: 0n, exFee: 0n }, 0n), /below the hub minimumValue/);
});

test('X1 slippage floor', () => {
  for (const v of V.x1Floor) assert.equal(floorBps(big(v.quote), v.slippageBps).toString(), v.amountOutMinimum);
  assert.throws(() => floorBps(1n, 10001));
});

test('X3 floors, incl. a fee that rises while the swap output rises', () => {
  for (const v of V.x3) {
    const qn = big(v.quote.networkFeeRate.nf), qr = big(v.quote.networkFeeRate.rate), min = big(v.quote.min);
    const out = big(v.quote.swapAmountOut);
    const floor = floorBps(out, v.quote.slippageBps);
    assert.equal(floor.toString(), v.expected.amountOutMinimum, v.name);
    const q = expectedBridgeAmount(out, qn, qr, min);
    assert.equal(q.bridgeValue.toString(), v.expected.bridgeValue);
    assert.equal(q.exFee.toString(), v.expected.exFee);
    const minBridge = expectedBridgeAmount(floor, qn, qr, min).bridgeValue;
    assert.equal(minBridge.toString(), v.expected.minBridgeValue);
    const atFresh = expectedBridgeAmount(floor, big(v.fresh.nf), big(v.fresh.rate), min);
    assert.equal(atFresh.bridgeValue.toString(), v.freshCheck.deliveryAtOriginalFloor, v.name);
    assert.equal(atFresh.status === 'Success' && atFresh.bridgeValue >= minBridge, v.freshCheck.deliveryOk, `${v.name} deliveryOk`);
  }
  const ex = V.x3[0];
  assert.equal(ex.freshCheck.deliveryAtOriginalFloor, '94');
  assert.equal(ex.expected.minBridgeValue, '99');
  assert.equal(ex.freshCheck.deliveryOk, false);
});

test('X2 coreUnit rounding', () => {
  for (const v of V.x2Core) {
    const r = coreAmounts(big(v.amount), big(v.coreUnit));
    assert.equal(r.coreAmount.toString(), v.coreAmount);
    assert.equal(r.coreAmountEvm.toString(), v.coreAmountEvm);
    assert.equal(r.dust.toString(), v.dust);
  }
});

test('config: routes with null values are refused at quote() - before any RPC, approve or send', async () => {
  // every provider fails, so the test never reaches a real RPC: the config gate must fire before any network access
  const offline = { send: async () => { throw new Error('offline: no RPC in this test'); } };
  const providers = { one: offline, bsc: offline, hyperevm: offline };
  const c = new BridgeClient({ network: 'mainnet', providers });
  const base = { amount: 10n ** 19n, sender: '0x1111111111111111111111111111111111111111', recipient: '0x2222222222222222222222222222222222222222' };
  // mainnet HyperEVM routes are fully configured: they get past the config gate and fail only for lack of a provider
  for (const route of [Route.ONE_TO_HYPEREVM, Route.HYPEREVM_TO_ONE, Route.BSC_TO_ONE_TO_HYPEREVM, Route.HYPEREVM_TO_ONE_TO_BSC, Route.ONE_TO_HYPERCORE]) {
    await assert.rejects(() => c.quote({ ...base, route }), (e: any) => !(e instanceof AddressNotConfiguredError), route);
  }
  // the mainnet SwapBridgeRouter is configured: X3 gets past the config gate and fails only for lack of a provider
  await assert.rejects(() => c.quote({ ...base, route: Route.SWAP_BRIDGE, x3: { tokenIn: 'native', tokenOut: '0x3333333333333333333333333333333333333333', poolFee: 0, toChain: 'bsc' } }), (e: any) => !(e instanceof AddressNotConfiguredError), 'X3');
  const t = new BridgeClient({ network: 'testnet', providers });
  await assert.rejects(() => t.quote({ ...base, route: Route.BSC_USDT_TO_ONEUSD }), (e: any) => e instanceof AddressNotConfiguredError && /ONEUSD/.test(e.field));
});
