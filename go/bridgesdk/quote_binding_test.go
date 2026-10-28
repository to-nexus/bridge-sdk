package bridgesdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// A quote belongs to the client that issued it: network, chains and every config-owned address must equal THIS client's configuration, while values the
// caller supplied (X3 tokens, recipients, amount) must equal the request and the built calldata. Nothing is signed or sent for a quote that does not belong.

func TestQuoteOfAnotherNetworkIsRejected(t *testing.T) {
	tw, tk, ta, tc := r3World(t)
	mw, mk, ma, mc := x1WorldFor(t)
	// mainnet quote -> testnet client (signer of the mainnet world: the binding fails before any signer check)
	mq := mustQuote(t, mc, QuoteRequest{Route: RouteBSCUSDTToONEUSD, Amount: n18(100), Sender: ma, Recipient: recipient})
	var nm *NetworkMismatchError
	if _, err := tc.Send(ctx, mw.signer(mk, ChainBSC), mq, SendOptions{}); !errors.As(err, &nm) || !strings.Contains(nm.Scope, "network") {
		t.Fatalf("mainnet quote into a testnet client: %v", err)
	}
	// testnet quote -> mainnet client
	tq := mustQuote(t, tc, r1Req(ta))
	if _, err := mc.Send(ctx, tw.signer(tk, ChainBSC), tq, SendOptions{}); !errors.As(err, &nm) {
		t.Fatalf("testnet quote into a mainnet client: %v", err)
	}
	for _, w := range []*world{tw, mw} {
		if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
			t.Fatalf("something was sent")
		}
	}
}

func TestTamperedQuotesAreRejected(t *testing.T) {
	w, k, a, c := r3World(t)
	q := mustQuote(t, c, r1Req(a))
	sg := w.signer(k, ChainBSC)
	var nm *NetworkMismatchError
	bad := *q
	bad.Tx.To, bad.SourceContract = otherAdr, otherAdr // route rules need tx.to == sourceContract; both retargeted
	mustSeal(t, &bad)
	if _, err := c.Send(ctx, sg, &bad, SendOptions{}); !errors.As(err, &nm) || !strings.Contains(nm.Scope, "sourceContract") && !strings.Contains(nm.Scope, "tx.to") {
		t.Fatalf("retargeted tx.to: %v", err)
	}
	ap := *q.Approval
	ap.Spender = otherAdr
	bad2 := *q
	bad2.Approval = &ap
	mustSeal(t, &bad2)
	if _, err := c.Send(ctx, sg, &bad2, SendOptions{}); !errors.As(err, &nm) || !strings.Contains(nm.Scope, "spender") {
		t.Fatalf("retargeted spender: %v", err)
	}
	bad3 := *q
	bad3.SourceChainID = 1
	mustSeal(t, &bad3)
	if _, err := c.Send(ctx, sg, &bad3, SendOptions{}); !errors.As(err, &nm) {
		t.Fatalf("source chain id: %v", err)
	}
	// user-supplied recipient swapped inside the calldata only => internal tampering
	data, _ := EncodeBridgeToken(BridgeTokenCall{ToChainID: uint64(w.net.Chains[ChainOne].ChainID), FromToken: tok(w, ChainBSC, "ONE"), To: otherAdr, Value: q.Value, NetworkFee: new(big.Int), ExFee: new(big.Int)})
	bad4 := *q
	bad4.Tx.Data = data
	mustSeal(t, &bad4)
	var lv *LimitViolationError
	if _, err := c.Send(ctx, sg, &bad4, SendOptions{}); !errors.As(err, &lv) {
		t.Fatalf("tampered calldata: %v", err)
	}
	if len(w.approveCalls) != 0 || totalRaw(w) != 0 {
		t.Fatalf("something was sent")
	}
}

func TestX3TokenInOutsideConfigPassesBinding(t *testing.T) {
	w, k, a, c := r3World(t)
	usdtx := tok(w, ChainOne, "USDTx")
	q := mustQuote(t, c, QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: a, Recipient: recipient, X3: &X3Request{TokenIn: tokenA, TokenOut: usdtx, ToChain: ChainBSC}})
	sent, err := c.Send(ctx, w.signer(k, ChainOne), q, SendOptions{})
	if err != nil || !bytes.Equal(sent.Intent.Data, q.Tx.Data) {
		t.Fatalf("a user-supplied tokenIn must pass the binding and keep its calldata: %v", err)
	}
	// calldata that encodes another tokenOut than the request is internal tampering
	other, _ := EncodeSwapBridge(SwapBridgeInput{TokenIn: tokenA, TokenOut: otherAdr, AmountIn: n18(50), AmountOutMinimum: big.NewInt(1), ToChainID: uint64(w.net.Chains[ChainBSC].ChainID), Recipient: recipient, Deadline: 2_000_000_000})
	bad := *q
	bad.Tx.Data = other
	mustSeal(t, &bad)
	var lv *LimitViolationError
	if _, err := c.Send(ctx, w.signer(k, ChainOne), &bad, SendOptions{}); !errors.As(err, &lv) {
		t.Fatalf("tampered X3 calldata: %v", err)
	}
}

func TestNetworkBindingRejectsBeforeAnyApproveOrSignature(t *testing.T) {
	w := testnetWorld(t)
	key, a := w.fundedWallet()
	// (a) RPC points at the wrong chain
	bad := w.opts()
	bad.RPCs[ChainBSC] = newFakeChain(1)
	bc, _ := NewClient(ctx, Testnet, bad)
	_, err := bc.Quote(ctx, r1Req(a))
	var nm *NetworkMismatchError
	if !errors.As(err, &nm) || nm.Expected != 97 || nm.Actual != "1" {
		t.Fatalf("want NetworkMismatchError, got %v", err)
	}
	c := w.client()
	q := mustQuote(t, c, r1Req(a))
	// (b) signer bound to another network
	alien := NewKeyedSigner(key, big.NewInt(56))
	if _, err := c.Send(ctx, alien, q, SendOptions{}); !errors.As(err, &nm) {
		t.Fatalf("signer network: %v", err)
	}
	// (c) signer is not the quote sender
	other, _ := crypto.GenerateKey()
	var sm *SenderMismatchError
	if _, err := c.Send(ctx, w.signer(other, ChainBSC), q, SendOptions{}); !errors.As(err, &sm) {
		t.Fatalf("sender mismatch: %v", err)
	}
	// (d) a signer that LIES about its address: the recovered sender is verified before any broadcast
	lying := w.signer(other, ChainBSC)
	lying.Address = a
	var sbm *SignerMisbehaviorError
	if _, err := c.Send(ctx, lying, q, SendOptions{}); !errors.As(err, &sbm) || sbm.Field != "from" {
		t.Fatalf("recovered sender: %v", err)
	}
	if len(w.approveCalls) != 0 || w.chains[ChainBSC].rawSent != 0 {
		t.Fatalf("something was sent: approves=%d raw=%d", len(w.approveCalls), w.chains[ChainBSC].rawSent)
	}
}

func TestOverrideClientAcceptsItsOwnQuotesAndGettersNeedNoClient(t *testing.T) {
	built, _ := GetNetwork(Testnet)
	before, _ := json.Marshal(built)
	e := newOverrideEnv(t)
	if *e.nd.Chains[ChainOne].Bridge == *built.Chains[ChainOne].Bridge {
		t.Fatal("the override must really differ")
	}
	for name, q := range e.quotes(t) {
		if _, err := openQuote(q); err != nil {
			t.Fatalf("%s: openQuote (structural validation needs no config): %v", name, err)
		}
		// getters: no client, no config
		if r, err := q.ApprovedRequest(); err != nil || r.Amount == nil {
			t.Fatalf("%s: ApprovedRequest on an override-config quote: %v", name, err)
		}
		if l, err := q.ApprovedLimits(); err != nil || l.MaxTotalSpend == nil {
			t.Fatalf("%s: ApprovedLimits: %v", name, err)
		}
		_, data, _, _, _, err := e.client.BuildTx(q)
		if err != nil || !bytes.Equal(data, q.Tx.Data) {
			t.Fatalf("%s: BuildTx on the issuing client: %v", name, err)
		}
		if _, err := e.client.Preflight(ctx, q); err != nil {
			t.Fatalf("%s: Preflight: %v", name, err)
		}
		var sr *SimulationRevertError
		if err := e.client.Simulate(ctx, q); err != nil && !errors.As(err, &sr) {
			t.Fatalf("%s: Simulate: %v", name, err)
		}
	}
	after, _ := json.Marshal(built)
	if !bytes.Equal(before, after) {
		t.Fatal("the built-in networks data was mutated")
	}
}

func TestCrossClientQuoteIsRefusedByTheBindingCheck(t *testing.T) {
	e := newOverrideEnv(t)
	sg := e.w.signer(e.key, ChainBSC)
	for name, q := range e.quotes(t) {
		if _, err := openQuote(q); err != nil {
			t.Fatalf("%s: structurally valid: %v", name, err)
		}
		_, _, _, _, _, err := e.defaultClient.BuildTx(q)
		asMismatch(t, err, name+" BuildTx")
		_, err = e.defaultClient.Preflight(ctx, q)
		asMismatch(t, err, name+" Preflight")
		asMismatch(t, e.defaultClient.Simulate(ctx, q), name+" Simulate")
		_, err = e.defaultClient.EnsureAllowance(ctx, sg, q, SendOptions{})
		asMismatch(t, err, name+" EnsureAllowance")
		_, err = e.defaultClient.Send(ctx, sg, q, SendOptions{})
		asMismatch(t, err, name+" Send")
	}
	// reverse: default-config quotes into the override client
	dw, _, da, dc := r3World(t)
	usdtx := tok(dw, ChainOne, "USDTx")
	for name, r := range map[string]QuoteRequest{
		"X2": {Route: RouteOneToHyperCore, Amount: n18(10)}, "R5": {Route: RouteBSCToOneToHyperEVM, Amount: n18(10)},
		"X3": {Route: RouteSwapBridge, Amount: n18(50), X3: &X3Request{TokenIn: tokenA, TokenOut: usdtx, ToChain: ChainBSC}},
	} {
		r.Sender, r.Recipient = da, recipient
		q := mustQuote(t, dc, r)
		if _, err := openQuote(q); err != nil {
			t.Fatal(err)
		}
		_, _, _, _, _, err := e.client.BuildTx(q)
		asMismatch(t, err, name+" reverse BuildTx")
		_, err = e.client.Preflight(ctx, q)
		asMismatch(t, err, name+" reverse Preflight")
	}
}

func TestStructuralVsBindingX1TokenOutAndFee(t *testing.T) {
	e := newOverrideEnv(t)
	q := e.quotes(t)["X1"]
	c := e.nd.Chains[ChainOne]
	mk := func(tokenOut common.Address, fee uint32) *Quote {
		ed, err := EncodeX1ExtraData(X1Input{Router: addr(c.Swap.V3SwapRouter), TokenIn: common.HexToAddress(c.Tokens["USDTx"].Address), TokenOut: tokenOut, Fee: fee, Recipient: recipient, Deadline: q.Deadline, AmountIn: q.Value, AmountOutMinimum: q.Limits.AmountOutMinimum})
		if err != nil {
			t.Fatal(err)
		}
		return withExtra(t, q, ed)
	}
	oneusd := common.HexToAddress(c.Tokens["ONEUSD"].Address)
	badOut := mk(otherAdr, 500)
	if _, err := openQuote(badOut); err != nil {
		t.Fatalf("structural validation accepts a well-formed tokenOut: %v", err)
	}
	_, _, _, _, _, err := e.client.BuildTx(badOut)
	asMismatch(t, err, "tokenOut")
	if !strings.Contains(err.Error(), "tokenOut") {
		t.Fatalf("scope: %v", err)
	}
	badFee := mk(oneusd, 3000)
	if _, err := openQuote(badFee); err != nil {
		t.Fatalf("structural validation accepts a valid uint24 fee: %v", err)
	}
	_, _, _, _, _, err = e.client.BuildTx(badFee)
	asMismatch(t, err, "fee")
	_, err = openQuote(mk(common.Address{}, 500))
	invalidPayload(t, err, "details.extraData")
	// exact structural X1 paths: tokenIn vs details.hop1ToToken
	ed, _ := EncodeX1ExtraData(X1Input{Router: addr(c.Swap.V3SwapRouter), TokenIn: otherAdr, TokenOut: oneusd, Fee: 500, Recipient: recipient, Deadline: q.Deadline, AmountIn: q.Value, AmountOutMinimum: q.Limits.AmountOutMinimum})
	_, err = openQuote(withExtra(t, q, ed))
	invalidPayload(t, err, "details.extraData")
}

func TestBuiltInConfigUntouchedByTheOverrideSuite(t *testing.T) {
	built, _ := GetNetwork(Testnet)
	before, _ := json.Marshal(built)
	_ = newOverrideEnv(t)
	after, _ := json.Marshal(built)
	if !bytes.Equal(before, after) {
		t.Fatal("the built-in networks data changed")
	}
}

func TestPartialClientConfigKeepsRouterAndFillsOnlyOneusdAndPool(t *testing.T) {
	built, _ := GetNetwork(Testnet)
	nd := deepCopyNet(t, built)
	c := nd.Chains[ChainOne]
	s := func(v string) *string { return &v }
	c.Tokens["ONEUSD"] = &Token{Address: fakeAdr("a1000000006"), Symbol: s("ONEUSD"), Decimals: 18, Kind: "erc20"}
	c.Swap.X1Pool = &X1Pool{TokenIn: c.Tokens["USDTx"].Address, TokenOut: fakeAdr("a1000000006"), Fee: 500, Pool: fakeAdr("a1000000007")}
	e := newEnv(t, nd)
	if *nd.Chains[ChainOne].Swap.V3SwapRouter != *built.Chains[ChainOne].Swap.V3SwapRouter {
		t.Fatal("the existing router must be kept")
	}
	q := e.quotes(t)["X1"]
	if _, err := openQuote(q); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := e.client.BuildTx(q); err != nil {
		t.Fatalf("the issuing client accepts it: %v", err)
	}
	sg := e.w.signer(e.key, ChainBSC)
	_, _, _, _, _, err := e.defaultClient.BuildTx(q)
	mismatchAt(t, err, "ONEUSD", "BuildTx")
	_, err = e.defaultClient.Preflight(ctx, q)
	mismatchAt(t, err, "ONEUSD", "Preflight")
	mismatchAt(t, e.defaultClient.Simulate(ctx, q), "ONEUSD", "Simulate")
	_, err = e.defaultClient.EnsureAllowance(ctx, sg, q, SendOptions{})
	mismatchAt(t, err, "ONEUSD", "EnsureAllowance")
	_, err = e.defaultClient.Send(ctx, sg, q, SendOptions{})
	mismatchAt(t, err, "ONEUSD", "Send")
}

func TestOneVariantPerMissingConfigComponent(t *testing.T) {
	e := newOverrideEnv(t)
	quotes := e.quotes(t)
	nat := mustQuote(t, e.client, QuoteRequest{Route: RouteSwapBridge, Amount: n18(50), Sender: e.a, Recipient: recipient, X3: &X3Request{NativeIn: true, TokenOut: tok(e.w, ChainOne, "USDTx"), ToChain: ChainBSC}})
	sg := e.w.signer(e.key, ChainBSC)
	variants := []struct {
		name string
		q    *Quote
		edit func(ch map[ChainKey]*ChainConfig)
		path string
	}{
		{"ONEUSD missing", quotes["X1"], func(ch map[ChainKey]*ChainConfig) { delete(ch[ChainOne].Tokens, "ONEUSD") }, "chains.one.tokens.ONEUSD"},
		{"X1 pool missing", quotes["X1"], func(ch map[ChainKey]*ChainConfig) { ch[ChainOne].Swap.X1Pool = nil }, "chains.one.swap.x1Pool"},
		{"USDTx missing", quotes["X1"], func(ch map[ChainKey]*ChainConfig) { delete(ch[ChainOne].Tokens, "USDTx") }, "chains.one.tokens.USDTx"},
		{"X1 router missing", quotes["X1"], func(ch map[ChainKey]*ChainConfig) { ch[ChainOne].Swap.V3SwapRouter = nil }, "chains.one.swap.v3SwapRouter"},
		{"X2 dest token missing", quotes["X2"], func(ch map[ChainKey]*ChainConfig) { delete(ch[ChainHyperEVM].Tokens, "ONE") }, "chains.hyperevm.tokens.ONE"},
		{"R5+core inner dest token missing", quotes["R5+core"], func(ch map[ChainKey]*ChainConfig) { delete(ch[ChainHyperEVM].Tokens, "ONE") }, "chains.hyperevm.tokens.ONE"},
		{"X3 router missing", quotes["X3"], func(ch map[ChainKey]*ChainConfig) { ch[ChainOne].Swap.SwapBridgeRouter = nil }, "chains.one.swap.swapBridgeRouter"},
		{"X3 native WCROSS missing", nat, func(ch map[ChainKey]*ChainConfig) { delete(ch[ChainOne].Tokens, "WCROSS") }, "chains.one.tokens.WCROSS"},
		{"multihop hub missing", quotes["R5"], func(ch map[ChainKey]*ChainConfig) { ch[ChainOne].Bridge = nil }, "chains.one.bridge"},
		{"whole chain missing", quotes["X2"], func(ch map[ChainKey]*ChainConfig) { delete(ch, ChainHyperEVM) }, "chains.hyperevm"},
	}
	for _, v := range variants {
		nd2 := deepCopyNet(t, e.nd)
		v.edit(nd2.Chains)
		o := e.w.opts()
		o.Networks = nd2
		c2, err := NewClient(ctx, Testnet, o)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, _, _, err = c2.BuildTx(v.q)
		mismatchAt(t, err, v.path, v.name+" BuildTx")
		_, err = c2.Preflight(ctx, v.q)
		mismatchAt(t, err, v.path, v.name+" Preflight")
		_, err = c2.Send(ctx, sg, v.q, SendOptions{})
		mismatchAt(t, err, v.path, v.name+" Send")
	}
}
