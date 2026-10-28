// R6  HyperEVM -> ONE hub -> BSC with ONE bridge request (first-time ERC20 approve is separate).
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R6", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteHyperEVMToOneToBSC, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient, Recipients: bridgesdk.Recipients{OnHub: runner.Addr(a.Extra["recipient-hub"])}, Hop2FeeBuffer: runner.Units(a.Extra["buffer"])}, nil
	})
}
