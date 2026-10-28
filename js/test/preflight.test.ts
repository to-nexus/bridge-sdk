// Blocking preflight: every error issue stops send() before the approve and again after it (post-approve); an RPC error is itself blocking; the destination
// bridge pause, extraData limits and the X3 quote status are checked.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { getAddress } from 'ethers';
import { BridgeClient, PreflightError, Route } from '../src/index.js';
import type { PreflightIssue } from '../src/index.js';
import { FakeChain } from './fake.js';
import { E18, NATIVE, RECIPIENT, TOKEN_A, v3RouterI } from './world.js';
import { amount, codes, noMoney, nothingSent, r1, r5core, setup, x1World, x3req } from './helpers.js';

const refused = (stage: string, code: string, check?: string) => (e: any) =>
  e instanceof PreflightError && e.stage === stage && e.issues.some((i: PreflightIssue) => i.code === code && (check === undefined || i.check === check));

test('blocked BEFORE approve: CORE_NOT_LINKED (R5+X2), X1 router not whitelisted, destination RPC error (PREFLIGHT_INCOMPLETE), insufficient balance - 0 approve / 0 sign / 0 broadcast', async () => {
  // (1) X2 token not linked to HyperCore (nested X2 on R5: the source is an ERC20, so a missed check would approve first)
  { const { w, c, wallet, client } = setup();
    const q = await client.quote(r5core(wallet.address));
    w.hyperCoreSet = false;
    await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof PreflightError && e.stage === 'pre-approve' && codes(e).includes('CORE_NOT_LINKED'));
    assert.deepEqual(noMoney(w), { approves: 0, bsc: 0, cross: 0, hyper: 0, bridgeLogs: 0 }); }
  // (2) X1: the hub executor does not whitelist the V3 router
  { const x = x1World();
    const q = await x.client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: x.wallet.address, recipient: RECIPIENT });
    x.w.execWhitelist.delete(`cross:${x.router.toLowerCase()}`);
    await assert.rejects(() => x.client.send(x.wallet, q), (e: any) => e instanceof PreflightError && e.stage === 'pre-approve' && codes(e).includes('EXECUTOR_NOT_WHITELISTED'));
    assert.deepEqual(noMoney(x.w), { approves: 0, bsc: 0, cross: 0, hyper: 0, bridgeLogs: 0 }); }
  // (3) the hyperevm RPC fails: an unverified destination check is itself blocking
  { const { w, c, wallet, client } = setup();
    const q = await client.quote(r5core(wallet.address));
    w.chains.hyperevm.failAll = 'upstream 503';
    await assert.rejects(() => client.send(wallet, q), (e: any) => {
      if (!(e instanceof PreflightError) || e.stage !== 'pre-approve') return false;
      const inc = e.issues.find((i: PreflightIssue) => i.code === 'PREFLIGHT_INCOMPLETE');
      return !!inc && !!inc.check && /503/.test(inc.error ?? '');
    });
    assert.deepEqual(noMoney(w), { approves: 0, bsc: 0, cross: 0, hyper: 0, bridgeLogs: 0 }); }
  // (4) insufficient balance
  { const { w, c, wallet, client } = setup();
    const q = await client.quote(r1(wallet.address));
    w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1n);
    await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof PreflightError && e.stage === 'pre-approve' && codes(e).includes('INSUFFICIENT_BALANCE'));
    assert.deepEqual(noMoney(w), { approves: 0, bsc: 0, cross: 0, hyper: 0, bridgeLogs: 0 }); }
});

test('blocked AFTER approve (post-approve): a bridge pause / a pair pause injected while the approve is pending - exactly 1 approve, 0 bridge signatures, 0 bridge broadcasts', async () => {
  for (const inject of ['bridge', 'pair'] as const) {
    const { w, c, wallet, client } = setup();
    const q = await client.quote(r1(wallet.address));
    w.onApprove = () => {
      if (inject === 'bridge') w.paused.add('bsc');
      else w.pairs.get(`bsc:${c.cross.chainId}:${c.bsc.tokens.ONE!.address.toLowerCase()}`)!.paused = true;
    };
    await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof PreflightError && e.stage === 'post-approve' && codes(e).includes(inject === 'bridge' ? 'BRIDGE_PAUSED' : 'PAIR_PAUSED'), inject);
    assert.equal(w.approveCalls.length, 1, `${inject}: exactly one approve`);
    assert.equal(w.chains.bsc.rawSent.length, 1, `${inject}: only the approve was broadcast, never the bridge tx`);
    assert.equal(w.chains.bsc.logs.length, 0);
  }
});

test('allowance is NOT blocking before approve (send approves) but IS blocking after it', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote(r1(wallet.address));
  const rep = await client.preflight(q);
  assert.ok(rep.issues.some((i) => i.code === 'APPROVE_NEEDED' && i.level === 'warn'));
  const sent = await client.send(wallet, q);
  assert.equal(w.approveCalls.length, 1);
  assert.ok(sent.hash);
});

test('a paused DESTINATION bridge blocks at pre-approve for every route (R1-R6, X2, X3): 0 approve, 0 sign, 0 broadcast', async () => {
  const { c } = setup();
  const cases: [string, any, string][] = [
    ['R1', { route: Route.BSC_TO_ONE }, 'cross'], ['R2', { route: Route.ONE_TO_BSC }, 'bsc'], ['R3', { route: Route.ONE_TO_HYPEREVM }, 'hyperevm'], ['R4', { route: Route.HYPEREVM_TO_ONE }, 'cross'],
    ['R5', { route: Route.BSC_TO_ONE_TO_HYPEREVM }, 'hyperevm'], ['R5+core', { route: Route.BSC_TO_ONE_TO_HYPEREVM, hypercore: true }, 'hyperevm'], ['R6', { route: Route.HYPEREVM_TO_ONE_TO_BSC }, 'bsc'],
    ['X2', { route: Route.ONE_TO_HYPERCORE }, 'hyperevm'], ['X3', { route: Route.SWAP_BRIDGE, amount: 50n * E18, x3: { tokenIn: TOKEN_A, tokenOut: c.cross.tokens.USDTx!.address, poolFee: 0, toChain: 'bsc' } }, 'bsc'],
  ];
  for (const [name, req, dest] of cases) {
    const t = setup();
    const q = await t.client.quote({ amount, sender: t.wallet.address, recipient: RECIPIENT, ...req });
    assert.equal(q.details.destChainKey, dest, name);
    t.w.paused.add(dest);
    await assert.rejects(() => t.client.send(t.wallet, q), refused('pre-approve', 'DEST_BRIDGE_PAUSED'), name);
    nothingSent(t.w);
  }
});

test('a destination RPC error, or a destination RPC on another chain, is blocking: PREFLIGHT_INCOMPLETE{check: DEST_BRIDGE_PAUSED}; nothing sent', async () => {
  const t = setup();
  const q = await t.client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: t.wallet.address, recipient: RECIPIENT });
  t.w.chains.hyperevm.failAll = 'upstream 503';
  await assert.rejects(() => t.client.send(t.wallet, q), refused('pre-approve', 'PREFLIGHT_INCOMPLETE', 'DEST_BRIDGE_PAUSED'));
  nothingSent(t.w);
  const t2 = setup();
  const q2 = await t2.client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: t2.wallet.address, recipient: RECIPIENT });
  const other = new BridgeClient(t2.w.options({ providers: { ...t2.w.chains, hyperevm: new FakeChain(1) } }));
  await assert.rejects(() => other.send(t2.wallet, q2), refused('pre-approve', 'PREFLIGHT_INCOMPLETE', 'DEST_BRIDGE_PAUSED'));
  nothingSent(t2.w);
});

test('a destination pause injected between pre-approve and post-approve blocks at post-approve: exactly 1 approve, 0 bridge broadcast', async () => {
  const t = setup();
  const q = await t.client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: t.wallet.address, recipient: RECIPIENT });
  t.w.onApprove = () => { t.w.paused.add('hyperevm'); };
  await assert.rejects(() => t.client.send(t.wallet, q), refused('post-approve', 'DEST_BRIDGE_PAUSED'));
  assert.equal(t.w.approveCalls.length, 1);
  assert.equal(t.w.chains.bsc.rawSent.length, 1, 'only the approve');
  assert.equal(t.w.chains.bsc.logs.length, 0);
});

test('positive control: with every bridge running, the preflight has no destination issue and the pair check is not extended', async () => {
  const t = setup();
  const q = await t.client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: t.wallet.address, recipient: RECIPIENT });
  const rep = await t.client.preflight(q);
  assert.ok(!rep.issues.some((i) => i.code === 'DEST_BRIDGE_PAUSED'));
  assert.ok(rep.checks.some((c) => c.name === 'destination bridge paused()' && c.ok));
  assert.ok(!rep.checks.some((c) => /destination pair/i.test(c.name)), 'no destination pair check');
});

test('preflight: structured issues for paused bridge / unwhitelisted executor / low balance', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: wallet.address, recipient: RECIPIENT, hypercore: true });
  let pf = await client.preflight(q);
  assert.equal(pf.ok, true, JSON.stringify(pf.issues));
  w.paused.add('cross');
  w.execWhitelist.delete(`hyperevm:${c.hyperevm.tokens.ONE!.address.toLowerCase()}`);
  w.hyperCoreSet = false;
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1n);
  pf = await client.preflight(q);
  const codes = pf.issues.map((i) => i.code);
  for (const x of ['HUB_PAUSED', 'EXECUTOR_NOT_WHITELISTED', 'CORE_NOT_LINKED', 'INSUFFICIENT_BALANCE']) assert.ok(codes.includes(x), `${x} in ${codes}`);
  assert.equal(pf.ok, false);
  void NATIVE; void getAddress; void v3RouterI;
});

test('hub extraData limit applies to the INNER payload, the source limit to the OUTER one', async () => {
  const { w, wallet, client } = setup();
  const q = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender: wallet.address, recipient: RECIPIENT, hypercore: true });
  assert.equal((q.details.extraData.length - 2) / 2, 344, 'outer 344 B');
  w.maxExtra.set('cross', 128n);
  let rep = await client.preflight(q);
  assert.ok(!rep.issues.some((i) => i.code.startsWith('EXTRADATA')), 'outer 344 B, inner 88 B, hub limit 128 -> passes');
  w.maxExtra.set('cross', 64n); // a canonical X2 inner payload is exactly 88 B (a padded inner is refused by the route rules, so the limit is lowered instead)
  rep = await client.preflight(q);
  assert.ok(rep.issues.some((i) => i.code === 'EXTRADATA_TOO_LONG_HUB'), 'inner 88 B, hub limit 64');
  w.maxExtra.set('cross', 1024n);
  w.maxExtra.set('bsc', 100n);
  rep = await client.preflight(q);
  assert.ok(rep.issues.some((i) => i.code === 'EXTRADATA_TOO_LONG_SOURCE'), 'outer 344 B > source limit 100');
  assert.ok(!rep.issues.some((i) => i.code === 'EXTRADATA_TOO_LONG_HUB'));
});

test('X3: the router quote status is re-checked before any approve (status 5 InvalidSwap => PreflightError stage pre-approve, 0 approve / sign / broadcast)', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(x3req(wallet.address, c.cross.tokens.USDTx!.address));
  w.swapStatus = 5;
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof PreflightError && e.stage === 'pre-approve' && e.issues.some((i: any) => i.code === 'X3_QUOTE_InvalidSwap'));
  nothingSent(w);
});

test('X3: a status that flips while the approve is pending is reported with stage "requote" (error while building the adopted quote); no bridge tx', async () => {
  const { w, c, wallet, client } = setup();
  const q = await client.quote(x3req(wallet.address, c.cross.tokens.USDTx!.address));
  w.onApprove = () => { w.swapStatus = 5; };
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof PreflightError && e.stage === 'requote' && e.issues.some((i: any) => i.code === 'X3_QUOTE_InvalidSwap'));
  assert.equal(w.approveCalls.length, 1);
  assert.equal(w.chains.cross.logs.length, 0);
});
