package bridgesdk

// Status tracking: keyed correlation on batch receipts, determination order, resumable chunked scan.

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

var (
	hubAddr  = common.HexToAddress("0xb81d6e000000000000000000000000000000C0de")
	oneUSD   = "0xff42C13D927a6b9265236619161Accb227b6d452"
	usdtxAdr = common.HexToAddress("0x1c37d4F44ed3eC86d96868001CfA89e97112a85C")
)

var txCounter int64

func newTx() common.Hash { txCounter++; return common.BigToHash(big.NewInt(txCounter + 1_000_000)) }

func logOf(t *testing.T, ev string, vals ...interface{}) types.Log {
	a := bridgeABI
	if ev == "ForwardInitiated" || ev == "ForwardFailed" {
		a = forwardLibABI
	}
	return encodeEventT(t, a, ev, hubAddr, vals...)
}

func at(l types.Log, tx common.Hash, idx uint) RawLog {
	l.TxHash, l.Index, l.BlockNumber = tx, idx, 2000
	return rawFromLog(l)
}

func finalizedLog(t *testing.T, from, idx int, tx common.Hash, li uint, value *big.Int, token, to common.Address) RawLog {
	return at(logOf(t, "BridgeFinalized", big.NewInt(int64(from)), big.NewInt(int64(idx)), token, to, value, big.NewInt(1)), tx, li)
}
func extraLog(t *testing.T, from, idx int, ok bool, consumed *big.Int, ret []byte, tx common.Hash, li uint) RawLog {
	return at(logOf(t, "ExtraCallExecuted", big.NewInt(int64(from)), big.NewInt(int64(idx)), hubAddr, [4]byte{0x55, 0xfd, 0x07, 0x76}, ok, consumed, ret), tx, li)
}
func extraLogTo(t *testing.T, from, idx int, ok bool, consumed *big.Int, ret []byte, tx common.Hash, li uint, target common.Address, selector [4]byte) RawLog {
	return at(logOf(t, "ExtraCallExecuted", big.NewInt(int64(from)), big.NewInt(int64(idx)), target, selector, ok, consumed, ret), tx, li)
}

// decoded extraData expectations (what the handle stores) for the pure classifier tests
var (
	v3Adr       = common.HexToAddress("0x6666666666666666666666666666666666666666")
	heTokAdr    = common.HexToAddress("0x00000000000000000000000000000000000000AA")
	x1Recipient = common.HexToAddress("0x3333333333333333333333333333333333333333")
	coreRcpt    = common.HexToAddress("0x4444444444444444444444444444444444444444")
	selX1       = ExactInputSingleSelector
	selX2       = CoreSelector
)

func x1Exp() *X1Expect {
	return &X1Expect{Router: v3Adr.Hex(), TokenIn: usdtxAdr.Hex(), TokenOut: oneUSD, Fee: 500, Recipient: x1Recipient.Hex(), Deadline: "1900000000", AmountIn: n18(10).String(), AmountOutMinimum: "1"}
}
func x2Exp(encoded string) *X2Expect {
	return &X2Expect{Token: heTokAdr.Hex(), CoreRecipient: coreRcpt.Hex(), EncodedAmount: encoded}
}
func initiatedL(t *testing.T, to, idx int, value, nf, ex *big.Int, extra []byte, tx common.Hash, li uint) RawLog {
	return at(logOf(t, "BridgeInitiated", big.NewInt(int64(to)), big.NewInt(int64(idx)), nativeAdr, nativeAdr, hubAddr, recipient, value, nf, ex, extra, big.NewInt(1)), tx, li)
}
func fwdInitL(t *testing.T, from, idx, to int, value, nf, ex *big.Int, extra []byte, tx common.Hash, li uint) RawLog {
	return at(logOf(t, "ForwardInitiated", big.NewInt(int64(from)), big.NewInt(int64(idx)), big.NewInt(int64(to)), nativeAdr, recipient, value, nf, ex, crypto.Keccak256Hash(extra)), tx, li)
}

func selOf(m map[common.Hash]string) func(string) (string, error) {
	return func(h string) (string, error) { return m[common.HexToHash(h)], nil }
}
func sel(name string) string { return "0x" + common.Bytes2Hex(bridgeABI.Methods[name].ID) }

func cls(t *testing.T, kind HopKind, logs []RawLog, selectors map[common.Hash]string, receipts map[common.Hash]*types.Receipt) *classified {
	t.Helper()
	in := classifyInput{kind: kind, logs: logs, selectorOf: selOf(selectors), hub: hubAddr, x1OneUSD: oneUSD,
		receiptOf: func(h string) (*types.Receipt, error) { return receipts[common.HexToHash(h)], nil }}
	switch kind {
	case HopX1:
		in.x1 = x1Exp()
	case HopX2:
		in.x2 = x2Exp("5000000000000000042")
	}
	return clsIn(t, in)
}

func clsIn(t *testing.T, in classifyInput) *classified {
	t.Helper()
	r, err := classifyHop(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func keyedFor(logs []RawLog, idx int) []RawLog { // mimic the tracker's topic filter (topics[2] == index) excluding BridgeInitiated
	var out []RawLog
	for _, l := range logs {
		if l.Topics[0] == topicBridgeInitiated.Hex() {
			continue
		}
		if common.HexToHash(l.Topics[2]) == common.BigToHash(big.NewInt(int64(idx))) {
			out = append(out, l)
		}
	}
	return out
}

func TestPlainWithoutExtraCallIsDelivered(t *testing.T) {
	tx := newTx()
	r := cls(t, HopPlain, []RawLog{finalizedLog(t, 97, 0, tx, 0, n18(10), nativeAdr, recipient)}, map[common.Hash]string{tx: sel("finalizeBridgeBatch")}, nil)
	if r.state != StateDelivered || r.amount != n18(10).String() {
		t.Fatalf("%+v", r)
	}
}

func extraLogX1(t *testing.T, from, idx int, ok bool, consumed *big.Int, ret []byte, tx common.Hash, li uint) RawLog {
	return extraLogTo(t, from, idx, ok, consumed, ret, tx, li, v3Adr, selX1)
}

func TestBatchReceiptPerItemCorrelationX1(t *testing.T) {
	tx := newTx()
	ret := common.LeftPadBytes(big.NewInt(123).Bytes(), 32)
	ret2 := common.LeftPadBytes(big.NewInt(222).Bytes(), 32)
	logs := []RawLog{
		finalizedLog(t, 97, 0, tx, 0, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 0, true, n18(10), ret, tx, 1),
		finalizedLog(t, 97, 1, tx, 2, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 1, false, new(big.Int), nil, tx, 3),
		finalizedLog(t, 97, 2, tx, 4, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 2, true, n18(10), ret2, tx, 5),
		finalizedLog(t, 97, 3, tx, 6, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 3, true, n18(10), nil, tx, 7), // undecodable
		finalizedLog(t, 97, 4, tx, 8, n18(10), usdtxAdr, recipient), // no ExtraCallExecuted
	}
	s := map[common.Hash]string{tx: sel("finalizeBridgeBatch")}
	a, b, c2, u, n := cls(t, HopX1, keyedFor(logs, 0), s, nil), cls(t, HopX1, keyedFor(logs, 1), s, nil), cls(t, HopX1, keyedFor(logs, 2), s, nil), cls(t, HopX1, keyedFor(logs, 3), s, nil), cls(t, HopX1, keyedFor(logs, 4), s, nil)
	if a.state != StateDelivered || a.amount != "123" || a.token != oneUSD {
		t.Fatalf("a %+v", a)
	}
	if b.state != StateDeliveredFallback || b.reason != "extraCall-failed" || !strings.EqualFold(b.token, usdtxAdr.Hex()) {
		t.Fatalf("b %+v", b)
	}
	if c2.amount != "222" {
		t.Fatalf("two successful items to the same recipient must use their OWN returnData: %+v", c2)
	}
	if u.state != StateDelivered || u.amount != "unknown" {
		t.Fatalf("undecodable: %+v", u)
	}
	if n.state != StateDeliveredFallback || n.reason != "extraCall-skipped" {
		t.Fatalf("absent: %+v", n)
	}
}

func TestX2CoreDeposit(t *testing.T) {
	tx := newTx()
	s := map[common.Hash]string{tx: sel("finalizeBridgeBatch")}
	xe2 := func(from, idx int, ok bool, consumed *big.Int, ret []byte, li uint) RawLog {
		return extraLogTo(t, from, idx, ok, consumed, ret, tx, li, heTokAdr, selX2)
	}
	sent := common.LeftPadBytes(bg("5000000000000000000").Bytes(), 32)
	ok := cls(t, HopX2, []RawLog{finalizedLog(t, 612044, 0, tx, 0, bg("5000000000000000042"), nativeAdr, recipient), xe2(612044, 0, true, bg("5000000000000000000"), sent, 1)}, s, nil)
	if ok.state != StateCoreDepositSubmitted || ok.recipient != coreRcpt.Hex() || ok.detail["returnedToDest"] != "42" || ok.detail["coreAmount"] != "5000000000000000000" ||
		ok.detail["coreRecipient"] != coreRcpt.Hex() || ok.detail["finalizedValue"] != "5000000000000000042" || ok.detail["returnedTo"] != recipient.Hex() {
		t.Fatalf("%+v", ok)
	}
	bad := cls(t, HopX2, []RawLog{finalizedLog(t, 612044, 1, tx, 2, n18(1), nativeAdr, recipient), xe2(612044, 1, false, new(big.Int), nil, 3)}, s, nil)
	none := cls(t, HopX2, []RawLog{finalizedLog(t, 612044, 2, tx, 4, n18(1), nativeAdr, recipient)}, s, nil)
	if bad.state != StateDeliveredFallback || bad.reason != "extraCall-failed" || none.state != StateDeliveredFallback || none.reason != "extraCall-skipped" {
		t.Fatalf("%+v %+v", bad, none)
	}
}

// X2: surplus when the hop-2 finalized value exceeds the committed amount (fee drop); coreUnit rounding dust; encodedAmount > finalized
// -> fallback; wrong target/method or no decoded call -> PROCESSED_UNVERIFIED.
func TestG4X2SurplusDustFallbackAndUnverified(t *testing.T) {
	tx := newTx()
	s := map[common.Hash]string{tx: sel("finalizeBridgeBatch")}
	xe2 := func(from, idx int, ok bool, consumed *big.Int, ret []byte, li uint) RawLog {
		return extraLogTo(t, from, idx, ok, consumed, ret, tx, li, heTokAdr, selX2)
	}
	word := func(v string) []byte { return common.LeftPadBytes(bg(v).Bytes(), 32) }
	in := func(logs []RawLog, x2 *X2Expect) classifyInput {
		return classifyInput{kind: HopX2, logs: logs, selectorOf: selOf(s), hub: hubAddr, x2: x2}
	}
	surplus := clsIn(t, in([]RawLog{finalizedLog(t, 612044, 0, tx, 0, bg("5700000000000000000"), nativeAdr, recipient), xe2(612044, 0, true, bg("5000000000000000000"), word("5000000000000000000"), 1)}, x2Exp("5000000000000000000")))
	if surplus.state != StateCoreDepositSubmitted || surplus.detail["returnedToDest"] != "700000000000000000" {
		t.Fatalf("surplus %+v", surplus)
	}
	dust := clsIn(t, in([]RawLog{finalizedLog(t, 612044, 1, tx, 2, bg("1000000000123"), nativeAdr, recipient), xe2(612044, 1, true, bg("1000000000000"), word("1000000000000"), 3)}, x2Exp("1000000000123")))
	if dust.detail["returnedToDest"] != "123" || dust.detail["coreAmount"] != "1000000000000" {
		t.Fatalf("dust %+v", dust)
	}
	over := clsIn(t, in([]RawLog{finalizedLog(t, 612044, 2, tx, 4, n18(4), nativeAdr, recipient), xe2(612044, 2, false, new(big.Int), nil, 5)}, x2Exp("5000000000000000000")))
	if over.state != StateDeliveredFallback || over.reason != "extraCall-failed" || over.amount != n18(4).String() {
		t.Fatalf("over %+v", over)
	}
	wrong := clsIn(t, in([]RawLog{finalizedLog(t, 612044, 3, tx, 6, n18(5), nativeAdr, recipient), extraLog(t, 612044, 3, true, n18(5), nil, tx, 7)}, x2Exp("5000000000000000000")))
	if wrong.state != StateProcessedUnverified || !strings.Contains(wrong.reason, "unsupported extraData") {
		t.Fatalf("wrong %+v", wrong)
	}
	nodec := clsIn(t, in([]RawLog{finalizedLog(t, 612044, 4, tx, 8, n18(5), nativeAdr, recipient), xe2(612044, 4, true, n18(5), nil, 9)}, nil))
	if nodec.state != StateProcessedUnverified || !strings.Contains(nodec.reason, "unsupported extraData") {
		t.Fatalf("nodec %+v", nodec)
	}
}

// X1: DELIVERED is reported to the decoded recipient / tokenOut (not BridgeFinalized.to); wrong amountIn / wrong call / missing decode
// -> PROCESSED_UNVERIFIED; the fallback still pays USDTx to .to.
func TestG4X1DecodedRecipientAndUnverified(t *testing.T) {
	tx := newTx()
	s := map[common.Hash]string{tx: sel("finalizeBridgeBatch")}
	in := func(logs []RawLog, x1 *X1Expect) classifyInput {
		return classifyInput{kind: HopX1, logs: logs, selectorOf: selOf(s), hub: hubAddr, x1: x1}
	}
	ret := common.LeftPadBytes(n18(9).Bytes(), 32)
	ok := clsIn(t, in([]RawLog{finalizedLog(t, 97, 0, tx, 0, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 0, true, n18(10), ret, tx, 1)}, x1Exp()))
	if ok.state != StateDelivered || ok.recipient != x1Recipient.Hex() || ok.token != oneUSD || ok.amount != n18(9).String() || ok.detail["bridgeFinalizedTo"] != recipient.Hex() {
		t.Fatalf("ok %+v", ok)
	}
	amt := clsIn(t, in([]RawLog{finalizedLog(t, 97, 1, tx, 2, n18(11), usdtxAdr, recipient), extraLogX1(t, 97, 1, true, n18(11), nil, tx, 3)}, x1Exp()))
	if amt.state != StateProcessedUnverified || !strings.Contains(amt.reason, "amountIn") {
		t.Fatalf("amt %+v", amt)
	}
	wrong := clsIn(t, in([]RawLog{finalizedLog(t, 97, 2, tx, 4, n18(10), usdtxAdr, recipient), extraLog(t, 97, 2, true, n18(10), nil, tx, 5)}, x1Exp()))
	if wrong.state != StateProcessedUnverified {
		t.Fatalf("wrong %+v", wrong)
	}
	nodec := clsIn(t, in([]RawLog{finalizedLog(t, 97, 3, tx, 6, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 3, true, n18(10), nil, tx, 7)}, nil))
	if nodec.state != StateProcessedUnverified {
		t.Fatalf("nodec %+v", nodec)
	}
	fb := clsIn(t, in([]RawLog{finalizedLog(t, 97, 4, tx, 8, n18(10), usdtxAdr, recipient), extraLogX1(t, 97, 4, false, new(big.Int), nil, tx, 9)}, x1Exp()))
	if fb.state != StateDeliveredFallback || fb.recipient != recipient.Hex() || !strings.EqualFold(fb.token, usdtxAdr.Hex()) {
		t.Fatalf("fb %+v", fb)
	}
}

func TestHubForwardEventsWinAndStrictHop2Match(t *testing.T) {
	tx := newTx()
	nf := new(big.Int).Mul(big.NewInt(18), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil))
	z := new(big.Int)
	logs := []RawLog{
		initiatedL(t, 998, 7, n18(10), nf, z, nil, tx, 0), fwdInitL(t, 97, 0, 998, n18(10), nf, z, nil, tx, 1), extraLog(t, 97, 0, true, n18(10), nil, tx, 2), finalizedLog(t, 97, 0, tx, 3, add(n18(10), nf), nativeAdr, recipient),
		initiatedL(t, 998, 8, n18(10), nf, z, nil, tx, 4), fwdInitL(t, 97, 1, 998, n18(10), nf, z, nil, tx, 5), extraLog(t, 97, 1, true, n18(10), nil, tx, 6), finalizedLog(t, 97, 1, tx, 7, add(n18(10), nf), nativeAdr, recipient),
	}
	rec := &types.Receipt{Status: 1, TxHash: tx}
	for _, l := range logs {
		x := logFromRaw(l)
		rec.Logs = append(rec.Logs, &x)
	}
	s, r := map[common.Hash]string{tx: sel("finalizeBridgeBatch")}, map[common.Hash]*types.Receipt{tx: rec}
	a, b := cls(t, HopHubForward, keyedFor(logs, 0), s, r), cls(t, HopHubForward, keyedFor(logs, 1), s, r)
	if a.state != StateForwarded || a.hop2Index != "7" || b.state != StateForwarded || b.hop2Index != "8" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	// identical duplicate BridgeInitiated before the same ForwardInitiated: not unique -> never guess
	tx2 := newTx()
	dup := []RawLog{initiatedL(t, 998, 1, n18(10), z, z, nil, tx2, 0), initiatedL(t, 998, 2, n18(10), z, z, nil, tx2, 1), fwdInitL(t, 97, 5, 998, n18(10), z, z, nil, tx2, 2), finalizedLog(t, 97, 5, tx2, 3, n18(10), nativeAdr, recipient)}
	rec2 := &types.Receipt{Status: 1, TxHash: tx2}
	for _, l := range dup {
		x := logFromRaw(l)
		rec2.Logs = append(rec2.Logs, &x)
	}
	amb := cls(t, HopHubForward, keyedFor(dup, 5), map[common.Hash]string{tx2: sel("finalizeBridgeBatch")}, map[common.Hash]*types.Receipt{tx2: rec2})
	if amb.state != StateProcessedUnverified {
		t.Fatalf("ambiguous must be PROCESSED_UNVERIFIED: %+v", amb)
	}
}

func TestForwardFailedIsHubFallback(t *testing.T) {
	tx := newTx()
	ff := at(logOf(t, "ForwardFailed", big.NewInt(97), big.NewInt(0), uint8(2), crypto.Keccak256Hash([]byte{0x12, 0x34})), tx, 1)
	r := cls(t, HopHubForward, []RawLog{ff, finalizedLog(t, 97, 0, tx, 2, n18(10), nativeAdr, recipient)}, map[common.Hash]string{tx: sel("finalizeBridgeBatch")}, nil)
	if r.state != StateHubFallback || r.code != "ExecutorCallReverted" {
		t.Fatalf("%+v", r)
	}
}

func TestReleaseResolutionBeforeExtraDataRules(t *testing.T) {
	for _, kind := range []HopKind{HopPlain, HopX1, HopX2, HopHubForward} {
		tx := newTx()
		rel := cls(t, kind, []RawLog{finalizedLog(t, 97, 0, tx, 0, n18(10), nativeAdr, recipient)}, map[common.Hash]string{tx: sel("releasePending")}, nil)
		if rel.state != StateReleasedFromPending || !strings.EqualFold(rel.recipient, recipient.Hex()) {
			t.Fatalf("%s public release: %+v", kind, rel)
		}
		tx2 := newTx()
		manual := at(logOf(t, "ManualReleased", big.NewInt(97), big.NewInt(1)), tx2, 1)
		man := cls(t, kind, []RawLog{finalizedLog(t, 97, 1, tx2, 0, n18(10), nativeAdr, senderAdr), manual}, map[common.Hash]string{tx2: sel("manualReleasePendingWithRecipient")}, nil)
		if man.state != StateReleasedFromPending || !strings.EqualFold(man.recipient, senderAdr.Hex()) {
			t.Fatalf("%s manual release: %+v", kind, man)
		}
		tx3 := newTx()
		mix := cls(t, kind, []RawLog{extraLog(t, 97, 2, true, big.NewInt(1), nil, tx3, 0), finalizedLog(t, 97, 2, tx3, 1, n18(10), nativeAdr, recipient)}, map[common.Hash]string{tx3: sel("releasePending")}, nil)
		if mix.state != StateReleasedFromPending {
			t.Fatalf("%s: a release tx must never be classified by forward/extraCall events: %+v", kind, mix)
		}
	}
}

func TestPendingRemovedAndStalePending(t *testing.T) {
	tx := newTx()
	if r := cls(t, HopPlain, []RawLog{at(logOf(t, "PendingRemoved", big.NewInt(97), big.NewInt(0)), tx, 0)}, nil, nil); r.state != StatePendingRemoved {
		t.Fatalf("%+v", r)
	}
	stale := at(logOf(t, "BridgePending", big.NewInt(97), big.NewInt(0), nativeAdr, recipient, big.NewInt(1), big.NewInt(1), uint8(2)), tx, 0)
	if r := cls(t, HopPlain, []RawLog{stale}, nil, nil); r.state != StateProcessedUnverified {
		t.Fatalf("stale pending must not be success: %+v", r)
	}
	if r := cls(t, HopPlain, nil, nil, nil); r.state != StateProcessedUnverified {
		t.Fatalf("%+v", r)
	}
}

// ---- RPC-backed tracker ---------------------------------------------------------------------------------------------

func sentR1(t *testing.T) (*world, *Client, *TransferHandle) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	sent, err := c.Send(ctx, w.signer(key, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil {
		t.Fatal(err)
	}
	return w, c, h
}

func finalizeOnHub(t *testing.T, w *world, logs ...types.Log) {
	data, perr := bridgeABI.Pack("finalizeBridgeBatch", []binding.IBridgeRegistryFinalizeArguments{}, [][]uint8{}, [][][32]byte{}, [][][32]byte{})
	if perr != nil {
		t.Fatal(perr)
	}
	w.chains[ChainCross].mineLogs(hubAddr, data, logs)
}

func TestTrackerInFlightPendingPrecedenceThenDelivered(t *testing.T) {
	w, c, h := sentR1(t)
	if h.ScanFrom[ChainCross] == 0 {
		t.Fatal("destination block must be recorded at send time")
	}
	bscID := w.net.Chains[ChainBSC].ChainID
	r, err := c.TrackOnce(ctx, h)
	if err != nil || r.State != StateInFlight {
		t.Fatalf("%+v %v", r, err)
	}
	w.pending[key(ChainCross, big.NewInt(int64(bscID)))] = []*big.Int{big.NewInt(0)}
	r, _ = c.TrackOnce(ctx, h)
	if r.State != StatePendingOnDest || r.Hops[0].Reason != "VerificationAmountThresholdExceeded" {
		t.Fatalf("%+v", r)
	}
	delete(w.pending, key(ChainCross, big.NewInt(int64(bscID))))
	w.finNext[key(ChainCross, big.NewInt(int64(bscID)))] = big.NewInt(1)
	if r, _ = c.TrackOnce(ctx, h); r.State != StateProcessedUnverified {
		t.Fatalf("no evidence must never be success: %+v", r)
	}
	finalizeOnHub(t, w, logOf(t, "BridgeFinalized", big.NewInt(int64(bscID)), big.NewInt(0), nativeAdr, recipient, n18(10), big.NewInt(1)))
	r, _ = c.TrackOnce(ctx, h)
	if r.State != StateDelivered || !r.Terminal {
		t.Fatalf("%+v", r)
	}
}

func TestTrackerChunkedScanBackoffResumeAndUnknown(t *testing.T) {
	w, c, h := sentR1(t)
	bscID := int64(w.net.Chains[ChainBSC].ChainID)
	w.finNext[key(ChainCross, big.NewInt(bscID))] = big.NewInt(1)
	finalizeOnHub(t, w, logOf(t, "BridgeFinalized", big.NewInt(bscID), big.NewInt(0), nativeAdr, recipient, n18(10), big.NewInt(1)))
	hub := w.chains[ChainCross]
	hub.block = h.ScanFrom[ChainCross] + 2600 // several 500-block chunks
	hub.getLogsCalls = nil
	hub.failLogs = 2
	t0 := w.clock
	r, err := c.TrackOnce(ctx, h)
	if err != nil || r.State != StateDelivered {
		t.Fatalf("%+v %v", r, err)
	}
	if len(hub.getLogsCalls) < 6 {
		t.Fatalf("chunked scan expected, got %d calls", len(hub.getLogsCalls))
	}
	if w.clock.Sub(t0).Seconds() < 3 {
		t.Fatal("expected 1 s + 2 s backoff sleeps")
	}
	// resume from a SERIALIZED handle: only NEW blocks are scanned
	b, _ := json.Marshal(r.Handle)
	var saved TransferHandle
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	var st *ScanState
	for _, s := range saved.Scans {
		st = s
	}
	if st.LastScanned != int64(hub.block) || len(st.Logs) != 1 {
		t.Fatalf("scan state %+v", st)
	}
	hub.getLogsCalls = nil
	hub.block += 10
	c2 := w.client()
	r2, err := c2.TrackOnce(ctx, &saved)
	if err != nil || r2.State != StateDelivered || len(hub.getLogsCalls) != 1 || hub.getLogsCalls[0].FromBlock.Int64() != st.LastScanned+1 {
		t.Fatalf("resume: %+v calls=%d %v", r2, len(hub.getLogsCalls), err)
	}
	// backoff exhaustion -> UNKNOWN with manual recovery
	w2, c3, h2 := sentR1(t)
	w2.finNext[key(ChainCross, big.NewInt(bscID))] = big.NewInt(1)
	w2.chains[ChainCross].unavailableLogs = true
	r3, _ := c3.TrackOnce(ctx, h2)
	if r3.State != StateUnknown || !strings.Contains(r3.Hops[0].Reason, "manually") {
		t.Fatalf("%+v", r3)
	}
}

func TestTrackerTimeoutReturnsNonTerminalState(t *testing.T) {
	_, c, h := sentR1(t)
	r, err := c.Track(ctx, h, TrackOpts{Timeout: 20e9, Poll: 5e9})
	if err != nil || r.State != StateInFlight || r.Terminal || !r.TimedOut {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestTrackerR5EndToEnd(t *testing.T) {
	w := testnetWorld(t)
	key_, a := w.fundedWallet()
	c := w.client()
	q := mustQuote(t, c, QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient})
	nf := bg("180000000000000000")
	if q.Value.Cmp(add(n18(10), nf)) != 0 {
		t.Fatalf("v1 %s", q.Value)
	}
	sent, err := c.Send(ctx, w.signer(key_, ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil || h.Hop1.Kind != HopHubForward || h.Hop2 == nil || h.Hop2.DestChainID != w.net.Chains[ChainHyperEVM].ChainID {
		t.Fatalf("%+v %v", h, err)
	}
	bscID, cross, heID := int64(w.net.Chains[ChainBSC].ChainID), int64(w.net.Chains[ChainCross].ChainID), int64(w.net.Chains[ChainHyperEVM].ChainID)
	w.finNext[key(ChainCross, big.NewInt(bscID))] = big.NewInt(1)
	z := new(big.Int)
	finalizeOnHub(t, w,
		logOf(t, "BridgeInitiated", big.NewInt(heID), big.NewInt(0), nativeAdr, nativeAdr, hubAddr, recipient, n18(10), nf, z, []byte{}, big.NewInt(1)),
		logOf(t, "ForwardInitiated", big.NewInt(bscID), big.NewInt(0), big.NewInt(heID), nativeAdr, recipient, n18(10), nf, z, crypto.Keccak256Hash(nil)),
		logOf(t, "ExtraCallExecuted", big.NewInt(bscID), big.NewInt(0), hubAddr, [4]byte{0x55, 0xfd, 0x07, 0x76}, true, n18(10), []byte{}),
		logOf(t, "BridgeFinalized", big.NewInt(bscID), big.NewInt(0), nativeAdr, recipient, add(n18(10), nf), big.NewInt(1)))
	r, err := c.TrackOnce(ctx, h)
	if err != nil || r.State != StateForwarded || r.Handle.Hop2.Index != "0" {
		t.Fatalf("%+v %v", r, err)
	}
	w.finNext[key(ChainHyperEVM, big.NewInt(cross))] = big.NewInt(1)
	heBridge := common.HexToAddress(*w.net.Chains[ChainHyperEVM].Bridge)
	fl := encodeEventT(t, bridgeABI, "BridgeFinalized", heBridge, big.NewInt(cross), big.NewInt(0), common.HexToAddress(w.net.Chains[ChainHyperEVM].Tokens["ONE"].Address), recipient, add(n18(10), big.NewInt(5)), big.NewInt(1))
	data, perr := bridgeABI.Pack("finalizeBridgeBatch", []binding.IBridgeRegistryFinalizeArguments{}, [][]uint8{}, [][][32]byte{}, [][][32]byte{})
	if perr != nil {
		t.Fatal(perr)
	}
	w.chains[ChainHyperEVM].mineLogs(heBridge, data, []types.Log{fl})
	r, _ = c.TrackOnce(ctx, r.Handle)
	if r.State != StateDelivered || r.Hops[1].Amount != add(n18(10), big.NewInt(5)).String() {
		t.Fatalf("surplus above v2 must be visible: %+v", r)
	}
}
