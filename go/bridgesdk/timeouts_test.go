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

// Every RPC call is time-bounded: quote reads, re-quote, broadcast and tracking return within the configured time on a hung provider.

func TestHungProviderQuoteIsBounded(t *testing.T) {
	w, _, a, _ := r3World(t)
	w.chains[ChainBSC].setHang("eth_call", true)
	c := realClockClient(w, 30*time.Millisecond)
	t0 := time.Now()
	_, err := c.Quote(ctx, r1Req(a))
	var te *RpcTimeoutError
	if !errors.As(err, &te) || te.Method != "eth_call" || time.Since(t0) > 2*time.Second {
		t.Fatalf("want *RpcTimeoutError(eth_call) quickly, got %v after %s", err, time.Since(t0))
	}
}

func TestRequoteHangAbortsBeforeSigning(t *testing.T) {
	w, k, a, _ := r3World(t)
	c := realClockClient(w, 40*time.Millisecond)
	q := mustQuote(t, c, r1Req(a))
	w.onApprove = func() { w.chains[ChainBSC].hang["eth_call"] = true } // the hook runs under the chain lock: write the map directly
	_, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	var te *RpcTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want *RpcTimeoutError, got %v", err)
	}
	if w.chains[ChainBSC].rawSent != 1 || len(w.chains[ChainBSC].logs) != 0 {
		t.Fatalf("only the approve may be broadcast: raw=%d", w.chains[ChainBSC].rawSent)
	}
}

func TestBroadcastHangIsUnknownOutcomeWithHashAndNoRetry(t *testing.T) {
	w, k, a, _ := r3World(t)
	c := realClockClient(w, 40*time.Millisecond)
	q := mustQuote(t, c, QuoteRequest{Route: RouteOneToBSC, Amount: n18(10), Sender: a, Recipient: recipient})
	w.chains[ChainCross].setHang("eth_sendRawTransaction", true)
	_, err := c.Send(ctx, w.signer(k, ChainCross), q, SendOptions{})
	var bu *BroadcastOutcomeUnknownError
	var te *RpcTimeoutError
	if !errors.As(err, &bu) || !errors.As(err, &te) {
		t.Fatalf("want BroadcastOutcomeUnknownError wrapping RpcTimeoutError (the existing recovery code must catch it), got %v", err)
	}
	var tx types.Transaction
	if bu.Hash == (common.Hash{}) || tx.UnmarshalBinary(bu.Raw) != nil || tx.Hash() != bu.Hash {
		t.Fatalf("the error must carry the hash and the raw tx")
	}
	time.Sleep(120 * time.Millisecond)
	if w.chains[ChainCross].rawCount() != 1 {
		t.Fatalf("exactly one broadcast, never retried: %d", w.chains[ChainCross].rawCount())
	}
}

func TestTrackIsBoundedOnAHungProvider(t *testing.T) {
	w, k, a, _ := r3World(t)
	c := realClockClient(w, 30*time.Millisecond)
	q := mustQuote(t, c, r1Req(a))
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil {
		t.Fatal(err)
	}
	// healthy but not finalized: the current non-terminal state with TimedOut
	quick, err := c.Track(ctx, h, TrackOpts{Timeout: 150 * time.Millisecond, Poll: 20 * time.Millisecond})
	if err != nil || quick.State != StateInFlight || !quick.TimedOut {
		t.Fatalf("want IN_FLIGHT timed out, got %+v %v", quick, err)
	}
	// the destination moved on, so the tracker scans logs; the provider hangs
	w.finNext[key(ChainCross, w.net.Chains[ChainBSC].ChainID)] = add(parseU(h.Hop1.Index), big.NewInt(1))
	w.chains[ChainCross].setHang("eth_getLogs", true)
	t0 := time.Now()
	r, err := c.Track(ctx, h, TrackOpts{Timeout: 400 * time.Millisecond, Poll: 20 * time.Millisecond})
	if err != nil || time.Since(t0) > 400*time.Millisecond+700*time.Millisecond {
		t.Fatalf("Track must return within timeout + epsilon: %v after %s", err, time.Since(t0))
	}
	if r.State != StateUnknown || !r.TimedOut || !strings.HasPrefix(r.Hops[0].Reason, "timeout:") {
		t.Fatalf("want UNKNOWN{timeout}: %+v", r)
	}
	// a hung SOURCE receipt call is bounded too
	w.chains[ChainBSC].setHang("eth_getTransactionReceipt", true)
	t0 = time.Now()
	r2, err := c.Track(ctx, h, TrackOpts{Timeout: 200 * time.Millisecond, Poll: 20 * time.Millisecond})
	if err != nil || time.Since(t0) > 900*time.Millisecond || r2.State != StateUnknown || !r2.TimedOut {
		t.Fatalf("hung source receipt: %+v %v after %s", r2, err, time.Since(t0))
	}
}
