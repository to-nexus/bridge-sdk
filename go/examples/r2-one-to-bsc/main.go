// R2  ONE -> BSC: native ONE becomes the CROSS ERC20 on BSC.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R2", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteOneToBSC, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient}, nil
	})
}
