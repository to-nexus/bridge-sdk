# bridge-sdk/go

English | [한국어](README_ko.md)

Go SDK for the ONE Bridge. It handles transfers between BSC, the ONE chain and HyperEVM, from the fee quote to confirmed delivery.

- Go 1.24 or later
- Mainnet and testnet settings built in
- Uses the contract bindings from [`github.com/to-nexus/bridge-contracts/binding/go`](https://github.com/to-nexus/bridge-contracts)

See the [root README](../README.md) for the supported routes and an overview.

## Installation

```bash
go get github.com/to-nexus/bridge-sdk/go@v0.1.0
```

Version tags have the form `go/vX.Y.Z`, because this module lives in the repository's `go/` directory. With `go get`, write the version without the prefix (`@vX.Y.Z`).

```go
import "github.com/to-nexus/bridge-sdk/go/bridgesdk"
```

## Usage

```go
ctx := context.Background()
c, err := bridgesdk.NewClient(ctx, bridgesdk.Mainnet, nil)
if err != nil {
	return err
}

amount, _ := bridgesdk.ParseUnits("10", 18)
q, err := c.Quote(ctx, bridgesdk.QuoteRequest{
	Route:     bridgesdk.RouteBSCToOneToHyperEVM,
	Amount:    amount,
	Sender:    from,
	Recipient: from,
})
if err != nil {
	return err
}

// Optional: see what would block the transfer
if rep, err := c.Preflight(ctx, q); err == nil && !rep.OK {
	log.Println(rep.Issues)
}

signer := bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID))
sent, err := c.Send(ctx, signer, q, bridgesdk.SendOptions{
	// Store the signed transaction before it is broadcast. Returning an error cancels the send.
	OnSigned: func(s bridgesdk.SignedInfo, in *bridgesdk.SubmittedIntent) error { return store.Save(s, in) },
})
if err != nil {
	return err
}

h, err := c.WaitHandle(ctx, sent)            // *TransferHandle (store it as JSON)
if err != nil {
	return err
}
res, err := c.Track(ctx, h, bridgesdk.TrackOpts{Timeout: 10 * time.Minute})
```

### Client options

```go
c, err := bridgesdk.NewClient(ctx, bridgesdk.Mainnet, &bridgesdk.ClientOptions{
	RPCURLs:     map[bridgesdk.ChainKey]string{bridgesdk.ChainBSC: "https://..."}, // override the RPC per chain
	CallTimeout: 15 * time.Second,                                            // timeout for each RPC call
	QuoteTTL:    30 * time.Second,                                            // how long a quote stays valid
})
```

### Quote request (`QuoteRequest`)

| Field | Description |
|---|---|
| `Route` | `RouteBSCToOne` and so on ([supported routes](../README.md#supported-routes)) |
| `Amount` | `*big.Int` in wei. Delivered amount for single-hop routes, minimum final delivered amount for R5/R6, USDT amount for X1, swap input for X3 |
| `Sender` · `Recipient` | Sending / receiving address (default for every hop) |
| `Recipients` | Per-hop recipients `{OnHub, Dest, Core}` (optional) |
| `Hypercore` | For R5, also deposit into HyperCore (X2) (optional) |
| `Swap` | `SwapOptions{SlippageBps, DeadlineSec}` for X1 and X3. `nil` means 50 bps / 1800 seconds; an explicit value such as `bridgesdk.Bps(0)` is used as given |
| `X3` | X3 only: `*X3Request` |
| `Hop2FeeBuffer` | Extra margin added to the second-hop fee of a multihop route (optional) |
| `MaxTotalSpend` · `MinDelivered` | Your own spending cap and delivery floor (optional) |

## API

| Function | Description |
|---|---|
| `NewClient(ctx, network, opts)` | Creates a client. Each RPC is dialled on first use and its chain ID is verified |
| `Quote` | Reads the fees and builds the transaction to send |
| `Preflight` | Checks bridge pauses, pairs, balances, allowances and more; returns `OK` and `Issues` |
| `Simulate` · `BuildTx` | Read-only simulation / builds the transaction to send (for signing it yourself) |
| `EnsureAllowance` | Runs only the approve |
| `Send` | approve → re-quote → checks → simulation → sign → broadcast |
| `WaitHandle` | Waits for the source receipt and builds a `*TransferHandle` |
| `Track` · `TrackOnce` | Tracks the transfer until delivery / reads the current state once |
| `HandleFromTx` | Rebuilds a `*TransferHandle` from a transaction hash |
| `FindSourceTransfer` | Finds the transfer on the source chain when the hash is unknown |
| `AnalyzeReplacement` · `Rebroadcast` | Decides whether another transaction was mined at the same nonce / re-broadcasts a signed transaction |
| `CheckCoreBalance` | Reads a HyperCore spot balance (X2) |
| `DecodeBridgeError` | Decodes revert data into the error name, arguments and a hint on what to do |
| `NewKeyedSigner` · `NewTxSignerFromOpts` | Creates a signer from a private key / from `*bind.TransactOpts` |
| `ParseUnits` · `FormatUnits` | Converts between decimal strings and wei |

`TransferHandle` uses the same JSON format as the JS SDK, so either SDK can track a handle created by the other.

## Error handling

Errors are pointer types; match them with `errors.As`.

```go
sent, err := c.Send(ctx, signer, q, bridgesdk.SendOptions{})
var unknown *bridgesdk.BroadcastOutcomeUnknownError
var pf *bridgesdk.PreflightError
switch {
case errors.As(err, &unknown):
	// The outcome is unknown: do not send again, check first
	found, _ := c.FindSourceTransfer(ctx, unknown.Intent, unknown.Checkpoint)
	_ = found
case errors.As(err, &pf):
	log.Println(pf.Stage, pf.Issues) // nothing was signed
}
```

| Error | When | What to do |
|---|---|---|
| `*AddressNotConfiguredError` | The route is not open on this network yet | Use another route or network |
| `*PreflightError` | A bridge is paused, the balance or allowance is too low, and so on. See `Issues[].Code` | Fix the cause, then `Quote` again |
| `*RequoteRequiredError` | The re-quote after the approve came back above the original limits because fees rose | `Quote` again |
| `*QuoteExpiredError` | The quote is older than its time to live | `Quote` again |
| `*NetworkMismatchError` | The RPC is on a different chain than configured | Check the RPC |
| `*SimulationRevertError` | The simulation reverted | Use `DecodeBridgeError` to see why |
| `*BroadcastOutcomeUnknownError` | The broadcast request got no answer, so the outcome is unknown | **Do not send again.** Check with `FindSourceTransfer` |
| `*RpcTimeoutError` | An RPC call timed out | Retry, or switch RPC |

If `Send` succeeds but `sent.CallbackError` is set, your `OnBroadcast` callback panicked. The transaction has already been sent, so do not send it again.

The recovery procedure and the table of revert codes are in [GUIDE.md §8](../GUIDE.md#8-에러-처리) (Korean).

## Things to keep in mind

- **Quotes are immutable.** The fields of `*Quote` are informational; to change a quote, call `Quote` again. Read the sealed values with `q.ApprovedRequest()` / `q.ApprovedLimits()`.
- **The limits of the first quote are kept.** The spending cap, the delivery floor and the minimum swap output never loosen in the re-quote right before sending.
- **The approve covers only the amount needed** by default.
- The Go SDK always signs with a local key, and before broadcasting it re-checks the signed transaction's chain ID, sender and contents.

## Examples

`examples/` has one example per route. They run as a dry run by default (quote, checks and simulation only).

```bash
go run ./examples/r1-bsc-to-one -network testnet -sender 0xYourAddress
go run ./examples/r5-bsc-to-hyperevm -network testnet -sender 0xYourAddress -recipient-core 0xYourAddress   # R5 + HyperCore deposit
PRIVATE_KEY=... go run ./examples/r1-bsc-to-one -network testnet -send
```

| Flag | Description |
|---|---|
| `-network mainnet\|testnet` | Network (default testnet) |
| `-amount 10` | Amount |
| `-sender` · `-recipient` | Sending / receiving address |
| `-recipient-hub` · `-recipient-core` · `-buffer` | Multihop and HyperCore options |
| `-slippage-bps` | Slippage for X1 and X3 |
| `-tokenIn` · `-tokenOut` · `-fee` · `-to` | X3 options |
| `-send` | Actually send (requires the `PRIVATE_KEY` environment variable) |

Override RPC endpoints with the `RPC_ONE`, `RPC_BSC` and `RPC_HYPEREVM` environment variables.

## Development

```bash
go build ./... && go vet ./... && go test ./...
```

`bridgesdk/networks.json`, `abi_extra.json` and `errors_table.json` are generated. To change network settings, edit the root `networks.json` and run `node ../tools/gen.mjs`.

## License

[MIT](../LICENSE)
