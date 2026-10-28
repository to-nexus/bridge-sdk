package bridgesdk

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Blocking preflight: every error issue stops Send before the approve and again after it (post-approve); an RPC error is itself blocking; the destination
// bridge pause, extraData limits and the X3 quote status are checked.

func TestPreflightBlocksBeforeApprove(t *testing.T) {
	t.Run("CORE_NOT_LINKED (R5+X2)", func(t *testing.T) {
		w, k, a, c := r3World(t)
		q := mustQuote(t, c, r5Core(a))
		w.hyperCoreSet = false
		_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
		if pe := asPreflight(t, err, StagePreApprove); !hasCode(pe, "CORE_NOT_LINKED") {
			t.Fatalf("codes %v", codesOf(pe))
		}
		if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
			t.Fatalf("something was sent: approves=%d raw=%d", len(w.approveCalls), totalRaw(w))
		}
	})
	t.Run("X1 router not whitelisted", func(t *testing.T) {
		w, k, a, c := x1WorldFor(t)
		q := mustQuote(t, c, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: a, Recipient: recipient})
		delete(w.whitelist, key(ChainOne, common.HexToAddress(*w.net.Chains[ChainOne].Swap.V3SwapRouter)))
		_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
		if pe := asPreflight(t, err, StagePreApprove); !hasCode(pe, "EXECUTOR_NOT_WHITELISTED") {
			t.Fatalf("codes %v", codesOf(pe))
		}
		if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
			t.Fatalf("something was sent")
		}
	})
	t.Run("destination RPC hangs -> PREFLIGHT_INCOMPLETE", func(t *testing.T) {
		w, k, a, _ := r3World(t)
		o := w.opts()
		o.CallTimeout = 40 * time.Millisecond
		c, _ := NewClient(ctx, Testnet, o)
		q := mustQuote(t, c, r5Core(a))
		w.chains[ChainHyperEVM].setHang("eth_call", true)
		_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
		pe := asPreflight(t, err, StagePreApprove)
		var inc *PreflightIssue
		for i := range pe.Issues {
			if pe.Issues[i].Code == "PREFLIGHT_INCOMPLETE" {
				inc = &pe.Issues[i]
			}
		}
		if inc == nil || inc.Check == "" || !strings.Contains(inc.Error, "timed out") {
			t.Fatalf("want PREFLIGHT_INCOMPLETE with check/error, got %+v", pe.Issues)
		}
		if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
			t.Fatalf("something was sent")
		}
	})
	t.Run("insufficient balance", func(t *testing.T) {
		w, k, a, c := r3World(t)
		q := mustQuote(t, c, r1Req(a))
		w.setBal(ChainBSC, tok(w, ChainBSC, "ONE"), a, big.NewInt(1))
		_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
		if pe := asPreflight(t, err, StagePreApprove); !hasCode(pe, "INSUFFICIENT_BALANCE") {
			t.Fatalf("codes %v", codesOf(pe))
		}
		if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
			t.Fatalf("something was sent")
		}
	})
}

func TestPreflightBlocksAfterApprove(t *testing.T) {
	for _, inject := range []string{"bridge", "pair"} {
		w, k, a, c := r3World(t)
		q := mustQuote(t, c, r1Req(a))
		hubID := w.net.Chains[ChainOne].ChainID
		w.onApprove = func() {
			if inject == "bridge" {
				w.paused[ChainBSC] = true
				return
			}
			kk := key(ChainBSC, hubID, tok(w, ChainBSC, "ONE"))
			p := w.pairs[kk]
			p.Paused = true
			w.pairs[kk] = p
		}
		_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
		pe := asPreflight(t, err, StagePostApprove)
		want := "BRIDGE_PAUSED"
		if inject == "pair" {
			want = "PAIR_PAUSED"
		}
		if !hasCode(pe, want) {
			t.Fatalf("%s: codes %v", inject, codesOf(pe))
		}
		if len(w.approveCalls) != 1 || w.chains[ChainBSC].rawSent != 1 || len(w.chains[ChainBSC].logs) != 0 {
			t.Fatalf("%s: want exactly the approve; approves=%d raw=%d logs=%d", inject, len(w.approveCalls), w.chains[ChainBSC].rawSent, len(w.chains[ChainBSC].logs))
		}
	}
}

func TestPreflightAllowanceWarnsThenBlocks(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, r1Req(a))
	rep, err := c.Preflight(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range rep.Issues {
		found = found || (i.Code == "APPROVE_NEEDED" && i.Level == "warn")
	}
	if !found {
		t.Fatalf("allowance must be a warning before the approve: %+v", rep.Issues)
	}
	if _, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{}); err != nil || len(w.approveCalls) != 1 {
		t.Fatalf("send must approve then proceed: %v", err)
	}
}

func TestPausedDestinationBlocksEveryRouteBeforeApprove(t *testing.T) {
	cases := []struct {
		name string
		req  QuoteRequest
		dest ChainKey
	}{
		{"R1", QuoteRequest{Route: RouteBSCToOne}, ChainOne}, {"R2", QuoteRequest{Route: RouteOneToBSC}, ChainBSC}, {"R3", QuoteRequest{Route: RouteOneToHyperEVM}, ChainHyperEVM},
		{"R4", QuoteRequest{Route: RouteHyperEVMToOne}, ChainOne}, {"R5", QuoteRequest{Route: RouteBSCToOneToHyperEVM}, ChainHyperEVM},
		{"R5+core", QuoteRequest{Route: RouteBSCToOneToHyperEVM, Hypercore: true}, ChainHyperEVM}, {"R6", QuoteRequest{Route: RouteHyperEVMToOneToBSC}, ChainBSC},
		{"X2", QuoteRequest{Route: RouteOneToHyperCore}, ChainHyperEVM},
		{"X3", QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), X3: &X3Request{TokenIn: tokenA, ToChain: ChainBSC}}, ChainBSC},
	}
	for _, cs := range cases {
		w, k, a, c := r3World(t)
		r := cs.req
		if r.Amount == nil {
			r.Amount = n18(10)
		}
		if r.X3 != nil {
			r.X3.TokenOut = tok(w, ChainOne, "USDTx")
		}
		r.Sender, r.Recipient = a, recipient
		q := mustQuote(t, c, r)
		if q.Details.DestChain != cs.dest {
			t.Fatalf("%s: dest %s", cs.name, q.Details.DestChain)
		}
		w.paused[cs.dest] = true
		signer := w.signer(k, q.SourceChain)
		_, err := c.Send(ctx, signer, q, SendOptions{})
		destRefused(t, err, StagePreApprove, "DEST_BRIDGE_PAUSED", "")
		if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
			t.Fatalf("%s: something was sent", cs.name)
		}
	}
}

func TestDestinationRPCErrorOrWrongChainIsBlocking(t *testing.T) {
	w, k, a, _ := r3World(t)
	o := w.opts()
	o.CallTimeout = 40 * time.Millisecond
	c, _ := NewClient(ctx, Testnet, o)
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient})
	w.chains[ChainHyperEVM].setHang("eth_call", true) // the destination RPC never answers
	_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	destRefused(t, err, StagePreApprove, "PREFLIGHT_INCOMPLETE", "DEST_BRIDGE_PAUSED")
	if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
		t.Fatal("something was sent")
	}
	w2, k2, a2, c2 := r3World(t)
	q2 := mustQuote(t, c2, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a2, Recipient: recipient})
	o2 := w2.opts()
	o2.RPCs[ChainHyperEVM] = newFakeChain(1) // another chain
	other, _ := NewClient(ctx, Testnet, o2)
	_, err = other.Send(ctx, w2.signer(k2, ChainBSC), q2, SendOptions{})
	destRefused(t, err, StagePreApprove, "PREFLIGHT_INCOMPLETE", "DEST_BRIDGE_PAUSED")
	if len(w2.approveCalls) != 0 || totalRaw(w2) != 0 {
		t.Fatal("something was sent")
	}
}

func TestDestinationPauseBetweenPreAndPostApprove(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient})
	w.onApprove = func() { w.paused[ChainHyperEVM] = true }
	_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	destRefused(t, err, StagePostApprove, "DEST_BRIDGE_PAUSED", "")
	if len(w.approveCalls) != 1 || w.chains[ChainBSC].rawSent != 1 || len(w.chains[ChainBSC].logs) != 0 {
		t.Fatalf("want exactly the approve: approves=%d raw=%d", len(w.approveCalls), w.chains[ChainBSC].rawSent)
	}
}

func TestNoDestinationIssueWhenEveryBridgeRuns(t *testing.T) {
	_, _, a, c := r3World(t)
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient})
	rep, err := c.Preflight(ctx, q)
	if err != nil || hasIssue(rep, "DEST_BRIDGE_PAUSED") {
		t.Fatalf("%v %+v", err, rep.Issues)
	}
	ok := false
	for _, ch := range rep.Checks {
		ok = ok || (ch.Name == "destination bridge paused()" && ch.OK)
		if strings.Contains(strings.ToLower(ch.Name), "destination pair") {
			t.Fatalf("the destination pair check must not be extended: %s", ch.Name)
		}
	}
	if !ok {
		t.Fatal("the destination pause check did not run")
	}
}

func TestPreflightStructuredIssues(t *testing.T) {
	w := testnetWorld(t)
	_, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient, Hypercore: true})
	pf, err := c.Preflight(ctx, q)
	if err != nil || !pf.OK {
		t.Fatalf("clean preflight expected: %+v %v", pf, err)
	}
	w.paused[ChainOne] = true
	delete(w.whitelist, key(ChainHyperEVM, tok(w, ChainHyperEVM, "ONE")))
	w.hyperCoreSet = false
	w.setBal(ChainBSC, tok(w, ChainBSC, "ONE"), a, big.NewInt(1))
	pf, _ = c.Preflight(ctx, q)
	codes := map[string]bool{}
	for _, i := range pf.Issues {
		codes[i.Code] = true
	}
	for _, want := range []string{"HUB_PAUSED", "EXECUTOR_NOT_WHITELISTED", "CORE_NOT_LINKED", "INSUFFICIENT_BALANCE"} {
		if !codes[want] {
			t.Fatalf("missing %s in %v", want, codes)
		}
	}
	if pf.OK {
		t.Fatal("must not be ok")
	}
}

func TestHubLimitAppliesToInnerPayloadSourceLimitToOuter(t *testing.T) {
	w, _, a, c := r3World(t)
	q := mustQuote(t, c, r5Core(a))
	if len(q.Details.ExtraData) != 344 {
		t.Fatalf("outer extraData is %d B, want 344", len(q.Details.ExtraData))
	}
	w.maxExtra[ChainOne] = big.NewInt(128)
	rep, _ := c.Preflight(ctx, q)
	for _, i := range rep.Issues {
		if strings.HasPrefix(i.Code, "EXTRADATA") {
			t.Fatalf("outer 344 B, inner 88 B, hub limit 128 must pass: %+v", i)
		}
	}
	// a canonical X2 inner payload is exactly 88 B (a padded inner is refused by the route rules), so the hub limit is lowered instead of padding
	w.maxExtra[ChainOne] = big.NewInt(64)
	rep, _ = c.Preflight(ctx, q)
	if !hasIssue(rep, "EXTRADATA_TOO_LONG_HUB") {
		t.Fatalf("inner 88 B, hub limit 64: %+v", rep.Issues)
	}
	w.maxExtra[ChainOne] = big.NewInt(1024)
	w.maxExtra[ChainBSC] = big.NewInt(100)
	rep, _ = c.Preflight(ctx, q)
	if !hasIssue(rep, "EXTRADATA_TOO_LONG_SOURCE") || hasIssue(rep, "EXTRADATA_TOO_LONG_HUB") {
		t.Fatalf("outer 344 B > source limit 100: %+v", rep.Issues)
	}
}

func TestX3QuoteStatusIsCheckedBeforeApprove(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, X3: &X3Request{TokenIn: tokenA, TokenOut: tok(w, ChainOne, "USDTx"), ToChain: ChainBSC}})
	st := uint8(5) // InvalidSwap
	w.swapStatus = &st
	_, err := c.Send(ctx, w.signer(k, ChainOne), q, SendOptions{})
	pe := asPreflight(t, err, StagePreApprove)
	if !hasCode(pe, "X3_QUOTE_InvalidSwap") {
		t.Fatalf("codes %v", codesOf(pe))
	}
	if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
		t.Fatalf("something was sent")
	}
}

func TestX3StatusFlippingDuringApproveIsStageRequote(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, X3: &X3Request{TokenIn: tokenA, TokenOut: tok(w, ChainOne, "USDTx"), ToChain: ChainBSC}})
	w.onApprove = func() { st := uint8(5); w.swapStatus = &st }
	_, err := c.Send(ctx, w.signer(k, ChainOne), q, SendOptions{})
	pe := asPreflight(t, err, StageRequote)
	if !hasCode(pe, "X3_QUOTE_InvalidSwap") || len(w.approveCalls) != 1 || len(w.chains[ChainOne].logs) != 0 {
		t.Fatalf("want exactly the approve and a requote-stage refusal: %v approves=%d", codesOf(pe), len(w.approveCalls))
	}
}
