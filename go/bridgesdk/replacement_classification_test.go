package bridgesdk

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Classification of a replacement transaction at the same nonce: any transfer-shaped log blocks a new transaction, X3 matching is split into a structural
// link and an intent comparison (recipient, extraData hash, expected destination token). Receipt-shaped fixtures.

func TestKindOfTheOriginalDoesNotDecide(t *testing.T) {
	// original X3, replacement = a DIRECT R2 bridgeToken of the same sender and nonce
	e := newS1(t, true)
	v, err := e.verdict(t, e.direct(e.a, otherAdr, n18(10)))
	wantVerdict(t, "X3 original / direct replacement", v, err, VerdictReplacedByDifferentTransfer)
	// original direct (R2), replacement = an X3
	d := newS1(t, false)
	d.sent.Quote.Match.TokenIn, d.sent.Quote.Match.TokenOut, d.sent.Quote.Match.AmountIn = tokenA, d.usdtx, n18(50)
	d.sent.Quote.Match.ToChainID, d.sent.Quote.Match.ToToken, d.sent.Quote.Match.Recipient = uint64(d.bscID), d.bscUSDT, recipient
	v, err = d.verdict(t, d.x3Logs(x3o{})...)
	wantVerdict(t, "direct original / X3 replacement", v, err, VerdictReplacedByDifferentTransfer)
}

func TestUnattributedTransferEvidenceIsNeverANewTx(t *testing.T) {
	e := newS1(t, true)
	other := common.HexToAddress("0x00000000000000000000000000000000dead0001")
	// a BridgeInitiated from an UNKNOWN emitter
	bi := e.w.initiatedLog(ChainOne, other, uint64(e.bscID), nativeAdr, e.bsc, e.a, recipient, n18(10), new(big.Int), new(big.Int), nil)
	v, err := e.verdict(t, bi)
	wantVerdict(t, "BridgeInitiated from an unknown emitter", v, err, VerdictReplacedUnverified)
	if len(v.Unattributed) != 1 {
		t.Fatalf("%+v", v)
	}
	// a SwapBridge from an UNKNOWN router
	e2 := newS1(t, true)
	v, err = e2.verdict(t, e2.x3Logs(x3o{emitterRouter: other})...)
	wantVerdict(t, "SwapBridge from an unknown router", v, err, VerdictReplacedUnverified)
	// positive controls: no transfer-shaped log at all
	e3 := newS1(t, true)
	v, err = e3.verdict(t)
	wantVerdict(t, "plain cancel (no logs)", v, err, VerdictNewTxAllowed)
}

func TestReplacementCombinationFixtures(t *testing.T) {
	// one match + one unattributed log -> REPLACED_UNVERIFIED
	e := newS1(t, true)
	other := common.HexToAddress("0x00000000000000000000000000000000dead0002")
	stray := e.w.initiatedLog(ChainOne, other, uint64(e.bscID), nativeAdr, e.bsc, e.a, recipient, n18(1), new(big.Int), new(big.Int), nil)
	v, err := e.verdict(t, append(e.x3Logs(x3o{}), stray)...)
	wantVerdict(t, "match + unattributed", v, err, VerdictReplacedUnverified)
	if len(v.Matched) != 1 || len(v.Unattributed) != 1 {
		t.Fatalf("%+v", v)
	}
	// two matches -> AmbiguousSourceError
	e = newS1(t, true)
	two := append(e.x3Logs(x3o{}), e.x3Logs(x3o{})...)
	if v, err = e.verdict(t, two...); err == nil || v != nil {
		t.Fatalf("want AmbiguousSourceError, got %+v %v", v, err)
	} else if ae := new(AmbiguousSourceError); !errors.As(err, &ae) || len(ae.Candidates) != 2 {
		t.Fatalf("%v", err)
	}
	// one match + one different transfer -> REPLACED_UNVERIFIED
	e = newS1(t, true)
	v, err = e.verdict(t, append(e.x3Logs(x3o{}), e.direct(e.a, otherAdr, n18(3)))...)
	wantVerdict(t, "match + different", v, err, VerdictReplacedUnverified)
	if len(v.Matched) != 1 || len(v.Different) != 1 || len(v.Unattributed) != 0 {
		t.Fatalf("%+v", v)
	}
	// one different transfer and nothing else -> REPLACED_BY_DIFFERENT_TRANSFER
	e = newS1(t, true)
	v, err = e.verdict(t, e.direct(e.a, otherAdr, n18(3)))
	wantVerdict(t, "different only", v, err, VerdictReplacedByDifferentTransfer)
	// the full match alone is still FOUND
	e = newS1(t, true)
	v, err = e.verdict(t, e.x3Logs(x3o{})...)
	wantVerdict(t, "match alone", v, err, VerdictFound)
}

func TestRecipientAndExtraDataAreIntentFields(t *testing.T) {
	cases := map[string]x3o{
		"another recipient":         {recipient: otherAdr},
		"another linked extra data": {extra: []byte{0x12, 0x34}},
		"another destination token": {toToken: otherAdr},
	}
	for name, o := range cases {
		e := newS1(t, true)
		logs := e.x3Logs(o)
		// the replacement path
		v, err := e.verdict(t, logs...)
		wantVerdict(t, name+" / settleReplacement", v, err, VerdictReplacedByDifferentTransfer)
		// FindSourceTransfer: the transfer is on chain but is not THE intent
		f, err := e.c.FindSourceTransfer(ctx, e.sent.Intent, e.sent.Checkpoint)
		if err != nil || f.Status != FindNotFoundYet {
			t.Fatalf("%s / find: %+v %v", name, f, err)
		}
		// quote-based buildHandle
		rec := e.w.chains[ChainOne].receipts[v.ReplacementHash]
		if _, err := e.c.buildHandle(e.c.handleInputFromQuote(e.sent.Quote, rec, nil)); err == nil {
			t.Fatalf("%s: quote-based buildHandle must not match", name)
		}
	}
	// handleFromTx has NO intent: the same receipt (other recipient) is restored from the actual events
	e := newS1(t, true)
	h := e.w.chains[ChainOne].mineLogs(e.router, nil, e.x3Logs(x3o{recipient: otherAdr}))
	if hd, err := e.c.HandleFromTx(ctx, e.w.net.Chains[ChainOne].ChainID, h, nil); err != nil || hd.Route != RouteSwapBridge {
		t.Fatalf("handleFromTx: %+v %v", hd, err)
	}
}

func TestBrokenLinkIsUnattributedAndHandleFromTxErrors(t *testing.T) {
	e := newS1(t, true)
	v, err := e.verdict(t, e.x3Logs(x3o{badIndex: true})...)
	wantVerdict(t, "initiateIndex points to another event", v, err, VerdictReplacedUnverified)
	e = newS1(t, true)
	v, err = e.verdict(t, e.x3Logs(x3o{noBI: true})...)
	wantVerdict(t, "missing BridgeInitiated", v, err, VerdictReplacedUnverified)
	e = newS1(t, true)
	h := e.w.chains[ChainOne].mineLogs(e.router, nil, e.x3Logs(x3o{badIndex: true}))
	if _, err := e.c.HandleFromTx(ctx, e.w.net.Chains[ChainOne].ChainID, h, nil); err == nil {
		t.Fatal("handleFromTx must error on an inconsistent link")
	}
}

func TestFeesAndOutputHigherThanQuotedAreStillFound(t *testing.T) {
	e := newS1(t, true)
	// fees HIGHER than quoted and an output HIGHER than quoted (value + fees <= amountOut): no upper bound is applied
	v, err := e.verdict(t, e.x3Logs(x3o{amountOut: new(big.Int).Add(e.sent.Quote.Limits.AmountOutMinimum, n18(20)), value: new(big.Int).Add(e.sent.Quote.Limits.AmountOutMinimum, n18(5)), nf: n18(4), ef: n18(5)})...)
	wantVerdict(t, "higher fees and output", v, err, VerdictFound)
	f, err := e.c.FindSourceTransfer(ctx, e.sent.Intent, e.sent.Checkpoint)
	if err != nil || f.Status != FindFound {
		t.Fatalf("find: %+v %v", f, err)
	}
}

func TestExpectedDestinationTokenIsStoredAndSealed(t *testing.T) {
	e := newS1(t, true)
	q := e.sent.Quote
	if q.Match.ToToken != e.bscUSDT || q.Match.ToToken == q.Details.Hop1ToToken {
		t.Fatalf("match.toToken %s must be the hub pair's remote token (BSC USDT), not the hub tokenOut %s", q.Match.ToToken, q.Details.Hop1ToToken)
	}
	// the sealed wire carries it at match.toToken
	var tree map[string]any
	_ = json.Unmarshal([]byte(q.Seal.Payload), &tree)
	if got, _ := tree["match"].(map[string]any)["toToken"].(string); got == "" {
		t.Fatalf("no match.toToken in the payload: %s", q.Seal.Payload)
	}
	// omitting it => invalid-payload at match.toToken
	bad := mutPayload(t, q, func(m map[string]any) { delete(subm(m, "match"), "toToken") })
	_, err := openQuote(bad)
	invalidPayload(t, err, "match.toToken")
	// the JSON round trip keeps it
	raw, _ := json.Marshal(q)
	var rt Quote
	if err := json.Unmarshal(raw, &rt); err != nil || rt.Match.ToToken != q.Match.ToToken {
		t.Fatalf("round trip: %v %s", err, rt.Match.ToToken)
	}
	if _, err := openQuote(&rt); err != nil {
		t.Fatal(err)
	}
	// the hub tokenOut differs from the destination token yet the transfer is FOUND
	v, err := e.verdict(t, e.x3Logs(x3o{})...)
	wantVerdict(t, "ONEUSD-like hub token vs destination token", v, err, VerdictFound)
}
