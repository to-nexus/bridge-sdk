package bridgesdk

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Recovery of a transfer whose outcome is unknown: FindSourceTransfer matching and the replacement analysis.

func TestFindSourceTransferFullFieldMatch(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	bridge := common.HexToAddress(*w.net.Chains[ChainBSC].Bridge)
	w.setAllow(ChainBSC, tok(w, ChainBSC, "ONE"), a, bridge, n18(1000))
	w.chains[ChainBSC].sendMode = "throw-no-mine"
	_, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{})
	var bu *BroadcastOutcomeUnknownError
	if !errors.As(err, &bu) {
		t.Fatalf("want unknown outcome, got %v", err)
	}
	w.chains[ChainBSC].sendMode = ""
	// same account, same toChainID, DIFFERENT value: must NOT match
	w.chains[ChainBSC].applyTx(a, bridge, bridgeCall(w, add(n18(10), big.NewInt(1))), new(big.Int), nil)
	f, _ := c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint)
	if f.Status != FindNotFoundYet || f.MinedNonce <= f.PendingNonceBefore {
		t.Fatalf("different value must not match: %+v", f)
	}
	// two identical requests -> AMBIGUOUS
	w.chains[ChainBSC].applyTx(a, bridge, bridgeCall(w, n18(10)), new(big.Int), nil)
	w.chains[ChainBSC].applyTx(a, bridge, bridgeCall(w, n18(10)), new(big.Int), nil)
	f, _ = c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint)
	if f.Status != FindAmbiguous || len(f.Candidates) != 2 {
		t.Fatalf("want AMBIGUOUS: %+v", f)
	}
}

func TestReplacementAnalysis(t *testing.T) {
	mk := func() (*world, *Client, *SentTx, common.Address, common.Address) {
		w := testnetWorld(t)
		key, a := w.fundedWallet()
		c := w.client()
		q := mustQuote(t, c, r1Req(a))
		bridge := common.HexToAddress(*w.net.Chains[ChainBSC].Bridge)
		w.setAllow(ChainBSC, tok(w, ChainBSC, "ONE"), a, bridge, n18(1000))
		w.chains[ChainBSC].sendMode = "pending"
		sent, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{})
		if err != nil {
			t.Fatal(err)
		}
		w.chains[ChainBSC].sendMode = ""
		return w, c, sent, a, bridge
	}
	an := func(c *Client, s *SentTx, rep common.Hash) *ReplacementVerdict {
		v, err := c.AnalyzeReplacement(ctx, ReplacementParams{Intent: s.Intent, Checkpoint: s.Checkpoint, OriginalHash: s.Hash, OriginalNonce: s.Nonce, ReplacementHash: rep})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	{ // nonce not consumed
		_, c, s, _, _ := mk()
		if v := an(c, s, common.Hash{}); v.Verdict != VerdictOriginalStillPossible {
			t.Fatalf("%+v", v)
		}
	}
	{ // 0-value self transfer (cancel)
		w, c, s, a, _ := mk()
		n := s.Nonce
		rep := w.chains[ChainBSC].applyTx(a, a, nil, new(big.Int), &n)
		if v := an(c, s, rep); v.Verdict != VerdictNewTxAllowed || v.Reason != "replacement-not-a-transfer" {
			t.Fatalf("%+v", v)
		}
		if v := an(c, s, common.Hash{}); v.Verdict != VerdictNewTxAllowed { // located by the bounded block scan
			t.Fatalf("scan: %+v", v)
		}
	}
	{ // failed replacement
		w, c, s, a, bridge := mk()
		n := s.Nonce
		rep := w.chains[ChainBSC].applyTx(a, bridge, bridgeCall(w, big.NewInt(1)), new(big.Int), &n)
		if v := an(c, s, rep); v.Verdict != VerdictNewTxAllowed || v.Reason != "replacement-failed" {
			t.Fatalf("%+v", v)
		}
	}
	{ // speed-up: same transfer
		w, c, s, a, bridge := mk()
		n := s.Nonce
		rep := w.chains[ChainBSC].applyTx(a, bridge, s.Intent.Data, new(big.Int), &n)
		if v := an(c, s, rep); v.Verdict != VerdictFound || v.Handle == nil || v.ReplacementHash != rep {
			t.Fatalf("%+v", v)
		}
	}
	{ // different transfer
		w, c, s, a, bridge := mk()
		n := s.Nonce
		rep := w.chains[ChainBSC].applyTx(a, bridge, bridgeCall(w, add(n18(10), big.NewInt(7))), new(big.Int), &n)
		if v := an(c, s, rep); v.Verdict != VerdictReplacedByDifferentTransfer {
			t.Fatalf("%+v", v)
		}
	}
	{ // nonce consumed by something we cannot locate: NOT safe
		w, c, s, a, _ := mk()
		w.chains[ChainBSC].nonces[a] = s.Nonce + 1
		if v := an(c, s, common.Hash{}); v.Verdict != VerdictOriginalStillPossible {
			t.Fatalf("%+v", v)
		}
	}
}

// fee decrease adopted at requote, then the broadcast response is lost: recovery must use the ADOPTED fee fields, not the first quote's.
func TestRecoveryUsesAdoptedFees(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	hubID := w.net.Chains[ChainOne].ChainID
	one := tok(w, ChainBSC, "ONE")
	w.setFee(ChainBSC, hubID, one, n18(1), n18(2), new(big.Int))
	q := mustQuote(t, c, r1Req(a))
	// the approve is mined inside SendTransaction (which then sees sendMode), so flip the mode only for the bridge broadcast
	var bu *BroadcastOutcomeUnknownError
	w.onApprove = func() { w.setFee(ChainBSC, hubID, one, n18(1), n18(1), new(big.Int)) }
	_, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{OnSubmitting: func(*SubmittedIntent, Checkpoint) error {
		w.chains[ChainBSC].sendMode = "throw-after-mine"
		return nil
	}})
	if !errors.As(err, &bu) {
		t.Fatalf("want unknown outcome, got %v", err)
	}
	if bu.Intent.Match.NetworkFee.Cmp(n18(1)) != 0 {
		t.Fatalf("intent must carry the adopted fee, got %s", bu.Intent.Match.NetworkFee)
	}
	w.chains[ChainBSC].sendMode = ""
	f, err := c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint)
	if err != nil || f.Status != FindFound {
		t.Fatalf("find with adopted fees: %+v %v", f, err)
	}
}

// Source-transfer matching: fees match when event.fee <= intent.fee (BaseBridge emits the verifier's calculated fees, which are
// always <= the calldata fees of a successful tx); every other field must match exactly.
func TestFindSourceTransferFeeRule(t *testing.T) {
	mk := func() (*world, *Client, *BroadcastOutcomeUnknownError, func(to common.Address, value, nf *big.Int), common.Address) {
		w := testnetWorld(t)
		key, a := w.fundedWallet()
		c := w.client()
		hubID := w.net.Chains[ChainOne].ChainID
		one := tok(w, ChainBSC, "ONE")
		bridge := common.HexToAddress(*w.net.Chains[ChainBSC].Bridge)
		w.setFee(ChainBSC, hubID, one, n18(1), n18(2), new(big.Int))
		q := mustQuote(t, c, r1Req(a))
		w.setAllow(ChainBSC, one, a, bridge, new(big.Int).Lsh(big.NewInt(1), 100))
		w.chains[ChainBSC].sendMode = "throw-no-mine"
		_, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{})
		var bu *BroadcastOutcomeUnknownError
		if !errors.As(err, &bu) || bu.Intent.Match.NetworkFee.Cmp(n18(2)) != 0 {
			t.Fatalf("want unknown outcome with intent nf=2, got %v", err)
		}
		w.chains[ChainBSC].sendMode = ""
		tx := func(to common.Address, value, nf *big.Int) {
			d, _ := EncodeBridgeToken(BridgeTokenCall{ToChainID: uint64(hubID), FromToken: one, To: to, Value: value, NetworkFee: nf, ExFee: new(big.Int)})
			w.chains[ChainBSC].applyTx(a, bridge, d, new(big.Int), nil)
		}
		return w, c, bu, tx, one
	}
	nd, _ := GetNetwork(Testnet)
	hubID := nd.Chains[ChainOne].ChainID
	{ // (1) fees dropped before inclusion: event nf = 1 < intent nf = 2 -> FOUND
		w, c, bu, tx, one := mk()
		w.setFee(ChainBSC, hubID, one, n18(1), n18(1), new(big.Int))
		tx(recipient, n18(10), n18(2))
		lg := w.chains[ChainBSC].logs[len(w.chains[ChainBSC].logs)-1]
		if e, ok := parseInitiated(lg); !ok || e.NetworkFee.Cmp(n18(1)) != 0 {
			t.Fatalf("event must carry the calculated (lower) fee")
		}
		if f, err := c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint); err != nil || f.Status != FindFound {
			t.Fatalf("lower event fee must match: %+v %v", f, err)
		}
	}
	{ // (2) event fee HIGHER than the intent (3 > 2): cannot be this transfer
		w, c, bu, tx, one := mk()
		w.setFee(ChainBSC, hubID, one, n18(1), n18(3), new(big.Int))
		tx(recipient, n18(10), n18(3))
		if f, _ := c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint); f.Status != FindNotFoundYet {
			t.Fatalf("higher fee must not match: %+v", f)
		}
	}
	{ // (3) equal fees, but to or value differs
		_, c, bu, tx, _ := mk()
		tx(r3Adr, n18(10), n18(2))
		tx(recipient, add(n18(10), big.NewInt(1)), n18(2))
		if f, _ := c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint); f.Status != FindNotFoundYet {
			t.Fatalf("different to/value must not match: %+v", f)
		}
	}
}
