package bridgesdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

var finalizeStatusNames = []string{"None", "Success", "TokenPaused", "VerificationAmountThresholdExceeded", "PeriodTotalValueThresholdExceeded", "TransferFailed",
	"MintFailed", "TokenScoreOverflow", "TokenCurrentVolumeOverflow", "CrossSupplyLimitExceeded", "TokenPriceUnavailable", "InsufficientLiquidity", "InvalidRecipient"}
var forwardFailureCodes = []string{"None", "ForwardNotAttempted", "ExecutorCallReverted", "ForwardCallNotConsumed"}

var (
	tpFinalized = bridgeABI.Events["BridgeFinalized"].ID
	tpPending   = bridgeABI.Events["BridgePending"].ID
	tpExtra     = bridgeABI.Events["ExtraCallExecuted"].ID
	tpManual    = bridgeABI.Events["ManualReleased"].ID
	tpRemoved   = bridgeABI.Events["PendingRemoved"].ID
	tpFwdInit   = forwardLibABI.Events["ForwardInitiated"].ID
	tpFwdFail   = forwardLibABI.Events["ForwardFailed"].ID

	keyedTopics = []common.Hash{tpFinalized, tpPending, tpExtra, tpFwdInit, tpFwdFail, tpManual, tpRemoved}

	releaseSelectors = map[string]bool{
		fmt.Sprintf("0x%x", bridgeABI.Methods["releasePending"].ID):                    true,
		fmt.Sprintf("0x%x", bridgeABI.Methods["manualReleasePending"].ID):              true,
		fmt.Sprintf("0x%x", bridgeABI.Methods["manualReleasePendingWithRecipient"].ID): true,
	}
	finalizeBatchSelector = fmt.Sprintf("0x%x", bridgeABI.Methods["finalizeBridgeBatch"].ID)
)

type classifyInput struct {
	kind       HopKind
	logs       []RawLog
	selectorOf func(tx string) (string, error)
	receiptOf  func(tx string) (*types.Receipt, error)
	hub        common.Address
	x1OneUSD   string
	// the decoded extraData call stored in the handle. Without it an x1 / x2 hop can never be reported as success (PROCESSED_UNVERIFIED).
	x1 *X1Expect
	x2 *X2Expect
}

type classified struct {
	state     TransferState
	token     string
	amount    string
	recipient string
	reason    string
	code      string
	txHash    string
	detail    map[string]string
	hop2Index string
}

type errUnknown struct{ msg string }

func (e *errUnknown) Error() string { return e.msg }

// classifyHop is the pure determination: explicit resolution events first, then the requested hop kind.
func classifyHop(in classifyInput) (*classified, error) {
	var finalized, removed, manual, extra, fwdInit, fwdFail []types.Log
	for _, r := range in.logs {
		l := logFromRaw(r)
		if len(l.Topics) == 0 {
			continue
		}
		switch l.Topics[0] {
		case tpFinalized:
			finalized = append(finalized, l)
		case tpRemoved:
			removed = append(removed, l)
		case tpManual:
			manual = append(manual, l)
		case tpExtra:
			extra = append(extra, l)
		case tpFwdInit:
			fwdInit = append(fwdInit, l)
		case tpFwdFail:
			fwdFail = append(fwdFail, l)
		}
	}
	// step 4
	if len(removed) > 0 {
		return &classified{state: StatePendingRemoved, txHash: removed[0].TxHash.Hex(), reason: "removed by an operator without being processed"}, nil
	}
	var fin *binding.CrossBridgeBridgeFinalized
	var finLog types.Log
	if len(finalized) > 0 {
		finLog = finalized[0]
		var f binding.CrossBridgeBridgeFinalized
		if err := unpackLog(bridgeABI, "BridgeFinalized", finLog, &f); err != nil {
			return nil, err
		}
		fin = &f
		sel, err := in.selectorOf(finLog.TxHash.Hex())
		if err != nil {
			return nil, err
		}
		if releaseSelectors[sel] || len(manual) > 0 {
			return &classified{state: StateReleasedFromPending, token: f.ToToken.Hex(), amount: f.Value.String(), recipient: f.To.Hex(), txHash: finLog.TxHash.Hex(),
				reason: "released from pending: the payout is the plain token to the release recipient (extraData is not re-executed)", detail: map[string]string{"selector": sel}}, nil
		}
	}
	// step 5
	if fin == nil {
		return &classified{state: StateProcessedUnverified, reason: "index advanced and is not pending, but no keyed BridgeFinalized was found (logs unavailable, pruned or removed)"}, nil
	}
	// step 6
	sel, err := in.selectorOf(finLog.TxHash.Hex())
	if err != nil {
		return nil, err
	}
	if sel != finalizeBatchSelector {
		return &classified{state: StateProcessedUnverified, txHash: finLog.TxHash.Hex(), reason: "BridgeFinalized was emitted by an unrecognised transaction (selector " + sel + ")"}, nil
	}
	value, to, token := fin.Value.String(), fin.To.Hex(), fin.ToToken.Hex()
	switch in.kind {
	case HopPlain:
		return &classified{state: StateDelivered, token: token, amount: value, recipient: to, txHash: finLog.TxHash.Hex()}, nil
	case HopHubForward:
		if len(fwdFail) > 0 {
			var ff binding.ForwardLibForwardFailed
			if err := unpackLog(forwardLibABI, "ForwardFailed", fwdFail[0], &ff); err != nil {
				return nil, err
			}
			code := strconv.Itoa(int(ff.Code))
			if int(ff.Code) < len(forwardFailureCodes) {
				code = forwardFailureCodes[ff.Code]
			}
			return &classified{state: StateHubFallback, token: token, amount: value, recipient: to, code: code, txHash: fwdFail[0].TxHash.Hex(),
				reason: "hop 2 could not be started: native ONE was paid to the hop-1 recipient (toOnHub)"}, nil
		}
		if len(fwdInit) == 1 {
			fi := fwdInit[0]
			var fd binding.ForwardLibForwardInitiated
			if err := unpackLog(forwardLibABI, "ForwardInitiated", fi, &fd); err != nil {
				return nil, err
			}
			rec, err := in.receiptOf(fi.TxHash.Hex())
			if err != nil {
				return nil, err
			}
			if rec == nil {
				return nil, &errUnknown{fmt.Sprintf("receipt of %s unavailable", fi.TxHash)}
			}
			var cands []*big.Int
			for _, l := range rec.Logs {
				if l.Address != in.hub || l.Index >= fi.Index {
					continue
				}
				e, ok := parseInitiated(*l)
				if !ok {
					continue
				}
				if e.ToChainID.Cmp(fd.ToChainID) != 0 || e.To != fd.To || e.Value.Cmp(fd.Value) != 0 || e.NetworkFee.Cmp(fd.NetworkFee) != 0 || e.ExFee.Cmp(fd.ExFee) != 0 || hashExtraData(e.ExtraData) != common.Hash(fd.ExtraDataHash) {
					continue
				}
				between := false
				for _, o := range rec.Logs {
					if o.Address == in.hub && len(o.Topics) > 0 && o.Topics[0] == tpFwdInit && o.Index > l.Index && o.Index < fi.Index {
						between = true
					}
				}
				if !between {
					cands = append(cands, e.Index)
				}
			}
			if len(cands) == 1 {
				return &classified{state: StateForwarded, txHash: fi.TxHash.Hex(), amount: fd.Value.String(), token: token, recipient: fd.To.Hex(), hop2Index: cands[0].String(),
					detail: map[string]string{"hop2Value": fd.Value.String(), "hop2NetworkFee": fd.NetworkFee.String(), "hop2ExFee": fd.ExFee.String()}}, nil
			}
			return &classified{state: StateProcessedUnverified, txHash: fi.TxHash.Hex(), reason: fmt.Sprintf("%d candidate hop-2 BridgeInitiated events matched ForwardInitiated (expected exactly 1); not guessing", len(cands))}, nil
		}
		return &classified{state: StateProcessedUnverified, txHash: finLog.TxHash.Hex(), reason: "hub hop 1 finalized, but neither ForwardInitiated nor ForwardFailed was found for this item"}, nil
	}
	// x1 / x2: the requested kind decides; absence of the event is NOT inferred as plain delivery
	var ec *binding.CrossBridgeExtraCallExecuted
	var ecLog types.Log
	if len(extra) > 0 {
		ecLog = extra[0]
		var e binding.CrossBridgeExtraCallExecuted
		if err := unpackLog(bridgeABI, "ExtraCallExecuted", ecLog, &e); err != nil {
			return nil, err
		}
		ec = &e
	}
	unsupported := func(why string) (*classified, error) {
		return &classified{state: StateProcessedUnverified, txHash: finLog.TxHash.Hex(), reason: "unsupported extraData: " + why}, nil
	}
	callIs := func(target, selector string) bool {
		if ec == nil {
			return false
		}
		return ec.TargetContract == common.HexToAddress(target) && fmt.Sprintf("0x%x", ec.MethodID[:]) == selector
	}
	if in.kind == HopX2 {
		if in.x2 == nil {
			return unsupported("the handle carries no decoded transferToCoreFor call")
		}
		if ec != nil && ec.Success {
			if !callIs(in.x2.Token, fmt.Sprintf("0x%x", CoreSelector[:])) {
				return unsupported(fmt.Sprintf("ExtraCallExecuted target/method (%s, 0x%x) is not the expected transferToCoreFor on %s", ec.TargetContract, ec.MethodID[:], in.x2.Token))
			}
			// returnData = abi.encode(uint sent), sent = coreTransferableAmount(encodedAmount) (floored to coreUnit); consumed == sent for this token
			sent := new(big.Int).Set(ec.Consumed)
			if len(ec.ReturnData) >= 32 {
				sent = new(big.Int).SetBytes(ec.ReturnData[:32])
			}
			returned := new(big.Int)
			if fin.Value.Cmp(ec.Consumed) > 0 {
				returned.Sub(fin.Value, ec.Consumed)
			}
			return &classified{state: StateCoreDepositSubmitted, token: token, amount: sent.String(), recipient: in.x2.CoreRecipient, txHash: ecLog.TxHash.Hex(),
				detail: map[string]string{"coreRecipient": in.x2.CoreRecipient, "coreAmount": sent.String(), "encodedAmount": in.x2.EncodedAmount, "finalizedValue": value,
					"consumedEvm": ec.Consumed.String(), "returnedToDest": returned.String(), "returnedTo": to,
					"note": "coreAmount is the EVM-wei amount sent to the HyperCore system address (floored to coreUnit); the surplus (returnedToDest) was paid to BridgeFinalized.to on the destination. The HyperCore credit is asynchronous - confirm via the HyperCore info API"}}, nil
		}
		reason := "extraCall-skipped"
		if ec != nil {
			reason = "extraCall-failed"
		}
		return &classified{state: StateDeliveredFallback, token: token, amount: value, recipient: to, txHash: finLog.TxHash.Hex(), reason: reason}, nil
	}
	// x1
	if in.x1 == nil {
		return unsupported("the handle carries no decoded exactInputSingle call")
	}
	if fin.Value.Cmp(parseU(in.x1.AmountIn)) != 0 {
		return unsupported(fmt.Sprintf("finalized value %s != the decoded exactInputSingle amountIn %s", value, in.x1.AmountIn))
	}
	if ec != nil && ec.Success {
		if !callIs(in.x1.Router, fmt.Sprintf("0x%x", ExactInputSingleSelector[:])) {
			return unsupported(fmt.Sprintf("ExtraCallExecuted target/method (%s, 0x%x) is not exactInputSingle on %s", ec.TargetContract, ec.MethodID[:], in.x1.Router))
		}
		amount := "unknown"
		if len(ec.ReturnData) >= 32 {
			amount = new(big.Int).SetBytes(ec.ReturnData[:32]).String()
		}
		// the swap output goes to the decoded exactInputSingle recipient (NOT BridgeFinalized.to, which is the executor path)
		return &classified{state: StateDelivered, token: in.x1.TokenOut, amount: amount, recipient: in.x1.Recipient, txHash: ecLog.TxHash.Hex(),
			detail: map[string]string{"usdtxDelivered": value, "amountOutMinimum": in.x1.AmountOutMinimum, "bridgeFinalizedTo": to, "fee": fmt.Sprint(in.x1.Fee)}}, nil
	}
	reason := "extraCall-skipped"
	if ec != nil {
		reason = "extraCall-failed"
	}
	return &classified{state: StateDeliveredFallback, token: token, amount: value, recipient: to, txHash: finLog.TxHash.Hex(), reason: reason,
		detail: map[string]string{"note": "the swap did not run (slippage / deadline / pool); plain USDTx was paid, no loss"}}, nil
}

func scanKey(chain ChainKey, fromChainID int64, index string) string {
	return fmt.Sprintf("%s:%d:%s", chain, fromChainID, index)
}

func (c *Client) evalHop(ctx context.Context, h *TransferHandle, which int) (HopStatus, string, error) {
	ref := h.Hop1
	if which == 2 {
		ref = *h.Hop2
	}
	chain := ref.DestChainKey
	cfg := c.net.Chains[chain]
	bridge := addr(cfg.Bridge)
	fromChainID := h.SrcChainID
	if which == 2 {
		fromChainID = c.net.Chains[ChainOne].ChainID
	}
	index := parseU(ref.Index)
	base := HopStatus{Chain: chain, ChainID: cfg.ChainID, Hop: which, Index: ref.Index, State: StateUnknown}
	unknown := func(err error) (HopStatus, string, error) {
		base.State = StateUnknown
		base.Reason = fmt.Sprintf("%sRPC/log evidence unavailable: %v. Recovery: check the bridge events on %s for (fromChainID=%d, index=%s) manually, then ask support.", timeoutPrefix(err), err, chain, fromChainID, ref.Index)
		return base, "", nil
	}
	if err := c.assertChain(ctx, chain); err != nil { // EVERY error stops all evidence reads on this chain (no receipt / log / block / tx read follows)
		var nm *NetworkMismatchError
		if errors.As(err, &nm) {
			return base, "", err
		}
		base.State = StateUnknown
		base.Reason = chainUnverifiedReason(err) + fmt.Sprintf(" Recovery: check the bridge events on %s for (fromChainID=%d, index=%s) manually, then ask support.", chain, fromChainID, ref.Index)
		return base, "", nil
	}
	r, err := c.rpcFor(ctx, chain)
	if err != nil {
		return unknown(err)
	}
	// step 2: currently pending is authoritative
	var pend []*big.Int
	if err := c.withBackoff(ctx, func() error {
		out, e := c.callABI(ctx, chain, bridgeABI, bridge, "allPendingIndex", nil, u(uint64(fromChainID)))
		if e == nil {
			pend = out[0].([]*big.Int)
		}
		return e
	}); err != nil {
		return unknown(err)
	}
	for _, p := range pend {
		if p.Cmp(index) == 0 {
			base.State = StatePendingOnDest
			if out, err := c.callABI(ctx, chain, bridgeABI, bridge, "getPendingArguments", nil, u(uint64(fromChainID)), index); err == nil {
				pd := abi.ConvertType(out[0], new(binding.IBridgeRegistryPendingData)).(*binding.IBridgeRegistryPendingData)
				if int(pd.Status) < len(finalizeStatusNames) {
					base.Reason = finalizeStatusNames[pd.Status]
				}
			}
			return base, "", nil
		}
	}
	// step 3
	var next *big.Int
	if err := c.withBackoff(ctx, func() error {
		out, e := c.callABI(ctx, chain, bridgeABI, bridge, "getNextFinalizeIndex", nil, u(uint64(fromChainID)))
		if e == nil {
			next = out[0].(*big.Int)
		}
		return e
	}); err != nil {
		return unknown(err)
	}
	if next.Cmp(index) <= 0 {
		base.State = StateInFlight
		return base, "", nil
	}
	// chunked, resumable, deduped scan
	key := scanKey(chain, fromChainID, ref.Index)
	st := h.Scans[key]
	if st == nil {
		st = &ScanState{LastScanned: -1}
	}
	head, err := c.headBlock(ctx, chain)
	if err != nil {
		return unknown(err)
	}
	var start uint64
	switch {
	case st.LastScanned >= 0:
		start = uint64(st.LastScanned) + 1
	case h.ScanFrom[chain] > 0:
		start = h.ScanFrom[chain]
	default:
		lb := c.opts.ScanLookbackBlocks
		if lb == 0 {
			lb = 20000
		}
		if head > lb {
			start = head - lb
		}
	}
	if start <= head {
		topics := [][]common.Hash{keyedTopics, {common.BigToHash(u(uint64(fromChainID)))}, {common.BigToHash(index)}}
		fresh, err := c.scanLogs(ctx, chain, bridge, topics, start, head)
		if err != nil {
			return unknown(err)
		}
		have := map[string]bool{}
		for _, l := range st.Logs {
			have[fmt.Sprintf("%s:%d", l.TransactionHash, l.LogIndex)] = true
		}
		for _, l := range fresh {
			rl := rawFromLog(l)
			if !have[fmt.Sprintf("%s:%d", rl.TransactionHash, rl.LogIndex)] {
				st.Logs = append(st.Logs, rl)
			}
		}
		st.LastScanned = int64(head)
	}
	if h.Scans == nil {
		h.Scans = map[string]*ScanState{}
	}
	h.Scans[key] = st

	selCache := map[string]string{}
	selectorOf := func(hash string) (string, error) {
		if s, ok := selCache[hash]; ok {
			return s, nil
		}
		var s string
		if err := c.withBackoff(ctx, func() error {
			tx, _, e := r.TransactionByHash(ctx, common.HexToHash(hash))
			if e != nil {
				return e
			}
			if tx != nil && len(tx.Data()) >= 4 {
				s = fmt.Sprintf("0x%x", tx.Data()[:4])
			}
			return nil
		}); err != nil {
			return "", err
		}
		selCache[hash] = s
		return s, nil
	}
	receiptOf := func(hash string) (*types.Receipt, error) {
		return c.getReceipt(ctx, chain, common.HexToHash(hash))
	}
	cls, err := classifyHop(classifyInput{kind: ref.Kind, logs: st.Logs, selectorOf: selectorOf, receiptOf: receiptOf, hub: bridge, x1OneUSD: tokenAddr(c.net, ChainOne, "ONEUSD"), x1: h.Expect.X1, x2: h.Expect.X2})
	if err != nil {
		return unknown(err)
	}
	base.State, base.Token, base.Amount, base.Recipient, base.Reason, base.Code, base.TxHash, base.Detail = cls.state, cls.token, cls.amount, cls.recipient, cls.reason, cls.code, cls.txHash, cls.detail
	return base, cls.hop2Index, nil
}

// timeoutPrefix marks an UNKNOWN whose evidence call timed out (per-call or overall deadline): reason starts with "timeout: ".
// chainUnverifiedReason is the UNKNOWN reason when eth_chainId could not be verified: a timeout keeps the "timeout:" prefix, every other error is "chain-unverified:".
func chainUnverifiedReason(err error) string {
	if p := timeoutPrefix(err); p != "" {
		return p + "the RPC chain id could not be verified in time: " + err.Error() + "."
	}
	return "chain-unverified: " + err.Error() + "."
}

func timeoutPrefix(err error) string {
	var te *RpcTimeoutError
	if errors.As(err, &te) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout: "
	}
	return ""
}

func tokenAddr(n *NetworkData, ck ChainKey, tk string) string {
	if t := n.Chains[ck].Tokens[tk]; t != nil {
		return t.Address
	}
	return ""
}

var stateNotes = map[TransferState]string{
	StateSourceSubmitted:      "source transaction not mined yet",
	StateSourceFailed:         "the source transaction failed; no transfer was created",
	StateInFlight:             "waiting for the destination finalize (normally seconds to minutes; arrival times are observations, not guarantees)",
	StatePendingOnDest:        "held in the destination pending queue (e.g. verification threshold); it is released automatically or by the operator",
	StateDeliveredFallback:    "delivered as the plain token instead of the requested action (see reason); funds are safe",
	StateReleasedFromPending:  "released from pending as the plain token",
	StateCoreDepositSubmitted: "EVM-side transfer to HyperCore done; the Core credit is asynchronous",
	StateForwarded:            "hop 1 finished on the hub and hop 2 was started; tracking hop 2",
	StateHubFallback:          "hop 2 could not start; native ONE is on the hub at the hop-1 recipient (toOnHub). Bridge it onward manually (R2/R3).",
	StatePendingRemoved:       "the pending item was removed by an operator; contact support",
	StateProcessedUnverified:  "processed on chain but the evidence is missing/ambiguous; NOT reported as success. Check the explorer.",
	StateUnknown:              "could not reach evidence; see reason for manual recovery",
}

// TrackOnce evaluates the handle once. The returned Handle carries the resumable scan state (the input is not modified).
func (c *Client) TrackOnce(ctx context.Context, in *TransferHandle) (*TrackResult, error) {
	b, _ := json.Marshal(in)
	h := &TransferHandle{}
	if err := json.Unmarshal(b, h); err != nil {
		return nil, err
	}
	done := func(s TransferState, hops ...HopStatus) *TrackResult {
		res := &TrackResult{State: s, Terminal: s.IsTerminal(), Hops: hops, Handle: h, Note: stateNotes[s]}
		if s == StateUnknown { // an UNKNOWN caused by a timeout is reported as timed out
			for _, hp := range hops {
				if strings.HasPrefix(hp.Reason, "timeout:") {
					res.TimedOut = true
				}
			}
		}
		return res
	}
	src := HopStatus{Chain: h.SrcChainKey, ChainID: h.SrcChainID, Hop: 1, Index: h.Hop1.Index, State: StateSourceSubmitted, TxHash: h.SrcTxHash}
	if err := c.assertChain(ctx, h.SrcChainKey); err != nil { // EVERY error stops all evidence reads (the receipt below is never read on an unverified chain)
		var nm *NetworkMismatchError
		if errors.As(err, &nm) {
			return nil, err // mismatch: returned as is
		}
		src.State, src.Reason = StateUnknown, chainUnverifiedReason(err) // timeout => "timeout: ..." (TimedOut=true via done); any other error => "chain-unverified: ..."
		return done(StateUnknown, src), nil
	}
	r, err := c.rpcFor(ctx, h.SrcChainKey)
	if err != nil {
		return nil, err
	}
	var rec *types.Receipt
	if err := c.withBackoff(ctx, func() (e error) {
		rec, e = r.TransactionReceipt(ctx, common.HexToHash(h.SrcTxHash))
		if errors.Is(e, ethereum.NotFound) {
			rec, e = nil, nil
		}
		return
	}); err != nil {
		src.State, src.Reason = StateUnknown, timeoutPrefix(err)+"source receipt unavailable: "+err.Error()
		return done(StateUnknown, src), nil
	}
	if rec == nil {
		return done(StateSourceSubmitted, src), nil
	}
	if rec.Status != types.ReceiptStatusSuccessful {
		src.State = StateSourceFailed
		return done(StateSourceFailed, src), nil
	}
	hop1, hop2Index, err := c.evalHop(ctx, h, 1)
	if err != nil {
		return nil, err
	}
	if hop1.State != StateForwarded {
		return done(hop1.State, hop1), nil
	}
	h.Hop2.Index = hop2Index
	hop2, _, err := c.evalHop(ctx, h, 2)
	if err != nil {
		return nil, err
	}
	st := hop2.State
	if st == StateInFlight {
		st = StateForwarded
	}
	return done(st, hop1, hop2), nil
}

// TrackOpts for Track.
type TrackOpts struct {
	Timeout  time.Duration      // default 10 min; on timeout the current NON-terminal state is returned with TimedOut=true
	Poll     time.Duration      // default 5 s
	OnUpdate func(*TrackResult) // called with each evaluated result
}

// Track polls until a terminal state or the timeout. With Timeout > 0 (default 10 min) every RPC call of the track is bounded by
// min(ClientOptions.CallTimeout, time left): Track returns within Timeout + epsilon even on a hung provider, with the current
// non-terminal state and TimedOut=true, or UNKNOWN{reason "timeout: ..."} when no state was ever established.
func (c *Client) Track(ctx context.Context, h *TransferHandle, o TrackOpts) (*TrackResult, error) {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Minute
	}
	if o.Poll == 0 {
		o.Poll = 5 * time.Second
	}
	start := c.now()
	tctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	cur := h
	var last *TrackResult // the last established (non-terminal) state
	for {
		r, err := c.TrackOnce(tctx, cur)
		if err != nil {
			if last != nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
				out := *last
				out.TimedOut = true
				return &out, nil
			}
			return nil, err
		}
		cur = r.Handle
		// evidence calls timed out AFTER a state was established: report that state, not a clobbering UNKNOWN
		if last != nil && r.State == StateUnknown && r.TimedOut {
			out := *last
			out.Handle, out.TimedOut = r.Handle, true
			if o.OnUpdate != nil {
				o.OnUpdate(&out)
			}
			return &out, nil
		}
		if o.OnUpdate != nil {
			o.OnUpdate(r)
		}
		if r.Terminal {
			return r, nil
		}
		last = r
		if c.now().Sub(start) >= o.Timeout || tctx.Err() != nil {
			r.TimedOut = true
			return r, nil
		}
		if err := c.sleep(tctx, o.Poll); err != nil {
			if errors.Is(tctx.Err(), context.DeadlineExceeded) { // overall deadline reached while waiting: no extra pass with a zero budget
				r.TimedOut = true
				return r, nil
			}
			return nil, err
		}
		if c.now().Sub(start) >= o.Timeout {
			r.TimedOut = true
			return r, nil
		}
	}
}

// CheckCoreBalance is an optional, best-effort HyperCore balance lookup via the public info API (the Core credit is asynchronous).
func (c *Client) CheckCoreBalance(ctx context.Context, coreRecipient common.Address, hc *http.Client) (json.RawMessage, error) {
	api := c.net.Chains[ChainHyperEVM].CoreInfoAPI
	if api == nil {
		return nil, fmt.Errorf("HyperCore info API is not configured for this network (%s)", c.net.Chains[ChainHyperEVM].Todo["coreInfoApi"])
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	body, _ := json.Marshal(map[string]string{"type": "spotClearinghouseState", "user": coreRecipient.Hex()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *api, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var out json.RawMessage
	err = json.NewDecoder(res.Body).Decode(&out)
	return out, err
}
