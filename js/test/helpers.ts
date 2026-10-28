// Shared fixtures for the SDK tests: a fully configured testnet world, a mainnet world with the X1 swap pool, and small request builders.
import assert from 'node:assert/strict';
import { Wallet, getAddress } from 'ethers';
import { BridgeClient, Route } from '../src/index.js';
import type { PreflightError, PreflightIssue } from '../src/index.js';
import { deepFreeze } from '../src/freeze.js';
import { sealQuote } from '../src/seal.js';
import type { Quote } from '../src/index.js';
import { E18, RECIPIENT, TOKEN_A, World } from './world.js';

export const amount = 10n * E18;
export const OTHER = getAddress('0x5555555555555555555555555555555555555555');
export const realSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** testnet world: R1-R6, X2 and X3 work; the wallet holds ONE on BSC / HyperEVM and TOKEN_A on the hub */
export function setup() {
  const w = World.testnet();
  const c = w.net.chains;
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', c.bsc.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('hyperevm', c.hyperevm.tokens.ONE!.address, wallet.address, 1000n * E18);
  w.setBal('one', TOKEN_A, wallet.address, 1000n * E18);
  w.setFee('one', c.bsc.chainId, c.one.tokens.USDTx!.address, { min: 1n, nf: 0n, rate: 0n });
  w.swapOut = 100n * E18;
  return { w, c, wallet, client: new BridgeClient(w.options()) };
}

/** mainnet world with the X1 pool (BSC USDT -> ONEUSD) */
export function x1World() {
  const w = new World('mainnet');
  const c = w.net.chains;
  const hubId = c.one.chainId;
  const usdt = c.bsc.tokens.USDT!.address, usdtx = c.one.tokens.USDTx!.address, router = c.one.swap!.v3SwapRouter!;
  w.setPair('bsc', hubId, usdt, usdtx, true);
  w.setFee('bsc', hubId, usdt, { min: 5n * E18, nf: 0n, rate: 0n });
  w.execWhitelist.add(`one:${router.toLowerCase()}`);
  const wallet = new Wallet(Wallet.createRandom().privateKey);
  w.setBal('bsc', usdt, wallet.address, 1000n * E18);
  w.x1Out = 100n * E18 + 3_511_957n;
  return { w, c, wallet, usdt, usdtx, router, client: new BridgeClient(w.options({ quoteTtlMs: 60_000 })) };
}

export const r1 = (sender: string) => ({ route: Route.BSC_TO_ONE, amount, sender, recipient: RECIPIENT });
export const r5core = (sender: string) => ({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount, sender, recipient: RECIPIENT, hypercore: true });
export const x3req = (sender: string, usdtx: string, tokenIn: string = TOKEN_A) => ({ route: Route.SWAP_BRIDGE, amount: 50n * E18, sender, recipient: RECIPIENT, x3: { tokenIn, tokenOut: usdtx, poolFee: 0, toChain: 'bsc' as const } });

/** counters that must all stay 0 when a send is refused before anything is signed */
export const noMoney = (w: World) => ({ approves: w.approveCalls.length, bsc: w.chains.bsc.rawSent.length, one: w.chains.one.rawSent.length, hyper: w.chains.hyperevm.rawSent.length, bridgeLogs: w.chains.bsc.logs.length });
export const nothingSent = (w: World) => assert.deepEqual([w.approveCalls.length, w.chains.bsc.rawSent.length, w.chains.one.rawSent.length, w.chains.hyperevm.rawSent.length], [0, 0, 0, 0]);
export const rpcCalls = (w: World) => Object.values(w.chains).reduce((n, ch) => n + ch.calls.length, 0);
export const codes = (e: PreflightError) => e.issues.map((i: PreflightIssue) => i.code);

/** (re)seal a hand-built or hand-modified quote, so only the structural / binding checks (not the digest) can refuse it */
export function sealForTest(q: Omit<Quote, 'seal'> & { seal?: unknown }): Quote {
  const { seal: _seal, ...rest } = q as any;
  return deepFreeze(sealQuote(rest));
}
