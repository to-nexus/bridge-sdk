// quote() for every route against the fake world: amounts, msg.value, approve target, recipients per hop, swap options and floors.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Interface, Wallet, getAddress } from 'ethers';
import { BridgeClient, InvalidOptionError, PreflightError, RequoteRequiredError, Route, decodeBridgeError, floorBps } from '../src/index.js';
import { swapBridgeRouterAbi, v3RouterAbi } from '../src/abi/index.js';
import { FakeWalletSigner } from './fake.js';
import { E18, NATIVE, RECIPIENT, SENDER, TOKEN_A, World, bridgeI } from './world.js';
import { r1 } from './helpers.js';

const R2 = getAddress('0x3333333333333333333333333333333333333333');
const R3 = getAddress('0x4444444444444444444444444444444444444444');
const dec = (data: string) => bridgeI.decodeFunctionData('bridgeToken', data);
const nf = 18n * 10n ** 16n;
const amount = 10n * E18;

async function x1WorldAsync() {
  const w = new World('mainnet');
  const c = w.net.chains;
  const hubId = c.cross.chainId;
  const usdt = c.bsc.tokens.USDT!.address, usdtx = c.cross.tokens.USDTx!.address, router = c.cross.swap!.v3SwapRouter!;
  w.setPair('bsc', hubId, usdt, usdtx, true);
  w.setFee('bsc', hubId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`cross:${router.toLowerCase()}`);
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
  w.setBal('cross', TOKEN_A, wallet.address, 1000n * E18);
  const usdtx = c.cross.tokens.USDTx!.address;
  w.setFee('cross', c.bsc.chainId, usdtx, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n * E18;
  return { w, c, wallet, usdtx, client: new BridgeClient(w.options()) };
}
const x3Req = (sender: string, usdtx: string, swap?: { slippageBps?: number; deadlineSec?: number }) => ({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender, recipient: RECIPIENT, ...(swap ? { swap } : {}), x3: { tokenIn: TOKEN_A, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' as const } });

test('R1..R4: single hop amounts, msg.value and approve target', async () => {
  const w = World.testnet(); const c = w.net.chains; const client = new BridgeClient(w.options());
  const v = 10n * E18;
  const r1 = await client.quote({ route: Route.BSC_TO_ONE, amount: v, sender: SENDER, recipient: RECIPIENT });
  assert.equal(r1.tx.to, c.bsc.bridge); assert.equal(r1.tx.value, 0n);
  assert.deepEqual(r1.approval, { token: c.bsc.tokens.ONE!.address, spender: c.bsc.bridge, amount: v });
  assert.equal(dec(r1.tx.data)[0], BigInt(c.cross.chainId));

  const r2 = await client.quote({ route: Route.ONE_TO_BSC, amount: v, sender: SENDER, recipient: RECIPIENT });
  assert.equal(r2.tx.to, c.cross.bridge); assert.equal(r2.approval, null);
  assert.equal(r2.tx.value, v + nf, 'msg.value = value + networkFee + exFee, exactly');
  assert.equal(dec(r2.tx.data)[1], NATIVE);

  const r3 = await client.quote({ route: Route.ONE_TO_HYPEREVM, amount: v, sender: SENDER, recipient: RECIPIENT });
  assert.equal(dec(r3.tx.data)[0], BigInt(c.hyperevm.chainId)); assert.equal(r3.tx.value, v + nf);

  const r4 = await client.quote({ route: Route.HYPEREVM_TO_ONE, amount: v, sender: SENDER, recipient: RECIPIENT });
  assert.equal(r4.approval!.spender, c.hyperevm.bridge); assert.equal(r4.approval!.token, c.hyperevm.tokens.ONE!.address);
  assert.equal(r4.details.hop1Kind, 'plain');
});

test('native source (R2/R3/X2/X3-native): send() needs no approve, msg.value = value+fees, Mode L and Mode W', async () => {
  const w = World.testnet(); const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  const client = new BridgeClient(w.options());
  const q = await client.quote({ route: Route.ONE_TO_BSC, amount: 10n * E18, sender: wallet.address, recipient: RECIPIENT });
  const sent = await client.send(wallet, q);
  assert.equal(w.approveCalls.length, 0);
  assert.equal(sent.nonce, 0);
  const h = await sent.wait();
  assert.equal(h.hop1.destChainId, c.bsc.chainId);
  const raw = w.chains.cross.rawSent;
  assert.equal(raw.length, 1);
  const { FakeWalletSigner } = await import('./fake.js');
  const wl = new FakeWalletSigner(wallet.address, w.chains.cross);
  const q2 = await client.quote({ route: Route.ONE_TO_HYPEREVM, amount: 10n * E18, sender: wallet.address, recipient: RECIPIENT });
  const s2 = await client.send(wl, q2);
  assert.equal(s2.mode, 'W');
  assert.equal(wl.sent.length, 1, 'only the bridge tx, no approve');
});

test('below minimum / unregistered pair are refused with structured issues', async () => {
  const w = World.testnet(); const client = new BridgeClient(w.options());
  await assert.rejects(() => client.quote({ route: Route.ONE_TO_BSC, amount: 4n * E18, sender: SENDER, recipient: RECIPIENT }), (e: any) => e instanceof PreflightError && e.issues[0].code === 'BELOW_MINIMUM');
  w.pairs.clear();
  await assert.rejects(() => client.quote({ route: Route.ONE_TO_BSC, amount: 10n * E18, sender: SENDER, recipient: RECIPIENT }), (e: any) => e instanceof PreflightError && e.issues[0].code === 'PAIR_NOT_REGISTERED');
});

test('R5/R6: backward computation, buffer in the declared exFee, per-hop recipients, min delivered v2', async () => {
  const w = World.testnet(); const c = w.net.chains; const client = new BridgeClient(w.options());
  const v2 = 10n * E18; const buffer = 5n * 10n ** 16n;
  const q = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: v2, sender: SENDER, recipient: RECIPIENT, recipients: { onHub: R2, dest: R3 }, hop2FeeBuffer: buffer });
  assert.equal(q.v2, v2);
  assert.equal(q.value, v2 + nf + buffer, 'v1 = v2 + nf2 + (ef2 + buffer)');
  assert.equal(q.fees.hop2ExFeeDeclared, buffer);
  assert.equal(q.limits.minDelivered, v2);
  const args = dec(q.tx.data);
  assert.equal(args[2], R2, 'hop-1 `to` = toOnHub'); assert.equal(args[3], q.value);
  const ed: string = args[6];
  assert.equal(ed.slice(0, 42).toLowerCase(), c.cross.bridge!.toLowerCase());
  assert.equal('0x' + ed.slice(42, 50), '0x55fd0776');
  const fwd = bridgeI.decodeFunctionData('bridgeTokenForwarded', '0x' + ed.slice(42));
  assert.deepEqual([fwd[0], fwd[1], fwd[2], fwd[3], fwd[4], fwd[5]], [BigInt(c.hyperevm.chainId), R3, v2, nf, buffer, '0x']);

  const q6 = await client.quote({ route: Route.HYPEREVM_TO_ONE_TO_BSC, amount: v2, sender: SENDER, recipient: RECIPIENT });
  assert.equal(q6.sourceChainKey, 'hyperevm'); assert.equal(q6.approval!.token, c.hyperevm.tokens.ONE!.address);
  assert.equal(q6.totalSpend, v2 + nf);
  // minimum applies at hop 2 (hub verifier) AND hop 1
  await assert.rejects(() => client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: 4n * E18, sender: SENDER, recipient: RECIPIENT }), (e: any) => e instanceof PreflightError);
});

test('X2: HyperCore deposit on R3 and on R5 (innermost extraData = token || transferToCoreFor(core, value)); rounding reported', async () => {
  const w = World.testnet(); const c = w.net.chains; const client = new BridgeClient(w.options());
  const v = 5n * E18 + 42n;
  const q = await client.quote({ route: Route.ONE_TO_HYPERCORE, amount: v, sender: SENDER, recipient: RECIPIENT, recipients: { core: R3 } });
  const ed: string = dec(q.tx.data)[6];
  assert.equal(ed.length - 2, 88 * 2);
  assert.equal(ed.slice(0, 42).toLowerCase(), c.hyperevm.tokens.ONE!.address.toLowerCase());
  assert.equal('0x' + ed.slice(42, 50), '0x9438568d');
  assert.equal(q.details.coreAmount, 500000000n); assert.equal(q.details.coreDust, 42n);
  assert.equal(q.details.hop1Kind, 'x2');
  const q5 = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, hypercore: true, amount: v, sender: SENDER, recipient: RECIPIENT });
  assert.equal(q5.details.hop2Kind, 'x2');
  assert.equal((dec(q5.tx.data)[6].length - 2) / 2, 344);
});

test('X3 quote: both floors (swap floor enforced on chain, delivery floor shown), native vs ERC20 input', async () => {
  const w = World.testnet(); const c = w.net.chains; const client = new BridgeClient(w.options());
  const usdtx = c.cross.tokens.USDTx!.address;
  w.swapOut = 100n * E18;
  const q = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: SENDER, recipient: RECIPIENT, swap: { slippageBps: 100 }, x3: { tokenIn: 'native', tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } });
  assert.equal(q.tx.value, 50n * E18); assert.equal(q.approval, null);
  assert.equal(q.limits.amountOutMinimum, 99n * E18);
  assert.equal(q.limits.minBridgeValue, 99n * E18 - nf);
  const sb = new Interface(swapBridgeRouterAbi as any).parseTransaction({ data: q.tx.data, value: q.tx.value })!;
  assert.equal(sb.name, 'swapBridgeExactInputSingleETH');
  assert.equal(sb.args[0][0], c.cross.tokens.WCROSS!.address);
  const erc = await client.quote({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender: SENDER, recipient: RECIPIENT, x3: { tokenIn: usdtx, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' } }).catch((e) => e);
  assert.ok(erc instanceof Error || erc.approval); // tokenIn == tokenOut is a contract-level revert; SDK surfaces via preflight/simulation
});

test('X1 on mainnet: quote via QuoterV2, floors, deadline, whitelist preflight; requote price drop refused / price rise keeps the ORIGINAL floor', async () => {
  const w = new World('mainnet'); const c = w.net.chains; const hubId = c.cross.chainId;
  const usdt = c.bsc.tokens.USDT!.address; const usdtx = c.cross.tokens.USDTx!.address; const router = c.cross.swap!.v3SwapRouter!;
  w.setPair('bsc', hubId, usdt, usdtx, true);
  w.setFee('bsc', hubId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`cross:${router.toLowerCase()}`);
  w.execWhitelist.add(`cross:${c.cross.bridge!.toLowerCase()}`);
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', usdt, wallet.address, 1000n * E18);
  w.x1Out = 100n * E18 + 3_511_957n; // quoter result for 100 USDTx
  const client = new BridgeClient(w.options({ quoteTtlMs: 60_000 }));
  const req = { route: Route.BSC_USDT_TO_ONEUSD, amount: 100n * E18, sender: wallet.address, recipient: RECIPIENT };
  const q = await client.quote(req);
  const expectedFloor = (w.x1Out * 9950n) / 10000n;
  assert.equal(q.limits.amountOutMinimum, expectedFloor);
  assert.equal(q.deadline, Math.floor(w.clock / 1000) + 1800, 'default deadline 1800 s');
  const ed: string = dec(q.tx.data)[6];
  assert.equal(ed.slice(0, 42).toLowerCase(), router.toLowerCase());
  assert.equal('0x' + ed.slice(42, 50), '0x414bf389');
  const swap = new Interface(v3RouterAbi as any).decodeFunctionData('exactInputSingle', '0x' + ed.slice(42))[0];
  assert.deepEqual([swap[0], swap[1], swap[2], swap[3], swap[5], swap[6]], [usdtx, c.cross.tokens.ONEUSD!.address, 0n, RECIPIENT, 100n * E18, expectedFloor]);
  const pf = await client.preflight(q);
  assert.equal(pf.ok, true, JSON.stringify(pf.issues));
  w.execWhitelist.delete(`cross:${router.toLowerCase()}`);
  assert.ok((await client.preflight(q)).issues.some((i) => i.code === 'EXECUTOR_NOT_WHITELISTED'));
  w.execWhitelist.add(`cross:${router.toLowerCase()}`);

  // price drop between quote and send: refused
  w.onApprove = () => { w.x1Out = (expectedFloor * 99n) / 100n; };
  await assert.rejects(() => client.send(wallet, q), (e: any) => e instanceof RequoteRequiredError && e.violated.includes('swapOutputBelowFloor'));
  assert.equal(w.chains.bsc.logs.length, 0);

  // price rise: adopted, floor and deadline stay as originally approved
  const w2 = new World('mainnet'); w2.setPair('bsc', hubId, usdt, usdtx, true); w2.setFee('bsc', hubId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w2.execWhitelist.add(`cross:${router.toLowerCase()}`);
  const wallet2 = new Wallet(Wallet.createRandom().privateKey); w2.setBal('bsc', usdt, wallet2.address, 1000n * E18);
  w2.x1Out = 100n * E18;
  const c2 = new BridgeClient(w2.options({ quoteTtlMs: 60_000 }));
  const q2 = await c2.quote({ ...req, sender: wallet2.address });
  w2.onApprove = () => { w2.x1Out = 105n * E18; };
  const sent = await c2.send(wallet2, q2);
  assert.equal(sent.quote.limits.amountOutMinimum, q2.limits.amountOutMinimum);
  assert.equal(sent.quote.deadline, q2.deadline);
  const swap2 = new Interface(v3RouterAbi as any).decodeFunctionData('exactInputSingle', '0x' + dec(sent.intent.data)[6].slice(42))[0];
  assert.equal(swap2[6], q2.limits.amountOutMinimum, 'calldata floor = ORIGINAL');
});

test('decodeBridgeError: selector table is generated from the ABIs', () => {
  assert.equal(decodeBridgeError('0x58e88785')!.name, 'BaseBridgeInvalidAmount');
  const inv = decodeBridgeError('0x943892eb' + (5n).toString(16).padStart(64, '0') + (4n).toString(16).padStart(64, '0'))!;
  assert.equal(inv.name, 'BaseBridgeInvalidValue'); assert.equal(inv.args.expected, 5n); assert.equal(inv.args.actual, 4n);
  assert.equal(decodeBridgeError('0x8399ac8d' + '00'.repeat(64))!.name, 'BaseBridgeForwardStagedInsufficient');
  assert.equal(decodeBridgeError('0xdeadbeef'), null);
  assert.equal(decodeBridgeError(null), null);
  assert.ok(decodeBridgeError('0xa6bec22b')!.hint);
});

test('X1: unset slippage = 50 bps, an explicit 0 is exactly 0 (floor == quote), out-of-range options are refused', async () => {
  const { w, wallet, client } = await x1WorldAsync();
  const q = await client.quote(x1Req(wallet.address));
  assert.equal(q.limits.amountOutMinimum, floorBps(w.x1Out, 50));
  assert.equal(q.limits.amountOutMinimum, (w.x1Out * 9950n) / 10000n);
  const q0 = await client.quote(x1Req(wallet.address, { slippageBps: 0 }));
  assert.equal(q0.limits.amountOutMinimum, w.x1Out, 'explicit 0 bps must not fall back to the 50 bps default');
  for (const bad of [{ slippageBps: 10_000 }, { slippageBps: -1 }, { slippageBps: 0.5 }, { deadlineSec: 0 }, { deadlineSec: -5 }]) {
    await assert.rejects(() => client.quote(x1Req(wallet.address, bad)), (e: any) => e instanceof InvalidOptionError, JSON.stringify(bad));
  }
  // Send path: the explicit-0 floor survives approve -> requote (unchanged price) and lands in the calldata
  const sent = await client.send(wallet, q0);
  assert.equal(sent.quote.limits.amountOutMinimum, w.x1Out);
  assert.equal(sent.quote.details.expectedSwapOut, w.x1Out);
});

test('X3: unset = 50 bps, explicit 0 => floor == swap output, out-of-range refused, Send keeps the floor', async () => {
  const { w, wallet, usdtx, client } = await x3WorldAsync();
  const q = await client.quote(x3Req(wallet.address, usdtx));
  assert.equal(q.limits.amountOutMinimum, (w.swapOut * 9950n) / 10000n);
  const q0 = await client.quote(x3Req(wallet.address, usdtx, { slippageBps: 0 }));
  assert.equal(q0.limits.amountOutMinimum, w.swapOut);
  for (const bad of [{ slippageBps: 10_000 }, { slippageBps: -1 }, { deadlineSec: 0 }]) {
    await assert.rejects(() => client.quote(x3Req(wallet.address, usdtx, bad)), (e: any) => e instanceof InvalidOptionError, JSON.stringify(bad));
  }
  const sent = await client.send(wallet, q0);
  assert.equal(sent.quote.limits.amountOutMinimum, w.swapOut);
});
