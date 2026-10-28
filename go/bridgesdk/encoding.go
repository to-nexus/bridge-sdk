package bridgesdk

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

func u(v uint64) *big.Int { return new(big.Int).SetUint64(v) }

// BridgeTokenCall are the arguments of BaseBridge.bridgeToken.
type BridgeTokenCall struct {
	ToChainID  uint64
	FromToken  common.Address
	To         common.Address
	Value      *big.Int
	NetworkFee *big.Int
	ExFee      *big.Int
	ExtraData  []byte
}

func EncodeBridgeToken(i BridgeTokenCall) ([]byte, error) {
	return bridgeABI.Pack("bridgeToken", u(i.ToChainID), i.FromToken, i.To, i.Value, i.NetworkFee, i.ExFee, nonNil(i.ExtraData))
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// PackExtraData = target (20 B) || calldata
func PackExtraData(target common.Address, calldata []byte) []byte {
	out := make([]byte, 0, 20+len(calldata))
	out = append(out, target.Bytes()...)
	return append(out, calldata...)
}

type ForwardInput struct {
	Hub            common.Address
	DestChainID    uint64
	ToDest         common.Address
	Value2         *big.Int
	NetworkFee2    *big.Int
	ExFee2Declared *big.Int
	Inner          []byte
}

// EncodeForwardExtraData: hub bridge || bridgeTokenForwarded(destChainId, toDest, v2, nf2, ef2', inner)
func EncodeForwardExtraData(i ForwardInput) ([]byte, error) {
	call, err := bridgeABI.Pack("bridgeTokenForwarded", u(i.DestChainID), i.ToDest, i.Value2, i.NetworkFee2, i.ExFee2Declared, nonNil(i.Inner))
	if err != nil {
		return nil, err
	}
	return PackExtraData(i.Hub, call), nil
}

type CoreInput struct {
	Token         common.Address
	CoreRecipient common.Address
	Value         *big.Int
}

// EncodeCoreExtraData (X2): HyperEVM ONE token || transferToCoreFor(coreRecipient, value). Never use transferToCore(uint256).
func EncodeCoreExtraData(i CoreInput) ([]byte, error) {
	call, err := hyperABI.Pack("transferToCoreFor", i.CoreRecipient, i.Value)
	if err != nil {
		return nil, err
	}
	return PackExtraData(i.Token, call), nil
}

type X1Input struct {
	Router           common.Address
	TokenIn          common.Address
	TokenOut         common.Address
	Fee              uint32
	Recipient        common.Address
	Deadline         uint64
	AmountIn         *big.Int
	AmountOutMinimum *big.Int
}

type exactInputSingleParams struct {
	TokenIn           common.Address
	TokenOut          common.Address
	Fee               *big.Int
	Recipient         common.Address
	Deadline          *big.Int
	AmountIn          *big.Int
	AmountOutMinimum  *big.Int
	SqrtPriceLimitX96 *big.Int
}

// EncodeX1ExtraData: V3 SwapRouter || exactInputSingle((tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum, 0))
func EncodeX1ExtraData(i X1Input) ([]byte, error) {
	call, err := v3RouterABI.Pack("exactInputSingle", exactInputSingleParams{
		TokenIn: i.TokenIn, TokenOut: i.TokenOut, Fee: u(uint64(i.Fee)), Recipient: i.Recipient, Deadline: u(i.Deadline),
		AmountIn: i.AmountIn, AmountOutMinimum: i.AmountOutMinimum, SqrtPriceLimitX96: new(big.Int),
	})
	if err != nil {
		return nil, err
	}
	return PackExtraData(i.Router, call), nil
}

type SwapBridgeInput struct {
	TokenIn          common.Address
	TokenOut         common.Address
	Fee              uint32
	AmountIn         *big.Int
	AmountOutMinimum *big.Int
	ToChainID        uint64
	Recipient        common.Address
	ExtraData        []byte
	Deadline         uint64
	Native           bool
}

// EncodeSwapBridge: SwapBridgeRouter.swapBridgeExactInputSingle(ETH)((...), deadline)
func EncodeSwapBridge(i SwapBridgeInput) ([]byte, error) {
	p := binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams{
		TokenIn: i.TokenIn, TokenOut: i.TokenOut, Fee: u(uint64(i.Fee)), AmountIn: i.AmountIn, AmountOutMinimum: i.AmountOutMinimum,
		SqrtPriceLimitX96: new(big.Int),
		BridgeParams:      binding.ISwapBridgeRouterBridgeParams{ToChainID: u(i.ToChainID), Recipient: i.Recipient, ExtraData: nonNil(i.ExtraData)},
	}
	m := "swapBridgeExactInputSingle"
	if i.Native {
		m = "swapBridgeExactInputSingleETH"
	}
	return sbrABI.Pack(m, p, u(i.Deadline))
}

func hashExtraData(b []byte) common.Hash { return crypto.Keccak256Hash(nonNil(b)) }
