// Signer and signing flow: signed transactions are verified before broadcast, signer mode is chosen by capability only, callbacks run before the broadcast,
// and an unknown broadcast outcome carries what recovery needs.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { NonceManager, Transaction, Wallet, getAddress, keccak256 } from 'ethers';
import { BroadcastOutcomeUnknownError, SignerMisbehaviorError, SignerRejectedError, SigningError, pickMode } from '../src/index.js';
import { FakeWalletSigner } from './fake.js';
import { E18, erc20I } from './world.js';
import { r1, setup } from './helpers.js';

class EvilSigner {
  provider = null;
  signed: string[] = [];
  constructor(public wallet: Wallet, public how: 'protected' | 'unprotected' | 'otherChain' | 'mutateData' | 'mutateValue' | 'mutateNonce' | 'otherSender', public target: 'approve' | 'bridge') {}
  async getAddress() { return this.wallet.address; }
  async signTransaction(tx: any) {
    const isApprove = String(tx.data).startsWith(erc20I.getFunction('approve')!.selector);
    if ((this.target === 'approve') !== isApprove || this.how === 'protected') return this.wallet.signTransaction(tx);
    const f: any = { type: 0, to: tx.to, data: tx.data, value: tx.value, nonce: tx.nonce, gasLimit: tx.gasLimit, gasPrice: tx.gasPrice, chainId: tx.chainId };
    let key = this.wallet;
    if (this.how === 'unprotected') f.chainId = 0;
    if (this.how === 'otherChain') f.chainId = 1;
    if (this.how === 'mutateData') f.data = tx.data.slice(0, -2) + (tx.data.endsWith('00') ? '01' : '00');
    if (this.how === 'mutateValue') f.value = (tx.value ?? 0n) + 1n;
    if (this.how === 'mutateNonce') f.nonce = tx.nonce + 1;
    if (this.how === 'otherSender') key = new Wallet(Wallet.createRandom().privateKey);
    const t = Transaction.from(f);
    t.signature = key.signingKey.sign(t.unsignedHash);
    this.signed.push(t.serialized);
    return t.serialized;
  }
}
test('a signed tx that is not replay-protected / has another chain id / mutated fields / another signer is rejected BEFORE broadcast (approve and bridge tx)', async () => {
  for (const target of ['approve', 'bridge'] as const) {
    for (const how of ['unprotected', 'otherChain', 'mutateData', 'mutateValue', 'mutateNonce', 'otherSender'] as const) {
      const { w, wallet, client } = setup();
      const q = await client.quote(r1(wallet.address));
      const signer = new EvilSigner(wallet, how, target);
      await assert.rejects(() => client.send(signer as any, q, { signerMode: 'local' }), (e: any) => e instanceof SignerMisbehaviorError, `${target}/${how}`);
      assert.equal(w.chains.bsc.rawSent.length, target === 'approve' ? 0 : 1, `${target}/${how}: the bad tx is never broadcast`);
      assert.equal(w.chains.bsc.logs.length, 0);
    }
  }
  // sanity: an honest signer through the same wrapper succeeds
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  await client.send(new EvilSigner(wallet, 'protected', 'bridge') as any, q, { signerMode: 'local' });
  assert.equal(w.chains.bsc.logs.length, 1);
});

test('a throwing onBroadcast still returns the SentTx (hash + callbackError); exactly one broadcast; findSourceTransfer finds it', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const sent = await client.send(wallet, q, { onBroadcast: () => { throw new TypeError('db down'); } });
  assert.ok(sent.hash);
  assert.deepEqual(sent.callbackError, { name: 'TypeError', message: 'db down' });
  assert.equal(JSON.parse(JSON.stringify(sent.callbackError)).message, 'db down', 'serializable');
  assert.equal(w.chains.bsc.rawSent.length, 2, 'approve + exactly one bridge broadcast');
  const f = await client.findSourceTransfer(sent.intent, sent.checkpoint);
  assert.equal(f.result.status, 'FOUND');
  assert.equal(w.chains.bsc.logs.length, 1);
  const ok = await client.send(wallet, await client.quote(r1(wallet.address)));
  assert.equal(ok.callbackError, undefined);
});

test('signer-mode selection is by capability only; rejection / signing error / onSigned throw never fall back to the other mode', async () => {
  const { w, wallet, client } = setup();
  assert.equal(pickMode(wallet), 'L');
  assert.equal(pickMode(new NonceManager(wallet)), 'L');
  const walletLike = new FakeWalletSigner(wallet.address, w.chains.bsc);
  assert.equal(pickMode(walletLike), 'W');
  assert.throws(() => pickMode(walletLike, { signerMode: 'local' }), /signTransaction/);

  // user rejection (Mode W): nothing sent, safeToRetry
  const q = await client.quote(r1(wallet.address));
  walletLike.behavior = 'reject';
  await assert.rejects(() => client.send(walletLike, q), (e: any) => e instanceof SignerRejectedError && e.safeToRetry === true);
  assert.equal(walletLike.sent.length, 1, 'asked exactly once, no retry');
  assert.equal(w.chains.bsc.rawSent.length, 0, 'never routed through the local-signing path');

  // signing error (Mode L)
  const broken = Object.assign(Object.create(Object.getPrototypeOf(wallet)), wallet, { signTransaction: async () => { throw new Error('hsm offline'); } });
  const w2 = setup();
  const q2 = await w2.client.quote(r1(w2.wallet.address));
  const brokenW = Object.assign(Object.create(Object.getPrototypeOf(w2.wallet)), w2.wallet);
  brokenW.signTransaction = async () => { throw new Error('hsm offline'); };
  Object.defineProperty(brokenW, 'address', { value: w2.wallet.address });
  await assert.rejects(() => w2.client.send(brokenW, q2, { signerMode: 'local' }), (e: any) => e instanceof SigningError || /hsm offline/.test(String(e.message)));
  assert.equal(w2.w.chains.bsc.rawSent.length, 0);
  void broken;

  // onSigned throws -> not broadcast
  const w3 = setup();
  const q3 = await w3.client.quote(r1(w3.wallet.address));
  await assert.rejects(() => w3.client.send(w3.wallet, q3, { onSigned: () => { throw new Error('disk full'); } }), /disk full/);
  assert.equal(w3.w.chains.bsc.rawSent.length, 1, 'only the approve was broadcast, never the bridge tx');
  assert.equal(w3.w.chains.bsc.logs.length, 0);
});

test('Mode L: onSigned and onSubmitting are awaited BEFORE broadcast; onSubmitting throw aborts', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const order: string[] = [];
  const before = () => w.chains.bsc.rawSent.length;
  let rawAtSigned = -1, rawAtSubmitting = -1;
  const sent = await client.send(wallet, q, {
    onSubmitting: async () => { await new Promise((r) => setTimeout(r, 5)); rawAtSubmitting = before(); order.push('submitting'); },
    onSigned: async (info) => { await new Promise((r) => setTimeout(r, 5)); rawAtSigned = before(); order.push('signed'); assert.match(info.raw, /^0x/); assert.equal(keccak256(info.raw), info.hash); },
    onBroadcast: () => { order.push('broadcast'); },
  });
  assert.deepEqual(order, ['submitting', 'signed', 'broadcast']);
  assert.equal(rawAtSubmitting, 1); // the approve only
  assert.equal(rawAtSigned, 1);
  assert.equal(before(), 2);
  assert.ok(sent.raw && sent.nonce === 1);

  const t = setup();
  const q2 = await t.client.quote(r1(t.wallet.address));
  await assert.rejects(() => t.client.send(t.wallet, q2, { onSubmitting: () => { throw new Error('persist failed'); } }), /persist failed/);
  assert.equal(t.w.chains.bsc.logs.length, 0);
  // Mode W: same abort
  const t2 = setup();
  const q3 = await t2.client.quote(r1(t2.wallet.address));
  const wl = new FakeWalletSigner(t2.wallet.address, t2.w.chains.bsc);
  await assert.rejects(() => t2.client.send(wl, q3, { onSubmitting: () => { throw new Error('persist failed'); } }), /persist failed/);
  assert.equal(wl.sent.filter((s) => s.to === t2.c.bsc.bridge).length, 0);
});

test('Mode L: broadcast error -> BroadcastOutcomeUnknownError with raw; rebroadcast is idempotent; findSourceTransfer finds the mined tx', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  let signedRaw = '';
  // the approve goes through normally; only the bridge broadcast "times out" after the node mined it
  let err: BroadcastOutcomeUnknownError | undefined;
  try { await client.send(wallet, q, { onSubmitting: () => { w.chains.bsc.sendMode = 'throw-after-mine'; }, onSigned: (i) => { signedRaw = i.raw; } }); } catch (e) { err = e as BroadcastOutcomeUnknownError; }
  assert.ok(err instanceof BroadcastOutcomeUnknownError);
  assert.equal(err!.mode, 'L');
  assert.equal(err!.info.raw, signedRaw);
  assert.ok(err!.info.hash && err!.info.nonce === 1);
  assert.match(err!.message, /DO NOT resend/);
  w.chains.bsc.sendMode = 'mine';
  const again = await client.rebroadcast('bsc', err!.info.raw!);
  assert.equal(again.toLowerCase(), err!.info.hash!.toLowerCase());
  assert.equal(w.chains.bsc.logs.length, 1, 'exactly one transfer on chain');
  const f = await client.findSourceTransfer(err!.intent, err!.checkpoint);
  assert.equal(f.result.status, 'FOUND');
  assert.equal(f.nonceInfo.pendingNonceBefore, 1);
  assert.equal(f.nonceInfo.minedNonce, 2);
  void c;
});

test('Mode W: wallet throws without a hash -> error carries intent + checkpoint (no raw/hash/nonce); recovery with FULL-field match', async () => {
  const { w, c, wallet, client } = setup();
  w.setFee('bsc', c.cross.chainId, c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 2n * E18, rate: 0n });
  const q = await client.quote(r1(wallet.address));
  w.onApprove = () => w.setFee('bsc', c.cross.chainId, c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 1n * E18, rate: 0n }); // fee drops -> adopted
  const wl = new FakeWalletSigner(wallet.address, w.chains.bsc);
  // the approve is ALSO sent by the wallet; make the first call fine, the second (bridge) lose its response
  const orig = wl.sendTransaction.bind(wl);
  let n = 0;
  wl.sendTransaction = async (tx) => { n++; if (n === 2) wl.behavior = 'throw-after-mine'; return orig(tx); };
  let err: BroadcastOutcomeUnknownError | undefined;
  try { await client.send(wl, q); } catch (e) { err = e as BroadcastOutcomeUnknownError; }
  assert.ok(err instanceof BroadcastOutcomeUnknownError && err.mode === 'W');
  assert.equal(err!.info.raw, undefined); assert.equal(err!.info.hash, undefined); assert.equal(err!.info.nonce, undefined);
  assert.ok(err!.intent && err!.checkpoint);
  assert.equal(err!.intent.match.kind === 'bridge' && err!.intent.match.networkFee, 1n * E18, 'intent carries the ADOPTED fees, not the first quote');
  const f = await client.findSourceTransfer(err!.intent, err!.checkpoint);
  assert.equal(f.result.status, 'FOUND');
});
