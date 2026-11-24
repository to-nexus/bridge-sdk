import { networksData } from './networks.generated.js';
import type { ChainConfig, ChainKey, DexKind, NetworkData, NetworkName, NetworksData } from './networks-types.js';
import { AddressNotConfiguredError } from './errors.js';

export const NATIVE_TOKEN = '0x0000000000000000000000000000000000000001';

export function getNetworksData(): NetworksData {
  return networksData;
}

export function getNetwork(name: NetworkName, override?: NetworksData): NetworkData {
  const n = (override ?? networksData).networks[name];
  if (!n) throw new Error(`unknown network "${name}" (expected "mainnet" or "testnet")`);
  return n;
}

/** The ONE chain DEX of a network ('v3' when unset). An unknown value throws RangeError. */
export function dexOf(net: NetworkData): DexKind {
  const d = net.chains.one?.swap?.dex ?? 'v3';
  if (d !== 'v3' && d !== 'cl') throw new RangeError(`one.swap.dex must be "v3" or "cl", got "${String(d)}"`);
  return d;
}

const CL_PATHS: Record<string, string> = {
  'one.swap.v3SwapRouter': 'one.swap.cl.swapRouter',
  'one.swap.quoterV2': 'one.swap.cl.quoterV2',
  'one.swap.v3Factory': 'one.swap.cl.factory',
  'one.swap.swapBridgeRouter': 'one.swap.cl.swapBridgeRouter',
  'one.swap.x1Pool': 'one.swap.cl.x1Pool',
};

/** Map a V3 swap config path to the path of the same role under the DEX (identity for V3 and for every other path). */
export function dexPath(dex: DexKind, path: string): string {
  return dex === 'cl' ? (CL_PATHS[path] ?? path) : path;
}

/** The ONE chain swap configuration of the ACTIVE DEX (the V3 keys, or the `cl` block). null = not configured. */
export interface SwapView {
  dex: DexKind;
  /** X1 extraCall target: V3 SwapRouter | CL SwapRouter */
  router: string | null;
  quoter: string | null;
  factory: string | null;
  /** X3 wrapper: SwapBridgeRouter | SwapBridgeRouterCL */
  sbr: string | null;
  /** X1 pool address */
  pool: string | null;
  /** V3 X1 pool fee */
  fee: number;
  /** CL X1 pool tick spacing */
  tickSpacing: number;
}

/** Resolve the active DEX of a network configuration (throws RangeError on an unknown dex value). */
export function swapView(net: NetworkData): SwapView {
  const dex = dexOf(net);
  const sw = net.chains.one?.swap;
  if (dex === 'cl') {
    const cl = sw?.cl ?? null;
    return { dex, router: cl?.swapRouter ?? null, quoter: cl?.quoterV2 ?? null, factory: cl?.factory ?? null, sbr: cl?.swapBridgeRouter ?? null, pool: cl?.x1Pool?.pool ?? null, fee: 0, tickSpacing: cl?.x1Pool?.tickSpacing ?? 0 };
  }
  return { dex, router: sw?.v3SwapRouter ?? null, quoter: sw?.quoterV2 ?? null, factory: sw?.v3Factory ?? null, sbr: sw?.swapBridgeRouter ?? null, pool: sw?.x1Pool?.pool ?? null, fee: sw?.x1Pool?.fee ?? 0, tickSpacing: 0 };
}

/** Resolve a dotted config path such as `hyperevm.bridge`, `one.swap.swapBridgeRouter`, `one.swap.cl.swapRouter` or `bsc.tokens.USDT`. */
export function configValue(net: NetworkData, path: string): string | null {
  const [chainKey, ...rest] = path.split('.');
  let cur: unknown = net.chains[chainKey as ChainKey];
  for (const p of rest) {
    if (cur == null || typeof cur !== 'object') return null;
    cur = (cur as Record<string, unknown>)[p];
  }
  if (cur == null) return null;
  if (typeof cur === 'string') return cur;
  if (typeof cur === 'object' && 'address' in (cur as object)) return (cur as { address: string }).address;
  if (typeof cur === 'object' && 'pool' in (cur as object)) return (cur as { pool: string }).pool;
  return null;
}

/** Throw AddressNotConfiguredError (before any approve / send) when a required config value is null. */
export function requireConfigured(route: string, net: NetworkData, paths: string[]): void {
  for (const path of paths) {
    if (configValue(net, path) == null) {
      const [chainKey, ...rest] = path.split('.');
      const todo = net.chains[chainKey as ChainKey]?.todo?.[rest.join('.')] ?? 'TODO(UNCONFIRMED)';
      throw new AddressNotConfiguredError(route, path, todo);
    }
  }
}

export function chainOf(net: NetworkData, key: ChainKey): ChainConfig {
  return net.chains[key];
}

export function chainKeyById(net: NetworkData, chainId: number): ChainKey | undefined {
  return (Object.keys(net.chains) as ChainKey[]).find((k) => net.chains[k].chainId === chainId);
}

export function must(v: string | null | undefined, what: string): string {
  if (v == null) throw new Error(`internal: ${what} is not configured`);
  return v;
}
