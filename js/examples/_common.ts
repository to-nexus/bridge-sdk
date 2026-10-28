// Shared helper for the examples. DRY-RUN by default: quote + preflight + buildTx + simulation (all read-only).
// Pass --send (and PRIVATE_KEY in the environment) to really broadcast. NEVER put a key in source code.
import { Wallet, getAddress } from 'ethers';
import { BridgeClient, PreflightError, Route, formatUnits, parseUnits } from '../src/index.js';
import type { NetworkName, QuoteRequest, TrackResult } from '../src/index.js';

export interface Args { network: NetworkName; amount: bigint; sender: string; recipient: string; send: boolean; extra: Record<string, string> }

function parseArgs(): Args {
  const argv = process.argv.slice(2);
  const get = (k: string) => { const i = argv.indexOf(`--${k}`); return i >= 0 ? argv[i + 1] : undefined; };
  const network = (get('network') ?? process.env.NETWORK ?? 'testnet') as NetworkName;
  if (network !== 'mainnet' && network !== 'testnet') throw new Error('--network must be mainnet or testnet');
  const pk = process.env.PRIVATE_KEY;
  const send = argv.includes('--send');
  if (send && !pk) throw new Error('--send needs PRIVATE_KEY in the environment');
  const sender = pk ? new Wallet(pk).address : getAddress(get('sender') ?? process.env.SENDER ?? '0x000000000000000000000000000000000000dEaD');
  const extra: Record<string, string> = {};
  for (const k of ['recipient-hub', 'recipient-core', 'tokenIn', 'tokenOut', 'fee', 'to', 'slippage-bps', 'buffer']) { const v = get(k); if (v) extra[k] = v; }
  return {
    network, send, sender, extra,
    amount: parseUnits(get('amount') ?? process.env.AMOUNT ?? '10'),
    recipient: getAddress(get('recipient') ?? process.env.RECIPIENT ?? sender),
  };
}

export async function runExample(label: string, build: (a: Args) => QuoteRequest): Promise<void> {
  const a = parseArgs();
  // optional RPC overrides, e.g. RPC_BSC=http://127.0.0.1:8545 (anvil fork) - see tools/fork-test
  const rpcUrls = Object.fromEntries((['one', 'bsc', 'hyperevm'] as const).flatMap((k) => { const v = process.env[`RPC_${k.toUpperCase()}`]; return v ? [[k, v]] : []; }));
  const client = new BridgeClient({ network: a.network, rpcUrls });
  const req = build(a);
  console.log(`== ${label} on ${a.network} (${a.send ? 'SEND' : 'dry-run'})  sender=${a.sender} recipient=${a.recipient} amount=${formatUnits(a.amount)}`);

  const q = await client.quote(req);
  console.log(`quote ${q.id}: route ${q.route} source ${q.sourceChainKey}(${q.sourceChainId}) to ${q.tx.to}`);
  console.log(`  total spend ${formatUnits(q.totalSpend)} | msg.value ${formatUnits(q.tx.value)} | fees hop1 nf=${formatUnits(q.fees.hop1.networkFee)} ef=${formatUnits(q.fees.hop1.exFee)}${q.fees.hop2 ? ` | hop2 nf=${formatUnits(q.fees.hop2.networkFee)}` : ''}`);
  console.log(`  limits: maxTotalSpend ${formatUnits(q.limits.maxTotalSpend)} minDelivered ${formatUnits(q.limits.minDelivered)}${q.limits.amountOutMinimum !== undefined ? ` amountOutMinimum ${formatUnits(q.limits.amountOutMinimum)}` : ''}`);
  if (q.approval) console.log(`  approve ${q.approval.token} spender ${q.approval.spender} amount ${formatUnits(q.approval.amount)}`);

  const pf = await client.preflight(q);
  for (const i of pf.issues) console.log(`  preflight ${i.level.toUpperCase()} ${i.code}: ${i.message}`);
  const tx = client.buildTx(q);
  console.log(`  tx: to=${tx.to} value=${tx.value} data=${tx.data.slice(0, 18)}... (${(tx.data.length - 2) / 2} bytes)`);

  // send() runs the blocking subset of preflight and throws PreflightError. A dry-run reports the same refusal: every error-level issue except
  // INSUFFICIENT_BALANCE (the dry-run sender is a dummy address with no funds, which says nothing about the route).
  const refusing = [...new Set(pf.issues.filter((i) => i.level === 'error' && i.code !== 'INSUFFICIENT_BALANCE').map((i) => i.code))];
  const blocked = pf.issues.filter((i) => i.code === 'APPROVE_NEEDED' || i.code === 'INSUFFICIENT_BALANCE').map((i) => (i.code === 'APPROVE_NEEDED' ? 'allowance' : 'balance'));
  let verdict: string;
  if (refusing.length) verdict = `preflight-refused:${refusing.join(',')}`;
  else if (blocked.length) verdict = `simulation-blocked(${[...new Set(blocked)].join(',')})`;
  else {
    const sim = await client.simulate(q);
    verdict = sim.ok ? 'simulation-passed' : `simulation-reverted(${sim.decoded?.name ?? sim.data ?? 'unknown'})`;
  }
  console.log(`RESULT ${label} ${a.network} encoding-verified ${verdict}${pf.ok ? '' : ' preflight-issues'}`);
  if (!a.send) return;

  const signer = new Wallet(process.env.PRIVATE_KEY!); // local-key signer -> Mode L (raw tx persisted before broadcast)
  let sent;
  try {
    sent = await client.send(signer, q, {
      onSubmitting: (intent, cp) => console.log('  SubmittedIntent frozen (persist it!)', { quoteId: intent.quoteId, checkpoint: cp }),
      onSigned: (s) => console.log('  signed, persist before broadcast:', { hash: s.hash, nonce: s.nonce }),
      onBroadcast: (h) => console.log('  broadcast', h),
    });
  } catch (e) {
    if (e instanceof PreflightError) { // blocking preflight (before OR after the approve step): STOP. Nothing was broadcast for the bridge tx.
      for (const i of e.issues) console.log(`  preflight ${i.level.toUpperCase()} ${i.code}: ${i.message}`);
      console.log(`RESULT ${label} ${a.network} preflight-refused:${[...new Set(e.issues.map((i) => i.code))].join(',')} stage=${e.stage ?? 'unknown'}`);
      process.exitCode = 2;
      return;
    }
    throw e;
  }
  if (sent.callbackError) console.log('  WARNING onBroadcast callback failed (the tx IS broadcast, do not resend):', sent.callbackError);
  const handle = await sent.wait();
  console.log('  source mined. TransferHandle (persist):', JSON.stringify(handle));
  if (process.argv.includes('--no-track')) return; // smoke tests: stop after the handle
  const print = (r: TrackResult) => console.log(`  state ${r.state}${r.hops.map((h) => ` [hop${h.hop} ${h.chainKey}:${h.state}${h.amount ? ' ' + h.amount : ''}]`).join('')}`);
  const res = await client.track(handle, { timeoutMs: 15 * 60_000, onUpdate: print });
  console.log(res.terminal ? `FINAL ${res.state}: ${res.note ?? ''}` : `NOT FINAL (timeout) ${res.state}: ${res.note ?? ''}`);
}
export { Route };
