package bridgesdk

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// HandleFromTx: the route is inferred from the SOURCE receipt alone; only logs of the configured bridge / router count; ambiguity is never resolved silently.

func TestHandleFromTxInfersAllNineRoutes(t *testing.T) {
	w := testnetWorld(t)
	k, a := w.fundedWallet()
	c := w.client()
	usdtx := tok(w, ChainCross, "USDTx")
	w.setFee(ChainCross, w.net.Chains[ChainBSC].ChainID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	w.swapOut = n18(100)
	base := QuoteRequest{Amount: n18(10), Sender: a, Recipient: recipient}
	with := func(r Route, f func(*QuoteRequest)) QuoteRequest {
		q := base
		q.Route = r
		if f != nil {
			f(&q)
		}
		return q
	}
	reqs := []QuoteRequest{
		with(RouteBSCToOne, nil), with(RouteOneToBSC, nil), with(RouteOneToHyperEVM, nil), with(RouteHyperEVMToOne, nil),
		with(RouteBSCToOneToHyperEVM, nil), with(RouteBSCToOneToHyperEVM, func(q *QuoteRequest) { q.Hypercore = true }),
		with(RouteHyperEVMToOneToBSC, nil), with(RouteOneToHyperCore, nil),
		with(RouteSwapBridge, func(q *QuoteRequest) {
			q.Amount = n18(50)
			q.X3 = &X3Request{TokenIn: tokenA, TokenOut: usdtx, ToChain: ChainBSC}
		}),
	}
	for _, r := range reqs {
		q := mustQuote(t, c, r)
		sent, err := c.Send(ctx, w.signer(k, q.SourceChain), q, SendOptions{})
		if err != nil {
			t.Fatalf("%s send: %v", r.Route, err)
		}
		built, err := c.WaitHandle(ctx, sent)
		if err != nil {
			t.Fatal(err)
		}
		inf, err := c.HandleFromTx(ctx, q.SourceChainID, sent.Hash, nil)
		if err != nil {
			t.Fatalf("%s infer: %v", r.Route, err)
		}
		if inf.Route != q.Route || inf.Hop1.Kind != q.Details.Hop1Kind {
			t.Fatalf("%s inferred %s/%s", q.Route, inf.Route, inf.Hop1.Kind)
		}
		sameHandle(t, inf, built)
		if h, err := c.HandleFromTx(ctx, q.SourceChainID, sent.Hash, &HandleOptions{Route: q.Route}); err != nil || h.Route != q.Route {
			t.Fatalf("matching explicit route: %v", err)
		}
	}
	// X1 (mainnet world)
	xw, xk, xa, xc := x1WorldFor(t)
	xq := mustQuote(t, xc, x1Req(xa, SwapOptions{}))
	xs, err := xc.Send(ctx, xw.signer(xk, ChainBSC), xq, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := xc.HandleFromTx(ctx, 56, xs.Hash, nil)
	if err != nil || h.Route != RouteBSCUSDTToONEUSD || h.Hop1.Kind != HopX1 {
		t.Fatalf("X1: %+v %v", h, err)
	}
}

func TestHandleFromTxTrustAndAmbiguity(t *testing.T) {
	w := testnetWorld(t)
	_, a := w.fundedWallet()
	c := w.client()
	bsc := w.chains[ChainBSC]
	bridge := common.HexToAddress(*w.net.Chains[ChainBSC].Bridge)
	hubID := w.net.Chains[ChainCross].ChainID
	forger := common.HexToAddress("0x00000000000000000000000000000000f04ce001")
	var nf *SourceTransferNotFoundError
	t1 := bsc.mineLogs(forger, nil, []types.Log{biLog(t, hubID, 0, a, nil, forger, n18(10))})
	if _, err := c.HandleFromTx(ctx, 97, t1, nil); !errors.As(err, &nf) {
		t.Fatalf("forged emitter only must be NotFound, got %v", err)
	}
	t2 := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 99, a, nil, forger, n18(10)), biLog(t, hubID, 7, a, nil, bridge, n18(10))})
	if h, err := c.HandleFromTx(ctx, 97, t2, nil); err != nil || h.Hop1.Index != "7" || h.Route != RouteBSCToOne {
		t.Fatalf("forged log must be ignored: %v", err)
	}
	t3 := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 5, a, nil, bridge, n18(10)), biLog(t, hubID, 6, a, nil, bridge, n18(10))})
	var amb *AmbiguousSourceError
	if _, err := c.HandleFromTx(ctx, 97, t3, nil); !errors.As(err, &amb) || len(amb.Candidates) != 2 || amb.Candidates[0].Index.Int64() != 5 || amb.Candidates[1].LogIndex != 1 {
		t.Fatalf("want AmbiguousSourceError with 2 candidates, got %v", err)
	}
	if h, err := c.HandleFromTx(ctx, 97, t3, &HandleOptions{Index: big.NewInt(6)}); err != nil || h.Hop1.Index != "6" {
		t.Fatalf("index selector: %v", err)
	}
	zero := uint(0)
	if h, err := c.HandleFromTx(ctx, 97, t3, &HandleOptions{LogIndex: &zero}); err != nil || h.Hop1.Index != "5" {
		t.Fatalf("logIndex selector: %v", err)
	}
	if _, err := c.HandleFromTx(ctx, 97, t3, &HandleOptions{Index: big.NewInt(42)}); !errors.As(err, &nf) {
		t.Fatalf("a selector matching none is an error, got %v", err)
	}
	t4 := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 3, a, nil, bridge, n18(10)), biLog(t, hubID, 3, a, nil, bridge, add(n18(10), big.NewInt(1)))})
	if _, err := c.HandleFromTx(ctx, 97, t4, &HandleOptions{Index: big.NewInt(3)}); !errors.As(err, &amb) {
		t.Fatalf("same index twice stays ambiguous: %v", err)
	}
	one := uint(1)
	if h, err := c.HandleFromTx(ctx, 97, t4, &HandleOptions{Index: big.NewInt(3), LogIndex: &one}); err != nil || h.Expect.Value != add(n18(10), big.NewInt(1)).String() {
		t.Fatalf("index+logIndex: %v", err)
	}
}

func TestHandleFromTxUnsupportedMismatchAndFailed(t *testing.T) {
	w := testnetWorld(t)
	k, a := w.fundedWallet()
	c := w.client()
	bsc := w.chains[ChainBSC]
	ch := w.net.Chains
	bridge := common.HexToAddress(*ch[ChainBSC].Bridge)
	hubID := ch[ChainCross].ChainID
	hub := common.HexToAddress(*ch[ChainCross].Bridge)
	fwd := func(dest uint64, inner []byte) []byte {
		ed, err := EncodeForwardExtraData(ForwardInput{Hub: hub, DestChainID: dest, ToDest: recipient, Value2: n18(10), NetworkFee2: new(big.Int), ExFee2Declared: new(big.Int), Inner: inner})
		if err != nil {
			t.Fatal(err)
		}
		return ed
	}
	var us *UnsupportedRouteError
	bad := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 0, a, fwd(1, nil), bridge, n18(10))})
	if _, err := c.HandleFromTx(ctx, 97, bad, nil); !errors.As(err, &us) {
		t.Fatalf("bad nested destination: %v", err)
	}
	same := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 1, a, fwd(uint64(ch[ChainBSC].ChainID), nil), bridge, n18(10))})
	if _, err := c.HandleFromTx(ctx, 97, same, nil); !errors.As(err, &us) {
		t.Fatalf("BSC -> hub -> BSC: %v", err)
	}
	junk := make([]byte, 30)
	for i := range junk {
		junk[i] = 0xab
	}
	nested := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 2, a, fwd(uint64(ch[ChainHyperEVM].ChainID), junk), bridge, n18(10))})
	if _, err := c.HandleFromTx(ctx, 97, nested, nil); !errors.As(err, &us) {
		t.Fatalf("nested extraData: %v", err)
	}
	unk := append(common.HexToAddress("0x7777777777777777777777777777777777777777").Bytes(), 0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0)
	u := bsc.mineLogs(bridge, nil, []types.Log{biLog(t, hubID, 3, a, unk, bridge, n18(10))})
	if _, err := c.HandleFromTx(ctx, 97, u, nil); !errors.As(err, &us) || us.Selector != "0xdeadbeef" {
		t.Fatalf("unknown selector: %v", err)
	}
	q := mustQuote(t, c, r1Req(a))
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var rm *RouteMismatchError
	if _, err := c.HandleFromTx(ctx, 97, sent.Hash, &HandleOptions{Route: RouteOneToBSC}); !errors.As(err, &rm) || rm.Given != RouteOneToBSC || rm.Inferred != RouteBSCToOne {
		t.Fatalf("route override mismatch: %v", err)
	}
	failed := bsc.applyTx(a, bridge, bridgeCall(w, big.NewInt(1)), new(big.Int), nil)
	var sf *SourceTxFailedError
	if _, err := c.HandleFromTx(ctx, 97, failed, nil); !errors.As(err, &sf) {
		t.Fatalf("failed source: %v", err)
	}
	_ = crypto.Keccak256
}
