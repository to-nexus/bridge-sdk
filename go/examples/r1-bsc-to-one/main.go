// R1  BSC -> ONE (CROSS): CROSS ERC20 on BSC becomes native ONE on the CROSS chain.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R1", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOne, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient}, nil
	})
}
