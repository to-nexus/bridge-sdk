// X2 on R5  BSC -> ONE hub -> HyperEVM -> HyperCore with one bridge request (first-time ERC20 approve is separate). -recipient-core = HyperCore account (defaults to -recipient); -recipient-hub / -buffer <ONE> are optional.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R5+X2", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOneToHyperEVM, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient, Recipients: bridgesdk.Recipients{OnHub: runner.Addr(a.Extra["recipient-hub"]), Core: runner.Addr(a.Extra["recipient-core"])}, Hypercore: true, Hop2FeeBuffer: runner.Units(a.Extra["buffer"])}, nil
	})
}
