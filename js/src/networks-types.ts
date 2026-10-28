export type NetworkName = 'mainnet' | 'testnet';
export type ChainKey = 'cross' | 'bsc' | 'hyperevm';

export interface TokenInfo {
  address: string;
  symbol: string | null;
  decimals: number;
  kind: 'native' | 'erc20';
  status?: string;
}

export interface SwapConfig {
  v3SwapRouter: string | null;
  quoterV2: string | null;
  v3Factory: string | null;
  swapBridgeRouter: string | null;
  x1Pool: { tokenIn: string; tokenOut: string; fee: number; pool: string } | null;
}

export interface ChainConfig {
  chainId: number;
  name: string;
  nativeSymbol: string | null;
  rpc: string;
  explorer: string | null;
  bridge: string | null;
  bridgeVerifier: string | null;
  bridgeExecutor: string | null;
  tokens: Record<string, TokenInfo | null>;
  swap: SwapConfig | null;
  coreSystemAddress?: string | null;
  coreInfoApi?: string | null;
  todo: Record<string, string>;
}

export interface NetworkData {
  label: string;
  hubChain: ChainKey;
  chains: Record<ChainKey, ChainConfig>;
}

export interface NetworksData {
  schemaVersion: number;
  verifiedAt: string;
  notes: string[];
  networks: Record<NetworkName, NetworkData>;
  provenance: Record<string, string>;
}
