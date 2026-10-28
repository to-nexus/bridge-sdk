/** Immutable snapshots: structuredClone (bigint-safe) + deep freeze. Callers can never mutate what the SDK will sign. */
export function deepFreeze<T>(o: T): T {
  if (o && typeof o === 'object' && !Object.isFrozen(o)) {
    Object.freeze(o);
    for (const v of Object.values(o as Record<string, unknown>)) deepFreeze(v);
  }
  return o;
}

export function snapshot<T>(o: T): T {
  return deepFreeze(structuredClone(o));
}
