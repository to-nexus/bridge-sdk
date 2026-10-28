// X3  Swap on the ONE chain (Uniswap V3 via SwapBridgeRouter) and bridge the output in ONE tx. Atomic: any failure reverts everything.
// Representative combination (assumption A6): ONEUSD -> USDTx -> BSC USDT (addresses come from the built-in config).
// Override with -tokenIn/-tokenOut/-fee/-to.  -tokenIn native = swap native ONE (through WCROSS) instead of an ERC20.
package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("X3", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		cross := net.Chain(bridgesdk.ChainCross)
		def := func(name string) common.Address {
			if t := cross.Tokens[name]; t != nil {
				return common.HexToAddress(t.Address)
			}
			return common.Address{}
		}
		x3 := &bridgesdk.X3Request{TokenIn: def("ONEUSD"), TokenOut: def("USDTx"), PoolFee: uint32(runner.Int(a.Extra["fee"])), ToChain: bridgesdk.ChainBSC}
		if v := a.Extra["tokenIn"]; v == "native" {
			x3.NativeIn = true
		} else if v != "" {
			x3.TokenIn = common.HexToAddress(v)
		}
		if v := a.Extra["tokenOut"]; v != "" {
			x3.TokenOut = common.HexToAddress(v)
		}
		if v := a.Extra["to"]; v != "" {
			x3.ToChain = bridgesdk.ChainKey(v)
		}
		if !x3.NativeIn && (x3.TokenIn == (common.Address{}) || x3.TokenOut == (common.Address{})) {
			return bridgesdk.QuoteRequest{}, fmt.Errorf("no default token pair on %s (ONEUSD is not configured): pass -tokenIn and -tokenOut", a.Network)
		}
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteSwapBridge, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient,
			Swap: bridgesdk.SwapOptions{SlippageBps: runner.Bps(a.Extra["slippage-bps"]), DeadlineSec: bridgesdk.Seconds(1800)}, X3: x3}, nil
	})
}
