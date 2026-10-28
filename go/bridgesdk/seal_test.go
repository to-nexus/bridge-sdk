package bridgesdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
)

// The quote seal: an unchanged quote (copy, JSON round trip) is accepted, any edit is refused with the offending path, and every public API that consumes a
// quote verifies the seal first.

func TestEditedLimitsAndRecipientAreRefusedAsModified(t *testing.T) {
	w, k, a, c := r3World(t)
	sg := w.signer(k, ChainBSC)
	q := mustQuote(t, c, r1Req(a))
	orig, err := q.ApprovedLimits()
	if err != nil {
		t.Fatal(err)
	}
	// loosen the limits, then raise the fees in the fake: the edited limits must NEVER become the requote baseline
	q.Limits.MaxTotalSpend.Add(q.Limits.MaxTotalSpend, n18(100))
	q.Limits.MinDelivered.SetInt64(0)
	w.setFee(ChainBSC, w.net.Chains[ChainOne].ChainID, tok(w, ChainBSC, "ONE"), n18(1), n18(3), new(big.Int))
	before := rpcCalls(w)
	_, err = c.Send(ctx, sg, q, SendOptions{})
	asSeal(t, err, SealModified, "limits.maxTotalSpend")
	if rpcCalls(w) != before {
		t.Fatalf("Send made RPC calls before the seal was verified")
	}
	nothingSent(t, w)
	// the sealed values are still readable and unchanged
	got, err := q.ApprovedLimits()
	if err != nil || got.MaxTotalSpend.Cmp(orig.MaxTotalSpend) != 0 || got.MinDelivered.Cmp(orig.MinDelivered) != 0 {
		t.Fatalf("ApprovedLimits must return the sealed values: %v %+v", err, got)
	}
	// edit the exported recipient only
	q2 := mustQuote(t, c, r1Req(a))
	q2.Request.Recipient = otherAdr
	_, err = c.Send(ctx, sg, q2, SendOptions{})
	asSeal(t, err, SealModified, "request.recipient")
	ar, err := q2.ApprovedRequest()
	if err != nil || ar.Recipient != recipient {
		t.Fatalf("ApprovedRequest must return the sealed request: %v %+v", err, ar.Recipient)
	}
	ar.Amount.SetInt64(1) // a deep copy: mutating it changes nothing
	if ar2, _ := q2.ApprovedRequest(); ar2.Amount.Cmp(n18(10)) != 0 {
		t.Fatalf("ApprovedRequest must return a deep copy")
	}
	nothingSent(t, w)
}

func TestMissingSealCopiesAndRoundTrip(t *testing.T) {
	w, k, a, c := r3World(t)
	sg := w.signer(k, ChainBSC)
	q := mustQuote(t, c, r1Req(a))
	before := rpcCalls(w)
	for name, bad := range map[string]*Quote{"struct literal": {}, "nil": nil, "no seal": func() *Quote { b := *q; b.Seal = QuoteSeal{}; return &b }()} {
		_, err := c.Send(ctx, sg, bad, SendOptions{})
		asSeal(t, err, SealMissing, "")
		_ = name
	}
	if rpcCalls(w) != before {
		t.Fatalf("RPC before the seal check")
	}
	nothingSent(t, w)
	// an unchanged value copy and an unchanged JSON round-trip are accepted
	cp := *q
	if s, err := c.Send(ctx, sg, &cp, SendOptions{}); err != nil || s.Hash == ([32]byte{}) {
		t.Fatalf("unchanged value copy: %v", err)
	}
	w2, k2, a2, c2 := r3World(t)
	q2 := mustQuote(t, c2, r1Req(a2))
	raw, err := json.Marshal(q2)
	if err != nil {
		t.Fatal(err)
	}
	var rt Quote
	if err := json.Unmarshal(raw, &rt); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Send(ctx, w2.signer(k2, ChainBSC), &rt, SendOptions{}); err != nil {
		t.Fatalf("unchanged JSON round-trip: %v", err)
	}
	// ... and the same round-trip with an edited recipient is refused
	w3, k3, a3, c3 := r3World(t)
	q3 := mustQuote(t, c3, r1Req(a3))
	raw, _ = json.Marshal(q3)
	var rt3 Quote
	_ = json.Unmarshal(raw, &rt3)
	rt3.Request.Recipient = otherAdr
	_, err = c3.Send(ctx, w3.signer(k3, ChainBSC), &rt3, SendOptions{})
	asSeal(t, err, SealModified, "request.recipient")
	nothingSent(t, w3)
}

func TestSealVersionSDKAndDigest(t *testing.T) {
	w, k, a, c := r3World(t)
	sg := w.signer(k, ChainBSC)
	q := mustQuote(t, c, r1Req(a))
	before := rpcCalls(w)
	cases := map[string]struct {
		mut    func(b *Quote)
		reason string
	}{
		"tampered digest":  {func(b *Quote) { b.Seal.Digest = "0x" + strings.Repeat("11", 32) }, SealCorrupt},
		"tampered payload": {func(b *Quote) { b.Seal.Payload += " " }, SealCorrupt},
		"v=2":              {func(b *Quote) { b.Seal.V = 2 }, SealUnsupportedVersion},
		"sdk js":           {func(b *Quote) { b.Seal.SDK = "js" }, SealUnsupportedSDK},
	}
	for name, cs := range cases {
		b := *q
		cs.mut(&b)
		_, err := c.Send(ctx, sg, &b, SendOptions{})
		var lv *LimitViolationError
		if !errors.As(err, &lv) || lv.Reason != cs.reason {
			t.Fatalf("%s: want %s, got %v", name, cs.reason, err)
		}
	}
	if rpcCalls(w) != before {
		t.Fatalf("RPC before the seal check")
	}
	nothingSent(t, w)
}

func TestInvalidPayloadsWithCorrectDigestNeverPanic(t *testing.T) {
	w, k, a, c := r3World(t)
	sg := w.signer(k, ChainBSC)
	q := mustQuote(t, c, r1Req(a))
	mut := func(f func(m map[string]any)) string {
		tree := jsonTree(q.Seal.Payload).(map[string]any)
		f(tree)
		var buf bytes.Buffer
		if err := writeCanonical(&buf, tree); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	sub := func(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }
	cases := []struct{ name, payload, path string }{
		{"malformed JSON", `{"a":`, "$"},
		{"not an object", `[]`, "$"},
		{"null", `null`, ""},
		{"trailing data", q.Seal.Payload + `{}`, "$"},
		{"missing required field", mut(func(m map[string]any) { delete(m, "sourceBridge") }), "sourceBridge"},
		{"missing nested required field", mut(func(m map[string]any) { delete(sub(m, "tx"), "data") }), "tx.data"},
		{"non-numeric amount", mut(func(m map[string]any) { m["value"] = "12x" }), "value"},
		{"negative amount", mut(func(m map[string]any) { m["totalSpend"] = "-5" }), "totalSpend"},
		{"leading zero", mut(func(m map[string]any) { m["value"] = "007" }), "value"},
		{"wrong type", mut(func(m map[string]any) { m["ttlMs"] = json.Number("30000") }), ""},
		{"malformed address", mut(func(m map[string]any) { m["sender"] = "0x12" }), "sender"},
		{"uppercase address", mut(func(m map[string]any) {
			m["sender"] = "0x" + strings.ToUpper(strings.TrimPrefix(m["sender"].(string), "0x"))
		}), "sender"},
		{"malformed hex", mut(func(m map[string]any) { sub(m, "tx")["data"] = "0xabc" }), "tx.data"},
		{"unknown network", mut(func(m map[string]any) { m["network"] = "devnet" }), "network"},
		{"unknown field", mut(func(m map[string]any) { m["extra"] = "1" }), ""},
		{"unknown match kind", mut(func(m map[string]any) { sub(m, "match")["kind"] = "other" }), "match.kind"},
	}
	before := rpcCalls(w)
	for _, cs := range cases {
		b := *q
		b.Seal.Payload = cs.payload
		b.Seal.Digest = digestOf(cs.payload)
		_, err := c.Send(ctx, sg, &b, SendOptions{})
		var lv *LimitViolationError
		if cs.name == "null" {
			if !errors.As(err, &lv) || lv.Reason != SealInvalidPayload {
				t.Fatalf("%s: %v", cs.name, err)
			}
			continue
		}
		if !errors.As(err, &lv) || lv.Reason != SealInvalidPayload || (cs.path != "" && lv.Path != cs.path) {
			t.Fatalf("%s: want invalid-payload at %q, got %v", cs.name, cs.path, err)
		}
	}
	if rpcCalls(w) != before {
		t.Fatalf("RPC before the seal check")
	}
	nothingSent(t, w)
}

func TestUnencodablePublicFieldIsModifiedWithPath(t *testing.T) {
	w, k, a, c := r3World(t)
	sg := w.signer(k, ChainBSC)
	q := mustQuote(t, c, r1Req(a))
	b := *q
	b.Value = nil
	_, err := c.Send(ctx, sg, &b, SendOptions{})
	asSeal(t, err, SealModified, "value")
	b2 := *q
	b2.Tx.Value = big.NewInt(-1)
	_, err = c.Send(ctx, sg, &b2, SendOptions{})
	asSeal(t, err, SealModified, "tx.value")
	nothingSent(t, w)
}

// ---- every public API that consumes a Quote verifies the seal first ------------------------------------------------------------------------
func TestEveryQuoteConsumerVerifiesTheSealFirst(t *testing.T) {
	w, k, a, c := r3World(t)
	sg := w.signer(k, ChainBSC)
	q := mustQuote(t, c, r1Req(a))
	bad := *q
	bad.Tx.Data = append([]byte{}, q.Tx.Data...)
	bad.Tx.Data[len(bad.Tx.Data)-1] ^= 0xff // modified calldata
	bad2 := *q
	bad2.Request.Recipient = otherAdr
	before := rpcCalls(w)
	for name, b := range map[string]*Quote{"calldata": &bad, "recipient": &bad2} {
		want := "tx.data"
		if name == "recipient" {
			want = "request.recipient"
		}
		if _, _, _, _, _, err := c.BuildTx(b); true {
			asSeal(t, err, SealModified, want)
		}
		_, err := c.Preflight(ctx, b)
		asSeal(t, err, SealModified, want)
		asSeal(t, c.Simulate(ctx, b), SealModified, want)
		_, err = c.EnsureAllowance(ctx, sg, b, SendOptions{})
		asSeal(t, err, SealModified, want)
	}
	if _, _, _, _, _, err := c.BuildTx(&Quote{}); true {
		asSeal(t, err, SealMissing, "")
	}
	if rpcCalls(w) != before {
		t.Fatalf("a consumer made RPC calls before the seal was verified")
	}
	nothingSent(t, w)
	// unchanged copies work
	cp := *q
	to, data, value, chainID, from, err := c.BuildTx(&cp)
	if err != nil || !bytes.Equal(data, q.Tx.Data) || to != q.Tx.To || value.Cmp(q.Tx.Value) != 0 || chainID != q.SourceChainID || from != q.Sender {
		t.Fatalf("BuildTx on an unchanged copy: %v", err)
	}
	if rep, err := c.Preflight(ctx, &cp); err != nil || !rep.OK {
		t.Fatalf("Preflight on an unchanged copy: %v", err)
	}
	var sr *SimulationRevertError
	if err := c.Simulate(ctx, &cp); err != nil && !errors.As(err, &sr) {
		t.Fatalf("Simulate must run on an unchanged copy: %v", err)
	}
	if len(w.approveCalls) != 0 {
		t.Fatalf("Preflight / Simulate must never approve")
	}
	if hs, err := c.EnsureAllowance(ctx, sg, &cp, SendOptions{}); err != nil || len(hs) != 1 || len(w.approveCalls) != 1 || len(w.chains[ChainBSC].logs) != 0 {
		t.Fatalf("EnsureAllowance on an unchanged copy: %v %d", err, len(hs))
	}
}

func TestSealVectorsReproduceByteForByte(t *testing.T) {
	raw, err := os.ReadFile("testdata/seal_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vec []sealVector
	if err := json.Unmarshal(raw, &vec); err != nil || len(vec) < 4 {
		t.Fatalf("vectors: %v (%d)", err, len(vec))
	}
	for _, v := range vec {
		var q Quote
		if err := json.Unmarshal(v.Quote, &q); err != nil {
			t.Fatalf("%s: %v", v.Family, err)
		}
		p1, err1 := sealPayload(&q)
		p2, err2 := sealPayload(&q)
		if err1 != nil || err2 != nil || p1 != p2 {
			t.Fatalf("%s: unstable encoding: %v %v", v.Family, err1, err2)
		}
		if p1 != v.Payload || digestOf(p1) != v.Digest {
			t.Fatalf("%s: payload/digest differ from the vector\n got %s\nwant %s", v.Family, p1, v.Payload)
		}
	}
}

func TestSealVectorsGenerator(t *testing.T) {
	if os.Getenv("UPDATE_SEAL_VECTORS") == "" {
		t.Skip("set UPDATE_SEAL_VECTORS=1 to regenerate testdata/seal_vectors.json")
	}
	var out []sealVector
	add := func(family string, q *Quote) {
		q.ID = "0x00000000000000aa"
		q.Seal = QuoteSeal{}
		p, err := sealPayload(q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(q)
		out = append(out, sealVector{family, b, p, digestOf(p)})
	}
	w, _, a, c := r3World(t)
	add("single R1 (erc20 source, approval)", mustQuote(t, c, r1Req(a)))
	add("single R2 (native source, no approval)", mustQuote(t, c, QuoteRequest{Route: RouteOneToBSC, Amount: n18(10), Sender: a, Recipient: recipient}))
	add("multihop R5 + X2 (hubForward, core)", mustQuote(t, c, r5Core(a)))
	add("x3 (swap match, erc20 in)", mustQuote(t, c, QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, X3: &X3Request{TokenIn: tokenA, TokenOut: tok(w, ChainOne, "USDTx"), ToChain: ChainBSC}}))
	xw, _, xa, xc := x1WorldFor(t)
	_ = xw
	add("x1 (swap deadline, amountOutMinimum)", mustQuote(t, xc, x1Req(xa, SwapOptions{})))
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile("testdata/seal_vectors.json", append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
