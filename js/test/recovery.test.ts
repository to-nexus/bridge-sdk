// Recovery of a transfer whose outcome is unknown: findSourceTransfer matching and the replacement analysis.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Wallet, getAddress } from 'ethers';
import { BridgeClient, BroadcastOutcomeUnknownError, Route } from '../src/index.js';
import { FakeWalletSigner } from './fake.js';
import { E18, RECIPIENT, TOKEN_A, World, bridgeI } from './world.js';
import { amount, r1, setup } from './helpers.js';

test('findSourceTransfer: NOT_FOUND_YET (same-from log with a different value must not match) / AMBIGUOUS (two identical requests) / nonce info is information only', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const wl = new FakeWalletSigner(wallet.address, w.chains.bsc);
  wl.behavior = 'throw-no-mine';
  w.setAllow('bsc', c.bsc.tokens.ONE!.address, wallet.address, c.bsc.bridge!, 10n ** 30n); // skip approve (wallet would throw on it)
  let err: BroadcastOutcomeUnknownError | undefined;
  try { await client.send(wl, q); } catch (e) { err = e as BroadcastOutcomeUnknownError; }
  assert.ok(err instanceof BroadcastOutcomeUnknownError);
  // someone else's / the same account's DIFFERENT transfer (value differs): same from + toChainID, must NOT match
  const tx = (value: bigint) => w.chains.bsc.applyTx({ from: wallet.address, to: c.bsc.bridge!, data: bridgeI.encodeFunctionData('bridgeToken', [c.one.chainId, c.bsc.tokens.ONE!.address, RECIPIENT, value, 0n, 0n, '0x']), value: 0n });
  tx(amount + 1n);
  let f = await client.findSourceTransfer(err!.intent, err!.checkpoint);
  assert.equal(f.result.status, 'NOT_FOUND_YET');
  assert.ok(f.nonceInfo.minedNonce > f.nonceInfo.pendingNonceBefore, 'nonce advanced - but that alone is NOT evidence');
  // two identical requests -> ambiguous, the SDK will not pick one
  tx(amount); tx(amount);
  f = await client.findSourceTransfer(err!.intent, err!.checkpoint);
  assert.equal(f.result.status, 'AMBIGUOUS');
  assert.equal((f.result as any).candidates.length, 2);
});

test('replacement analysis (Mode L): speed-up = FOUND, cancel / failed replacement = new tx allowed, different transfer = reported, never auto-safe', async () => {
  const mk = async () => {
    const t = setup();
    const q = await t.client.quote(r1(t.wallet.address));
    t.w.setAllow('bsc', t.c.bsc.tokens.ONE!.address, t.wallet.address, t.c.bsc.bridge!, 10n ** 30n);
    t.w.chains.bsc.sendMode = 'pending'; // accepted by the node, never mined
    const sent = await t.client.send(t.wallet, q);
    t.w.chains.bsc.sendMode = 'mine';
    return { ...t, sent, q };
  };
  const callData = (t: any, value: bigint) => bridgeI.encodeFunctionData('bridgeToken', [t.c.one.chainId, t.c.bsc.tokens.ONE!.address, RECIPIENT, value, 0n, 0n, '0x']);
  const analyze = (t: any, replacementHash?: string) => t.client.analyzeReplacement({ intent: t.sent.intent, checkpoint: t.sent.checkpoint, original: { hash: t.sent.hash, nonce: t.sent.nonce }, replacementHash });

  { // original still possible: nonce not consumed
    const t = await mk();
    assert.equal((await analyze(t)).verdict, 'ORIGINAL_STILL_POSSIBLE');
  }
  { // 0-value self transfer cancel (status 1, no bridge event)
    const t = await mk();
    const rep = t.w.chains.bsc.applyTx({ from: t.wallet.address, to: t.wallet.address, data: '0x', value: 0n, nonce: t.sent.nonce });
    const v = await analyze(t, rep.hash);
    assert.deepEqual([v.verdict, (v as any).reason], ['NEW_TX_ALLOWED', 'replacement-not-a-transfer']);
    // and block scanning locates it without the hash too
    assert.equal((await analyze(t)).verdict, 'NEW_TX_ALLOWED');
  }
  { // failed replacement (status 0)
    const t = await mk();
    const rep = t.w.chains.bsc.applyTx({ from: t.wallet.address, to: t.c.bsc.bridge!, data: callData(t, 1n), value: 0n, nonce: t.sent.nonce }); // value below minimum -> reverts
    assert.equal(t.w.chains.bsc.receipts.get(rep.hash)!.status, '0x0');
    const v = await analyze(t, rep.hash);
    assert.deepEqual([v.verdict, (v as any).reason], ['NEW_TX_ALLOWED', 'replacement-failed']);
  }
  { // wallet "speed-up": the replacement IS the same transfer
    const t = await mk();
    const rep = t.w.chains.bsc.applyTx({ from: t.wallet.address, to: t.c.bsc.bridge!, data: t.sent.intent.data, value: 0n, nonce: t.sent.nonce });
    const v = await analyze(t, rep.hash);
    assert.equal(v.verdict, 'FOUND');
    assert.equal((v as any).txHash.toLowerCase(), rep.hash.toLowerCase());
  }
  { // replacement executes a DIFFERENT bridge request
    const t = await mk();
    const rep = t.w.chains.bsc.applyTx({ from: t.wallet.address, to: t.c.bsc.bridge!, data: callData(t, amount + 7n), value: 0n, nonce: t.sent.nonce });
    assert.equal((await analyze(t, rep.hash)).verdict, 'REPLACED_BY_DIFFERENT_TRANSFER');
  }
  { // the nonce is consumed by something we cannot locate in the scan window: NOT safe
    const t = await mk();
    t.w.chains.bsc.nonces.set(t.wallet.address.toLowerCase(), (t.sent.nonce ?? 0) + 1);
    assert.equal((await analyze(t)).verdict, 'ORIGINAL_STILL_POSSIBLE');
  }
});

test('X3 recovery without a hash goes through SwapBridge (bridge-level from is the router)', async () => {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  const usdtx = c.one.tokens.USDTx!.address;
  w.setFee('one', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  const client = new BridgeClient(w.options());
  const q = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } });
  w.setAllow('one', TOKEN_A, wallet.address, c.one.swap!.swapBridgeRouter!, 10n ** 30n);
  const wl = new FakeWalletSigner(wallet.address, w.chains.one);
  wl.behavior = 'throw-after-mine';
  let err: BroadcastOutcomeUnknownError | undefined;
  try { await client.send(wl, q); } catch (e) { err = e as BroadcastOutcomeUnknownError; }
  assert.ok(err instanceof BroadcastOutcomeUnknownError);
  const f = await client.findSourceTransfer(err!.intent, err!.checkpoint);
  assert.equal(f.result.status, 'FOUND');
  const h = (f.result as any).handle;
  assert.equal(h.route, Route.SWAP_BRIDGE);
  assert.equal(h.srcContract.toLowerCase(), c.one.swap!.swapBridgeRouter!.toLowerCase());
  assert.equal(h.hop1.destChainId, c.bsc.chainId);
});

// Source-transfer matching: fees match when event.fee <= intent.fee (BaseBridge emits the verifier's calculated fees, which are
// always <= the calldata fees of a successful tx); every other field must match exactly.
test('findSourceTransfer fee rule: lower event fees => FOUND; higher => no match; equal fees but different to/value => no match', async () => {
  const mk = async () => {
    const { w, c, wallet, client } = setup();
    const one = c.bsc.tokens.ONE!.address;
    w.setFee('bsc', c.one.chainId, one, { min: 1n * E18, nf: 2n * E18, rate: 0n });
    const q = await client.quote(r1(wallet.address));
    w.setAllow('bsc', one, wallet.address, c.bsc.bridge!, 10n ** 30n);
    const wl = new FakeWalletSigner(wallet.address, w.chains.bsc);
    wl.behavior = 'throw-no-mine';
    let err: BroadcastOutcomeUnknownError | undefined;
    try { await client.send(wl, q); } catch (e) { err = e as BroadcastOutcomeUnknownError; }
    assert.ok(err instanceof BroadcastOutcomeUnknownError);
    assert.equal(err!.intent.match.kind === 'bridge' && err!.intent.match.networkFee, 2n * E18);
    const tx = (to: string, value: bigint, nf: bigint) => w.chains.bsc.applyTx({ from: wallet.address, to: c.bsc.bridge!, data: bridgeI.encodeFunctionData('bridgeToken', [c.one.chainId, one, to, value, nf, 0n, '0x']), value: 0n });
    return { w, c, wallet, client, err: err!, tx, one };
  };
  { // (1) fees dropped before inclusion: the event carries nf = 1 < intent nf = 2 -> FOUND
    const t = await mk();
    t.w.setFee('bsc', t.c.one.chainId, t.one, { min: 1n * E18, nf: 1n * E18, rate: 0n });
    t.tx(RECIPIENT, amount, 2n * E18); // calldata keeps the intent's fee; the contract emits the calculated (lower) one
    const log = t.w.chains.bsc.logs.at(-1)!;
    assert.equal(bridgeI.parseLog({ topics: log.topics, data: log.data })!.args[7], 1n * E18);
    const f = await t.client.findSourceTransfer(t.err.intent, t.err.checkpoint);
    assert.equal(f.result.status, 'FOUND');
  }
  { // (2) the event carries a HIGHER fee than the intent (nf = 3 > 2): cannot be this transfer
    const t = await mk();
    t.w.setFee('bsc', t.c.one.chainId, t.one, { min: 1n * E18, nf: 3n * E18, rate: 0n });
    t.tx(RECIPIENT, amount, 3n * E18);
    const f = await t.client.findSourceTransfer(t.err.intent, t.err.checkpoint);
    assert.equal(f.result.status, 'NOT_FOUND_YET');
  }
  { // (3) equal fees, but `to` or `value` differs: not matched
    const t = await mk();
    t.tx(getAddress('0x4444444444444444444444444444444444444444'), amount, 2n * E18);
    t.tx(RECIPIENT, amount + 1n, 2n * E18);
    const f = await t.client.findSourceTransfer(t.err.intent, t.err.checkpoint);
    assert.equal(f.result.status, 'NOT_FOUND_YET');
  }
});
