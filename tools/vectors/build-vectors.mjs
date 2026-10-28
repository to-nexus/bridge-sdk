#!/usr/bin/env node
// Builds tools/vectors/vectors.json (shared golden vectors for the JS and Go SDK tests) and copies it to
// js/test/vectors.json and go/bridgesdk/testdata/vectors.json.
//
//   BRIDGE_CONTRACTS_DIR=<checkout of github.com/to-nexus/bridge-contracts> node tools/vectors/build-vectors.mjs
//   re-runs the Solidity harness (local EVM, no RPC) and `cast`. The harness is copied into that checkout for the run (script/ForwardMathVectors.tmp.s.sol)
//   and removed afterwards; forge writes its build output to a temp dir, so the checkout is left as it was.
//
// Sources of truth, deliberately NOT the SDK code under test:
//   - multihop / raw settlement numbers : forge script ForwardMath.s.sol (real CrossBridge + BridgeVerifier bytecode)
//   - calldata / extraData encodings    : `cast calldata` / `cast concat-hex`
//   - X1 / X3 / X2 floor arithmetic     : plain BigInt below, formulas from src/SwapBridgeRouter.sol
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, copyFileSync, mkdirSync, existsSync, rmSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const kit = resolve(here, '../..');
const repo = process.env.BRIDGE_CONTRACTS_DIR ? resolve(process.env.BRIDGE_CONTRACTS_DIR) : '';
if (!repo || !existsSync(resolve(repo, 'src/CrossBridge.sol'))) {
  console.error('BRIDGE_CONTRACTS_DIR must point to a checkout of github.com/to-nexus/bridge-contracts (src/CrossBridge.sol not found).');
  process.exit(2);
}
const nets = JSON.parse(readFileSync(resolve(kit, 'networks.json'), 'utf8')).networks;

const sh = (cmd, args, opts = {}) => execFileSync(cmd, args, { encoding: 'utf8', cwd: repo, maxBuffer: 64 * 1024 * 1024, ...opts }).trim();
const cast = (...a) => sh('cast', a);
const calldata = (sig, ...args) => cast('calldata', sig, ...args.map(String));
const concat = (...hex) => cast('concat-hex', ...hex).toLowerCase();
const byteLen = (hex) => (hex.length - 2) / 2;

// ---- 1. multihop + raw settlement vectors from the Solidity harness ------------------------------------------
const harness = resolve(repo, 'script/ForwardMathVectors.tmp.s.sol');
const buildDir = mkdtempSync(resolve(tmpdir(), 'sdk-vectors-'));
let forgeOut;
try {
  copyFileSync(resolve(here, 'ForwardMath.s.sol'), harness);
  forgeOut = sh('forge', ['script', 'script/ForwardMathVectors.tmp.s.sol:ForwardMathVectors', '-vv', '--out', resolve(buildDir, 'out'), '--cache-path', resolve(buildDir, 'cache')]);
} finally {
  rmSync(harness, { force: true });
  rmSync(buildDir, { recursive: true, force: true });
}
const forwardMath = [];
const multihop = [];
for (const line of forgeOut.split('\n')) {
  const m = line.match(/VEC (\{.*\})\s*$/);
  if (!m) continue;
  const o = JSON.parse(m[1]);
  (o.kind === 'raw' ? forwardMath : multihop).push(o);
}
if (forwardMath.length < 8 || multihop.length < 16) throw new Error(`unexpected harness output: raw=${forwardMath.length} multihop=${multihop.length}`);
for (const o of [...forwardMath, ...multihop]) {
  const sel = o.reverted ? o.selector : o.settle?.reverted ? o.settle.selector : null;
  if (sel) {
    const short = sel.slice(0, 10);
    if (o.reverted) o.selector = short;
    else o.settle.selector = short;
  }
}

// ---- 2. encoding vectors via cast ------------------------------------------------------------------------------
const NATIVE = '0x0000000000000000000000000000000000000001';
const U1 = '0x1111111111111111111111111111111111111111'; // synthetic recipients (docs-only examples)
const U2 = '0x2222222222222222222222222222222222222222';
const U3 = '0x3333333333333333333333333333333333333333';
const BRIDGE_SIG = 'bridgeToken(uint256,address,address,uint256,uint256,uint256,bytes)';
const FWD_SIG = 'bridgeTokenForwarded(uint256,address,uint256,uint256,uint256,bytes)';
const CORE_SIG = 'transferToCoreFor(address,uint256)';
const EIS_SIG = 'exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))';
const SBR_SIG = 'swapBridgeExactInputSingle((address,address,uint24,uint256,uint256,uint160,(uint256,address,bytes)),uint256)';
const SBR_ETH_SIG = 'swapBridgeExactInputSingleETH((address,address,uint24,uint256,uint256,uint160,(uint256,address,bytes)),uint256)';

const bridgeCall = (i) => calldata(BRIDGE_SIG, i.toChainId, i.fromToken, i.to, i.value, i.networkFee, i.exFee, i.extraData);
const forwardExtra = (i) => concat(i.hub, calldata(FWD_SIG, i.destChainId, i.toDest, i.value2, i.networkFee2, i.exFee2Declared, i.inner));
const coreExtra = (i) => concat(i.token, calldata(CORE_SIG, i.coreRecipient, i.value));
const x1Extra = (i) => concat(i.router, calldata(EIS_SIG, `(${i.tokenIn},${i.tokenOut},${i.fee},${i.recipient},${i.deadline},${i.amountIn},${i.amountOutMinimum},0)`));
const sbrCall = (i, eth) => calldata(eth ? SBR_ETH_SIG : SBR_SIG, `(${i.tokenIn},${i.tokenOut},${i.fee},${i.amountIn},${i.amountOutMinimum},0,(${i.toChainId},${i.recipient},${i.extraData}))`, i.deadline);

const M = nets.mainnet.chains, T = nets.testnet.chains;
const E18 = 10n ** 18n;
const encoding = [];

// single-hop routes (mainnet addresses)
const single = [
  ['R1', 'mainnet', { toChainId: 612055, fromToken: M.bsc.tokens.ONE.address, to: U1, value: 10n * E18, networkFee: 0n, exFee: 0n, extraData: '0x' }, 'BSC->ONE: BSC bridge, ERC20 source (value=10 ONE, fee 0), msg.value 0'],
  ['R2', 'mainnet', { toChainId: 56, fromToken: NATIVE, to: U1, value: 10n * E18, networkFee: 180000000000000000n, exFee: 0n, extraData: '0x' }, 'ONE->BSC: hub bridge, native source, msg.value = value+nf+ef'],
  ['R3', 'mainnet', { toChainId: 999, fromToken: NATIVE, to: U1, value: 10n * E18, networkFee: 180000000000000000n, exFee: 0n, extraData: '0x' }, 'ONE->HyperEVM: hub bridge, native source'],
  ['R4', 'testnet', { toChainId: 612044, fromToken: T.hyperevm.tokens.ONE.address, to: U1, value: 10n * E18, networkFee: 0n, exFee: 0n, extraData: '0x' }, 'HyperEVM->ONE: HyperEVM bridge, burn path, approve spender = bridge'],
];
for (const [route, network, i, note] of single) encoding.push({ name: `${route}-${network}`, kind: 'bridgeToken', route, network, note, inputs: s(i), expected: { calldata: bridgeCall(i) } });

// R5 (mainnet shape, plain delivery on the destination) — BSC -> ONE -> HyperEVM
{
  const hub = M.one.bridge;
  const f = { hub, destChainId: 999, toDest: U2, value2: 10n * E18, networkFee2: 180000000000000000n, exFee2Declared: 0n, inner: '0x' };
  const ed1 = forwardExtra(f);
  const call = { toChainId: 612055, fromToken: M.bsc.tokens.ONE.address, to: U1, value: 10n * E18 + 180000000000000000n, networkFee: 0n, exFee: 0n, extraData: ed1 };
  encoding.push({ name: 'R5-mainnet-plain', kind: 'multihop', route: 'R5', network: 'mainnet', note: 'BSC->ONE->HyperEVM, plain delivery', inputs: { forward: s(f), call: s(call) }, expected: { extraData1: ed1, extraData1Bytes: byteLen(ed1), calldata: bridgeCall(call) } });
}
// R6 — HyperEVM -> ONE -> BSC (testnet, 997/97 not used: hub -> 97)
{
  const hub = T.one.bridge;
  const f = { hub, destChainId: 97, toDest: U2, value2: 10n * E18, networkFee2: 180000000000000000n, exFee2Declared: 0n, inner: '0x' };
  const ed1 = forwardExtra(f);
  const call = { toChainId: 612044, fromToken: T.hyperevm.tokens.ONE.address, to: U1, value: 10n * E18 + 180000000000000000n, networkFee: 0n, exFee: 0n, extraData: ed1 };
  encoding.push({ name: 'R6-testnet-plain', kind: 'multihop', route: 'R6', network: 'testnet', note: 'HyperEVM->ONE->BSC, plain delivery', inputs: { forward: s(f), call: s(call) }, expected: { extraData1: ed1, extraData1Bytes: byteLen(ed1), calldata: bridgeCall(call) } });
}
// reference lengths: BSC(97) -> ONE -> HyperEVM(998) -> HyperCore, extraData2 = 88 B, extraData1 = 344 B
{
  const coreIn = { token: T.hyperevm.tokens.ONE.address, coreRecipient: U3, value: 5000000000000000042n };
  const ed2 = coreExtra(coreIn);
  const f = { hub: T.one.bridge, destChainId: 998, toDest: U2, value2: 5000000000000000042n, networkFee2: 180000000000000000n, exFee2Declared: 0n, inner: ed2 };
  const ed1 = forwardExtra(f);
  const call = { toChainId: 612044, fromToken: T.bsc.tokens.ONE.address, to: U1, value: 5180000000000000042n, networkFee: 0n, exFee: 0n, extraData: ed1 };
  if (byteLen(ed2) !== 88 || byteLen(ed1) !== 344) throw new Error(`extraData reference lengths wrong: ${byteLen(ed2)} / ${byteLen(ed1)}`);
  encoding.push({ name: 'R5-X2-testnet-reference-lengths', kind: 'multihop', route: 'R5', network: 'testnet', note: 'reference lengths: extraData2 = 88 B, extraData1 = 344 B', inputs: { core: s(coreIn), forward: s(f), call: s(call) }, expected: { extraData2: ed2, extraData2Bytes: 88, extraData1: ed1, extraData1Bytes: 344, calldata: bridgeCall(call) } });
}
// X2 on R3 (HyperCore deposit directly from the hub)
{
  const coreIn = { token: T.hyperevm.tokens.ONE.address, coreRecipient: U3, value: 12345678901234567890n };
  const ed = coreExtra(coreIn);
  const call = { toChainId: 998, fromToken: NATIVE, to: U2, value: 12345678901234567890n, networkFee: 180000000000000000n, exFee: 0n, extraData: ed };
  encoding.push({ name: 'X2-R3-testnet', kind: 'core', route: 'X2', network: 'testnet', note: 'ONE->HyperEVM with HyperCore deposit (transferToCoreFor)', inputs: { core: s(coreIn), call: s(call) }, expected: { extraData: ed, extraDataBytes: byteLen(ed), calldata: bridgeCall(call) } });
}
// X1: BSC USDT -> ONE chain ONEUSD (mainnet)
{
  const sw = { router: M.one.swap.v3SwapRouter, tokenIn: M.one.tokens.USDTx.address, tokenOut: M.one.tokens.ONEUSD.address, fee: 0, recipient: U1, deadline: 1800000000, amountIn: 100n * E18, amountOutMinimum: 99495929364966904498n };
  const ed = x1Extra(sw);
  const call = { toChainId: 612055, fromToken: M.bsc.tokens.USDT.address, to: U1, value: 100n * E18, networkFee: 5n * E18, exFee: 0n, extraData: ed };
  encoding.push({ name: 'X1-mainnet', kind: 'x1', route: 'X1', network: 'mainnet', note: 'BSC USDT -> ONEUSD via BridgeExecutor extraCall (deadline-in-struct exactInputSingle, selector 0x414bf389)', inputs: { swap: s(sw), call: s(call) }, expected: { extraData: ed, extraDataBytes: byteLen(ed), calldata: bridgeCall(call) } });
}
// X3: swap on ONE then bridge (ERC20 and native-in variants)
{
  const sb = { tokenIn: M.one.tokens.ONEUSD.address, tokenOut: M.one.tokens.USDTx.address, fee: 0, amountIn: 100n * E18, amountOutMinimum: 99495929364966904498n, toChainId: 56, recipient: U1, extraData: '0x', deadline: 1800000000 };
  encoding.push({ name: 'X3-erc20-mainnet-shape', kind: 'x3', route: 'X3', network: 'mainnet', note: 'ONEUSD -> USDTx -> BSC USDT', inputs: s(sb), expected: { calldata: sbrCall(sb, false) } });
  const sbe = { ...sb, tokenIn: M.one.tokens.WCROSS.address, tokenOut: M.one.tokens.USDTx.address };
  encoding.push({ name: 'X3-native-mainnet-shape', kind: 'x3eth', route: 'X3', network: 'mainnet', note: 'native ONE (tokenIn = WCROSS) -> USDTx -> BSC USDT, msg.value = amountIn', inputs: s(sbe), expected: { calldata: sbrCall(sbe, true) } });
}

// ---- 3. arithmetic vectors (plain BigInt, formulas from SwapBridgeRouter) -----------------------------
const floorBps = (x, bps) => (x * (10000n - BigInt(bps))) / 10000n;
const x1Floor = [
  { quote: '1000035119570278628', slippageBps: 50 },
  { quote: '99995908909514476883', slippageBps: 50 },
  { quote: '1', slippageBps: 50 },
  { quote: '123456789012345678901', slippageBps: 0 },
  { quote: '123456789012345678901', slippageBps: 10000 },
].map((c) => ({ ...c, amountOutMinimum: floorBps(BigInt(c.quote), c.slippageBps).toString() }));

// SwapBridgeRouter._getExpectedBridgeAmount: remaining = total - nf; bridgeValue = remaining*d/(d+rate); exFee = bridgeValue*rate/d
const D = 10000n;
const expectedBridge = (total, nf, rate, min) => {
  if (total <= nf) return { status: 'InsufficientForFee', bridgeValue: 0n, networkFee: nf, exFee: 0n };
  const bv = ((total - nf) * D) / (D + rate);
  const ex = (bv * rate) / D;
  return { status: bv >= min ? 'Success' : 'InsufficientValue', bridgeValue: bv, networkFee: nf, exFee: ex };
};
const x3Case = (name, cfgQuote, cfgFresh, swapOut, freshSwapOut, slippageBps, min = 1n) => {
  const amountOutMinimum = floorBps(swapOut, slippageBps);
  const q = expectedBridge(swapOut, cfgQuote.nf, cfgQuote.rate, min);
  const minBridge = expectedBridge(amountOutMinimum, cfgQuote.nf, cfgQuote.rate, min).bridgeValue;
  const atFresh = expectedBridge(amountOutMinimum, cfgFresh.nf, cfgFresh.rate, min);
  const freshExpected = expectedBridge(freshSwapOut, cfgFresh.nf, cfgFresh.rate, min);
  return {
    name,
    quote: { swapAmountOut: swapOut.toString(), networkFeeRate: { nf: cfgQuote.nf.toString(), rate: cfgQuote.rate.toString() }, min: min.toString(), slippageBps },
    expected: { amountOutMinimum: amountOutMinimum.toString(), bridgeValue: q.bridgeValue.toString(), exFee: q.exFee.toString(), minBridgeValue: minBridge.toString() },
    fresh: { swapAmountOut: freshSwapOut.toString(), nf: cfgFresh.nf.toString(), rate: cfgFresh.rate.toString() },
    freshCheck: {
      deliveryAtOriginalFloor: atFresh.bridgeValue.toString(),
      deliveryOk: atFresh.status === 'Success' && atFresh.bridgeValue >= minBridge,
      freshSwapOutAtLeastFloor: freshSwapOut >= amountOutMinimum,
      freshExpectedBridgeValue: freshExpected.bridgeValue.toString(),
    },
  };
};
const x3 = [
  x3Case('x3-fee-rises-while-output-rises', { nf: 0n, rate: 0n }, { nf: 5n, rate: 0n }, 100n, 110n, 100),
  x3Case('x3-unchanged', { nf: 18n * 10n ** 16n, rate: 0n }, { nf: 18n * 10n ** 16n, rate: 0n }, 100n * E18, 100n * E18, 50, 5n * E18),
  x3Case('x3-fee-drops', { nf: 18n * 10n ** 16n, rate: 30n }, { nf: 10n * 10n ** 16n, rate: 30n }, 100n * E18, 100n * E18, 50, 5n * E18),
  x3Case('x3-rate-rises', { nf: 0n, rate: 30n }, { nf: 0n, rate: 60n }, 100n * E18, 100n * E18, 50, 5n * E18),
  x3Case('x3-price-drop-below-floor', { nf: 0n, rate: 0n }, { nf: 0n, rate: 0n }, 100n * E18, 98n * E18, 50, 5n * E18),
];

const x2Core = [
  { amount: '5000000000000000042', coreUnit: '10000000000' },
  { amount: '9999999999', coreUnit: '10000000000' },
  { amount: '10000000000', coreUnit: '10000000000' },
  { amount: '123456789012345678901', coreUnit: '10000000000' },
  { amount: '777', coreUnit: '1' },
].map((c) => {
  const a = BigInt(c.amount), u = BigInt(c.coreUnit);
  const core = a / u;
  return { ...c, coreAmountEvm: (core * u).toString(), coreAmount: core.toString(), dust: (a - core * u).toString() };
});

const out = {
  version: 1,
  generatedBy: 'tools/vectors/build-vectors.mjs (+ ForwardMath.s.sol, cast)',
  generatedAt: new Date().toISOString(),
  note: 'Encodings from `cast`; multihop/raw settlement from the real CrossBridge/BridgeVerifier bytecode on a local EVM.',
  forwardMath,
  multihop,
  encoding,
  x1Floor,
  x3,
  x2Core,
};
const json = JSON.stringify(out, null, 2) + '\n';
writeFileSync(resolve(here, 'vectors.json'), json);
mkdirSync(resolve(kit, 'js/test'), { recursive: true });
mkdirSync(resolve(kit, 'go/bridgesdk/testdata'), { recursive: true });
copyFileSync(resolve(here, 'vectors.json'), resolve(kit, 'js/test/vectors.json'));
copyFileSync(resolve(here, 'vectors.json'), resolve(kit, 'go/bridgesdk/testdata/vectors.json'));
console.log(`vectors.json: raw=${forwardMath.length} multihop=${multihop.length} encoding=${encoding.length} x1Floor=${x1Floor.length} x3=${x3.length} x2Core=${x2Core.length}`);

function s(o) {
  const r = {};
  for (const [k, v] of Object.entries(o)) r[k] = typeof v === 'bigint' ? v.toString() : v;
  return r;
}
