// Structural validation (config-free). No networks configuration is read here (neither the built-in nor a client's): config equality is the client binding check (binding.ts).
// Route-specific required-field rules for a sealed quote payload (second pass after the generic schema). The generic schema in seal.ts checks types and
// formats; THIS pass checks that every field the SDK dereferences for the quote's route is present and consistent with the sealed calldata. It runs on the
// quote restored from the (already type-checked) payload and reports the ACTUAL wire path of the offending field (QuoteWireV1: `limits.amountOutMinimum`,
// `deadlineSec`, `v2`, `fees.hop2`, `fees.hop2ExFeeDeclared`, `verifiers.hub`, `details.hubChainKey`, `details.hop2Kind`, `tx.data`, `details.extraData`, ...).
// Recipients are resolved with the SAME function quote() uses (spec.ts resolveRecipients); extraData is decoded with the SAME decoders as handle building (extra.ts).
import { bridgeIface, hashExtraData, swapBridgeRouterIface } from './encoding.js';
import { decodeForward, decodeX1Struct, decodeX2Struct } from './extra.js';
import { resolveRecipients, specFor } from './spec.js';
import { Route } from './types.js';
import type { Quote } from './types.js';

export class RuleError extends Error {
  constructor(public path: string, msg: string) { super(`${path}: ${msg}`); }
}
const bad = (path: string, msg: string): never => { throw new RuleError(path, msg); };
const eq = (a: string | undefined | null, b: string | undefined | null): boolean => !!a && !!b && a.toLowerCase() === b.toLowerCase();

export function checkRouteRules(q: Quote): void {
  const req = q.request;

  // ms values are positive safe integers
  if (!(Number.isSafeInteger(q.ttlMs) && q.ttlMs > 0)) bad('ttlMs', 'must be a positive safe integer (ms)');
  if (!(Number.isSafeInteger(q.createdAt) && q.createdAt > 0)) bad('createdAtMs', 'must be a positive safe integer (ms)');
  if (req.quoteTtlMs !== undefined && !(Number.isSafeInteger(req.quoteTtlMs) && req.quoteTtlMs > 0)) bad('request.quoteTtlMs', 'must be a positive safe integer (ms)');

  // all routes
  let spec;
  try { spec = specFor(req.route, req); } catch (e) { return bad('request.x3', (e as Error).message); }
  if (q.route !== req.route) bad('route', 'differs from request.route');
  if (q.kind !== spec.kind) bad('kind', `route ${q.route} is a ${spec.kind} quote`);
  if (q.sourceChainKey !== spec.src) bad('sourceChainKey', `route ${q.route} starts on ${spec.src}`);
  if (q.details.destChainKey !== spec.dest) bad('details.destChainKey', `route ${q.route} ends on ${spec.dest}`);
  if (!eq(q.sender, req.sender)) bad('sender', 'differs from request.sender');
  if (!eq(q.tx.to, q.sourceContract)) bad('tx.to', 'must equal sourceContract');
  const rec = resolveRecipients(req);
  const nativeSrc = req.route === Route.SWAP_BRIDGE ? req.x3!.tokenIn === 'native' : spec.srcToken === 'native';
  if (nativeSrc && q.approval !== null) bad('approval', 'a native source has no approval');
  if (!nativeSrc && q.approval === null) bad('approval', 'an ERC20 source needs an approval');

  if (spec.kind === 'x3') return x3Rules(q, req, rec);

  // bridge routes: the sealed calldata must be a bridgeToken call consistent with the quote
  let args: any;
  try { args = bridgeIface.decodeFunctionData('bridgeToken', q.tx.data); } catch { return bad('tx.data', 'not a bridgeToken call'); }
  const hop1To = spec.kind === 'multihop' ? rec.onHub : rec.dest;
  if (q.match.kind !== 'bridge') bad('match.kind', 'a bridge route needs a bridge match');
  const m = q.match as Extract<Quote['match'], { kind: 'bridge' }>;
  if (BigInt(args[0]) !== BigInt(q.details.hop1ToChainId)) bad('tx.data', 'toChainId differs from details.hop1ToChainId');
  if (!eq(args[1], m.fromToken)) bad('tx.data', 'fromToken differs from match.fromToken');
  if (!eq(args[2], hop1To)) bad('tx.data', 'recipient differs from the resolved hop-1 recipient');
  if (!eq(m.recipient, hop1To)) bad('match.recipient', 'differs from the resolved hop-1 recipient');
  if (args[3] !== q.value || m.value !== q.value) bad('value', 'differs from the calldata / match value');
  if (args[4] !== q.fees.hop1.networkFee || args[5] !== q.fees.hop1.exFee || m.networkFee !== args[4] || m.exFee !== args[5]) bad('fees.hop1', 'differs from the calldata / match fees');
  if (String(args[6]).toLowerCase() !== q.details.extraData) bad('details.extraData', 'differs from the calldata extraData');
  if (!eq(m.extraDataHash, hashExtraData(q.details.extraData))) bad('match.extraDataHash', 'differs from keccak256(extraData)');
  const ed = q.details.extraData;

  if (spec.kind === 'single') {
    if (!spec.core) { if (ed !== '0x') bad('details.extraData', 'a plain route has no extraData'); return; }
    if (q.details.hop1Kind !== 'x2') bad('details.hop1Kind', 'X2 needs hop1Kind x2');
    let x2;
    try { x2 = decodeX2Struct(ed); } catch (e) { return bad('details.extraData', (e as Error).message); }
    if (BigInt(x2.encodedAmount) !== q.value) bad('details.extraData', 'encoded amount differs from the committed value');
    if (!eq(x2.coreRecipient, rec.core)) bad('details.extraData', 'coreRecipient differs from the resolved core recipient');
    return;
  }

  if (spec.kind === 'multihop') {
    if (q.v2 === undefined) bad('v2', 'required for a multihop quote');
    if (q.fees.hop2 === undefined) bad('fees.hop2', 'required for a multihop quote');
    if (q.fees.hop2ExFeeDeclared === undefined) bad('fees.hop2ExFeeDeclared', 'required for a multihop quote');
    if (q.verifiers.hub === undefined) bad('verifiers.hub', 'required for a multihop quote');
    if (q.details.hubChainKey !== 'one') bad('details.hubChainKey', 'a multihop quote goes through the hub chain');
    if (q.details.hop2Kind === undefined) bad('details.hop2Kind', 'required for a multihop quote');
    if (q.details.hop2Kind !== (spec.core ? 'x2' : 'plain')) bad('details.hop2Kind', `must be ${spec.core ? 'x2' : 'plain'}`);
    let f;
    try {
      f = decodeForward(ed); // the target must be the client's hub bridge (checked by the client binding check)
    } catch (e) { return bad('details.extraData', (e as Error).message); }
    if (f.toChainID !== BigInt(q.details.destChainId)) bad('details.extraData', 'hop-2 destination chain differs from details.destChainId');
    if (!eq(f.to, rec.dest)) bad('details.extraData', 'hop-2 recipient differs from the resolved destination recipient');
    if (f.value !== q.v2!) bad('details.extraData', 'hop-2 value differs from v2');
    if (f.networkFee !== q.fees.hop2!.networkFee) bad('details.extraData', 'hop-2 networkFee differs from fees.hop2');
    if (f.exFee !== q.fees.hop2ExFeeDeclared!) bad('details.extraData', 'hop-2 exFee differs from fees.hop2ExFeeDeclared');
    if (!spec.core) { if (f.inner !== '0x') bad('details.extraData', 'inner extraData must be empty without the core option'); return; }
    let x2;
    try { x2 = decodeX2Struct(f.inner); } catch (e) { return bad('details.extraData', (e as Error).message); }
    if (BigInt(x2.encodedAmount) !== q.v2!) bad('details.extraData', 'encoded amount differs from v2');
    if (!eq(x2.coreRecipient, rec.core)) bad('details.extraData', 'coreRecipient differs from the resolved core recipient');
    return;
  }

  // x1
  if (q.limits.amountOutMinimum === undefined) bad('limits.amountOutMinimum', 'required for an X1 quote (0 is a valid floor)');
  if (q.deadline === undefined) bad('deadlineSec', 'required for an X1 quote');
  if (BigInt(q.deadline!) * 1000n <= BigInt(q.createdAt)) bad('deadlineSec', 'must be after createdAtMs / 1000');
  if (q.details.hubChainKey !== 'one') bad('details.hubChainKey', 'an X1 quote goes through the hub chain');
  if (q.details.hop1Kind !== 'x1') bad('details.hop1Kind', 'X1 needs hop1Kind x1');
  let x1;
  try { x1 = decodeX1Struct(ed); } catch (e) { return bad('details.extraData', (e as Error).message); }
  if (!eq(x1.tokenIn, q.details.hop1ToToken)) bad('details.extraData', 'exactInputSingle tokenIn differs from details.hop1ToToken');
  if (BigInt(x1.amountIn) !== q.value) bad('details.extraData', 'exactInputSingle amountIn differs from the committed value');
  if (!eq(x1.recipient, rec.dest)) bad('details.extraData', 'exactInputSingle recipient differs from the resolved destination recipient');
  if (BigInt(x1.amountOutMinimum) !== q.limits.amountOutMinimum) bad('details.extraData', 'exactInputSingle amountOutMinimum differs from limits.amountOutMinimum');
  if (BigInt(x1.deadline) !== BigInt(q.deadline!)) bad('details.extraData', 'exactInputSingle deadline differs from deadlineSec');
}

function x3Rules(q: Quote, req: Quote['request'], rec: ReturnType<typeof resolveRecipients>): void {
  if (q.match.kind !== 'swap') bad('match.kind', 'an X3 quote needs a swap match');
  if (q.limits.amountOutMinimum === undefined) bad('limits.amountOutMinimum', 'required for an X3 quote (0 is a valid floor)');
  if (q.limits.minBridgeValue === undefined) bad('limits.minBridgeValue', 'required for an X3 quote (0 is a valid floor)');
  if (q.deadline === undefined) bad('deadlineSec', 'required for an X3 quote');
  const x3 = req.x3!;
  const native = x3.tokenIn === 'native';
  let d: any;
  try { d = swapBridgeRouterIface.parseTransaction({ data: q.tx.data, value: q.tx.value }); } catch { return bad('tx.data', 'not a SwapBridgeRouter call'); }
  if (!d || d.name !== (native ? 'swapBridgeExactInputSingleETH' : 'swapBridgeExactInputSingle')) return bad('tx.data', 'unexpected swap function');
  const p = d.args[0];
  if (!native && !eq(p[0], x3.tokenIn)) bad('tx.data', 'tokenIn differs from request.x3.tokenIn'); // native: tokenIn is the client's WCROSS (client binding check)
  if (!eq(p[1], x3.tokenOut)) bad('tx.data', 'tokenOut differs from request.x3.tokenOut');
  if (Number(p[2]) !== x3.poolFee) bad('tx.data', 'fee differs from request.x3.poolFee');
  if (p[3] !== req.amount) bad('tx.data', 'amountIn differs from request.amount');
  if (p[4] !== q.limits.amountOutMinimum) bad('tx.data', 'amountOutMinimum differs from limits.amountOutMinimum');
  if (BigInt(p[6][0]) !== BigInt(q.details.destChainId)) bad('tx.data', 'toChainID differs from details.destChainId');
  if (!eq(p[6][1], rec.dest)) bad('tx.data', 'recipient differs from the resolved destination recipient');
  if (String(p[6][2]) !== '0x') bad('tx.data', 'X3 supports extraData = 0x only');
  if (BigInt(d.args[1]) !== BigInt(q.deadline!)) bad('tx.data', 'deadline differs from deadlineSec');
  if (q.tx.value !== (native ? req.amount : 0n)) bad('tx.value', 'must equal amountIn for a native input, 0 otherwise');
  const m = q.match as Extract<Quote['match'], { kind: 'swap' }>;
  if (m.amountIn !== req.amount) bad('match.amountIn', 'differs from request.amount');
  if (!eq(m.recipient, rec.dest)) bad('match.recipient', 'differs from the resolved destination recipient');
  if (!eq(m.extraDataHash, hashExtraData('0x'))) bad('match.extraDataHash', 'differs from keccak256(0x)');
}
