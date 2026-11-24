package bridgesdk

// Slipstream CL (dex "cl") support: encoding vectors cross-checked against `cast calldata`, config defaulting, the X1 / X3 flows through the fake
// RPC world, and the rejection of the other DEX's selector.

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Expected calldata produced with foundry `cast calldata` (A = 0x11..11, B = 0x22..22, C = 0x33..33, deadline 1800000000, amountIn 100e18,
// amountOutMinimum 99e18, quote amountIn 1e18, toChainID 56). Do not regenerate them with the SDK encoders.
const (
	// cast calldata 'exactInputSingle((address,address,int24,address,uint256,uint256,uint256,uint160))' '(A,B,200,C,1800000000,100e18,99e18,0)'
	castCLExactInputSingle200 = "0xa026383e0000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000000c80000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000"
	// same with tickSpacing -8388608 (int24 min)
	castCLExactInputSingleMin = "0xa026383e00000000000000000000000011111111111111111111111111111111111111110000000000000000000000002222222222222222222222222222222222222222ffffffffffffffffffffffffffffffffffffffffffffffffffffffffff8000000000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000"
	// same with tickSpacing 8388607 (int24 max)
	castCLExactInputSingleMax = "0xa026383e0000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000007fffff0000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000"
	// cast calldata 'exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))' '(A,B,500,C,1800000000,100e18,99e18,0)'
	castV3ExactInputSingle500 = "0x414bf3890000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000001f40000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac00000000000000000000000000000000000000000000000000000000000000000000"
	// cast calldata 'quoteExactInputSingle((address,address,uint256,int24,uint160))' '(A,B,1e18,200,0)'
	castCLQuote200 = "0x9e7defe6000000000000000000000000111111111111111111111111111111111111111100000000000000000000000022222222222222222222222222222222222222220000000000000000000000000000000000000000000000000de0b6b3a764000000000000000000000000000000000000000000000000000000000000000000c80000000000000000000000000000000000000000000000000000000000000000"
	// same with tickSpacing -1
	castCLQuoteNeg1 = "0x9e7defe6000000000000000000000000111111111111111111111111111111111111111100000000000000000000000022222222222222222222222222222222222222220000000000000000000000000000000000000000000000000de0b6b3a7640000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff0000000000000000000000000000000000000000000000000000000000000000"
	// cast calldata 'swapBridgeExactInputSingle((address,address,int24,uint256,uint256,uint160,(uint256,address,bytes)),uint256)' '(A,B,200,100e18,99e18,0,(56,C,0x))' 1800000000
	castSBRCL200 = "0x49d9a7f70000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000006b49d2000000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000000c80000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000038000000000000000000000000333333333333333333333333333333333333333300000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000000"
	// cast calldata 'swapBridgeExactInputSingleETH(...same...)' '(A,B,-100,100e18,99e18,0,(56,C,0x))' 1800000000
	castSBRCLETHNeg100 = "0xa26337a60000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000006b49d20000000000000000000000000011111111111111111111111111111111111111110000000000000000000000002222222222222222222222222222222222222222ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff9c0000000000000000000000000000000000000000000000056bc75e2d631000000000000000000000000000000000000000000000000000055de6a779bbac0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000038000000000000000000000000333333333333333333333333333333333333333300000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000000"
	// cast calldata 'getAmountSwapBridgeOut(uint256,address,address,int24,uint256)' 56 A B 200 1e18
	castGetAmountOut200 = "0xdf7da91c00000000000000000000000000000000000000000000000000000000000000380000000000000000000000001111111111111111111111111111111111111111000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000000c80000000000000000000000000000000000000000000000000de0b6b3a7640000"
	// same with tickSpacing -100
	castGetAmountOutNeg100 = "0xdf7da91c000000000000000000000000000000000000000000000000000000000000003800000000000000000000000011111111111111111111111111111111111111110000000000000000000000002222222222222222222222222222222222222222ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff9c0000000000000000000000000000000000000000000000000de0b6b3a7640000"
)

var (
	vecA = common.HexToAddress("0x1111111111111111111111111111111111111111")
	vecB = common.HexToAddress("0x2222222222222222222222222222222222222222")
	vecC = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

func hexOf(b []byte) string { return "0x" + hex.EncodeToString(b) }

func TestCLSelectorsAndABI(t *testing.T) {
	for name, c := range map[string]struct {
		got  [4]byte
		want string
	}{
		"V3 exactInputSingle":        {ExactInputSingleSelector, "0x414bf389"},
		"CL exactInputSingle":        {CLExactInputSingleSelector, "0xa026383e"},
		"CL quoteExactInputSingle":   {CLQuoteExactInputSingleSelector, "0x9e7defe6"},
		"clRouter ABI":               {[4]byte(clRouterABI.Methods["exactInputSingle"].ID), "0xa026383e"},
		"clQuoter ABI":               {[4]byte(clQuoterABI.Methods["quoteExactInputSingle"].ID), "0x9e7defe6"},
		"clFactory ABI":              {[4]byte(clFactoryABI.Methods["getPool"].ID), "0x28af8d0b"},
		"SBRCL swapBridge":           {[4]byte(sbrCLABI.Methods["swapBridgeExactInputSingle"].ID), "0x49d9a7f7"},
		"SBRCL swapBridgeETH":        {[4]byte(sbrCLABI.Methods["swapBridgeExactInputSingleETH"].ID), "0xa26337a6"},
		"SBRCL getAmountSwapBridge":  {[4]byte(sbrCLABI.Methods["getAmountSwapBridgeOut"].ID), "0xdf7da91c"},
		"SBRCL getExpectedBridgeAmt": {[4]byte(sbrCLABI.Methods["getExpectedBridgeAmount"].ID), hexOf(sbrABI.Methods["getExpectedBridgeAmount"].ID)},
		"SBRCL bridge()":             {[4]byte(sbrCLABI.Methods["bridge"].ID), hexOf(sbrABI.Methods["bridge"].ID)},
	} {
		if hexOf(c.got[:]) != c.want {
			t.Errorf("%s: %s != %s", name, hexOf(c.got[:]), c.want)
		}
	}
	// the SwapBridge event (handle building / recovery) is identical in both wrappers
	if sbrCLABI.Events["SwapBridge"].ID != sbrABI.Events["SwapBridge"].ID {
		t.Fatal("SwapBridgeRouterCL.SwapBridge topic differs from SwapBridgeRouter.SwapBridge")
	}
}

func TestCLEncodingVectorsMatchCast(t *testing.T) {
	x1 := func(dex DexKind, fee uint32, ts int32) string {
		b, err := EncodeX1ExtraData(X1Input{Router: vecC, TokenIn: vecA, TokenOut: vecB, Fee: fee, Recipient: vecC, Deadline: 1_800_000_000, AmountIn: n18(100), AmountOutMinimum: n18(99), Dex: dex, TickSpacing: ts})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b[:20], vecC.Bytes()) {
			t.Fatal("extraData must start with the router")
		}
		return hexOf(b[20:])
	}
	for name, c := range map[string]struct{ got, want string }{
		"CL exactInputSingle ts=200":      {x1(DexCL, 0, 200), castCLExactInputSingle200},
		"CL exactInputSingle ts=int24min": {x1(DexCL, 0, MinInt24), castCLExactInputSingleMin},
		"CL exactInputSingle ts=int24max": {x1(DexCL, 0, MaxInt24), castCLExactInputSingleMax},
		"V3 exactInputSingle fee=500":     {x1(DexV3, 500, 0), castV3ExactInputSingle500},
		"default dex is V3":               {x1("", 500, 0), castV3ExactInputSingle500},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", name, c.got, c.want)
		}
	}
	q := func(ts int64) string {
		b, err := clQuoterABI.Pack("quoteExactInputSingle", clQuoteArgs{vecA, vecB, e18, big.NewInt(ts), new(big.Int)})
		if err != nil {
			t.Fatal(err)
		}
		return hexOf(b)
	}
	if q(200) != castCLQuote200 || q(-1) != castCLQuoteNeg1 {
		t.Fatalf("CL quoteExactInputSingle vectors differ from cast:\n%s\n%s", q(200), q(-1))
	}
	sb := func(native bool, ts int32) string {
		b, err := EncodeSwapBridge(SwapBridgeInput{TokenIn: vecA, TokenOut: vecB, AmountIn: n18(100), AmountOutMinimum: n18(99), ToChainID: 56, Recipient: vecC, Deadline: 1_800_000_000, Native: native, Dex: DexCL, TickSpacing: ts})
		if err != nil {
			t.Fatal(err)
		}
		return hexOf(b)
	}
	if got := sb(false, 200); got != castSBRCL200 {
		t.Errorf("SwapBridgeRouterCL.swapBridgeExactInputSingle:\n got %s\nwant %s", got, castSBRCL200)
	}
	if got := sb(true, -100); got != castSBRCLETHNeg100 {
		t.Errorf("SwapBridgeRouterCL.swapBridgeExactInputSingleETH:\n got %s\nwant %s", got, castSBRCLETHNeg100)
	}
	g := func(ts int64) string {
		b, err := sbrCLABI.Pack("getAmountSwapBridgeOut", big.NewInt(56), vecA, vecB, big.NewInt(ts), e18)
		if err != nil {
			t.Fatal(err)
		}
		return hexOf(b)
	}
	if g(200) != castGetAmountOut200 || g(-100) != castGetAmountOutNeg100 {
		t.Fatalf("getAmountSwapBridgeOut(int24) vectors differ from cast:\n%s\n%s", g(200), g(-100))
	}
	// the int24 range is enforced, never truncated
	for _, ts := range []int32{MaxInt24 + 1, MinInt24 - 1} {
		if _, err := EncodeX1ExtraData(X1Input{Router: vecC, TokenIn: vecA, TokenOut: vecB, Recipient: vecC, AmountIn: e18, AmountOutMinimum: e18, Dex: DexCL, TickSpacing: ts}); err == nil {
			t.Errorf("X1 tickSpacing %d must be refused", ts)
		}
		if _, err := EncodeSwapBridge(SwapBridgeInput{TokenIn: vecA, TokenOut: vecB, AmountIn: e18, AmountOutMinimum: e18, Recipient: vecC, Dex: DexCL, TickSpacing: ts}); err == nil {
			t.Errorf("X3 tickSpacing %d must be refused", ts)
		}
	}
	if _, err := EncodeX1ExtraData(X1Input{Router: vecC, TokenIn: vecA, TokenOut: vecB, Recipient: vecC, AmountIn: e18, AmountOutMinimum: e18, Dex: "v4"}); err == nil {
		t.Error("an unknown dex must be refused")
	}
	// structural decoding: a negative / zero tickSpacing is not a real pool
	neg, _ := hex.DecodeString(strings.TrimPrefix(castCLExactInputSingleMin, "0x"))
	if _, _, err := decodeX1Struct(append(vecC.Bytes(), neg...)); err == nil || !strings.Contains(err.Error(), "tickSpacing") {
		t.Fatalf("a negative tickSpacing must fail structural X1 decoding, got %v", err)
	}
	ok, _ := hex.DecodeString(strings.TrimPrefix(castCLExactInputSingle200, "0x"))
	x, _, err := decodeX1Struct(append(vecC.Bytes(), ok...))
	if err != nil || x.Dex != DexCL || x.TickSpacing == nil || *x.TickSpacing != 200 || x.Fee != 0 {
		t.Fatalf("CL decode: %+v %v", x, err)
	}
}

func TestDexConfigDefaulting(t *testing.T) {
	main, _ := GetNetwork(Mainnet)
	test, _ := GetNetwork(Testnet)
	if d, err := main.Dex(); err != nil || d != DexV3 || main.Chains[ChainOne].Swap.Dex != "" || main.Chains[ChainOne].Swap.CL != nil {
		t.Fatalf("mainnet has no dex key and must default to v3: %v %v", d, err)
	}
	if d, err := test.Dex(); err != nil || d != DexV3 {
		t.Fatalf("testnet stays on v3: %v %v", d, err)
	}
	cl := test.Chains[ChainOne].Swap.CL
	if cl == nil || cl.SwapRouter == nil || cl.QuoterV2 == nil || cl.Factory == nil || cl.SwapBridgeRouter != nil || cl.X1Pool == nil || cl.X1Pool.TickSpacing != 200 {
		t.Fatalf("testnet cl block: %+v", cl)
	}
	if !strings.Contains(test.Chains[ChainOne].Todo["swap.cl.swapBridgeRouter"], "TODO(PENDING)") {
		t.Fatal("the CL wrapper must stay null with a TODO(PENDING) note")
	}
	if v := test.ConfigValue("one.swap.cl.swapRouter"); v == nil || *v != *cl.SwapRouter {
		t.Fatal("ConfigValue one.swap.cl.swapRouter")
	}
	if test.ConfigValue("one.swap.cl.swapBridgeRouter") != nil || main.ConfigValue("one.swap.cl.swapRouter") != nil {
		t.Fatal("unset CL values resolve to nil")
	}
	// a V3 swap block without dex / cl keeps its JSON shape
	raw, _ := json.Marshal(main.Chains[ChainOne].Swap)
	if strings.Contains(string(raw), `"dex"`) || strings.Contains(string(raw), `"cl"`) {
		t.Fatalf("V3 swap JSON gained keys: %s", raw)
	}
	// an unknown dex is refused when the client is created
	bad := deepCopyNet(t, main)
	bad.Chains[ChainOne].Swap.Dex = "v4"
	if _, err := NewClient(ctx, Mainnet, &ClientOptions{Networks: bad}); err == nil || !strings.Contains(err.Error(), "dex") {
		t.Fatalf("unknown dex must fail NewClient, got %v", err)
	}
}

// The testnet CL block does not change any testnet route: X1 stays closed and X3 still uses the V3 SwapBridgeRouter.
func TestTestnetCLBlockKeepsV3Routes(t *testing.T) {
	w := testnetWorld(t)
	c := w.client()
	var anc *AddressNotConfiguredError
	_, err := c.Quote(ctx, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: senderAdr, Recipient: recipient})
	if !errors.As(err, &anc) || anc.Field != "one.tokens.ONEUSD" {
		t.Fatalf("testnet X1 must stay refused on one.tokens.ONEUSD, got %v", err)
	}
	q := mustQuote(t, c, x3Req(w, senderAdr, 50))
	if q.Tx.To != common.HexToAddress(*w.net.Chains[ChainOne].Swap.SwapBridgeRouter) || !bytes.Equal(q.Tx.Data[:4], sbrABI.Methods["swapBridgeExactInputSingle"].ID) {
		t.Fatal("testnet X3 must use the V3 SwapBridgeRouter")
	}
	if strings.Contains(q.Seal.Payload, "tickSpacing") {
		t.Fatal("a V3 quote payload must not carry tickSpacing")
	}
	// the same testnet switched to dex "cl": X3 is refused on the missing CL wrapper (TODO(PENDING)), X1 on the missing ONEUSD
	flip := deepCopyNet(t, w.net)
	flip.Chains[ChainOne].Swap.Dex = DexCL
	fc, err := NewClient(ctx, Testnet, &ClientOptions{Networks: flip, RPCs: w.opts().RPCs})
	if err != nil {
		t.Fatal(err)
	}
	ts := int32(200)
	r := x3Req(w, senderAdr, 50)
	r.X3.TickSpacing = &ts
	if _, err := fc.Quote(ctx, r); !errors.As(err, &anc) || anc.Field != "one.swap.cl.swapBridgeRouter" || !strings.Contains(anc.Todo, "TODO(PENDING)") {
		t.Fatalf("CL X3 without the CL wrapper must be AddressNotConfiguredError(one.swap.cl.swapBridgeRouter), got %v", err)
	}
	if _, err := fc.Quote(ctx, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: senderAdr, Recipient: recipient}); !errors.As(err, &anc) || anc.Field != "one.tokens.ONEUSD" {
		t.Fatalf("CL X1 on testnet must stay refused, got %v", err)
	}
}

// clNet: the mainnet configuration switched to dex "cl" with fake CL contracts (tick spacing 200).
func clNet(t *testing.T) *NetworkData {
	base, err := GetNetwork(Mainnet)
	if err != nil {
		t.Fatal(err)
	}
	n := deepCopyNet(t, base)
	s := func(h string) *string { v := fakeAdr(h); return &v }
	n.Chains[ChainOne].Swap.Dex = DexCL
	n.Chains[ChainOne].Swap.CL = &CLSwapConfig{SwapRouter: s("c1a0001"), QuoterV2: s("c1a0002"), Factory: s("c1a0003"), SwapBridgeRouter: s("c1a0004"),
		X1Pool: &CLX1Pool{TokenIn: "USDTx", TokenOut: "ONEUSD", TickSpacing: 200, Pool: fakeAdr("c1a0005")}}
	return n
}

func clX1World(t *testing.T) (*world, *ecdsa.PrivateKey, common.Address, *Client) {
	w := newWorldNet(t, Mainnet, clNet(t))
	ch := w.net.Chains
	hubID := ch[ChainOne].ChainID
	usdt, usdtx := tok(w, ChainBSC, "USDT"), tok(w, ChainOne, "USDTx")
	w.setPair(ChainBSC, hubID, usdt, usdtx, true)
	w.setFee(ChainBSC, hubID, usdt, n18(5), new(big.Int), new(big.Int))
	w.whitelist[key(ChainOne, *ch[ChainOne].Swap.CL.SwapRouter)] = true
	w.whitelist[key(ChainOne, *ch[ChainOne].Bridge)] = true
	k, a := w.fundedWallet()
	w.setBal(ChainBSC, usdt, a, n18(1000))
	w.x1Out = add(n18(100), big.NewInt(3_511_957))
	return w, k, a, w.client()
}

func TestCLX1Flow(t *testing.T) {
	w, k, a, c := clX1World(t)
	clRouter := common.HexToAddress(*w.net.Chains[ChainOne].Swap.CL.SwapRouter)
	q := mustQuote(t, c, x1Req(a, SwapOptions{}))
	ed := decBridge(t, q.Tx.Data)[6].([]byte)
	if !bytes.Equal(ed[:20], clRouter.Bytes()) || !bytes.Equal(ed[20:24], CLExactInputSingleSelector[:]) {
		t.Fatalf("CL X1 extraData must be CL router || 0xa026383e, got %x", ed[:24])
	}
	x, _, err := decodeX1Struct(ed)
	if err != nil || x.Dex != DexCL || *x.TickSpacing != 200 || x.AmountIn != q.Value.String() {
		t.Fatalf("decoded CL X1: %+v %v", x, err)
	}
	if len(w.clTicks) != 1 || w.clTicks[0] != 200 {
		t.Fatalf("the CL quoter must be called with tickSpacing 200, got %v", w.clTicks)
	}
	accepted(t, q, "CL X1")
	pf, err := c.Preflight(ctx, q)
	if err != nil || !pf.OK {
		t.Fatalf("preflight: %+v %v", pf, err)
	}
	if w.clTicks[len(w.clTicks)-1] != 200 {
		t.Fatal("preflight must call CL factory.getPool with tickSpacing 200")
	}
	// the executor whitelist is checked for the CL router
	delete(w.whitelist, key(ChainOne, clRouter))
	if pf, _ = c.Preflight(ctx, q); !hasIssue(pf, "EXECUTOR_NOT_WHITELISTED") {
		t.Fatal("missing EXECUTOR_NOT_WHITELISTED for the CL router")
	}
	w.whitelist[key(ChainOne, clRouter)] = true
	w.poolMissing = true
	if pf, _ = c.Preflight(ctx, q); !hasIssue(pf, "POOL_NOT_FOUND") {
		t.Fatal("missing POOL_NOT_FOUND from the CL factory")
	}
	w.poolMissing = false

	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.HandleFromTx(ctx, 56, sent.Hash, nil)
	if err != nil || h.Route != RouteBSCUSDTToONEUSD || h.Expect.X1 == nil || h.Expect.X1.Dex != DexCL || h.Expect.X1.TickSpacing == nil || *h.Expect.X1.TickSpacing != 200 {
		t.Fatalf("CL X1 handle: %+v %v", h, err)
	}
	raw, _ := json.Marshal(h.Expect.X1)
	if !strings.Contains(string(raw), `"dex":"cl"`) || !strings.Contains(string(raw), `"tickSpacing":200`) {
		t.Fatalf("CL X1 handle JSON: %s", raw)
	}
	var rt TransferHandle
	hraw, _ := json.Marshal(h)
	if err := json.Unmarshal(hraw, &rt); err != nil || rt.Expect.X1.Dex != DexCL || *rt.Expect.X1.TickSpacing != 200 {
		t.Fatalf("CL handle JSON round-trip: %v", err)
	}

	// tracking: the hub's ExtraCallExecuted must be the CL selector on the CL router
	tx := newTx()
	s := map[common.Hash]string{tx: sel("finalizeBridgeBatch")}
	ret := common.LeftPadBytes(n18(9).Bytes(), 32)
	amt := bigOf(h.Expect.X1.AmountIn)
	in := func(selector [4]byte) classifyInput {
		return classifyInput{kind: HopX1, selectorOf: selOf(s), hub: hubAddr, x1: h.Expect.X1, logs: []RawLog{
			finalizedLog(t, 56, 0, tx, 0, amt, usdtxAdr, recipient), extraLogTo(t, 56, 0, true, amt, ret, tx, 1, clRouter, selector)}}
	}
	if r := clsIn(t, in(CLExactInputSingleSelector)); r.state != StateDelivered || r.amount != n18(9).String() {
		t.Fatalf("CL X1 delivered: %+v", r)
	}
	if r := clsIn(t, in(ExactInputSingleSelector)); r.state != StateProcessedUnverified {
		t.Fatalf("a V3 selector on a CL handle must not count as delivered: %+v", r)
	}

	// V3 handles keep their JSON shape (no dex / tickSpacing)
	vw, vk, va, vc := x1WorldFor(t)
	vs, err := vc.Send(ctx, vw.signer(vk, ChainBSC), mustQuote(t, vc, x1Req(va, SwapOptions{})), SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	vh, err := vc.HandleFromTx(ctx, 56, vs.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	vraw, _ := json.Marshal(vh.Expect.X1)
	if strings.Contains(string(vraw), "dex") || strings.Contains(string(vraw), "tickSpacing") {
		t.Fatalf("V3 X1 handle JSON changed: %s", vraw)
	}
}

func TestCLWrongSelectorRejected(t *testing.T) {
	clw, _, ca, clc := clX1World(t)
	v3w, _, va, v3c := x1WorldFor(t)
	clQ := mustQuote(t, clc, x1Req(ca, SwapOptions{}))
	v3Q := mustQuote(t, v3c, x1Req(va, SwapOptions{}))
	clED := decBridge(t, clQ.Tx.Data)[6].([]byte)
	v3ED := decBridge(t, v3Q.Tx.Data)[6].([]byte)
	// handle build / inference decoders: the other DEX's selector is refused with a clear message
	if _, err := checkX1Net(clw.net, v3ED, v3Q.Value); err == nil || !strings.Contains(err.Error(), `configured dex is "cl"`) {
		t.Fatalf("V3 extraData against a CL config: %v", err)
	}
	if _, err := checkX1Net(v3w.net, clED, clQ.Value); err == nil || !strings.Contains(err.Error(), `configured dex is "v3"`) {
		t.Fatalf("CL extraData against a V3 config: %v", err)
	}
	// CL selector re-targeted at the V3 config's own router: still refused (selector / dex mismatch)
	v3Router := common.HexToAddress(*v3w.net.Chains[ChainOne].Swap.V3SwapRouter)
	retarget := append(append([]byte{}, v3Router.Bytes()...), clED[20:]...)
	var ur *UnsupportedRouteError
	if _, err := checkX1Net(v3w.net, retarget, clQ.Value); !errors.As(err, &ur) || !strings.Contains(err.Error(), "dex") {
		t.Fatalf("CL call on the V3 router: %v", err)
	}
	// inference from a receipt: a CL exactInputSingle to the V3 router of a V3 client is unsupported
	bsc := v3w.chains[ChainBSC]
	bridge := common.HexToAddress(*v3w.net.Chains[ChainBSC].Bridge)
	txh := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, v3w.net.Chains[ChainOne].ChainID, 0, va, retarget, bridge, clQ.Value)})
	if _, err := v3c.HandleFromTx(ctx, 56, txh, nil); !errors.As(err, &ur) || !strings.Contains(err.Error(), "dex") {
		t.Fatalf("inference must refuse the CL selector on a V3 client: %v", err)
	}
	// client binding: a CL quote given to a V3 client (and vice versa) is a configuration mismatch
	if _, _, _, _, _, err := v3c.BuildTx(clQ); !isMismatch(err) {
		t.Fatalf("CL quote on a V3 client: %v", err)
	}
	if _, _, _, _, _, err := clc.BuildTx(v3Q); !isMismatch(err) {
		t.Fatalf("V3 quote on a CL client: %v", err)
	}
	// a CL client whose quote extraData was re-encoded with the V3 selector (same router) is refused by the binding check
	clRouter := common.HexToAddress(*clw.net.Chains[ChainOne].Swap.CL.SwapRouter)
	p, _ := decodeX1Call(clED)
	asV3, _ := EncodeX1ExtraData(X1Input{Router: clRouter, TokenIn: p.TokenIn, TokenOut: p.TokenOut, Fee: 0, Recipient: p.Recipient, Deadline: p.Deadline.Uint64(), AmountIn: p.AmountIn, AmountOutMinimum: p.AmountOutMinimum})
	if _, _, _, _, _, err := clc.BuildTx(withExtra(t, clQ, asV3)); !isMismatch(err) || !strings.Contains(err.Error(), "selector") {
		t.Fatalf("V3 selector on a CL client: %v", err)
	}
}

func clX3World(t *testing.T) (*world, *ecdsa.PrivateKey, common.Address, *Client) {
	n := deepCopyNet(t, mustNet(t, Testnet))
	s := func(h string) *string { v := fakeAdr(h); return &v }
	n.Chains[ChainOne].Swap.Dex = DexCL
	n.Chains[ChainOne].Swap.CL.SwapBridgeRouter = s("c1a0004")
	w := testnetWorldNet(t, n)
	k, a := w.fundedWallet()
	w.setBal(ChainOne, tokenA, a, n18(1000))
	return w, k, a, w.client()
}

func mustNet(t *testing.T, name NetworkName) *NetworkData {
	n, err := GetNetwork(name)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCLX3Flow(t *testing.T) {
	w, k, a, c := clX3World(t)
	sbrCL := common.HexToAddress(*w.net.Chains[ChainOne].Swap.CL.SwapBridgeRouter)
	ts := int32(200)
	req := x3Req(w, a, 50)
	req.X3.TickSpacing = &ts
	q := mustQuote(t, c, req)
	ts = 1 // the quote snapshot must not follow the caller's pointer
	if q.Request.X3.TickSpacing == nil || *q.Request.X3.TickSpacing != 200 {
		t.Fatal("the request snapshot must deep-copy X3.TickSpacing")
	}
	if q.Tx.To != sbrCL || q.SourceContract != sbrCL || q.Approval == nil || q.Approval.Spender != sbrCL {
		t.Fatalf("CL X3 must target SwapBridgeRouterCL: to=%s", q.Tx.To)
	}
	p, err := decodeSwapBridgeCall(q.Tx.Data, false, DexCL)
	if err != nil || p.TickSpacing.Int64() != 200 || hexOf(q.Tx.Data[:4]) != "0x49d9a7f7" {
		t.Fatalf("CL X3 calldata: %v", err)
	}
	if len(w.clTicks) == 0 || w.clTicks[0] != 200 {
		t.Fatalf("SwapBridgeRouterCL.getAmountSwapBridgeOut must get tickSpacing 200: %v", w.clTicks)
	}
	if !strings.Contains(q.Seal.Payload, `"tickSpacing":200`) {
		t.Fatalf("CL X3 payload must carry tickSpacing: %s", q.Seal.Payload)
	}
	accepted(t, q, "CL X3")
	if pf, err := c.Preflight(ctx, q); err != nil || !pf.OK {
		t.Fatalf("CL X3 preflight: %+v %v", pf, err)
	}
	// payload edits are caught by the seal rules
	invalidPayload(t, mustOpen(mutPayload(t, q, func(m map[string]any) { subm(subm(m, "request"), "x3")["tickSpacing"] = json.Number("100") })), "tx.data")
	invalidPayload(t, mustOpen(mutPayload(t, q, func(m map[string]any) { subm(subm(m, "request"), "x3")["tickSpacing"] = json.Number("-200") })), "request.x3.tickSpacing")
	invalidPayload(t, mustOpen(mutPayload(t, q, func(m map[string]any) { delete(subm(subm(m, "request"), "x3"), "tickSpacing") })), "tx.data")

	sent, err := c.Send(ctx, w.signer(k, ChainOne), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.HandleFromTx(ctx, w.net.Chains[ChainOne].ChainID, sent.Hash, nil)
	if err != nil || h.Route != RouteSwapBridge || !strings.EqualFold(h.SrcContract, sbrCL.Hex()) {
		t.Fatalf("CL X3 handle: %+v %v", h, err)
	}

	// request validation against the DEX
	var io *InvalidOptionError
	r := x3Req(w, a, 50)
	if _, err := c.Quote(ctx, r); !errors.As(err, &io) || !strings.Contains(io.Msg, "TickSpacing is required") {
		t.Fatalf("CL X3 without TickSpacing: %v", err)
	}
	for _, bad := range []int32{0, -200, MaxInt24 + 1} {
		v := bad
		r.X3.TickSpacing = &v
		if _, err := c.Quote(ctx, r); !errors.As(err, &io) {
			t.Fatalf("CL X3 TickSpacing %d must be refused, got %v", bad, err)
		}
	}
	v := int32(200)
	r.X3.TickSpacing, r.X3.PoolFee = &v, 500
	if _, err := c.Quote(ctx, r); !errors.As(err, &io) || !strings.Contains(io.Msg, "PoolFee must be 0") {
		t.Fatalf("CL X3 with PoolFee: %v", err)
	}
	vw := testnetWorld(t)
	vr := x3Req(vw, senderAdr, 50)
	vr.X3.TickSpacing = &v
	if _, err := vw.client().Quote(ctx, vr); !errors.As(err, &io) || !strings.Contains(io.Msg, `only valid on a network whose dex is "cl"`) {
		t.Fatalf("V3 X3 with TickSpacing: %v", err)
	}
	// a CL X3 quote handed to a V3 client is a configuration mismatch
	if _, _, _, _, _, err := vw.client().BuildTx(q); !isMismatch(err) {
		t.Fatalf("CL X3 quote on a V3 client: %v", err)
	}
}

func mustOpen(q *Quote) error {
	_, err := openQuote(q)
	return err
}
