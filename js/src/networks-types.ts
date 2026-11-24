export type NetworkName = 'mainnet' | 'testnet';
export type ChainKey = 'one' | 'bsc' | 'hyperevm';

export interface TokenInfo {
  address: string;
  symbol: string | null;
  decimals: number;
  kind: 'native' | 'erc20';
  status?: string;
}

/** The ONE chain DEX used by X1 and X3: Uniswap V3 (`'v3'`, the default) or Slipstream concentrated liquidity (`'cl'`). */
export type DexKind = 'v3' | 'cl';

/** Slipstream CL contracts. Single-pool calls carry `int24 tickSpacing` where Uniswap V3 carries `uint24 fee`. */
export interface ClSwapConfig {
  swapRouter: string | null;
  quoterV2: string | null;
  factory: string | null;
  /** SwapBridgeRouterCL (X3 with dex 'cl') */
  swapBridgeRouter: string | null;
  /** X1 pool; tickSpacing is read from the pool (`tickSpacing()`), never derived from its (dynamic) fee */
  x1Pool: { tokenIn: string; tokenOut: string; tickSpacing: number; pool: string } | null;
}

export interface SwapConfig {
  v3SwapRouter: string | null;
  quoterV2: string | null;
  v3Factory: string | null;
  swapBridgeRouter: string | null;
  x1Pool: { tokenIn: string; tokenOut: string; fee: number; pool: string } | null;
  /** absent = 'v3' (the V3 keys above). 'cl' switches X1 and X3 to the contracts in `cl`. */
  dex?: DexKind;
  cl?: ClSwapConfig | null;
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
