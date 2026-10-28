package bridgesdk

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// extraData is decoded, not trusted: delivery is reported to the DECODED recipient and token, and a call that differs from what the SDK builds is unsupported.

func TestX1InferenceRejectsUnsupportedCalls(t *testing.T) {
	w, _, a, c := x1WorldFor(t)
	cross := w.net.Chains[ChainCross]
	bridge := common.HexToAddress(*w.net.Chains[ChainBSC].Bridge)
	hubID := cross.ChainID
	fee := cross.Swap.X1Pool.Fee
	good := X1Input{Router: common.HexToAddress(*cross.Swap.V3SwapRouter), TokenIn: tok(w, ChainCross, "USDTx"), TokenOut: tok(w, ChainCross, "ONEUSD"), Fee: fee, Recipient: recipient, Deadline: 2_000_000_000, AmountIn: n18(100), AmountOutMinimum: big.NewInt(1)}
	idx := int64(0)
	at := func(extra []byte) common.Hash {
		idx++
		return w.chains[ChainBSC].mineLogs(bridge, nil, []types.Log{biLog(t, hubID, idx, a, extra, bridge, n18(100))})
	}
	enc := func(x X1Input) []byte {
		b, err := EncodeX1ExtraData(x)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if h, err := c.HandleFromTx(ctx, 56, at(enc(good)), nil); err != nil || h.Route != RouteBSCUSDTToONEUSD || h.Expect.X1 == nil || h.Expect.X1.Recipient != recipient.Hex() {
		t.Fatalf("the SDK-shaped call must be accepted: %v %+v", err, h)
	}
	cases := map[string][]byte{}
	g := good
	g.TokenOut = otherAdr
	cases["tokenOut"] = enc(g)
	g = good
	g.TokenIn = otherAdr
	cases["tokenIn"] = enc(g)
	g = good
	g.AmountIn = n18(99)
	cases["amountIn"] = enc(g)
	g = good
	g.Fee = fee + 1
	cases["fee"] = enc(g)
	cases["trailing"] = append(enc(good), 0xab)
	for name, ed := range cases {
		var ur *UnsupportedRouteError
		if _, err := c.HandleFromTx(ctx, 56, at(ed), nil); !errors.As(err, &ur) || !strings.Contains(ur.Msg, "unsupported X1 extraData") {
			t.Fatalf("%s: want UnsupportedRouteError, got %v", name, err)
		}
	}
}

func TestX2InferenceCommittedAmount(t *testing.T) {
	w, _, a, c := r3World(t)
	ch := w.net.Chains
	he := common.HexToAddress(ch[ChainHyperEVM].Tokens["ONE"].Address)
	hub := common.HexToAddress(*ch[ChainCross].Bridge)
	idx := int64(0)
	direct := func(encoded *big.Int) common.Hash {
		idx++
		ed, _ := EncodeCoreExtraData(CoreInput{Token: he, CoreRecipient: otherAdr, Value: encoded})
		return w.chains[ChainCross].mineLogs(hub, nil, []types.Log{biLog(t, ch[ChainHyperEVM].ChainID, idx, senderAdr, ed, hub, n18(10))})
	}
	h, err := c.HandleFromTx(ctx, ch[ChainCross].ChainID, direct(n18(10)), nil)
	if err != nil || h.Route != RouteOneToHyperCore || h.Expect.X2 == nil || h.Expect.X2.CoreRecipient != otherAdr.Hex() || h.Expect.X2.EncodedAmount != n18(10).String() {
		t.Fatalf("matching call: %v %+v", err, h)
	}
	var ur *UnsupportedRouteError
	if _, err := c.HandleFromTx(ctx, ch[ChainCross].ChainID, direct(add(n18(10), big.NewInt(1))), nil); !errors.As(err, &ur) || !strings.Contains(ur.Msg, "unsupported X2 extraData") {
		t.Fatalf("direct mismatch: %v", err)
	}
	// nested R5: the committed amount is the forward payload value
	bscBridge := common.HexToAddress(*ch[ChainBSC].Bridge)
	nested := func(v *big.Int) common.Hash {
		idx++
		inner, _ := EncodeCoreExtraData(CoreInput{Token: he, CoreRecipient: otherAdr, Value: v})
		fwd, _ := EncodeForwardExtraData(ForwardInput{Hub: hub, DestChainID: uint64(ch[ChainHyperEVM].ChainID), ToDest: recipient, Value2: n18(10), NetworkFee2: new(big.Int), ExFee2Declared: new(big.Int), Inner: inner})
		return w.chains[ChainBSC].mineLogs(bscBridge, nil, []types.Log{biLog(t, ch[ChainCross].ChainID, idx, a, fwd, bscBridge, add(n18(10), big.NewInt(1)))})
	}
	h5, err := c.HandleFromTx(ctx, ch[ChainBSC].ChainID, nested(n18(10)), nil)
	if err != nil || h5.Route != RouteBSCToOneToHyperEVM || h5.Expect.X2 == nil || h5.Expect.X2.CoreRecipient != otherAdr.Hex() {
		t.Fatalf("nested matching: %v %+v", err, h5)
	}
	if _, err := c.HandleFromTx(ctx, ch[ChainBSC].ChainID, nested(add(n18(10), big.NewInt(7))), nil); !errors.As(err, &ur) || !strings.Contains(ur.Msg, "unsupported X2 extraData") {
		t.Fatalf("nested mismatch: %v", err)
	}
}

func TestX3NonEmptyExtraDataIsUnsupportedAtInference(t *testing.T) {
	w, _, _, c := r3World(t)
	ch := w.net.Chains
	router := common.HexToAddress(*ch[ChainCross].Swap.SwapBridgeRouter)
	hub := common.HexToAddress(*ch[ChainCross].Bridge)
	usdtx := tok(w, ChainCross, "USDTx")
	pair := func(extra []byte) common.Hash {
		bi := w.initiatedLog(ChainCross, hub, uint64(ch[ChainBSC].ChainID), usdtx, nativeAdr, router, recipient, n18(48), new(big.Int), new(big.Int), extra)
		idx := new(big.Int).Sub(w.initIdx[key(ChainCross, ch[ChainBSC].ChainID)], big.NewInt(1))
		sb := w.encodeEvent(sbrABI, "SwapBridge", router, senderAdr, tokenA, usdtx, n18(50), n18(49), big.NewInt(ch[ChainBSC].ChainID), idx, n18(48), new(big.Int), new(big.Int))
		return w.chains[ChainCross].mineLogs(router, nil, []types.Log{sb, bi})
	}
	if h, err := c.HandleFromTx(ctx, ch[ChainCross].ChainID, pair(nil), nil); err != nil || h.Route != RouteSwapBridge {
		t.Fatalf("empty extraData must be accepted: %v", err)
	}
	var ur *UnsupportedRouteError
	if _, err := c.HandleFromTx(ctx, ch[ChainCross].ChainID, pair([]byte{0x12, 0x34}), nil); !errors.As(err, &ur) || !strings.Contains(ur.Msg, "X3 with non-empty extraData") {
		t.Fatalf("non-empty X3 extraData: %v", err)
	}
}

func TestX1TrackedDeliveryGoesToTheDecodedRecipient(t *testing.T) {
	w, k, a, c := x1WorldFor(t)
	ch := w.net.Chains
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: a, Recipient: otherAdr, QuoteTTL: 60 * time.Second})
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil || h.Expect.X1 == nil || h.Expect.X1.Recipient != otherAdr.Hex() || h.Expect.X1.AmountIn != n18(100).String() {
		t.Fatalf("handle: %v %+v", err, h.Expect)
	}
	hub := common.HexToAddress(*ch[ChainCross].Bridge)
	router := common.HexToAddress(*ch[ChainCross].Swap.V3SwapRouter)
	exec := common.HexToAddress("0x00000000000000000000000000000000000000e1")
	idx := parseU(h.Hop1.Index)
	w.finNext[key(ChainCross, ch[ChainBSC].ChainID)] = add(idx, big.NewInt(1))
	ret := common.LeftPadBytes(n18(99).Bytes(), 32)
	w.chains[ChainCross].mineLogs(hub, bridgeABI.Methods["finalizeBridgeBatch"].ID, []types.Log{
		encodeEventT(t, bridgeABI, "ExtraCallExecuted", hub, big.NewInt(ch[ChainBSC].ChainID), idx, router, ExactInputSingleSelector, true, n18(100), ret),
		encodeEventT(t, bridgeABI, "BridgeFinalized", hub, big.NewInt(ch[ChainBSC].ChainID), idx, tok(w, ChainCross, "USDTx"), exec, n18(100), big.NewInt(1)),
	})
	r, err := c.TrackOnce(ctx, h)
	if err != nil || r.State != StateDelivered || r.Hops[0].Recipient != otherAdr.Hex() || r.Hops[0].Amount != n18(99).String() || r.Hops[0].Detail["bridgeFinalizedTo"] != exec.Hex() {
		t.Fatalf("delivery must be reported to the decoded recipient: %v %+v", err, r)
	}
}
