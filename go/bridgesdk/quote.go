package bridgesdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

var quoteStatusNames = []string{"Invalid", "Success", "NoPair", "InsufficientForFee", "InsufficientValue", "InvalidSwap"}

func quoteStatusName(s uint8) string {
	if int(s) < len(quoteStatusNames) {
		return quoteStatusNames[s]
	}
	return fmt.Sprint(s)
}

type frozenQuote struct {
	old              *Quote
	limits           Limits
	deadline         uint64
	amountOutMinimum *big.Int
	minBridgeValue   *big.Int
}

type routeSpec struct {
	kind     QuoteKind
	src      ChainKey
	dest     ChainKey
	hub      ChainKey
	srcToken string // "native" or a token key
	core     bool
	required []string
}

func specFor(req *QuoteRequest) (routeSpec, error) {
	switch req.Route {
	case RouteBSCToOne:
		return routeSpec{KindSingle, ChainBSC, ChainOne, "", "ONE", false, []string{"bsc.bridge", "bsc.tokens.ONE", "one.bridge"}}, nil
	case RouteOneToBSC:
		return routeSpec{KindSingle, ChainOne, ChainBSC, "", "native", false, []string{"one.bridge", "bsc.bridge", "bsc.tokens.ONE"}}, nil
	case RouteOneToHyperEVM:
		return routeSpec{KindSingle, ChainOne, ChainHyperEVM, "", "native", false, []string{"one.bridge", "hyperevm.bridge", "hyperevm.tokens.ONE"}}, nil
	case RouteOneToHyperCore:
		return routeSpec{KindSingle, ChainOne, ChainHyperEVM, "", "native", true, []string{"one.bridge", "hyperevm.bridge", "hyperevm.tokens.ONE"}}, nil
	case RouteHyperEVMToOne:
		return routeSpec{KindSingle, ChainHyperEVM, ChainOne, "", "ONE", false, []string{"hyperevm.bridge", "hyperevm.tokens.ONE", "one.bridge"}}, nil
	case RouteBSCToOneToHyperEVM:
		return routeSpec{KindMultihop, ChainBSC, ChainHyperEVM, ChainOne, "ONE", req.Hypercore, []string{"bsc.bridge", "bsc.tokens.ONE", "one.bridge", "hyperevm.bridge", "hyperevm.tokens.ONE"}}, nil
	case RouteHyperEVMToOneToBSC:
		return routeSpec{KindMultihop, ChainHyperEVM, ChainBSC, ChainOne, "ONE", false, []string{"hyperevm.bridge", "hyperevm.tokens.ONE", "one.bridge", "bsc.bridge", "bsc.tokens.ONE"}}, nil
	case RouteBSCUSDTToONEUSD:
		return routeSpec{KindX1, ChainBSC, ChainOne, ChainOne, "USDT", false, []string{"bsc.bridge", "bsc.tokens.USDT", "one.bridge", "one.tokens.USDTx", "one.tokens.ONEUSD", "one.swap.v3SwapRouter", "one.swap.quoterV2", "one.swap.x1Pool"}}, nil
	case RouteSwapBridge:
		if req.X3 == nil || (req.X3.ToChain != ChainBSC && req.X3.ToChain != ChainHyperEVM) {
			return routeSpec{}, fmt.Errorf("X3 requires QuoteRequest.X3 with ToChain = bsc | hyperevm")
		}
		r := []string{"one.bridge", "one.swap.swapBridgeRouter", string(req.X3.ToChain) + ".bridge"}
		if req.X3.NativeIn {
			r = append(r, "one.tokens.WCROSS")
		}
		return routeSpec{KindX3, ChainOne, req.X3.ToChain, ChainOne, "native", false, r}, nil
	}
	return routeSpec{}, fmt.Errorf("unknown route %q", req.Route)
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "0x" + hex.EncodeToString(b)
}

func (c *Client) readFee(ctx context.Context, key ChainKey, verifier common.Address, toChainID uint64, token common.Address, value *big.Int) (FeeTriple, error) {
	out, err := c.callABI(ctx, key, verifierABI, verifier, "calculateFee", nil, u(toChainID), token, value)
	if err != nil {
		return FeeTriple{}, err
	}
	return FeeTriple{MinimumValue: out[0].(*big.Int), NetworkFee: out[1].(*big.Int), ExFee: out[2].(*big.Int)}, nil
}

func (c *Client) readPair(ctx context.Context, key ChainKey, bridge common.Address, toChainID uint64, token common.Address) (binding.IBridgeRegistryTokenPair, error) {
	out, err := c.callABI(ctx, key, bridgeABI, bridge, "getTokenPair", nil, u(toChainID), token)
	if err != nil {
		return binding.IBridgeRegistryTokenPair{}, err
	}
	return *abi.ConvertType(out[0], new(binding.IBridgeRegistryTokenPair)).(*binding.IBridgeRegistryTokenPair), nil
}

func (c *Client) bridgeVerifier(ctx context.Context, key ChainKey, bridge common.Address) (common.Address, error) {
	out, err := c.callABI(ctx, key, bridgeABI, bridge, "bridgeVerifier", nil)
	if err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}

func (c *Client) maxExtraData(ctx context.Context, key ChainKey, bridge common.Address) (*big.Int, error) {
	out, err := c.callABI(ctx, key, bridgeABI, bridge, "maxExtraDataLength", nil)
	if err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

func addr(s *string) common.Address { return common.HexToAddress(*s) }

func orDefault(a, b common.Address) common.Address {
	if a == (common.Address{}) {
		return b
	}
	return a
}

// resolveRecipients is THE recipient resolution (recipients.<hop> override, otherwise the default recipient). buildQuote, the quote -> client binding and
// the seal's route rules all use it; nothing re-implements it.
func resolveRecipients(req *QuoteRequest) (onHub, dest, core common.Address) {
	return orDefault(req.Recipients.OnHub, req.Recipient), orDefault(req.Recipients.Dest, req.Recipient), orDefault(req.Recipients.Core, req.Recipient)
}

// Quote does the live fee reads and the route-specific encoding. Routes whose config is null are refused with
// *AddressNotConfiguredError before any network access.
func (c *Client) Quote(ctx context.Context, request QuoteRequest) (*Quote, error) {
	return c.buildQuote(ctx, request, nil)
}

func (c *Client) buildQuote(ctx context.Context, reqIn QuoteRequest, fz *frozenQuote) (*Quote, error) {
	req := cloneRequest(reqIn) // snapshot: later mutation of the caller's request can never change the quote
	spec, err := specFor(&req)
	if err != nil {
		return nil, err
	}
	if err := c.net.requireConfigured(req.Route, spec.required); err != nil {
		return nil, err
	}
	if req.Amount == nil || req.Amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be > 0")
	}
	chains := c.net.Chains
	src := chains[spec.src]
	ttl := req.QuoteTTL
	if ttl == 0 {
		ttl = c.opts.QuoteTTL
	}
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	onHub, dest, coreR := resolveRecipients(&req)
	var violated []string

	if err := c.assertChain(ctx, spec.src); err != nil {
		return nil, err
	}
	if spec.hub != "" && spec.hub != spec.src {
		if err := c.assertChain(ctx, spec.hub); err != nil {
			return nil, err
		}
	}

	slippage, deadlineSec := 50, uint64(1800)
	if spec.kind == KindX1 || spec.kind == KindX3 {
		if b := req.Swap.SlippageBps; b != nil {
			if *b >= 10000 {
				return nil, &InvalidOptionError{Msg: fmt.Sprintf("Swap.SlippageBps must be in [0, 10000), got %d", *b)}
			}
			slippage = int(*b)
		}
		if d := req.Swap.DeadlineSec; d != nil {
			if *d == 0 {
				return nil, &InvalidOptionError{Msg: `Swap.DeadlineSec must be > 0 (a deadline of "now" always fails)`}
			}
			deadlineSec = *d
		}
	}

	// ---------------------------------------------------------------------------------------- X3
	if spec.kind == KindX3 {
		x3 := req.X3
		router := addr(chains[ChainOne].Swap.SwapBridgeRouter)
		toChainID := uint64(chains[spec.dest].ChainID)
		tokenIn := x3.TokenIn
		if x3.NativeIn {
			tokenIn = common.HexToAddress(chains[ChainOne].Tokens["WCROSS"].Address)
		}
		deadline := uint64(c.now().Unix()) + deadlineSec
		if fz != nil {
			deadline = fz.deadline
		}
		sender := req.Sender
		out, err := c.callABI(ctx, ChainOne, sbrABI, router, "getAmountSwapBridgeOut", &sender, u(toChainID), tokenIn, x3.TokenOut, u(uint64(x3.PoolFee)), req.Amount)
		if err != nil {
			return nil, err
		}
		status := out[0].(uint8)
		if status != 1 {
			return nil, &PreflightError{Issues: []PreflightIssue{{Level: "error", Code: "X3_QUOTE_" + quoteStatusName(status), Message: "SwapBridgeRouter quote status " + quoteStatusName(status),
				Hint: "NoPair: tokenOut has no hub pair to the destination; InsufficientForFee/InsufficientValue: amount too small; InvalidSwap: no pool / liquidity."}}}
		}
		// the EXPECTED destination token: the hub pair's remoteToken, read on the chain-verified hub RPC; a missing pair fails the quote
		xpair, err := c.readPair(ctx, ChainOne, addr(chains[ChainOne].Bridge), toChainID, x3.TokenOut)
		if err != nil {
			return nil, err
		}
		if xpair.LocalToken != x3.TokenOut {
			return nil, &PreflightError{Issues: []PreflightIssue{{Level: "error", Code: "PAIR_NOT_REGISTERED", Message: fmt.Sprintf("tokenOut %s has no hub pair to chain %d", x3.TokenOut, toChainID), Hint: "The token pair is not registered (RegistryNotExistToken)."}}}
		}
		swapOut, expectedBridge := out[1].(*big.Int), out[2].(*big.Int)
		hop1 := FeeTriple{MinimumValue: new(big.Int), NetworkFee: out[3].(*big.Int), ExFee: out[4].(*big.Int)}
		amountOutMin, _ := FloorBps(swapOut, slippage)
		if fz != nil {
			amountOutMin = fz.amountOutMinimum
		}
		eb, err := c.callABI(ctx, ChainOne, sbrABI, router, "getExpectedBridgeAmount", nil, u(toChainID), x3.TokenOut, amountOutMin)
		if err != nil {
			return nil, err
		}
		ebStatus, deliveryAtFloor := eb[0].(uint8), eb[1].(*big.Int)
		if fz != nil {
			if swapOut.Cmp(amountOutMin) < 0 {
				violated = append(violated, "swapOutputBelowFloor")
			}
			if ebStatus != 1 || deliveryAtFloor.Cmp(fz.minBridgeValue) < 0 {
				violated = append(violated, "bridgeDeliveryFloor")
			}
		} else if ebStatus != 1 {
			return nil, &PreflightError{Issues: []PreflightIssue{{Level: "error", Code: "X3_FLOOR_" + quoteStatusName(ebStatus), Message: "the delivery floor cannot be bridged; lower slippage or raise the amount"}}}
		}
		minBridge := deliveryAtFloor
		if fz != nil {
			minBridge = fz.minBridgeValue
		}
		data, err := EncodeSwapBridge(SwapBridgeInput{TokenIn: tokenIn, TokenOut: x3.TokenOut, Fee: x3.PoolFee, AmountIn: req.Amount, AmountOutMinimum: amountOutMin, ToChainID: toChainID, Recipient: dest, ExtraData: nil, Deadline: deadline, Native: x3.NativeIn})
		if err != nil {
			return nil, err
		}
		limits := Limits{MaxTotalSpend: req.Amount, MinDelivered: minBridge, AmountOutMinimum: amountOutMin, MinBridgeValue: minBridge}
		if req.MaxTotalSpend != nil {
			limits.MaxTotalSpend = req.MaxTotalSpend
		}
		if req.MinDelivered != nil {
			limits.MinDelivered = req.MinDelivered
		}
		if fz != nil {
			limits = fz.limits
		}
		val := new(big.Int)
		var approval *Approval
		if x3.NativeIn {
			val = new(big.Int).Set(req.Amount)
		} else {
			approval = &Approval{Token: tokenIn, Spender: router, Amount: req.Amount}
		}
		q := &Quote{
			ID: randID(), Network: c.network, Route: req.Route, Request: req, Kind: KindX3, SourceChain: ChainOne, SourceChainID: chains[ChainOne].ChainID,
			SourceContract: router, SourceBridge: addr(chains[ChainOne].Bridge), Sender: req.Sender, CreatedAt: c.now(), TTL: ttl,
			Hop1Fees: hop1, Value: expectedBridge, TotalSpend: req.Amount, Tx: TxData{To: router, Data: data, Value: val}, Approval: approval, Limits: limits, Deadline: deadline,
			Match:   MatchSpec{Kind: MatchSwap, TokenIn: tokenIn, TokenOut: x3.TokenOut, AmountIn: req.Amount, ToChainID: toChainID, ToToken: xpair.RemoteToken, Recipient: dest, ExtraHash: hashExtraData(nil)},
			Details: QuoteDetails{Hop1ToChainID: toChainID, Hop1ToToken: x3.TokenOut, DestChain: spec.dest, DestChainID: toChainID, Hop1Kind: HopPlain, ExtraData: []byte{}, Dest: dest, SwapAmountOut: swapOut},
		}
		if req.Amount.Cmp(limits.MaxTotalSpend) > 0 {
			violated = append(violated, "maxTotalSpend")
		}
		if minBridge.Cmp(limits.MinDelivered) < 0 {
			violated = append(violated, "minDelivered")
		}
		return finishQuote(q, violated, fz)
	}

	// ---------------------------------------------------------------------------------------- bridge routes
	srcBridge := addr(src.Bridge)
	native := spec.srcToken == "native"
	srcTokenAddr := common.HexToAddress(NativeToken)
	if !native {
		srcTokenAddr = common.HexToAddress(src.Tokens[spec.srcToken].Address)
	}
	toChainID := uint64(chains[spec.dest].ChainID)
	if spec.kind == KindMultihop || spec.kind == KindX1 {
		toChainID = uint64(chains[ChainOne].ChainID)
	}
	srcVerifier, err := c.bridgeVerifier(ctx, spec.src, srcBridge)
	if err != nil {
		return nil, err
	}
	pair, err := c.readPair(ctx, spec.src, srcBridge, toChainID, srcTokenAddr)
	if err != nil {
		return nil, err
	}
	if pair.LocalToken != srcTokenAddr {
		return nil, &PreflightError{Issues: []PreflightIssue{{Level: "error", Code: "PAIR_NOT_REGISTERED", Message: fmt.Sprintf("token %s has no registered pair to chain %d on %s", srcTokenAddr, toChainID, spec.src), Hint: "The token pair is not registered (RegistryNotExistToken)."}}}
	}

	value := new(big.Int).Set(req.Amount)
	extraData := []byte{}
	var hop2 *FeeTriple
	var ef2Declared, v2 *big.Int
	var hubVerifier common.Address
	hop1Kind, hop2Kind := HopPlain, HopKind("")
	var deadline uint64
	var amountOutMin, expectedSwapOut, coreAmount, coreDust *big.Int
	destToChainID := uint64(chains[spec.dest].ChainID)

	coreTokenAddr := func() common.Address {
		return common.HexToAddress(chains[ChainHyperEVM].Tokens["ONE"].Address)
	}
	coreInfo := func(amount *big.Int) {
		if err := c.assertChain(ctx, ChainHyperEVM); err != nil {
			return
		}
		out, err := c.callABI(ctx, ChainHyperEVM, hyperABI, coreTokenAddr(), "coreUnit", nil)
		if err != nil {
			return
		}
		if ca, _, d, err := CoreAmounts(amount, out[0].(*big.Int)); err == nil {
			coreAmount, coreDust = ca, d
		}
	}

	switch {
	case spec.kind == KindSingle && spec.core:
		extraData, err = EncodeCoreExtraData(CoreInput{Token: coreTokenAddr(), CoreRecipient: coreR, Value: value})
		if err != nil {
			return nil, err
		}
		hop1Kind = HopX2
		coreInfo(value)
	case spec.kind == KindMultihop:
		hub := addr(chains[ChainOne].Bridge)
		if hubVerifier, err = c.bridgeVerifier(ctx, ChainOne, hub); err != nil {
			return nil, err
		}
		hubFee, err := c.readFee(ctx, ChainOne, hubVerifier, destToChainID, common.HexToAddress(NativeToken), req.Amount)
		if err != nil {
			return nil, err
		}
		v2 = new(big.Int).Set(req.Amount)
		if v2.Cmp(hubFee.MinimumValue) < 0 {
			return nil, &PreflightError{Issues: []PreflightIssue{{Level: "error", Code: "BELOW_MINIMUM_HOP2", Message: fmt.Sprintf("amount %s is below the hub minimumValue %s for chain %d", v2, hubFee.MinimumValue, destToChainID)}}}
		}
		hop2 = &hubFee
		ef, v1, err := MultihopAmounts(v2, hubFee, req.Hop2FeeBuffer)
		if err != nil {
			return nil, err
		}
		ef2Declared, value = ef, v1
		inner := []byte{}
		hop2Kind = HopPlain
		if spec.core {
			if inner, err = EncodeCoreExtraData(CoreInput{Token: coreTokenAddr(), CoreRecipient: coreR, Value: v2}); err != nil {
				return nil, err
			}
			hop2Kind = HopX2
			coreInfo(v2)
		}
		extraData, err = EncodeForwardExtraData(ForwardInput{Hub: hub, DestChainID: destToChainID, ToDest: dest, Value2: v2, NetworkFee2: hubFee.NetworkFee, ExFee2Declared: ef, Inner: inner})
		if err != nil {
			return nil, err
		}
		hop1Kind = HopHubForward
	case spec.kind == KindX1:
		oneChain := chains[ChainOne]
		pool := oneChain.Swap.X1Pool
		router := addr(oneChain.Swap.V3SwapRouter)
		deadline = uint64(c.now().Unix()) + deadlineSec
		if fz != nil {
			deadline = fz.deadline
		}
		usdtx, oneusd := common.HexToAddress(oneChain.Tokens["USDTx"].Address), common.HexToAddress(oneChain.Tokens["ONEUSD"].Address)
		if err := c.assertChain(ctx, ChainOne); err != nil {
			return nil, err
		}
		type quoteParams struct {
			TokenIn           common.Address
			TokenOut          common.Address
			AmountIn          *big.Int
			Fee               *big.Int
			SqrtPriceLimitX96 *big.Int
		}
		qo, err := c.callABI(ctx, ChainOne, quoterABI, addr(oneChain.Swap.QuoterV2), "quoteExactInputSingle", nil, quoteParams{usdtx, oneusd, value, u(uint64(pool.Fee)), new(big.Int)})
		if err != nil {
			return nil, err
		}
		expectedSwapOut = qo[0].(*big.Int)
		amountOutMin, _ = FloorBps(expectedSwapOut, slippage)
		if fz != nil {
			amountOutMin = fz.amountOutMinimum
			if expectedSwapOut.Cmp(amountOutMin) < 0 {
				violated = append(violated, "swapOutputBelowFloor")
			}
		}
		extraData, err = EncodeX1ExtraData(X1Input{Router: router, TokenIn: usdtx, TokenOut: oneusd, Fee: pool.Fee, Recipient: dest, Deadline: deadline, AmountIn: value, AmountOutMinimum: amountOutMin})
		if err != nil {
			return nil, err
		}
		hop1Kind = HopX1
		destToChainID = uint64(chains[ChainOne].ChainID)
	}

	fee1, err := c.readFee(ctx, spec.src, srcVerifier, toChainID, srcTokenAddr, value)
	if err != nil {
		return nil, err
	}
	if value.Cmp(fee1.MinimumValue) < 0 {
		return nil, &PreflightError{Issues: []PreflightIssue{{Level: "error", Code: "BELOW_MINIMUM", Message: fmt.Sprintf("value %s is below minimumValue %s of the source verifier", value, fee1.MinimumValue), Hint: "Quote live: minimums move with the price feed."}}}
	}
	total := add(value, fee1.NetworkFee, fee1.ExFee)
	hop1To := dest
	if spec.kind == KindMultihop {
		hop1To = onHub
	}
	data, err := EncodeBridgeToken(BridgeTokenCall{ToChainID: toChainID, FromToken: srcTokenAddr, To: hop1To, Value: value, NetworkFee: fee1.NetworkFee, ExFee: fee1.ExFee, ExtraData: extraData})
	if err != nil {
		return nil, err
	}
	minDelivered := value
	if spec.kind == KindMultihop {
		minDelivered = v2
	}
	limits := Limits{MaxTotalSpend: total, MinDelivered: minDelivered, AmountOutMinimum: amountOutMin}
	if req.MaxTotalSpend != nil {
		limits.MaxTotalSpend = req.MaxTotalSpend
	}
	if req.MinDelivered != nil {
		limits.MinDelivered = req.MinDelivered
	}
	if fz != nil {
		limits = fz.limits
	}
	if total.Cmp(limits.MaxTotalSpend) > 0 {
		violated = append(violated, "maxTotalSpend")
	}
	if minDelivered.Cmp(limits.MinDelivered) < 0 {
		violated = append(violated, "minDelivered")
	}
	val := new(big.Int)
	var approval *Approval
	if native {
		val = new(big.Int).Set(total)
	} else {
		approval = &Approval{Token: srcTokenAddr, Spender: srcBridge, Amount: total}
	}
	q := &Quote{
		ID: randID(), Network: c.network, Route: req.Route, Request: req, Kind: spec.kind, SourceChain: spec.src, SourceChainID: src.ChainID,
		SourceContract: srcBridge, SourceBridge: srcBridge, Sender: req.Sender, CreatedAt: c.now(), TTL: ttl, SourceVerifier: srcVerifier, HubVerifier: hubVerifier,
		Hop1Fees: fee1, Hop2Fees: hop2, Hop2ExFeeDeclared: ef2Declared, Value: value, V2: v2, TotalSpend: total,
		Tx: TxData{To: srcBridge, Data: data, Value: val}, Approval: approval, Limits: limits, Deadline: deadline,
		Match: MatchSpec{Kind: MatchBridge, ToChainID: toChainID, FromToken: srcTokenAddr, ToToken: pair.RemoteToken, Recipient: hop1To, Value: value, NetworkFee: fee1.NetworkFee, ExFee: fee1.ExFee, ExtraHash: hashExtraData(extraData)},
		Details: QuoteDetails{Hop1ToChainID: toChainID, Hop1ToToken: pair.RemoteToken, HubChain: spec.hub, DestChain: spec.dest, DestChainID: destToChainID, Hop1Kind: hop1Kind, Hop2Kind: hop2Kind,
			ExtraData: extraData, OnHub: onHub, Dest: dest, Core: coreR, ExpectedSwapOut: expectedSwapOut, CoreAmount: coreAmount, CoreDust: coreDust},
	}
	return finishQuote(q, violated, fz)
}

func finishQuote(q *Quote, violated []string, fz *frozenQuote) (*Quote, error) {
	if err := sealQuoteInPlace(q); err != nil { // sealed: canonical payload of the WHOLE quote + keccak256 digest
		return nil, fmt.Errorf("seal quote: %w", err)
	}
	if len(violated) > 0 {
		if fz != nil {
			return nil, &RequoteRequiredError{OldQuote: fz.old, NewQuote: cloneQuote(q), Violated: violated}
		}
		return nil, &LimitViolationError{Violated: violated, Quote: cloneQuote(q)}
	}
	return cloneQuote(q), nil
}

func frozenFrom(q *Quote) *frozenQuote {
	return &frozenQuote{old: q, limits: q.Limits, deadline: q.Deadline, amountOutMinimum: q.Limits.AmountOutMinimum, minBridgeValue: q.Limits.MinBridgeValue}
}
