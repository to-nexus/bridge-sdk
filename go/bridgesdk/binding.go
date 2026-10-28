package bridgesdk

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

// The ONE way the binding check reads a config-owned value (same pattern as the JS requireCfg): a component this client's config does not have raises
// *NetworkMismatchError naming the path (a quote that relies on a component the client lacks does not belong to that client), never a nil dereference.
func cfgMissing(path, quoteValue string) error {
	return &NetworkMismatchError{Scope: path, ExpectedValue: "null (not configured by this client)", Actual: quoteValue}
}

func (c *Client) cfgChain(key ChainKey, quoteValue string) (*ChainConfig, error) {
	if ch := c.net.Chains[key]; ch != nil {
		return ch, nil
	}
	return nil, cfgMissing("chains."+string(key), quoteValue)
}

func (c *Client) cfgBridge(key ChainKey, quoteValue string) (string, error) {
	ch, err := c.cfgChain(key, quoteValue)
	if err != nil {
		return "", err
	}
	if ch.Bridge == nil || *ch.Bridge == "" {
		return "", cfgMissing("chains."+string(key)+".bridge", quoteValue)
	}
	return *ch.Bridge, nil
}

func (c *Client) cfgToken(key ChainKey, name, quoteValue string) (string, error) {
	ch, err := c.cfgChain(key, quoteValue)
	if err != nil {
		return "", err
	}
	if t := ch.Tokens[name]; t != nil && t.Address != "" {
		return t.Address, nil
	}
	return "", cfgMissing("chains."+string(key)+".tokens."+name+".address", quoteValue)
}

// cfgSwap returns cross.swap.<field> ("v3SwapRouter" | "swapBridgeRouter").
func (c *Client) cfgSwap(field, quoteValue string) (string, error) {
	ch, err := c.cfgChain(ChainCross, quoteValue)
	if err != nil {
		return "", err
	}
	var v *string
	if ch.Swap != nil {
		switch field {
		case "v3SwapRouter":
			v = ch.Swap.V3SwapRouter
		case "swapBridgeRouter":
			v = ch.Swap.SwapBridgeRouter
		}
	}
	if v == nil || *v == "" {
		return "", cfgMissing("chains.cross.swap."+field, quoteValue)
	}
	return *v, nil
}

func (c *Client) cfgPoolFee(quoteValue string) (uint32, error) {
	ch, err := c.cfgChain(ChainCross, quoteValue)
	if err != nil {
		return 0, err
	}
	if ch.Swap == nil || ch.Swap.X1Pool == nil {
		return 0, cfgMissing("chains.cross.swap.x1Pool.fee", quoteValue)
	}
	return ch.Swap.X1Pool.Fee, nil
}

// assertQuoteBelongs is the quote -> client binding check (config equality against THIS client's own networks config, c.net); (it runs FIRST in Send, before signer checks and before any approve; a re-quote
// re-derives from THIS client's configuration, so it could otherwise silently re-target another network).
//   - Network / SourceChainID / SourceChain must equal this client's configuration;
//   - every CONFIG-OWNED address embedded in the quote (source bridge / router, hub, fixed route tokens, hop and executor targets,
//     approve spender) must equal this client's configuration: mismatch => *NetworkMismatchError;
//   - USER-SUPPLIED values (X3 tokens, recipients, amount) are not checked against the configuration: they must equal the request
//     snapshot and the built calldata must encode exactly those values: mismatch => *LimitViolationError (internal tampering).
//     The calldata is never re-targeted.
func (c *Client) assertQuoteBelongs(q *Quote) error {
	mism := func(field, client, quote string) error {
		return &NetworkMismatchError{Scope: "quote." + field, ExpectedValue: client, Actual: quote}
	}
	tamper := func(what string) error { return &LimitViolationError{Violated: []string{"tampered:" + what}, Quote: q} }
	same := func(a common.Address, b string) bool { return b != "" && a == common.HexToAddress(b) }

	if q.Network != c.network {
		return mism("network", string(c.network), string(q.Network))
	}
	src, err := c.cfgChain(q.SourceChain, string(q.SourceChain))
	if err != nil {
		return err
	}
	if q.SourceChainID != src.ChainID {
		return &NetworkMismatchError{Scope: "quote.sourceChainId", Expected: src.ChainID, Actual: fmt.Sprint(q.SourceChainID)}
	}
	req := q.Request
	spec, err := specFor(&req)
	if err != nil {
		return tamper("request")
	}
	if spec.src != q.SourceChain {
		return mism("sourceChain", string(spec.src), string(q.SourceChain))
	}
	if spec.kind != q.Kind {
		return tamper("kind")
	}
	cross, err := c.cfgChain(ChainCross, q.SourceBridge.Hex())
	if err != nil {
		return err
	}
	wantBridge, err := c.cfgBridge(q.SourceChain, q.SourceBridge.Hex())
	if err != nil {
		return err
	}
	wantContract := wantBridge
	if q.Kind == KindX3 {
		if wantContract, err = c.cfgSwap("swapBridgeRouter", q.SourceContract.Hex()); err != nil {
			return err
		}
	}
	if !same(q.SourceBridge, wantBridge) {
		return mism("sourceBridge", wantBridge, q.SourceBridge.Hex())
	}
	if !same(q.SourceContract, wantContract) {
		return mism("sourceContract", wantContract, q.SourceContract.Hex())
	}
	if !same(q.Tx.To, wantContract) {
		return mism("tx.to", wantContract, q.Tx.To.Hex())
	}
	if q.Approval != nil && !same(q.Approval.Spender, wantContract) {
		return mism("approval.spender", wantContract, q.Approval.Spender.Hex())
	}
	recHub, recDest, _ := resolveRecipients(&req)
	destChain, err := c.cfgChain(spec.dest, fmt.Sprint(q.Details.DestChainID))
	if err != nil {
		return err
	}
	destChainID := destChain.ChainID
	hubID := cross.ChainID
	// the chain id the payload carries must be the CLIENT's chain id for this route (structural validation only checks it against the calldata)
	wantDestID := destChainID
	if spec.kind == KindX1 {
		wantDestID = hubID
	}
	if int64(q.Details.DestChainID) != wantDestID {
		return &NetworkMismatchError{Scope: "quote.details.destChainId", Expected: wantDestID, Actual: fmt.Sprint(q.Details.DestChainID)}
	}

	if q.Kind == KindX3 {
		x3 := req.X3
		if x3 == nil {
			return tamper("x3 request")
		}
		name := "swapBridgeExactInputSingle"
		if x3.NativeIn {
			name = "swapBridgeExactInputSingleETH"
		}
		m := sbrABI.Methods[name]
		if len(q.Tx.Data) < 4 || !bytes.Equal(q.Tx.Data[:4], m.ID) {
			return tamper("swap function")
		}
		out, err := m.Inputs.Unpack(q.Tx.Data[4:])
		if err != nil || len(out) != 2 {
			return tamper("calldata is not a SwapBridgeRouter call")
		}
		p := abi.ConvertType(out[0], new(binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)).(*binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)
		if x3.NativeIn {
			wcross, err := c.cfgToken(ChainCross, "WCROSS", p.TokenIn.Hex())
			if err != nil {
				return err
			}
			if !same(p.TokenIn, wcross) {
				return mism("tokenIn(WCROSS)", wcross, p.TokenIn.Hex())
			}
		}
		if !x3.NativeIn && p.TokenIn != x3.TokenIn {
			return tamper("tokenIn")
		}
		if p.TokenOut != x3.TokenOut {
			return tamper("tokenOut")
		}
		if p.Fee.Uint64() != uint64(x3.PoolFee) {
			return tamper("poolFee")
		}
		if p.AmountIn.Cmp(req.Amount) != 0 {
			return tamper("amountIn")
		}
		if p.BridgeParams.ToChainID.Int64() != destChainID {
			return mism("toChainID", fmt.Sprint(destChainID), p.BridgeParams.ToChainID.String())
		}
		if p.BridgeParams.Recipient != recDest {
			return tamper("recipient")
		}
		if len(p.BridgeParams.ExtraData) != 0 {
			return tamper("extraData")
		}
		if q.Approval != nil && (q.Approval.Token != x3.TokenIn || q.Approval.Amount.Cmp(req.Amount) != 0) {
			return tamper("approval")
		}
		if x3.NativeIn && q.Tx.Value.Cmp(req.Amount) != 0 {
			return tamper("msg.value")
		}
		return nil
	}

	// bridge routes: bridgeToken(toChainId, fromToken, to, value, nf, ef, extraData)
	m := bridgeABI.Methods["bridgeToken"]
	if len(q.Tx.Data) < 4 || !bytes.Equal(q.Tx.Data[:4], m.ID) {
		return tamper("calldata is not a bridgeToken call")
	}
	args, err := m.Inputs.Unpack(q.Tx.Data[4:])
	if err != nil || len(args) != 7 {
		return tamper("calldata is not a bridgeToken call")
	}
	wantToken := NativeToken
	if spec.srcToken != "native" {
		a, err := c.cfgToken(q.SourceChain, spec.srcToken, args[1].(common.Address).Hex())
		if err != nil {
			return err
		}
		wantToken = a
	}
	if !same(args[1].(common.Address), wantToken) {
		return mism("fromToken", wantToken, args[1].(common.Address).Hex())
	}
	if q.Approval != nil && !same(q.Approval.Token, wantToken) {
		return mism("approval.token", wantToken, q.Approval.Token.Hex())
	}
	hop1To := destChainID
	if spec.kind == KindMultihop || spec.kind == KindX1 {
		hop1To = hubID
	}
	if args[0].(*big.Int).Int64() != hop1To {
		return mism("toChainID", fmt.Sprint(hop1To), args[0].(*big.Int).String())
	}
	wantRcpt := recDest
	if spec.kind == KindMultihop {
		wantRcpt = recHub
	}
	if args[2].(common.Address) != wantRcpt {
		return tamper("recipient")
	}
	if q.Value == nil || args[3].(*big.Int).Cmp(q.Value) != 0 {
		return tamper("value")
	}
	ed := args[6].([]byte)
	var target common.Address
	var sel [4]byte
	if len(ed) >= 20 {
		target = common.BytesToAddress(ed[:20])
	}
	if len(ed) >= 24 {
		copy(sel[:], ed[20:24])
	}
	switch {
	case spec.kind == KindMultihop:
		hub, err := c.cfgBridge(ChainCross, target.Hex())
		if err != nil {
			return err
		}
		if !same(target, hub) || sel != ForwardSelector {
			return mism("extraData.target(hub bridge)", hub, target.Hex())
		}
		f, err := decodeForward(ed)
		if err != nil {
			return tamper("forward extraData")
		}
		if f.ToChainID.Int64() != destChainID {
			return mism("hop2 toChainID", fmt.Sprint(destChainID), f.ToChainID.String())
		}
		if f.To != recDest {
			return tamper("hop2 recipient")
		}
		if len(f.Inner) > 0 {
			inner := fmt.Sprintf("%x", f.Inner[:min(20, len(f.Inner))])
			he, err := c.cfgToken(ChainHyperEVM, "ONE", inner)
			if err != nil {
				return err
			}
			if len(f.Inner) < 24 || !same(common.BytesToAddress(f.Inner[:20]), he) || [4]byte(f.Inner[20:24]) != CoreSelector {
				return mism("extraData.inner.target(HyperEVM ONE)", he, inner)
			}
		}
	case spec.kind == KindX1:
		v3, err := c.cfgSwap("v3SwapRouter", target.Hex())
		if err != nil {
			return err
		}
		if !same(target, v3) || sel != ExactInputSingleSelector {
			return mism("extraData.target(V3 router)", v3, target.Hex())
		}
		_, p, err := decodeX1Call(ed)
		if err != nil {
			return tamper("x1 extraData")
		}
		usdtx, err := c.cfgToken(ChainCross, "USDTx", p.TokenIn.Hex())
		if err != nil {
			return err
		}
		if !same(p.TokenIn, usdtx) {
			return mism("x1.tokenIn(USDTx)", usdtx, p.TokenIn.Hex())
		}
		if !same(q.Details.Hop1ToToken, usdtx) {
			return mism("details.hop1ToToken(USDTx)", usdtx, q.Details.Hop1ToToken.Hex())
		}
		oneusd, err := c.cfgToken(ChainCross, "ONEUSD", p.TokenOut.Hex())
		if err != nil {
			return err
		}
		if !same(p.TokenOut, oneusd) {
			return mism("x1.tokenOut(ONEUSD)", oneusd, p.TokenOut.Hex())
		}
		poolFee, err := c.cfgPoolFee(p.Fee.String())
		if err != nil {
			return err
		}
		if p.Fee.Uint64() != uint64(poolFee) {
			return mism("x1.fee", fmt.Sprint(poolFee), p.Fee.String())
		}
		if p.Recipient != recDest {
			return tamper("x1 swap recipient")
		}
	case spec.core:
		he, err := c.cfgToken(ChainHyperEVM, "ONE", target.Hex())
		if err != nil {
			return err
		}
		if !same(target, he) || sel != CoreSelector {
			return mism("extraData.target(HyperEVM ONE)", he, target.Hex())
		}
	default:
		if len(ed) != 0 {
			return tamper("extraData")
		}
	}
	return nil
}
