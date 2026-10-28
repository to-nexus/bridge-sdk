package bridgesdk

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// All SDK errors are pointer types so errors.As(err, &target) works.

type NetworkMismatchError struct {
	Scope    string
	Expected int64
	Actual   string
	// ExpectedValue is set instead of Expected when the mismatching field is not a chain id (quote -> client binding: network name,
	// configured contract address); Scope names the field (for example "quote.tx.to").
	ExpectedValue string
}

func (e *NetworkMismatchError) Error() string {
	if e.ExpectedValue != "" {
		return fmt.Sprintf("network mismatch (%s): this client is configured for %s, the quote carries %s. Nothing was sent.", e.Scope, e.ExpectedValue, e.Actual)
	}
	return fmt.Sprintf("network mismatch (%s): expected chainId %d, got %s. Nothing was sent.", e.Scope, e.Expected, e.Actual)
}

type AddressNotConfiguredError struct {
	Route Route
	Field string
	Todo  string
}

func (e *AddressNotConfiguredError) Error() string {
	return fmt.Sprintf("route %s is not available on this network: %s is not configured (%s)", e.Route, e.Field, e.Todo)
}

type QuoteExpiredError struct{ AgeMs, TTLMs int64 }

func (e *QuoteExpiredError) Error() string {
	return fmt.Sprintf("quote expired (%d ms old, ttl %d ms). Request a new quote.", e.AgeMs, e.TTLMs)
}

type LimitViolationError struct {
	Violated []string
	Quote    *Quote
	// Quote seal failures: Field == "seal", Reason one of missing | unsupported-version | unsupported-sdk | corrupt | invalid-payload | modified,
	// Path = the offending JSON path (invalid-payload / modified).
	Field, Reason, Path, Detail string
}

func (e *LimitViolationError) Error() string {
	if e.Field == "seal" {
		s := "quote seal rejected (" + e.Reason
		if e.Path != "" {
			s += " at " + e.Path
		}
		if e.Detail != "" {
			s += ": " + e.Detail
		}
		return s + "). Quotes are sealed: edit the request by calling Quote() again, not by editing the object. Nothing was sent."
	}
	return "quote violates the requested limits: " + strings.Join(e.Violated, ", ")
}

// RequoteRequiredError: a re-quote after approve would loosen an approved limit. Nothing was sent.
type RequoteRequiredError struct {
	OldQuote *Quote
	NewQuote *Quote
	Violated []string
}

func (e *RequoteRequiredError) Error() string {
	return "re-quote would loosen an approved limit (" + strings.Join(e.Violated, ", ") + "). Nothing was sent; review NewQuote and call Quote again."
}

// PreflightError: a blocking preflight check failed. Send returns it (Stage "pre-approve" before any approve, "post-approve" after the
// approve + re-quote, immediately before the simulation); nothing further is signed or broadcast. Quote also returns it for
// unquotable routes (Stage empty).
type PreflightError struct {
	Issues []PreflightIssue
	Stage  string
}

func (e *PreflightError) Error() string {
	codes := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		codes = append(codes, i.Code)
	}
	st := ""
	if e.Stage != "" {
		st = " (" + e.Stage + ")"
	}
	return "preflight failed" + st + ": " + strings.Join(codes, ", ")
}

// SignerMisbehaviorError: the signed transaction is not the one that was requested (not replay-protected, another chain id, another
// sender, or a changed field). Nothing was broadcast.
type SignerMisbehaviorError struct{ Field, Detail string }

func (e *SignerMisbehaviorError) Error() string {
	return fmt.Sprintf("signer misbehavior (%s): %s. Nothing was broadcast.", e.Field, e.Detail)
}

// RpcTimeoutError: one RPC call exceeded its per-call timeout (ClientOptions.CallTimeout) or the overall deadline. After a broadcast it is
// never surfaced on its own: Send wraps it in *BroadcastOutcomeUnknownError (errors.As finds both).
type RpcTimeoutError struct {
	Method  string
	Timeout time.Duration
}

func (e *RpcTimeoutError) Error() string {
	return fmt.Sprintf("RPC %s timed out after %s", e.Method, e.Timeout)
}

type SimulationRevertError struct {
	Decoded *DecodedError
	Data    []byte
	Cause   error
}

func (e *SimulationRevertError) Error() string {
	if e.Decoded != nil {
		return "simulation reverted: " + e.Decoded.String() + ". Nothing was sent."
	}
	return fmt.Sprintf("simulation reverted: 0x%x (%v). Nothing was sent.", e.Data, e.Cause)
}
func (e *SimulationRevertError) Unwrap() error { return e.Cause }

type ApproveFailedError struct {
	Msg   string
	Hash  common.Hash
	Cause error
}

func (e *ApproveFailedError) Error() string { return "approve failed: " + e.Msg }
func (e *ApproveFailedError) Unwrap() error { return e.Cause }

type SigningError struct{ Cause error }

func (e *SigningError) Error() string {
	return "signing failed; nothing was broadcast: " + e.Cause.Error()
}
func (e *SigningError) Unwrap() error { return e.Cause }

type SenderMismatchError struct{ Expected, Actual common.Address }

func (e *SenderMismatchError) Error() string {
	return fmt.Sprintf("signer address %s does not match the quote sender %s. Nothing was signed.", e.Actual, e.Expected)
}

type SourceTxFailedError struct{ Hash common.Hash }

func (e *SourceTxFailedError) Error() string {
	return fmt.Sprintf("source transaction %s failed (status 0). No bridge transfer was created.", e.Hash)
}

type WaitTimeoutError struct{ Hash common.Hash }

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf("timed out waiting for the receipt of %s; the tx may still be mined. Keep tracking with the hash.", e.Hash)
}

// BroadcastOutcomeUnknownError: the tx may or may not be on chain. DO NOT resend.
type BroadcastOutcomeUnknownError struct {
	Intent     *SubmittedIntent
	Checkpoint Checkpoint
	Hash       common.Hash
	Nonce      uint64
	Raw        []byte
	Cause      error
}

func (e *BroadcastOutcomeUnknownError) Error() string {
	return "broadcast outcome unknown: the transaction may or may not be on chain. DO NOT resend. Use Client.FindSourceTransfer(err.Intent, err.Checkpoint) or Client.Rebroadcast(err.Raw). Cause: " + e.Cause.Error()
}
func (e *BroadcastOutcomeUnknownError) Unwrap() error { return e.Cause }

// SourceCandidate describes one candidate BridgeInitiated of a source receipt.
type SourceCandidateInfo struct {
	ToChainID uint64
	Index     *big.Int
	LogIndex  uint
}

// AmbiguousSourceError: several candidate transfers in one receipt; never auto-selected. Pick one with HandleOptions.Index / LogIndex.
type AmbiguousSourceError struct {
	Msg        string
	Candidates []SourceCandidateInfo
}

func (e *AmbiguousSourceError) Error() string {
	s := e.Msg
	if len(e.Candidates) > 0 {
		s += " Candidates (toChainID, index, logIndex):"
		for _, c := range e.Candidates {
			s += fmt.Sprintf(" (%d, %s, %d)", c.ToChainID, c.Index, c.LogIndex)
		}
	}
	return s
}

// SourceTransferNotFoundError: no (consistent) BridgeInitiated from the configured source bridge / router, or the selector matched none.
type SourceTransferNotFoundError struct{ Msg string }

func (e *SourceTransferNotFoundError) Error() string { return e.Msg }

// UnsupportedRouteError: the extraData target/selector (or nested hop-2 destination) maps to no supported route.
type UnsupportedRouteError struct {
	Msg      string
	Target   string
	Selector string
}

func (e *UnsupportedRouteError) Error() string { return e.Msg }

// RouteMismatchError: the caller-supplied route contradicts the route decoded from the source receipt.
type RouteMismatchError struct{ Given, Inferred Route }

func (e *RouteMismatchError) Error() string {
	return fmt.Sprintf("route %s contradicts the transfer in the source receipt, which decodes as %s", e.Given, e.Inferred)
}

// InvalidOptionError: invalid swap option (slippageBps outside [0, 10000), deadlineSec == 0).
type InvalidOptionError struct{ Msg string }

func (e *InvalidOptionError) Error() string { return e.Msg }

// ---- revert-data decoding -------------------------------------------------------------------------------------

//go:embed errors_table.json
var errorsTableJSON []byte

type errorTableEntry struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Inputs    []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"inputs"`
	Hint *string `json:"hint"`
}

var (
	errTableOnce sync.Once
	errTable     map[string]errorTableEntry
)

type DecodedError struct {
	Selector  string
	Name      string
	Signature string
	Args      map[string]interface{}
	Hint      string
}

func (d *DecodedError) String() string {
	parts := []string{}
	for k, v := range d.Args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	s := d.Name + "(" + strings.Join(parts, ", ") + ")"
	if d.Hint != "" {
		s += " - " + d.Hint
	}
	return s
}

// DecodeBridgeError decodes revert data into {name, args, hint}; unknown selectors return nil.
func DecodeBridgeError(data []byte) *DecodedError {
	if len(data) < 4 {
		return nil
	}
	errTableOnce.Do(func() { _ = json.Unmarshal(errorsTableJSON, &errTable) })
	sel := fmt.Sprintf("0x%x", data[:4])
	switch sel {
	case "0x08c379a0":
		if msg, err := abi.UnpackRevert(data); err == nil {
			return &DecodedError{Selector: sel, Name: "Error", Signature: "Error(string)", Args: map[string]interface{}{"message": msg}, Hint: msg}
		}
		return nil
	case "0x4e487b71":
		return &DecodedError{Selector: sel, Name: "Panic", Signature: "Panic(uint256)", Args: map[string]interface{}{}, Hint: "solidity panic"}
	}
	e, ok := errTable[sel]
	if !ok {
		return nil
	}
	d := &DecodedError{Selector: sel, Name: e.Name, Signature: e.Signature, Args: map[string]interface{}{}}
	if e.Hint != nil {
		d.Hint = *e.Hint
	}
	args := abi.Arguments{}
	for i, in := range e.Inputs {
		t, err := abi.NewType(in.Type, "", nil)
		if err != nil {
			return d
		}
		name := in.Name
		if name == "" {
			name = fmt.Sprintf("arg%d", i)
		}
		args = append(args, abi.Argument{Name: name, Type: t})
	}
	if len(args) > 0 && len(data) >= 4 {
		if vals, err := args.Unpack(data[4:]); err == nil {
			for i, a := range args {
				d.Args[a.Name] = vals[i]
			}
		}
	}
	return d
}

func bigStr(b *big.Int) string {
	if b == nil {
		return "0"
	}
	return b.String()
}

func selectorOf(sig string) [4]byte {
	var out [4]byte
	copy(out[:], crypto.Keccak256([]byte(sig))[:4])
	return out
}
