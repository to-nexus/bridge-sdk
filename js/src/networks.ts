import { networksData } from './networks.generated.js';
import type { ChainConfig, ChainKey, NetworkData, NetworkName, NetworksData } from './networks-types.js';
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

/** Resolve a dotted config path such as `hyperevm.bridge` or `cross.swap.swapBridgeRouter` or `bsc.tokens.USDT`. */
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
