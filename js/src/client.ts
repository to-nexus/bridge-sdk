import { JsonRpcProvider, Network } from 'ethers';
import type { ClientOptions, Ctx } from './ctx.js';
import { NetworkMismatchError, SourceTxFailedError } from './errors.js';
import { buildHandle, type RawReceipt } from './handle.js';
import { inferFromReceipt, type InferOptions } from './infer.js';
import { chainOf, getNetwork } from './networks.js';
import type { ChainKey } from './networks-types.js';
import { preflight } from './preflight.js';
import { assertQuoteBelongs } from './binding.js';
import { openQuote } from './seal.js';
import { buildQuote } from './quote.js';
import { analyzeReplacement, findSourceTransfer } from './recovery.js';
import { defaultSleep, fromHexQty, timedRpc, type RpcLike } from './rpc.js';
import { ensureAllowance, rebroadcast, send, simulate, waitForHandle, type SignerLike } from './send.js';
import { checkCoreBalance, track, trackOnce } from './tracking.js';
import type {
  Checkpoint, FindSourceTransferResult, PreflightReport, Quote, QuoteRequest, ReplacementVerdict, Route, SendOptions, SentTx,
  SubmittedIntent, TrackResult, TransferHandle,
} from './types.js';

export interface HandleFromTxOptions extends InferOptions {
  /** destination block numbers recorded at send time (scan start for tracking), if known */
  scanFrom?: Partial<Record<ChainKey, number>>;
}

export class BridgeClient {
  private readonly ctx: Ctx;
  /** the network this client is bound to (a Quote built by another client/network is rejected by send()) */
  readonly network: ClientOptions['network'];
  private readonly providers = new Map<ChainKey, RpcLike>();
  private readonly verified = new Set<ChainKey>();

  constructor(opts: ClientOptions) {
    this.network = opts.network;
    const net = getNetwork(opts.network, opts.networksData);
    this.ctx = this.makeCtx(opts, net, undefined);
  }

  private makeCtx(opts: ClientOptions, net: ReturnType<typeof getNetwork>, deadlineAt: number | undefined): Ctx {
    const self = this;
    const now = opts.now ?? (() => Date.now());
    const callTimeoutMs = opts.callTimeoutMs ?? 15_000;
    const ctx: Ctx = {
      network: opts.network, net, opts, deadlineAt,
      now,
      sleep: opts.sleep ?? defaultSleep,
      withDeadline: (d: number) => self.makeCtx(opts, net, d),
      rpc(key: ChainKey): RpcLike {
        let p = self.providers.get(key);
        if (!p) {
          p = opts.providers?.[key] ?? new JsonRpcProvider(opts.rpcUrls?.[key] ?? chainOf(net, key).rpc, Network.from(chainOf(net, key).chainId), { staticNetwork: Network.from(chainOf(net, key).chainId), batchMaxCount: 1 });
          self.providers.set(key, p);
        }
        return timedRpc(p, callTimeoutMs, () => (deadlineAt === undefined ? Infinity : deadlineAt - now()));
      },
      async assertChain(key: ChainKey): Promise<void> {
        if (self.verified.has(key)) return;
        const expected = chainOf(net, key).chainId;
        const actual = Number(fromHexQty(await ctx.rpc(key).send('eth_chainId', [])));
        if (actual !== expected) throw new NetworkMismatchError(`${key} RPC`, expected, actual);
        self.verified.add(key);
      },
    };
    return ctx;
  }

  /** the order at every client API entry: (1) openQuote = seal + structural validation (config-free), (2) assertQuoteBelongs = client binding check against THIS client's config, (3) proceed with the restored copy */
  private open(q: Quote): Quote {
    const r = openQuote(q);
    assertQuoteBelongs(this.ctx, r);
    return r;
  }

  /** Live fee reads + route-specific encoding. Refuses routes whose config is null (AddressNotConfiguredError). */
  quote(req: QuoteRequest): Promise<Quote> { return buildQuote(this.ctx, req); }
  /** every API that consumes a Quote first runs openQuote(q) (seal verification, see seal.ts) and then uses only the restored private copy */
  async preflight(q: Quote): Promise<PreflightReport> { return preflight(this.ctx, this.open(q)); }
  /** the exact tx the SDK would send: { to, data, value, chainId, from } */
  buildTx(quote: Quote): { to: string; data: string; value: bigint; chainId: number; from: string } {
    const q = this.open(quote); // a modified quote can never produce modified signable calldata
    return { ...q.tx, chainId: q.sourceChainId, from: q.sender };
  }
  /** eth_call of the exact final tx (read-only). */
  async simulate(q: Quote) { return simulate(this.ctx, this.open(q)); }
  ensureAllowance(signer: SignerLike, q: Quote, opts?: SendOptions) { return ensureAllowance(this.ctx, signer, q, opts); } // opens the seal itself
  /** approve (if needed) -> re-quote under immutable limits -> simulate -> broadcast. Returns as soon as the hash is known. */
  send(signer: SignerLike, q: Quote, opts?: SendOptions): Promise<SentTx> { return send(this.ctx, signer, q, opts); }
  wait(sent: SentTx, o?: { timeoutMs?: number; pollMs?: number }): Promise<TransferHandle> { return waitForHandle(this.ctx, sent, o); }
  track(handle: TransferHandle, o?: { timeoutMs?: number; pollMs?: number; onUpdate?: (r: TrackResult) => void }): Promise<TrackResult> { return track(this.ctx, handle, o); }
  trackOnce(handle: TransferHandle): Promise<TrackResult> { return trackOnce(this.ctx, handle); }

  /**
   * Build a TransferHandle from a source tx hash. The route is decoded from the source receipt's BridgeInitiated (emitted by the configured
   * source bridge / router only) - never from absent events. `opts.route` is optional and verified (RouteMismatchError on contradiction).
   * Several candidate transfers in one receipt => AmbiguousSourceError; select with `opts.index` / `opts.logIndex`.
   */
  async handleFromTx(srcChain: ChainKey | number, txHash: string, opts: HandleFromTxOptions = {}): Promise<TransferHandle> {
    const key: ChainKey = typeof srcChain === 'number' ? ((Object.keys(this.ctx.net.chains) as ChainKey[]).find((k) => this.ctx.net.chains[k].chainId === srcChain) as ChainKey) : srcChain;
    if (!key) throw new RangeError(`chain ${String(srcChain)} is not part of ${this.ctx.network}`);
    await this.ctx.assertChain(key);
    const r = (await this.ctx.rpc(key).send('eth_getTransactionReceipt', [txHash])) as RawReceipt | null;
    if (!r) throw new Error(`no receipt for ${txHash} on ${key} yet (SOURCE_SUBMITTED)`);
    if (BigInt(r.status) !== 1n) throw new SourceTxFailedError(txHash); // SOURCE_FAILED
    const inf = inferFromReceipt(this.ctx, key, r, opts);
    return buildHandle(this.ctx, { route: inf.route, srcKey: key, receipt: r, hypercore: inf.hypercore, x3ToChain: inf.x3ToChain, logIndex: inf.logIndex, scanFrom: opts.scanFrom });
  }

  /** Recovery without a hash: matches candidates against the FULL SubmittedIntent. Never auto-resends. */
  findSourceTransfer(intent: SubmittedIntent, checkpoint: Checkpoint): Promise<FindSourceTransferResult> { return findSourceTransfer(this.ctx, intent, checkpoint); }
  /** Mode L: is a new tx provably safe after a replacement at the same nonce? */
  analyzeReplacement(p: { intent: SubmittedIntent; checkpoint: Checkpoint; original: { hash: string; nonce: number }; replacementHash?: string; maxScanBlocks?: number }): Promise<ReplacementVerdict> { return analyzeReplacement(this.ctx, p); }
  /** Mode L: re-broadcast the SAME signed tx (cannot duplicate). */
  rebroadcast(chain: ChainKey, raw: string): Promise<string> { return rebroadcast(this.ctx, chain, raw); }
  /** optional, best-effort HyperCore balance lookup (public info API) */
  checkCoreBalance(coreRecipient: string, fetchImpl?: typeof fetch) { return checkCoreBalance(this.ctx, coreRecipient, fetchImpl); }
}
