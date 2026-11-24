package bridgesdk

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

// Full decoding + validation of the extraData the SDK supports (X1 exactInputSingle on V3 or Slipstream CL, X2 transferToCoreFor). Anything else is unsupported.

func badExtra(kind, why string) error {
	return &UnsupportedRouteError{Msg: fmt.Sprintf("unsupported %s extraData: %s", kind, why)}
}

// x1Call is a decoded exactInputSingle call of either DEX: Fee is set for V3, TickSpacing for Slipstream CL.
type x1Call struct {
	Dex               DexKind
	Target            common.Address
	TokenIn           common.Address
	TokenOut          common.Address
	Fee               *big.Int // V3 only
	TickSpacing       *big.Int // CL only
	Recipient         common.Address
	Deadline          *big.Int
	AmountIn          *big.Int
	AmountOutMinimum  *big.Int
	SqrtPriceLimitX96 *big.Int
}

func dexLabel(d DexKind) string {
	if d == DexCL {
		return "Slipstream CL"
	}
	return "V3"
}

// decodeX1Call decodes `target(20 B) || exactInputSingle(params)` of either DEX (by selector) without any policy check.
func decodeX1Call(extra []byte) (*x1Call, error) {
	if len(extra) < 24 {
		return nil, fmt.Errorf("shorter than target + selector")
	}
	c := &x1Call{Target: common.BytesToAddress(extra[:20])}
	switch {
	case bytes.Equal(extra[20:24], ExactInputSingleSelector[:]):
		out, e := v3RouterABI.Methods["exactInputSingle"].Inputs.Unpack(extra[24:])
		if e != nil || len(out) != 1 {
			return nil, fmt.Errorf("undecodable call (%v)", e)
		}
		p := abi.ConvertType(out[0], new(exactInputSingleParams)).(*exactInputSingleParams)
		c.Dex, c.TokenIn, c.TokenOut, c.Fee, c.Recipient, c.Deadline, c.AmountIn, c.AmountOutMinimum, c.SqrtPriceLimitX96 =
			DexV3, p.TokenIn, p.TokenOut, p.Fee, p.Recipient, p.Deadline, p.AmountIn, p.AmountOutMinimum, p.SqrtPriceLimitX96
	case bytes.Equal(extra[20:24], CLExactInputSingleSelector[:]):
		out, e := clRouterABI.Methods["exactInputSingle"].Inputs.Unpack(extra[24:])
		if e != nil || len(out) != 1 {
			return nil, fmt.Errorf("undecodable call (%v)", e)
		}
		p := abi.ConvertType(out[0], new(clExactInputSingleParams)).(*clExactInputSingleParams)
		c.Dex, c.TokenIn, c.TokenOut, c.TickSpacing, c.Recipient, c.Deadline, c.AmountIn, c.AmountOutMinimum, c.SqrtPriceLimitX96 =
			DexCL, p.TokenIn, p.TokenOut, p.TickSpacing, p.Recipient, p.Deadline, p.AmountIn, p.AmountOutMinimum, p.SqrtPriceLimitX96
	default:
		return nil, fmt.Errorf("selector is not exactInputSingle (V3 0x%x or Slipstream CL 0x%x)", ExactInputSingleSelector[:], CLExactInputSingleSelector[:])
	}
	return c, nil
}

// decodeX1Struct is the STRUCTURAL, config-free X1 decoder: `target(20 B) || exactInputSingle(params)` (V3 or Slipstream CL, by selector) with a
// decodable call, a non-zero tokenOut, a valid uint24 fee (V3) or an int24 tickSpacing > 0 (CL), sqrtPriceLimitX96 == 0, a non-zero recipient, a uint64
// deadline and a canonical encoding (no trailing bytes). Nothing is compared to a configuration (which DEX is allowed is a configuration check: checkX1Net).
func decodeX1Struct(extra []byte) (*X1Expect, *x1Call, error) {
	p, err := decodeX1Call(extra)
	if err != nil {
		return nil, nil, badExtra("X1", err.Error())
	}
	switch {
	case p.TokenOut == (common.Address{}):
		return nil, nil, badExtra("X1", "tokenOut is the zero address")
	case p.Dex == DexV3 && (p.Fee == nil || !p.Fee.IsUint64() || p.Fee.Uint64() > 0xffffff):
		return nil, nil, badExtra("X1", fmt.Sprintf("fee %v is not a valid uint24", p.Fee))
	case p.Dex == DexCL && (p.TickSpacing == nil || !p.TickSpacing.IsInt64() || checkPoolTickSpacing(p.TickSpacing.Int64()) != nil):
		return nil, nil, badExtra("X1", fmt.Sprintf("tickSpacing %v is not a valid pool tick spacing (int24, > 0)", p.TickSpacing))
	case p.SqrtPriceLimitX96.Sign() != 0:
		return nil, nil, badExtra("X1", "sqrtPriceLimitX96 must be 0")
	case p.Recipient == (common.Address{}):
		return nil, nil, badExtra("X1", "recipient is the zero address")
	case !p.Deadline.IsUint64():
		return nil, nil, badExtra("X1", "deadline does not fit uint64")
	}
	in := X1Input{Router: p.Target, TokenIn: p.TokenIn, TokenOut: p.TokenOut, Recipient: p.Recipient, Deadline: p.Deadline.Uint64(), AmountIn: p.AmountIn, AmountOutMinimum: p.AmountOutMinimum}
	x := &X1Expect{Router: p.Target.Hex(), TokenIn: p.TokenIn.Hex(), TokenOut: p.TokenOut.Hex(), Recipient: p.Recipient.Hex(),
		Deadline: p.Deadline.String(), AmountIn: p.AmountIn.String(), AmountOutMinimum: p.AmountOutMinimum.String()}
	if p.Dex == DexCL {
		ts := int32(p.TickSpacing.Int64())
		in.Dex, in.TickSpacing = DexCL, ts
		x.Dex, x.TickSpacing = DexCL, &ts
	} else {
		in.Fee = uint32(p.Fee.Uint64())
		x.Fee = in.Fee
	}
	canon, err := EncodeX1ExtraData(in)
	if err != nil || !bytes.Equal(canon, extra) {
		return nil, nil, badExtra("X1", "non-canonical encoding (trailing or extra bytes)")
	}
	return x, p, nil
}

// checkX1 is the X1 decoder against THIS client's configuration (handle build / inference): the selector is the configured DEX's exactInputSingle,
// target == the configured swap router, tokenIn == hub USDTx, tokenOut == ONEUSD, fee (V3) / tickSpacing (CL) == the configured pool's,
// amountIn == the committed value. The client binding check uses the same comparisons (client.checkX1).
func (c *Client) checkX1(extra []byte, committed *big.Int) (*X1Expect, error) {
	return checkX1Net(c.net, extra, committed)
}

// checkX1Net is the same on an explicit network configuration (the issuing client's, never a built-in one chosen by the seal code).
func checkX1Net(net *NetworkData, extra []byte, committed *big.Int) (*X1Expect, error) {
	if _, err := net.Dex(); err != nil {
		return nil, badExtra("X1", err.Error())
	}
	oneChain := net.Chains[ChainOne]
	sv := swapViewOf(net)
	if sv.router == nil || sv.pool == nil || oneChain.Tokens["USDTx"] == nil || oneChain.Tokens["ONEUSD"] == nil {
		return nil, badExtra("X1", "the X1 router / pool / tokens are not configured")
	}
	router, usdtx, oneusd := addr(sv.router), common.HexToAddress(oneChain.Tokens["USDTx"].Address), common.HexToAddress(oneChain.Tokens["ONEUSD"].Address)
	x, p, err := decodeX1Struct(extra)
	if err != nil {
		return nil, err
	}
	switch {
	case p.Dex != sv.dex:
		sel := exactInputSelector(p.Dex)
		return nil, badExtra("X1", fmt.Sprintf("exactInputSingle selector 0x%x is the %s variant, but the configured dex is %q (expected selector 0x%x)", sel[:], dexLabel(p.Dex), sv.dex, exactInputSelector(sv.dex)))
	case common.HexToAddress(x.Router) != router:
		return nil, badExtra("X1", fmt.Sprintf("target %s is not the configured %s router", x.Router, dexLabel(sv.dex)))
	case p.TokenIn != usdtx:
		return nil, badExtra("X1", fmt.Sprintf("tokenIn %s is not the hub USDTx %s", p.TokenIn, usdtx))
	case p.TokenOut != oneusd:
		return nil, badExtra("X1", fmt.Sprintf("tokenOut %s is not the configured ONEUSD %s", p.TokenOut, oneusd))
	case sv.dex == DexV3 && x.Fee != sv.fee:
		return nil, badExtra("X1", fmt.Sprintf("fee %d != configured pool fee %d", x.Fee, sv.fee))
	case sv.dex == DexCL && *x.TickSpacing != sv.tickSpacing:
		return nil, badExtra("X1", fmt.Sprintf("tickSpacing %d != configured pool tickSpacing %d", *x.TickSpacing, sv.tickSpacing))
	case p.AmountIn.Cmp(committed) != 0:
		return nil, badExtra("X1", fmt.Sprintf("amountIn %s != the committed value %s", p.AmountIn, committed))
	}
	return x, nil
}

// swapBridgeCall is a decoded swapBridgeExactInputSingle(ETH) call of SwapBridgeRouter (Fee set) or SwapBridgeRouterCL (TickSpacing set).
type swapBridgeCall struct {
	TokenIn          common.Address
	TokenOut         common.Address
	Fee              *big.Int // V3 wrapper only
	TickSpacing      *big.Int // CL wrapper only
	AmountIn         *big.Int
	AmountOutMinimum *big.Int
	BridgeParams     binding.ISwapBridgeRouterBridgeParams
	Deadline         *big.Int
}

var (
	errSwapFunction = errors.New("unexpected swap function")
	errSwapCall     = errors.New("not a SwapBridgeRouter call")
)

// decodeSwapBridgeCall decodes the X3 calldata with the wrapper ABI of the given DEX: errSwapFunction when the selector is not the expected
// (ETH) function, errSwapCall when the arguments do not decode.
func decodeSwapBridgeCall(data []byte, native bool, dex DexKind) (*swapBridgeCall, error) {
	name := "swapBridgeExactInputSingle"
	if native {
		name = "swapBridgeExactInputSingleETH"
	}
	a := sbrABI
	if dex == DexCL {
		a = sbrCLABI
	}
	m := a.Methods[name]
	if len(data) < 4 || !bytes.Equal(data[:4], m.ID) {
		return nil, errSwapFunction
	}
	out, err := m.Inputs.Unpack(data[4:])
	if err != nil || len(out) != 2 {
		return nil, errSwapCall
	}
	c := &swapBridgeCall{Deadline: out[1].(*big.Int)}
	if dex == DexCL {
		p := abi.ConvertType(out[0], new(swapBridgeCLParams)).(*swapBridgeCLParams)
		c.TokenIn, c.TokenOut, c.TickSpacing, c.AmountIn, c.AmountOutMinimum, c.BridgeParams = p.TokenIn, p.TokenOut, p.TickSpacing, p.AmountIn, p.AmountOutMinimum, p.BridgeParams
		return c, nil
	}
	p := abi.ConvertType(out[0], new(binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)).(*binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)
	c.TokenIn, c.TokenOut, c.Fee, c.AmountIn, c.AmountOutMinimum, c.BridgeParams = p.TokenIn, p.TokenOut, p.Fee, p.AmountIn, p.AmountOutMinimum, p.BridgeParams
	return c, nil
}

// decodeX2Call decodes `target(20 B) || transferToCoreFor(coreRecipient, amount)` without any policy check.
func decodeX2Call(extra []byte) (target, coreRecipient common.Address, amount *big.Int, err error) {
	if len(extra) < 24 {
		return target, coreRecipient, nil, fmt.Errorf("shorter than target + selector")
	}
	target = common.BytesToAddress(extra[:20])
	if !bytes.Equal(extra[20:24], CoreSelector[:]) {
		return target, coreRecipient, nil, fmt.Errorf("selector is not transferToCoreFor(address,uint256)")
	}
	out, e := hyperABI.Methods["transferToCoreFor"].Inputs.Unpack(extra[24:])
	if e != nil || len(out) != 2 {
		return target, coreRecipient, nil, fmt.Errorf("undecodable call (%v)", e)
	}
	return target, out[0].(common.Address), out[1].(*big.Int), nil
}

// decodeX2Struct is the STRUCTURAL, config-free X2 decoder: selector, decodable call, non-zero coreRecipient, canonical encoding.
func decodeX2Struct(extra []byte) (*X2Expect, error) {
	target, rec, amount, err := decodeX2Call(extra)
	if err != nil {
		return nil, badExtra("X2", err.Error())
	}
	if rec == (common.Address{}) {
		return nil, badExtra("X2", "coreRecipient is the zero address")
	}
	canon, err := EncodeCoreExtraData(CoreInput{Token: target, CoreRecipient: rec, Value: amount})
	if err != nil || !bytes.Equal(canon, extra) {
		return nil, badExtra("X2", "non-canonical encoding (trailing or extra bytes)")
	}
	return &X2Expect{Token: target.Hex(), CoreRecipient: rec.Hex(), EncodedAmount: amount.String()}, nil
}

// checkX2 is the X2 decoder against THIS client's configuration: target == the configured HyperEVM ONE token, amount == the amount committed on the
// source side (the source BridgeInitiated.value for a direct ONE->HyperEVM X2, the forward payload's value for R5).
func (c *Client) checkX2(extra []byte, committed *big.Int) (*X2Expect, error) {
	return checkX2Net(c.net, extra, committed)
}

// checkX2Net is the same on an explicit network configuration (the issuing client's).
func checkX2Net(net *NetworkData, extra []byte, committed *big.Int) (*X2Expect, error) {
	he := net.Chains[ChainHyperEVM]
	if he.Tokens["ONE"] == nil {
		return nil, badExtra("X2", "the HyperEVM ONE token is not configured")
	}
	x, err := decodeX2Struct(extra)
	if err != nil {
		return nil, err
	}
	if common.HexToAddress(x.Token) != common.HexToAddress(he.Tokens["ONE"].Address) {
		return nil, badExtra("X2", fmt.Sprintf("target %s is not the configured HyperEVM ONE token", x.Token))
	}
	if bigOf(x.EncodedAmount).Cmp(committed) != 0 {
		return nil, badExtra("X2", fmt.Sprintf("encoded amount %s != the committed amount %s", x.EncodedAmount, committed))
	}
	return x, nil
}
