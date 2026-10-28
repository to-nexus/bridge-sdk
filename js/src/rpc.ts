import { Interface } from 'ethers';
import { RpcTimeoutError, extractRevertData } from './errors.js';

/** The only thing the SDK needs from a provider: raw JSON-RPC. ethers' JsonRpcProvider satisfies it. */
export interface RpcLike {
  send(method: string, params: unknown[]): Promise<any>;
}

export const toHexQty = (n: bigint | number): string => '0x' + BigInt(n).toString(16);
export const fromHexQty = (h: string): bigint => BigInt(h);

export async function ethCall(p: RpcLike, tx: { to: string; data: string; from?: string; value?: bigint }, block: string = 'latest'): Promise<string> {
  const req: Record<string, string> = { to: tx.to, data: tx.data };
  if (tx.from) req.from = tx.from;
  if (tx.value) req.value = toHexQty(tx.value);
  return (await p.send('eth_call', [req, block])) as string;
}

/** Typed read helper: encode with `iface`, eth_call, decode. */
export async function readContract<T = any>(p: RpcLike, iface: Interface, to: string, fn: string, args: unknown[] = [], opts: { from?: string } = {}): Promise<T> {
  const data = iface.encodeFunctionData(fn, args);
  let out: string;
  try {
    out = await ethCall(p, { to, data, from: opts.from });
  } catch (e) {
    if (e instanceof RpcTimeoutError) throw e; // a timeout is not a revert: keep its identity for callers (quote/preflight/send)
    const rd = extractRevertData(e);
    const err = new Error(`${fn} reverted${rd ? ` (${rd.slice(0, 10)})` : ''}: ${(e as Error).message}`);
    (err as any).data = rd;
    (err as any).cause = e;
    throw err;
  }
  const r = iface.decodeFunctionResult(fn, out);
  return (r.length === 1 ? r[0] : r) as T;
}

/** Race `p` against a timer; the (possibly still pending) provider call is abandoned, never retried here. */
export function withTimeout<T>(p: Promise<T>, ms: number, method: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new RpcTimeoutError(method, ms)), Math.max(0, ms));
    p.then((v) => { clearTimeout(t); resolve(v); }, (e) => { clearTimeout(t); reject(e); });
  });
}

/** Every provider call gets min(callTimeoutMs, remaining overall deadline). `remaining()` returns ms left (Infinity = no overall deadline). */
export function timedRpc(inner: RpcLike, callTimeoutMs: number, remaining: () => number): RpcLike {
  return {
    send(method: string, params: unknown[]): Promise<any> {
      const budget = Math.min(callTimeoutMs, remaining());
      if (budget <= 0) return Promise.reject(new RpcTimeoutError(method, 0));
      return withTimeout(Promise.resolve().then(() => inner.send(method, params)), budget, method);
    },
  };
}

export interface BackoffOptions {
  /** stop retrying once this returns true (overall deadline reached) */
  expired?: () => boolean;
  /** upper bound for one backoff sleep (ms left until the overall deadline) */
  maxSleep?: () => number;
  tries?: number;
  baseMs?: number;
  capMs?: number;
  sleep: (ms: number) => Promise<void>;
}

/** bounded exponential backoff (base 1 s, x2, cap 30 s, 6 tries by default) */
export async function withBackoff<T>(fn: () => Promise<T>, o: BackoffOptions): Promise<T> {
  const tries = o.tries ?? 6;
  let delay = o.baseMs ?? 1000;
  const cap = o.capMs ?? 30_000;
  let last: unknown;
  for (let i = 0; i < tries; i++) {
    try {
      return await fn();
    } catch (e) {
      last = e;
      if (i === tries - 1 || o.expired?.()) break;
      await o.sleep(Math.max(0, Math.min(delay, o.maxSleep ? o.maxSleep() : delay)));
      delay = Math.min(delay * 2, cap);
    }
  }
  throw last;
}

export const defaultSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
