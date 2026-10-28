package bridgesdk

// Quote seal (format v1). A quote carries Seal = {V, SDK, Payload, Digest}: Payload is the canonical JSON of the WHOLE quote (without the seal) in this
// SDK's wire DTO (quoteWireV1: fixed units, decimal strings, lowercase hex), Digest = keccak256(utf8(Payload)). Send verifies it FIRST (before any RPC)
// and then works ONLY on an unexported copy restored from the payload; the caller's *Quote is never read again.
//
// The digest is an integrity check against accidental or concurrent mutation (an edited copy, a shared quote changed while an approve is pending).
// It is NOT an authenticity proof: whoever holds the quote can always build a new seal. Edit a request by calling Quote() again.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// QuoteSeal is public, enumerable and survives value copies and a JSON round-trip.
type QuoteSeal struct {
	V       int    `json:"v"`
	SDK     string `json:"sdk"` // "go"; a seal of the other SDK is refused ("unsupported-sdk")
	Payload string `json:"payload"`
	Digest  string `json:"digest"` // 0x-hex keccak256(utf8(Payload))
}

const sealSDK = "go"

// Seal failure reasons (LimitViolationError{Field:"seal", Reason}).
const (
	SealMissing            = "missing"
	SealUnsupportedVersion = "unsupported-version"
	SealUnsupportedSDK     = "unsupported-sdk"
	SealCorrupt            = "corrupt"
	SealInvalidPayload     = "invalid-payload"
	SealModified           = "modified"
)

// ---------------------------------------------------------------------------------------------------- wire DTO (explicit json tags)
type wireFee struct {
	MinimumValue string `json:"minimumValue"`
	NetworkFee   string `json:"networkFee"`
	ExFee        string `json:"exFee"`
}

type wireRecipients struct {
	OnHub *string `json:"onHub,omitempty"`
	Dest  *string `json:"dest,omitempty"`
	Core  *string `json:"core,omitempty"`
}

type wireSwap struct {
	SlippageBps *uint32 `json:"slippageBps,omitempty"`
	DeadlineSec *string `json:"deadlineSec,omitempty"`
}

type wireX3 struct {
	TokenIn  string `json:"tokenIn"` // address or "native"
	TokenOut string `json:"tokenOut"`
	PoolFee  uint32 `json:"poolFee"`
	ToChain  string `json:"toChain"`
}

type wireRequest struct {
	Route         string          `json:"route"`
	Amount        string          `json:"amount"`
	Sender        string          `json:"sender"`
	Recipient     string          `json:"recipient"`
	Recipients    *wireRecipients `json:"recipients,omitempty"`
	Hypercore     bool            `json:"hypercore,omitempty"`
	Swap          *wireSwap       `json:"swap,omitempty"`
	X3            *wireX3         `json:"x3,omitempty"`
	Hop2FeeBuffer *string         `json:"hop2FeeBuffer,omitempty"`
	MaxTotalSpend *string         `json:"maxTotalSpend,omitempty"`
	MinDelivered  *string         `json:"minDelivered,omitempty"`
	QuoteTTLMs    *string         `json:"quoteTtlMs,omitempty"`
}

type wireTx struct {
	To    string `json:"to"`
	Data  string `json:"data"`
	Value string `json:"value"`
}

type wireApproval struct {
	Token   string `json:"token"`
	Spender string `json:"spender"`
	Amount  string `json:"amount"`
}

type wireLimits struct {
	MaxTotalSpend    string  `json:"maxTotalSpend"`
	MinDelivered     string  `json:"minDelivered"`
	AmountOutMinimum *string `json:"amountOutMinimum,omitempty"`
	MinBridgeValue   *string `json:"minBridgeValue,omitempty"`
}

type wireMatch struct {
	Kind       string `json:"kind"`
	ToChainID  string `json:"toChainId"`
	Recipient  string `json:"recipient"`
	ExtraHash  string `json:"extraDataHash"`
	FromToken  string `json:"fromToken,omitempty"` // bridge
	ToToken    string `json:"toToken,omitempty"`   // bridge
	Value      string `json:"value,omitempty"`     // bridge
	NetworkFee string `json:"networkFee,omitempty"`
	ExFee      string `json:"exFee,omitempty"`
	TokenIn    string `json:"tokenIn,omitempty"` // swap
	TokenOut   string `json:"tokenOut,omitempty"`
	AmountIn   string `json:"amountIn,omitempty"`
}

type wireDetails struct {
	Hop1ToChainID   string  `json:"hop1ToChainId"`
	Hop1ToToken     string  `json:"hop1ToToken"`
	HubChain        *string `json:"hubChain,omitempty"`
	DestChain       string  `json:"destChain"`
	DestChainID     string  `json:"destChainId"`
	Hop1Kind        string  `json:"hop1Kind"`
	Hop2Kind        *string `json:"hop2Kind,omitempty"`
	ExtraData       string  `json:"extraData"`
	OnHub           *string `json:"onHub,omitempty"`
	Dest            *string `json:"dest,omitempty"`
	Core            *string `json:"core,omitempty"`
	ExpectedSwapOut *string `json:"expectedSwapOut,omitempty"`
	SwapAmountOut   *string `json:"swapAmountOut,omitempty"`
	CoreAmount      *string `json:"coreAmount,omitempty"`
	CoreDust        *string `json:"coreDust,omitempty"`
}

// quoteWireV1 is the Go wire DTO of a quote. Every amount / fee / chain id / timestamp is a decimal string; createdAtMs / ttlMs in ms, deadlineSec in Unix s.
type quoteWireV1 struct {
	ID                string        `json:"id"`
	Network           string        `json:"network"`
	Route             string        `json:"route"`
	Request           wireRequest   `json:"request"`
	Kind              string        `json:"kind"`
	SourceChain       string        `json:"sourceChain"`
	SourceChainID     string        `json:"sourceChainId"`
	SourceContract    string        `json:"sourceContract"`
	SourceBridge      string        `json:"sourceBridge"`
	Sender            string        `json:"sender"`
	CreatedAtMs       string        `json:"createdAtMs"`
	TTLMs             string        `json:"ttlMs"`
	SourceVerifier    *string       `json:"sourceVerifier,omitempty"`
	HubVerifier       *string       `json:"hubVerifier,omitempty"`
	Hop1Fees          wireFee       `json:"hop1Fees"`
	Hop2Fees          *wireFee      `json:"hop2Fees,omitempty"`
	Hop2ExFeeDeclared *string       `json:"hop2ExFeeDeclared,omitempty"`
	Value             string        `json:"value"`
	V2                *string       `json:"v2,omitempty"`
	TotalSpend        string        `json:"totalSpend"`
	Tx                wireTx        `json:"tx"`
	Approval          *wireApproval `json:"approval,omitempty"`
	Limits            wireLimits    `json:"limits"`
	DeadlineSec       *string       `json:"deadlineSec,omitempty"`
	Match             wireMatch     `json:"match"`
	Details           wireDetails   `json:"details"`
}

// ---------------------------------------------------------------------------------------------------- errors / paths
type wireError struct{ path, msg string }

func (e *wireError) Error() string { return e.path + ": " + e.msg }

// ---------------------------------------------------------------------------------------------------- encode: public fields -> wire
type encoder struct{ err *wireError }

func (e *encoder) fail(path, msg string) {
	if e.err == nil {
		e.err = &wireError{path, msg}
	}
}

func (e *encoder) dec(path string, b *big.Int) string {
	if b == nil || b.Sign() < 0 {
		e.fail(path, "expected a non-negative integer")
		return ""
	}
	return b.String()
}

func (e *encoder) optDec(path string, b *big.Int) *string {
	if b == nil {
		return nil
	}
	s := e.dec(path, b)
	return &s
}

func (e *encoder) u64(path string, v uint64) string { return new(big.Int).SetUint64(v).String() }

func (e *encoder) i64(path string, v int64) string {
	if v < 0 {
		e.fail(path, "expected a non-negative integer")
	}
	return big.NewInt(v).String()
}

func (e *encoder) addr(a common.Address) string { return strings.ToLower(a.Hex()) }

func (e *encoder) optAddr(a common.Address) *string {
	if a == (common.Address{}) {
		return nil
	}
	s := e.addr(a)
	return &s
}

func (e *encoder) optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (e *encoder) str(path, s string) string {
	if s == "" {
		e.fail(path, "expected a non-empty string")
	}
	return s
}

func (e *encoder) fee(path string, f FeeTriple) wireFee {
	return wireFee{e.dec(path+".minimumValue", f.MinimumValue), e.dec(path+".networkFee", f.NetworkFee), e.dec(path+".exFee", f.ExFee)}
}

func (e *encoder) hex(b []byte) string { return "0x" + hex.EncodeToString(b) }

func encodeQuoteWire(q *Quote) (*quoteWireV1, error) {
	e := &encoder{}
	w := &quoteWireV1{}
	w.ID = e.str("id", q.ID)
	w.Network = e.str("network", string(q.Network))
	w.Route = e.str("route", string(q.Route))
	r := q.Request
	w.Request = wireRequest{Route: e.str("request.route", string(r.Route)), Amount: e.dec("request.amount", r.Amount), Sender: e.addr(r.Sender), Recipient: e.addr(r.Recipient), Hypercore: r.Hypercore}
	if r.Recipients != (Recipients{}) {
		w.Request.Recipients = &wireRecipients{e.optAddr(r.Recipients.OnHub), e.optAddr(r.Recipients.Dest), e.optAddr(r.Recipients.Core)}
	}
	if r.Swap.SlippageBps != nil || r.Swap.DeadlineSec != nil {
		ws := &wireSwap{SlippageBps: clonePtr(r.Swap.SlippageBps)}
		if r.Swap.DeadlineSec != nil {
			s := e.u64("request.swap.deadlineSec", *r.Swap.DeadlineSec)
			ws.DeadlineSec = &s
		}
		w.Request.Swap = ws
	}
	if r.X3 != nil {
		tin := e.addr(r.X3.TokenIn)
		if r.X3.NativeIn {
			tin = "native"
		}
		w.Request.X3 = &wireX3{TokenIn: tin, TokenOut: e.addr(r.X3.TokenOut), PoolFee: r.X3.PoolFee, ToChain: e.str("request.x3.toChain", string(r.X3.ToChain))}
	}
	w.Request.Hop2FeeBuffer = e.optDec("request.hop2FeeBuffer", r.Hop2FeeBuffer)
	w.Request.MaxTotalSpend = e.optDec("request.maxTotalSpend", r.MaxTotalSpend)
	w.Request.MinDelivered = e.optDec("request.minDelivered", r.MinDelivered)
	if r.QuoteTTL != 0 {
		s := e.i64("request.quoteTtlMs", r.QuoteTTL.Milliseconds())
		w.Request.QuoteTTLMs = &s
	}
	w.Kind = e.str("kind", string(q.Kind))
	w.SourceChain = e.str("sourceChain", string(q.SourceChain))
	w.SourceChainID = e.i64("sourceChainId", q.SourceChainID)
	w.SourceContract, w.SourceBridge, w.Sender = e.addr(q.SourceContract), e.addr(q.SourceBridge), e.addr(q.Sender)
	w.CreatedAtMs = e.i64("createdAtMs", q.CreatedAt.UnixMilli())
	w.TTLMs = e.i64("ttlMs", q.TTL.Milliseconds())
	w.SourceVerifier, w.HubVerifier = e.optAddr(q.SourceVerifier), e.optAddr(q.HubVerifier)
	w.Hop1Fees = e.fee("hop1Fees", q.Hop1Fees)
	if q.Hop2Fees != nil {
		f := e.fee("hop2Fees", *q.Hop2Fees)
		w.Hop2Fees = &f
	}
	w.Hop2ExFeeDeclared = e.optDec("hop2ExFeeDeclared", q.Hop2ExFeeDeclared)
	w.Value = e.dec("value", q.Value)
	w.V2 = e.optDec("v2", q.V2)
	w.TotalSpend = e.dec("totalSpend", q.TotalSpend)
	w.Tx = wireTx{e.addr(q.Tx.To), e.hex(q.Tx.Data), e.dec("tx.value", q.Tx.Value)}
	if q.Approval != nil {
		w.Approval = &wireApproval{e.addr(q.Approval.Token), e.addr(q.Approval.Spender), e.dec("approval.amount", q.Approval.Amount)}
	}
	w.Limits = wireLimits{MaxTotalSpend: e.dec("limits.maxTotalSpend", q.Limits.MaxTotalSpend), MinDelivered: e.dec("limits.minDelivered", q.Limits.MinDelivered),
		AmountOutMinimum: e.optDec("limits.amountOutMinimum", q.Limits.AmountOutMinimum), MinBridgeValue: e.optDec("limits.minBridgeValue", q.Limits.MinBridgeValue)}
	if q.Deadline != 0 {
		s := e.u64("deadlineSec", q.Deadline)
		w.DeadlineSec = &s
	}
	m := q.Match
	w.Match = wireMatch{Kind: string(m.Kind), ToChainID: e.u64("match.toChainId", m.ToChainID), Recipient: e.addr(m.Recipient), ExtraHash: e.hex(m.ExtraHash[:])}
	switch m.Kind {
	case MatchBridge:
		w.Match.FromToken, w.Match.ToToken = e.addr(m.FromToken), e.addr(m.ToToken)
		w.Match.Value, w.Match.NetworkFee, w.Match.ExFee = e.dec("match.value", m.Value), e.dec("match.networkFee", m.NetworkFee), e.dec("match.exFee", m.ExFee)
	case MatchSwap:
		w.Match.TokenIn, w.Match.TokenOut, w.Match.AmountIn = e.addr(m.TokenIn), e.addr(m.TokenOut), e.dec("match.amountIn", m.AmountIn)
		w.Match.ToToken = e.addr(m.ToToken) // X3: the EXPECTED destination token (wire path match.toToken, required)
	default:
		e.fail("match.kind", "unknown variant")
	}
	d := q.Details
	w.Details = wireDetails{Hop1ToChainID: e.u64("details.hop1ToChainId", d.Hop1ToChainID), Hop1ToToken: e.addr(d.Hop1ToToken), HubChain: e.optStr(string(d.HubChain)),
		DestChain: e.str("details.destChain", string(d.DestChain)), DestChainID: e.u64("details.destChainId", d.DestChainID), Hop1Kind: e.str("details.hop1Kind", string(d.Hop1Kind)),
		Hop2Kind: e.optStr(string(d.Hop2Kind)), ExtraData: e.hex(d.ExtraData), OnHub: e.optAddr(d.OnHub), Dest: e.optAddr(d.Dest), Core: e.optAddr(d.Core),
		ExpectedSwapOut: e.optDec("details.expectedSwapOut", d.ExpectedSwapOut), SwapAmountOut: e.optDec("details.swapAmountOut", d.SwapAmountOut),
		CoreAmount: e.optDec("details.coreAmount", d.CoreAmount), CoreDust: e.optDec("details.coreDust", d.CoreDust)}
	if e.err != nil {
		return nil, e.err
	}
	return w, nil
}

// ---------------------------------------------------------------------------------------------------- canonical JSON
// canonicalJSON: keys sorted bytewise, no whitespace, no null. The wire DTO only holds strings, booleans and small unsigned integers.
func canonicalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, tree); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		return fmt.Errorf("canonicalJSON: null is never emitted")
	case string:
		b, _ := json.Marshal(x)
		buf.Write(bytes.ReplaceAll(bytes.ReplaceAll(bytes.ReplaceAll(b, []byte(`<`), []byte("<")), []byte(`>`), []byte(">")), []byte(`&`), []byte("&")))
	case bool:
		fmt.Fprintf(buf, "%t", x)
	case json.Number:
		buf.WriteString(x.String())
	case []any:
		buf.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, it); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonicalJSON: unsupported %T", v)
	}
	return nil
}

func digestOf(payload string) string { return crypto.Keccak256Hash([]byte(payload)).Hex() }

func sealPayload(q *Quote) (string, error) {
	w, err := encodeQuoteWire(q)
	if err != nil {
		return "", err
	}
	return canonicalJSON(w)
}

// sealQuoteInPlace seals a quote built by this SDK (also the test helper: it re-seals a hand-modified quote; unexported on purpose).
func sealQuoteInPlace(q *Quote) error {
	p, err := sealPayload(q)
	if err != nil {
		return err
	}
	q.Seal = QuoteSeal{V: 1, SDK: sealSDK, Payload: p, Digest: digestOf(p)}
	return nil
}

// ---------------------------------------------------------------------------------------------------- strict parse + schema validation of the payload
var (
	reDec  = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	reAddr = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	reHex  = regexp.MustCompile(`^0x([0-9a-f]{2})*$`)
	reHash = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
)

type validator struct{ err *wireError }

func (v *validator) fail(path, msg string) {
	if v.err == nil {
		v.err = &wireError{path, msg}
	}
}

func (v *validator) dec(path, s string, max uint64) {
	if !reDec.MatchString(s) {
		v.fail(path, "expected a decimal string (non-negative integer)")
		return
	}
	b, _ := new(big.Int).SetString(s, 10)
	if b == nil {
		v.fail(path, "not a number")
		return
	}
	if max != 0 && (!b.IsUint64() || b.Uint64() > max) {
		v.fail(path, "out of range")
	}
}

func (v *validator) optDec(path string, s *string) {
	if s != nil {
		v.dec(path, *s, 0)
	}
}

func (v *validator) addr(path, s string) {
	if !reAddr.MatchString(s) {
		v.fail(path, "malformed address")
	}
}

func (v *validator) optAddr(path string, s *string) {
	if s != nil {
		v.addr(path, *s)
	}
}

func (v *validator) hex(path, s string, hash bool) {
	if hash && !reHash.MatchString(s) || !hash && !reHex.MatchString(s) {
		v.fail(path, "malformed hex")
	}
}

func (v *validator) enum(path, s string, allowed ...string) {
	for _, a := range allowed {
		if s == a {
			return
		}
	}
	v.fail(path, "unexpected value")
}

func (v *validator) fee(path string, f wireFee) {
	v.dec(path+".minimumValue", f.MinimumValue, 0)
	v.dec(path+".networkFee", f.NetworkFee, 0)
	v.dec(path+".exFee", f.ExFee, 0)
}

var (
	allRoutes = []string{string(RouteBSCToOne), string(RouteOneToBSC), string(RouteOneToHyperEVM), string(RouteHyperEVMToOne), string(RouteBSCToOneToHyperEVM), string(RouteHyperEVMToOneToBSC),
		string(RouteBSCUSDTToONEUSD), string(RouteOneToHyperCore), string(RouteSwapBridge)}
	allChains = []string{string(ChainOne), string(ChainBSC), string(ChainHyperEVM)}
	allHops   = []string{string(HopPlain), string(HopHubForward), string(HopX1), string(HopX2)}
)

func (v *validator) quote(w *quoteWireV1) {
	if w.ID == "" {
		v.fail("id", "expected a non-empty string")
	}
	// the network name is a static NetworkName; whether it equals the CLIENT's network is checked by the client binding check (binding.go). No configuration is read here.
	v.enum("network", w.Network, string(Mainnet), string(Testnet))
	v.enum("route", w.Route, allRoutes...)
	r := w.Request
	v.enum("request.route", r.Route, allRoutes...)
	v.dec("request.amount", r.Amount, 0)
	v.addr("request.sender", r.Sender)
	v.addr("request.recipient", r.Recipient)
	if r.Recipients != nil {
		v.optAddr("request.recipients.onHub", r.Recipients.OnHub)
		v.optAddr("request.recipients.dest", r.Recipients.Dest)
		v.optAddr("request.recipients.core", r.Recipients.Core)
	}
	if r.Swap != nil && r.Swap.DeadlineSec != nil {
		v.dec("request.swap.deadlineSec", *r.Swap.DeadlineSec, math.MaxUint64)
	}
	if r.X3 != nil {
		if r.X3.TokenIn != "native" {
			v.addr("request.x3.tokenIn", r.X3.TokenIn)
		}
		v.addr("request.x3.tokenOut", r.X3.TokenOut)
		v.enum("request.x3.toChain", r.X3.ToChain, string(ChainBSC), string(ChainHyperEVM))
	}
	v.optDec("request.hop2FeeBuffer", r.Hop2FeeBuffer)
	v.optDec("request.maxTotalSpend", r.MaxTotalSpend)
	v.optDec("request.minDelivered", r.MinDelivered)
	if r.QuoteTTLMs != nil {
		v.durationMs("request.quoteTtlMs", *r.QuoteTTLMs)
	}
	v.enum("kind", w.Kind, string(KindSingle), string(KindMultihop), string(KindX1), string(KindX3))
	v.enum("sourceChain", w.SourceChain, allChains...)
	v.dec("sourceChainId", w.SourceChainID, math.MaxInt64)
	v.addr("sourceContract", w.SourceContract)
	v.addr("sourceBridge", w.SourceBridge)
	v.addr("sender", w.Sender)
	v.createdAtMs("createdAtMs", w.CreatedAtMs)
	v.durationMs("ttlMs", w.TTLMs)
	v.optAddr("sourceVerifier", w.SourceVerifier)
	v.optAddr("hubVerifier", w.HubVerifier)
	v.fee("hop1Fees", w.Hop1Fees)
	if w.Hop2Fees != nil {
		v.fee("hop2Fees", *w.Hop2Fees)
	}
	v.optDec("hop2ExFeeDeclared", w.Hop2ExFeeDeclared)
	v.dec("value", w.Value, 0)
	v.optDec("v2", w.V2)
	v.dec("totalSpend", w.TotalSpend, 0)
	v.addr("tx.to", w.Tx.To)
	v.hex("tx.data", w.Tx.Data, false)
	v.dec("tx.value", w.Tx.Value, 0)
	if w.Approval != nil {
		v.addr("approval.token", w.Approval.Token)
		v.addr("approval.spender", w.Approval.Spender)
		v.dec("approval.amount", w.Approval.Amount, 0)
	}
	v.dec("limits.maxTotalSpend", w.Limits.MaxTotalSpend, 0)
	v.dec("limits.minDelivered", w.Limits.MinDelivered, 0)
	v.optDec("limits.amountOutMinimum", w.Limits.AmountOutMinimum)
	v.optDec("limits.minBridgeValue", w.Limits.MinBridgeValue)
	if w.DeadlineSec != nil {
		v.dec("deadlineSec", *w.DeadlineSec, math.MaxUint64)
	}
	m := w.Match
	v.dec("match.toChainId", m.ToChainID, math.MaxUint64)
	v.addr("match.recipient", m.Recipient)
	v.hex("match.extraDataHash", m.ExtraHash, true)
	switch MatchKind(m.Kind) {
	case MatchBridge:
		v.addr("match.fromToken", m.FromToken)
		v.addr("match.toToken", m.ToToken)
		v.dec("match.value", m.Value, 0)
		v.dec("match.networkFee", m.NetworkFee, 0)
		v.dec("match.exFee", m.ExFee, 0)
	case MatchSwap:
		v.addr("match.tokenIn", m.TokenIn)
		v.addr("match.tokenOut", m.TokenOut)
		v.dec("match.amountIn", m.AmountIn, 0)
		v.addr("match.toToken", m.ToToken) // required for X3
	default:
		v.fail("match.kind", "unknown variant")
	}
	d := w.Details
	v.dec("details.hop1ToChainId", d.Hop1ToChainID, math.MaxUint64)
	v.addr("details.hop1ToToken", d.Hop1ToToken)
	if d.HubChain != nil {
		v.enum("details.hubChain", *d.HubChain, allChains...)
	}
	v.enum("details.destChain", d.DestChain, allChains...)
	v.dec("details.destChainId", d.DestChainID, math.MaxUint64)
	v.enum("details.hop1Kind", d.Hop1Kind, allHops...)
	if d.Hop2Kind != nil {
		v.enum("details.hop2Kind", *d.Hop2Kind, allHops...)
	}
	v.hex("details.extraData", d.ExtraData, false)
	v.optAddr("details.onHub", d.OnHub)
	v.optAddr("details.dest", d.Dest)
	v.optAddr("details.core", d.Core)
	v.optDec("details.expectedSwapOut", d.ExpectedSwapOut)
	v.optDec("details.swapAmountOut", d.SwapAmountOut)
	v.optDec("details.coreAmount", d.CoreAmount)
	v.optDec("details.coreDust", d.CoreDust)
}

// parseQuoteWire: strict decode (DisallowUnknownFields, no trailing data) + schema validation. Never panics, never indexes before it succeeds.
func parseQuoteWire(payload string) (*quoteWireV1, error) {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.DisallowUnknownFields()
	var w quoteWireV1
	if err := dec.Decode(&w); err != nil {
		return nil, &wireError{"$", err.Error()}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &wireError{"$", "trailing data after the JSON value"}
	}
	v := &validator{}
	v.quote(&w)
	if v.err != nil {
		return nil, v.err
	}
	// second pass: the route-specific required fields, consistent with the sealed calldata
	if re := routeRules(&w, restoreQuote(&w, QuoteSeal{})); re != nil {
		return nil, re
	}
	return &w, nil
}

// ---------------------------------------------------------------------------------------------------- restore: validated wire -> Quote
func bigOf(s string) *big.Int { b, _ := new(big.Int).SetString(s, 10); return b }
func optBig(s *string) *big.Int {
	if s == nil {
		return nil
	}
	return bigOf(*s)
}
func u64Of(s string) uint64          { return bigOf(s).Uint64() }
func addrOf(s string) common.Address { return common.HexToAddress(s) }
func optAddrOf(s *string) common.Address {
	if s == nil {
		return common.Address{}
	}
	return common.HexToAddress(*s)
}
func optStrOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func bytesOf(s string) []byte { b, _ := hex.DecodeString(strings.TrimPrefix(s, "0x")); return b }
func feeOf(f wireFee) FeeTriple {
	return FeeTriple{MinimumValue: bigOf(f.MinimumValue), NetworkFee: bigOf(f.NetworkFee), ExFee: bigOf(f.ExFee)}
}

func restoreQuote(w *quoteWireV1, seal QuoteSeal) *Quote {
	r := w.Request
	req := QuoteRequest{Route: Route(r.Route), Amount: bigOf(r.Amount), Sender: addrOf(r.Sender), Recipient: addrOf(r.Recipient), Hypercore: r.Hypercore,
		Hop2FeeBuffer: optBig(r.Hop2FeeBuffer), MaxTotalSpend: optBig(r.MaxTotalSpend), MinDelivered: optBig(r.MinDelivered)}
	if r.Recipients != nil {
		req.Recipients = Recipients{OnHub: optAddrOf(r.Recipients.OnHub), Dest: optAddrOf(r.Recipients.Dest), Core: optAddrOf(r.Recipients.Core)}
	}
	if r.Swap != nil {
		req.Swap.SlippageBps = clonePtr(r.Swap.SlippageBps)
		if r.Swap.DeadlineSec != nil {
			d := u64Of(*r.Swap.DeadlineSec)
			req.Swap.DeadlineSec = &d
		}
	}
	if r.X3 != nil {
		x := &X3Request{TokenOut: addrOf(r.X3.TokenOut), PoolFee: r.X3.PoolFee, ToChain: ChainKey(r.X3.ToChain)}
		if r.X3.TokenIn == "native" {
			x.NativeIn = true
		} else {
			x.TokenIn = addrOf(r.X3.TokenIn)
		}
		req.X3 = x
	}
	if r.QuoteTTLMs != nil {
		req.QuoteTTL = time.Duration(u64Of(*r.QuoteTTLMs)) * time.Millisecond
	}
	q := &Quote{
		ID: w.ID, Network: NetworkName(w.Network), Route: Route(w.Route), Request: req, Kind: QuoteKind(w.Kind), SourceChain: ChainKey(w.SourceChain), SourceChainID: int64(u64Of(w.SourceChainID)),
		SourceContract: addrOf(w.SourceContract), SourceBridge: addrOf(w.SourceBridge), Sender: addrOf(w.Sender), CreatedAt: time.UnixMilli(int64(u64Of(w.CreatedAtMs))),
		TTL: time.Duration(u64Of(w.TTLMs)) * time.Millisecond, SourceVerifier: optAddrOf(w.SourceVerifier), HubVerifier: optAddrOf(w.HubVerifier), Hop1Fees: feeOf(w.Hop1Fees),
		Hop2ExFeeDeclared: optBig(w.Hop2ExFeeDeclared), Value: bigOf(w.Value), V2: optBig(w.V2), TotalSpend: bigOf(w.TotalSpend),
		Tx:     TxData{To: addrOf(w.Tx.To), Data: bytesOf(w.Tx.Data), Value: bigOf(w.Tx.Value)},
		Limits: Limits{MaxTotalSpend: bigOf(w.Limits.MaxTotalSpend), MinDelivered: bigOf(w.Limits.MinDelivered), AmountOutMinimum: optBig(w.Limits.AmountOutMinimum), MinBridgeValue: optBig(w.Limits.MinBridgeValue)},
		Seal:   seal,
	}
	if q.Tx.Data == nil {
		q.Tx.Data = []byte{}
	}
	if w.Hop2Fees != nil {
		f := feeOf(*w.Hop2Fees)
		q.Hop2Fees = &f
	}
	if w.Approval != nil {
		q.Approval = &Approval{Token: addrOf(w.Approval.Token), Spender: addrOf(w.Approval.Spender), Amount: bigOf(w.Approval.Amount)}
	}
	if w.DeadlineSec != nil {
		q.Deadline = u64Of(*w.DeadlineSec)
	}
	m := w.Match
	q.Match = MatchSpec{Kind: MatchKind(m.Kind), ToChainID: u64Of(m.ToChainID), Recipient: addrOf(m.Recipient), ExtraHash: common.BytesToHash(bytesOf(m.ExtraHash))}
	if q.Match.Kind == MatchBridge {
		q.Match.FromToken, q.Match.ToToken = addrOf(m.FromToken), addrOf(m.ToToken)
		q.Match.Value, q.Match.NetworkFee, q.Match.ExFee = bigOf(m.Value), bigOf(m.NetworkFee), bigOf(m.ExFee)
	} else {
		q.Match.TokenIn, q.Match.TokenOut, q.Match.AmountIn = addrOf(m.TokenIn), addrOf(m.TokenOut), bigOf(m.AmountIn)
		q.Match.ToToken = addrOf(m.ToToken)
	}
	d := w.Details
	q.Details = QuoteDetails{Hop1ToChainID: u64Of(d.Hop1ToChainID), Hop1ToToken: addrOf(d.Hop1ToToken), HubChain: ChainKey(optStrOf(d.HubChain)), DestChain: ChainKey(d.DestChain),
		DestChainID: u64Of(d.DestChainID), Hop1Kind: HopKind(d.Hop1Kind), Hop2Kind: HopKind(optStrOf(d.Hop2Kind)), ExtraData: bytesOf(d.ExtraData), OnHub: optAddrOf(d.OnHub), Dest: optAddrOf(d.Dest),
		Core: optAddrOf(d.Core), ExpectedSwapOut: optBig(d.ExpectedSwapOut), SwapAmountOut: optBig(d.SwapAmountOut), CoreAmount: optBig(d.CoreAmount), CoreDust: optBig(d.CoreDust)}
	if q.Details.ExtraData == nil {
		q.Details.ExtraData = []byte{}
	}
	return q
}

// ---------------------------------------------------------------------------------------------------- verification
func sealFail(q *Quote, reason, path, detail string) error {
	v := "seal:" + reason
	if path != "" {
		v += "@" + path
	}
	return &LimitViolationError{Violated: []string{v}, Quote: q, Field: "seal", Reason: reason, Path: path, Detail: detail}
}

func firstDiff(a, b any, path string) string {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if d := firstDiff(am[k], bm[k], p); d != "" {
				return d
			}
		}
		return ""
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if bytes.Equal(ab, bb) {
		return ""
	}
	if path == "" {
		return "$"
	}
	return path
}

func jsonTree(s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var t any
	_ = dec.Decode(&t)
	return t
}

// parseSealed runs steps 1-4 (shape, version / sdk, digest, strict parse + schema) on the seal alone and returns the validated wire DTO.
func parseSealed(q *Quote) (*quoteWireV1, error) {
	if q == nil || q.Seal.Payload == "" || q.Seal.Digest == "" {
		return nil, sealFail(q, SealMissing, "", "")
	}
	s := q.Seal
	if s.V != 1 {
		return nil, sealFail(q, SealUnsupportedVersion, "", "")
	}
	if s.SDK != sealSDK {
		return nil, sealFail(q, SealUnsupportedSDK, "", "")
	}
	if !strings.EqualFold(digestOf(s.Payload), s.Digest) {
		return nil, sealFail(q, SealCorrupt, "", "")
	}
	w, err := parseQuoteWire(s.Payload)
	if err != nil {
		path := "$"
		if we, ok := err.(*wireError); ok {
			path = we.path
		}
		return nil, sealFail(q, SealInvalidPayload, path, err.Error())
	}
	return w, nil
}

// openQuote is THE one verification used by Send and by every public API that consumes a Quote (BuildTx, EnsureAllowance, Preflight, Simulate), before ANY RPC: 1 shape, 2 version / sdk, 3 digest, 4 strict parse + schema, 5 public fields vs payload,
// 6 restore into a NEW private Quote. Every failure is a *LimitViolationError{Field: "seal", Reason, Path}; nothing panics.
func openQuote(q *Quote) (*Quote, error) {
	w, err := parseSealed(q)
	if err != nil {
		return nil, err
	}
	s := q.Seal
	pubW, err := encodeQuoteWire(q)
	if err != nil {
		path := "$"
		if we, ok := err.(*wireError); ok {
			path = we.path
		}
		return nil, sealFail(q, SealModified, path, err.Error())
	}
	pub, err := canonicalJSON(pubW)
	if err != nil {
		return nil, sealFail(q, SealModified, "$", err.Error())
	}
	if pub != s.Payload {
		path := firstDiff(jsonTree(s.Payload), jsonTree(pub), "")
		if path == "" {
			path = "$"
		}
		return nil, sealFail(q, SealModified, path, "")
	}
	return restoreQuote(w, QuoteSeal{V: s.V, SDK: s.SDK, Payload: s.Payload, Digest: s.Digest}), nil
}

// ApprovedRequest returns a deep copy of the request exactly as it was sealed, parsed from the seal (the exported Request field is informational and may
// have been edited). It fails only when the seal itself is unusable (missing / unsupported / corrupt / invalid-payload).
func (q *Quote) ApprovedRequest() (QuoteRequest, error) {
	w, err := parseSealed(q)
	if err != nil {
		return QuoteRequest{}, err
	}
	return restoreQuote(w, q.Seal).Request, nil
}

// ApprovedLimits returns a deep copy of the limits exactly as they were sealed (parsed from the seal; see ApprovedRequest).
func (q *Quote) ApprovedLimits() (Limits, error) {
	w, err := parseSealed(q)
	if err != nil {
		return Limits{}, err
	}
	return restoreQuote(w, q.Seal).Limits, nil
}

// openQuote on a client: the order at every client API entry is (1) the package-level openQuote = seal + structural validation (config-free), (2) assertQuoteBelongs = the
// client binding check against THIS client's own networks config, (3) the API proceeds with the restored copy.
func (c *Client) openQuote(q *Quote) (*Quote, error) {
	r, err := openQuote(q)
	if err != nil {
		return nil, err
	}
	if err := c.assertQuoteBelongs(r); err != nil {
		return nil, err
	}
	return r, nil
}
