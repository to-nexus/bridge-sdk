// R4  HyperEVM -> ONE: ONE ERC20 on HyperEVM becomes native ONE.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R4", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteHyperEVMToOne, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient}, nil
	})
}
