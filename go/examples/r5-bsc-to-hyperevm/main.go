// R5  BSC -> ONE hub -> HyperEVM with ONE bridge request (first-time ERC20 approve is separate). -recipient-hub / -recipient (dest) / -recipient-core / -buffer <ONE> are optional.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("R5", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOneToHyperEVM, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient, Recipients: bridgesdk.Recipients{OnHub: runner.Addr(a.Extra["recipient-hub"]), Core: runner.Addr(a.Extra["recipient-core"])}, Hypercore: a.Extra["recipient-core"] != "", Hop2FeeBuffer: runner.Units(a.Extra["buffer"])}, nil
	})
}
