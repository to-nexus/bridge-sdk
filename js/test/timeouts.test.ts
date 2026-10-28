// Every RPC call is time-bounded: quote reads, re-quote, broadcast, signer provider reads and tracking return within the configured time on a hung provider.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Transaction, getAddress } from 'ethers';
import { BridgeClient, BroadcastOutcomeUnknownError, Route, RpcTimeoutError } from '../src/index.js';
import { FakeChain, FakeWalletSigner } from './fake.js';
import { RECIPIENT, SENDER, World } from './world.js';
import { amount, nothingSent, r1, realSleep, setup } from './helpers.js';

test('a hung provider: quote reads are bounded by callTimeoutMs (RpcTimeoutError)', async () => {
  const w = World.testnet();
  w.chains.bsc.hang.add('eth_call');
  const client = new BridgeClient(w.options({ callTimeoutMs: 30 }));
  const t0 = Date.now();
  await assert.rejects(() => client.quote(r1(SENDER)), (e: any) => e instanceof RpcTimeoutError && e.method === 'eth_call');
  assert.ok(Date.now() - t0 < 2000);
});

test('send: a requote that hangs aborts before signing the bridge tx (RpcTimeoutError, bridge broadcast count 0)', async () => {
  const { w, wallet } = setup();
  const client = new BridgeClient(w.options({ callTimeoutMs: 40 }));
  const q = await client.quote(r1(wallet.address));
  w.onApprove = () => { w.chains.bsc.hang.add('eth_call'); }; // from here on, reads on the source chain never answer
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof RpcTimeoutError);
  assert.equal(w.chains.bsc.rawSent.length, 1, 'only the approve was broadcast');
  assert.equal(w.chains.bsc.logs.length, 0);
});

test('send: a broadcast that never answers => BroadcastOutcomeUnknownError carrying the hash (cause RpcTimeoutError); broadcast count 1; no retry', async () => {
  const { w, wallet } = setup();
  const client = new BridgeClient(w.options({ callTimeoutMs: 40 }));
  const q = await client.quote({ route: Route.ONE_TO_BSC, amount, sender: wallet.address, recipient: RECIPIENT });
  w.chains.cross.sendMode = 'hang';
  let err: BroadcastOutcomeUnknownError | undefined;
  try { await client.send(wallet, q); } catch (e) { err = e as BroadcastOutcomeUnknownError; }
  assert.ok(err instanceof BroadcastOutcomeUnknownError, 'the existing recovery catch handles it');
  assert.equal(err!.mode, 'L');
  assert.ok(err!.info.hash && err!.info.raw && err!.info.nonce !== undefined);
  assert.ok((err as any).cause instanceof RpcTimeoutError);
  assert.equal(Transaction.from(err!.info.raw!).hash, err!.info.hash);
  await realSleep(120);
  assert.equal(w.chains.cross.rawSent.length, 1, 'exactly one broadcast, never retried');
});

test('Mode W: no default wallet timeout (the prompt may take as long as the user needs); an opt-in walletTimeoutMs => BroadcastOutcomeUnknownError(W)', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote({ route: Route.ONE_TO_BSC, amount, sender: wallet.address, recipient: RECIPIENT }); // native source: no approve step
  const hang = new FakeWalletSigner(wallet.address, w.chains.cross);
  hang.behavior = 'hang';
  const racing = client.send(hang, q).then(() => 'resolved', (e) => `rejected:${(e as Error).name}`);
  const state = await Promise.race([racing, realSleep(150).then(() => 'still-waiting')]);
  assert.equal(state, 'still-waiting', 'no timeout without walletTimeoutMs');
  const w2 = setup();
  const q2 = await w2.client.quote({ route: Route.ONE_TO_BSC, amount, sender: w2.wallet.address, recipient: RECIPIENT });
  const hang2 = new FakeWalletSigner(w2.wallet.address, w2.w.chains.cross);
  hang2.behavior = 'hang';
  await assert.rejects(() => w2.client.send(hang2, q2, { walletTimeoutMs: 30 }), (e: any) => e instanceof BroadcastOutcomeUnknownError && e.mode === 'W');
  assert.equal(hang2.sent.length, 1);
});

test('track(): a hung provider returns within timeoutMs + epsilon as UNKNOWN{reason: timeout} with timedOut', async () => {
  const { w, c, wallet } = setup();
  const client = new BridgeClient(w.options({ callTimeoutMs: 30, now: () => Date.now(), sleep: realSleep }));
  const q = await client.quote(r1(wallet.address));
  const sent = await client.send(wallet, q);
  const handle = await sent.wait();
  w.finalizeNext.set(`cross:${c.bsc.chainId}`, BigInt(handle.hop1.index) + 1n); // the destination moved on, so the tracker must scan logs
  w.chains.cross.hang.add('eth_getLogs');
  const t0 = Date.now();
  const r = await client.track(handle, { timeoutMs: 400, pollMs: 20 });
  const took = Date.now() - t0;
  assert.ok(took < 400 + 600, `returned in ${took} ms`);
  assert.equal(r.state, 'UNKNOWN');
  assert.equal(r.timedOut, true);
  assert.match(r.hops[0].reason!, /^timeout:/);
});

test('track(): a hung SOURCE receipt call is bounded too; a healthy but slow chain still returns the non-terminal state with timedOut', async () => {
  const { w, wallet } = setup();
  const client = new BridgeClient(w.options({ callTimeoutMs: 25, now: () => Date.now(), sleep: realSleep }));
  const q = await client.quote(r1(wallet.address));
  const sent = await client.send(wallet, q);
  const handle = await sent.wait();
  const quick = await client.track(handle, { timeoutMs: 120, pollMs: 20 });
  assert.equal(quick.state, 'IN_FLIGHT', JSON.stringify(quick.hops)); assert.equal(quick.timedOut, true); // nothing finalized yet on the fake destination
  w.chains.bsc.hang.add('eth_getTransactionReceipt');
  const t0 = Date.now();
  const hung = await client.track(handle, { timeoutMs: 200, pollMs: 20 });
  assert.ok(Date.now() - t0 < 800);
  assert.equal(hung.state, 'UNKNOWN'); assert.equal(hung.timedOut, true);
  void FakeChain;
});

test('a signer whose provider.getNetwork() never resolves => RpcTimeoutError within callTimeoutMs + epsilon; 0 approve / sign / broadcast', async () => {
  const { w, wallet } = setup();
  const client = new BridgeClient(w.options({ callTimeoutMs: 40 }));
  const q = await client.quote(r1(wallet.address));
  let signed = 0;
  const hung = { provider: { getNetwork: () => new Promise<never>(() => {}) }, getAddress: async () => wallet.address, signTransaction: async () => { signed++; return '0x'; } };
  const t0 = Date.now();
  await assert.rejects(() => client.send(hung as any, q, { signerMode: 'local' }), (e: any) => e instanceof RpcTimeoutError && /getNetwork/.test(e.method));
  assert.ok(Date.now() - t0 < 1500);
  assert.equal(signed, 0);
  nothingSent(w);
  const hungAddr = { getAddress: () => new Promise<string>(() => {}), signTransaction: async () => '0x' };
  await assert.rejects(() => client.send(hungAddr as any, q, { signerMode: 'local' }), (e: any) => e instanceof RpcTimeoutError && /getAddress/.test(e.method));
  nothingSent(w);
});
