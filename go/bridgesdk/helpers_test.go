package bridgesdk

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

// Shared test helpers: request builders, quote/client fixtures and small assertions.

// mustSeal re-seals a hand-modified quote (test-only; sealQuoteInPlace is unexported on purpose).
func mustSeal(t *testing.T, q *Quote) {
	t.Helper()
	if err := sealQuoteInPlace(q); err != nil {
		t.Fatal(err)
	}
}

var otherAdr = common.HexToAddress("0x5555555555555555555555555555555555555555")

// r3World: testnet world + funded wallet + client, with the USDTx fee set for X3.
func r3World(t *testing.T) (*world, *ecdsa.PrivateKey, common.Address, *Client) {
	w := testnetWorld(t)
	k, a := w.fundedWallet()
	w.setFee(ChainOne, w.net.Chains[ChainBSC].ChainID, tok(w, ChainOne, "USDTx"), big.NewInt(1), new(big.Int), new(big.Int))
	w.swapOut = n18(100)
	return w, k, a, w.client()
}

func r5Core(a common.Address) QuoteRequest {
	return QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Sender: a, Recipient: recipient, Hypercore: true}
}

func totalRaw(w *world) int {
	n := 0
	for _, c := range w.chains {
		n += c.rawCount()
	}
	return n
}

func codesOf(pe *PreflightError) []string {
	var out []string
	for _, i := range pe.Issues {
		out = append(out, i.Code)
	}
	return out
}

func hasCode(pe *PreflightError, code string) bool { return contains(codesOf(pe), code) }

func asPreflight(t *testing.T, err error, stage string) *PreflightError {
	t.Helper()
	var pe *PreflightError
	if !errors.As(err, &pe) || pe.Stage != stage {
		t.Fatalf("want *PreflightError at stage %q, got %v", stage, err)
	}
	return pe
}

func signWith(chainID int64, key *ecdsa.PrivateKey, tx *types.Transaction) (*types.Transaction, error) {
	return types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(chainID)), key)
}

func realClockClient(w *world, callTimeout time.Duration) *Client {
	o := w.opts()
	o.Now, o.Sleep = nil, nil // real clock
	o.CallTimeout = callTimeout
	c, _ := NewClient(ctx, w.network, o)
	return c
}

func fakeAdr(n string) string { return "0x" + strings.Repeat("0", 40-len(n)) + n }

func overrideNetwork(t *testing.T) *NetworkData {
	t.Helper()
	built, err := GetNetwork(Testnet)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(built)
	var nd NetworkData
	if err := json.Unmarshal(raw, &nd); err != nil { // an independent deep copy
		t.Fatal(err)
	}
	c := nd.Chains
	s := func(v string) *string { return &v }
	c[ChainOne].Bridge = s(fakeAdr("a1000000001"))
	c[ChainOne].Swap.SwapBridgeRouter = s(fakeAdr("a1000000002"))
	c[ChainOne].Swap.V3SwapRouter = s(fakeAdr("a1000000003"))
	c[ChainOne].Swap.QuoterV2 = s(fakeAdr("a1000000004"))
	c[ChainOne].Swap.V3Factory = s(fakeAdr("a1000000005"))
	c[ChainOne].Tokens["ONEUSD"] = &Token{Address: fakeAdr("a1000000006"), Symbol: s("ONEUSD"), Decimals: 18, Kind: "erc20"}
	c[ChainOne].Swap.X1Pool = &X1Pool{TokenIn: c[ChainOne].Tokens["USDTx"].Address, TokenOut: fakeAdr("a1000000006"), Fee: 500, Pool: fakeAdr("a1000000007")}
	c[ChainHyperEVM].Tokens["ONE"].Address = fakeAdr("a1000000008")
	return &nd
}

type overrideEnv struct {
	nd            *NetworkData
	w             *world
	key           *ecdsa.PrivateKey
	a             common.Address
	client        *Client // the override-config client
	defaultClient *Client // same chains, built-in config
}

func newOverrideEnv(t *testing.T) *overrideEnv { return newEnv(t, overrideNetwork(t)) }

func newEnv(t *testing.T, nd *NetworkData) *overrideEnv {
	t.Helper()
	w := testnetWorldNet(t, nd)
	k, a := w.fundedWallet()
	c := w.net.Chains
	hubID := c[ChainOne].ChainID
	usdt, usdtx := common.HexToAddress(c[ChainBSC].Tokens["USDT"].Address), common.HexToAddress(c[ChainOne].Tokens["USDTx"].Address)
	w.setPair(ChainBSC, hubID, usdt, usdtx, true)
	w.setFee(ChainBSC, hubID, usdt, n18(5), new(big.Int), new(big.Int))
	w.whitelist[key(ChainOne, *c[ChainOne].Swap.V3SwapRouter)] = true
	w.setBal(ChainBSC, usdt, a, n18(1000))
	w.setFee(ChainOne, c[ChainBSC].ChainID, usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	w.swapOut = n18(100)
	w.x1Out = add(n18(100), big.NewInt(3_511_957))
	o := w.opts()
	cl, err := NewClient(ctx, Testnet, o)
	if err != nil {
		t.Fatal(err)
	}
	do := w.opts()
	do.Networks = nil
	dc, err := NewClient(ctx, Testnet, do)
	if err != nil {
		t.Fatal(err)
	}
	return &overrideEnv{nd, w, k, a, cl, dc}
}

func (e *overrideEnv) quotes(t *testing.T) map[string]*Quote {
	t.Helper()
	usdtx := tok(e.w, ChainOne, "USDTx")
	mk := func(r QuoteRequest) *Quote {
		r.Sender, r.Recipient = e.a, recipient
		return mustQuote(t, e.client, r)
	}
	return map[string]*Quote{
		"X1":      mk(QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), QuoteTTL: 60 * time.Second}),
		"X2":      mk(QuoteRequest{Route: RouteOneToHyperCore, Amount: n18(10)}),
		"R5":      mk(QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10)}),
		"R5+core": mk(QuoteRequest{Route: RouteBSCToOneToHyperEVM, Amount: n18(10), Hypercore: true}),
		"X3":      mk(QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), X3: &X3Request{TokenIn: tokenA, TokenOut: usdtx, ToChain: ChainBSC}}),
	}
}

func asMismatch(t *testing.T, err error, what string) {
	t.Helper()
	var nm *NetworkMismatchError
	var lv *LimitViolationError
	if !errors.As(err, &nm) || errors.As(err, &lv) {
		t.Fatalf("%s: want *NetworkMismatchError (client binding check), got %v", what, err)
	}
}

func mismatchAt(t *testing.T, err error, path, what string) {
	t.Helper()
	var nm *NetworkMismatchError
	if !errors.As(err, &nm) || !strings.Contains(nm.Scope, path) {
		t.Fatalf("%s: want *NetworkMismatchError naming %q, got %v", what, path, err)
	}
}

func deepCopyNet(t *testing.T, n *NetworkData) *NetworkData {
	t.Helper()
	raw, _ := json.Marshal(n)
	var out NetworkData
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func x1WorldFor(t *testing.T) (*world, *ecdsa.PrivateKey, common.Address, *Client) {
	w := newWorld(t, Mainnet)
	ch := w.net.Chains
	hubID := ch[ChainOne].ChainID
	usdt, usdtx := tok(w, ChainBSC, "USDT"), tok(w, ChainOne, "USDTx")
	router := common.HexToAddress(*ch[ChainOne].Swap.V3SwapRouter)
	w.setPair(ChainBSC, hubID, usdt, usdtx, true)
	w.setFee(ChainBSC, hubID, usdt, n18(5), new(big.Int), new(big.Int))
	w.whitelist[key(ChainOne, router)] = true
	k, a := w.fundedWallet()
	w.setBal(ChainBSC, usdt, a, n18(1000))
	w.x1Out = add(n18(100), big.NewInt(3_511_957))
	return w, k, a, w.client()
}

func x1Req(a common.Address, s SwapOptions) QuoteRequest {
	return QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: a, Recipient: recipient, Swap: s, QuoteTTL: 60e9}
}

func sameHandle(t *testing.T, a, b *TransferHandle) {
	t.Helper()
	x, y := *a, *b
	x.ScanFrom, y.ScanFrom = nil, nil
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	if string(ja) != string(jb) {
		t.Fatalf("handles differ:\n%s\n%s", ja, jb)
	}
}

func biLog(t *testing.T, to int64, idx int64, from common.Address, extra []byte, emitter common.Address, value *big.Int) types.Log {
	return encodeEventT(t, bridgeABI, "BridgeInitiated", emitter, big.NewInt(to), big.NewInt(idx), common.HexToAddress("0x26ca2A92ea9659e073621bb85b9117Dc5944Cff1"), nativeAdr, from, recipient, value, new(big.Int), new(big.Int), extra, big.NewInt(1))
}

type q1env struct {
	w       *world
	pending *SentTx
	mined   *SentTx
	handle  *TransferHandle
}

func q1Setup(t *testing.T) *q1env {
	t.Helper()
	w, c, pending, a := newPending(t)
	q := mustQuote(t, c, r1Req(a))
	mined, err := c.Send(ctx, w.signer(w.chains[ChainBSC].keys[a], ChainBSC), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, mined)
	if err != nil {
		t.Fatal(err)
	}
	// the destination moved on: a healthy tracker would scan logs
	w.finNext[key(ChainOne, w.net.Chains[ChainBSC].ChainID)] = add(parseU(h.Hop1.Index), big.NewInt(1))
	return &q1env{w: w, pending: pending, mined: mined, handle: h}
}

func (e *q1env) break_(chain ChainKey, mode string) {
	if mode == "hang" {
		e.w.chains[chain].setHang("eth_chainId", true)
	} else {
		e.w.chains[chain].mu.Lock()
		e.w.chains[chain].chainIDErr = errors.New("boom: eth_chainId failed")
		e.w.chains[chain].mu.Unlock()
	}
}

func (e *q1env) fresh() *Client { return realClockClient(e.w, 30*time.Millisecond) }

func wantReason(mode string) string {
	if mode == "hang" {
		return "timeout:"
	}
	return "chain-unverified:"
}

// mineSigned mines a SIGNED tx of `from` at `nonce` (chain-id protected, so it is valid replacement evidence) whose receipt carries exactly these logs.
func (f *fakeChain) mineSigned(from common.Address, nonce uint64, to common.Address, logs []types.Log) common.Hash {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block++
	tx := types.NewTx(&types.LegacyTx{Nonce: nonce, GasPrice: big.NewInt(1), Gas: 300000, To: &to, Value: new(big.Int)})
	tx, _ = types.SignTx(tx, f.signer(), f.keys[from])
	hash := tx.Hash()
	var rl []*types.Log
	for i, l := range logs {
		l.BlockNumber, l.TxHash, l.Index = f.block, hash, uint(i)
		f.logs = append(f.logs, l)
		cp := l
		rl = append(rl, &cp)
	}
	f.nonces[from] = nonce + 1
	f.receipts[hash] = &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: rl, TxHash: hash, BlockNumber: new(big.Int).SetUint64(f.block)}
	f.txs[hash] = tx
	f.blocks[f.block] = append(f.blocks[f.block], tx)
	return hash
}

type s1env struct {
	w                                *world
	c                                *Client
	a                                common.Address
	sent                             *SentTx
	router, hub, usdtx, bscUSDT, bsc common.Address
	bscID                            int64
}

func newS1(t *testing.T, x3 bool) *s1env {
	t.Helper()
	w := testnetWorld(t)
	k, a := w.fundedWallet()
	ch := w.net.Chains
	e := &s1env{w: w, a: a, router: common.HexToAddress(*ch[ChainOne].Swap.SwapBridgeRouter), hub: common.HexToAddress(*ch[ChainOne].Bridge),
		usdtx: tok(w, ChainOne, "USDTx"), bscUSDT: tok(w, ChainBSC, "USDT"), bsc: tok(w, ChainBSC, "ONE"), bscID: ch[ChainBSC].ChainID}
	w.setFee(ChainOne, e.bscID, e.usdtx, big.NewInt(1), new(big.Int), new(big.Int))
	e.c = w.client()
	var q *Quote
	if x3 {
		q = mustQuote(t, e.c, x3Req(w, a, 50))
		w.setAllow(ChainOne, tokenA, a, e.router, n18(1000))
	} else {
		q = mustQuote(t, e.c, QuoteRequest{Route: RouteOneToBSC, Amount: n18(10), Sender: a, Recipient: recipient})
	}
	w.chains[ChainOne].sendMode = "pending" // accepted by the node, never mined: the nonce stays open for a replacement
	sent, err := e.c.Send(ctx, w.signer(k, ChainOne), q, SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w.chains[ChainOne].sendMode = ""
	e.sent = sent
	return e
}

type x3o struct {
	user, recipient, toToken, emitterRouter, emitterBridge common.Address
	amountOut, value, nf, ef                               *big.Int
	extra                                                  []byte
	badIndex                                               bool
	noBI                                                   bool
}

// x3Logs builds [SwapBridge, BridgeInitiated(from = router)] that MATCH the sent quote unless an option overrides a field.
func (e *s1env) x3Logs(o x3o) []types.Log {
	m := e.sent.Quote.Match
	or := func(a, d common.Address) common.Address {
		if a == (common.Address{}) {
			return d
		}
		return a
	}
	obg := func(a, d *big.Int) *big.Int {
		if a == nil {
			return d
		}
		return a
	}
	user, rcpt, toTok := or(o.user, e.a), or(o.recipient, m.Recipient), or(o.toToken, m.ToToken)
	er, eb := or(o.emitterRouter, e.router), or(o.emitterBridge, e.hub)
	minOut := e.sent.Quote.Limits.AmountOutMinimum
	if minOut == nil { // a direct (non-X3) original: any consistent amounts
		minOut = n18(40)
	}
	out, val, nf, ef := obg(o.amountOut, new(big.Int).Add(minOut, n18(1))), obg(o.value, minOut), obg(o.nf, new(big.Int)), obg(o.ef, new(big.Int))
	bi := e.w.initiatedLog(ChainOne, eb, m.ToChainID, m.TokenOut, toTok, e.router, rcpt, val, nf, ef, o.extra)
	idx := new(big.Int).Sub(e.w.initIdx[key(ChainOne, int64(m.ToChainID))], big.NewInt(1))
	sbIdx := idx
	if o.badIndex {
		sbIdx = new(big.Int).Add(idx, big.NewInt(7))
	}
	sb := e.w.encodeEvent(sbrABI, "SwapBridge", er, user, m.TokenIn, m.TokenOut, m.AmountIn, out, new(big.Int).SetUint64(m.ToChainID), sbIdx, val, nf, ef)
	if o.noBI {
		return []types.Log{sb}
	}
	return []types.Log{sb, bi}
}

func (e *s1env) direct(from, rcpt common.Address, v *big.Int) types.Log {
	return e.w.initiatedLog(ChainOne, e.hub, uint64(e.bscID), nativeAdr, e.bsc, from, rcpt, v, new(big.Int), new(big.Int), nil)
}

func (e *s1env) verdict(t *testing.T, logs ...types.Log) (*ReplacementVerdict, error) {
	t.Helper()
	h := e.w.chains[ChainOne].mineSigned(e.a, e.sent.Nonce, e.router, logs)
	return analyzeP(e.c, e.sent, h)
}

func wantVerdict(t *testing.T, label string, v *ReplacementVerdict, err error, want ReplacementVerdictKind) {
	t.Helper()
	if err != nil || v == nil || v.Verdict != want {
		t.Fatalf("%s: want %s, got %+v %v", label, want, v, err)
	}
	if v.Verdict == VerdictNewTxAllowed && want != VerdictNewTxAllowed {
		t.Fatalf("%s: NEW_TX_ALLOWED", label)
	}
}

var (
	r2Adr = common.HexToAddress("0x3333333333333333333333333333333333333333")
	r3Adr = common.HexToAddress("0x4444444444444444444444444444444444444444")
	nf18  = bg("180000000000000000")
)

func decBridge(t *testing.T, data []byte) []interface{} {
	t.Helper()
	out, err := bridgeABI.Methods["bridgeToken"].Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func rpcCalls(w *world) int {
	n := 0
	for _, c := range w.chains {
		n += c.callCount()
	}
	return n
}

func nothingSent(t *testing.T, w *world) {
	t.Helper()
	if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
		t.Fatalf("something was sent: approves=%d raw=%d", len(w.approveCalls), totalRaw(w))
	}
}

func asSeal(t *testing.T, err error, reason, path string) {
	t.Helper()
	var lv *LimitViolationError
	if !errors.As(err, &lv) || lv.Field != "seal" || lv.Reason != reason || (path != "" && lv.Path != path) {
		t.Fatalf("want seal %q at %q, got %v", reason, path, err)
	}
}

// ---- seal vectors --------------------------------------------------------------------------------------------------------------------
type sealVector struct {
	Family  string          `json:"family"`
	Quote   json.RawMessage `json:"quote"`
	Payload string          `json:"payload"`
	Digest  string          `json:"digest"`
}

func hasIssue(rep *PreflightReport, code string) bool {
	for _, i := range rep.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

var _ = types.LegacyTxType

func destRefused(t *testing.T, err error, stage, code, check string) {
	t.Helper()
	var pe *PreflightError
	if !errors.As(err, &pe) || pe.Stage != stage {
		t.Fatalf("want *PreflightError at %q, got %v", stage, err)
	}
	for _, i := range pe.Issues {
		if i.Code == code && (check == "" || i.Check == check) {
			return
		}
	}
	t.Fatalf("want issue %s (check %q) in %+v", code, check, pe.Issues)
}

var _ = common.Address{}

var ctx = context.Background()

func r1Req(sender common.Address) QuoteRequest {
	return QuoteRequest{Route: RouteBSCToOne, Amount: n18(10), Sender: sender, Recipient: recipient}
}

func tok(w *world, k ChainKey, n string) common.Address {
	return common.HexToAddress(w.net.Chains[k].Tokens[n].Address)
}

func mustQuote(t *testing.T, c *Client, r QuoteRequest) *Quote {
	t.Helper()
	q, err := c.Quote(ctx, r)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	return q
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func bridgeCall(w *world, v *big.Int) []byte {
	d, _ := EncodeBridgeToken(BridgeTokenCall{ToChainID: uint64(w.net.Chains[ChainOne].ChainID), FromToken: tok(w, ChainBSC, "ONE"), To: recipient, Value: v, NetworkFee: new(big.Int), ExFee: new(big.Int)})
	return d
}

func x3Req(w *world, a common.Address, bps int) QuoteRequest {
	return QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, Swap: SwapOptions{SlippageBps: Bps(uint32(bps))},
		X3: &X3Request{TokenIn: tokenA, TokenOut: tok(w, ChainOne, "USDTx"), PoolFee: 0, ToChain: ChainBSC}}
}

var _ = binding.CrossBridgeMetaData

var (
	coreAdr = common.HexToAddress("0x4444444444444444444444444444444444444444")
	destAdr = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

func invalidPayload(t *testing.T, err error, path string) {
	t.Helper()
	var lv *LimitViolationError
	if !errors.As(err, &lv) || lv.Field != "seal" || lv.Reason != SealInvalidPayload || (path != "" && lv.Path != path) {
		t.Fatalf("want invalid-payload at %q, got %v", path, err)
	}
}

// mutPayload returns a copy of q whose payload was edited (the digest is recomputed, so only the schema / rules can catch it).
func mutPayload(t *testing.T, q *Quote, f func(m map[string]any)) *Quote {
	t.Helper()
	tree := jsonTree(q.Seal.Payload).(map[string]any)
	f(tree)
	var buf bytes.Buffer
	if err := writeCanonical(&buf, tree); err != nil {
		t.Fatal(err)
	}
	b := *q
	b.Seal.Payload = buf.String()
	b.Seal.Digest = digestOf(b.Seal.Payload)
	return &b
}

func subm(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }

func accepted(t *testing.T, q *Quote, label string) {
	t.Helper()
	if _, err := openQuote(q); err != nil {
		t.Fatalf("%s: the quote: %v", label, err)
	}
	cp := *q
	if _, err := openQuote(&cp); err != nil {
		t.Fatalf("%s: value copy: %v", label, err)
	}
	raw, _ := json.Marshal(q)
	var rt Quote
	if err := json.Unmarshal(raw, &rt); err != nil {
		t.Fatal(err)
	}
	if _, err := openQuote(&rt); err != nil {
		t.Fatalf("%s: JSON round-trip: %v", label, err)
	}
}

// withExtra re-encodes the sealed bridgeToken call with another extraData (every other argument kept) and re-seals.
func withExtra(t *testing.T, q *Quote, extra []byte) *Quote {
	t.Helper()
	m := bridgeABI.Methods["bridgeToken"]
	a, err := m.Inputs.Unpack(q.Tx.Data[4:])
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeBridgeToken(BridgeTokenCall{ToChainID: a[0].(*big.Int).Uint64(), FromToken: a[1].(common.Address), To: a[2].(common.Address), Value: a[3].(*big.Int), NetworkFee: a[4].(*big.Int), ExFee: a[5].(*big.Int), ExtraData: extra})
	if err != nil {
		t.Fatal(err)
	}
	b := cloneQuote(q)
	b.Tx.Data, b.Details.ExtraData, b.Match.ExtraHash = data, extra, hashExtraData(extra)
	mustSeal(t, b)
	return b
}

func tok2(c *Client, name string) common.Address {
	return common.HexToAddress(c.net.Chains[ChainOne].Tokens[name].Address)
}

func newPending(t *testing.T) (*world, *Client, *SentTx, common.Address) {
	t.Helper()
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
	return w, c, sent, a
}

func analyzeP(c *Client, s *SentTx, rep common.Hash) (*ReplacementVerdict, error) {
	return c.AnalyzeReplacement(ctx, ReplacementParams{Intent: s.Intent, Checkpoint: s.Checkpoint, OriginalHash: s.Hash, OriginalNonce: s.Nonce, ReplacementHash: rep})
}

func isMismatch(err error) bool {
	var nm *NetworkMismatchError
	return errors.As(err, &nm)
}

// clientOnWrongChain: a FRESH client (a "restart") whose BSC RPC is another chain (id 1).
func clientOnWrongChain(w *world, wrong *fakeChain) *Client {
	o := w.opts()
	o.RPCs[ChainBSC] = wrong
	c, _ := NewClient(ctx, Testnet, o)
	return c
}
