# @to-nexus/bridge-sdk

English | [한국어](README_ko.md)

TypeScript SDK for the ONE Bridge. It handles transfers between BSC, the ONE chain and HyperEVM, from the fee quote to confirmed delivery.

- Built on ethers v6, ESM, Node.js 18 or later
- Mainnet and testnet settings built in
- Type definitions included

See the [root README](../README.md) for the supported routes and an overview.

## Installation

The package is not published to the npm registry yet, so build it from source:

```bash
git clone --branch v0.1.0 https://github.com/to-nexus/bridge-sdk.git
cd bridge-sdk/js
npm ci && npm run build && npm pack

# in your project
npm install /path/to/to-nexus-bridge-sdk-<version>.tgz ethers
```

## Usage

```ts
import { BridgeClient, Route, parseUnits } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'mainnet' });
const wallet = new Wallet(process.env.PRIVATE_KEY!);

const quote = await client.quote({
  route: Route.BSC_TO_ONE_TO_HYPEREVM,
  amount: parseUnits('10'),
  sender: wallet.address,
  recipient: wallet.address,
});

const report = await client.preflight(quote);     // optional: see what would block the transfer
if (!report.ok) console.log(report.issues);

const sent = await client.send(wallet, quote, {
  onBroadcast: (hash) => db.save(hash),           // store the hash as soon as it is known
});
const handle = await sent.wait();                 // TransferHandle (JSON)
await db.save(JSON.stringify(handle));

const result = await client.track(handle, { timeoutMs: 600_000 });
console.log(result.state, result.hops);
```

### Client options

```ts
new BridgeClient({
  network: 'mainnet',                 // 'mainnet' | 'testnet'
  rpcUrls: { bsc: 'https://...' },    // override the RPC per chain (one | bsc | hyperevm)
  callTimeoutMs: 15_000,              // timeout for each RPC call
  quoteTtlMs: 30_000,                 // how long a quote stays valid
});
```

### Quote request (`quote`)

| Field | Description |
|---|---|
| `route` | `Route.BSC_TO_ONE` and so on ([supported routes](../README.md#supported-routes)) |
| `amount` | `bigint` in wei. Delivered amount for single-hop routes, minimum final delivered amount for R5/R6, USDT amount for X1, swap input for X3 |
| `sender` | Sending address |
| `recipient` | Receiving address (default for every hop) |
| `recipients` | Per-hop recipients `{ onHub, dest, core }` (optional) |
| `hypercore` | For R5, also deposit into HyperCore (X2) (optional) |
| `swap` | `{ slippageBps, deadlineSec }` for X1 and X3. Defaults to 50 bps / 1800 seconds |
| `x3` | X3 only: `{ tokenIn, tokenOut, poolFee, toChain }` |
| `hop2FeeBuffer` | Extra margin added to the second-hop fee of a multihop route (optional) |
| `limits` | `{ maxTotalSpend, minDelivered }`: your own spending cap and delivery floor (optional) |

### Send options (`send`)

| Option | Description |
|---|---|
| `approveAmount` | `'exact'` (default) or `'max'` |
| `onSubmitting` · `onSigned` · `onBroadcast` | Callbacks: right before signing · right after signing (before broadcast) · right after broadcast |
| `walletTimeoutMs` | How long to wait for a browser wallet to sign. No limit by default |

## API

| Method | Description |
|---|---|
| `quote(req)` | Reads the fees and builds the transaction to send |
| `preflight(quote)` | Checks bridge pauses, pairs, balances, allowances and more; returns `{ ok, issues }` |
| `simulate(quote)` | Read-only simulation |
| `buildTx(quote)` | The transaction to send, `{ to, data, value, chainId, from }` (for signing it yourself) |
| `ensureAllowance(signer, quote)` | Runs only the approve |
| `send(signer, quote, opts?)` | approve → re-quote → checks → simulation → broadcast. Returns a `SentTx` once the hash is known |
| `wait(sent)` / `sent.wait()` | Waits for the source receipt and builds a `TransferHandle` |
| `track(handle, opts?)` | Tracks the transfer until delivery. Can be resumed later with the same handle |
| `trackOnce(handle)` | Reads the current state once |
| `handleFromTx(chain, txHash)` | Rebuilds a `TransferHandle` from a transaction hash |
| `findSourceTransfer(intent, checkpoint)` | Finds the transfer on the source chain when the hash is unknown |
| `analyzeReplacement(...)` · `rebroadcast(chain, raw)` | Decides whether another transaction was mined at the same nonce / re-broadcasts a signed transaction |
| `checkCoreBalance(address)` | Reads a HyperCore spot balance (X2) |
| `decodeBridgeError(data)` | Decodes revert data into the error name, arguments and a hint on what to do |

Utilities: `parseUnits` · `formatUnits` · `jsonStringify` / `jsonParse` (serialisation of objects that contain `bigint`) · `getNetwork(name)` (reads the built-in settings).

## Error handling

Every error extends `BridgeSdkError`.

| Error | When | What to do |
|---|---|---|
| `AddressNotConfiguredError` | The route is not open on this network yet | Use another route or network |
| `PreflightError` | A bridge is paused, the balance or allowance is too low, and so on. See `issues[].code` | Fix the cause, then `quote()` again |
| `RequoteRequiredError` | The re-quote after the approve came back above the original limits because fees rose | `quote()` again |
| `QuoteExpiredError` | The quote is older than its time to live | `quote()` again |
| `NetworkMismatchError` | The RPC or wallet is on a different chain than configured | Check the RPC and the wallet network |
| `SimulationRevertError` | The simulation reverted. Use `decodeBridgeError` to see why | Fix the cause |
| `BroadcastOutcomeUnknownError` | The broadcast request got no answer, so the outcome is unknown | **Do not send again.** Check with `findSourceTransfer(e.intent, e.checkpoint)` |
| `RpcTimeoutError` | An RPC call timed out | Retry, or switch RPC |

If `send()` succeeds but `sent.callbackError` is set, your `onBroadcast` callback failed. The transaction has already been sent, so do not send it again.

The recovery procedure and the table of revert codes are in [GUIDE.md §8](../GUIDE.md#8-에러-처리) (Korean).

## Things to keep in mind

- **Quotes are immutable.** To change one, call `quote()` again. An unchanged copy (`{...quote}`, `structuredClone`) can be used as is.
- **The limits of the first quote are kept.** The spending cap, the delivery floor and the minimum swap output never loosen in the re-quote right before sending.
- **The approve covers only the amount needed** by default. If an existing allowance is too small, it is reset to zero first.
- **Quotes and intents contain `bigint`**, so store them with `jsonStringify` / `jsonParse`. `TransferHandle` is plain JSON.

## Examples

`examples/` has one example per route. They run as a dry run by default (quote, checks and simulation only).

```bash
npm run build && npm run examples:build
node dist-examples/examples/r1-bsc-to-one.js --network testnet --sender 0xYourAddress
PRIVATE_KEY=... node dist-examples/examples/r1-bsc-to-one.js --network testnet --send
```

| Flag | Description |
|---|---|
| `--network mainnet\|testnet` | Network (default testnet) |
| `--amount 10` | Amount |
| `--sender` · `--recipient` | Sending / receiving address |
| `--recipient-hub` · `--recipient-core` · `--buffer` | Multihop and HyperCore options |
| `--slippage-bps` | Slippage for X1 and X3 |
| `--tokenIn` · `--tokenOut` · `--fee` · `--to` | X3 options |
| `--send` | Actually send (requires the `PRIVATE_KEY` environment variable) |

Override RPC endpoints with the `RPC_ONE`, `RPC_BSC` and `RPC_HYPEREVM` environment variables.

## Development

```bash
npm ci
npm run typecheck
npm test
```

`src/networks.generated.ts`, `src/abi/` and `src/errors.generated.ts` are generated. To change network settings, edit the root `networks.json` and run `node ../tools/gen.mjs`.

## License

[MIT](../LICENSE)
