import { AbiCoder, Interface } from 'ethers';
import { errorTable } from './errors.generated.js';
import type { Checkpoint, PreflightIssue, Quote, SubmittedIntent } from './types.js';

export class BridgeSdkError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options as ErrorOptions);
    this.name = new.target.name;
  }
}

export class NetworkMismatchError extends BridgeSdkError {
  /** `scope` names what mismatched (e.g. "mainnet RPC", "signer provider", "quote.sourceBridge"). Also used for the Quote -> Client binding. */
  constructor(public scope: string, public expected: number | string, public actual: number | string) {
    super(`network mismatch (${scope}): expected chainId ${expected}, got ${actual}. Nothing was sent.`);
  }
}

export class AddressNotConfiguredError extends BridgeSdkError {
  constructor(public route: string, public field: string, public todo: string) {
    super(`route ${route} is not available on this network: ${field} is not configured (${todo})`);
  }
}

export class QuoteExpiredError extends BridgeSdkError {
  constructor(public ageMs: number, public ttlMs: number) {
    super(`quote expired (${ageMs} ms old, ttl ${ttlMs} ms). Request a new quote.`);
  }
}

export class LimitViolationError extends BridgeSdkError {
  /** quote seal failures: `field === 'seal'`, `reason` = missing | unsupported-version | unsupported-sdk | corrupt | invalid-payload | modified, `path` = the offending JSON path (invalid-payload / modified) */
  public field?: string;
  public reason?: string;
  public path?: string;
  constructor(public violated: string[], public quote: Quote, info?: { field?: string; reason?: string; path?: string; detail?: string }) {
    super(info?.field === 'seal'
      ? `quote seal rejected (${info.reason}${info.path ? ` at ${info.path}` : ''}${info.detail ? `: ${info.detail}` : ''}). Quotes are sealed: edit the request by calling quote() again, not by editing the object. Nothing was sent.`
      : `quote violates the requested limits: ${violated.join(', ')}`);
    if (info) { this.field = info.field; this.reason = info.reason; this.path = info.path; }
  }
}

export class RequoteRequiredError extends BridgeSdkError {
  constructor(public oldQuote: Quote, public newQuote: Quote | null, public violated: string[]) {
    super(`re-quote would loosen an approved limit (${violated.join(', ')}). Nothing was sent; review newQuote and call quote() again.`);
  }
}

export class PreflightError extends BridgeSdkError {
  /** always set when thrown by send(): 'pre-approve' (before any approve), 'requote' (an error while building the adopted quote after the approve) or 'post-approve' (after approve + re-quote, before simulation) */
  constructor(public issues: PreflightIssue[], public stage?: 'pre-approve' | 'post-approve' | 'requote') {
    super(`preflight failed${stage ? ` (${stage})` : ''}: ${issues.map((i) => i.code).join(', ')}`);
  }
}

/** The local signer returned a transaction that is not what the SDK asked for (unprotected / wrong chain id / mutated fields). Nothing was broadcast. */
export class SignerMisbehaviorError extends BridgeSdkError {
  constructor(public field: string, detail: string) {
    super(`signer misbehavior (${field}): ${detail}. Nothing was broadcast.`);
  }
}

/** A single RPC call exceeded its budget (callTimeoutMs, or the remaining overall deadline). Raised AFTER a broadcast it is always wrapped in BroadcastOutcomeUnknownError. */
export class RpcTimeoutError extends BridgeSdkError {
  constructor(public method: string, public timeoutMs: number) {
    super(`RPC ${method} timed out after ${timeoutMs} ms`);
  }
}

export class SimulationRevertError extends BridgeSdkError {
  constructor(public decoded: DecodedError | null, public data: string | null, cause?: unknown) {
    super(`simulation reverted: ${decoded ? formatDecoded(decoded) : data ?? 'unknown reason'}. Nothing was sent.`, { cause });
  }
}

export class ApproveFailedError extends BridgeSdkError {
  constructor(message: string, public hash?: string, cause?: unknown) {
    super(`approve failed: ${message}`, { cause });
  }
}

export class SignerRejectedError extends BridgeSdkError {
  /** true: the wallet refused before signing - nothing was signed, a fresh attempt is safe. */
  readonly safeToRetry = true;
  constructor(cause?: unknown) {
    super('the wallet rejected the request before signing; nothing was signed or sent.', { cause });
  }
}

export class SigningError extends BridgeSdkError {
  constructor(cause?: unknown) {
    super(`signing failed; nothing was broadcast: ${String((cause as Error)?.message ?? cause)}`, { cause });
  }
}

export class SourceTxFailedError extends BridgeSdkError {
  constructor(public hash: string) {
    super(`source transaction ${hash} failed (status 0). No bridge transfer was created.`);
  }
}

export class WaitTimeoutError extends BridgeSdkError {
  constructor(public hash: string) {
    super(`timed out waiting for the receipt of ${hash}; the tx may still be mined. Keep tracking with the hash.`);
  }
}

export class BroadcastOutcomeUnknownError extends BridgeSdkError {
  constructor(
    public mode: 'L' | 'W',
    public intent: SubmittedIntent,
    public checkpoint: Checkpoint,
    public info: { hash?: string; nonce?: number; raw?: string },
    cause?: unknown,
  ) {
    super(
      `broadcast outcome unknown (mode ${mode}): the transaction may or may not be on chain. DO NOT resend. ` +
        `Use client.findSourceTransfer(error.intent, error.checkpoint)${mode === 'L' ? ' or client.rebroadcast(error.info.raw)' : ''}. ` +
        `Cause: ${String((cause as Error)?.message ?? cause)}`,
      { cause },
    );
  }
  get hash() { return this.info.hash; }
  get nonce() { return this.info.nonce; }
  get raw() { return this.info.raw; }
}

export interface SourceCandidateInfo { toChainID: bigint; index: bigint; logIndex: number }

/** Several candidate transfers in one receipt: never auto-selected. Pick one with `opts.index` / `opts.logIndex`. */
export class AmbiguousSourceError extends BridgeSdkError {
  constructor(message: string, public candidates: SourceCandidateInfo[] = []) {
    super(`${message}${candidates.length ? ` Candidates (toChainID, index, logIndex): ${candidates.map((c) => `(${c.toChainID}, ${c.index}, ${c.logIndex})`).join(' ')}` : ''}`);
  }
}

/** No (consistent) BridgeInitiated from the configured source bridge / SwapBridgeRouter in the receipt, or the selector matched none. */
export class SourceTransferNotFoundError extends BridgeSdkError {}

/** The source BridgeInitiated carries an extraData target/selector (or a nested hop-2 destination) that maps to no supported route. */
export class UnsupportedRouteError extends BridgeSdkError {
  constructor(message: string, public target?: string, public selector?: string) {
    super(message);
  }
}

/** The caller-supplied route contradicts the route decoded from the source receipt. */
export class RouteMismatchError extends BridgeSdkError {
  constructor(public given: string, public inferred: string) {
    super(`route ${given} contradicts the transfer in the source receipt, which decodes as ${inferred}`);
  }
}

/** Invalid swap option (slippageBps outside [0, 10000), deadlineSec not a positive integer). */
export class InvalidOptionError extends BridgeSdkError {}

// ------------------------------------------------------------------------------------------------ decoding
export interface DecodedError {
  selector: string;
  name: string;
  signature: string;
  args: Record<string, unknown>;
  hint: string | null;
}

const errorIface = new Interface(
  Object.values(errorTable)
    .map((e) => `error ${e.name}(${e.inputs.map((i, n) => `${i.type} ${i.name || 'arg' + n}`).join(', ')})`)
    .filter((v, i, a) => a.indexOf(v) === i),
);

/** Decode revert data into `{name, args, hint}`. Unknown selectors return `null`. */
export function decodeBridgeError(data: string | null | undefined): DecodedError | null {
  if (!data || data.length < 10) return null;
  const selector = data.slice(0, 10).toLowerCase();
  if (selector === '0x08c379a0') {
    try {
      const [msg] = AbiCoder.defaultAbiCoder().decode(['string'], '0x' + data.slice(10));
      return { selector, name: 'Error', signature: 'Error(string)', args: { message: msg }, hint: String(msg) };
    } catch { return null; }
  }
  if (selector === '0x4e487b71') {
    return { selector, name: 'Panic', signature: 'Panic(uint256)', args: {}, hint: 'solidity panic' };
  }
  const entry = errorTable[selector];
  if (!entry) return null;
  const args: Record<string, unknown> = {};
  try {
    const parsed = errorIface.parseError(data);
    parsed?.fragment.inputs.forEach((inp, i) => { args[inp.name || String(i)] = parsed.args[i]; });
  } catch { /* arguments undecodable: keep name only */ }
  return { selector, name: entry.name, signature: entry.signature, args, hint: entry.hint };
}

export function formatDecoded(d: DecodedError): string {
  const a = Object.entries(d.args).map(([k, v]) => `${k}=${String(v)}`).join(', ');
  return `${d.name}(${a})${d.hint ? ` - ${d.hint}` : ''}`;
}

/** Dig revert data out of the many shapes ethers / JSON-RPC nodes use. */
export function extractRevertData(err: unknown): string | null {
  const seen = new Set<unknown>();
  const walk = (e: unknown): string | null => {
    if (!e || typeof e !== 'object' || seen.has(e)) return null;
    seen.add(e);
    const o = e as Record<string, unknown>;
    for (const k of ['data', 'revertData']) {
      const v = o[k];
      if (typeof v === 'string' && /^0x[0-9a-fA-F]{8,}$/.test(v)) return v;
      if (v && typeof v === 'object') { const r = walk(v); if (r) return r; }
    }
    for (const k of ['error', 'info', 'cause', 'payload']) {
      const r = walk(o[k]);
      if (r) return r;
    }
    return null;
  };
  return walk(err);
}

export class SenderMismatchError extends BridgeSdkError {
  constructor(public expected: string, public actual: string) {
    super(`signer address ${actual} does not match the quote sender ${expected}. Nothing was signed.`);
  }
}
