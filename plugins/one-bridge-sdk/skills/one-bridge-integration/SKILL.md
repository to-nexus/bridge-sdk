---
name: one-bridge-integration
description: Integrate the ONE Bridge SDK (@to-nexus/bridge-sdk for TypeScript, github.com/to-nexus/bridge-sdk/go for Go) into an application. Use when writing or reviewing code that bridges ONE or USDT between BSC, the ONE chain and HyperEVM, including multihop transfers (BSC ↔ HyperEVM through the ONE hub), USDT → ONEUSD, HyperCore deposits and swap-and-bridge, or when handling transfer tracking, recovery and bridge errors.
---

# ONE Bridge SDK integration

Write integration code with the ONE Bridge SDK. Every route uses one flow: **quote → send → wait for a handle → track**. Network settings (chain IDs, RPC endpoints, contract addresses) are built into the SDK, so never hard-code addresses or fees.

Chain IDs: ONE 612055 (testnet 612044), BSC 56 (97), HyperEVM 999 (998).

## 1. Install

Ask which language the project uses if it is not obvious from the codebase.

- **TypeScript** (Node ≥ 18, ethers v6, ESM). The package is **not on the npm registry**, so `npm install @to-nexus/bridge-sdk` fails. Build a tarball:
  ```bash
  git clone --branch v0.1.0 https://github.com/to-nexus/bridge-sdk.git
  cd bridge-sdk/js && npm ci && npm run build && npm pack
  # in the project:
  npm install /path/to/to-nexus-bridge-sdk-<version>.tgz ethers
  ```
- **Go** (≥ 1.24): `go get github.com/to-nexus/bridge-sdk/go@v0.1.0`, then `import "github.com/to-nexus/bridge-sdk/go/bridgesdk"`.

## 2. Pick the route

| Goal | Route | TypeScript | Go | Extra request fields |
|---|---|---|---|---|
| ONE token on BSC → native ONE | R1 | `Route.BSC_TO_ONE` | `RouteBSCToOne` | — |
| Native ONE → ONE token on BSC | R2 | `Route.ONE_TO_BSC` | `RouteOneToBSC` | — |
| Native ONE → ONE token on HyperEVM | R3 | `Route.ONE_TO_HYPEREVM` | `RouteOneToHyperEVM` | — |
| ONE token on HyperEVM → native ONE | R4 | `Route.HYPEREVM_TO_ONE` | `RouteHyperEVMToOne` | — |
| BSC → HyperEVM in one request | R5 | `Route.BSC_TO_ONE_TO_HYPEREVM` | `RouteBSCToOneToHyperEVM` | `recipients.onHub`, optional `hop2FeeBuffer`, `hypercore` |
| HyperEVM → BSC in one request | R6 | `Route.HYPEREVM_TO_ONE_TO_BSC` | `RouteHyperEVMToOneToBSC` | `recipients.onHub`, optional `hop2FeeBuffer` |
| USDT on BSC → ONEUSD on the ONE chain | X1 | `Route.BSC_USDT_TO_ONEUSD` | `RouteBSCUSDTToONEUSD` | optional `swap: { slippageBps, deadlineSec }` |
| ONE → HyperCore spot account | X2 | `Route.ONE_TO_HYPERCORE` | `RouteOneToHyperCore` | `recipients.core` |
| Swap on the ONE chain, then bridge | X3 | `Route.SWAP_BRIDGE` | `RouteSwapBridge` | `x3: { tokenIn, tokenOut, poolFee, toChain }`, optional `swap` |

- X2 is **not open on mainnet** yet. The SDK refuses it before signing (`AddressNotConfiguredError` / `PreflightError`). Say so instead of working around it.
- For X3, read token addresses from the built-in settings: `getNetwork('mainnet').chains.one.tokens.ONEUSD.address` (JS), `Client.Network()` (Go). `tokenIn: 'native'` (JS) or `X3Request.NativeIn` (Go) swaps native ONE.
- **`amount` (wei)**: the delivered amount for R1–R4, the **minimum** final delivered amount for R5/R6, the USDT amount sent for X1, the swap input for X3. Use `parseUnits('10')` (JS) / `bridgesdk.ParseUnits("10", 18)` (Go). All bridge tokens have 18 decimals.

## 3. Write the integration

Use this shape. Keep every persistence step and both error branches: they are what make a crash or a lost RPC response recoverable without paying twice.

```ts
import {
  BridgeClient, Route, parseUnits, jsonStringify,
  BroadcastOutcomeUnknownError, PreflightError, RequoteRequiredError,
  type TransferHandle,
} from '@to-nexus/bridge-sdk';
import type { Signer } from 'ethers';
// `db` and `log` stand for the application's own storage and logger.

const client = new BridgeClient({ network: 'testnet' });       // 'mainnet' only when asked

export async function bridge(signer: Signer, amount: string): Promise<TransferHandle> {
  const from = await signer.getAddress();
  const quote = await client.quote({ route: Route.BSC_TO_ONE, amount: parseUnits(amount), sender: from, recipient: from });
  // show quote fees / limits to the user here if the app has a UI

  try {
    const sent = await client.send(signer, quote, {
      onSubmitting: (intent, checkpoint) => db.saveIntent(jsonStringify({ intent, checkpoint })), // before broadcast
      onBroadcast: (hash) => db.saveHash(hash),
    });
    if (sent.callbackError) log.error('onBroadcast failed; the tx WAS sent', sent.hash, sent.callbackError);
    const handle = await sent.wait();
    await db.saveHandle(JSON.stringify(handle));                 // resume with client.track(handle) after a restart
    return handle;
  } catch (e) {
    if (e instanceof BroadcastOutcomeUnknownError) {
      // NEVER send again here. Find out whether the first one went out.
      const { result } = await client.findSourceTransfer(e.intent, e.checkpoint);
      if (result.status === 'FOUND') return result.handle;
      throw e;                                                   // AMBIGUOUS / NOT_FOUND_YET: check again later, do not re-send
    }
    if (e instanceof PreflightError) throw new Error(`blocked: ${e.issues.map((i) => i.code).join(', ')}`); // nothing was signed
    if (e instanceof RequoteRequiredError) throw e;              // fees rose: show e.newQuote and ask the user
    throw e;
  }
}

export async function follow(handle: TransferHandle) {
  const result = await client.track(handle, { timeoutMs: 600_000 });
  // result.state: DELIVERED | DELIVERED_FALLBACK | PENDING_ON_DEST | HUB_FALLBACK | CORE_DEPOSIT_SUBMITTED | ...
  // result.timedOut: still in progress; call track(handle) again later
  return result;
}
```

Go follows the same steps:

```go
c, err := bridgesdk.NewClient(ctx, bridgesdk.Testnet, nil)
q, err := c.Quote(ctx, bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOne, Amount: amount, Sender: from, Recipient: from})
sent, err := c.Send(ctx, bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID)), q, bridgesdk.SendOptions{
	OnSigned:    func(s bridgesdk.SignedInfo, in *bridgesdk.SubmittedIntent) error { return store.SaveSigned(s, in) }, // error aborts the send
	OnBroadcast: func(h common.Hash) { store.SaveHash(h) },
})
var unknown *bridgesdk.BroadcastOutcomeUnknownError
if errors.As(err, &unknown) {
	found, ferr := c.FindSourceTransfer(ctx, unknown.Intent, unknown.Checkpoint) // never send again here
	_ = found; _ = ferr
}
if sent != nil && sent.CallbackError != nil { /* the tx WAS sent */ }
h, err := c.WaitHandle(ctx, sent)  // store h as JSON
res, err := c.Track(ctx, h, bridgesdk.TrackOpts{Timeout: 10 * time.Minute})
```

Handle every `err`; the snippet elides the checks for space. `preflight(quote)` / `Preflight` is optional: `send` runs the same blocking checks itself. Call it to show reasons before asking the user to sign.

## 4. Rules

1. **Never re-send on an unknown outcome**, and never put `send()` inside a generic retry helper. Recovery is `findSourceTransfer`, `handleFromTx(chain, hash)`, or with a local key `rebroadcast(chain, raw)` (same signed tx, so it cannot double-send).
2. **`sent.callbackError` set ⇒ the tx was broadcast.** Never treat it as a failure.
3. **Persist** the hash or signed tx before or at broadcast, and the `TransferHandle` after `wait()`.
4. **Quotes are immutable.** Call `quote()` again to change anything. Edited quotes throw `LimitViolationError`.
5. **Do not compute, pad or hard-code fees or minimums.** They come from the chain and change during operation. For multihop fee margin use `hop2FeeBuffer`.
6. **Report the SDK's state honestly.** `DELIVERED_FALLBACK` means the plain token arrived (USDTx for X1, HyperEVM ONE for X2). `PROCESSED_UNVERIFIED` and `UNKNOWN` are not success. `HUB_FALLBACK` means native ONE is waiting on the hub for the user.
7. **Keys** come only from the environment (`PRIVATE_KEY`) or a wallet. Never write them into code, config or logs.
8. **Testnet and dry runs first.** Mainnet sends only when the user explicitly asks, with a small amount first.
9. **Recipients** must be addresses the user controls on that chain. For R5/R6 set `recipients.onHub` too.
10. **No confirmation waits or reorg handling.** The ONE chain has no reorgs, and the SDK already waits for the source receipt.

## 5. Errors

| Error | Meaning | Action |
|---|---|---|
| `PreflightError` | Blocking check failed (pause, pair, balance, allowance, …); nothing signed. `issues[].code` | Fix, quote again |
| `RequoteRequiredError` | Fees rose past the first quote's limits after the approve | Show `newQuote`, ask the user |
| `QuoteExpiredError` | Quote older than 30 s (default) | Quote again |
| `AddressNotConfiguredError` | Route not open on this network | Other route or network |
| `SimulationRevertError` | Simulation reverted | `decodeBridgeError(data)` gives name, args and a hint |
| `NetworkMismatchError` | RPC, wallet or quote on another chain | Fix the RPC / wallet |
| `RpcTimeoutError` | RPC timed out before broadcast | Safe to retry |
| `SignerRejectedError` | Wallet rejected before signing | Safe to retry |
| `BroadcastOutcomeUnknownError` | Outcome unknown | **Do not send again.** `findSourceTransfer` |

## 6. Verify the integration

- Run against **testnet** first. The SDK's own examples are a reference: `js/examples/<route>.ts` and `go/examples/<route>/main.go` run as a dry run (quote, checks, simulation) unless `--send` / `-send` is given.
- A dry run of your own code: call `quote()` and `preflight()` and print the result; print `buildTx(quote)` instead of sending.
- Override RPCs with `rpcUrls` (JS) / `ClientOptions.RPCURLs` (Go) if the public endpoints are slow.

## References

- Rules and repository map: https://github.com/to-nexus/bridge-sdk/blob/master/AGENTS.md
- TypeScript API: https://github.com/to-nexus/bridge-sdk/blob/master/js/README.md
- Go API: https://github.com/to-nexus/bridge-sdk/blob/master/go/README.md
- States, retry table, when a new transaction is safe, revert reasons: https://github.com/to-nexus/bridge-sdk/blob/master/TRACKING_AND_RECOVERY.md
- Types: https://github.com/to-nexus/bridge-sdk/blob/master/js/src/types.ts
