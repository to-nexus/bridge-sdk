package bridgesdk

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Route enumerates the 9 routes: R1-R6 base paths, X1 USDT->ONEUSD, X2 HyperCore deposit (R3 + core), X3 swap+bridge.
type Route string

const (
	RouteBSCToOne           Route = "R1"
	RouteOneToBSC           Route = "R2"
	RouteOneToHyperEVM      Route = "R3"
	RouteHyperEVMToOne      Route = "R4"
	RouteBSCToOneToHyperEVM Route = "R5"
	RouteHyperEVMToOneToBSC Route = "R6"
	RouteBSCUSDTToONEUSD    Route = "X1"
	RouteOneToHyperCore     Route = "X2"
	RouteSwapBridge         Route = "X3"
)

// Recipients overrides the per-hop recipients (zero address = use QuoteRequest.Recipient).
type Recipients struct{ OnHub, Dest, Core common.Address }

// Bps returns a pointer for SwapOptions.SlippageBps (Bps(0) = explicit 0 bps; leave the field nil for the default).
func Bps(n uint32) *uint32 { return &n }

// Seconds returns a pointer for SwapOptions.DeadlineSec.
func Seconds(n uint64) *uint64 { return &n }

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

type QuoteRequest struct {
	Route  Route
	Amount *big.Int // R1-R4/X2 delivered value; R5/R6 minimum delivered v2; X1 USDT value; X3 amountIn
	Sender common.Address
	// Recipient is the default recipient of every hop.
	Recipient  common.Address
	Recipients Recipients
	// Hypercore (R5 only) adds the HyperCore deposit; for R3 use RouteOneToHyperCore.
	Hypercore bool
	Swap      SwapOptions
	X3        *X3Request
	// Hop2FeeBuffer is added to the DECLARED hop-2 exFee (never to value).
	Hop2FeeBuffer *big.Int
	// Limits are optional explicit (tighter) user limits.
	MaxTotalSpend *big.Int
	MinDelivered  *big.Int
	QuoteTTL      time.Duration
}

type FeeTriple struct{ MinimumValue, NetworkFee, ExFee *big.Int }

type Limits struct {
	MaxTotalSpend    *big.Int
	MinDelivered     *big.Int
	AmountOutMinimum *big.Int // X1/X3: absolute floor, never recomputed downward
	MinBridgeValue   *big.Int // X3
}

type MatchKind string

const (
	MatchBridge MatchKind = "bridge"
	MatchSwap   MatchKind = "swap"
)

type MatchSpec struct {
	Kind       MatchKind
	ToChainID  uint64
	FromToken  common.Address
	ToToken    common.Address
	Recipient  common.Address
	Value      *big.Int
	NetworkFee *big.Int
	ExFee      *big.Int
	ExtraHash  common.Hash
	TokenIn    common.Address // swap
	TokenOut   common.Address // swap
	AmountIn   *big.Int       // swap
	// ToToken (bridge: the pair's remote token; swap: the EXPECTED destination token = hub getTokenPair(toChainID, tokenOut).remoteToken, read at quote time)
}

type HopKind string

const (
	HopPlain      HopKind = "plain"
	HopHubForward HopKind = "hubForward"
	HopX1         HopKind = "x1"
	HopX2         HopKind = "x2"
)

type QuoteKind string

const (
	KindSingle   QuoteKind = "single"
	KindMultihop QuoteKind = "multihop"
	KindX1       QuoteKind = "x1"
	KindX3       QuoteKind = "x3"
)

type Approval struct {
	Token, Spender common.Address
	Amount         *big.Int
}

type QuoteDetails struct {
	Hop1ToChainID     uint64
	Hop1ToToken       common.Address
	HubChain          ChainKey
	DestChain         ChainKey
	DestChainID       uint64
	Hop1Kind          HopKind
	Hop2Kind          HopKind
	ExtraData         []byte
	OnHub, Dest, Core common.Address
	ExpectedSwapOut   *big.Int
	SwapAmountOut     *big.Int
	CoreAmount        *big.Int
	CoreDust          *big.Int
}

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
	TTL               time.Duration
	SourceVerifier    common.Address
	HubVerifier       common.Address
	Hop1Fees          FeeTriple
	Hop2Fees          *FeeTriple
	Hop2ExFeeDeclared *big.Int
	Value             *big.Int // hop-1 delivered value (multihop: v1; X3: expected bridge value)
	V2                *big.Int
	TotalSpend        *big.Int
	Tx                TxData
	Approval          *Approval
	Limits            Limits
	Deadline          uint64
	Match             MatchSpec
	Details           QuoteDetails
}

type Checkpoint struct {
	From               common.Address
	ChainID            int64
	PendingNonceBefore uint64
	BlockBefore        uint64
	SrcBridge          common.Address
	DestBlocks         map[ChainKey]uint64
}

type SubmittedIntent struct {
	ChainID      int64
	From         common.Address
	To           common.Address
	Data         []byte
	Value        *big.Int
	QuoteID      string
	AdoptedQuote *Quote
	Match        MatchSpec
}

type SignedInfo struct {
	Raw   []byte
	Hash  common.Hash
	Nonce uint64
}

type ApproveMode int

const (
	ApproveExact ApproveMode = iota
	ApproveMax
)

type ZeroFirst int

const (
	ZeroFirstAuto ZeroFirst = iota
	ZeroFirstAlways
	ZeroFirstNever
)

type SendOptions struct {
	ApproveAmount ApproveMode
	ZeroFirst     ZeroFirst
	// OnSubmitting is called (and must return) before any broadcast; an error aborts the send.
	OnSubmitting func(intent *SubmittedIntent, cp Checkpoint) error
	// OnSigned is called with the signed raw tx BEFORE broadcast; an error aborts (nothing is broadcast).
	OnSigned func(info SignedInfo, intent *SubmittedIntent) error
	// OnBroadcast is called after the broadcast with the hash. A panic in it is recovered and reported as SentTx.CallbackError; Send
	// still returns the SentTx (the tx IS broadcast: never retry the send).
	OnBroadcast    func(hash common.Hash)
	SkipSimulation bool
}

type SentTx struct {
	Hash       common.Hash
	ChainID    int64
	Nonce      uint64
	Raw        []byte
	Checkpoint Checkpoint
	Intent     *SubmittedIntent
	Quote      *Quote
	// CallbackError is set when the OnBroadcast callback panicked. The tx IS broadcast; check this field. Never retry the send.
	CallbackError error
}

// ---- tracking -------------------------------------------------------------------------------------------------

type TransferState string

const (
	StateSourceSubmitted      TransferState = "SOURCE_SUBMITTED"
	StateSourceFailed         TransferState = "SOURCE_FAILED"
	StateInFlight             TransferState = "IN_FLIGHT"
	StatePendingOnDest        TransferState = "PENDING_ON_DEST"
	StateDelivered            TransferState = "DELIVERED"
	StateDeliveredFallback    TransferState = "DELIVERED_FALLBACK"
	StateReleasedFromPending  TransferState = "RELEASED_FROM_PENDING"
	StateCoreDepositSubmitted TransferState = "CORE_DEPOSIT_SUBMITTED"
	StateForwarded            TransferState = "FORWARDED"
	StateHubFallback          TransferState = "HUB_FALLBACK"
	StatePendingRemoved       TransferState = "PENDING_REMOVED"
	StateProcessedUnverified  TransferState = "PROCESSED_UNVERIFIED"
	StateUnknown              TransferState = "UNKNOWN"
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

type HopRef struct {
	DestChainKey ChainKey `json:"destChainKey"`
	DestChainID  int64    `json:"destChainId"`
	Index        string   `json:"index,omitempty"`
	Kind         HopKind  `json:"kind"`
}

type ScanState struct {
	LastScanned int64    `json:"lastScanned"`
	Logs        []RawLog `json:"logs"`
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

type HandleExpect struct {
	Value string    `json:"value"`
	V2    string    `json:"v2,omitempty"`
	Token string    `json:"token,omitempty"`
	X1    *X1Expect `json:"x1,omitempty"`
	X2    *X2Expect `json:"x2,omitempty"`
}

// TransferHandle is plain JSON (same shape as the JS SDK): persist it; Track resumes from it after a restart.
type TransferHandle struct {
	V           int                   `json:"v"`
	Network     NetworkName           `json:"network"`
	Route       Route                 `json:"route"`
	SrcChainKey ChainKey              `json:"srcChainKey"`
	SrcChainID  int64                 `json:"srcChainId"`
	SrcContract string                `json:"srcContract"`
	SrcBridge   string                `json:"srcBridge"`
	SrcTxHash   string                `json:"srcTxHash"`
	SrcBlock    uint64                `json:"srcBlock"`
	Hop1        HopRef                `json:"hop1"`
	Hop2        *HopRef               `json:"hop2,omitempty"`
	Expect      HandleExpect          `json:"expect"`
	ScanFrom    map[ChainKey]uint64   `json:"scanFrom"`
	Scans       map[string]*ScanState `json:"scans"`
}

type HopStatus struct {
	Chain     ChainKey
	ChainID   int64
	Hop       int
	Index     string
	State     TransferState
	Token     string
	Amount    string // decimal, or "unknown"
	Recipient string
	Reason    string
	Code      string
	TxHash    string
	Detail    map[string]string
}

type TrackResult struct {
	State    TransferState
	Terminal bool
	TimedOut bool
	Hops     []HopStatus
	Handle   *TransferHandle
	Note     string
}

type PreflightIssue struct {
	Level   string // error | warn
	Code    string
	Message string
	Hint    string
	// PREFLIGHT_INCOMPLETE: the check that could not be completed and why (an unverified check never counts as passed).
	Check string
	Error string
}

type PreflightReport struct {
	OK     bool
	Issues []PreflightIssue
	Checks []PreflightCheck
}

type PreflightCheck struct {
	Name   string
	OK     bool
	Detail string
}

type FindStatus string

const (
	FindFound       FindStatus = "FOUND"
	FindAmbiguous   FindStatus = "AMBIGUOUS"
	FindNotFoundYet FindStatus = "NOT_FOUND_YET"
)

type SourceCandidate struct {
	TxHash   common.Hash
	LogIndex uint
}

type FindResult struct {
	Status     FindStatus
	Handle     *TransferHandle
	TxHash     common.Hash
	Candidates []SourceCandidate
	// information only - never evidence for a re-send
	MinedNonce         uint64
	PendingNonceBefore uint64
}

type ReplacementVerdictKind string

const (
	VerdictFound                       ReplacementVerdictKind = "FOUND"
	VerdictNewTxAllowed                ReplacementVerdictKind = "NEW_TX_ALLOWED"
	VerdictReplacedByDifferentTransfer ReplacementVerdictKind = "REPLACED_BY_DIFFERENT_TRANSFER"
	VerdictOriginalStillPossible       ReplacementVerdictKind = "ORIGINAL_STILL_POSSIBLE"
	VerdictOriginalMined               ReplacementVerdictKind = "ORIGINAL_MINED"
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

type ReplacementVerdict struct {
	Verdict         ReplacementVerdictKind
	Reason          string
	ReplacementHash common.Hash
	Handle          *TransferHandle
	// Matched / Different / Unattributed: set for REPLACED_BY_DIFFERENT_TRANSFER (Different) and REPLACED_UNVERIFIED (all three)
	Matched      []TransferEvidence
	Different    []TransferEvidence
	Unattributed []TransferEvidence
}
