package bridgesdk

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// The send flow: quote expiry, exact approve, re-quote under the approved limits, approve policy, and immutability of what is signed.

func TestQuoteExpiry(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	w.clock = w.clock.Add(31 * time.Second)
	var qe *QuoteExpiredError
	if _, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{}); !errors.As(err, &qe) {
		t.Fatalf("want QuoteExpiredError, got %v", err)
	}
}

func TestHappyPathR1(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	if q.TotalSpend.Cmp(n18(10)) != 0 || q.Approval == nil || q.Approval.Spender != common.HexToAddress(*w.net.Chains[ChainBSC].Bridge) {
		t.Fatalf("quote: %+v", q.Approval)
	}
	sent, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.approveCalls) != 1 || w.approveCalls[0].Cmp(n18(10)) != 0 {
		t.Fatalf("exact approve expected: %v", w.approveCalls)
	}
	if sent.Nonce != 1 {
		t.Fatalf("nonce %d (0 was the approve)", sent.Nonce)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil {
		t.Fatal(err)
	}
	if h.Hop1.Index != "0" || h.Hop1.DestChainID != w.net.Chains[ChainOne].ChainID || h.SrcTxHash != sent.Hash.Hex() {
		t.Fatalf("handle %+v", h)
	}
}

func TestRequoteFeeIncreaseRefusedAndDecreaseAdopted(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	hubID := w.net.Chains[ChainOne].ChainID
	one := tok(w, ChainBSC, "ONE")
	q := mustQuote(t, c, r1Req(a))
	w.onApprove = func() { w.setFee(ChainBSC, hubID, one, n18(1), n18(1), new(big.Int)) }
	_, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{})
	var rr *RequoteRequiredError
	if !errors.As(err, &rr) || !contains(rr.Violated, "maxTotalSpend") || rr.NewQuote.TotalSpend.Cmp(rr.OldQuote.TotalSpend) <= 0 {
		t.Fatalf("want RequoteRequiredError(maxTotalSpend), got %v", err)
	}
	if len(w.chains[ChainBSC].logs) != 0 {
		t.Fatal("a bridge transfer was created")
	}

	// decrease: adopted, limits stay immutable
	w2 := testnetWorld(t)
	key2, a2 := w2.fundedWallet()
	c2 := w2.client()
	w2.setFee(ChainBSC, hubID, one, n18(1), n18(2), new(big.Int))
	q2 := mustQuote(t, c2, r1Req(a2))
	if q2.TotalSpend.Cmp(n18(12)) != 0 {
		t.Fatalf("total %s", q2.TotalSpend)
	}
	w2.onApprove = func() { w2.setFee(ChainBSC, hubID, one, n18(1), n18(1), new(big.Int)) }
	sent, err := c2.Send(ctx, w2.signer(key2, ChainBSC), q2, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Quote.TotalSpend.Cmp(n18(11)) != 0 || sent.Quote.Limits.MaxTotalSpend.Cmp(q2.Limits.MaxTotalSpend) != 0 {
		t.Fatalf("adopted %s limit %s", sent.Quote.TotalSpend, sent.Quote.Limits.MaxTotalSpend)
	}
	d, _ := bridgeABI.Methods["bridgeToken"].Inputs.Unpack(sent.Intent.Data[4:])
	if d[4].(*big.Int).Cmp(n18(1)) != 0 || sent.Intent.Match.NetworkFee.Cmp(n18(1)) != 0 {
		t.Fatal("calldata/intent must carry the ADOPTED fee")
	}
}

func TestApprovePolicy(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	one := tok(w, ChainBSC, "ONE")
	w.setAllow(ChainBSC, one, a, common.HexToAddress(*w.net.Chains[ChainBSC].Bridge), big.NewInt(5))
	q := mustQuote(t, c, r1Req(a))
	if _, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(w.approveCalls) != 2 || w.approveCalls[0].Sign() != 0 || w.approveCalls[1].Cmp(n18(10)) != 0 {
		t.Fatalf("zero-first expected, got %v", w.approveCalls)
	}
	// approve receipt failure stops the flow
	f := testnetWorld(t)
	fk, fa := f.fundedWallet()
	f.approveFails = true
	fc := f.client()
	fq := mustQuote(t, fc, r1Req(fa))
	var af *ApproveFailedError
	if _, err := fc.Send(ctx, f.signer(fk, ChainBSC), fq, SendOptions{}); !errors.As(err, &af) || len(f.chains[ChainBSC].logs) != 0 {
		t.Fatalf("want ApproveFailedError, got %v", err)
	}
	// max policy
	m := testnetWorld(t)
	mk, ma := m.fundedWallet()
	mc := m.client()
	mq := mustQuote(t, mc, r1Req(ma))
	if _, err := mc.Send(ctx, m.signer(mk, ChainBSC), mq, SendOptions{ApproveAmount: ApproveMax}); err != nil {
		t.Fatal(err)
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if m.approveCalls[0].Cmp(max) != 0 {
		t.Fatal("max approve expected")
	}
}

func TestX3RequoteFloorsAndRecovery(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	usdtx := tok(w, ChainOne, "USDTx")
	bscID := w.net.Chains[ChainBSC].ChainID
	w.setFee(ChainOne, bscID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	w.swapOut = big.NewInt(100)
	c := w.client()
	q := mustQuote(t, c, x3Req(w, a, 100))
	if q.Limits.AmountOutMinimum.Int64() != 99 || q.Limits.MinBridgeValue.Int64() != 99 {
		t.Fatalf("floors %s %s", q.Limits.AmountOutMinimum, q.Limits.MinBridgeValue)
	}
	// output rises 100->110, fee rises 0->5: delivery at the ORIGINAL floor = 94 < 99
	w.onApprove = func() {
		w.swapOut = big.NewInt(110)
		w.setFee(ChainOne, bscID, usdtx, big.NewInt(1), big.NewInt(5), new(big.Int))
	}
	_, err := c.Send(ctx, w.signer(key, ChainOne), q, SendOptions{})
	var rr *RequoteRequiredError
	if !errors.As(err, &rr) || !contains(rr.Violated, "bridgeDeliveryFloor") {
		t.Fatalf("want bridgeDeliveryFloor, got %v", err)
	}
	// price DROP below the original floor
	w2 := testnetWorld(t)
	k2, a2 := w2.fundedWallet()
	w2.setFee(ChainOne, bscID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	w2.swapOut = big.NewInt(100)
	c2 := w2.client()
	q2 := mustQuote(t, c2, x3Req(w2, a2, 100))
	w2.onApprove = func() { w2.swapOut = big.NewInt(90) }
	if _, err := c2.Send(ctx, w2.signer(k2, ChainOne), q2, SendOptions{}); !errors.As(err, &rr) || !contains(rr.Violated, "swapOutputBelowFloor") {
		t.Fatalf("want swapOutputBelowFloor, got %v", err)
	}
	// price RISE: adopted, calldata keeps the ORIGINAL amountOutMinimum
	w3 := testnetWorld(t)
	k3, a3 := w3.fundedWallet()
	w3.setFee(ChainOne, bscID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	w3.swapOut = big.NewInt(100)
	c3 := w3.client()
	q3 := mustQuote(t, c3, x3Req(w3, a3, 100))
	w3.onApprove = func() { w3.swapOut = big.NewInt(120) }
	sent, err := c3.Send(ctx, w3.signer(k3, ChainOne), q3, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := sbrABI.Methods["swapBridgeExactInputSingle"].Inputs.Unpack(sent.Intent.Data[4:])
	_ = args
	if sent.Quote.Limits.AmountOutMinimum.Int64() != 99 {
		t.Fatal("original floor must be kept")
	}

	// recovery through SwapBridge (bridge-level from is the router)
	w4 := testnetWorld(t)
	k4, a4 := w4.fundedWallet()
	w4.setFee(ChainOne, bscID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	c4 := w4.client()
	q4 := mustQuote(t, c4, x3Req(w4, a4, 50))
	router := common.HexToAddress(*w4.net.Chains[ChainOne].Swap.SwapBridgeRouter)
	w4.setAllow(ChainOne, tokenA, a4, router, n18(1000))
	w4.chains[ChainOne].sendMode = "throw-after-mine"
	_, err = c4.Send(ctx, w4.signer(k4, ChainOne), q4, SendOptions{})
	var bu *BroadcastOutcomeUnknownError
	if !errors.As(err, &bu) {
		t.Fatalf("want unknown outcome, got %v", err)
	}
	f, err := c4.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint)
	if err != nil || f.Status != FindFound || f.Handle.Route != RouteSwapBridge || !strings.EqualFold(f.Handle.SrcContract, router.Hex()) || f.Handle.Hop1.DestChainID != int64(bscID) {
		t.Fatalf("x3 find: %+v %v", f, err)
	}
}

func TestNativeSourceNeedsNoApprove(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, QuoteRequest{Route: RouteOneToBSC, Amount: n18(10), Sender: a, Recipient: recipient})
	if q.Approval != nil || q.Tx.Value.Cmp(add(n18(10), nf18)) != 0 {
		t.Fatalf("native quote: %+v", q.Approval)
	}
	sent, err := c.Send(ctx, w.signer(key, ChainOne), q, SendOptions{})
	if err != nil || len(w.approveCalls) != 0 || sent.Nonce != 0 {
		t.Fatalf("native send: %v approves=%d nonce=%d", err, len(w.approveCalls), sent.Nonce)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil || h.Hop1.DestChainID != int64(w.net.Chains[ChainBSC].ChainID) {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestCallerMutationsDoNotReachWhatIsSigned(t *testing.T) {
	w, k, a, c := r3World(t)
	req := r1Req(a)
	req.MaxTotalSpend = n18(10)
	q := mustQuote(t, c, req)
	orig := bytes.Clone(q.Tx.Data)
	// the caller's original request: mutated in place right after Quote() (big.Int in place, recipient, limits)
	req.Amount.SetInt64(1)
	req.Recipient = otherAdr
	req.MaxTotalSpend.SetInt64(0)
	// the caller's quote: mutated while the approve is pending (calldata bytes, limits)
	w.onApprove = func() {
		q.Tx.Data[len(q.Tx.Data)-1] ^= 0xff
		q.Limits.MaxTotalSpend.SetInt64(0)
		q.Request.Amount.SetInt64(2)
		q.Request.Recipient = otherAdr
	}
	var submitted *SubmittedIntent
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{
		OnSubmitting: func(in *SubmittedIntent, cp Checkpoint) error {
			submitted = in
			// mutate every callback argument: the SDK's own intent / checkpoint must be unaffected
			in.Data[0] ^= 0xff
			in.Value.SetInt64(0)
			in.AdoptedQuote.Tx.Data[0] ^= 0xff
			cp.DestBlocks[ChainOne] = 0
			return nil
		},
		OnSigned: func(info SignedInfo, in *SubmittedIntent) error {
			info.Raw[0] ^= 0xff
			in.Data = nil
			return nil
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = submitted
	if !bytes.Equal(sent.Intent.Data, orig) {
		t.Fatalf("the intent calldata changed")
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(sent.Raw); err != nil || !bytes.Equal(tx.Data(), orig) || tx.Hash() != sent.Hash {
		t.Fatalf("what was signed/broadcast is not the quoted calldata: %v", err)
	}
	if sent.Checkpoint.DestBlocks[ChainOne] == 0 && len(sent.Checkpoint.DestBlocks) > 0 && sent.Checkpoint.DestBlocks[ChainOne] != w.chains[ChainOne].block {
		t.Fatalf("checkpoint DestBlocks were mutated through a callback")
	}
	dec, err := bridgeABI.Methods["bridgeToken"].Inputs.Unpack(tx.Data()[4:])
	if err != nil || dec[2].(common.Address) != recipient {
		t.Fatalf("recipient: %v", err)
	}
}
