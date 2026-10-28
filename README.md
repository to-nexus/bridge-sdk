# bridge-sdk

English | [한국어](README_ko.md)

The **ONE Bridge SDK** moves ONE and USDT between BSC, the ONE chain and HyperEVM.
It takes you from a fee quote to a confirmed delivery in a few lines, without needing to know how the bridge contracts work.

- **TypeScript SDK** (`js/`, ethers v6): `@to-nexus/bridge-sdk`
- **Go SDK** (`go/`): `github.com/to-nexus/bridge-sdk/go`
- **cast examples** (`cast/`): shell scripts built on foundry `cast`

Network settings (chain IDs, RPC endpoints, contract addresses) are built into the SDKs, so there is nothing to configure.

## Supported routes

| ID | Route | Description | JS `Route` | Go | Mainnet | Testnet |
|---|---|---|---|---|:---:|:---:|
| R1 | BSC → ONE | ONE token on BSC → native ONE on the ONE chain | `BSC_TO_ONE` | `RouteBSCToOne` | ✅ | ✅ |
| R2 | ONE → BSC | Native ONE → ONE token on BSC | `ONE_TO_BSC` | `RouteOneToBSC` | ✅ | ✅ |
| R3 | ONE → HyperEVM | Native ONE → ONE token on HyperEVM | `ONE_TO_HYPEREVM` | `RouteOneToHyperEVM` | ✅ | ✅ |
| R4 | HyperEVM → ONE | ONE token on HyperEVM → native ONE | `HYPEREVM_TO_ONE` | `RouteHyperEVMToOne` | ✅ | ✅ |
| R5 | BSC → ONE → HyperEVM | Two hops in a single request (multihop) | `BSC_TO_ONE_TO_HYPEREVM` | `RouteBSCToOneToHyperEVM` | ✅ | ✅ |
| R6 | HyperEVM → ONE → BSC | Two hops in a single request (multihop) | `HYPEREVM_TO_ONE_TO_BSC` | `RouteHyperEVMToOneToBSC` | ✅ | ✅ |
| X1 | BSC USDT → ONEUSD | Swaps to ONEUSD automatically on arrival on the ONE chain | `BSC_USDT_TO_ONEUSD` | `RouteBSCUSDTToONEUSD` | ✅ | — |
| X2 | ONE → HyperCore | Deposits into a HyperCore spot account after arriving on HyperEVM (option of R3 and R5) | `ONE_TO_HYPERCORE` | `RouteOneToHyperCore` | Coming soon | ✅ |
| X3 | Swap and bridge | Swaps on the ONE chain and bridges the result to BSC or HyperEVM | `SWAP_BRIDGE` | `RouteSwapBridge` | ✅ | ✅¹ |

¹ Testnet has few swap pools with liquidity, so quotes may fail.

The SDK refuses a route that is not open yet before anything is signed (`AddressNotConfiguredError` or `PreflightError`).

### Networks

| Chain | Mainnet chain ID | Testnet chain ID | Gas token |
|---|---|---|---|
| ONE (hub) | 612055 | 612044 | ONE |
| BNB Smart Chain | 56 | 97 | BNB |
| HyperEVM | 999 | 998 | HYPE |

Contract addresses and RPC endpoints are listed in [GUIDE.md §3](GUIDE.md#3-네트워크-정보) (Korean) and in [`networks.json`](networks.json).

## Installation

### TypeScript / JavaScript

Requires Node.js 18 or later. The package is not published to the npm registry yet, so build it from source:

```bash
git clone --branch v0.1.0 https://github.com/to-nexus/bridge-sdk.git
cd bridge-sdk/js
npm ci && npm run build && npm pack     # creates to-nexus-bridge-sdk-<version>.tgz

# in your project
npm install /path/to/to-nexus-bridge-sdk-<version>.tgz ethers
```

### Go

Requires Go 1.24 or later.

```bash
go get github.com/to-nexus/bridge-sdk/go@v0.1.0
```

### cast

Requires foundry's `cast` and `python3`. Nothing to install: run the scripts in `cast/` directly.

### Versioning

Releases are git tags. Each release carries two tags on the same commit:

| Tag | Covers | Used by |
|---|---|---|
| `vX.Y.Z` | The whole repository and the TypeScript SDK | `git clone --branch vX.Y.Z` |
| `go/vX.Y.Z` | The Go SDK (the `go/` module) | `go get github.com/to-nexus/bridge-sdk/go@vX.Y.Z` |

The Go SDK is a module in the repository's `go/` directory, and Go only recognises `go/`-prefixed tags as versions of that module. With `go get`, write the version without the prefix (`@vX.Y.Z`).

## Quick start

Every route follows the same flow: **quote → send → wait for a handle → track**.

### TypeScript

```ts
import { BridgeClient, Route, parseUnits } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'testnet' });
const wallet = new Wallet(process.env.PRIVATE_KEY!);

// 1. Quote: read the fees and build the transaction to send
const quote = await client.quote({
  route: Route.BSC_TO_ONE,
  amount: parseUnits('10'),          // amount to deliver (wei)
  sender: wallet.address,
  recipient: wallet.address,
});

// 2. Send: approve if needed, re-quote, run the safety checks and a simulation, then broadcast
const sent = await client.send(wallet, quote);

// 3. Handle: a JSON document that identifies the transfer. Store it to resume tracking after a restart
const handle = await sent.wait();

// 4. Track: follow the transfer until it arrives at the destination
const result = await client.track(handle);
console.log(result.state);           // e.g. 'DELIVERED'
```

### Go

```go
package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
)

func main() {
	ctx := context.Background()
	key, err := crypto.HexToECDSA(os.Getenv("PRIVATE_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)

	c, err := bridgesdk.NewClient(ctx, bridgesdk.Testnet, nil)
	if err != nil {
		log.Fatal(err)
	}
	amount, _ := bridgesdk.ParseUnits("10", 18)

	q, err := c.Quote(ctx, bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOne, Amount: amount, Sender: from, Recipient: from})
	if err != nil {
		log.Fatal(err)
	}
	sent, err := c.Send(ctx, bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID)), q, bridgesdk.SendOptions{})
	if err != nil {
		log.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil {
		log.Fatal(err)
	}
	res, err := c.Track(ctx, h, bridgesdk.TrackOpts{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.State)
}
```

### cast

```bash
cd cast
FROM=0xYourAddress AMOUNT=10 NETWORK=testnet ./r1.sh                          # quote, balance checks and simulation only (nothing is sent)
FROM=0xYourAddress AMOUNT=10 NETWORK=testnet SEND=1 PRIVATE_KEY=... ./r1.sh   # actually send
```

## Good to know

- **What `amount` means**: the delivered amount for single-hop routes (R1–R4), the **minimum** final delivered amount for multihop routes (R5/R6), the USDT amount to send for X1, and the swap input for X3. The quote computes and adds the fees.
- **Each route has a minimum transfer amount.** It can change during operation, so read it from a quote instead of hard-coding it.
- **Quotes are immutable.** To change the amount or the recipient, call `quote()` again. `send()` refuses a quote object that was modified.
- **`send()` enforces safety checks.** Before and after the approve it checks bridge pauses, pair registration, balances, allowances and more. If anything fails it signs nothing and throws `PreflightError`. Call `preflight()` to see the reasons in advance.
- **Nothing is ever re-sent automatically.** If the outcome of a broadcast is unknown you get `BroadcastOutcomeUnknownError`. Before sending again, use `findSourceTransfer()` to check whether the first transfer went out; this prevents sending the same transfer twice.
- **Store the handle.** `TransferHandle` is plain JSON. You can also rebuild it from a transaction hash with `handleFromTx()`.
- The ONE chain has no reorgs, so there is no confirmation wait. When the source chain is BSC or HyperEVM, only the source transaction receipt is awaited.

## Transfer states

The main states returned by `track()`. The full list and how to handle each state are in [GUIDE.md §7](GUIDE.md#7-이벤트--상태-추적) (Korean).

| State | Meaning |
|---|---|
| `IN_FLIGHT` | Processed on the source chain, waiting for delivery at the destination |
| `DELIVERED` | Delivered at the destination |
| `DELIVERED_FALLBACK` | The requested follow-up action (swap or HyperCore deposit) failed, so the original token was delivered instead |
| `PENDING_ON_DEST` | Held at the destination; paid out automatically or after operator review |
| `CORE_DEPOSIT_SUBMITTED` | X2: the HyperCore deposit request has been submitted |
| `SOURCE_FAILED` | The source transaction failed (no funds moved) |

## Examples

`js/examples/` and `go/examples/` have one example per route. They run as a **dry run** by default (quote, safety checks and simulation only) and send only with `--send` / `-send`.

```bash
# TypeScript
cd js && npm ci && npm run build && npm run examples:build
node dist-examples/examples/r1-bsc-to-one.js --network testnet --sender 0xYourAddress

# Go
cd go && go run ./examples/r1-bsc-to-one -network testnet -sender 0xYourAddress
```

| Example | Route |
|---|---|
| `r1-bsc-to-one` · `r2-one-to-bsc` | R1 · R2 |
| `r3-one-to-hyperevm` · `r4-hyperevm-to-one` | R3 · R4 |
| `r5-bsc-to-hyperevm` · `r6-hyperevm-to-bsc` | R5 · R6 (multihop) |
| `x1-usdt-to-oneusd` · `x2-one-to-hypercore` · `x3-swap-bridge` | X1 · X2 · X3 |

## Documentation

- [js/README.md](js/README.md): TypeScript API
- [go/README.md](go/README.md): Go API
- [GUIDE.md](GUIDE.md) (Korean): integration guide covering each route in detail, how the bridge works, fees, status tracking, error handling and recovery
- [cast/README.md](cast/README.md) (Korean): shell scripts and environment variables
- [SOURCES.md](SOURCES.md) (Korean): where each address and setting comes from, and how to verify it yourself

## Development

```bash
# TypeScript
cd js && npm ci && npm run typecheck && npm test

# Go
cd go && go vet ./... && go test ./...
```

### Releasing

Create the `vX.Y.Z` and `go/vX.Y.Z` tags on the same commit and push both. Keep `version` in `js/package.json` at the same value.

```bash
git tag -a vX.Y.Z -m vX.Y.Z
git tag -a go/vX.Y.Z -m go/vX.Y.Z
git push origin vX.Y.Z go/vX.Y.Z
```

### Network settings

Change network settings only in the root `networks.json`. Then run `node tools/gen.mjs` to propagate them to the SDKs and docs, and `node tools/check-consistency.mjs` to verify. The tools are described in [tools/README.md](tools/README.md) (Korean).

## Security

- Pass private keys only through environment variables. Never put them in code or in a repository.
- On mainnet, try a small amount first.

## License

[MIT](LICENSE)
