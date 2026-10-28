package bridgesdk

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Route enumerates the 9 routes: R1-R6 base paths, X1 USDT->ONEUSD, X2 HyperCore deposit (R3 + core), X3 swap+bridge.
type Route string

const (
	RouteBSCToOne           Route = "R1" // BSC -> ONE chain (single hop)
	RouteOneToBSC           Route = "R2" // ONE chain -> BSC (single hop, native ONE source)
	RouteOneToHyperEVM      Route = "R3" // ONE chain -> HyperEVM (single hop, native ONE source)
	RouteHyperEVMToOne      Route = "R4" // HyperEVM -> ONE chain (single hop)
	RouteBSCToOneToHyperEVM Route = "R5" // BSC -> ONE chain -> HyperEVM (multihop through the hub; QuoteRequest.Hypercore adds the HyperCore deposit)
	RouteHyperEVMToOneToBSC Route = "R6" // HyperEVM -> ONE chain -> BSC (multihop through the hub)
	RouteBSCUSDTToONEUSD    Route = "X1" // BSC USDT -> ONEUSD on the ONE chain (bridge, then a swap executed on arrival)
	RouteOneToHyperCore     Route = "X2" // ONE chain -> HyperEVM with a HyperCore deposit on arrival
	RouteSwapBridge         Route = "X3" // swap on the ONE chain, then bridge the output (needs QuoteRequest.X3)
)

// Recipients overrides the per-hop recipients (zero address = use QuoteRequest.Recipient).
// OnHub receives native ONE on the hub if hop 2 of a multihop route fails; Dest is the hop-2 (or final) recipient on the destination chain and
// also gets any dust; Core is the HyperCore account credited by an X2 deposit.
type Recipients struct{ OnHub, Dest, Core common.Address }

// Bps returns a pointer for SwapOptions.SlippageBps (Bps(0) = explicit 0 bps; leave the field nil for the default).
func Bps(n uint32) *uint32 { return &n }

// Seconds returns a pointer for SwapOptions.DeadlineSec.
func Seconds(n uint64) *uint64 { return &n }

// SwapOptions tunes the swap of RouteBSCUSDTToONEUSD (X1) and RouteSwapBridge (X3). It is ignored by the other routes.
type SwapOptions struct {
	// SlippageBps: nil = default (50 bps); a non-nil 0 is exactly 0 bps. Valid range [0, 10000).
	SlippageBps *uint32
	// DeadlineSec: nil = default (1800 s). Must be > 0 (a deadline of "now" always fails).
	DeadlineSec *uint64
}

// X3Request describes the swap for RouteSwapBridge. TokenIn "native" (NativeIn=true) swaps native ONE through WCROSS.
type X3Request struct {
	NativeIn bool
	TokenIn  common.Address
	TokenOut common.Address
	PoolFee  uint32
	ToChain  ChainKey // bsc | hyperevm
	// X3 supports extraData = 0x only (no extraData option): the SwapBridgeRouter extraCall path is not a supported product path.
}

// QuoteRequest is the input of Client.Quote. All amounts are in the smallest unit (wei); every supported token has 18 decimals.
// Quote copies the request, so later changes to the caller's value never affect the quote.
type QuoteRequest struct {
	// Route selects the path; the source chain and token follow from it.
	Route Route
	// Amount is route dependent: the delivered amount for single-hop routes (R1-R4, X2); the MINIMUM final delivered amount (v2) for the
	// multihop routes R5/R6, where the source-side value is derived from it by adding hop-2 fees; the USDT amount for X1; the swap input
	// (amountIn) for X3. Must be > 0.
	Amount *big.Int
	// Sender is the account that signs and pays on the source chain. The signer given to Send must match it.
	Sender common.Address
	// Recipient is the default recipient of every hop.
	Recipient common.Address
	// Recipients overrides Recipient per hop; zero fields fall back to Recipient.
	Recipients Recipients
	// Hypercore (R5 only) adds the HyperCore deposit; for R3 use RouteOneToHyperCore.
	Hypercore bool
	// Swap is read by X1 and X3 only.
	Swap SwapOptions
	// X3 is required for RouteSwapBridge and ignored by the other routes.
	X3 *X3Request
	// Hop2FeeBuffer is added to the DECLARED hop-2 exFee (never to value).
	Hop2FeeBuffer *big.Int
	// Limits are optional explicit (tighter) user limits.
	// MaxTotalSpend caps the total the sender pays (value plus fees; for X3 the swap input); nil = the quoted total.
	MaxTotalSpend *big.Int
	// MinDelivered is the floor for the amount delivered (multihop: v2; X3: the minimum bridged value); nil = the quoted amount.
	// A quote that violates a limit is refused with *LimitViolationError.
	MinDelivered *big.Int
	// QuoteTTL overrides ClientOptions.QuoteTTL for this quote (0 = the client default).
	QuoteTTL time.Duration
}

// FeeTriple is a bridge fee quote from a BridgeVerifier: the transfer value must be at least MinimumValue, NetworkFee and ExFee are the two fee
// components paid on top of the value.
type FeeTriple struct{ MinimumValue, NetworkFee, ExFee *big.Int }

// Limits are the user-visible bounds a quote must satisfy. They are fixed when the quote is created: the re-quote that Send performs after the
// approve is checked against them and fails with *RequoteRequiredError instead of loosening them.
type Limits struct {
	// MaxTotalSpend is the most the sender may pay in total (value plus fees; X3: the swap input).
	MaxTotalSpend *big.Int
	// MinDelivered is the least that must be delivered (multihop: v2).
	MinDelivered     *big.Int
	AmountOutMinimum *big.Int // X1/X3: absolute floor, never recomputed downward
	MinBridgeValue   *big.Int // X3
}

// MatchKind tells FindSourceTransfer and the tracking code which on-chain log shape a MatchSpec describes.
type MatchKind string

const (
	MatchBridge MatchKind = "bridge" // a plain BridgeInitiated of the source bridge
	MatchSwap   MatchKind = "swap"   // an X3 swap: the router's SwapBridge log linked to the bridge's BridgeInitiated
)

// MatchSpec describes the source transfer a quote intends to create. Recovery (Client.FindSourceTransfer, Client.AnalyzeReplacement) compares it
// with the BridgeInitiated logs of a source receipt to decide whether that exact transfer happened. Value, fees and extraData are matched
// exactly; the fees of the event may be lower than the declared ones, never higher.
type MatchSpec struct {
	Kind       MatchKind
	ToChainID  uint64         // destination chain id of hop 1 (the hub for multihop and X1)
	FromToken  common.Address // source token (the native-token placeholder for a native source)
	ToToken    common.Address
	Recipient  common.Address // hop-1 recipient
	Value      *big.Int       // hop-1 value
	NetworkFee *big.Int
	ExFee      *big.Int
	ExtraHash  common.Hash    // keccak256 of the hop-1 extraData
	TokenIn    common.Address // swap
	TokenOut   common.Address // swap
	AmountIn   *big.Int       // swap
	// ToToken (bridge: the pair's remote token; swap: the EXPECTED destination token = hub getTokenPair(toChainID, tokenOut).remoteToken, read at quote time)
}

// HopKind is what a hop does on its destination chain; the tracker uses it to decide how to read the arrival logs.
type HopKind string

const (
	HopPlain      HopKind = "plain"      // a plain token transfer to the recipient
	HopHubForward HopKind = "hubForward" // hop 1 of a multihop route: arrives on the hub and starts hop 2
	HopX1         HopKind = "x1"         // arrival followed by an exactInputSingle swap executed on the hub
	HopX2         HopKind = "x2"         // arrival followed by a HyperCore deposit executed on HyperEVM
)

// QuoteKind is the structural family of a quote.
type QuoteKind string

const (
	KindSingle   QuoteKind = "single"   // one bridge hop (R1-R4, X2)
	KindMultihop QuoteKind = "multihop" // two hops through the hub (R5, R6)
	KindX1       QuoteKind = "x1"       // bridge plus a swap executed on arrival
	KindX3       QuoteKind = "x3"       // swap through the SwapBridgeRouter, then bridge
)

// Approval is the ERC-20 allowance a quote needs from the sender before the transaction can succeed. Quote.Approval is nil for a native source.
// Amount is the exact amount required; Send approves exactly that unless SendOptions.ApproveAmount says otherwise.
type Approval struct {
	Token, Spender common.Address
	Amount         *big.Int
}

// QuoteDetails holds the route-specific facts behind a quote, mostly for display and for the tracker.
type QuoteDetails struct {
	Hop1ToChainID     uint64         // destination chain id of hop 1 (the hub for multihop and X1)
	Hop1ToToken       common.Address // token received on the hop-1 destination
	HubChain          ChainKey       // the hub chain for multihop and X1; empty otherwise
	DestChain         ChainKey       // final destination chain
	DestChainID       uint64
	Hop1Kind          HopKind
	Hop2Kind          HopKind // empty unless the quote is multihop
	ExtraData         []byte  // the hop-1 extraData that is signed into the transaction (empty when none)
	OnHub, Dest, Core common.Address
	ExpectedSwapOut   *big.Int // X1: quoted swap output (before slippage)
	SwapAmountOut     *big.Int // X3: quoted swap output (before slippage)
	CoreAmount        *big.Int // X2: amount credited on HyperCore, in Core units (delivered amount divided by the token's Core unit, rounded down); nil when unavailable
	CoreDust          *big.Int // X2: remainder in wei that does not fit a whole Core unit and goes back to the destination recipient; nil when unavailable
}

// TxData is the transaction a quote asks the sender to sign: a call to To with Data, carrying Value of the native token.
type TxData struct {
	To    common.Address
	Data  []byte
	Value *big.Int
}

// Quote is bound to {network, route, source chain, sender}; limits are immutable once created. A Quote is SEALED: Seal carries the canonical payload of
// the whole quote and its keccak256 digest. Send verifies it first and then works only on a private copy restored from it, so the exported fields are
// INFORMATIONAL: a quote whose exported fields differ from the seal is refused (*LimitViolationError{Field: "seal", Reason: "modified"}). Unchanged
// copies (value copy, JSON round-trip) are accepted; to change anything call Quote() again. Use ApprovedRequest() / ApprovedLimits() for the sealed
// values. The digest guards against accidental or concurrent mutation; it is not an authenticity proof (anyone can build a new seal).
type Quote struct {
	Seal              QuoteSeal
	ID                string
	Network           NetworkName
	Route             Route
	Request           QuoteRequest
	Kind              QuoteKind
	SourceChain       ChainKey
	SourceChainID     int64
	SourceContract    common.Address // bridge, or SwapBridgeRouter for X3
	SourceBridge      common.Address
	Sender            common.Address
	CreatedAt         time.Time
	TTL               time.Duration  // Send refuses the quote once it is older than this (*QuoteExpiredError)
	SourceVerifier    common.Address // BridgeVerifier of the source bridge that priced hop 1
	HubVerifier       common.Address // BridgeVerifier of the hub that priced hop 2; set for multihop only
	Hop1Fees          FeeTriple
	Hop2Fees          *FeeTriple // hub fees of hop 2; multihop only
	Hop2ExFeeDeclared *big.Int   // hop-2 exFee declared in the hop-1 extraData (the hub fee plus Request.Hop2FeeBuffer); multihop only
	Value             *big.Int   // hop-1 delivered value (multihop: v1; X3: expected bridge value)
	V2                *big.Int   // multihop: the minimum final delivered amount (the requested Amount)
	TotalSpend        *big.Int   // everything the sender pays: value plus both hop-1 fees (X3: the swap input)
	Tx                TxData
	Approval          *Approval // nil when the source is the native token
	Limits            Limits
	Deadline          uint64 // X1/X3: swap deadline as a unix timestamp in seconds; 0 for the other routes
	Match             MatchSpec
	Details           QuoteDetails
}

// Checkpoint is the chain state Send records right before it signs and broadcasts. It is what FindSourceTransfer and AnalyzeReplacement start from
// when the broadcast outcome is unknown, so persist it together with the SubmittedIntent (OnSubmitting) if you need to recover after a crash.
type Checkpoint struct {
	From               common.Address
	ChainID            int64
	PendingNonceBefore uint64              // the sender's pending nonce on the source chain at that moment
	BlockBefore        uint64              // source chain head at that moment; the recovery log scan starts here
	SrcBridge          common.Address      // the contract whose logs are scanned (the bridge, or the SwapBridgeRouter for X3)
	DestBlocks         map[ChainKey]uint64 // destination chain heads, used as the starting point of tracking
}

// SubmittedIntent is the transaction Send is about to broadcast, as the SDK decided it: the exact call plus the transfer it is meant to create
// (Match). Recovery compares it with chain evidence instead of relying on a transaction hash.
type SubmittedIntent struct {
	ChainID      int64
	From         common.Address
	To           common.Address
	Data         []byte
	Value        *big.Int
	QuoteID      string
	AdoptedQuote *Quote // the quote that was actually sent: the re-quote made after the approve, not the caller's original
	Match        MatchSpec
}

// SignedInfo describes a signed transaction before it is broadcast (see SendOptions.OnSigned).
type SignedInfo struct {
	Raw   []byte // the signed transaction, ready for Client.Rebroadcast
	Hash  common.Hash
	Nonce uint64
}

// ApproveMode selects the amount of the ERC-20 approve that Send issues.
type ApproveMode int

const (
	ApproveExact ApproveMode = iota // approve exactly the amount the quote needs (the default)
	ApproveMax                      // approve the maximum uint256 value
)

// ZeroFirst controls the approve(0) that some tokens require before a non-zero allowance can be changed. It only applies when the current
// allowance is too low and an approve is needed.
type ZeroFirst int

const (
	ZeroFirstAuto   ZeroFirst = iota // approve(0) first only when the current allowance is non-zero (the default)
	ZeroFirstAlways                  // always send approve(0) before the real approve
	ZeroFirstNever                   // never send approve(0)
)

// SendOptions configure Client.Send and Client.EnsureAllowance. The zero value is valid: exact approve, zero-first only when needed, with
// simulation, and no callbacks.
type SendOptions struct {
	// ApproveAmount selects the approve amount (default ApproveExact).
	ApproveAmount ApproveMode
	// ZeroFirst selects when approve(0) is sent first (default ZeroFirstAuto).
	ZeroFirst ZeroFirst
	// OnSubmitting is called (and must return) before any broadcast; an error aborts the send.
	OnSubmitting func(intent *SubmittedIntent, cp Checkpoint) error
	// OnSigned is called with the signed raw tx BEFORE broadcast; an error aborts (nothing is broadcast).
	OnSigned func(info SignedInfo, intent *SubmittedIntent) error
	// OnBroadcast is called after the broadcast with the hash. A panic in it is recovered and reported as SentTx.CallbackError; Send
	// still returns the SentTx (the tx IS broadcast: never retry the send).
	OnBroadcast func(hash common.Hash)
	// SkipSimulation skips the eth_call of the final transaction before signing. Gas estimation still runs, so a revert is still reported.
	SkipSimulation bool
}

// SentTx is the result of a broadcast Send. Once you hold one the transaction has been submitted: never call Send again for the same transfer.
type SentTx struct {
	Hash       common.Hash // hash of the source transaction
	ChainID    int64
	Nonce      uint64
	Raw        []byte     // the signed transaction, ready for Client.Rebroadcast
	Checkpoint Checkpoint // chain state recorded before the broadcast (recovery starting point)
	Intent     *SubmittedIntent
	Quote      *Quote // the adopted quote that was actually sent
	// CallbackError is set when the OnBroadcast callback panicked. The tx IS broadcast; check this field. Never retry the send.
	CallbackError error
}

// ---- tracking -------------------------------------------------------------------------------------------------

// TransferState is the tracking state of a transfer or of one hop. Use IsTerminal to tell whether more progress is expected. A Track timeout is
// reported through TrackResult.TimedOut together with the last established non-terminal state, not as a state of its own.
type TransferState string

const (
	StateSourceSubmitted      TransferState = "SOURCE_SUBMITTED"       // the source transaction has no receipt yet
	StateSourceFailed         TransferState = "SOURCE_FAILED"          // the source transaction reverted; no transfer was created (terminal)
	StateInFlight             TransferState = "IN_FLIGHT"              // the destination has not finalized this transfer yet
	StatePendingOnDest        TransferState = "PENDING_ON_DEST"        // held in the destination pending queue (see HopStatus.Reason); released automatically or by an operator
	StateDelivered            TransferState = "DELIVERED"              // the hop completed as requested (terminal)
	StateDeliveredFallback    TransferState = "DELIVERED_FALLBACK"     // the plain token was delivered instead of the requested swap or Core deposit (terminal)
	StateReleasedFromPending  TransferState = "RELEASED_FROM_PENDING"  // released from the pending queue as the plain token (terminal)
	StateCoreDepositSubmitted TransferState = "CORE_DEPOSIT_SUBMITTED" // X2: the EVM-side transfer to HyperCore succeeded; the Core credit itself is asynchronous (terminal)
	StateForwarded            TransferState = "FORWARDED"              // multihop: hop 1 finished on the hub and hop 2 was started; hop 2 is still in progress
	StateHubFallback          TransferState = "HUB_FALLBACK"           // multihop: hop 2 could not start; native ONE stays on the hub for the hop-1 recipient (terminal)
	StatePendingRemoved       TransferState = "PENDING_REMOVED"        // an operator removed the pending item (terminal)
	StateProcessedUnverified  TransferState = "PROCESSED_UNVERIFIED"   // processed on chain but the evidence is missing or ambiguous; never reported as success (terminal)
	StateUnknown              TransferState = "UNKNOWN"                // evidence could not be read (RPC or log failure, timeout); see HopStatus.Reason (terminal)
)

var terminalStates = map[TransferState]bool{
	StateSourceFailed: true, StateDelivered: true, StateDeliveredFallback: true, StateReleasedFromPending: true,
	StateCoreDepositSubmitted: true, StateHubFallback: true, StatePendingRemoved: true, StateProcessedUnverified: true, StateUnknown: true,
}

// IsTerminal reports whether no further on-chain progress is expected.
func (s TransferState) IsTerminal() bool { return terminalStates[s] }

// RawLog is a compact, JSON-serialisable log.
type RawLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        uint     `json:"logIndex"`
	BlockNumber     uint64   `json:"blockNumber"`
}

// HopRef identifies one hop of a transfer by its destination. Together with the source chain id, Index is the key that links the destination
// events to this transfer.
type HopRef struct {
	DestChainKey ChainKey `json:"destChainKey"`
	DestChainID  int64    `json:"destChainId"`
	Index        string   `json:"index,omitempty"` // bridge transfer index as a decimal string; for hop 2 it is filled in while tracking
	Kind         HopKind  `json:"kind"`
}

// ScanState is the resumable log-scan progress of one destination chain and transfer key, stored in the handle.
type ScanState struct {
	LastScanned int64    `json:"lastScanned"` // last block already scanned
	Logs        []RawLog `json:"logs"`        // the relevant logs found so far; duplicates are ignored on resume
}

// X1Expect is the decoded X1 exactInputSingle call stored in the handle (numbers as decimal strings).
type X1Expect struct {
	Router           string `json:"router"`
	TokenIn          string `json:"tokenIn"`
	TokenOut         string `json:"tokenOut"`
	Fee              uint32 `json:"fee"`
	Recipient        string `json:"recipient"`
	Deadline         string `json:"deadline"`
	AmountIn         string `json:"amountIn"`
	AmountOutMinimum string `json:"amountOutMinimum"`
}

// X2Expect is the decoded X2 transferToCoreFor call stored in the handle. EncodedAmount is the amount the extraData asks for.
type X2Expect struct {
	Token         string `json:"token"`
	CoreRecipient string `json:"coreRecipient"`
	EncodedAmount string `json:"encodedAmount"`
}

// HandleExpect is what the source transaction committed to, decoded from the source receipt (amounts as decimal strings). The tracker checks the
// destination evidence against it and never reports an X1 or X2 hop as delivered without the matching decoded call.
type HandleExpect struct {
	Value string    `json:"value"`           // hop-1 value
	V2    string    `json:"v2,omitempty"`    // multihop: the minimum final delivered amount committed in the forward payload
	Token string    `json:"token,omitempty"` // token received on the hop-1 destination
	X1    *X1Expect `json:"x1,omitempty"`    // set for X1 only
	X2    *X2Expect `json:"x2,omitempty"`    // set for X2 (direct, or nested in a multihop forward payload)
}

// TransferHandle is plain JSON (same shape as the JS SDK): persist it; Track resumes from it after a restart.
type TransferHandle struct {
	V           int                   `json:"v"` // handle format version
	Network     NetworkName           `json:"network"`
	Route       Route                 `json:"route"`
	SrcChainKey ChainKey              `json:"srcChainKey"`
	SrcChainID  int64                 `json:"srcChainId"`
	SrcContract string                `json:"srcContract"` // contract that emitted the transfer: the bridge, or the SwapBridgeRouter for X3
	SrcBridge   string                `json:"srcBridge"`
	SrcTxHash   string                `json:"srcTxHash"`
	SrcBlock    uint64                `json:"srcBlock"`
	Hop1        HopRef                `json:"hop1"`
	Hop2        *HopRef               `json:"hop2,omitempty"` // multihop routes only
	Expect      HandleExpect          `json:"expect"`
	ScanFrom    map[ChainKey]uint64   `json:"scanFrom"` // block to start scanning each destination chain from
	Scans       map[string]*ScanState `json:"scans"`    // log-scan progress per destination chain and transfer key; this is what lets Track resume
}

// HopStatus is the tracking status of one hop.
type HopStatus struct {
	Chain     ChainKey // the chain this hop settles on (its destination); the source chain while the source transaction is not confirmed
	ChainID   int64
	Hop       int    // 1 or 2
	Index     string // bridge transfer index of this hop, decimal
	State     TransferState
	Token     string            // token actually delivered, when known
	Amount    string            // decimal, or "unknown"
	Recipient string            // who received the funds, when known
	Reason    string            // why the hop is pending (the finalize status), fell back, is unverified or unknown; an UNKNOWN caused by a timeout starts with "timeout:"
	Code      string            // HUB_FALLBACK only: the hub's forward failure code
	TxHash    string            // destination transaction that settled the hop, when known
	Detail    map[string]string // extra evidence, for example the HyperCore amounts of an X2 deposit
}

// TrackResult is what Track and TrackOnce return.
type TrackResult struct {
	State    TransferState   // overall state; for a multihop transfer this is the state of the furthest hop reached
	Terminal bool            // State.IsTerminal()
	TimedOut bool            // Track ran out of time; State is the last established non-terminal state (or UNKNOWN with a "timeout:" reason)
	Hops     []HopStatus     // one entry per evaluated hop
	Handle   *TransferHandle // the handle with updated scan progress; pass it to the next Track or TrackOnce call
	Note     string          // short human-readable explanation of State
}

// PreflightIssue is one finding of Client.Preflight.
type PreflightIssue struct {
	Level   string // error | warn
	Code    string // stable machine-readable identifier (for example PAIR_NOT_REGISTERED)
	Message string // what is wrong
	Hint    string // how to fix it, when known
	// PREFLIGHT_INCOMPLETE: the check that could not be completed and why (an unverified check never counts as passed).
	Check string
	Error string
}

// PreflightReport is the result of Client.Preflight.
type PreflightReport struct {
	OK     bool // false when at least one issue has Level "error"
	Issues []PreflightIssue
	Checks []PreflightCheck // every check that ran, passed or not
}

// PreflightCheck is one check that Client.Preflight ran.
type PreflightCheck struct {
	Name   string
	OK     bool
	Detail string
}

// FindStatus is the outcome of Client.FindSourceTransfer.
type FindStatus string

const (
	FindFound       FindStatus = "FOUND"         // exactly one source transfer matches the intent; FindResult.Handle is set
	FindAmbiguous   FindStatus = "AMBIGUOUS"     // several source transfers match; see FindResult.Candidates, never pick one automatically
	FindNotFoundYet FindStatus = "NOT_FOUND_YET" // no matching transfer found (yet); this alone does not prove the transaction was not sent
)

// SourceCandidate points at one source-chain log that matched an intent.
type SourceCandidate struct {
	TxHash   common.Hash
	LogIndex uint
}

// FindResult is the result of Client.FindSourceTransfer.
type FindResult struct {
	Status FindStatus
	// Handle and TxHash are set when Status is FindFound.
	Handle *TransferHandle
	TxHash common.Hash
	// Candidates lists the matches when Status is FindAmbiguous.
	Candidates []SourceCandidate
	// information only - never evidence for a re-send
	MinedNonce         uint64 // the sender's mined (confirmed) nonce on the source chain now
	PendingNonceBefore uint64 // the pending nonce recorded in the Checkpoint before the broadcast
}

// ReplacementVerdictKind is the verdict of Client.AnalyzeReplacement about a transaction that was mined at the nonce of the original one.
// Only VerdictNewTxAllowed means that a new transaction is safe.
type ReplacementVerdictKind string

const (
	VerdictFound                       ReplacementVerdictKind = "FOUND"                          // the intended transfer exists in the replacement; ReplacementVerdict.Handle is set
	VerdictNewTxAllowed                ReplacementVerdictKind = "NEW_TX_ALLOWED"                 // the replacement provably did not bridge anything; a new transaction is safe
	VerdictReplacedByDifferentTransfer ReplacementVerdictKind = "REPLACED_BY_DIFFERENT_TRANSFER" // the replacement carried other transfers only, none matching the intent
	VerdictOriginalStillPossible       ReplacementVerdictKind = "ORIGINAL_STILL_POSSIBLE"        // the original may still be mined; never safe to send again
	VerdictOriginalMined               ReplacementVerdictKind = "ORIGINAL_MINED"                 // the original transaction itself was mined
	// VerdictReplacedUnverified: the replacement receipt holds transfer-shaped logs that cannot be fully attributed (or a match mixed with other transfers):
	// review manually, NEVER a new transaction.
	VerdictReplacedUnverified ReplacementVerdictKind = "REPLACED_UNVERIFIED"
)

// TransferEvidence is one transfer-shaped log of a replacement receipt.
type TransferEvidence struct {
	Kind     string // "bridge" | "swap" | "unknown"
	Emitter  common.Address
	LogIndex uint
	Detail   string
}

// ReplacementVerdict is the result of Client.AnalyzeReplacement.
type ReplacementVerdict struct {
	Verdict         ReplacementVerdictKind
	Reason          string          // explanation of the verdict
	ReplacementHash common.Hash     // the transaction found at the original nonce, when one was located
	Handle          *TransferHandle // set for VerdictFound
	// Matched / Different / Unattributed: set for REPLACED_BY_DIFFERENT_TRANSFER (Different) and REPLACED_UNVERIFIED (all three)
	Matched      []TransferEvidence
	Different    []TransferEvidence
	Unattributed []TransferEvidence
}
