package bridgesdk

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

type routeShape struct {
	src      ChainKey
	hub      ChainKey
	dest     ChainKey
	hop1Dest ChainKey
	hop1Kind HopKind
	hop2Kind HopKind
	swap     bool
}

func shapeFor(route Route, hypercore bool, x3To ChainKey) (routeShape, error) {
	switch route {
	case RouteBSCToOne:
		return routeShape{ChainBSC, "", ChainCross, ChainCross, HopPlain, "", false}, nil
	case RouteOneToBSC:
		return routeShape{ChainCross, "", ChainBSC, ChainBSC, HopPlain, "", false}, nil
	case RouteOneToHyperEVM:
		return routeShape{ChainCross, "", ChainHyperEVM, ChainHyperEVM, HopPlain, "", false}, nil
	case RouteOneToHyperCore:
		return routeShape{ChainCross, "", ChainHyperEVM, ChainHyperEVM, HopX2, "", false}, nil
	case RouteHyperEVMToOne:
		return routeShape{ChainHyperEVM, "", ChainCross, ChainCross, HopPlain, "", false}, nil
	case RouteBSCToOneToHyperEVM:
		k := HopPlain
		if hypercore {
			k = HopX2
		}
		return routeShape{ChainBSC, ChainCross, ChainHyperEVM, ChainCross, HopHubForward, k, false}, nil
	case RouteHyperEVMToOneToBSC:
		return routeShape{ChainHyperEVM, ChainCross, ChainBSC, ChainCross, HopHubForward, HopPlain, false}, nil
	case RouteBSCUSDTToONEUSD:
		return routeShape{ChainBSC, ChainCross, ChainCross, ChainCross, HopX1, "", false}, nil
	case RouteSwapBridge:
		if x3To == "" {
			x3To = ChainBSC
		}
		return routeShape{ChainCross, ChainCross, x3To, x3To, HopPlain, "", true}, nil
	}
	return routeShape{}, fmt.Errorf("unknown route %q", route)
}

var (
	topicBridgeInitiated = bridgeABI.Events["BridgeInitiated"].ID
	topicSwapBridge      = sbrABI.Events["SwapBridge"].ID
)

type parsedInitiated struct {
	binding.CrossBridgeBridgeInitiated
	LogIndex uint
	Address  common.Address
}

func parseInitiated(l types.Log) (*parsedInitiated, bool) {
	if len(l.Topics) == 0 || l.Topics[0] != topicBridgeInitiated {
		return nil, false
	}
	var e binding.CrossBridgeBridgeInitiated
	if err := unpackLog(bridgeABI, "BridgeInitiated", l, &e); err != nil {
		return nil, false
	}
	return &parsedInitiated{CrossBridgeBridgeInitiated: e, LogIndex: l.Index, Address: l.Address}, true
}

// matchesBridge: does a BridgeInitiated event correspond to the (adopted) bridge intent?
// Every non-fee field must match exactly: toChainID, fromToken, toToken, from (when from != zero), to, value, keccak(extraData).
// Fees match when event.fee <= intent.fee for BOTH networkFee and exFee. Reason (BaseBridge invariant): bridgeToken only accepts a
// tx when networkFee >= calc && exFee >= calc (_checkInitiateAmount, src/BaseBridge.sol) and then emits the verifier's CALCULATED
// values, so for any successful transfer the event fees are always <= the calldata fees (lower when fees dropped between
// quote/adoption and inclusion). Strict equality would miss such a transfer and invite a duplicate send.
// (Hop-2 / ForwardInitiated matching is a different rule - the hub re-settles those fees.)
func matchesBridge(m *MatchSpec, e *parsedInitiated, from common.Address) bool {
	if m == nil || m.Kind != MatchBridge {
		return true
	}
	return e.ToChainID.Uint64() == m.ToChainID && e.FromToken == m.FromToken && e.ToToken == m.ToToken &&
		(from == (common.Address{}) || e.From == from) && e.To == m.Recipient &&
		e.Value.Cmp(m.Value) == 0 && e.NetworkFee.Cmp(m.NetworkFee) <= 0 && e.ExFee.Cmp(m.ExFee) <= 0 && hashExtraData(e.ExtraData) == m.ExtraHash
}

// SwapTransfer is one X3 transfer decoded from the router's SwapBridge log and the bridge's BridgeInitiated log it is LINKED to (structural, intent-free).
type SwapTransfer struct {
	User          common.Address
	TokenIn       common.Address
	TokenOut      common.Address
	AmountIn      *big.Int
	AmountOut     *big.Int
	ToChainID     *big.Int
	ToToken       common.Address // BridgeInitiated.toToken (what the BRIDGE emitted)
	Recipient     common.Address // BridgeInitiated.to
	ExtraDataHash common.Hash    // keccak256(BridgeInitiated.extraData)
	Index         *big.Int
	Value         *big.Int
	SwapLogIndex  uint
	SwapEmitter   common.Address
	bi            *parsedInitiated
}

type unlinkedSwap struct {
	LogIndex uint
	Emitter  common.Address
}

type swapLinkResult struct {
	linked   []SwapTransfer
	unlinked []unlinkedSwap
	consumed map[uint]bool // logIndex of every BridgeInitiated consumed by a link: not a separate transfer
}

// linkSwapTransfer is the STRUCTURAL, INTENT-FREE link of the router's SwapBridge logs to the bridge's BridgeInitiated logs of ONE receipt. A SwapBridge log
// (topic0, emitted by the configured router) is linked when the same receipt holds a BridgeInitiated emitted by the configured bridge with from == router,
// index == SwapBridge.initiateIndex, toChainID == SwapBridge.toChainID, fromToken == SwapBridge.tokenOut, value == SwapBridge.bridgeValue and
// value + networkFee + exFee <= SwapBridge.amountOut (the router refunds floor dust). It compares NOTHING with a quote: actual fees and output are decided at
// inclusion and may legitimately be higher than quoted. A SwapBridge log without a consistent link is `unlinked` (unattributed evidence).
func linkSwapTransfer(rec *types.Receipt, router, bridge common.Address) swapLinkResult {
	res := swapLinkResult{consumed: map[uint]bool{}}
	var initiated []*parsedInitiated
	for _, l := range rec.Logs {
		if l == nil || len(l.Topics) == 0 || l.Address != bridge || l.Topics[0] != topicBridgeInitiated {
			continue
		}
		if p, ok := parseInitiated(*l); ok {
			initiated = append(initiated, p)
		}
	}
	for _, l := range rec.Logs {
		if l == nil || len(l.Topics) == 0 || l.Topics[0] != topicSwapBridge {
			continue
		}
		var s binding.SwapBridgeRouterSwapBridge
		if router == (common.Address{}) || l.Address != router || unpackLog(sbrABI, "SwapBridge", *l, &s) != nil {
			res.unlinked = append(res.unlinked, unlinkedSwap{LogIndex: l.Index, Emitter: l.Address})
			continue
		}
		var link *parsedInitiated
		for _, e := range initiated {
			if res.consumed[e.LogIndex] || e.Index.Cmp(s.InitiateIndex) != 0 || e.From != router || e.ToChainID.Cmp(s.ToChainID) != 0 || e.FromToken != s.TokenOut ||
				e.Value.Cmp(s.BridgeValue) != 0 || add(e.Value, e.NetworkFee, e.ExFee).Cmp(s.AmountOut) > 0 {
				continue
			}
			link = e
			break
		}
		if link == nil {
			res.unlinked = append(res.unlinked, unlinkedSwap{LogIndex: l.Index, Emitter: l.Address})
			continue
		}
		res.consumed[link.LogIndex] = true
		res.linked = append(res.linked, SwapTransfer{User: s.User, TokenIn: s.TokenIn, TokenOut: s.TokenOut, AmountIn: s.AmountIn, AmountOut: s.AmountOut, ToChainID: s.ToChainID,
			ToToken: link.ToToken, Recipient: link.To, ExtraDataHash: hashExtraData(link.ExtraData), Index: link.Index, Value: link.Value, SwapLogIndex: l.Index, SwapEmitter: l.Address, bi: link})
	}
	return res
}

// matchesSwapIntent: does a LINKED X3 transfer correspond to the (adopted) intent? user == from (when given); tokenIn, tokenOut, amountIn and toChainID equal;
// recipient equals the intent's resolved destination recipient; the linked extraData hash is keccak256(empty) (X3 user extraData is unsupported); the
// BridgeInitiated.toToken equals the STORED expected destination token (m.ToToken, from the hub pair at quote time - never the hub tokenOut and never the
// event's own value); and amountOut >= amountOutMinimum when known (consistency only; the chain enforces it). NO upper bound on fees or output.
func matchesSwapIntent(t *SwapTransfer, m *MatchSpec, from common.Address, amountOutMinimum *big.Int) bool {
	return (from == (common.Address{}) || t.User == from) &&
		t.TokenIn == m.TokenIn && t.TokenOut == m.TokenOut && t.AmountIn.Cmp(m.AmountIn) == 0 && t.ToChainID.Uint64() == m.ToChainID &&
		t.Recipient == m.Recipient && t.ExtraDataHash == hashExtraData(nil) && t.ToToken == m.ToToken &&
		(amountOutMinimum == nil || t.AmountOut.Cmp(amountOutMinimum) >= 0)
}

type handleInput struct {
	route     Route
	srcKey    ChainKey
	receipt   *types.Receipt
	hypercore bool
	x3To      ChainKey
	sender    common.Address
	match     *MatchSpec
	// amountOutMinimum: X3 intent consistency (amountOut >= amountOutMinimum), from the adopted quote's limits
	amountOutMinimum *big.Int
	initiateIndex    *big.Int
	// logIndex selects exactly one BridgeInitiated log (set by route inference; X3: the bridge log paired with the SwapBridge event)
	logIndex *uint
	scanFrom map[ChainKey]uint64
}

func (c *Client) buildHandle(h handleInput) (*TransferHandle, error) {
	shape, err := shapeFor(h.route, h.hypercore, h.x3To)
	if err != nil {
		return nil, err
	}
	if h.srcKey != shape.src {
		return nil, fmt.Errorf("route %s starts on %s, not %s", h.route, shape.src, h.srcKey)
	}
	src := c.net.Chains[h.srcKey]
	srcBridge := addr(src.Bridge)
	srcContract := srcBridge
	var router common.Address
	if shape.swap {
		if sw := c.net.Chains[ChainCross].Swap; sw != nil && sw.SwapBridgeRouter != nil {
			router = addr(sw.SwapBridgeRouter)
			srcContract = router
		}
	}
	var initiated []*parsedInitiated
	for _, l := range h.receipt.Logs {
		if l == nil || len(l.Topics) == 0 {
			continue
		}
		if l.Address == srcBridge && l.Topics[0] == topicBridgeInitiated {
			if p, ok := parseInitiated(*l); ok {
				initiated = append(initiated, p)
			}
		}
	}
	var swapLinked []SwapTransfer
	if shape.swap {
		swapLinked = linkSwapTransfer(h.receipt, router, srcBridge).linked
	}
	var chosen *parsedInitiated
	if h.logIndex != nil {
		for _, i := range initiated {
			if i.LogIndex == *h.logIndex {
				chosen = i
			}
		}
		if chosen == nil {
			return nil, fmt.Errorf("no BridgeInitiated with logIndex %d emitted by the source bridge in the receipt", *h.logIndex)
		}
	} else if shape.swap {
		// X3: ONLY the structurally LINKED transfers are candidates (linkSwapTransfer); with an adopted intent they must also match it (matchesSwapIntent)
		var cands []SwapTransfer
		for i := range swapLinked {
			s := &swapLinked[i]
			if h.sender != (common.Address{}) && s.User != h.sender {
				continue
			}
			if m := h.match; m != nil && m.Kind == MatchSwap && !matchesSwapIntent(s, m, h.sender, h.amountOutMinimum) {
				continue
			}
			cands = append(cands, *s)
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("no linked SwapBridge + BridgeInitiated pair in the receipt matches the intent")
		}
		if len(cands) > 1 && h.initiateIndex == nil {
			return nil, &AmbiguousSourceError{Msg: "several SwapBridge events match; pass an initiate index"}
		}
		sb := cands[0]
		if h.initiateIndex != nil {
			found := false
			for _, s := range cands {
				if s.Index.Cmp(h.initiateIndex) == 0 {
					sb, found = s, true
				}
			}
			if !found {
				return nil, fmt.Errorf("initiate index not found among SwapBridge events")
			}
		}
		chosen = sb.bi
	} else {
		var cands []*parsedInitiated
		for _, i := range initiated {
			if (h.sender == (common.Address{}) || i.From == h.sender) && matchesBridge(h.match, i, common.Address{}) {
				cands = append(cands, i)
			}
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("no matching BridgeInitiated event emitted by the source bridge in the receipt")
		}
		if len(cands) > 1 && h.initiateIndex == nil {
			return nil, &AmbiguousSourceError{Msg: "several BridgeInitiated events match; pass an initiate index"}
		}
		chosen = cands[0]
		if h.initiateIndex != nil {
			chosen = nil
			for _, i := range cands {
				if i.Index.Cmp(h.initiateIndex) == 0 {
					chosen = i
				}
			}
			if chosen == nil {
				return nil, fmt.Errorf("initiate index not found among BridgeInitiated events")
			}
		}
	}
	hop1Dest := c.net.Chains[shape.hop1Dest]
	hd := &TransferHandle{
		V: 1, Network: c.network, Route: h.route, SrcChainKey: h.srcKey, SrcChainID: src.ChainID, SrcContract: srcContract.Hex(), SrcBridge: srcBridge.Hex(),
		SrcTxHash: h.receipt.TxHash.Hex(), SrcBlock: h.receipt.BlockNumber.Uint64(),
		Hop1:   HopRef{DestChainKey: shape.hop1Dest, DestChainID: hop1Dest.ChainID, Index: chosen.Index.String(), Kind: shape.hop1Kind},
		Expect: HandleExpect{Value: chosen.Value.String(), Token: chosen.ToToken.Hex()}, ScanFrom: map[ChainKey]uint64{}, Scans: map[string]*ScanState{},
	}
	for k, v := range h.scanFrom {
		hd.ScanFrom[k] = v
	}
	switch {
	case shape.hop1Kind == HopHubForward:
		fwd, err := decodeForward(chosen.ExtraData)
		if err != nil {
			return nil, err
		}
		dc := c.net.Chains[shape.dest]
		if fwd.ToChainID.Int64() != dc.ChainID {
			return nil, fmt.Errorf("forward extraData targets chain %s, route expects %d", fwd.ToChainID, dc.ChainID)
		}
		hd.Hop2 = &HopRef{DestChainKey: shape.dest, DestChainID: dc.ChainID, Kind: shape.hop2Kind}
		hd.Expect.V2 = fwd.Value.String()
		if shape.hop2Kind == HopX2 { // nested X2: the amount committed in the forward payload
			if hd.Expect.X2, err = c.checkX2(fwd.Inner, fwd.Value); err != nil {
				return nil, err
			}
		}
	case shape.hop1Kind == HopX1:
		if hd.Expect.X1, err = c.checkX1(chosen.ExtraData, chosen.Value); err != nil {
			return nil, err
		}
	case shape.hop1Kind == HopX2:
		if hd.Expect.X2, err = c.checkX2(chosen.ExtraData, chosen.Value); err != nil {
			return nil, err
		}
	case shape.swap && len(chosen.ExtraData) > 0:
		return nil, &UnsupportedRouteError{Msg: "X3 with non-empty extraData is not supported by this SDK"}
	}
	return hd, nil
}

type forwardArgs struct {
	ToChainID  *big.Int
	To         common.Address
	Value      *big.Int
	NetworkFee *big.Int
	ExFee      *big.Int
	Inner      []byte
}

func decodeForward(extraData []byte) (*forwardArgs, error) {
	if len(extraData) < 24 || common.Bytes2Hex(extraData[20:24]) != common.Bytes2Hex(ForwardSelector[:]) {
		return nil, fmt.Errorf("extraData is not a bridgeTokenForwarded call")
	}
	out, err := bridgeABI.Methods["bridgeTokenForwarded"].Inputs.Unpack(extraData[24:])
	if err != nil {
		return nil, err
	}
	return &forwardArgs{ToChainID: out[0].(*big.Int), To: out[1].(common.Address), Value: out[2].(*big.Int), NetworkFee: out[3].(*big.Int), ExFee: out[4].(*big.Int), Inner: out[5].([]byte)}, nil
}

func (c *Client) handleInputFromQuote(q *Quote, r *types.Receipt, scanFrom map[ChainKey]uint64) handleInput {
	var x3To ChainKey
	if q.Request.X3 != nil {
		x3To = q.Request.X3.ToChain
	}
	m := q.Match
	return handleInput{route: q.Route, srcKey: q.SourceChain, receipt: r, hypercore: q.Request.Hypercore, x3To: x3To, sender: q.Sender, match: &m, amountOutMinimum: q.Limits.AmountOutMinimum, scanFrom: scanFrom}
}

func parseU(s string) *big.Int {
	v, _ := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if v == nil {
		return new(big.Int)
	}
	return v
}

var _ = strconv.Itoa
