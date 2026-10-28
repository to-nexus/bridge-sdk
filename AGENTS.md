# AGENTS.md

Instructions for AI coding agents working with the ONE Bridge SDK, either **integrating it into another project** or **changing this repository**. Humans should start with [README.md](README.md).

## What this is

SDKs that move ONE and USDT between BSC, the ONE chain (the hub) and HyperEVM through the ONE Bridge. One flow covers every route: **quote → send → wait for a handle → track**.

| Part | Path | Package |
|---|---|---|
| TypeScript SDK (ethers v6, ESM, Node ≥ 18) | `js/` | `@to-nexus/bridge-sdk` |
| Go SDK (Go ≥ 1.24) | `go/` | `github.com/to-nexus/bridge-sdk/go/bridgesdk` |
| Shell scripts (foundry `cast`) | `cast/` | — |
| Network settings (single source) | `networks.json` | — |

Chain IDs: ONE 612055 (testnet 612044), BSC 56 (97), HyperEVM 999 (998). Contract addresses and RPC endpoints are built into the SDKs; never hard-code them in integration code.

## Installing (integration projects)

- **TypeScript**: the package is **not on the npm registry**. `npm install @to-nexus/bridge-sdk` fails. Build a tarball from a release tag:
  ```bash
  git clone --branch v0.1.0 https://github.com/to-nexus/bridge-sdk.git
  cd bridge-sdk/js && npm ci && npm run build && npm pack
  # then, in the target project:
  npm install /path/to/to-nexus-bridge-sdk-<version>.tgz ethers
  ```
- **Go**: `go get github.com/to-nexus/bridge-sdk/go@v0.1.0`. The tag in git is `go/v0.1.0`, because the module lives in `go/`; `go get` takes the version without the prefix.

## Choosing a route

| Goal | Route | JS `Route.` | Go |
|---|---|---|---|
| ONE token on BSC → native ONE on the ONE chain | R1 | `BSC_TO_ONE` | `RouteBSCToOne` |
| Native ONE → ONE token on BSC | R2 | `ONE_TO_BSC` | `RouteOneToBSC` |
| Native ONE → ONE token on HyperEVM | R3 | `ONE_TO_HYPEREVM` | `RouteOneToHyperEVM` |
| ONE token on HyperEVM → native ONE | R4 | `HYPEREVM_TO_ONE` | `RouteHyperEVMToOne` |
| BSC → HyperEVM in one request (through the hub) | R5 | `BSC_TO_ONE_TO_HYPEREVM` | `RouteBSCToOneToHyperEVM` |
| HyperEVM → BSC in one request (through the hub) | R6 | `HYPEREVM_TO_ONE_TO_BSC` | `RouteHyperEVMToOneToBSC` |
| USDT on BSC → ONEUSD on the ONE chain | X1 | `BSC_USDT_TO_ONEUSD` | `RouteBSCUSDTToONEUSD` |
| ONE → a HyperCore spot account (option of R3, or `hypercore: true` on R5) | X2 | `ONE_TO_HYPERCORE` | `RouteOneToHyperCore` |
| Swap on the ONE chain, then bridge to BSC or HyperEVM | X3 | `SWAP_BRIDGE` | `RouteSwapBridge` |

X2 is not open on mainnet yet; the SDK rejects it before anything is signed. On testnet, X3 quotes can fail because few pools have liquidity.

## Minimal integration

```ts
import { BridgeClient, Route, parseUnits } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'testnet' });
const wallet = new Wallet(process.env.PRIVATE_KEY!);

const quote = await client.quote({ route: Route.BSC_TO_ONE, amount: parseUnits('10'), sender: wallet.address, recipient: wallet.address });
const sent = await client.send(wallet, quote, { onBroadcast: (hash) => store.saveHash(hash) });
if (sent.callbackError) { /* the tx WAS sent: log it, never send again */ }
const handle = await sent.wait();
await store.saveHandle(JSON.stringify(handle));
const result = await client.track(handle, { timeoutMs: 600_000 });
```

The Go equivalent is `NewClient` → `Quote` → `Send` → `WaitHandle` → `Track`. See the Go quick start in [README.md](README.md#quick-start) and [go/README.md](go/README.md).

## Rules that must hold in generated code

These protect real funds. Do not trade them for brevity.

1. **Never re-send on an unknown outcome.** On `BroadcastOutcomeUnknownError`, call `findSourceTransfer(e.intent, e.checkpoint)`, or `handleFromTx(chain, hash)` if the hash is known. Do not wrap `send()` in a generic retry loop or a retry helper.
2. **Check `sent.callbackError`** (Go `sent.CallbackError`). If it is set, the transaction was already broadcast. Do not treat it as a failure, and do not send again.
3. **Persist the handle and the hash.** Store the hash in `onBroadcast` (or the signed raw tx in `onSigned`), and store `TransferHandle` as JSON after `wait()`. A restart must resume with `track(handle)`, not with a new send.
4. **Quotes are immutable.** To change the amount, recipient or route, call `quote()` again. Editing a quote object makes `send()` throw `LimitViolationError`.
5. **Do not compute or pad fees.** The quote reads them from the chain. Adding a margin makes native sends revert (`BaseBridgeInvalidValue`). For R5/R6 use `hop2FeeBuffer` instead.
6. **Know what `amount` means**: the delivered amount for R1–R4, the **minimum** final delivered amount for R5/R6, the USDT amount sent for X1, the swap input for X3. Amounts are wei (`bigint` / `*big.Int`); use `parseUnits`. All bridge tokens have 18 decimals.
7. **Read minimums from a quote.** Minimum amounts and fees change during operation; never hard-code them.
8. **Report the state the SDK returns.** `DELIVERED_FALLBACK` means the plain token arrived instead of the swapped or deposited one. `PROCESSED_UNVERIFIED` and `UNKNOWN` are not success.
9. **Private keys come from the environment** (`PRIVATE_KEY`) or a wallet. Never put them in code, config files or logs.
10. **Default to testnet** and dry runs while developing. Send on mainnet only when the user asks, and with a small amount first.
11. **Recipients must be addresses the user controls on that chain.** For R5/R6 set `recipients.onHub` (receives native ONE on the hub if hop 2 fails) as well as the destination recipient.
12. **No confirmation waits.** The ONE chain has no reorgs; the SDK already waits for the source receipt where needed. Do not add block-confirmation logic.

## Error handling at a glance

| Error | Meaning | Action |
|---|---|---|
| `PreflightError` | A blocking check failed (pause, pair, balance, allowance, …). Nothing signed. See `issues[].code` | Fix the cause, quote again |
| `RequoteRequiredError` | Fees rose after the approve beyond the first quote's limits | Show `newQuote`, ask the user |
| `QuoteExpiredError` | Quote older than its time to live (30 s by default) | Quote again |
| `AddressNotConfiguredError` | Route not open on this network | Use another route or network |
| `SimulationRevertError` | Simulation reverted | `decodeBridgeError(data)` for the reason |
| `NetworkMismatchError` | RPC, wallet or quote on another chain | Check the RPC and wallet |
| `RpcTimeoutError` | RPC call timed out before broadcast | Safe to retry |
| `BroadcastOutcomeUnknownError` | Outcome unknown | **Do not send again.** `findSourceTransfer` |

The full state list, the retry table and the rules for when a new transaction is safe are in [TRACKING_AND_RECOVERY.md](TRACKING_AND_RECOVERY.md).

## Where to look

| Need | File |
|---|---|
| TypeScript API, options, errors | [js/README.md](js/README.md), types in `js/src/types.ts` and `js/src/errors.ts` |
| Go API | [go/README.md](go/README.md), `go doc github.com/to-nexus/bridge-sdk/go/bridgesdk` |
| A working program for each route | `js/examples/<route>.ts`, `go/examples/<route>/main.go` (dry run by default) |
| States, recovery, revert reasons | [TRACKING_AND_RECOVERY.md](TRACKING_AND_RECOVERY.md) |
| Contract calls, fees, multihop math (Korean) | [GUIDE.md](GUIDE.md) §4–§6 |
| Addresses, RPC endpoints, chain IDs | [`networks.json`](networks.json); provenance in [SOURCES.md](SOURCES.md) (Korean) |

## Working on this repository

- **Generated files: do not edit by hand.** `js/src/networks.generated.ts`, `js/src/errors.generated.ts`, `js/src/abi/`, `go/bridgesdk/networks.json`, `go/bridgesdk/abi_extra.json`, `go/bridgesdk/errors_table.json`, and the network tables in `GUIDE.md`. Change the root `networks.json` (or `tools/error-hints.json`), then run `node tools/gen.mjs`.
- **Every address in `networks.json` needs a provenance entry** with a matching row in `SOURCES.md`. Unconfirmed values stay `null` with a `TODO(PENDING)` or `TODO(UNCONFIRMED)` note in `todo`; never fill one with a guess.
- **Checks before you finish:**
  ```bash
  node tools/check-consistency.mjs              # generated copies, provenance, stray addresses/URLs, route coverage
  cd js && npm ci && npm run typecheck && npm test
  cd go && go vet ./... && go test ./...
  ```
  The consistency gate fails on any 0x address or URL that is not in `networks.json` or its allowlist, so do not paste addresses into docs or code.
- Seal test vectors are regenerated only when the quote format changes on purpose: `UPDATE_SEAL_VECTORS=1 npm test` (js) and `UPDATE_SEAL_VECTORS=1 go test ./bridgesdk -run TestSealVectorsGenerator` (go).
- Behaviour must stay the same in both SDKs: `TransferHandle` JSON, states and error names are shared. A change to one SDK usually needs the same change in the other.
- Releases: tag `vX.Y.Z` and `go/vX.Y.Z` on the same commit, and keep `js/package.json` `version` in step.
- Public repository: no internal hostnames, ticket IDs, personal data or history notes in code or docs. Code comments are in English.
