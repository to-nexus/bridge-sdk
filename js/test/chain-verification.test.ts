// Recovery, tracking and waiting verify the chain of every RPC before reading any evidence: a wrong chain is a NetworkMismatchError, a chain id that cannot be
// verified (error or hang) stops all reads, and a replacement transaction must match chain id, sender and nonce.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Wallet } from 'ethers';
import { BridgeClient, NetworkMismatchError, Route, RpcTimeoutError } from '../src/index.js';
import { FakeChain } from './fake.js';
import { E18, RECIPIENT, TOKEN_A, World } from './world.js';
import { OTHER, amount, realSleep } from './helpers.js';

async function pendingSend() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  const client = new BridgeClient(w.options());
  const q = await client.quote({ route: Route.BSC_TO_ONE, amount, sender: wallet.address, recipient: RECIPIENT });
  w.setAllow('bsc', c.bsc.tokens.ONE!.address, wallet.address, c.bsc.bridge!, 10n ** 30n);
  w.chains.bsc.sendMode = 'pending'; // accepted by the node, never mined
  const sent = await client.send(wallet, q);
  w.chains.bsc.sendMode = 'mine';
  return { w, c, wallet, client, sent, q };
}
const analyze = (cl: BridgeClient, t: { sent: any }, replacementHash?: string) => cl.analyzeReplacement({ intent: t.sent.intent, checkpoint: t.sent.checkpoint, original: { hash: t.sent.hash, nonce: t.sent.nonce }, replacementHash });
const evidenceReads = (ch: FakeChain) => ch.calls.filter((c) => c.method !== 'eth_chainId').length;
const cancelTx = (t: Awaited<ReturnType<typeof pendingSend>>) => t.w.chains.bsc.applyTx({ from: t.wallet.address, to: t.wallet.address, data: '0x', value: 0n, nonce: t.sent.nonce });

async function setupPendingAndMined() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  w.setAllow('bsc', c.bsc.tokens.ONE!.address, wallet.address, c.bsc.bridge!, 10n ** 30n);
  const client = new BridgeClient(w.options());
  const req = { route: Route.BSC_TO_ONE, amount, sender: wallet.address, recipient: RECIPIENT } as const;
  w.chains.bsc.sendMode = 'pending';
  const pending = await client.send(wallet, await client.quote(req));
  w.chains.bsc.sendMode = 'mine';
  const mined = await client.send(wallet, await client.quote(req));
  const handle = await mined.wait();
  w.finalizeNext.set(`one:${c.bsc.chainId}`, BigInt(handle.hop1.index) + 1n); // the destination moved on: a healthy tracker would scan logs
  return { w, wallet, pending, mined, handle };
}
type T = Awaited<ReturnType<typeof setupPendingAndMined>>;
// a FRESH client (empty chain-id cache) per case: "restart"
const fresh = (t: T) => new BridgeClient(t.w.options({ callTimeoutMs: 30, now: () => Date.now(), sleep: realSleep }));
function breakChainId(ch: FakeChain, mode: 'error' | 'hang') {
  if (mode === 'error') ch.failMethods.set('eth_chainId', 'boom: eth_chainId failed'); else ch.hang.add('eth_chainId');
}
const reasonRe = (mode: 'error' | 'hang') => (mode === 'error' ? /^chain-unverified:/ : /^timeout:/);

test('a fresh client whose source RPC reports ANOTHER chain (holding a same-account, same-nonce cancel tx) => NetworkMismatchError, never NEW_TX_ALLOWED, 0 evidence reads before the chain check', async () => {
  const t = await pendingSend();
  const wrong = new FakeChain(1);
  const rep = wrong.applyTx({ from: t.wallet.address, to: t.wallet.address, data: '0x', value: 0n, nonce: t.sent.nonce });
  wrong.nonces.set(t.wallet.address.toLowerCase(), t.sent.nonce! + 1);
  const fresh = new BridgeClient(t.w.options({ providers: { ...t.w.chains, bsc: wrong } })); // "restart": a new client, a wrong RPC
  for (const hash of [undefined, rep.hash]) {
    await assert.rejects(() => analyze(fresh, t, hash), (e: any) => e instanceof NetworkMismatchError);
  }
  assert.equal(evidenceReads(wrong), 0, 'no receipt / nonce / block / tx read happened before the chain check');
  assert.ok(wrong.calls.some((c) => c.method === 'eth_chainId'));
});

test('a replacement tx with another chain id, no chain id (pre-EIP-155), another nonce or another sender is not evidence: never NEW_TX_ALLOWED (with and without the hash)', async () => {
  const mutations: Record<string, (rec: any, t: Awaited<ReturnType<typeof pendingSend>>) => void> = {
    'other chain id': (rec) => { rec.chainId = '0x1'; },
    'no chain id (pre-EIP-155)': (rec) => { delete rec.chainId; },
    'another nonce': (rec, t) => { rec.nonce = '0x' + (t.sent.nonce! + 5).toString(16); },
    'another sender': (rec) => { rec.from = OTHER; },
  };
  for (const [name, mutate] of Object.entries(mutations)) {
    const t = await pendingSend();
    const rep = cancelTx(t);
    const rec: any = t.w.chains.bsc.txs.get(rep.hash.toLowerCase())!; // the same object is in the block, so block scanning sees it too
    mutate(rec, t);
    for (const hash of [rep.hash, undefined]) {
      const v = await analyze(t.client, t, hash);
      assert.notEqual(v.verdict, 'NEW_TX_ALLOWED', `${name} ${hash ? 'with' : 'without'} the hash`);
      assert.equal(v.verdict, 'ORIGINAL_STILL_POSSIBLE', `${name} ${hash ? 'with' : 'without'} the hash`);
    }
  }
});

test('positive control: the correct chain + a provable non-transfer replacement still gives NEW_TX_ALLOWED (cancel and failed replacement)', async () => {
  const t = await pendingSend();
  const rep = cancelTx(t);
  const v: any = await analyze(t.client, t, rep.hash);
  assert.deepEqual([v.verdict, v.reason], ['NEW_TX_ALLOWED', 'replacement-not-a-transfer']);
  assert.equal((await analyze(t.client, t)).verdict, 'NEW_TX_ALLOWED', 'block scanning too');
});

test('an intent whose chain id is not this client\'s chain (or disagrees with its checkpoint) => NetworkMismatchError before any read', async () => {
  const t = await pendingSend();
  const before = t.w.chains.bsc.calls.length;
  await assert.rejects(() => t.client.analyzeReplacement({ intent: { ...t.sent.intent, chainId: 1 }, checkpoint: t.sent.checkpoint, original: { hash: t.sent.hash, nonce: t.sent.nonce! } }), (e: any) => e instanceof NetworkMismatchError);
  await assert.rejects(() => t.client.findSourceTransfer({ ...t.sent.intent, chainId: 1 }, t.sent.checkpoint), (e: any) => e instanceof NetworkMismatchError);
  await assert.rejects(() => t.client.analyzeReplacement({ intent: t.sent.intent, checkpoint: { ...t.sent.checkpoint, chainId: 1 }, original: { hash: t.sent.hash, nonce: t.sent.nonce! } }), (e: any) => e instanceof NetworkMismatchError);
  assert.equal(t.w.chains.bsc.calls.length, before, '0 RPC');
});

test('the other entry points (findSourceTransfer, handleFromTx, track / trackOnce, wait, rebroadcast) on a wrong-chain RPC raise NetworkMismatchError before any log / receipt read', async () => {
  const t = await pendingSend();
  const mined = await new BridgeClient(t.w.options()).send(t.wallet, await t.client.quote({ route: Route.BSC_TO_ONE, amount, sender: t.wallet.address, recipient: RECIPIENT }));
  const handle = await mined.wait();
  const wrong = new FakeChain(1);
  wrong.applyTx({ from: t.wallet.address, to: t.wallet.address, data: '0x', value: 0n });
  const fresh = new BridgeClient(t.w.options({ providers: { ...t.w.chains, bsc: wrong } }));
  const mm = (e: any) => e instanceof NetworkMismatchError;
  await assert.rejects(() => fresh.findSourceTransfer(t.sent.intent, t.sent.checkpoint), mm);
  await assert.rejects(() => fresh.handleFromTx('bsc', mined.hash), mm);
  await assert.rejects(() => fresh.track(handle, { timeoutMs: 50, pollMs: 10 }), mm);
  await assert.rejects(() => fresh.trackOnce(handle), mm);
  await assert.rejects(() => fresh.wait(t.sent), mm);
  await assert.rejects(() => fresh.rebroadcast('bsc', t.sent.raw!), mm);
  assert.equal(evidenceReads(wrong), 0, 'not one log / receipt / tx / nonce / block read (or send) reached the wrong chain');
});

for (const mode of ['error', 'hang'] as const) {
  for (const which of ['bsc', 'one'] as const) { // the SOURCE chain (source receipt) and the DESTINATION chain (evalHop)
    test(`trackOnce / track: eth_chainId ${mode} on the ${which} RPC => UNKNOWN{${mode === 'error' ? 'chain-unverified' : 'timeout'}}, 0 evidence reads on that chain`, async () => {
      const t = await setupPendingAndMined();
      const ch = t.w.chains[which];
      breakChainId(ch, mode);
      const base = ch.calls.length;
      const r = await fresh(t).trackOnce(t.handle);
      assert.equal(r.state, 'UNKNOWN');
      assert.match(r.hops[r.hops.length - 1].reason!, reasonRe(mode));
      assert.equal(r.timedOut === true, mode === 'hang');
      assert.equal(ch.calls.slice(base).filter((c) => c.method !== 'eth_chainId').length, 0, 'no receipt / log / block / tx read on the unverified chain');
      const base2 = ch.calls.length;
      const tr = await fresh(t).track(t.handle, { timeoutMs: 200, pollMs: 20 });
      assert.equal(tr.state, 'UNKNOWN');
      assert.equal(tr.timedOut === true, mode === 'hang'); // UNKNOWN is terminal: a non-timeout reason ends track() at once, a timeout is reported as timedOut
      assert.match(tr.hops[tr.hops.length - 1].reason!, reasonRe(mode));
      assert.equal(ch.calls.slice(base2).filter((c) => c.method !== 'eth_chainId').length, 0);
    });
  }

  test(`wait + every recovery entry point: eth_chainId ${mode} on the source RPC => an error (no verdict), 0 evidence reads`, async () => {
    const t = await setupPendingAndMined();
    breakChainId(t.w.chains.bsc, mode);
    const cl = fresh(t);
    const base = t.w.chains.bsc.calls.length;
    const bad = (e: any) => !(e instanceof NetworkMismatchError) && (mode === 'hang' ? e instanceof RpcTimeoutError : /boom/.test(e.message));
    const orig = { hash: t.pending.hash, nonce: t.pending.nonce! };
    await assert.rejects(() => cl.wait(t.mined), bad);
    await assert.rejects(() => cl.findSourceTransfer(t.pending.intent, t.pending.checkpoint), bad);
    await assert.rejects(() => cl.analyzeReplacement({ intent: t.pending.intent, checkpoint: t.pending.checkpoint, original: orig }), bad);
    await assert.rejects(() => cl.analyzeReplacement({ intent: t.pending.intent, checkpoint: t.pending.checkpoint, original: orig, replacementHash: t.mined.hash }), bad);
    await assert.rejects(() => cl.handleFromTx('bsc', t.mined.hash), bad);
    await assert.rejects(() => cl.rebroadcast('bsc', t.pending.raw!), bad);
    assert.equal(t.w.chains.bsc.calls.slice(base).filter((c) => c.method !== 'eth_chainId').length, 0, 'not one receipt / log / tx / nonce / block read (or send)');
  });
}

for (const mode of ['error', 'hang'] as const) {
  test(`preflight: eth_chainId ${mode} on the source RPC => PREFLIGHT_INCOMPLETE and 0 evidence reads from that RPC`, async () => {
    const t = await setupPendingAndMined();
    const c = t.w.net.chains;
    const q = await new BridgeClient(t.w.options()).quote({ route: Route.BSC_TO_ONE, amount, sender: t.wallet.address, recipient: RECIPIENT });
    breakChainId(t.w.chains.bsc, mode);
    const base = t.w.chains.bsc.calls.length;
    const rep = await fresh(t).preflight(q);
    assert.equal(rep.ok, false);
    assert.ok(rep.issues.some((i) => i.code === 'PREFLIGHT_INCOMPLETE' && /chainId\(bsc\)/.test(String((i as any).check))), JSON.stringify(rep.issues.map((i) => i.code)));
    assert.equal(t.w.chains.bsc.calls.slice(base).filter((x) => x.method !== 'eth_chainId').length, 0, 'no bridge / pair / balance read on the unverified chain');
    void c;
  });
}
