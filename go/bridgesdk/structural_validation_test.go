package bridgesdk

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Structural validation of a sealed quote payload: route-specific required fields, decoded-calldata consistency and time bounds. Every negative case carries a
// CORRECT digest, so only the schema / route rules can catch it.

// TestPositiveControls: every route, an X1 quote with a floor of 0 and the recipient overrides pass openQuote (value copy and JSON round trip too).
func TestPositiveControls(t *testing.T) {
	positiveEveryRoute(t)
	positiveFloorZeroAndRecipientOverrides(t)
}

func positiveEveryRoute(t *testing.T) {
	t.Helper()
	w, _, a, c := r3World(t)
	usdtx := tok(w, ChainOne, "USDTx")
	base := QuoteRequest{Amount: n18(10), Sender: a, Recipient: recipient}
	with := func(f func(r *QuoteRequest)) QuoteRequest { r := base; f(&r); return r }
	reqs := map[string]QuoteRequest{
		"R1": with(func(r *QuoteRequest) { r.Route = RouteBSCToOne }), "R2": with(func(r *QuoteRequest) { r.Route = RouteOneToBSC }),
		"R3": with(func(r *QuoteRequest) { r.Route = RouteOneToHyperEVM }), "R4": with(func(r *QuoteRequest) { r.Route = RouteHyperEVMToOne }),
		"R5":      with(func(r *QuoteRequest) { r.Route = RouteBSCToOneToHyperEVM }),
		"R5+core": with(func(r *QuoteRequest) { r.Route, r.Hypercore = RouteBSCToOneToHyperEVM, true }),
		"R6":      with(func(r *QuoteRequest) { r.Route = RouteHyperEVMToOneToBSC }), "X2": with(func(r *QuoteRequest) { r.Route = RouteOneToHyperCore }),
		"X3 erc20": with(func(r *QuoteRequest) {
			r.Route, r.Amount, r.X3 = RouteSwapBridge, n18(50), &X3Request{TokenIn: tokenA, TokenOut: usdtx, ToChain: ChainBSC}
		}),
		"X3 native hyperevm": with(func(r *QuoteRequest) {
			r.Route, r.Amount, r.X3 = RouteSwapBridge, n18(50), &X3Request{NativeIn: true, TokenOut: usdtx, ToChain: ChainHyperEVM}
		}),
	}
	for name, r := range reqs {
		accepted(t, mustQuote(t, c, r), name)
	}
	_, _, xa, xc := x1WorldFor(t)
	accepted(t, mustQuote(t, xc, x1Req(xa, SwapOptions{})), "X1")
}

func positiveFloorZeroAndRecipientOverrides(t *testing.T) {
	t.Helper()
	xw, _, xa, xc := x1WorldFor(t)
	xw.x1Out = big.NewInt(1) // floor(1 * 9950 / 10000) = 0
	q0 := mustQuote(t, xc, x1Req(xa, SwapOptions{}))
	if q0.Limits.AmountOutMinimum.Sign() != 0 {
		t.Fatalf("expected a floor of 0, got %s", q0.Limits.AmountOutMinimum)
	}
	accepted(t, q0, "X1 floor 0")
	rd := x1Req(xa, SwapOptions{})
	rd.Recipients = Recipients{Dest: destAdr}
	accepted(t, mustQuote(t, xc, rd), "X1 recipients.dest")
	_, _, a, c := r3World(t)
	accepted(t, mustQuote(t, c, QuoteRequest{Route: RouteOneToHyperCore, Amount: n18(10), Sender: a, Recipient: recipient, Recipients: Recipients{Core: coreAdr}}), "X2 R3 + recipients.core")
	accepted(t, mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient, Hypercore: true, Recipients: Recipients{Core: coreAdr, Dest: destAdr, OnHub: otherAdr}}), "X2 R5 + recipients")
}

func TestX1MissingFloorAndDeadline(t *testing.T) {
	w, k, a, c := x1WorldFor(t)
	q := mustQuote(t, c, x1Req(a, SwapOptions{}))
	sg := w.signer(k, ChainBSC)
	noFloor := cloneQuote(q)
	noFloor.Limits.AmountOutMinimum = nil // public fields AND (after re-seal) payload both omit the floor
	mustSeal(t, noFloor)
	noDeadline := cloneQuote(q)
	noDeadline.Deadline = 0
	mustSeal(t, noDeadline)
	cases := []struct {
		name string
		q    *Quote
		path string
	}{
		{"no amountOutMinimum (payload)", mutPayload(t, q, func(m map[string]any) { delete(subm(m, "limits"), "amountOutMinimum") }), "limits.amountOutMinimum"},
		{"no deadlineSec (payload)", mutPayload(t, q, func(m map[string]any) { delete(m, "deadlineSec") }), "deadlineSec"},
		{"deadline not after createdAt", mutPayload(t, q, func(m map[string]any) { m["deadlineSec"] = "1" }), "deadlineSec"},
		{"public fields AND payload omit the floor (a quote that was re-sealed after editing)", noFloor, "limits.amountOutMinimum"},
		{"public fields AND payload omit the deadline", noDeadline, "deadlineSec"},
		{"floor contradicts the calldata", mutPayload(t, q, func(m map[string]any) {
			l := subm(m, "limits")
			l["amountOutMinimum"] = new(big.Int).Add(bigOf(l["amountOutMinimum"].(string)), big.NewInt(1)).String()
		}), "details.extraData"},
	}
	before := rpcCalls(w)
	for _, cs := range cases {
		_, err := openQuote(cs.q)
		invalidPayload(t, err, cs.path)
		_, err = c.Send(ctx, sg, cs.q, SendOptions{})
		invalidPayload(t, err, cs.path)
	}
	if rpcCalls(w) != before {
		t.Fatalf("RPC before the seal was verified")
	}
	nothingSent(t, w)
}

func TestX1DecodedRecipientMustBeTheResolvedDestination(t *testing.T) {
	_, _, a, c := x1WorldFor(t)
	oneChain := c.net.Chains[ChainOne]
	newX1 := func(q *Quote, rcpt common.Address) []byte {
		ed, err := EncodeX1ExtraData(X1Input{Router: addr(oneChain.Swap.V3SwapRouter), TokenIn: tok2(c, "USDTx"), TokenOut: tok2(c, "ONEUSD"), Fee: oneChain.Swap.X1Pool.Fee, Recipient: rcpt, Deadline: q.Deadline, AmountIn: q.Value, AmountOutMinimum: q.Limits.AmountOutMinimum})
		if err != nil {
			t.Fatal(err)
		}
		return ed
	}
	q := mustQuote(t, c, x1Req(a, SwapOptions{}))
	_, err := openQuote(withExtra(t, q, newX1(q, otherAdr)))
	invalidPayload(t, err, "details.extraData")
	rd := x1Req(a, SwapOptions{})
	rd.Recipients = Recipients{Dest: destAdr}
	qd := mustQuote(t, c, rd)
	_, err = openQuote(withExtra(t, qd, newX1(qd, recipient))) // the default recipient is now the WRONG one
	invalidPayload(t, err, "details.extraData")
	if _, err := openQuote(withExtra(t, qd, newX1(qd, destAdr))); err != nil {
		t.Fatalf("the override recipient is the right one: %v", err)
	}
}

func TestX3MissingFieldsAndCalldataContradiction(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, X3: &X3Request{TokenIn: tokenA, TokenOut: tok(w, ChainOne, "USDTx"), ToChain: ChainBSC}})
	noMin := cloneQuote(q)
	noMin.Limits.MinBridgeValue = nil
	mustSeal(t, noMin)
	cases := []struct {
		name string
		q    *Quote
		path string
	}{
		{"no minBridgeValue", mutPayload(t, q, func(m map[string]any) { delete(subm(m, "limits"), "minBridgeValue") }), "limits.minBridgeValue"},
		{"no amountOutMinimum", mutPayload(t, q, func(m map[string]any) { delete(subm(m, "limits"), "amountOutMinimum") }), "limits.amountOutMinimum"},
		{"no deadlineSec", mutPayload(t, q, func(m map[string]any) { delete(m, "deadlineSec") }), "deadlineSec"},
		{"request recipient differs from the calldata", mutPayload(t, q, func(m map[string]any) { subm(m, "request")["recipient"] = "0x5555555555555555555555555555555555555555" }), "tx.data"},
		{"public fields AND payload omit minBridgeValue", noMin, "limits.minBridgeValue"},
	}
	before := rpcCalls(w)
	for _, cs := range cases {
		_, err := openQuote(cs.q)
		invalidPayload(t, err, cs.path)
		_, err = c.Send(ctx, w.signer(k, ChainOne), cs.q, SendOptions{})
		invalidPayload(t, err, cs.path)
	}
	if rpcCalls(w) != before {
		t.Fatalf("RPC before the seal was verified")
	}
	nothingSent(t, w)
}

func TestMultihopMissingFields(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient})
	noV2 := cloneQuote(q)
	noV2.V2 = nil
	mustSeal(t, noV2)
	cases := []struct {
		name string
		q    *Quote
		path string
	}{
		{"no v2", mutPayload(t, q, func(m map[string]any) { delete(m, "v2") }), "v2"},
		{"no hop2Fees", mutPayload(t, q, func(m map[string]any) { delete(m, "hop2Fees") }), "hop2Fees"},
		{"no hop2ExFeeDeclared", mutPayload(t, q, func(m map[string]any) { delete(m, "hop2ExFeeDeclared") }), "hop2ExFeeDeclared"},
		{"no hubVerifier", mutPayload(t, q, func(m map[string]any) { delete(m, "hubVerifier") }), "hubVerifier"},
		{"no details.hop2Kind", mutPayload(t, q, func(m map[string]any) { delete(subm(m, "details"), "hop2Kind") }), "details.hop2Kind"},
		{"v2 contradicts the forward payload", mutPayload(t, q, func(m map[string]any) { m["v2"] = new(big.Int).Add(bigOf(m["v2"].(string)), big.NewInt(1)).String() }), "details.extraData"},
		{"public fields AND payload omit v2", noV2, "v2"},
	}
	before := rpcCalls(w)
	for _, cs := range cases {
		_, err := openQuote(cs.q)
		invalidPayload(t, err, cs.path)
		_, err = c.Send(ctx, w.signer(k, ChainBSC), cs.q, SendOptions{})
		invalidPayload(t, err, cs.path)
	}
	if rpcCalls(w) != before {
		t.Fatalf("RPC before the seal was verified")
	}
	nothingSent(t, w)
}

func TestX2ZeroRecipientWrongAmountAndResolvedCore(t *testing.T) {
	w, _, a, c := r3World(t)
	he := common.HexToAddress(w.net.Chains[ChainHyperEVM].Tokens["ONE"].Address)
	enc := func(rcpt common.Address, v *big.Int) []byte {
		ed, err := EncodeCoreExtraData(CoreInput{Token: he, CoreRecipient: rcpt, Value: v})
		if err != nil {
			t.Fatal(err)
		}
		return ed
	}
	r3 := mustQuote(t, c, QuoteRequest{Route: RouteOneToHyperCore, Amount: n18(10), Sender: a, Recipient: recipient, Recipients: Recipients{Core: coreAdr}})
	for name, ed := range map[string][]byte{
		"R3 zero coreRecipient":                       enc(common.Address{}, r3.Value),
		"R3 wrong amount":                             enc(coreAdr, add(r3.Value, big.NewInt(1))),
		"R3 coreRecipient != resolved core recipient": enc(recipient, r3.Value),
	} {
		_, err := openQuote(withExtra(t, r3, ed))
		invalidPayload(t, err, "details.extraData")
		_ = name
	}
	// R5 + core: the inner payload is decoded from the sealed forward call
	r5 := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient, Hypercore: true, Recipients: Recipients{Core: coreAdr}})
	fwd, _ := decodeForward(r5.Details.ExtraData)
	mk := func(rcpt common.Address, v *big.Int) []byte {
		ed, err := EncodeForwardExtraData(ForwardInput{Hub: addr(w.net.Chains[ChainOne].Bridge), DestChainID: fwd.ToChainID.Uint64(), ToDest: fwd.To, Value2: fwd.Value, NetworkFee2: fwd.NetworkFee, ExFee2Declared: fwd.ExFee, Inner: enc(rcpt, v)})
		if err != nil {
			t.Fatal(err)
		}
		return ed
	}
	for name, ed := range map[string][]byte{"R5 zero coreRecipient": mk(common.Address{}, r5.V2), "R5 wrong amount": mk(coreAdr, add(r5.V2, big.NewInt(1))), "R5 coreRecipient != resolved": mk(recipient, r5.V2)} {
		_, err := openQuote(withExtra(t, r5, ed))
		invalidPayload(t, err, "details.extraData")
		_ = name
	}
}

func TestMsBoundsAndGetters(t *testing.T) {
	_, _, a, c := r3World(t)
	req := r1Req(a)
	req.QuoteTTL = 60 * time.Second
	q := mustQuote(t, c, req)
	maxMs := strconv.FormatInt(math.MaxInt64/int64(time.Millisecond), 10)
	over := new(big.Int).Add(bigOf(maxMs), big.NewInt(1)).String()
	parse := func(f func(m map[string]any)) error {
		_, err := parseQuoteWire(mutPayload(t, q, f).Seal.Payload)
		return err
	}
	for _, f := range []struct {
		name string
		set  func(m map[string]any, v string)
		path string
	}{
		{"ttlMs", func(m map[string]any, v string) { m["ttlMs"] = v }, "ttlMs"},
		{"request.quoteTtlMs", func(m map[string]any, v string) { subm(m, "request")["quoteTtlMs"] = v }, "request.quoteTtlMs"},
	} {
		if err := parse(func(m map[string]any) { f.set(m, maxMs) }); err != nil {
			t.Fatalf("%s: the bound must be accepted: %v", f.name, err)
		}
		for _, v := range []string{over, "0", "-1", "9223372036854775807", "99999999999999999999"} {
			err := parse(func(m map[string]any) { f.set(m, v) })
			var we *wireError
			if !errors.As(err, &we) || we.path != f.path {
				t.Fatalf("%s=%s: want a wire error at %s, got %v", f.name, v, f.path, err)
			}
		}
		// the getters return the error, never a wrapped (negative) duration
		bad := mutPayload(t, q, func(m map[string]any) { f.set(m, over) })
		if r, err := bad.ApprovedRequest(); err == nil || r.QuoteTTL != 0 {
			t.Fatalf("%s: ApprovedRequest must fail on bound+1, got %v %v", f.name, r.QuoteTTL, err)
		}
		if _, err := bad.ApprovedLimits(); err == nil {
			t.Fatalf("%s: ApprovedLimits must fail on bound+1", f.name)
		}
		invalidPayload(t, func() error { _, e := openQuote(bad); return e }(), f.path)
	}
	// createdAtMs: positive and inside the time.UnixMilli range
	for _, v := range []string{"0", "9223372036854775808", "99999999999999999999"} {
		if err := parse(func(m map[string]any) { m["createdAtMs"] = v }); err == nil {
			t.Fatalf("createdAtMs=%s must be refused", v)
		}
	}
}
