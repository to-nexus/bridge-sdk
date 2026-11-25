package bridgesdk

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"

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

// EncodeBridgeToken returns the calldata of BaseBridge.bridgeToken. A nil ExtraData is encoded as empty bytes. The caller must pass the fees
// exactly as the source verifier quoted them.
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

// ForwardInput is the hop-2 request of a multihop route (R5/R6) that is carried as extraData of hop 1 and executed on the hub.
type ForwardInput struct {
	Hub            common.Address // the hub bridge that receives hop 1 and runs the forward call
	DestChainID    uint64         // final destination chain id of hop 2
	ToDest         common.Address // hop-2 recipient on the destination chain
	Value2         *big.Int       // minimum final delivered amount (v2)
	NetworkFee2    *big.Int       // hop-2 network fee
	ExFee2Declared *big.Int       // declared hop-2 exFee: the hub exFee plus any Hop2FeeBuffer
	Inner          []byte         // extraData of hop 2 (empty, or an X2 call); nil is encoded as empty
}

// EncodeForwardExtraData: hub bridge || bridgeTokenForwarded(destChainId, toDest, v2, nf2, ef2', inner)
func EncodeForwardExtraData(i ForwardInput) ([]byte, error) {
	call, err := bridgeABI.Pack("bridgeTokenForwarded", u(i.DestChainID), i.ToDest, i.Value2, i.NetworkFee2, i.ExFee2Declared, nonNil(i.Inner))
	if err != nil {
		return nil, err
	}
	return PackExtraData(i.Hub, call), nil
}

// CoreInput is the HyperCore deposit (X2) executed on HyperEVM after arrival.
type CoreInput struct {
	Token         common.Address // the HyperEVM ONE token that performs the transfer
	CoreRecipient common.Address // account credited on HyperCore
	Value         *big.Int       // amount handed to the token; the token floors it to its Core unit and the dust goes back to the destination recipient
}

// EncodeCoreExtraData (X2): HyperEVM ONE token || transferToCoreFor(coreRecipient, value). Never use transferToCore(uint256).
func EncodeCoreExtraData(i CoreInput) ([]byte, error) {
	call, err := hyperABI.Pack("transferToCoreFor", i.CoreRecipient, i.Value)
	if err != nil {
		return nil, err
	}
	return PackExtraData(i.Token, call), nil
}

// X1Input is the swap executed on the hub after hop 1 arrives (exactInputSingle on the V3 swap router).
type X1Input struct {
	Router           common.Address // V3 swap router that is called
	TokenIn          common.Address
	TokenOut         common.Address
	Fee              uint32         // pool fee tier
	Recipient        common.Address // receives the swap output
	Deadline         uint64         // unix timestamp in seconds
	AmountIn         *big.Int       // must equal the bridged value
	AmountOutMinimum *big.Int       // minimum swap output
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

// SwapBridgeInput is the call to SwapBridgeRouter for X3: swap on the ONE chain, then bridge the output to ToChainID.
type SwapBridgeInput struct {
	TokenIn          common.Address
	TokenOut         common.Address
	Fee              uint32 // 3-byte pool key of the 1-hop path: the V3 fee tier, or the tick spacing on a Slipstream CL router
	AmountIn         *big.Int
	AmountOutMinimum *big.Int // minimum swap output
	ToChainID        uint64   // destination chain id of the bridge
	Recipient        common.Address
	ExtraData        []byte // bridge extraData; nil is encoded as empty
	Deadline         uint64 // unix timestamp in seconds
	Native           bool   // true: the input is native ONE sent as msg.value (the ETH variant of the call)
}

// maxPoolKey is the largest 3-byte pool key a path can carry.
const maxPoolKey = 0xffffff

// X3Path is the 1-hop SwapBridgeRouter path `tokenIn (20 B) | pool key (3 B) | tokenOut (20 B)`. The pool key is the V3 fee tier, or the tick
// spacing when the router is deployed against a Slipstream CL swap router: the router passes the 3 bytes through unchanged.
func X3Path(tokenIn common.Address, poolKey uint32, tokenOut common.Address) ([]byte, error) {
	if poolKey > maxPoolKey {
		return nil, fmt.Errorf("pool key %d does not fit in 3 bytes", poolKey)
	}
	p := make([]byte, 0, 43)
	p = append(p, tokenIn.Bytes()...)
	p = append(p, byte(poolKey>>16), byte(poolKey>>8), byte(poolKey))
	return append(p, tokenOut.Bytes()...), nil
}

// splitX3Path is the inverse of X3Path; any length other than one hop (43 B) is rejected.
func splitX3Path(p []byte) (tokenIn common.Address, poolKey uint32, tokenOut common.Address, ok bool) {
	if len(p) != 43 {
		return tokenIn, 0, tokenOut, false
	}
	return common.BytesToAddress(p[:20]), uint32(p[20])<<16 | uint32(p[21])<<8 | uint32(p[22]), common.BytesToAddress(p[23:]), true
}

// x3Method is the SwapBridgeRouter entry point X3 uses: the path variant (the single-pool variants do not exist on a CL router).
func x3Method(native bool) string {
	if native {
		return "swapBridgeExactInputETH"
	}
	return "swapBridgeExactInput"
}

// EncodeSwapBridge: SwapBridgeRouter.swapBridgeExactInput(ETH)((path, amountIn, amountOutMinimum, (toChainID, recipient, extraData)), deadline)
// with the 1-hop path X3Path(TokenIn, Fee, TokenOut).
func EncodeSwapBridge(i SwapBridgeInput) ([]byte, error) {
	path, err := X3Path(i.TokenIn, i.Fee, i.TokenOut)
	if err != nil {
		return nil, err
	}
	p := binding.ISwapBridgeRouterSwapBridgeExactInputParams{
		Path: path, AmountIn: i.AmountIn, AmountOutMinimum: i.AmountOutMinimum,
		BridgeParams: binding.ISwapBridgeRouterBridgeParams{ToChainID: u(i.ToChainID), Recipient: i.Recipient, ExtraData: nonNil(i.ExtraData)},
	}
	return sbrABI.Pack(x3Method(i.Native), p, u(i.Deadline))
}

// x3Call is a decoded X3 SwapBridgeRouter call (path variant, one hop).
type x3Call struct {
	TokenIn, TokenOut common.Address
	PoolKey           uint32
	AmountIn          *big.Int
	AmountOutMinimum  *big.Int
	Bridge            binding.ISwapBridgeRouterBridgeParams
	Deadline          *big.Int
}

// decodeX3Call decodes `swapBridgeExactInput(ETH)` calldata with a 1-hop path. The reason is empty on success.
func decodeX3Call(data []byte, native bool) (*x3Call, string) {
	m := sbrABI.Methods[x3Method(native)]
	if len(data) < 4 || !bytes.Equal(data[:4], m.ID) {
		return nil, "unexpected swap function"
	}
	out, err := m.Inputs.Unpack(data[4:])
	if err != nil || len(out) != 2 {
		return nil, "not a SwapBridgeRouter call"
	}
	p := abi.ConvertType(out[0], new(binding.ISwapBridgeRouterSwapBridgeExactInputParams)).(*binding.ISwapBridgeRouterSwapBridgeExactInputParams)
	tin, key, tout, ok := splitX3Path(p.Path)
	if !ok {
		return nil, "path is not a single pool (tokenIn | 3-byte pool key | tokenOut)"
	}
	return &x3Call{TokenIn: tin, TokenOut: tout, PoolKey: key, AmountIn: p.AmountIn, AmountOutMinimum: p.AmountOutMinimum, Bridge: p.BridgeParams,
		Deadline: out[1].(*big.Int)}, ""
}

func hashExtraData(b []byte) common.Hash { return crypto.Keccak256Hash(nonNil(b)) }
