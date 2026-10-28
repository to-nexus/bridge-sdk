import type { ChainKey, NetworkData, NetworkName } from './networks-types.js';
import type { RpcLike } from './rpc.js';

export interface ClientOptions {
  network: NetworkName;
  /** override the built-in public RPC per chain */
  rpcUrls?: Partial<Record<ChainKey, string>>;
  /** inject providers (anything with `send(method, params)`, e.g. an ethers JsonRpcProvider) - mainly for tests */
  providers?: Partial<Record<ChainKey, RpcLike>>;
  quoteTtlMs?: number; // default 30 000
  /** per-RPC-call timeout (default 15 000 ms): eth_call, getLogs, receipts, blocks, chain id, nonce, send. Wallet prompts (Mode W) are NOT timed out. */
  callTimeoutMs?: number;
  /** log scan chunk size in blocks (default 2000) */
  scanChunkBlocks?: number;
  /** when a handle has no recorded scan start for a chain: how many blocks back from head to start (default 20000) */
  scanLookbackBlocks?: number;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  /** override the built-in networks.json data (tests) */
  networksData?: import('./networks-types.js').NetworksData;
}

export interface Ctx {
  network: NetworkName;
  net: NetworkData;
  opts: ClientOptions;
  rpc(key: ChainKey): RpcLike;
  /** eth_chainId check, cached per chain; throws NetworkMismatchError */
  assertChain(key: ChainKey): Promise<void>;
  now(): number;
  sleep(ms: number): Promise<void>;
  /** overall deadline (ctx.now() ms) applied to every RPC call, when set (track()) */
  deadlineAt?: number;
  /** a ctx that shares everything but bounds every RPC call by `deadlineAt` */
  withDeadline(deadlineAt: number): Ctx;
}

/** backoff options bound to the ctx: sleep via ctx.sleep, retries stop (and sleeps are capped) at the overall deadline */
export function backoffOf(ctx: Ctx): { sleep: (ms: number) => Promise<void>; expired: () => boolean; maxSleep: () => number } {
  return {
    sleep: (ms) => ctx.sleep(ms),
    expired: () => ctx.deadlineAt !== undefined && ctx.now() >= ctx.deadlineAt,
    maxSleep: () => (ctx.deadlineAt === undefined ? Infinity : ctx.deadlineAt - ctx.now()),
  };
}
