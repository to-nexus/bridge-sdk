// A tiny deterministic "world": three FakeChains with just enough contract behaviour for the SDK state machine.
import { Interface, getAddress } from 'ethers';
import { bridgeAbi, erc20Abi, executorAbi, quoterV2Abi, swapBridgeRouterAbi, verifierAbi, hyperTokenAbi, v3PoolAbi } from '../src/abi/index.js';
import type { ClientOptions } from '../src/ctx.js';
import { expectedBridgeAmount } from '../src/math.js';
import { getNetworksData } from '../src/networks.js';
import type { ChainKey, NetworkData, NetworkName, NetworksData } from '../src/networks-types.js';
import { FakeChain, type FakeContract, type LogInput, type TxCtx } from './fake.js';

export const NATIVE = '0x0000000000000000000000000000000000000001';
export const bridgeI = new Interface(bridgeAbi as any);
export const verifierI = new Interface(verifierAbi as any);
export const erc20I = new Interface(erc20Abi as any);
export const executorI = new Interface(executorAbi as any);
export const quoterI = new Interface(quoterV2Abi as any);
export const sbrI = new Interface(swapBridgeRouterAbi as any);
export const hyperI = new Interface(hyperTokenAbi as any);
export const v3RouterI = new Interface([]);
export const poolI = new Interface(v3PoolAbi as any);
export const v3FactoryI = new Interface(['function getPool(address,address,uint24) view returns (address)']);

export const SENDER = getAddress('0x1111111111111111111111111111111111111111');
export const RECIPIENT = getAddress('0x2222222222222222222222222222222222222222');
export const TOKEN_A = getAddress('0xaAaAaAaaAaAaAaaAaAAAAAAAAaaaAaAaAaaAaaAa');
export const E18 = 10n ** 18n;

export interface FeeCfg { min: bigint; nf: bigint; rate: bigint }
const revert = (data: string) => Object.assign(new Error('execution reverted'), { data, code: 3 });
export const INVALID_AMOUNT = '0x58e88785';

export class World {
  net: NetworkData;
  chains: Record<ChainKey, FakeChain>;
  fees = new Map<string, FeeCfg>();
  pairs = new Map<string, { remote: string; isOrigin: boolean; paused: boolean; minted: bigint }>();
  balances = new Map<string, bigint>();
  allowances = new Map<string, bigint>();
  initIndex = new Map<string, bigint>();
  finalizeNext = new Map<string, bigint>();
  pending = new Map<string, bigint[]>();
  paused = new Set<string>();
  execWhitelist = new Set<string>();
  methodChecks = new Set<string>();
  hyperCoreSet = true;
  poolMissing = false;
  swapStatus: number | null = null; // override the SwapBridgeRouter quote status (e.g. 5 = InvalidSwap)
  maxExtra = new Map<string, bigint>(); // per-chain maxExtraDataLength (default 1024)
  routerBridgeOverride: string | null = null;
  coreUnit = 10n ** 10n;
  x1Out = 1_000_000_000_000_000_000n; // quoter output per call (set per test)
  swapOut = 100n * E18; // SwapBridgeRouter quote swap output
  approveFails = false;
  approveCalls: bigint[] = [];
  onApprove: (() => void) | null = null;
  clock = 1_700_000_000_000;

  /** `networksData`: a client-side override of the whole networks data (the world then deploys its contracts at the OVERRIDE addresses) */
  constructor(public network: NetworkName, public networksData?: NetworksData) {
    this.net = (networksData ?? getNetworksData()).networks[network];
    this.chains = {
      one: new FakeChain(this.net.chains.one.chainId),
      bsc: new FakeChain(this.net.chains.bsc.chainId),
      hyperevm: new FakeChain(this.net.chains.hyperevm.chainId),
    };
    for (const key of ['one', 'bsc', 'hyperevm'] as ChainKey[]) this.deployChain(key);
  }

  feeKey = (key: ChainKey, to: number, token: string) => `${key}:${to}:${token.toLowerCase()}`;
  setFee(key: ChainKey, to: number, token: string, cfg: FeeCfg) { this.fees.set(this.feeKey(key, to, token), cfg); }
  getFee(key: ChainKey, to: number, token: string): FeeCfg { return this.fees.get(this.feeKey(key, to, token)) ?? { min: 5n * E18, nf: 0n, rate: 0n }; }
  setPair(key: ChainKey, to: number, local: string, remote: string, isOrigin = true, minted = 10n ** 30n) { this.pairs.set(`${key}:${to}:${local.toLowerCase()}`, { remote, isOrigin, paused: false, minted }); }
  bal(key: ChainKey, token: string, who: string) { return this.balances.get(`${key}:${token.toLowerCase()}:${who.toLowerCase()}`) ?? 0n; }
  setBal(key: ChainKey, token: string, who: string, v: bigint) { this.balances.set(`${key}:${token.toLowerCase()}:${who.toLowerCase()}`, v); }
  allow(key: ChainKey, token: string, owner: string, spender: string) { return this.allowances.get(`${key}:${token.toLowerCase()}:${owner.toLowerCase()}:${spender.toLowerCase()}`) ?? 0n; }
  setAllow(key: ChainKey, token: string, owner: string, spender: string, v: bigint) { this.allowances.set(`${key}:${token.toLowerCase()}:${owner.toLowerCase()}:${spender.toLowerCase()}`, v); }

  options(extra: Partial<ClientOptions> = {}): ClientOptions {
    return {
      network: this.network, providers: this.chains, now: () => this.clock, sleep: async (ms) => { this.clock += ms; },
      ...(this.networksData ? { networksData: this.networksData } : {}),
      ...extra,
    };
  }

  /** deploy a generic ERC20 on a chain */
  deployErc20(key: ChainKey, address: string) {
    const w = this;
    this.chains[key].deploy(address, {
      iface: erc20I,
      call: {
        balanceOf: ([a]) => w.bal(key, address, a),
        allowance: ([o, s]) => w.allow(key, address, o, s),
        decimals: () => 18,
        symbol: () => 'TKN',
      },
      tx: {
        approve: ([spender, amount], c) => {
          if (w.approveFails) throw new Error('approve reverts');
          w.approveCalls.push(amount);
          w.setAllow(key, address, c.from, spender, amount);
          w.onApprove?.();
        },
      },
    });
  }

  private deployChain(key: ChainKey) {
    const w = this;
    const cfg = this.net.chains[key];
    const chain = this.chains[key];
    for (const t of Object.values(cfg.tokens)) if (t && t.kind === 'erc20') this.deployErc20(key, t.address);
    if (key === 'hyperevm' && cfg.tokens.ONE) this.deployHyperToken(cfg.tokens.ONE.address);
    if (!cfg.bridge) return;

    const bridge: FakeContract = {
      iface: bridgeI,
      call: {
        paused: () => w.paused.has(key),
        bridgeVerifier: () => cfg.bridgeVerifier ?? '0x00000000000000000000000000000000000000f1',
        bridgeExecutor: () => (key === 'one' || key === 'hyperevm' ? (cfg.bridgeExecutor ?? '0x00000000000000000000000000000000000000e1') : '0x0000000000000000000000000000000000000000'),
        maxExtraDataLength: () => w.maxExtra.get(key) ?? 1024n,
        getTokenPair: ([to, token]) => {
          const p = w.pairs.get(`${key}:${to}:${String(token).toLowerCase()}`);
          return p ? [token, p.remote, p.isOrigin, p.paused, 0n, 0n, p.minted] : ['0x0000000000000000000000000000000000000000', '0x0000000000000000000000000000000000000000', false, false, 0n, 0n, 0n];
        },
        allPendingIndex: ([from]) => w.pending.get(`${key}:${from}`) ?? [],
        getNextFinalizeIndex: ([from]) => w.finalizeNext.get(`${key}:${from}`) ?? 0n,
        getPendingArguments: () => [[0n, 0n, NATIVE, NATIVE, 0n, '0x'], 3n, 0n],
        bridgeToken: ([to, token, , value, nf, ef], c) => { w.checkBridge(key, [to, token, value, nf, ef], c.from, c.value, false); return true; },
      },
      tx: {
        bridgeToken: (args, c) => w.execBridge(key, cfg.bridge!, args, c),
      },
    };
    chain.deploy(cfg.bridge, bridge);
    if (cfg.bridgeVerifier) {
      chain.deploy(cfg.bridgeVerifier, {
        iface: verifierI,
        call: {
          calculateFee: ([to, token, value]) => { const f = w.getFee(key, Number(to), token); return [f.min, f.nf, (value * f.rate) / 10000n]; },
          getTokenConfig: ([to, token]) => { const f = w.getFee(key, Number(to), token); return [f.min, f.nf, f.rate]; },
          denominator: () => 10000n,
        },
      });
    }
    if (cfg.bridgeExecutor) {
      chain.deploy(cfg.bridgeExecutor, {
        iface: executorI,
        call: {
          isWhitelistedTarget: ([t]) => w.execWhitelist.has(`${key}:${String(t).toLowerCase()}`),
          isMethodCheckEnabled: ([t]) => w.methodChecks.has(`${key}:${String(t).toLowerCase()}`),
          isWhitelistedMethod: ([t]) => w.execWhitelist.has(`${key}:${String(t).toLowerCase()}`),
        },
      });
    }
    if (key === 'one') this.deployOneExtras();
  }

  deployHyperToken(address: string) {
    const w = this;
    this.chains.hyperevm.deploy(address, {
      iface: new Interface([...(hyperTokenAbi as any), ...(erc20Abi as any).filter((x: any) => !(hyperTokenAbi as any).some((h: any) => h.name === x.name))]),
      call: {
        isCoreTokenIndexSet: () => w.hyperCoreSet,
        coreUnit: () => w.coreUnit,
        coreTransferableAmount: ([a]: any[]) => (a / w.coreUnit) * w.coreUnit,
        coreSystemAddress: () => '0x2000000000000000000000000000000000000Bda',
        balanceOf: ([a]: any[]) => w.bal('hyperevm', address, a),
        allowance: ([o, s]: any[]) => w.allow('hyperevm', address, o, s),
        approve: () => true,
        symbol: () => 'ONE',
        decimals: () => 18,
      } as any,
      tx: {
        approve: ([spender, amount], c) => { if (w.approveFails) throw new Error('approve reverts'); w.approveCalls.push(amount); w.setAllow('hyperevm', address, c.from, spender, amount); w.onApprove?.(); },
      },
    });
  }

  private deployOneExtras() {
    const w = this;
    const oneChain = this.net.chains.one;
    const sw = oneChain.swap;
    if (sw?.quoterV2) {
      this.chains.one.deploy(sw.quoterV2, { iface: quoterI, call: { quoteExactInputSingle: () => [w.x1Out, 0n, 0, 100000n] } });
    }
    if (sw?.v3Factory && sw?.x1Pool) {
      const pool = sw.x1Pool;
      this.chains.one.deploy(sw.v3Factory, { iface: v3FactoryI, call: { getPool: () => (w.poolMissing ? '0x0000000000000000000000000000000000000000' : pool.pool) } });
    }
    if (sw?.x1Pool) this.chains.one.deploy(sw.x1Pool.pool, { iface: poolI, call: { liquidity: () => 10n ** 27n, fee: () => 0, token0: () => NATIVE, token1: () => NATIVE } });
    if (sw?.swapBridgeRouter) {
      const router = sw.swapBridgeRouter;
      this.chains.one.deploy(router, {
        iface: sbrI,
        call: {
          bridge: () => w.routerBridgeOverride ?? w.net.chains.one.bridge!,
          getAmountSwapBridgeOut: ([to, , tokenOut, , amountIn]) => {
            const f = w.getFee('one', Number(to), tokenOut);
            const eb = expectedBridgeAmount(w.swapOut, f.nf, f.rate, f.min);
            const st = w.swapStatus !== null ? BigInt(w.swapStatus) : eb.status === 'Success' ? 1n : eb.status === 'InsufficientForFee' ? 3n : 4n;
            return [st, w.swapOut, eb.bridgeValue, eb.networkFee, eb.exFee, amountIn].slice(0, 5);
          },
          getExpectedBridgeAmount: ([to, token, total]) => {
            const f = w.getFee('one', Number(to), token);
            const eb = expectedBridgeAmount(total, f.nf, f.rate, f.min);
            return [eb.status === 'Success' ? 1n : eb.status === 'InsufficientForFee' ? 3n : 4n, eb.bridgeValue, eb.networkFee, eb.exFee];
          },
        },
        tx: {
          swapBridgeExactInputSingle: ([params], c) => w.execSwapBridge(router, params, c, false),
          swapBridgeExactInputSingleETH: ([params], c) => w.execSwapBridge(router, params, c, true),
        },
      });
    }
  }

  // ---- bridge behaviour ---------------------------------------------------------------------------------------
  checkBridge(key: ChainKey, [to, token, value, nf, ef]: any[], from: string | undefined, msgValue: bigint, wouldMutate: boolean) {
    const f = this.getFee(key, Number(to), token);
    const calcEx = (value * f.rate) / 10000n;
    if (!(value !== 0n && value >= f.min && nf >= f.nf && ef >= calcEx)) throw revert(INVALID_AMOUNT);
    const total = value + f.nf + calcEx;
    if (String(token).toLowerCase() === NATIVE) {
      if (msgValue < total) throw revert('0x943892eb' + total.toString(16).padStart(64, '0') + msgValue.toString(16).padStart(64, '0'));
    } else if (from) {
      if (this.allow(key, token, from, this.net.chains[key].bridge!) < total) throw revert('0xfb8f41b2');
      if (this.bal(key, token, from) < total) throw revert('0xe450d38c');
    }
    void wouldMutate;
    return { f, calcEx, total };
  }

  execBridge(key: ChainKey, bridgeAddr: string, args: any[], c: TxCtx): LogInput[] {
    const [to, token, rcpt, value, nf, ef, extra] = args;
    const { f, calcEx, total } = this.checkBridge(key, [to, token, value, nf, ef], c.from, c.value, true);
    const pair = this.pairs.get(`${key}:${to}:${String(token).toLowerCase()}`);
    if (String(token).toLowerCase() !== NATIVE) {
      this.setBal(key, token, c.from, this.bal(key, token, c.from) - total);
      this.setAllow(key, token, c.from, bridgeAddr, this.allow(key, token, c.from, bridgeAddr) - total);
    }
    return [this.initiatedLog(key, bridgeAddr, Number(to), token, pair?.remote ?? NATIVE, c.from, rcpt, value, f.nf, calcEx, extra)];
  }

  initiatedLog(key: ChainKey, bridgeAddr: string, to: number, fromToken: string, toToken: string, from: string, rcpt: string, value: bigint, nf: bigint, ef: bigint, extra: string): LogInput {
    const k = `${key}:${to}`;
    const idx = this.initIndex.get(k) ?? 0n;
    this.initIndex.set(k, idx + 1n);
    const ev = bridgeI.encodeEventLog('BridgeInitiated', [to, idx, fromToken, toToken, from, rcpt, value, nf, ef, extra, 1_700_000_000n]);
    return { address: bridgeAddr, topics: ev.topics as string[], data: ev.data };
  }

  execSwapBridge(router: string, params: any, c: TxCtx, native: boolean): LogInput[] {
    const [tokenIn, tokenOut, , amountIn, amountOutMinimum, , bp] = params as any[];
    const [toChainID, recipient, extra] = bp as any[];
    if (this.swapOut < amountOutMinimum) throw revert('0x');
    const f = this.getFee('one', Number(toChainID), tokenOut);
    const eb = expectedBridgeAmount(this.swapOut, f.nf, f.rate, f.min);
    if (eb.status !== 'Success') throw revert('0x');
    if (!native) {
      if (this.allow('one', tokenIn, c.from, router) < amountIn) throw revert('0xfb8f41b2');
      this.setBal('one', tokenIn, c.from, this.bal('one', tokenIn, c.from) - amountIn);
    }
    const hub = this.net.chains.one.bridge!;
    const pair = this.pairs.get(`one:${toChainID}:${String(tokenOut).toLowerCase()}`);
    const bi = this.initiatedLog('one', hub, Number(toChainID), tokenOut, pair?.remote ?? NATIVE, router, recipient, eb.bridgeValue, eb.networkFee, eb.exFee, extra);
    const idx = this.initIndex.get(`one:${Number(toChainID)}`)! - 1n;
    const sb = sbrI.encodeEventLog('SwapBridge', [c.from, tokenIn, tokenOut, amountIn, this.swapOut, toChainID, idx, eb.bridgeValue, eb.networkFee, eb.exFee]);
    return [{ address: router, topics: sb.topics as string[], data: sb.data }, bi];
  }

  /** configure a fully working testnet world for R1-R6 / X2 / X3 */
  static testnet(networksData?: NetworksData): World {
    const w = new World('testnet', networksData);
    const c = w.net.chains;
    const hubId = c.one.chainId;
    // pairs (source bridge -> toChain, token)
    w.setPair('bsc', hubId, c.bsc.tokens.ONE!.address, NATIVE, true);
    w.setPair('hyperevm', hubId, c.hyperevm.tokens.ONE!.address, NATIVE, false);
    w.setPair('one', c.bsc.chainId, NATIVE, c.bsc.tokens.ONE!.address, false);
    w.setPair('one', c.hyperevm.chainId, NATIVE, c.hyperevm.tokens.ONE!.address, true);
    w.setPair('one', c.bsc.chainId, c.one.tokens.USDTx!.address, c.bsc.tokens.USDT!.address, false);
    w.setPair('one', c.hyperevm.chainId, c.one.tokens.USDTx!.address, c.hyperevm.tokens.ONE!.address, false);
    // fees: hub -> dest 5 ONE min / 0.18 nf; leaves -> hub 5 ONE min
    w.setFee('one', c.bsc.chainId, NATIVE, { min: 5n * E18, nf: 18n * 10n ** 16n, rate: 0n });
    w.setFee('one', c.hyperevm.chainId, NATIVE, { min: 5n * E18, nf: 18n * 10n ** 16n, rate: 0n });
    w.setFee('one', c.bsc.chainId, c.one.tokens.USDTx!.address, { min: 5n * E18, nf: 18n * 10n ** 16n, rate: 0n });
    w.setFee('bsc', hubId, c.bsc.tokens.ONE!.address, { min: 1n * E18, nf: 0n, rate: 0n });
    w.setFee('hyperevm', hubId, c.hyperevm.tokens.ONE!.address, { min: 5n * E18, nf: 0n, rate: 0n });
    // executor whitelists
    const hub = c.one.bridge!.toLowerCase();
    w.execWhitelist.add(`one:${hub}`);
    w.execWhitelist.add(`hyperevm:${c.hyperevm.tokens.ONE!.address.toLowerCase()}`);
    w.methodChecks.add(`one:${hub}`);
    // funds
    w.setBal('bsc', c.bsc.tokens.ONE!.address, SENDER, 1000n * E18);
    w.setBal('hyperevm', c.hyperevm.tokens.ONE!.address, SENDER, 1000n * E18);
    w.setBal('one', TOKEN_A, SENDER, 1000n * E18);
    w.deployErc20('one', TOKEN_A);
    return w;
  }
}
