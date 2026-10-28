package bridgesdk

// Structural validation (config-free). No networks configuration is read here (neither the built-in nor a client's): config equality is the client binding check (binding.go).
// Route-specific required-field rules for a sealed quote payload (second pass after the generic schema). The generic schema in seal.go checks types and
// formats; THIS pass checks that every field the SDK dereferences for the quote's route is present and consistent with the sealed calldata. It runs on the
// quote restored from the (already type-checked) payload and reports the ACTUAL wire path of the offending field (quoteWireV1: limits.amountOutMinimum,
// deadlineSec, v2, hop2Fees, hop2ExFeeDeclared, hubVerifier, details.hubChain, details.hop2Kind, tx.data, details.extraData, ...). Recipients are resolved
// with the SAME function quote() uses (resolveRecipients); extraData is decoded with the SAME decoders as handle building (checkX1Net / checkX2Net / decodeForward).

import (
	"bytes"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

// maxDurationMs is the largest ms value that fits a time.Duration without wrapping (checked BEFORE any multiplication).
const maxDurationMs = uint64(math.MaxInt64 / int64(time.Millisecond))

// ttlMs / request.quoteTtlMs must be > 0 and <= maxDurationMs; createdAtMs must be > 0 and inside the time.UnixMilli safe range.
func (v *validator) durationMs(path, s string) {
	b, ok := new(big.Int).SetString(s, 10)
	if !ok || b.Sign() <= 0 || !b.IsUint64() || b.Uint64() > maxDurationMs {
		v.fail(path, fmt.Sprintf("must be a positive millisecond count <= %d (time.Duration range)", maxDurationMs))
	}
}

func (v *validator) createdAtMs(path, s string) {
	b, ok := new(big.Int).SetString(s, 10)
	if !ok || b.Sign() <= 0 || !b.IsInt64() || time.UnixMilli(b.Int64()).UnixMilli() != b.Int64() {
		v.fail(path, "must be a positive millisecond timestamp inside the time.UnixMilli range")
	}
}

func eqAddr(a common.Address, b *string) bool { return b != nil && a == common.HexToAddress(*b) }

// routeRules returns the first violation (nil when the quote is consistent for its route).
func routeRules(w *quoteWireV1, q *Quote) *wireError {
	var out *wireError
	bad := func(path, format string, a ...any) *wireError {
		if out == nil {
			out = &wireError{path, fmt.Sprintf(format, a...)}
		}
		return out
	}
	req := q.Request
	spec, err := specFor(&req)
	if err != nil {
		return bad("request.x3", "%v", err)
	}
	switch {
	case q.Route != req.Route:
		return bad("route", "differs from request.route")
	case q.Kind != spec.kind:
		return bad("kind", "route %s is a %s quote", q.Route, spec.kind)
	case q.SourceChain != spec.src:
		return bad("sourceChain", "route %s starts on %s", q.Route, spec.src)
	case q.Details.DestChain != spec.dest:
		return bad("details.destChain", "route %s ends on %s", q.Route, spec.dest)
	case q.Sender != req.Sender:
		return bad("sender", "differs from request.sender")
	case q.Tx.To != q.SourceContract:
		return bad("tx.to", "must equal sourceContract")
	}
	onHub, dest, core := resolveRecipients(&req)
	nativeSrc := spec.srcToken == "native"
	if q.Kind == KindX3 {
		nativeSrc = req.X3.NativeIn
	}
	if nativeSrc && w.Approval != nil {
		return bad("approval", "a native source has no approval")
	}
	if !nativeSrc && w.Approval == nil {
		return bad("approval", "an ERC20 source needs an approval")
	}
	if q.Kind == KindX3 {
		return x3Rules(w, q, &req, dest)
	}

	// bridge routes: the sealed calldata must be a bridgeToken call consistent with the quote
	m := bridgeABI.Methods["bridgeToken"]
	if len(q.Tx.Data) < 4 || !bytes.Equal(q.Tx.Data[:4], m.ID) {
		return bad("tx.data", "not a bridgeToken call")
	}
	args, err := m.Inputs.Unpack(q.Tx.Data[4:])
	if err != nil || len(args) != 7 {
		return bad("tx.data", "not a bridgeToken call")
	}
	hop1To := dest
	if q.Kind == KindMultihop {
		hop1To = onHub
	}
	if q.Match.Kind != MatchBridge {
		return bad("match.kind", "a bridge route needs a bridge match")
	}
	ed := args[6].([]byte)
	switch {
	case args[0].(*big.Int).Cmp(u(q.Details.Hop1ToChainID)) != 0:
		return bad("tx.data", "toChainId differs from details.hop1ToChainId")
	case args[1].(common.Address) != q.Match.FromToken:
		return bad("tx.data", "fromToken differs from match.fromToken")
	case args[2].(common.Address) != hop1To:
		return bad("tx.data", "recipient differs from the resolved hop-1 recipient")
	case q.Match.Recipient != hop1To:
		return bad("match.recipient", "differs from the resolved hop-1 recipient")
	case args[3].(*big.Int).Cmp(q.Value) != 0 || q.Match.Value.Cmp(q.Value) != 0:
		return bad("value", "differs from the calldata / match value")
	case args[4].(*big.Int).Cmp(q.Hop1Fees.NetworkFee) != 0 || args[5].(*big.Int).Cmp(q.Hop1Fees.ExFee) != 0 || q.Match.NetworkFee.Cmp(q.Hop1Fees.NetworkFee) != 0 || q.Match.ExFee.Cmp(q.Hop1Fees.ExFee) != 0:
		return bad("hop1Fees", "differs from the calldata / match fees")
	case !bytes.Equal(ed, q.Details.ExtraData):
		return bad("details.extraData", "differs from the calldata extraData")
	case q.Match.ExtraHash != hashExtraData(ed):
		return bad("match.extraDataHash", "differs from keccak256(extraData)")
	}

	switch q.Kind {
	case KindSingle:
		if !spec.core {
			if len(ed) != 0 {
				return bad("details.extraData", "a plain route has no extraData")
			}
			return nil
		}
		if q.Details.Hop1Kind != HopX2 {
			return bad("details.hop1Kind", "X2 needs hop1Kind x2")
		}
		x2, err := decodeX2Struct(ed)
		if err != nil {
			return bad("details.extraData", "%v", err)
		}
		if bigOf(x2.EncodedAmount).Cmp(q.Value) != 0 {
			return bad("details.extraData", "encoded amount differs from the committed value")
		}
		if common.HexToAddress(x2.CoreRecipient) != core {
			return bad("details.extraData", "coreRecipient differs from the resolved core recipient")
		}
	case KindMultihop:
		switch {
		case w.V2 == nil:
			return bad("v2", "required for a multihop quote")
		case w.Hop2Fees == nil:
			return bad("hop2Fees", "required for a multihop quote")
		case w.Hop2ExFeeDeclared == nil:
			return bad("hop2ExFeeDeclared", "required for a multihop quote")
		case w.HubVerifier == nil:
			return bad("hubVerifier", "required for a multihop quote")
		case q.Details.HubChain != ChainCross:
			return bad("details.hubChain", "a multihop quote goes through the hub chain")
		case w.Details.Hop2Kind == nil:
			return bad("details.hop2Kind", "required for a multihop quote")
		}
		wantHop2 := HopPlain
		if spec.core {
			wantHop2 = HopX2
		}
		if q.Details.Hop2Kind != wantHop2 {
			return bad("details.hop2Kind", "must be %s", wantHop2)
		}
		f, err := decodeForward(ed) // the target must be the client's hub bridge (checked by the client binding check)
		if err != nil {
			return bad("details.extraData", "%v", err)
		}
		switch {
		case f.ToChainID.Cmp(u(q.Details.DestChainID)) != 0:
			return bad("details.extraData", "hop-2 destination chain differs from details.destChainId")
		case f.To != dest:
			return bad("details.extraData", "hop-2 recipient differs from the resolved destination recipient")
		case f.Value.Cmp(q.V2) != 0:
			return bad("details.extraData", "hop-2 value differs from v2")
		case f.NetworkFee.Cmp(q.Hop2Fees.NetworkFee) != 0:
			return bad("details.extraData", "hop-2 networkFee differs from hop2Fees")
		case f.ExFee.Cmp(q.Hop2ExFeeDeclared) != 0:
			return bad("details.extraData", "hop-2 exFee differs from hop2ExFeeDeclared")
		}
		if !spec.core {
			if len(f.Inner) != 0 {
				return bad("details.extraData", "inner extraData must be empty without the core option")
			}
			return nil
		}
		x2, err := decodeX2Struct(f.Inner)
		if err != nil {
			return bad("details.extraData", "%v", err)
		}
		if bigOf(x2.EncodedAmount).Cmp(q.V2) != 0 {
			return bad("details.extraData", "encoded amount differs from v2")
		}
		if common.HexToAddress(x2.CoreRecipient) != core {
			return bad("details.extraData", "coreRecipient differs from the resolved core recipient")
		}
	case KindX1:
		switch {
		case w.Limits.AmountOutMinimum == nil:
			return bad("limits.amountOutMinimum", "required for an X1 quote (0 is a valid floor)")
		case w.DeadlineSec == nil:
			return bad("deadlineSec", "required for an X1 quote")
		case new(big.Int).Mul(u(q.Deadline), big.NewInt(1000)).Cmp(big.NewInt(q.CreatedAt.UnixMilli())) <= 0:
			return bad("deadlineSec", "must be after createdAtMs / 1000")
		case q.Details.HubChain != ChainCross:
			return bad("details.hubChain", "an X1 quote goes through the hub chain")
		case q.Details.Hop1Kind != HopX1:
			return bad("details.hop1Kind", "X1 needs hop1Kind x1")
		}
		x1, p, err := decodeX1Struct(ed)
		if err != nil {
			return bad("details.extraData", "%v", err)
		}
		switch {
		case p.TokenIn != q.Details.Hop1ToToken:
			return bad("details.extraData", "exactInputSingle tokenIn differs from details.hop1ToToken")
		case p.AmountIn.Cmp(q.Value) != 0:
			return bad("details.extraData", "exactInputSingle amountIn differs from the committed value")
		case common.HexToAddress(x1.Recipient) != dest:
			return bad("details.extraData", "exactInputSingle recipient differs from the resolved destination recipient")
		case bigOf(x1.AmountOutMinimum).Cmp(q.Limits.AmountOutMinimum) != 0:
			return bad("details.extraData", "exactInputSingle amountOutMinimum differs from limits.amountOutMinimum")
		case bigOf(x1.Deadline).Cmp(u(q.Deadline)) != 0:
			return bad("details.extraData", "exactInputSingle deadline differs from deadlineSec")
		}
	}
	return nil
}

func x3Rules(w *quoteWireV1, q *Quote, req *QuoteRequest, dest common.Address) *wireError {
	bad := func(path, format string, a ...any) *wireError { return &wireError{path, fmt.Sprintf(format, a...)} }
	switch {
	case q.Match.Kind != MatchSwap:
		return bad("match.kind", "an X3 quote needs a swap match")
	case w.Limits.AmountOutMinimum == nil:
		return bad("limits.amountOutMinimum", "required for an X3 quote (0 is a valid floor)")
	case w.Limits.MinBridgeValue == nil:
		return bad("limits.minBridgeValue", "required for an X3 quote (0 is a valid floor)")
	case w.DeadlineSec == nil:
		return bad("deadlineSec", "required for an X3 quote")
	}
	x3 := req.X3
	name := "swapBridgeExactInputSingle"
	if x3.NativeIn {
		name = "swapBridgeExactInputSingleETH"
	}
	m := sbrABI.Methods[name]
	if len(q.Tx.Data) < 4 || !bytes.Equal(q.Tx.Data[:4], m.ID) {
		return bad("tx.data", "unexpected swap function")
	}
	out, err := m.Inputs.Unpack(q.Tx.Data[4:])
	if err != nil || len(out) != 2 {
		return bad("tx.data", "not a SwapBridgeRouter call")
	}
	p := abi.ConvertType(out[0], new(binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)).(*binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)
	val := new(big.Int)
	if x3.NativeIn {
		val = req.Amount
	}
	switch {
	case !x3.NativeIn && p.TokenIn != x3.TokenIn: // native: tokenIn is the client's WCROSS (client binding check)
		return bad("tx.data", "tokenIn differs from request.x3.tokenIn")
	case p.TokenOut != x3.TokenOut:
		return bad("tx.data", "tokenOut differs from request.x3.tokenOut")
	case !p.Fee.IsUint64() || p.Fee.Uint64() != uint64(x3.PoolFee):
		return bad("tx.data", "fee differs from request.x3.poolFee")
	case p.AmountIn.Cmp(req.Amount) != 0:
		return bad("tx.data", "amountIn differs from request.amount")
	case p.AmountOutMinimum.Cmp(q.Limits.AmountOutMinimum) != 0:
		return bad("tx.data", "amountOutMinimum differs from limits.amountOutMinimum")
	case p.BridgeParams.ToChainID.Cmp(u(q.Details.DestChainID)) != 0:
		return bad("tx.data", "toChainID differs from details.destChainId")
	case p.BridgeParams.Recipient != dest:
		return bad("tx.data", "recipient differs from the resolved destination recipient")
	case len(p.BridgeParams.ExtraData) != 0:
		return bad("tx.data", "X3 supports extraData = 0x only")
	case out[1].(*big.Int).Cmp(u(q.Deadline)) != 0:
		return bad("tx.data", "deadline differs from deadlineSec")
	case q.Tx.Value.Cmp(val) != 0:
		return bad("tx.value", "must equal amountIn for a native input, 0 otherwise")
	case q.Match.AmountIn.Cmp(req.Amount) != 0:
		return bad("match.amountIn", "differs from request.amount")
	case q.Match.Recipient != dest:
		return bad("match.recipient", "differs from the resolved destination recipient")
	case q.Match.ExtraHash != hashExtraData(nil):
		return bad("match.extraDataHash", "differs from keccak256(0x)")
	}
	return nil
}
