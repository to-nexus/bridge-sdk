package bridgesdk

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

// Quote for every route against the fake world: amounts, msg.value, approve target, recipients per hop, swap options and floors.

func TestQuoteSingleHopRoutes(t *testing.T) {
	w := testnetWorld(t)
	c := w.client()
	ch := w.net.Chains
	v := n18(10)
	q1 := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOne, Amount: v, Sender: senderAdr, Recipient: recipient})
	if q1.Tx.To != common.HexToAddress(*ch[ChainBSC].Bridge) || q1.Tx.Value.Sign() != 0 || q1.Approval == nil || q1.Approval.Amount.Cmp(v) != 0 {
		t.Fatalf("R1 %+v", q1)
	}
	q2 := mustQuote(t, c, QuoteRequest{Route: RouteOneToBSC, Amount: v, Sender: senderAdr, Recipient: recipient})
	if q2.Approval != nil || q2.Tx.Value.Cmp(add(v, nf18)) != 0 {
		t.Fatalf("R2 msg.value must be value+fees exactly: %s", q2.Tx.Value)
	}
	q3 := mustQuote(t, c, QuoteRequest{Route: RouteOneToHyperEVM, Amount: v, Sender: senderAdr, Recipient: recipient})
	if decBridge(t, q3.Tx.Data)[0].(*big.Int).Int64() != ch[ChainHyperEVM].ChainID || q3.Tx.Value.Cmp(add(v, nf18)) != 0 {
		t.Fatalf("R3")
	}
	q4 := mustQuote(t, c, QuoteRequest{Route: RouteHyperEVMToOne, Amount: v, Sender: senderAdr, Recipient: recipient})
	if q4.Approval.Spender != common.HexToAddress(*ch[ChainHyperEVM].Bridge) || q4.Approval.Token != tok(w, ChainHyperEVM, "ONE") {
		t.Fatalf("R4 approve target %+v", q4.Approval)
	}
}

func TestQuoteRefusals(t *testing.T) {
	w := testnetWorld(t)
	c := w.client()
	_, err := c.Quote(ctx, QuoteRequest{Route: RouteOneToBSC, Amount: n18(4), Sender: senderAdr, Recipient: recipient})
	var pe *PreflightError
	if !errors.As(err, &pe) || pe.Issues[0].Code != "BELOW_MINIMUM" {
		t.Fatalf("below minimum: %v", err)
	}
	w.pairs = map[string]binding.IBridgeRegistryTokenPair{}
	_, err = c.Quote(ctx, QuoteRequest{Route: RouteOneToBSC, Amount: n18(10), Sender: senderAdr, Recipient: recipient})
	if !errors.As(err, &pe) || pe.Issues[0].Code != "PAIR_NOT_REGISTERED" {
		t.Fatalf("unregistered pair: %v", err)
	}
}

func TestQuoteMultihopBackwardComputationAndRecipients(t *testing.T) {
	w := testnetWorld(t)
	c := w.client()
	ch := w.net.Chains
	v2, buffer := n18(10), bg("50000000000000000")
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: v2, Sender: senderAdr, Recipient: recipient, Recipients: Recipients{OnHub: r2Adr, Dest: r3Adr}, Hop2FeeBuffer: buffer})
	if q.V2.Cmp(v2) != 0 || q.Value.Cmp(add(v2, nf18, buffer)) != 0 || q.Hop2ExFeeDeclared.Cmp(buffer) != 0 || q.Limits.MinDelivered.Cmp(v2) != 0 {
		t.Fatalf("amounts: v1=%s", q.Value)
	}
	args := decBridge(t, q.Tx.Data)
	if args[2].(common.Address) != r2Adr || args[3].(*big.Int).Cmp(q.Value) != 0 {
		t.Fatal("hop-1 `to` must be toOnHub and value v1")
	}
	ed := args[6].([]byte)
	if !bytes.Equal(ed[:20], common.HexToAddress(*ch[ChainOne].Bridge).Bytes()) || !bytes.Equal(ed[20:24], ForwardSelector[:]) {
		t.Fatal("hop-1 extraData must start with hub || 0x55fd0776")
	}
	fwd, err := decodeForward(ed)
	if err != nil || fwd.ToChainID.Int64() != ch[ChainHyperEVM].ChainID || fwd.To != r3Adr || fwd.Value.Cmp(v2) != 0 || fwd.NetworkFee.Cmp(nf18) != 0 || fwd.ExFee.Cmp(buffer) != 0 || len(fwd.Inner) != 0 {
		t.Fatalf("forward %+v %v", fwd, err)
	}
	q6 := mustQuote(t, c, QuoteRequest{Route: RouteHyperEVMToOneToBSC, Amount: v2, Sender: senderAdr, Recipient: recipient})
	if q6.SourceChain != ChainHyperEVM || q6.TotalSpend.Cmp(add(v2, nf18)) != 0 {
		t.Fatalf("R6 %+v", q6.TotalSpend)
	}
	_, err = c.Quote(ctx, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(4), Sender: senderAdr, Recipient: recipient})
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("minimum applies at hop 2: %v", err)
	}
}

func TestQuoteX2HyperCore(t *testing.T) {
	w := testnetWorld(t)
	c := w.client()
	ch := w.net.Chains
	v := add(n18(5), big.NewInt(42))
	q := mustQuote(t, c, QuoteRequest{Route: RouteOneToHyperCore, Amount: v, Sender: senderAdr, Recipient: recipient, Recipients: Recipients{Core: r3Adr}})
	ed := decBridge(t, q.Tx.Data)[6].([]byte)
	if len(ed) != 88 || !bytes.Equal(ed[:20], common.HexToAddress(ch[ChainHyperEVM].Tokens["ONE"].Address).Bytes()) || !bytes.Equal(ed[20:24], CoreSelector[:]) {
		t.Fatalf("X2 extraData: %d B", len(ed))
	}
	if q.Details.CoreAmount.Int64() != 500000000 || q.Details.CoreDust.Int64() != 42 || q.Details.Hop1Kind != HopX2 {
		t.Fatalf("core rounding: %s %s", q.Details.CoreAmount, q.Details.CoreDust)
	}
	q5 := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Hypercore: true, Amount: v, Sender: senderAdr, Recipient: recipient})
	if q5.Details.Hop2Kind != HopX2 || len(decBridge(t, q5.Tx.Data)[6].([]byte)) != 344 {
		t.Fatalf("R5+X2 extraData must be 344 B")
	}
}

func TestQuoteX3Floors(t *testing.T) {
	w := testnetWorld(t)
	c := w.client()
	ch := w.net.Chains
	usdtx := tok(w, ChainOne, "USDTx")
	w.swapOut = n18(100)
	req := QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: senderAdr, Recipient: recipient, Swap: SwapOptions{SlippageBps: Bps(100)}, X3: &X3Request{NativeIn: true, TokenOut: usdtx, ToChain: ChainBSC}}
	q := mustQuote(t, c, req)
	if q.Tx.Value.Cmp(n18(50)) != 0 || q.Approval != nil || q.Limits.AmountOutMinimum.Cmp(n18(99)) != 0 || q.Limits.MinBridgeValue.Cmp(new(big.Int).Sub(n18(99), nf18)) != 0 {
		t.Fatalf("X3 native: %+v %s %s", q.Approval, q.Limits.AmountOutMinimum, q.Limits.MinBridgeValue)
	}
	m := sbrABI.Methods["swapBridgeExactInputSingleETH"]
	if !bytes.Equal(q.Tx.Data[:4], m.ID) {
		t.Fatal("native input must use swapBridgeExactInputSingleETH")
	}
	args, _ := m.Inputs.Unpack(q.Tx.Data[4:])
	p := *abi.ConvertType(args[0], new(binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)).(*binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)
	if p.TokenIn != common.HexToAddress(ch[ChainOne].Tokens["WCROSS"].Address) {
		t.Fatalf("tokenIn must be WCROSS for native input, got %s", p.TokenIn)
	}
}

func TestQuoteX1Mainnet(t *testing.T) {
	w := newWorld(t, Mainnet)
	ch := w.net.Chains
	hubID := ch[ChainOne].ChainID
	usdt, usdtx := tok(w, ChainBSC, "USDT"), tok(w, ChainOne, "USDTx")
	router := common.HexToAddress(*ch[ChainOne].Swap.V3SwapRouter)
	w.setPair(ChainBSC, hubID, usdt, usdtx, true)
	w.setFee(ChainBSC, hubID, usdt, n18(5), new(big.Int), new(big.Int))
	w.whitelist[key(ChainOne, router)] = true
	w.whitelist[key(ChainOne, *ch[ChainOne].Bridge)] = true
	w.x1Out = add(n18(100), big.NewInt(3_511_957))
	w.setBal(ChainBSC, usdt, senderAdr, n18(1000))
	w.setAllow(ChainBSC, usdt, senderAdr, common.HexToAddress(*ch[ChainBSC].Bridge), n18(1000))
	c := w.client()
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: senderAdr, Recipient: recipient})
	floor, _ := FloorBps(w.x1Out, 50)
	if q.Limits.AmountOutMinimum.Cmp(floor) != 0 || q.Deadline != uint64(w.clock.Unix())+1800 {
		t.Fatalf("floor/deadline: %s %d", q.Limits.AmountOutMinimum, q.Deadline)
	}
	ed := decBridge(t, q.Tx.Data)[6].([]byte)
	if !bytes.Equal(ed[:20], router.Bytes()) || !bytes.Equal(ed[20:24], ExactInputSingleSelector[:]) {
		t.Fatal("X1 extraData must be router || 0x414bf389")
	}
	pf, err := c.Preflight(ctx, q)
	if err != nil || !pf.OK {
		t.Fatalf("preflight: %+v %v", pf, err)
	}
	delete(w.whitelist, key(ChainOne, router))
	pf, _ = c.Preflight(ctx, q)
	found := false
	for _, i := range pf.Issues {
		found = found || i.Code == "EXECUTOR_NOT_WHITELISTED"
	}
	if !found {
		t.Fatal("missing whitelist issue")
	}
	// price drop between quote and send -> refused, floor never loosened
	w.whitelist[key(ChainOne, router)] = true
	k, a := w.fundedWallet()
	w.setBal(ChainBSC, usdt, a, n18(1000))
	q = mustQuote(t, c, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: a, Recipient: recipient, QuoteTTL: 60e9})
	w.onApprove = func() { w.x1Out = new(big.Int).Div(new(big.Int).Mul(floor, big.NewInt(99)), big.NewInt(100)) }
	_, err = c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	var rr *RequoteRequiredError
	if !errors.As(err, &rr) || !contains(rr.Violated, "swapOutputBelowFloor") || len(w.chains[ChainBSC].logs) != 0 {
		t.Fatalf("want swapOutputBelowFloor, got %v", err)
	}
	// price rise: adopted, calldata keeps the ORIGINAL floor and deadline
	w.x1Out = n18(100)
	q = mustQuote(t, c, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: a, Recipient: recipient, QuoteTTL: 60e9})
	orig := q.Limits.AmountOutMinimum
	w.approveCalls = nil
	w.setAllow(ChainBSC, usdt, a, common.HexToAddress(*ch[ChainBSC].Bridge), new(big.Int))
	w.onApprove = func() { w.x1Out = n18(105) }
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Quote.Limits.AmountOutMinimum.Cmp(orig) != 0 || sent.Quote.Deadline != q.Deadline {
		t.Fatal("floor and deadline must stay as approved")
	}
	ed2 := decBridge(t, sent.Intent.Data)[6].([]byte)
	sw, _ := v3RouterABI.Methods["exactInputSingle"].Inputs.Unpack(ed2[24:])
	p := sw[0].(struct {
		TokenIn           common.Address `json:"tokenIn"`
		TokenOut          common.Address `json:"tokenOut"`
		Fee               *big.Int       `json:"fee"`
		Recipient         common.Address `json:"recipient"`
		Deadline          *big.Int       `json:"deadline"`
		AmountIn          *big.Int       `json:"amountIn"`
		AmountOutMinimum  *big.Int       `json:"amountOutMinimum"`
		SqrtPriceLimitX96 *big.Int       `json:"sqrtPriceLimitX96"`
	})
	if p.AmountOutMinimum.Cmp(orig) != 0 {
		t.Fatal("calldata floor = ORIGINAL")
	}
	_ = strings.ToLower
}

func TestSwapOptionsPointerSemantics_X1(t *testing.T) {
	w, k, a, c := x1WorldFor(t)
	q := mustQuote(t, c, x1Req(a, SwapOptions{}))
	want, _ := FloorBps(w.x1Out, 50)
	if q.Limits.AmountOutMinimum.Cmp(want) != 0 {
		t.Fatalf("unset must be 50 bps: %s vs %s", q.Limits.AmountOutMinimum, want)
	}
	if q.Deadline != uint64(w.clock.Unix())+1800 {
		t.Fatalf("default deadline 1800 s, got %d", q.Deadline-uint64(w.clock.Unix()))
	}
	q0 := mustQuote(t, c, x1Req(a, SwapOptions{SlippageBps: Bps(0), DeadlineSec: Seconds(60)}))
	if q0.Limits.AmountOutMinimum.Cmp(w.x1Out) != 0 || q0.Deadline != uint64(w.clock.Unix())+60 {
		t.Fatalf("explicit 0 bps must give floor == quote, got %s", q0.Limits.AmountOutMinimum)
	}
	for _, bad := range []SwapOptions{{SlippageBps: Bps(10000)}, {SlippageBps: Bps(65000)}, {DeadlineSec: Seconds(0)}} {
		var io *InvalidOptionError
		if _, err := c.Quote(ctx, x1Req(a, bad)); !errors.As(err, &io) {
			t.Fatalf("want InvalidOptionError for %+v, got %v", bad, err)
		}
	}
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q0, SendOptions{})
	if err != nil || sent.Quote.Limits.AmountOutMinimum.Cmp(w.x1Out) != 0 {
		t.Fatalf("send keeps the explicit-0 floor: %v", err)
	}
}

func TestSwapOptionsPointerSemantics_X3(t *testing.T) {
	w := testnetWorld(t)
	k, a := w.fundedWallet()
	usdtx := tok(w, ChainOne, "USDTx")
	w.setFee(ChainOne, w.net.Chains[ChainBSC].ChainID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	w.swapOut = n18(100)
	c := w.client()
	req := func(s SwapOptions) QuoteRequest {
		return QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, Swap: s, X3: &X3Request{TokenIn: tokenA, TokenOut: usdtx, ToChain: ChainBSC}}
	}
	q := mustQuote(t, c, req(SwapOptions{}))
	want, _ := FloorBps(w.swapOut, 50)
	if q.Limits.AmountOutMinimum.Cmp(want) != 0 {
		t.Fatalf("unset must be 50 bps")
	}
	q0 := mustQuote(t, c, req(SwapOptions{SlippageBps: Bps(0)}))
	if q0.Limits.AmountOutMinimum.Cmp(w.swapOut) != 0 {
		t.Fatalf("explicit 0: floor == swap output, got %s", q0.Limits.AmountOutMinimum)
	}
	for _, bad := range []SwapOptions{{SlippageBps: Bps(10000)}, {DeadlineSec: Seconds(0)}} {
		var io *InvalidOptionError
		if _, err := c.Quote(ctx, req(bad)); !errors.As(err, &io) {
			t.Fatalf("want InvalidOptionError for %+v, got %v", bad, err)
		}
	}
	sent, err := c.Send(ctx, w.signer(k, ChainOne), q0, SendOptions{})
	if err != nil || sent.Quote.Limits.AmountOutMinimum.Cmp(w.swapOut) != 0 {
		t.Fatalf("send keeps the explicit-0 floor: %v", err)
	}
}
