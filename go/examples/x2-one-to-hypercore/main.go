// X2  ONE -> HyperEVM with a HyperCore deposit (transferToCoreFor). -recipient-core = HyperCore account.
package main

import (
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
	"github.com/to-nexus/bridge-sdk/go/examples/internal/runner"
)

func main() {
	runner.Run("X2", func(a runner.Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error) {
		return bridgesdk.QuoteRequest{Route: bridgesdk.RouteOneToHyperCore, Amount: a.Amount, Sender: a.Sender, Recipient: a.Recipient, Recipients: bridgesdk.Recipients{Core: runner.Addr(a.Extra["recipient-core"])}}, nil
	})
}
