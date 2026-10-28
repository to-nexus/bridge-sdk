// X1  BSC USDT -> ONE chain ONEUSD (BridgeExecutor extraCall: Uniswap V3 swap on the hub). Outcome: ONEUSD, or plain USDTx if the swap cannot run (never a loss).
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("X1", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCUSDTToONEUSD, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient, Swap: bridgesdk.SwapOptions{SlippageBps: runner.Bps(a.Extra["slippage-bps"]), DeadlineSec: bridgesdk.Seconds(1800)}}, nil
	})
}
