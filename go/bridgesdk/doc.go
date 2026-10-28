// Package bridgesdk is the Go SDK for the ONE bridge. It moves tokens between BSC, the ONE chain (the hub) and HyperEVM, from the fee quote to
// confirmed delivery.
//
// Routes: R1 BSC to ONE, R2 ONE to BSC, R3 ONE to HyperEVM, R4 HyperEVM to ONE, R5 BSC to ONE to HyperEVM and R6 HyperEVM to ONE to BSC
// (two hops through the hub), X1 BSC USDT to ONEUSD (bridge plus swap), X2 HyperCore deposit, X3 swap then bridge. See the Route constants.
//
// The standard flow is NewClient, Quote, optionally Preflight, Send, WaitHandle and Track:
//
//	c, err := bridgesdk.NewClient(ctx, bridgesdk.Testnet, nil)
//	q, err := c.Quote(ctx, bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOne, Amount: amount, Sender: from, Recipient: from})
//	rep, err := c.Preflight(ctx, q) // optional: rep.OK / rep.Issues tell what would block the transfer
//	signer := bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID))
//	sent, err := c.Send(ctx, signer, q, bridgesdk.SendOptions{})
//	h, err := c.WaitHandle(ctx, sent)                              // *TransferHandle: persist it as JSON
//	res, err := c.Track(ctx, h, bridgesdk.TrackOpts{Timeout: time.Minute})
//
// QuoteRequest.Amount means: the delivered amount for single-hop routes, the minimum final delivered amount for R5 and R6, the USDT amount
// for X1, and the swap input for X3.
//
// Safety rules:
//   - A Quote is sealed and immutable. Its exported fields are informational; a quote that was edited is refused by Send. To change anything,
//     call Quote again. Read the sealed values with Quote.ApprovedRequest and Quote.ApprovedLimits.
//   - Send never re-sends on its own and never loosens the limits of the quote it was given.
//   - On *BroadcastOutcomeUnknownError do NOT send again: the transaction may be on chain. Use FindSourceTransfer with the error's Intent and
//     Checkpoint (or Rebroadcast with its Raw bytes).
//   - If SentTx.CallbackError is set, an OnBroadcast callback panicked; the transaction has already been sent.
//   - Persist the *TransferHandle (it is plain JSON). Track resumes from it after a restart, and HandleFromTx rebuilds one from a hash.
//   - Errors are pointer types; match them with errors.As. DecodeBridgeError explains revert data.
package bridgesdk

import (
	_ "github.com/to-nexus/bridge-contracts/binding/go/src"
)
