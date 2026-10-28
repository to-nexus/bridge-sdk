package bridgesdk

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Full decoding + validation of the extraData the SDK supports (X1 exactInputSingle, X2 transferToCoreFor). Anything else is unsupported.

func badExtra(kind, why string) error {
	return &UnsupportedRouteError{Msg: fmt.Sprintf("unsupported %s extraData: %s", kind, why)}
}

// decodeX1Call decodes `target(20 B) || exactInputSingle(params)` without any policy check.
func decodeX1Call(extra []byte) (target common.Address, p exactInputSingleParams, err error) {
	if len(extra) < 24 {
		return target, p, fmt.Errorf("shorter than target + selector")
	}
	target = common.BytesToAddress(extra[:20])
	if !bytes.Equal(extra[20:24], ExactInputSingleSelector[:]) {
		return target, p, fmt.Errorf("selector is not exactInputSingle")
	}
	out, e := v3RouterABI.Methods["exactInputSingle"].Inputs.Unpack(extra[24:])
	if e != nil || len(out) != 1 {
		return target, p, fmt.Errorf("undecodable call (%v)", e)
	}
	p = *abi.ConvertType(out[0], new(exactInputSingleParams)).(*exactInputSingleParams)
	return target, p, nil
}

// decodeX1Struct is the STRUCTURAL, config-free X1 decoder: `target(20 B) || exactInputSingle(params)` with a decodable call, a non-zero tokenOut,
// a valid uint24 fee, sqrtPriceLimitX96 == 0, a non-zero recipient, a uint64 deadline and a canonical encoding (no trailing bytes). Nothing is compared to a configuration.
func decodeX1Struct(extra []byte) (*X1Expect, *exactInputSingleParams, error) {
	target, p, err := decodeX1Call(extra)
	if err != nil {
		return nil, nil, badExtra("X1", err.Error())
	}
	switch {
	case p.TokenOut == (common.Address{}):
		return nil, nil, badExtra("X1", "tokenOut is the zero address")
	case p.Fee == nil || !p.Fee.IsUint64() || p.Fee.Uint64() > 0xffffff:
		return nil, nil, badExtra("X1", fmt.Sprintf("fee %v is not a valid uint24", p.Fee))
	case p.SqrtPriceLimitX96.Sign() != 0:
		return nil, nil, badExtra("X1", "sqrtPriceLimitX96 must be 0")
	case p.Recipient == (common.Address{}):
		return nil, nil, badExtra("X1", "recipient is the zero address")
	case !p.Deadline.IsUint64():
		return nil, nil, badExtra("X1", "deadline does not fit uint64")
	}
	canon, err := EncodeX1ExtraData(X1Input{Router: target, TokenIn: p.TokenIn, TokenOut: p.TokenOut, Fee: uint32(p.Fee.Uint64()), Recipient: p.Recipient, Deadline: p.Deadline.Uint64(), AmountIn: p.AmountIn, AmountOutMinimum: p.AmountOutMinimum})
	if err != nil || !bytes.Equal(canon, extra) {
		return nil, nil, badExtra("X1", "non-canonical encoding (trailing or extra bytes)")
	}
	return &X1Expect{Router: target.Hex(), TokenIn: p.TokenIn.Hex(), TokenOut: p.TokenOut.Hex(), Fee: uint32(p.Fee.Uint64()), Recipient: p.Recipient.Hex(),
		Deadline: p.Deadline.String(), AmountIn: p.AmountIn.String(), AmountOutMinimum: p.AmountOutMinimum.String()}, &p, nil
}

// checkX1 is the X1 decoder against THIS client's configuration (handle build / inference): target == the configured V3 router, tokenIn == hub USDTx,
// tokenOut == ONEUSD, fee == the configured pool fee, amountIn == the committed value. The client binding check uses the same comparisons (client.checkX1).
func (c *Client) checkX1(extra []byte, committed *big.Int) (*X1Expect, error) {
	return checkX1Net(c.net, extra, committed)
}

// checkX1Net is the same on an explicit network configuration (the issuing client's, never a built-in one chosen by the seal code).
func checkX1Net(net *NetworkData, extra []byte, committed *big.Int) (*X1Expect, error) {
	cross := net.Chains[ChainCross]
	if cross.Swap == nil || cross.Swap.V3SwapRouter == nil || cross.Swap.X1Pool == nil || cross.Tokens["USDTx"] == nil || cross.Tokens["ONEUSD"] == nil {
		return nil, badExtra("X1", "the X1 router / pool / tokens are not configured")
	}
	router, usdtx, oneusd := addr(cross.Swap.V3SwapRouter), common.HexToAddress(cross.Tokens["USDTx"].Address), common.HexToAddress(cross.Tokens["ONEUSD"].Address)
	x, p, err := decodeX1Struct(extra)
	if err != nil {
		return nil, err
	}
	switch {
	case common.HexToAddress(x.Router) != router:
		return nil, badExtra("X1", fmt.Sprintf("target %s is not the configured V3 router", x.Router))
	case p.TokenIn != usdtx:
		return nil, badExtra("X1", fmt.Sprintf("tokenIn %s is not the hub USDTx %s", p.TokenIn, usdtx))
	case p.TokenOut != oneusd:
		return nil, badExtra("X1", fmt.Sprintf("tokenOut %s is not the configured ONEUSD %s", p.TokenOut, oneusd))
	case x.Fee != cross.Swap.X1Pool.Fee:
		return nil, badExtra("X1", fmt.Sprintf("fee %d != configured pool fee %d", x.Fee, cross.Swap.X1Pool.Fee))
	case p.AmountIn.Cmp(committed) != 0:
		return nil, badExtra("X1", fmt.Sprintf("amountIn %s != the committed value %s", p.AmountIn, committed))
	}
	return x, nil
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
