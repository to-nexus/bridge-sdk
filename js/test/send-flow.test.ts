// The send flow: quote expiry, exact approve, re-quote under the approved limits, approve policy, and immutability of what is signed.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Interface, Transaction, Wallet } from 'ethers';
import { ApproveFailedError, BridgeClient, QuoteExpiredError, RequoteRequiredError, Route, pickMode } from '../src/index.js';
import { swapBridgeRouterAbi } from '../src/abi/index.js';
import { E18, RECIPIENT, TOKEN_A, World, bridgeI } from './world.js';
import { OTHER, amount, r1, setup } from './helpers.js';

test('quote expiry', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  w.clock += 31_000;
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof QuoteExpiredError);
  assert.equal(w.approveCalls.length, 0);
});

test('happy path R1 (Mode L): exact approve -> requote -> simulate -> broadcast -> handle', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  assert.equal(q.totalSpend, amount); // bsc->hub fee is 0 in this world
  assert.deepEqual(q.approval, { token: c.bsc.tokens.ONE!.address, spender: c.bsc.bridge, amount });
  assert.equal(pickMode(wallet), 'L');
  const sent = await client.send(wallet, q);
  assert.deepEqual(w.approveCalls, [amount]);
  assert.equal(sent.mode, 'L');
  assert.equal(sent.nonce, 1); // nonce 0 was the approve
  const h = await sent.wait();
  assert.equal(h.hop1.index, '0');
  assert.equal(h.hop1.destChainId, c.one.chainId);
  assert.equal(h.srcTxHash, sent.hash.toLowerCase());
});

test('requote after approve: a fee INCREASE is refused (RequoteRequiredError), nothing is sent', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  w.onApprove = () => w.setFee('bsc', c.one.chainId, c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 1n * E18, rate: 0n });
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof RequoteRequiredError && e.violated.includes('maxTotalSpend') && e.newQuote!.totalSpend > e.oldQuote.totalSpend);
  assert.equal(w.chains.bsc.logs.length, 0, 'no bridge transfer was created');
});

test('requote after approve: a fee DECREASE is adopted (calldata carries the fresh fees)', async () => {
  const { w, c, wallet, client } = setup();
  w.setFee('bsc', c.one.chainId, c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 2n * E18, rate: 0n });
  const q = await client.quote(r1(wallet.address));
  assert.equal(q.totalSpend, amount + 2n * E18);
  w.onApprove = () => w.setFee('bsc', c.one.chainId, c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 1n * E18, rate: 0n });
  const sent = await client.send(wallet, q);
  assert.equal(sent.quote.totalSpend, amount + 1n * E18);
  assert.equal(sent.quote.limits.maxTotalSpend, q.limits.maxTotalSpend, 'limits are immutable');
  const decoded = bridgeI.decodeFunctionData('bridgeToken', sent.intent.data);
  assert.equal(decoded[4], 1n * E18);
  assert.equal(sent.intent.match.kind === 'bridge' && sent.intent.match.networkFee, 1n * E18);
});

test('approve policy: zero-first when 0 < allowance < needed; approve receipt failure stops the flow', async () => {
  const { w, c, wallet, client } = setup();
  w.setAllow('bsc', c.bsc.tokens.ONE!.address, wallet.address, c.bsc.bridge!, 5n);
  const q = await client.quote(r1(wallet.address));
  await client.send(wallet, q);
  assert.deepEqual(w.approveCalls, [0n, amount]);

  const t = setup();
  t.w.approveFails = true;
  const q2 = await t.client.quote(r1(t.wallet.address));
  await assert.rejects(() => t.client.send(t.wallet, q2), (e: any) => e instanceof ApproveFailedError);
  assert.equal(t.w.chains.bsc.logs.length, 0);
  // 'max' policy
  const m = setup();
  const q3 = await m.client.quote(r1(m.wallet.address));
  await m.client.send(m.wallet, q3, { approveAmount: 'max' });
  assert.equal(m.w.approveCalls[0], 2n ** 256n - 1n);
});

test('X3: requote with a higher swap output but a higher bridge fee -> RequoteRequiredError (delivery floor 94 < original 99)', async () => {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  const usdtx = c.one.tokens.USDTx!.address;
  w.setFee('one', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n;
  const client = new BridgeClient(w.options());
  const q = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, swap: { slippageBps: 100 }, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } });
  assert.equal(q.limits.amountOutMinimum, 99n);
  assert.equal(q.limits.minBridgeValue, 99n);
  w.onApprove = () => { w.swapOut = 110n; w.setFee('one', c.bsc.chainId, usdtx, { min: 1n, nf: 5n, rate: 0n }); };
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof RequoteRequiredError && e.violated.includes('bridgeDeliveryFloor'));
  assert.equal(w.chains.one.logs.length, 0);

  // price DROP below the original floor -> refused, floor never loosened
  const w2 = World.testnet();
  const wallet2 = new Wallet(Wallet.createRandom().privateKey);
  w2.setBal('one', TOKEN_A, wallet2.address, 1000n * E18);
  w2.setFee('one', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  w2.swapOut = 100n;
  const client2 = new BridgeClient(w2.options());
  const q2 = await client2.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet2.address, recipient: RECIPIENT, swap: { slippageBps: 100 }, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } });
  w2.onApprove = () => { w2.swapOut = 90n; };
  await assert.rejects(() => client2.send(wallet2, q2), (e: any) => e instanceof RequoteRequiredError && e.violated.includes('swapOutputBelowFloor'));

  // price RISES: adopted, but calldata keeps the ORIGINAL amountOutMinimum
  const w3 = World.testnet();
  const wallet3 = new Wallet(Wallet.createRandom().privateKey);
  w3.setBal('one', TOKEN_A, wallet3.address, 1000n * E18);
  w3.setFee('one', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  w3.swapOut = 100n;
  const client3 = new BridgeClient(w3.options());
  const q3 = await client3.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet3.address, recipient: RECIPIENT, swap: { slippageBps: 100 }, x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } });
  w3.onApprove = () => { w3.swapOut = 120n; };
  const sent = await client3.send(wallet3, q3);
  const d = new Interface((await import('../src/abi/index.js')).swapBridgeRouterAbi as any).decodeFunctionData('swapBridgeExactInputSingle', sent.intent.data);
  assert.equal(d[0][4], 99n, 'original amountOutMinimum kept');
  assert.equal(sent.quote.limits.amountOutMinimum, 99n);
});

test('mutating the caller request after quote() and while the approve is pending does not change what is signed; frozen quote / callback args cannot be mutated', async () => {
  const { w, wallet, client } = setup();
  const req: any = { route: Route.BSC_TO_ONE, amount, sender: wallet.address, recipient: RECIPIENT, limits: { maxTotalSpend: amount } };
  const q = await client.quote(req);
  const before = q.tx.data;
  req.recipient = OTHER; req.amount = 1n; req.limits.maxTotalSpend = 1n;
  w.onApprove = () => { req.recipient = OTHER; req.amount = 3n; req.limits = { maxTotalSpend: 0n }; };
  assert.throws(() => { (q.request as any).amount = 1n; }, TypeError);
  assert.throws(() => { (q.tx as any).data = '0x'; }, TypeError);
  const seen: string[] = [];
  const sent = await client.send(wallet, q, {
    onSubmitting: (intent, cp) => {
      assert.throws(() => { (intent as any).data = '0x'; }, TypeError);
      assert.throws(() => { (intent.adoptedQuote.tx as any).data = '0x'; }, TypeError);
      assert.throws(() => { (cp as any).pendingNonceBefore = 99; }, TypeError);
      seen.push('submitting');
    },
    onSigned: (info, intent) => { assert.throws(() => { (info as any).raw = '0x'; }, TypeError); assert.throws(() => { (intent as any).to = OTHER; }, TypeError); seen.push('signed'); },
  });
  assert.deepEqual(seen, ['submitting', 'signed']);
  assert.equal(sent.intent.data, before, 'the calldata is the one quoted, whatever the caller did to its request object');
  assert.equal(Transaction.from(sent.raw!).data, before);
  const decoded = bridgeI.decodeFunctionData('bridgeToken', before);
  assert.equal(decoded[2], RECIPIENT);
});

test('X3: mutating the caller x3 object after quote() does not change the quote or the calldata', async () => {
  const { c, wallet, client } = setup();
  const usdtx = c.one.tokens.USDTx!.address;
  const x3: any = { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' };
  const q = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: wallet.address, recipient: RECIPIENT, x3 });
  x3.tokenOut = OTHER; x3.poolFee = 500; x3.tokenIn = OTHER;
  assert.equal(q.request.x3!.tokenOut, usdtx);
  const sent = await client.send(wallet, q);
  assert.equal(sent.intent.data, q.tx.data);
});
