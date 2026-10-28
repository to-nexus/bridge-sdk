// R1  BSC -> ONE: the ONE token on BSC (contract symbol CROSS) becomes native ONE on the ONE chain.
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
