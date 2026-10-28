package bridgesdk

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// HandleOptions for HandleFromTx. All fields are optional.
type HandleOptions struct {
	// Route is verified against the route decoded from the receipt (contradiction => *RouteMismatchError).
	Route Route
	// Index selects the candidate whose BridgeInitiated index equals this; LogIndex selects by log index.
	Index    *big.Int
	LogIndex *uint
	// ScanFrom: destination block numbers recorded at send time (tracking scan start), if known.
	ScanFrom map[ChainKey]uint64
}

type inferred struct {
	route     Route
	hypercore bool
	x3To      ChainKey
	logIndex  uint
}

type candidate struct {
	x3    bool
	event *parsedInitiated
}

// inferFromReceipt: deterministic route inference from the SOURCE receipt only (never from absent events).
// Trust rule: only logs emitted by the configured source bridge (and, for X3, the configured SwapBridgeRouter) of srcKey are candidates;
// a log with identical topics from any other address is ignored. X3 additionally needs the router's SwapBridge log and the bridge's
// BridgeInitiated (from = router) in the same receipt, consistent with each other.
func (c *Client) inferFromReceipt(srcKey ChainKey, rec *types.Receipt, o HandleOptions) (*inferred, error) {
	src := c.net.Chains[srcKey]
	if src == nil || src.Bridge == nil {
		return nil, &UnsupportedRouteError{Msg: fmt.Sprintf("the source bridge of %s is not configured", srcKey)}
	}
	srcBridge := addr(src.Bridge)
	var router common.Address
	if srcKey == ChainCross {
		if sw := c.net.Chains[ChainCross].Swap; sw != nil && sw.SwapBridgeRouter != nil {
			router = addr(sw.SwapBridgeRouter)
		}
	}
	var bridgeLogs []*parsedInitiated
	for _, l := range rec.Logs {
		if l == nil || len(l.Topics) == 0 || l.Address != srcBridge || l.Topics[0] != topicBridgeInitiated {
			continue
		}
		if p, ok := parseInitiated(*l); ok {
			bridgeLogs = append(bridgeLogs, p)
		}
	}
	// X3 candidates come from the structural, intent-free link (linkSwapTransfer); handleFromTx has NO intent, so nothing is compared with a quote
	var cands []candidate
	paired := map[uint]bool{}
	for _, t := range linkSwapTransfer(rec, router, srcBridge).linked {
		paired[t.bi.LogIndex] = true
		cands = append(cands, candidate{x3: true, event: t.bi})
	}
	for _, e := range bridgeLogs {
		if paired[e.LogIndex] || (router != (common.Address{}) && e.From == router) {
			continue // router-origin transfer without a consistent SwapBridge: not a candidate
		}
		cands = append(cands, candidate{event: e})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].event.LogIndex < cands[j].event.LogIndex })
	if len(cands) == 0 {
		return nil, &SourceTransferNotFoundError{Msg: fmt.Sprintf("no BridgeInitiated emitted by the configured source bridge %s (%s) with a consistent X3 pairing in the receipt", srcBridge, srcKey)}
	}
	info := func(cs []candidate) []SourceCandidateInfo {
		out := make([]SourceCandidateInfo, 0, len(cs))
		for _, x := range cs {
			out = append(out, SourceCandidateInfo{ToChainID: x.event.ToChainID.Uint64(), Index: x.event.Index, LogIndex: x.event.LogIndex})
		}
		return out
	}
	pool := cands
	if o.Index != nil {
		var f []candidate
		for _, x := range pool {
			if x.event.Index.Cmp(o.Index) == 0 {
				f = append(f, x)
			}
		}
		pool = f
	}
	if o.LogIndex != nil {
		var f []candidate
		for _, x := range pool {
			if x.event.LogIndex == *o.LogIndex {
				f = append(f, x)
			}
		}
		pool = f
	}
	if len(pool) == 0 {
		return nil, &SourceTransferNotFoundError{Msg: fmt.Sprintf("the selector matches none of the candidates: %v", info(cands))}
	}
	if len(pool) > 1 {
		return nil, &AmbiguousSourceError{Msg: "several candidate BridgeInitiated events in the receipt; refusing to pick one. Pass HandleOptions.Index or LogIndex.", Candidates: info(pool)}
	}
	chosen := pool[0]
	e := chosen.event
	destKey := chainKeyByID(c.net, e.ToChainID.Int64())
	unsupported := func(why, target, selector string) error {
		msg := why
		if target != "" {
			msg += fmt.Sprintf(" (target %s, selector %s)", target, selector)
		}
		return &UnsupportedRouteError{Msg: msg, Target: target, Selector: selector}
	}
	out := &inferred{logIndex: e.LogIndex}
	switch {
	case chosen.x3:
		if destKey != ChainBSC && destKey != ChainHyperEVM {
			return nil, unsupported(fmt.Sprintf("X3 destination chain %s is not a configured route destination", e.ToChainID), "", "")
		}
		if len(e.ExtraData) > 0 {
			return nil, unsupported("X3 with non-empty extraData is not supported by this SDK", "", "")
		}
		out.route, out.x3To = RouteSwapBridge, destKey
	case len(e.ExtraData) == 0:
		plain := map[string]Route{"bsc>cross": RouteBSCToOne, "cross>bsc": RouteOneToBSC, "cross>hyperevm": RouteOneToHyperEVM, "hyperevm>cross": RouteHyperEVMToOne}
		r, ok := plain[string(srcKey)+">"+string(destKey)]
		if destKey == "" || !ok {
			return nil, unsupported(fmt.Sprintf("no plain route from %s to chain %s", srcKey, e.ToChainID), "", "")
		}
		out.route = r
	default:
		if len(e.ExtraData) < 24 {
			return nil, unsupported("extraData is shorter than target(20 B) + selector(4 B)", "", "")
		}
		target := common.BytesToAddress(e.ExtraData[:20])
		var sel [4]byte
		copy(sel[:], e.ExtraData[20:24])
		selHex := fmt.Sprintf("0x%x", sel)
		cross := c.net.Chains[ChainCross]
		var hub, v3, heTok common.Address
		if cross.Bridge != nil {
			hub = addr(cross.Bridge)
		}
		if cross.Swap != nil && cross.Swap.V3SwapRouter != nil {
			v3 = addr(cross.Swap.V3SwapRouter)
		}
		if t := c.net.Chains[ChainHyperEVM].Tokens["ONE"]; t != nil {
			heTok = common.HexToAddress(t.Address)
		}
		switch {
		case hub != (common.Address{}) && target == hub && sel == ForwardSelector:
			if destKey != ChainCross {
				return nil, unsupported("a hub forward must be sent to the hub chain", target.Hex(), selHex)
			}
			fwd, err := decodeForward(e.ExtraData)
			if err != nil {
				return nil, unsupported("undecodable bridgeTokenForwarded call: "+err.Error(), target.Hex(), selHex)
			}
			dest := chainKeyByID(c.net, fwd.ToChainID.Int64())
			multi := map[string]Route{"bsc>hyperevm": RouteBSCToOneToHyperEVM, "hyperevm>bsc": RouteHyperEVMToOneToBSC}
			r, ok := multi[string(srcKey)+">"+string(dest)]
			if dest == "" || !ok {
				return nil, unsupported(fmt.Sprintf("hop-2 destination chain %s is not a configured route destination for a multihop from %s", fwd.ToChainID, srcKey), target.Hex(), selHex)
			}
			out.route = r
			if len(fwd.Inner) > 0 {
				if r == RouteBSCToOneToHyperEVM && heTok != (common.Address{}) && len(fwd.Inner) >= 24 && common.BytesToAddress(fwd.Inner[:20]) == heTok && [4]byte(fwd.Inner[20:24]) == CoreSelector {
					if _, err := c.checkX2(fwd.Inner, fwd.Value); err != nil {
						return nil, err
					}
					out.hypercore = true
				} else {
					it, is := "", ""
					if len(fwd.Inner) >= 24 {
						it, is = common.BytesToAddress(fwd.Inner[:20]).Hex(), fmt.Sprintf("0x%x", fwd.Inner[20:24])
					}
					return nil, unsupported("unsupported nested hop-2 extraData", it, is)
				}
			}
		case v3 != (common.Address{}) && target == v3 && sel == ExactInputSingleSelector && srcKey == ChainBSC && destKey == ChainCross:
			if _, err := c.checkX1(e.ExtraData, e.Value); err != nil {
				return nil, err
			}
			out.route = RouteBSCUSDTToONEUSD
		case heTok != (common.Address{}) && target == heTok && sel == CoreSelector && srcKey == ChainCross && destKey == ChainHyperEVM:
			if _, err := c.checkX2(e.ExtraData, e.Value); err != nil {
				return nil, err
			}
			out.route = RouteOneToHyperCore
		default:
			return nil, unsupported("the extraData target/selector maps to no supported route", target.Hex(), selHex)
		}
	}
	if o.Route != "" && o.Route != out.route {
		return nil, &RouteMismatchError{Given: o.Route, Inferred: out.route}
	}
	return out, nil
}

// HandleFromTx builds a TransferHandle from a source tx hash on chain srcChainID. The route is decoded from the source receipt's
// BridgeInitiated (emitted by the configured source bridge / router only) - never from absent events; opts.Route is optional and verified.
// Several candidate transfers in one receipt => *AmbiguousSourceError; select with opts.Index / opts.LogIndex.
func (c *Client) HandleFromTx(ctx context.Context, srcChainID int64, txHash common.Hash, opts *HandleOptions) (*TransferHandle, error) {
	var o HandleOptions
	if opts != nil {
		o = *opts
	}
	key := chainKeyByID(c.net, srcChainID)
	if key == "" {
		return nil, fmt.Errorf("chain %d is not part of %s", srcChainID, c.network)
	}
	if err := c.assertChain(ctx, key); err != nil {
		return nil, err
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	rec, err := r.TransactionReceipt(ctx, txHash)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("no receipt for %s on %s yet (SOURCE_SUBMITTED): %v", txHash, key, err)
	}
	if rec.Status != types.ReceiptStatusSuccessful {
		return nil, &SourceTxFailedError{Hash: txHash} // SOURCE_FAILED
	}
	inf, err := c.inferFromReceipt(key, rec, o)
	if err != nil {
		return nil, err
	}
	li := inf.logIndex
	return c.buildHandle(handleInput{route: inf.route, srcKey: key, receipt: rec, hypercore: inf.hypercore, x3To: inf.x3To, logIndex: &li, scanFrom: o.ScanFrom})
}
