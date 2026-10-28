import type { ChainKey, NetworkName } from './networks-types.js';
import type { QuoteSeal } from './seal.js';

/** 9 routes: R1-R6 base paths, X1 USDT->ONEUSD, X2 HyperCore deposit (R3 + core), X3 swap+bridge. */
export enum Route {
  BSC_TO_ONE = 'R1',
  ONE_TO_BSC = 'R2',
  ONE_TO_HYPEREVM = 'R3',
  HYPEREVM_TO_ONE = 'R4',
  BSC_TO_ONE_TO_HYPEREVM = 'R5',
  HYPEREVM_TO_ONE_TO_BSC = 'R6',
  BSC_USDT_TO_ONEUSD = 'X1',
  /** X2 on R3: ONE -> HyperEVM with a HyperCore deposit. (For R5 + HyperCore set `hypercore: true`.) */
  ONE_TO_HYPERCORE = 'X2',
  SWAP_BRIDGE = 'X3',
}

export interface QuoteRequest {
  route: Route;
  /** R1-R4/X2: delivered value. R5/R6: minimum delivered v2. X1: USDT value. X3: amountIn. (wei, bigint) */
  amount: bigint;
  sender: string;
  /** default recipient for every hop (override per hop with `recipients`) */
  recipient: string;
  recipients?: { onHub?: string; dest?: string; core?: string };
  /** R5 only: add the HyperCore deposit (X2) as the innermost extraData. */
  hypercore?: boolean;
  /** X1 / X3 slippage + deadline (defaults 50 bps / 1800 s) */
  swap?: { slippageBps?: number; deadlineSec?: number };
  /** X3 only */
  x3?: { tokenIn: string | 'native'; tokenOut: string; poolFee: number; toChain: 'bsc' | 'hyperevm' };
  /** multihop: wei added to the declared hop-2 exFee (see GUIDE 수수료). default 0 */
  hop2FeeBuffer?: bigint;
  /** explicit, tighter user limits (otherwise derived from the first quote) */
  limits?: { maxTotalSpend?: bigint; minDelivered?: bigint };
  quoteTtlMs?: number;
}

export interface FeeTriple {
  minimumValue: bigint;
  networkFee: bigint;
  exFee: bigint;
}

export interface Limits {
  /** source-token total incl. fees (X3: amountIn) */
  maxTotalSpend: bigint;
  /** single hop: value; multihop: v2; X1: USDTx value; X3: minBridgeValue */
  minDelivered: bigint;
  /** X1/X3: absolute swap output floor (never recomputed downward) */
  amountOutMinimum?: bigint;
  /** X3: bridge-delivery floor shown to the user */
  minBridgeValue?: bigint;
}

export interface BridgeMatch {
  kind: 'bridge';
  toChainID: number;
  fromToken: string;
  toToken: string;
  recipient: string;
  value: bigint;
  networkFee: bigint;
  exFee: bigint;
  extraDataHash: string;
}
export interface SwapMatch {
  kind: 'swap';
  tokenIn: string;
  tokenOut: string;
  amountIn: bigint;
  toChainID: number;
  /** the EXPECTED destination token: the hub `getTokenPair(toChainID, tokenOut).remoteToken` read at quote time (never the hub tokenOut, never the event's own value) */
  toToken: string;
  recipient: string;
  extraDataHash: string;
}
export type MatchSpec = BridgeMatch | SwapMatch;

export type HopKind = 'plain' | 'hubForward' | 'x1' | 'x2';

/**
 * A quote is SEALED: `seal` (public, enumerable, frozen) carries the canonical payload of the whole quote and its keccak256 digest. `send()` verifies the seal
 * first and then works only on a private copy restored from it; a quote whose public fields differ from the seal is refused (`LimitViolationError` field 'seal').
 * Unchanged copies (spread, structuredClone, jsonParse(jsonStringify(q))) are accepted. To change anything call `quote()` again. The digest guards against
 * accidental or concurrent mutation; it is not an authenticity proof (anyone can build a new seal).
 */
export interface Quote {
  seal: QuoteSeal;
  id: string;
  network: NetworkName;
  route: Route;
  request: QuoteRequest;
  kind: 'single' | 'multihop' | 'x1' | 'x3';
  sourceChainKey: ChainKey;
  sourceChainId: number;
  /** contract the user's tx goes to: source bridge, or SwapBridgeRouter for X3 */
  sourceContract: string;
  /** source bridge (for X3: the hub bridge that the router calls) */
  sourceBridge: string;
  sender: string;
  createdAt: number;
  ttlMs: number;
  verifiers: { source?: string; hub?: string };
  fees: { hop1: FeeTriple; hop2?: FeeTriple; hop2ExFeeDeclared?: bigint };
  /** hop-1 delivered value (multihop: v1 = v2 + nf2 + ef2'; X3: expected bridgeValue) */
  value: bigint;
  /** multihop only: minimum delivered on the final destination */
  v2?: bigint;
  /** total spent in the source token incl. fees (X3: amountIn) */
  totalSpend: bigint;
  tx: { to: string; data: string; value: bigint };
  approval: { token: string; spender: string; amount: bigint } | null;
  limits: Limits;
  /** unix seconds (X1 / X3) */
  deadline?: number;
  match: MatchSpec;
  details: {
    hop1ToChainId: number;
    hop1ToToken: string;
    hubChainKey?: ChainKey;
    destChainKey: ChainKey;
    destChainId: number;
    hop1Kind: HopKind;
    hop2Kind?: HopKind;
    extraData: string;
    recipients: { onHub?: string; dest?: string; core?: string };
    expectedSwapOut?: bigint;
    swapAmountOut?: bigint;
    coreAmount?: bigint;
    coreDust?: bigint;
  };
}

export interface Checkpoint {
  from: string;
  chainId: number;
  pendingNonceBefore: number;
  blockBefore: number;
  srcBridge: string;
  /** destination-chain block numbers recorded at send time (scan start for tracking) */
  destBlocks: Partial<Record<ChainKey, number>>;
}

export interface SubmittedIntent {
  chainId: number;
  from: string;
  to: string;
  data: string;
  value: bigint;
  quoteId: string;
  adoptedQuote: Quote;
  match: MatchSpec;
}

export interface SignedInfo {
  raw: string;
  hash: string;
  nonce: number;
}

export interface SendOptions {
  /** Mode L only if the signer is a local-key signer; set 'local' to force (signer must implement signTransaction). */
  signerMode?: 'local';
  approveAmount?: 'exact' | 'max';
  zeroFirst?: 'auto' | 'always' | 'never';
  /** awaited before any broadcast; a throw aborts the send (nothing is sent) */
  onSubmitting?: (intent: SubmittedIntent, checkpoint: Checkpoint) => void | Promise<void>;
  /** Mode L: awaited with the signed raw tx BEFORE broadcast; a throw aborts (nothing is broadcast) */
  onSigned?: (info: SignedInfo, intent: SubmittedIntent) => void | Promise<void>;
  /** called after broadcast with the hash. A throw is caught and reported as `sent.callbackError`; send() still returns the SentTx. */
  onBroadcast?: (hash: string) => void | Promise<void>;
  /** Mode W: OPTIONAL wallet timeout. Default: none (a timeout cannot prove the wallet did not send). On expiry => BroadcastOutcomeUnknownError */
  walletTimeoutMs?: number;
  /** skip the (cheap) final eth_call simulation. NOT recommended. */
  skipSimulation?: boolean;
}

export interface SentTx {
  hash: string;
  chainId: number;
  mode: 'L' | 'W';
  nonce?: number;
  raw?: string;
  checkpoint: Checkpoint;
  intent: SubmittedIntent;
  /** adopted (post re-quote) quote */
  quote: Quote;
  /** set when the onBroadcast callback threw: the tx IS broadcast. send() still returns this SentTx; CHECK this field. Never retry the send. */
  callbackError?: { name: string; message: string };
  wait(opts?: { timeoutMs?: number; pollMs?: number }): Promise<TransferHandle>;
}

export interface RawLog {
  address: string;
  topics: string[];
  data: string;
  transactionHash: string;
  logIndex: number;
  blockNumber: number;
}

export interface HopRef {
  destChainKey: ChainKey;
  destChainId: number;
  index: string; // decimal string (uint)
  kind: HopKind;
}

export interface ScanState {
  lastScanned: number;
  logs: RawLog[];
}

/** Decoded X1 `exactInputSingle` call, stored in the handle (all numbers as decimal strings). */
export interface X1Expect { router: string; tokenIn: string; tokenOut: string; fee: number; recipient: string; deadline: string; amountIn: string; amountOutMinimum: string }
/** Decoded X2 `transferToCoreFor` call, stored in the handle. `encodedAmount` is the amount the extraData asks for (the committed amount). */
export interface X2Expect { token: string; coreRecipient: string; encodedAmount: string }

/** Serializable (plain JSON). Persist it; `client.track(handle)` resumes from it after a restart. */
export interface TransferHandle {
  v: 1;
  network: NetworkName;
  route: Route;
  srcChainKey: ChainKey;
  srcChainId: number;
  srcContract: string;
  srcBridge: string;
  srcTxHash: string;
  srcBlock: number;
  hop1: HopRef;
  hop2?: Omit<HopRef, 'index'> & { index?: string };
  /** hop1 recipient on the hub for multihop (informational), expected deliveries */
  expect: { value: string; v2?: string; x1?: X1Expect; x2?: X2Expect; token?: string; coreAmount?: string; dust?: string; hop2Match?: { toChainID: number; to: string; value: string; networkFee: string; exFee: string; extraDataHash: string } };
  scanFrom: Partial<Record<ChainKey, number>>;
  scans: Record<string, ScanState>;
}

export type TransferState =
  | 'SOURCE_SUBMITTED'
  | 'SOURCE_FAILED'
  | 'IN_FLIGHT'
  | 'PENDING_ON_DEST'
  | 'DELIVERED'
  | 'DELIVERED_FALLBACK'
  | 'RELEASED_FROM_PENDING'
  | 'CORE_DEPOSIT_SUBMITTED'
  | 'FORWARDED'
  | 'HUB_FALLBACK'
  | 'PENDING_REMOVED'
  | 'PROCESSED_UNVERIFIED'
  | 'UNKNOWN';

export interface HopStatus {
  chainKey: ChainKey;
  chainId: number;
  /** which leg: 1 = first, 2 = hop after the hub */
  hop: 1 | 2;
  index: string;
  state: TransferState;
  /** token actually received (symbol/address as known) */
  token?: string;
  amount?: string | 'unknown';
  recipient?: string;
  reason?: string;
  code?: string;
  txHash?: string;
  detail?: Record<string, unknown>;
}

export interface TrackResult {
  state: TransferState;
  terminal: boolean;
  timedOut?: boolean;
  hops: HopStatus[];
  handle: TransferHandle;
  /** human-readable next step */
  note?: string;
}

export const TERMINAL_STATES: ReadonlySet<TransferState> = new Set<TransferState>([
  'SOURCE_FAILED',
  'DELIVERED',
  'DELIVERED_FALLBACK',
  'RELEASED_FROM_PENDING',
  'CORE_DEPOSIT_SUBMITTED',
  'HUB_FALLBACK',
  'PENDING_REMOVED',
  'PROCESSED_UNVERIFIED',
  'UNKNOWN',
]);

export interface PreflightIssue {
  level: 'error' | 'warn';
  code: string;
  message: string;
  hint?: string;
  /** PREFLIGHT_INCOMPLETE: the check that could not be completed and why (an unverified check never counts as passed) */
  check?: string;
  error?: string;
}
export interface PreflightReport {
  ok: boolean;
  issues: PreflightIssue[];
  checks: { name: string; ok: boolean; detail?: string }[];
}

export type FindResult =
  | { status: 'FOUND'; handle: TransferHandle; txHash: string }
  | { status: 'AMBIGUOUS'; candidates: { txHash: string; logIndex: number }[] }
  | { status: 'NOT_FOUND_YET' };

export interface FindSourceTransferResult {
  result: FindResult;
  /** information only: account nonce mined so far vs the nonce before send. Never evidence for a re-send. */
  nonceInfo: { minedNonce: number; pendingNonceBefore: number };
}

/** one transfer-shaped log of a replacement receipt */
export interface TransferEvidence { kind: 'bridge' | 'swap' | 'unknown'; emitter: string; logIndex: number; detail?: string }

export type ReplacementVerdict =
  | { verdict: 'FOUND'; handle: TransferHandle; txHash: string }
  | { verdict: 'NEW_TX_ALLOWED'; reason: 'replacement-failed' | 'replacement-not-a-transfer'; replacementHash: string }
  | { verdict: 'REPLACED_BY_DIFFERENT_TRANSFER'; replacementHash: string; different: TransferEvidence[] }
  /** the replacement receipt holds transfer-shaped logs that cannot be fully attributed (or a match mixed with other transfers): review manually, NEVER a new transaction */
  | { verdict: 'REPLACED_UNVERIFIED'; replacementHash: string; matched: TransferEvidence[]; different: TransferEvidence[]; unattributed: TransferEvidence[] }
  | { verdict: 'ORIGINAL_STILL_POSSIBLE'; reason: string }
  | { verdict: 'ORIGINAL_MINED'; txHash: string };
