package bridgesdk

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Recovery, tracking and waiting verify the chain of every RPC before reading any evidence: a wrong chain is a NetworkMismatchError, a chain id that cannot be
// verified (error or hang) stops all reads, and a replacement transaction must match chain id, sender and nonce.

func TestWrongChainRPCIsNeverEvidence(t *testing.T) {
	w, _, s, a := newPending(t)
	wrong := newFakeChain(1)
	wrong.keys[a] = w.chains[ChainBSC].keys[a]
	n := s.Nonce
	rep := wrong.applyTx(a, a, nil, new(big.Int), &n) // a same-account, same-nonce "cancel" tx on the OTHER chain
	wrong.nonces[a] = s.Nonce + 1
	fresh := clientOnWrongChain(w, wrong)
	for _, h := range []common.Hash{{}, rep} {
		v, err := analyzeP(fresh, s, h)
		if !isMismatch(err) || v != nil {
			t.Fatalf("want *NetworkMismatchError and no verdict, got %+v %v", v, err)
		}
	}
	if wrong.evidenceCount() != 0 || wrong.chainIDCalls == 0 {
		t.Fatalf("evidence was read from the wrong chain (non-chainId calls: %d, chainId calls: %d)", wrong.evidenceCount(), wrong.chainIDCalls)
	}
}

func TestReplacementWithAnotherOrNoChainIDIsNotEvidence(t *testing.T) {
	for _, how := range []string{"other-chain", "unprotected"} {
		w, c, s, a := newPending(t)
		n := s.Nonce
		to := a
		tx := types.NewTx(&types.LegacyTx{Nonce: n, GasPrice: big.NewInt(1), Gas: 300000, To: &to, Value: new(big.Int)})
		k := w.chains[ChainBSC].keys[a]
		var signed *types.Transaction
		var err error
		if how == "other-chain" {
			signed, err = types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(1)), k)
		} else {
			signed, err = types.SignTx(tx, types.HomesteadSigner{}, k)
		}
		if err != nil {
			t.Fatal(err)
		}
		rep := w.chains[ChainBSC].applyTxFake(a, signed)
		for _, h := range []common.Hash{rep, {}} {
			v, err := analyzeP(c, s, h)
			if err != nil || v.Verdict == VerdictNewTxAllowed || v.Verdict != VerdictOriginalStillPossible {
				t.Fatalf("%s: must not be NewTxAllowed: %+v %v", how, v, err)
			}
		}
	}
}

func TestNonTransferReplacementAllowsANewTx(t *testing.T) {
	w, c, s, a := newPending(t)
	n := s.Nonce
	rep := w.chains[ChainBSC].applyTx(a, a, nil, new(big.Int), &n)
	if v, err := analyzeP(c, s, rep); err != nil || v.Verdict != VerdictNewTxAllowed || v.Reason != "replacement-not-a-transfer" {
		t.Fatalf("%+v %v", v, err)
	}
	if v, err := analyzeP(c, s, common.Hash{}); err != nil || v.Verdict != VerdictNewTxAllowed {
		t.Fatalf("scan: %+v %v", v, err)
	}
}

func TestIntentChainMismatchIsRefusedBeforeAnyRead(t *testing.T) {
	w, c, s, _ := newPending(t)
	before := w.chains[ChainBSC].evidenceCount()
	bad := *s.Intent
	bad.ChainID = 1
	if _, err := c.AnalyzeReplacement(ctx, ReplacementParams{Intent: &bad, Checkpoint: s.Checkpoint, OriginalHash: s.Hash, OriginalNonce: s.Nonce}); !isMismatch(err) {
		t.Fatalf("intent chain id: %v", err)
	}
	if _, err := c.FindSourceTransfer(ctx, &bad, s.Checkpoint); !isMismatch(err) {
		t.Fatalf("FindSourceTransfer intent chain id: %v", err)
	}
	cp := s.Checkpoint
	cp.ChainID = 1
	if _, err := c.AnalyzeReplacement(ctx, ReplacementParams{Intent: s.Intent, Checkpoint: cp, OriginalHash: s.Hash, OriginalNonce: s.Nonce}); !isMismatch(err) {
		t.Fatalf("checkpoint chain id: %v", err)
	}
	if w.chains[ChainBSC].evidenceCount() != before {
		t.Fatal("RPC calls before the intent was verified")
	}
}

func TestOtherEntryPointsOnAWrongChainRPC(t *testing.T) {
	w, c, s, a := newPending(t)
	q := mustQuote(t, c, r1Req(a))
	mined, err := c.Send(ctx, w.signer(w.chains[ChainBSC].keys[a], ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.WaitHandle(ctx, mined)
	if err != nil {
		t.Fatal(err)
	}
	wrong := newFakeChain(1)
	fresh := clientOnWrongChain(w, wrong)
	if _, err := fresh.FindSourceTransfer(ctx, s.Intent, s.Checkpoint); !isMismatch(err) {
		t.Fatalf("FindSourceTransfer: %v", err)
	}
	if _, err := fresh.HandleFromTx(ctx, w.net.Chains[ChainBSC].ChainID, mined.Hash, nil); !isMismatch(err) {
		t.Fatalf("HandleFromTx: %v", err)
	}
	if _, err := fresh.TrackOnce(ctx, handle); !isMismatch(err) {
		t.Fatalf("TrackOnce: %v", err)
	}
	if _, err := fresh.Track(ctx, handle, TrackOpts{Timeout: 50 * 1e6, Poll: 10 * 1e6}); !isMismatch(err) {
		t.Fatalf("Track: %v", err)
	}
	if _, err := fresh.WaitHandle(ctx, s); !isMismatch(err) {
		t.Fatalf("WaitHandle: %v", err)
	}
	if _, err := fresh.Rebroadcast(ctx, ChainBSC, s.Raw); !isMismatch(err) {
		t.Fatalf("Rebroadcast: %v", err)
	}
	// only eth_chainId reached the wrong chain: no log / receipt / tx / nonce / block read, no send
	if wrong.evidenceCount() != 0 || wrong.rawCount() != 0 {
		t.Fatalf("evidence reads or a send reached the wrong chain: non-chainId calls=%d raw=%d", wrong.evidenceCount(), wrong.rawCount())
	}
}

func TestTrackOnceTrackAndEvalHopOnAnUnverifiableChain(t *testing.T) {
	for _, mode := range []string{"error", "hang"} {
		for _, chain := range []ChainKey{ChainBSC, ChainCross} { // SOURCE (receipt) and DESTINATION (evalHop)
			e := q1Setup(t)
			e.break_(chain, mode)
			ch := e.w.chains[chain]
			base := ch.evidenceCount()
			r, err := e.fresh().TrackOnce(ctx, e.handle)
			if err != nil || r.State != StateUnknown || !strings.HasPrefix(r.Hops[len(r.Hops)-1].Reason, wantReason(mode)) || r.TimedOut != (mode == "hang") {
				t.Fatalf("%s/%s TrackOnce: want UNKNOWN{%s}: %+v %v", mode, chain, wantReason(mode), r, err)
			}
			tr, err := e.fresh().Track(ctx, e.handle, TrackOpts{Timeout: 300 * time.Millisecond, Poll: 20 * time.Millisecond})
			if err != nil || tr.State != StateUnknown || !strings.HasPrefix(tr.Hops[len(tr.Hops)-1].Reason, wantReason(mode)) || tr.TimedOut != (mode == "hang") {
				t.Fatalf("%s/%s Track: want UNKNOWN{%s}: %+v %v", mode, chain, wantReason(mode), tr, err)
			}
			if got := ch.evidenceCount() - base; got != 0 && chain == ChainCross {
				t.Fatalf("%s/%s: %d evidence reads reached the unverified chain", mode, chain, got)
			}
			if chain == ChainCross { // evalHop called directly: the same result and no read on the destination chain
				hs, _, err := e.fresh().evalHop(ctx, e.handle, 1)
				if err != nil || hs.State != StateUnknown || !strings.HasPrefix(hs.Reason, wantReason(mode)) {
					t.Fatalf("%s evalHop: %+v %v", mode, hs, err)
				}
				if got := ch.evidenceCount() - base; got != 0 {
					t.Fatalf("%s evalHop: %d evidence reads", mode, got)
				}
			}
			if chain == ChainBSC && ch.evidenceCount() != base {
				t.Fatalf("%s/%s: %d evidence reads reached the unverified source chain", mode, chain, ch.evidenceCount()-base)
			}
		}
	}
}

func TestWaitHandleAndEveryRecoveryEntryPointOnAnUnverifiableChain(t *testing.T) {
	for _, mode := range []string{"error", "hang"} {
		e := q1Setup(t)
		e.break_(ChainBSC, mode)
		c := e.fresh()
		bsc := e.w.chains[ChainBSC]
		base, raw0 := bsc.evidenceCount(), bsc.rawCount()
		bad := func(what string, err error) {
			t.Helper()
			var te *RpcTimeoutError
			switch {
			case err == nil:
				t.Fatalf("%s/%s: want an error, got none", mode, what)
			case isMismatch(err):
				t.Fatalf("%s/%s: a non-mismatch failure must not be a mismatch: %v", mode, what, err)
			case mode == "hang" && !errors.As(err, &te) && !errors.Is(err, context.DeadlineExceeded):
				t.Fatalf("%s/%s: want a timeout, got %v", mode, what, err)
			case mode == "error" && !strings.Contains(err.Error(), "boom"):
				t.Fatalf("%s/%s: want the chain-id error, got %v", mode, what, err)
			}
		}
		_, err := c.WaitHandle(ctx, e.mined)
		bad("WaitHandle", err)
		_, err = c.FindSourceTransfer(ctx, e.pending.Intent, e.pending.Checkpoint)
		bad("FindSourceTransfer", err)
		for _, rep := range []common.Hash{{}, e.mined.Hash} {
			v, err := analyzeP(c, e.pending, rep)
			bad("AnalyzeReplacement", err)
			if v != nil && v.Verdict == VerdictNewTxAllowed {
				t.Fatalf("NewTxAllowed on an unverified chain")
			}
		}
		_, err = c.HandleFromTx(ctx, e.w.net.Chains[ChainBSC].ChainID, e.mined.Hash, nil)
		bad("HandleFromTx", err)
		_, err = c.Rebroadcast(ctx, ChainBSC, e.pending.Raw)
		bad("Rebroadcast", err)
		if bsc.evidenceCount() != base || bsc.rawCount() != raw0 {
			t.Fatalf("%s: %d evidence reads / %d sends reached the unverified chain", mode, bsc.evidenceCount()-base, bsc.rawCount()-raw0)
		}
	}
}

func TestPreflightOnAnUnverifiableSourceChain(t *testing.T) {
	for _, mode := range []string{"error", "hang"} {
		e := q1Setup(t)
		a := e.pending.Quote.Sender
		q := mustQuote(t, e.w.client(), r1Req(a))
		e.break_(ChainBSC, mode)
		bsc := e.w.chains[ChainBSC]
		base := bsc.evidenceCount()
		rep, err := e.fresh().Preflight(ctx, q)
		if err != nil || rep.OK {
			t.Fatalf("%s: %+v %v", mode, rep, err)
		}
		found := false
		for _, i := range rep.Issues {
			if i.Code == "PREFLIGHT_INCOMPLETE" && strings.HasPrefix(i.Check, "chainId(bsc") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: want PREFLIGHT_INCOMPLETE on chainId(bsc): %+v", mode, rep.Issues)
		}
		if got := bsc.evidenceCount() - base; got != 0 {
			t.Fatalf("%s: %d evidence reads reached the unverified chain", mode, got)
		}
	}
}
