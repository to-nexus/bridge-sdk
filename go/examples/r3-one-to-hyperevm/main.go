// R3  ONE -> HyperEVM: native ONE becomes the ONE ERC20 on HyperEVM.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R3", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteOneToHyperEVM, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient}, nil
	})
}
