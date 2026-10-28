// Minimal in-repo fake JSON-RPC chain + fake signers (no extra test dependencies).
import { Interface, Transaction, Wallet, getAddress, keccak256, randomBytes, hexlify, zeroPadValue, toBeHex } from 'ethers';
import type { RpcLike } from '../src/rpc.js';

export interface TxCtx { from: string; to: string; value: bigint; chain: FakeChain; hash: string; data: string }
export interface FakeLog { address: string; topics: string[]; data: string; blockNumber: number; logIndex: number; transactionHash: string }
export interface FakeReceipt { status: '0x1' | '0x0'; blockNumber: string; transactionHash: string; logs: any[] }
export type LogInput = { address: string; topics: string[]; data: string };

export interface FakeContract {
  iface: Interface;
  /** read-only handlers (eth_call): return the decoded result (single value, or array for multiple outputs) */
  call?: Record<string, (args: any[], c: { from?: string; value: bigint }) => any>;
  /** state-changing handlers (mined txs): may return logs; throw to produce a failed receipt */
  tx?: Record<string, (args: any[], c: TxCtx) => LogInput[] | void>;
}

const revertErr = (data: string, msg = 'execution reverted') => Object.assign(new Error(msg), { data, code: 3 });

export class FakeChain implements RpcLike {
  block = 1000;
  nonces = new Map<string, number>();
  contracts = new Map<string, FakeContract>();
  logs: FakeLog[] = [];
  receipts = new Map<string, FakeReceipt>();
  txs = new Map<string, any>();
  blocks = new Map<number, any[]>();
  balances = new Map<string, bigint>();
  gasPrice = 1_000_000_000n;
  calls: { method: string; params: any[] }[] = [];
  /** how eth_sendRawTransaction behaves: 'mine' (default), 'pending' (accept, never mine), 'throw-no-mine', 'throw-after-mine' */
  sendMode: 'mine' | 'pending' | 'throw-no-mine' | 'throw-after-mine' | 'hang' = 'mine';
  /** RPC methods whose promise never settles (a hung provider) */
  hang = new Set<string>();
  failLogs = 0; // number of eth_getLogs calls to fail
  failAll: string | null = null; // fail every call with this message
  /** methods that fail with this message (e.g. a provider whose eth_chainId errors while receipts / logs / blocks answer normally) */
  failMethods = new Map<string, string>();
  unavailableLogs = false;
  rawSent: string[] = [];
  constructor(public chainId: number) {}

  deploy(address: string, c: FakeContract) { this.contracts.set(address.toLowerCase(), c); }

  async send(method: string, params: any[]): Promise<any> {
    this.calls.push({ method, params });
    if (this.hang.has(method)) return new Promise(() => {});
    if (this.failMethods.has(method)) throw new Error(this.failMethods.get(method)!);
    if (this.failAll) throw new Error(this.failAll);
    switch (method) {
      case 'eth_chainId': return toBeHex(this.chainId);
      case 'eth_blockNumber': return toBeHex(this.block);
      case 'eth_gasPrice': return toBeHex(this.gasPrice);
      case 'eth_getBalance': return toBeHex(this.balances.get(params[0].toLowerCase()) ?? 10n ** 24n);
      case 'eth_getTransactionCount': return toBeHex(this.nonces.get(params[0].toLowerCase()) ?? 0);
      case 'eth_estimateGas': { this.dryRun(params[0]); return toBeHex(200_000); }
      case 'eth_call': return this.dryRun(params[0]);
      case 'eth_sendRawTransaction': return this.sendRaw(params[0]);
      case 'eth_getTransactionReceipt': return this.receipts.get(String(params[0]).toLowerCase()) ?? null;
      case 'eth_getTransactionByHash': return this.txs.get(String(params[0]).toLowerCase()) ?? null;
      case 'eth_getBlockByNumber': { const n = Number(BigInt(params[0])); return { number: toBeHex(n), transactions: this.blocks.get(n) ?? [] }; }
      case 'eth_getLogs': return this.getLogs(params[0]);
      default: throw new Error(`FakeChain: unsupported method ${method}`);
    }
  }

  private lookup(to: string, data: string) {
    const c = this.contracts.get(to.toLowerCase());
    if (!c) return null;
    const frag = c.iface.getFunction(data.slice(0, 10));
    if (!frag) throw revertErr('0x');
    return { c, frag, args: c.iface.decodeFunctionData(frag, data) as any[] };
  }

  private dryRun(req: { from?: string; to: string; data: string; value?: string }): string {
    const l = this.lookup(req.to, req.data);
    if (!l) return '0x';
    const value = req.value ? BigInt(req.value) : 0n;
    const h = l.c.call?.[l.frag.name];
    if (h) {
      const r = h([...l.args], { from: req.from, value });
      if (l.frag.outputs.length === 0) return '0x';
      return l.c.iface.encodeFunctionResult(l.frag, l.frag.outputs.length === 1 ? [r] : r);
    }
    return '0x';
  }

  /** mine a transaction immediately */
  applyTx(tx: { from: string; to: string | null; data: string; value: bigint; nonce?: number; hash?: string }): { hash: string; receipt: FakeReceipt } {
    const from = getAddress(tx.from);
    const nonce = tx.nonce ?? (this.nonces.get(from.toLowerCase()) ?? 0);
    const hash = (tx.hash ?? hexlify(randomBytes(32))).toLowerCase();
    this.block += 1;
    const logs: FakeLog[] = [];
    let status: '0x1' | '0x0' = '0x1';
    try {
      const l = tx.to ? this.lookup(tx.to, tx.data) : null;
      const handler = l?.c.tx?.[l.frag.name];
      if (l && handler) {
        const out = handler([...l.args], { from, to: tx.to!, value: tx.value, chain: this, hash, data: tx.data });
        for (const o of out ?? []) logs.push({ ...o, blockNumber: this.block, logIndex: logs.length, transactionHash: hash });
      }
    } catch { status = '0x0'; logs.length = 0; }
    this.nonces.set(from.toLowerCase(), nonce + 1);
    for (const l of logs) this.logs.push(l);
    const receipt: FakeReceipt = {
      status, blockNumber: toBeHex(this.block), transactionHash: hash,
      logs: logs.map((l) => ({ ...l, blockNumber: toBeHex(l.blockNumber), logIndex: toBeHex(l.logIndex) })),
    };
    this.receipts.set(hash, receipt);
    const txRec = { hash, from, to: tx.to, input: tx.data, nonce: toBeHex(nonce), value: toBeHex(tx.value), blockNumber: toBeHex(this.block), chainId: toBeHex(this.chainId) };
    this.txs.set(hash, txRec);
    this.blocks.set(this.block, [...(this.blocks.get(this.block) ?? []), txRec]);
    return { hash, receipt };
  }

  private sendRaw(raw: string): string {
    this.rawSent.push(raw);
    const t = Transaction.from(raw);
    const hash = t.hash!.toLowerCase();
    if (this.sendMode === 'hang') return new Promise(() => {}) as any; // the request left, no answer ever comes (tx not mined)
    if (this.sendMode === 'throw-no-mine') throw new Error('connection reset by peer');
    if (this.sendMode === 'pending') return hash;
    const cur = this.nonces.get(t.from!.toLowerCase()) ?? 0;
    if (t.nonce < cur) throw new Error('nonce too low');
    this.applyTx({ from: t.from!, to: t.to, data: t.data, value: t.value, nonce: t.nonce, hash });
    if (this.sendMode === 'throw-after-mine') throw new Error('timeout while waiting for the node response');
    return hash;
  }

  private getLogs(f: { address?: string; topics?: (string | string[] | null)[]; fromBlock: string; toBlock: string }): any[] {
    if (this.unavailableLogs) throw new Error('missing trie node / logs pruned');
    if (this.failLogs > 0) { this.failLogs--; throw new Error('rate limited'); }
    const from = Number(BigInt(f.fromBlock)), to = Number(BigInt(f.toBlock));
    return this.logs
      .filter((l) => l.blockNumber >= from && l.blockNumber <= to)
      .filter((l) => !f.address || l.address.toLowerCase() === f.address.toLowerCase())
      .filter((l) => (f.topics ?? []).every((t, i) => t == null || (Array.isArray(t) ? t.map((x) => x.toLowerCase()).includes(l.topics[i]?.toLowerCase() ?? '') : l.topics[i]?.toLowerCase() === t.toLowerCase())))
      .map((l) => ({ ...l, blockNumber: toBeHex(l.blockNumber), logIndex: toBeHex(l.logIndex) }));
  }

  /** mine a tx of `from` at `nonce` (carries this chain's chainId) whose receipt holds exactly these logs: replacement-evidence fixtures */
  mineSignedLogs(p: { from: string; nonce: number; to: string; logs: LogInput[] }): string {
    const hash = hexlify(randomBytes(32)).toLowerCase();
    this.block += 1;
    const out: FakeLog[] = p.logs.map((l, i) => ({ ...l, blockNumber: this.block, logIndex: i, transactionHash: hash }));
    this.logs.push(...out);
    this.receipts.set(hash, { status: '0x1', blockNumber: toBeHex(this.block), transactionHash: hash, logs: out.map((l) => ({ ...l, blockNumber: toBeHex(l.blockNumber), logIndex: toBeHex(l.logIndex) })) });
    const txRec = { hash, from: getAddress(p.from), to: p.to, input: '0x', nonce: toBeHex(p.nonce), value: '0x0', blockNumber: toBeHex(this.block), chainId: toBeHex(this.chainId) };
    this.txs.set(hash, txRec);
    this.blocks.set(this.block, [...(this.blocks.get(this.block) ?? []), txRec]);
    this.nonces.set(getAddress(p.from).toLowerCase(), p.nonce + 1);
    return hash;
  }

  /** append a raw log into a (new) mined tx, for tracking fixtures */
  mineLogs(from: string, to: string, data: string, logs: LogInput[]): string {
    const hash = hexlify(randomBytes(32)).toLowerCase();
    this.block += 1;
    const out: FakeLog[] = logs.map((l, i) => ({ ...l, blockNumber: this.block, logIndex: i, transactionHash: hash }));
    this.logs.push(...out);
    const receipt: FakeReceipt = { status: '0x1', blockNumber: toBeHex(this.block), transactionHash: hash, logs: out.map((l) => ({ ...l, blockNumber: toBeHex(l.blockNumber), logIndex: toBeHex(l.logIndex) })) };
    this.receipts.set(hash, receipt);
    const txRec = { hash, from, to, input: data, nonce: '0x0', value: '0x0', blockNumber: toBeHex(this.block), chainId: toBeHex(this.chainId) };
    this.txs.set(hash, txRec);
    return hash;
  }
}

// ------------------------------------------------------------------------------------------------ signers
/** Mode L signer: a real ethers Wallet (local key, no provider). */
export const localWallet = () => Wallet.createRandom();

/** Mode W signer: sign+send together like an injected wallet. */
export class FakeWalletSigner {
  sent: { to: string; data: string; value: bigint }[] = [];
  behavior: 'ok' | 'reject' | 'throw-no-mine' | 'throw-after-mine' | 'hang' = 'ok';
  constructor(public address: string, public chain: FakeChain) {}
  provider = { getNetwork: async () => ({ chainId: BigInt(this.chain.chainId) }) };
  async getAddress() { return this.address; }
  async sendTransaction(tx: { to: string; data: string; value: bigint }) {
    this.sent.push(tx);
    if (this.behavior === 'reject') throw Object.assign(new Error('User rejected the request.'), { code: 4001 });
    if (this.behavior === 'hang') return new Promise<{ hash: string }>(() => {});
    if (this.behavior === 'throw-no-mine') throw new Error('wallet crashed');
    const r = this.chain.applyTx({ from: this.address, to: tx.to, data: tx.data, value: tx.value });
    if (this.behavior === 'throw-after-mine') throw new Error('lost the wallet response');
    return { hash: r.hash };
  }
}

export const topicOf = (iface: Interface, name: string) => iface.getEvent(name)!.topicHash;
export const pad = (v: string | bigint | number) => (typeof v === 'string' && v.startsWith('0x') ? zeroPadValue(v, 32) : zeroPadValue(toBeHex(v), 32));
export const hashOf = keccak256;
