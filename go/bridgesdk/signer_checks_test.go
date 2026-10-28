package bridgesdk

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Signer and signing flow: signed transactions are verified before broadcast, callbacks run before the broadcast, and an unknown broadcast outcome carries what
// recovery needs.

func TestSignedTxIsVerifiedBeforeBroadcast(t *testing.T) {
	type tc struct{ how, field string }
	cases := []tc{{"unprotected", "protected"}, {"otherChain", "chainId"}, {"mutateData", "data"}, {"mutateValue", "value"}, {"mutateNonce", "nonce"}, {"otherSender", "from"}, {"nilTx", "signed"}}
	for _, target := range []string{"approve", "bridge"} {
		for _, c0 := range cases {
			w, k, a, c := r3World(t)
			other, _ := crypto.GenerateKey()
			calls := 0
			honest := w.signer(k, ChainBSC)
			evil := honest
			evil.Sign = func(tx *types.Transaction) (*types.Transaction, error) {
				calls++
				isApprove := calls == 1
				if (target == "approve") != isApprove {
					return signWith(97, k, tx)
				}
				to := *tx.To()
				mk := func(mut func(l *types.LegacyTx)) *types.Transaction {
					l := &types.LegacyTx{Nonce: tx.Nonce(), GasPrice: tx.GasPrice(), Gas: tx.Gas(), To: &to, Value: new(big.Int).Set(tx.Value()), Data: bytes.Clone(tx.Data())}
					mut(l)
					return types.NewTx(l)
				}
				switch c0.how {
				case "unprotected":
					return types.SignTx(tx, types.HomesteadSigner{}, k)
				case "otherChain":
					return signWith(1, k, tx)
				case "mutateData":
					return signWith(97, k, mk(func(l *types.LegacyTx) { l.Data[len(l.Data)-1] ^= 0x01 }))
				case "mutateValue":
					return signWith(97, k, mk(func(l *types.LegacyTx) { l.Value = big.NewInt(1) }))
				case "mutateNonce":
					return signWith(97, k, mk(func(l *types.LegacyTx) { l.Nonce++ }))
				case "otherSender":
					return signWith(97, other, tx)
				default:
					return nil, nil
				}
			}
			q := mustQuote(t, c, r1Req(a))
			_, err := c.Send(ctx, evil, q, SendOptions{})
			var sm *SignerMisbehaviorError
			if !errors.As(err, &sm) || sm.Field != c0.field {
				t.Fatalf("%s/%s: want SignerMisbehaviorError{%s}, got %v", target, c0.how, c0.field, err)
			}
			wantRaw := 0
			if target == "bridge" {
				wantRaw = 1 // the (honest) approve only
			}
			if w.chains[ChainBSC].rawSent != wantRaw || len(w.chains[ChainBSC].logs) != 0 {
				t.Fatalf("%s/%s: the bad tx must never be broadcast (raw=%d logs=%d)", target, c0.how, w.chains[ChainBSC].rawSent, len(w.chains[ChainBSC].logs))
			}
		}
	}
}

func TestPanickingOnBroadcastStillReturnsSentTx(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, r1Req(a))
	sent, err := c.Send(ctx, w.signer(k, ChainBSC), q, SendOptions{OnBroadcast: func(common.Hash) { panic("db down") }})
	if err != nil || sent == nil || sent.Hash == (common.Hash{}) {
		t.Fatalf("a callback failure must not look like a pre-send failure: %v", err)
	}
	if sent.CallbackError == nil || !strings.Contains(sent.CallbackError.Error(), "db down") {
		t.Fatalf("callbackError missing: %v", sent.CallbackError)
	}
	if w.chains[ChainBSC].rawSent != 2 {
		t.Fatalf("approve + exactly one bridge broadcast, got %d", w.chains[ChainBSC].rawSent)
	}
	f, err := c.FindSourceTransfer(ctx, sent.Intent, sent.Checkpoint)
	if err != nil || f.Status != FindFound || len(w.chains[ChainBSC].logs) != 1 {
		t.Fatalf("find: %+v %v", f, err)
	}
	ok, err := c.Send(ctx, w.signer(k, ChainBSC), mustQuote(t, c, r1Req(a)), SendOptions{})
	if err != nil || ok.CallbackError != nil {
		t.Fatalf("no callback => no callbackError: %v", err)
	}
}

func TestSigningErrorAndCallbackAbortsNeverBroadcast(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	// pre-approve so we count only bridge broadcasts
	w.setAllow(ChainBSC, tok(w, ChainBSC, "ONE"), a, common.HexToAddress(*w.net.Chains[ChainBSC].Bridge), n18(100))
	broken := w.signer(key, ChainBSC)
	broken.Sign = func(*types.Transaction) (*types.Transaction, error) { return nil, errors.New("hsm offline") }
	var se *SigningError
	if _, err := c.Send(ctx, broken, q, SendOptions{}); !errors.As(err, &se) {
		t.Fatalf("want SigningError, got %v", err)
	}
	if w.chains[ChainBSC].rawSent != 0 {
		t.Fatal("broadcast after a signing error")
	}
	// OnSigned error aborts
	_, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{OnSigned: func(SignedInfo, *SubmittedIntent) error { return errors.New("disk full") }})
	if err == nil || !strings.Contains(err.Error(), "disk full") || w.chains[ChainBSC].rawSent != 0 {
		t.Fatalf("OnSigned abort: %v raw=%d", err, w.chains[ChainBSC].rawSent)
	}
	// OnSubmitting error aborts
	_, err = c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{OnSubmitting: func(*SubmittedIntent, Checkpoint) error { return errors.New("persist failed") }})
	if err == nil || !strings.Contains(err.Error(), "persist failed") || w.chains[ChainBSC].rawSent != 0 {
		t.Fatalf("OnSubmitting abort: %v", err)
	}
	// ordering: OnSubmitting -> OnSigned (both BEFORE broadcast) -> OnBroadcast
	var order []string
	rawAtSigned := -1
	sent, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{
		OnSubmitting: func(*SubmittedIntent, Checkpoint) error { order = append(order, "submitting"); return nil },
		OnSigned: func(i SignedInfo, _ *SubmittedIntent) error {
			rawAtSigned = w.chains[ChainBSC].rawSent
			order = append(order, "signed")
			return nil
		},
		OnBroadcast: func(common.Hash) { order = append(order, "broadcast") },
	})
	if err != nil || strings.Join(order, ",") != "submitting,signed,broadcast" || rawAtSigned != 0 || len(sent.Raw) == 0 {
		t.Fatalf("order %v raw=%d err=%v", order, rawAtSigned, err)
	}
}

func TestBroadcastUnknownRebroadcastAndFind(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	w.setAllow(ChainBSC, tok(w, ChainBSC, "ONE"), a, common.HexToAddress(*w.net.Chains[ChainBSC].Bridge), n18(100))
	w.chains[ChainBSC].sendMode = "throw-after-mine"
	var signedRaw []byte
	_, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{OnSigned: func(i SignedInfo, _ *SubmittedIntent) error { signedRaw = i.Raw; return nil }})
	var bu *BroadcastOutcomeUnknownError
	if !errors.As(err, &bu) || string(bu.Raw) != string(signedRaw) || !strings.Contains(bu.Error(), "DO NOT resend") {
		t.Fatalf("want BroadcastOutcomeUnknownError, got %v", err)
	}
	w.chains[ChainBSC].sendMode = ""
	h, err := c.Rebroadcast(ctx, ChainBSC, bu.Raw)
	if err != nil || h != bu.Hash {
		t.Fatalf("rebroadcast: %v %v", h, err)
	}
	if len(w.chains[ChainBSC].logs) != 1 {
		t.Fatalf("exactly one transfer expected, got %d", len(w.chains[ChainBSC].logs))
	}
	f, err := c.FindSourceTransfer(ctx, bu.Intent, bu.Checkpoint)
	if err != nil || f.Status != FindFound || f.MinedNonce != bu.Checkpoint.PendingNonceBefore+1 {
		t.Fatalf("find: %+v %v", f, err)
	}
}
