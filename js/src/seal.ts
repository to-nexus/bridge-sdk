// Quote seal (format v1). A quote carries `seal = { v, sdk, payload, digest }`: `payload` is the canonical JSON of the WHOLE quote (without the seal) in
// this SDK's wire DTO (fixed units, decimal strings), `digest` = keccak256(utf8(payload)). send() verifies it BEFORE anything else and then works
// ONLY on a new private, deep-frozen object restored from the payload. The caller's object is never read again.
//
// The digest is an integrity check against accidental or concurrent mutation (a spread copy that was edited, a shared object changed while an approve
// is pending). It is NOT an authenticity proof: whoever holds the quote can always build a new seal. Edit a request by calling quote() again.
import { getAddress, keccak256, toUtf8Bytes } from 'ethers';
import { LimitViolationError } from './errors.js';
import { RuleError, checkRouteRules } from './route-rules.js';
import { deepFreeze } from './freeze.js';
import { Route } from './types.js';
import type { Quote } from './types.js';

export interface QuoteSeal {
  readonly v: 1;
  /** the SDK that produced the payload; a seal of the other SDK is refused ('unsupported-sdk') */
  readonly sdk: 'js';
  /** canonicalJSON(quote without seal) in the JS wire DTO (QuoteWireV1) */
  readonly payload: string;
  /** 0x-hex keccak256(utf8(payload)) */
  readonly digest: string;
}

export type SealReason = 'missing' | 'unsupported-version' | 'unsupported-sdk' | 'corrupt' | 'invalid-payload' | 'modified';

/** QuoteWireV1: every amount / fee / gas / nonce / chain id / timestamp is a decimal string, createdAtMs / ttlMs in ms, deadlineSec in Unix s, addresses and hex lowercase. */
export type QuoteWireV1 = { [k: string]: unknown };

class WireError extends Error {
  constructor(public path: string, msg: string) { super(`${path}: ${msg}`); }
}

// ---------------------------------------------------------------------------------------------------- schema
type Node =
  | { t: 'dec'; as: 'big' | 'num'; wire?: string }
  | { t: 'int'; wire?: string }
  | { t: 'addr'; native?: boolean }
  | { t: 'hex'; bytes?: number }
  | { t: 'str' }
  | { t: 'bool' }
  | { t: 'enum'; values: readonly string[] }
  | { t: 'obj'; fields: Record<string, Field> }
  | { t: 'union'; tag: string; variants: Record<string, Node & { t: 'obj' }> };
type Field = Node & { opt?: boolean; nullable?: boolean; wire?: string };

const o = (fields: Record<string, Field>): Node & { t: 'obj' } => ({ t: 'obj', fields });
const big = (opt?: boolean): Field => ({ t: 'dec', as: 'big', opt });
const num = (opt?: boolean, wire?: string): Field => ({ t: 'dec', as: 'num', opt, wire });
const addr = (opt?: boolean): Field => ({ t: 'addr', opt });
const CHAINS = ['one', 'bsc', 'hyperevm'] as const;
const HOPKIND = ['plain', 'hubForward', 'x1', 'x2'] as const;
const ROUTES = Object.values(Route) as string[];
const fee: Field = o({ minimumValue: big(), networkFee: big(), exFee: big() });
const recipients = (opt?: boolean): Field => ({ ...o({ onHub: addr(true), dest: addr(true), core: addr(true) }), opt });

const SCHEMA = o({
  id: { t: 'str' },
  network: { t: 'enum', values: [] }, // filled from networks.json (known networks only) in checkEnums
  route: { t: 'enum', values: ROUTES },
  request: o({
    route: { t: 'enum', values: ROUTES }, amount: big(), sender: addr(), recipient: addr(), recipients: recipients(true), hypercore: { t: 'bool', opt: true },
    swap: { ...o({ slippageBps: { t: 'int', opt: true }, deadlineSec: num(true) }), opt: true },
    x3: { ...o({ tokenIn: { t: 'addr', native: true }, tokenOut: addr(), poolFee: { t: 'int' }, toChain: { t: 'enum', values: ['bsc', 'hyperevm'] } }), opt: true },
    hop2FeeBuffer: big(true), limits: { ...o({ maxTotalSpend: big(true), minDelivered: big(true) }), opt: true }, quoteTtlMs: num(true),
  }),
  kind: { t: 'enum', values: ['single', 'multihop', 'x1', 'x3'] },
  sourceChainKey: { t: 'enum', values: CHAINS }, sourceChainId: num(), sourceContract: addr(), sourceBridge: addr(), sender: addr(),
  createdAt: num(false, 'createdAtMs'), ttlMs: num(),
  verifiers: o({ source: addr(true), hub: addr(true) }),
  fees: o({ hop1: fee, hop2: { ...fee, opt: true }, hop2ExFeeDeclared: big(true) }),
  value: big(), v2: big(true), totalSpend: big(),
  tx: o({ to: addr(), data: { t: 'hex' }, value: big() }),
  approval: { ...o({ token: addr(), spender: addr(), amount: big() }), nullable: true },
  limits: o({ maxTotalSpend: big(), minDelivered: big(), amountOutMinimum: big(true), minBridgeValue: big(true) }),
  deadline: num(true, 'deadlineSec'),
  match: {
    t: 'union', tag: 'kind', variants: {
      bridge: o({ kind: { t: 'str' }, toChainID: num(), fromToken: addr(), toToken: addr(), recipient: addr(), value: big(), networkFee: big(), exFee: big(), extraDataHash: { t: 'hex', bytes: 32 } }),
      swap: o({ kind: { t: 'str' }, tokenIn: addr(), tokenOut: addr(), amountIn: big(), toChainID: num(), toToken: addr(), recipient: addr(), extraDataHash: { t: 'hex', bytes: 32 } }),
    },
  },
  details: o({
    hop1ToChainId: num(), hop1ToToken: addr(), hubChainKey: { t: 'enum', values: CHAINS, opt: true }, destChainKey: { t: 'enum', values: CHAINS }, destChainId: num(),
    hop1Kind: { t: 'enum', values: HOPKIND }, hop2Kind: { t: 'enum', values: HOPKIND, opt: true }, extraData: { t: 'hex' }, recipients: recipients(),
    expectedSwapOut: big(true), swapAmountOut: big(true), coreAmount: big(true), coreDust: big(true),
  }),
});

const DEC = /^(0|[1-9][0-9]*)$/;
const ADDR = /^0x[0-9a-f]{40}$/;
const HEX = /^0x([0-9a-f]{2})*$/;
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
// the network name is a static NetworkName; whether it equals the CLIENT's network is checked by the client binding check (binding.ts). No configuration is read here.
const KNOWN_NETWORKS: readonly string[] = ['mainnet', 'testnet'];
const wireName = (k: string, f: Field | Node): string => (('wire' in f && f.wire) || k);

// ---------------------------------------------------------------------------------------------------- encode: public fields -> wire (throws WireError)
function encode(n: Node, v: unknown, path: string): unknown {
  switch (n.t) {
    case 'dec': {
      if (typeof v === 'bigint' && v >= 0n) return v.toString();
      if (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0) return String(v);
      throw new WireError(path, 'expected a non-negative integer (bigint or safe integer)');
    }
    case 'int': if (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0) return v; throw new WireError(path, 'expected a small non-negative integer');
    case 'addr':
      if (n.native && v === 'native') return 'native';
      if (typeof v === 'string' && /^0x[0-9a-fA-F]{40}$/.test(v)) return v.toLowerCase();
      throw new WireError(path, 'expected an address');
    case 'hex': if (typeof v === 'string' && /^0x([0-9a-fA-F]{2})*$/.test(v) && (!n.bytes || v.length === 2 + 2 * n.bytes)) return v.toLowerCase(); throw new WireError(path, 'expected hex bytes');
    case 'str': if (typeof v === 'string' && v.length > 0) return v; throw new WireError(path, 'expected a non-empty string');
    case 'bool': if (typeof v === 'boolean') return v; throw new WireError(path, 'expected a boolean');
    case 'enum': if (typeof v === 'string' && (n.values.length === 0 || n.values.includes(v))) return v; throw new WireError(path, 'unexpected value');
    case 'obj': return encodeObj(n.fields, v, path);
    case 'union': {
      if (!isObj(v) || typeof v[n.tag] !== 'string' || !(v[n.tag] as string in n.variants)) throw new WireError(`${path}.${n.tag}`, 'unknown variant');
      return encodeObj(n.variants[v[n.tag] as string].fields, v, path);
    }
  }
}
function encodeObj(fields: Record<string, Field>, v: unknown, path: string): Record<string, unknown> {
  if (!isObj(v)) throw new WireError(path, 'expected an object');
  const out: Record<string, unknown> = {};
  for (const [k, f] of Object.entries(fields)) {
    const x = v[k];
    const p = path ? `${path}.${k}` : k;
    if (x === undefined || (x === null && f.nullable)) {
      if (f.opt || f.nullable) continue;
      throw new WireError(p, 'missing');
    }
    out[wireName(k, f)] = encode(f, x, p);
  }
  return out;
}

// ---------------------------------------------------------------------------------------------------- validate: wire (parsed payload) strictness (throws WireError)
function validate(n: Node, v: unknown, path: string): void {
  switch (n.t) {
    case 'dec': if (typeof v === 'string' && DEC.test(v) && (n.as === 'big' || Number.isSafeInteger(Number(v)))) return; throw new WireError(path, 'expected a decimal string (non-negative integer)');
    case 'int': if (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0) return; throw new WireError(path, 'expected a small non-negative integer');
    case 'addr': if (typeof v === 'string' && (ADDR.test(v) || (n.native && v === 'native'))) return; throw new WireError(path, 'malformed address');
    case 'hex': if (typeof v === 'string' && HEX.test(v) && (!n.bytes || v.length === 2 + 2 * n.bytes)) return; throw new WireError(path, 'malformed hex');
    case 'str': if (typeof v === 'string' && v.length > 0) return; throw new WireError(path, 'expected a non-empty string');
    case 'bool': if (typeof v === 'boolean') return; throw new WireError(path, 'expected a boolean');
    case 'enum': if (typeof v === 'string' && (n.values.length === 0 || n.values.includes(v))) return; throw new WireError(path, 'unexpected value');
    case 'obj': validateObj(n.fields, v, path); return;
    case 'union': {
      if (!isObj(v) || typeof v[n.tag] !== 'string' || !(v[n.tag] as string in n.variants)) throw new WireError(`${path}.${n.tag}`, 'unknown variant');
      validateObj(n.variants[v[n.tag] as string].fields, v, path);
      return;
    }
  }
}
function validateObj(fields: Record<string, Field>, v: unknown, path: string): void {
  if (!isObj(v)) throw new WireError(path || '$', 'expected an object');
  const known = new Set<string>();
  for (const [k, f] of Object.entries(fields)) {
    const w = wireName(k, f);
    known.add(w);
    const p = path ? `${path}.${w}` : w;
    if (!(w in v) || v[w] === undefined) { if (f.opt || f.nullable) continue; throw new WireError(p, 'missing required field'); }
    validate(f, v[w], p);
  }
  for (const k of Object.keys(v)) if (!known.has(k)) throw new WireError(path ? `${path}.${k}` : k, 'unknown field');
}

// ---------------------------------------------------------------------------------------------------- decode: validated wire -> public
function decode(n: Node, v: any): unknown {
  switch (n.t) {
    case 'dec': return n.as === 'big' ? BigInt(v) : Number(v);
    case 'addr': return v === 'native' ? v : getAddress(v);
    case 'obj': return decodeObj(n.fields, v);
    case 'union': return decodeObj(n.variants[v[n.tag]].fields, v);
    default: return v;
  }
}
function decodeObj(fields: Record<string, Field>, v: any): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, f] of Object.entries(fields)) {
    const w = wireName(k, f);
    if (v[w] === undefined) { if (f.nullable) out[k] = null; continue; }
    out[k] = decode(f, v[w]);
  }
  return out;
}

// ---------------------------------------------------------------------------------------------------- canonical JSON
/** Keys sorted by UTF-16 code unit, no whitespace, no undefined / null. Strings JSON-escaped; numbers only small integers; booleans. */
export function canonicalJSON(v: unknown): string {
  if (v === null || v === undefined) throw new Error('canonicalJSON: null / undefined is never emitted');
  if (typeof v === 'string') return JSON.stringify(v);
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (typeof v === 'number') { if (!Number.isSafeInteger(v)) throw new Error('canonicalJSON: only safe integers'); return String(v); }
  if (Array.isArray(v)) return `[${v.map(canonicalJSON).join(',')}]`;
  if (typeof v === 'object') {
    const keys = Object.keys(v as object).filter((k) => (v as any)[k] !== undefined).sort();
    return `{${keys.map((k) => `${JSON.stringify(k)}:${canonicalJSON((v as any)[k])}`).join(',')}}`;
  }
  throw new Error(`canonicalJSON: unsupported ${typeof v}`);
}

const digestOf = (payload: string): string => keccak256(toUtf8Bytes(payload));
const fail = (quote: unknown, reason: SealReason, path?: string, detail?: string): never => {
  throw new LimitViolationError([`seal:${reason}${path ? `@${path}` : ''}`], quote as Quote, { field: 'seal', reason, path, detail });
};

function firstDiff(a: unknown, b: unknown, path: string): string | null {
  if (isObj(a) && isObj(b)) {
    for (const k of [...new Set([...Object.keys(a), ...Object.keys(b)])].sort()) {
      const d = firstDiff(a[k], b[k], path ? `${path}.${k}` : k);
      if (d) return d;
    }
    return null;
  }
  return JSON.stringify(a) === JSON.stringify(b) ? null : path;
}

function checkNetworks(w: Record<string, unknown>): void {
  if (typeof w.network !== 'string' || !KNOWN_NETWORKS.includes(w.network)) throw new WireError('network', 'unknown network');
}

/** The payload for a quote's public fields (throws WireError when a field cannot be encoded). */
function payloadOf(q: unknown): string {
  const w = encodeObj(SCHEMA.fields, q, '') as Record<string, unknown>;
  checkNetworks(w);
  return canonicalJSON(w);
}

/** Seal a quote built by this SDK. */
export function sealQuote(q: Omit<Quote, 'seal'>): Quote {
  const payload = payloadOf(q);
  return { ...q, seal: { v: 1, sdk: 'js', payload, digest: digestOf(payload) } } as Quote;
}

/** The exact payload / digest of a quote (vectors). */
export function sealPayloadOf(q: Omit<Quote, 'seal'>): { payload: string; digest: string } {
  const payload = payloadOf(q);
  return { payload, digest: digestOf(payload) };
}

/**
 * THE one verification used by send() and by every public API that consumes a Quote (buildTx, ensureAllowance, preflight, simulate): synchronous; nothing is awaited, nothing is read from the network):
 *  1 shape  2 version / sdk  3 digest  4 parse + schema  5 public fields vs payload  6 restore into a NEW private deep-frozen object.
 */
export function openQuote(q: unknown): Quote {
  const seal = isObj(q) ? (q as any).seal : undefined;
  if (!isObj(seal) || typeof seal.payload !== 'string' || typeof seal.digest !== 'string') return fail(q, 'missing');
  if (seal.v !== 1) return fail(q, 'unsupported-version');
  if (seal.sdk !== 'js') return fail(q, 'unsupported-sdk');
  if (digestOf(seal.payload).toLowerCase() !== seal.digest.toLowerCase()) return fail(q, 'corrupt');
  let parsed: Record<string, unknown>;
  let restored: Quote;
  try {
    parsed = JSON.parse(seal.payload);
    validateObj(SCHEMA.fields, parsed, '');
    checkNetworks(parsed);
    restored = decodeObj(SCHEMA.fields, parsed) as unknown as Quote;
    checkRouteRules(restored); // second pass: the route-specific required fields
  } catch (e) {
    return fail(q, 'invalid-payload', e instanceof WireError || e instanceof RuleError ? e.path : '$', (e as Error).message);
  }
  let pub: string;
  let pubObj: Record<string, unknown> | undefined;
  try {
    pubObj = encodeObj(SCHEMA.fields, q, '');
    pub = canonicalJSON(pubObj);
  } catch (e) {
    return fail(q, 'modified', e instanceof WireError ? e.path : '$', (e as Error).message);
  }
  if (pub !== seal.payload) return fail(q, 'modified', firstDiff(parsed, pubObj, '') ?? '$');
  return deepFreeze({ ...restored, seal: { v: 1, sdk: 'js', payload: seal.payload, digest: seal.digest } } as Quote);
}

