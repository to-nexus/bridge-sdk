# Tracking and recovery

This is the English version of §7 (status tracking) and §8 (error handling) of [GUIDE.md](GUIDE.md) (Korean).

How to follow a transfer to its destination, what each state means, how to handle every error, and when it is safe to send a new transaction. The names below are the TypeScript names; the Go SDK uses the same states and error names (`TransferState`, `*PreflightError`, …) with Go-style method names (`Track`, `FindSourceTransfer`, …).

## 1. The transfer handle

`sent.wait()` (Go `WaitHandle`) and `client.handleFromTx(chain, txHash)` (Go `HandleFromTx`) return a `TransferHandle`: plain JSON that identifies the transfer.

- **Store it.** After a restart, `track(handle)` resumes from it. It keeps the scan position and de-duplicates logs by `(txHash, logIndex)`.
- The JS and Go SDKs share the handle format, so a handle created by one can be tracked by the other.
- `handleFromTx` rebuilds the handle from the source receipt alone: from the `BridgeInitiated` event of the configured source bridge (for X3, the configured SwapBridgeRouter). It never infers a route from a missing event.
  - `RouteMismatchError`: you passed a `route` option that differs from the receipt.
  - `UnsupportedRouteError`: the receipt calls a target or selector the SDK does not support.
  - `AmbiguousSourceError`: the receipt holds more than one candidate transfer. The error lists them; pick one with the `index` / `logIndex` options.

Events are correlated only by the indexed `(fromChainID, index)` key. Logs with another key in the same receipt are ignored.

## 2. Transfer states

`track()` returns `{ state, terminal, timedOut?, hops, handle, note? }`. `hops` has one entry per leg (two for R5/R6). `TERMINAL_STATES` lists the states that will not change any more.

| State | Terminal | Meaning | What to do |
|---|:---:|---|---|
| `SOURCE_SUBMITTED` | | No source receipt yet | Keep tracking |
| `SOURCE_FAILED` | ✅ | The source transaction reverted. No funds moved | Fix the cause, quote again |
| `IN_FLIGHT` | | Processed on the source chain, not yet finalized at the destination | Keep tracking |
| `PENDING_ON_DEST` | | Held in the destination's pending queue (a threshold, a paused token, …). `reason` gives the `FinalizeStatus` | Wait: it is released automatically or by an operator |
| `FORWARDED` | | R5/R6: the hub started hop 2. Also the overall state while hop 2 is still in flight | Keep tracking |
| `DELIVERED` | ✅ | Delivered at the destination. For X1, the swap succeeded and `token` / `recipient` come from the decoded swap call | Done |
| `DELIVERED_FALLBACK` | ✅ | The follow-up action (X1 swap or X2 HyperCore deposit) failed, so the plain token was delivered instead: USDTx for X1, the HyperEVM ONE token for X2. No loss | Tell the user which token arrived. Never report it as the swapped token |
| `RELEASED_FROM_PENDING` | ✅ | Released from the pending queue as a plain token (any extra call was dropped). For hop 1 of a multihop route this is native ONE on the hub; there is no hop 2 | The user continues manually if needed |
| `CORE_DEPOSIT_SUBMITTED` | ✅ | X2: the deposit to HyperCore was submitted on HyperEVM. `detail` has `coreRecipient`, `coreAmount`, `returnedToDest`, `finalizedValue`. The HyperCore credit itself is asynchronous | Confirm with `checkCoreBalance(address)` |
| `HUB_FALLBACK` | ✅ | R5/R6: hop 2 failed to start, so native ONE was paid on the hub to the hub recipient (`recipients.onHub`) | The user sends the rest with R2 or R3 |
| `PENDING_REMOVED` | ✅ | An operator removed the pending entry | Contact the bridge operator |
| `PROCESSED_UNVERIFIED` | ✅ | Processed, but the evidence is missing or ambiguous | **Do not report success.** Check manually |
| `UNKNOWN` | ✅ | RPC or logs unavailable after retries, or the call timed out (`reason` starts with `timeout:` and `timedOut` is true). `chain-unverified:` means the RPC's chain ID could not be checked | Retry later or switch RPC. This is not an on-chain failure |

Notes:

- A normal hop takes about 10 seconds to 5 minutes (observed on testnet, not guaranteed). A pending or paused transfer can take longer than any deadline.
- When `track()` times out it returns the **current** state, which is not an on-chain failure. Every RPC call is capped at `min(callTimeoutMs, remaining time)` (default 15 seconds), so `track()` returns within its timeout even if a provider hangs.
- Log queries run in chunks of 2,000 blocks with exponential backoff (1 s doubling, capped at 30 s, 6 attempts) before giving up with `UNKNOWN`.
- `FinalizeStatus` (pending reasons): `TokenPaused`, `VerificationAmountThresholdExceeded`, `PeriodTotalValueThresholdExceeded`, `TransferFailed`, `MintFailed`, `TokenScoreOverflow`, `TokenCurrentVolumeOverflow`, `CrossSupplyLimitExceeded`, `TokenPriceUnavailable`, `InsufficientLiquidity`, `InvalidRecipient`.

## 3. Errors and what to do

**The SDK never re-sends automatically.** Every error below either happened before anything was signed, or tells you exactly what is known.

| Situation | What the SDK does | What to do |
|---|---|---|
| Simulation reverted | `SimulationRevertError` with the decoded error. Nothing sent | Fix the cause, quote again |
| Quote expired | `QuoteExpiredError` | `quote()` again |
| Preflight failed | `PreflightError { issues, stage }`. `stage` is `pre-approve`, `requote` or `post-approve`. Nothing is signed from that point on. An RPC error or timeout during a check is reported as `PREFLIGHT_INCOMPLETE { check, error }` and also blocks: an unverified check never counts as passed | Fix the issues (or switch RPC), quote again |
| Quote was modified | `LimitViolationError { field: 'seal', reason, path }`, before any RPC call. `reason` is `missing`, `unsupported-version`, `unsupported-sdk`, `corrupt`, `invalid-payload` or `modified` | `quote()` again. Never edit a quote object |
| Quote from another network or configuration | `NetworkMismatchError`, before approve or signing | Quote with this client |
| Re-quote after approve loosens your limits | `RequoteRequiredError { oldQuote, newQuote, violated }`. Nothing sent | Review `newQuote` and ask the user to accept it |
| Approve failed | `ApproveFailedError` | Fix the cause |
| Wallet rejected before signing | `SignerRejectedError { safeToRetry }` | Safe to try again: nothing was signed |
| Signer returned a different transaction | `SignerMisbehaviorError { field }` (no replay protection, other chain ID, other sender, changed `to` / `value` / `data` / `nonce` / gas) | Check the signer or wallet |
| Signing error, or `onSigned` / `onSubmitting` threw | Nothing sent | Fix the cause |
| RPC timeout before broadcast | `RpcTimeoutError { method, timeoutMs }` | Safe to retry. An approve may already have been sent, so check the allowance |
| **Broadcast outcome unknown** | `BroadcastOutcomeUnknownError` with `intent` and `checkpoint` (and, for a local key, the signed raw transaction, hash and nonce) | **Do not send again.** Call `findSourceTransfer(intent, checkpoint)`. If you know the hash, call `handleFromTx(chain, hash)`. With a local key you may `rebroadcast(chain, raw)`: it is the same signed transaction, so it cannot double-send |
| **`onBroadcast` callback threw** | `send()` returns the `SentTx` anyway and sets `sent.callbackError` (Go: `SentTx.CallbackError`, from a recovered panic) | The transaction was sent. Handle the storage failure separately and **never retry the send** |
| Browser wallet does not answer | No timeout by default, because a timeout cannot prove the wallet did not send. With `walletTimeoutMs`, expiry raises `BroadcastOutcomeUnknownError` | `findSourceTransfer` |
| Route not open on this network | `AddressNotConfiguredError` | Use another route or network |

### Finding a transfer without its hash

`findSourceTransfer(intent, checkpoint)` scans the source chain and matches candidates against every field of the submitted intent (token, sender, recipient, value and extraData hash must match exactly; the event fee must be ≤ the intent fee, because the bridge records the computed fee). For X3 it searches the router's `SwapBridge` events, because the bridge event's sender is the router.

| `result.status` | Meaning |
|---|---|
| `FOUND` | Exactly one match. Track `result.handle` |
| `AMBIGUOUS` | Two or more identical requests. The SDK does not choose |
| `NOT_FOUND_YET` | Nothing yet. **This is not proof that nothing was sent.** Check again later and check the wallet history |

`nonceInfo` is information only and is never evidence for a re-send.

### When is a new transaction safe?

Only when it is **proven that the original can never be mined**:

1. the wallet rejected **before** signing (`SignerRejectedError`), or
2. with a local key, you know the original hash and nonce, **a different transaction was mined at the same nonce**, and `analyzeReplacement(...)` returns `NEW_TX_ALLOWED`.

| `analyzeReplacement` verdict | Meaning |
|---|---|
| `NEW_TX_ALLOWED` | The replacement failed, or made no transfer at all (for example a zero-value self-transfer that cancelled it). A new transaction is safe |
| `FOUND` | The replacement performed **the same** transfer (a wallet "speed up"). Track `handle`, do not re-send |
| `REPLACED_BY_DIFFERENT_TRANSFER` | The replacement performed a different transfer. Let the user decide |
| `REPLACED_UNVERIFIED` | Transfer evidence that cannot be fully attributed. Manual review, **never** a new transaction |
| `ORIGINAL_MINED` | The original itself was mined. Track it with `handleFromTx(chain, txHash)` |
| `ORIGINAL_STILL_POSSIBLE` | Nothing proves the original is dead (the nonce is not used yet, the replacement has no receipt, …). `reason` says why. If the SDK could not find the transaction at that nonce, pass `replacementHash` from the wallet history. Do not re-send |

A different hash alone, a higher account nonce, missing logs, or a wallet UI showing "cancelled" are **not** proof: a wallet may re-submit later with another nonce. Before reading any evidence, every recovery and tracking call checks that the RPC's `eth_chainId` matches the configuration (`NetworkMismatchError` otherwise).

## 4. Where funds end up when something fails

Funds are never lost. They stop at a predictable place:

| Failure point | Result |
|---|---|
| Hop 1 itself | The transaction reverts. Nothing moves |
| Hub starting hop 2 (R5/R6) | Native ONE is paid on the hub to the hub recipient → `HUB_FALLBACK`. Continue with R2 or R3 |
| Follow-up action (X1 swap, X2 HyperCore deposit) | The plain token is delivered → `DELIVERED_FALLBACK` (X1: USDTx, X2: the HyperEVM ONE token) |
| Pending queue | Released automatically or by an operator as a plain token → `RELEASED_FROM_PENDING` |
| X3 (swap and bridge) | Any failure reverts the whole transaction. Nothing moves |

Common causes of a failed follow-up action: the executor allowlist is missing the target, the HyperCore token link is not set, the amount rounds down to zero, or swap slippage / deadline.

## 5. Revert reasons

`decodeBridgeError(data)` (Go `DecodeBridgeError`) turns revert data into the error name, its arguments and a hint. Unknown selectors are returned as is.

| Selector | Error | What to do |
|---|---|---|
| `0x58e88785` | `BaseBridgeInvalidAmount()` | Below the minimum, or the quote is stale → quote again |
| `0x943892eb` | `BaseBridgeInvalidValue(uint256,uint256)` | `msg.value` mismatch; the first argument is the expected value. Never add a buffer to the fees |
| `0x9df556d1` | `BaseBridgeForwardAmountMismatch(uint256,uint256)` | Multihop amounts do not add up → quote again |
| `0x8399ac8d` | `BaseBridgeForwardStagedInsufficient(uint256,uint256)` | Hub fees rose above the buffer → hop 2 fails and stops on the hub. Use a larger `hop2FeeBuffer` |
| `0xbe75e8c7` | `BaseBridgeForwardNotOrigin()` | This token cannot be forwarded through the hub |
| `0x72a7667e` | `BaseBridgeExtraDataTooLong()` | extraData too long |
| `0x8219abe3` | `BaseBridgeCanNotZeroAddress()` | Recipient address missing |
| `0x70709ede` / `0xdf8496f2` / `0x2a612de6` | `RegistryTokenPaused` / `RegistryChainPaused` / `RegistryNotExistToken` | Token or chain paused / pair not registered |
| `0xd93c0665` | `EnforcedPause()` | Bridge under maintenance |
| `0xfb8f41b2` | `ERC20InsufficientAllowance(...)` | Approve `value + fees` |
| `0xa6bec22b` / `0x428239e8` | `BETargetNotWhitelisted` / `BEMethodNotWhitelisted` | Executor not configured → contact the bridge operator (a plain token is delivered as a fallback) |
