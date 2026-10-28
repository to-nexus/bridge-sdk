import { backoffOf, type Ctx } from './ctx.js';
import type { ChainKey } from './networks-types.js';
import { fromHexQty, toHexQty, withBackoff } from './rpc.js';
import type { RawLog } from './types.js';

export const hex32 = (v: bigint | number | string): string => {
  const h = typeof v === 'string' ? v.replace(/^0x/, '').toLowerCase() : BigInt(v).toString(16);
  return '0x' + h.padStart(64, '0');
};

export function normalizeLog(l: any): RawLog {
  return {
    address: l.address, topics: l.topics, data: l.data, transactionHash: l.transactionHash,
    logIndex: Number(l.logIndex), blockNumber: Number(l.blockNumber),
  };
}

export async function headBlock(ctx: Ctx, key: ChainKey): Promise<number> {
  return Number(fromHexQty(await withBackoff(() => ctx.rpc(key).send('eth_blockNumber', []), backoffOf(ctx))));
}

/**
 * Chunked, topic-filtered eth_getLogs (default 2 000 blocks per call), each chunk retried with bounded exponential
 * backoff (1 s x2, cap 30 s, 6 tries). Throws when a chunk keeps failing (the tracker turns that into UNKNOWN).
 */
export async function scanLogs(
  ctx: Ctx, key: ChainKey, filter: { address: string; topics: (string | string[] | null)[] },
  fromBlock: number, toBlock: number, chunk = ctx.opts.scanChunkBlocks ?? 2000,
): Promise<RawLog[]> {
  const out: RawLog[] = [];
  const seen = new Set<string>();
  for (let from = fromBlock; from <= toBlock; from += chunk) {
    const to = Math.min(from + chunk - 1, toBlock);
    const logs = await withBackoff(
      () => ctx.rpc(key).send('eth_getLogs', [{ address: filter.address, topics: filter.topics, fromBlock: toHexQty(from), toBlock: toHexQty(to) }]),
      backoffOf(ctx),
    );
    for (const l of logs as any[]) {
      const n = normalizeLog(l);
      const k = `${n.transactionHash}:${n.logIndex}`; // dedupe by (txHash, logIndex)
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(n);
    }
  }
  return out;
}
