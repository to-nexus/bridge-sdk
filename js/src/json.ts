/** JSON helpers that round-trip bigint (Quote / SubmittedIntent / Checkpoint are not plain JSON). TransferHandle is already plain JSON. */
export function jsonStringify(v: unknown, space?: number): string {
  return JSON.stringify(v, (_k, x) => (typeof x === 'bigint' ? { $bigint: x.toString() } : x), space);
}
export function jsonParse<T = unknown>(s: string): T {
  return JSON.parse(s, (_k, x) => (x && typeof x === 'object' && typeof (x as any).$bigint === 'string' ? BigInt((x as any).$bigint) : x)) as T;
}
