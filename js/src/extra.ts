// Full decoding + validation of the extraData the SDK supports (X1 exactInputSingle on V3 or Slipstream CL, X2 transferToCoreFor). Anything else is unsupported.
import { getAddress } from 'ethers';
import {
  FORWARD_SELECTOR, bridgeIface, CORE_SELECTOR, CL_EXACT_INPUT_SINGLE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR, checkPoolTickSpacing, clRouterIface, encodeCoreExtraData,
  encodeX1ExtraData, exactInputSelector, hyperTokenIface, v3RouterIface,
} from './encoding.js';
import { BridgeSdkError, UnsupportedRouteError } from './errors.js';
import { swapView } from './networks.js';
import type { DexKind, NetworkData } from './networks-types.js';
import type { X1Expect, X2Expect } from './types.js';

const ZERO = '0x0000000000000000000000000000000000000000';
const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

const U24_MAX = 0xffffff;

/**
 * X1, STRUCTURAL and config-free: `target(20 B) || exactInputSingle(params)` of either DEX (by selector: V3 0x414bf389 or Slipstream CL 0xa026383e).
 * Requires a decodable call, a well-formed non-zero tokenOut, a valid uint24 fee (V3) or an int24 tickSpacing > 0 (CL), sqrtPriceLimitX96 == 0, a non-zero
 * recipient and a canonical encoding (no trailing bytes). Nothing is compared to any configuration (which DEX is allowed is checked by checkX1).
 */
export function decodeX1Struct(extraData: string): X1Expect {
  const bad = (why: string) => new UnsupportedRouteError(`unsupported X1 extraData: ${why}`);
  if (extraData.length < 2 + 48) throw bad('shorter than target + selector');
  const target = getAddress('0x' + extraData.slice(2, 42));
  const selector = ('0x' + extraData.slice(42, 50)).toLowerCase();
  const dex: DexKind | null = selector === EXACT_INPUT_SINGLE_SELECTOR ? 'v3' : selector === CL_EXACT_INPUT_SINGLE_SELECTOR ? 'cl' : null;
  if (!dex) throw bad(`selector is not exactInputSingle (V3 ${EXACT_INPUT_SINGLE_SELECTOR} or Slipstream CL ${CL_EXACT_INPUT_SINGLE_SELECTOR})`);
  let p: any;
  try { p = (dex === 'cl' ? clRouterIface : v3RouterIface).decodeFunctionData('exactInputSingle', '0x' + extraData.slice(42))[0]; } catch (e) { throw bad(`undecodable call (${(e as Error).message})`); }
  const [tokenIn, tokenOut, poolKey, recipient, deadline, amountIn, amountOutMinimum, sqrtLimit] = [p[0], p[1], Number(p[2]), p[3], BigInt(p[4]), BigInt(p[5]), BigInt(p[6]), BigInt(p[7])];
  if (same(tokenOut, ZERO)) throw bad('tokenOut is the zero address');
  if (dex === 'v3' && (!Number.isInteger(poolKey) || poolKey < 0 || poolKey > U24_MAX)) throw bad(`fee ${poolKey} is not a valid uint24`);
  if (dex === 'cl') {
    try { checkPoolTickSpacing(poolKey); } catch { throw bad(`tickSpacing ${poolKey} is not a valid pool tick spacing (int24, > 0)`); }
  }
  if (sqrtLimit !== 0n) throw bad('sqrtPriceLimitX96 must be 0');
  if (same(recipient, ZERO)) throw bad('recipient is the zero address');
  const canonical = dex === 'cl'
    ? encodeX1ExtraData({ router: target, tokenIn, tokenOut, fee: 0, recipient, deadline, amountIn, amountOutMinimum, dex: 'cl', tickSpacing: poolKey })
    : encodeX1ExtraData({ router: target, tokenIn, tokenOut, fee: poolKey, recipient, deadline, amountIn, amountOutMinimum });
  if (canonical.toLowerCase() !== extraData.toLowerCase()) throw bad('non-canonical encoding (trailing or extra bytes)');
  const x: X1Expect = { router: target, tokenIn: getAddress(tokenIn), tokenOut: getAddress(tokenOut), fee: dex === 'cl' ? 0 : poolKey, recipient: getAddress(recipient), deadline: deadline.toString(), amountIn: amountIn.toString(), amountOutMinimum: amountOutMinimum.toString() };
  if (dex === 'cl') { x.dex = 'cl'; x.tickSpacing = poolKey; } // V3 handles keep their JSON shape
  return x;
}

const dexLabel = (d: DexKind) => (d === 'cl' ? 'Slipstream CL' : 'V3');

/**
 * X1 against a configuration (handle build / inference; the client binding check uses the same comparisons against the CLIENT's config): the selector is the
 * configured DEX's exactInputSingle, target is the configured swap router, tokenIn == hub USDTx, tokenOut == ONEUSD, fee (V3) / tickSpacing (CL) == the
 * configured pool's, amountIn == the committed value.
 */
export function checkX1(net: NetworkData, extraData: string, committedValue: bigint): X1Expect {
  const oneChain = net.chains.one;
  const bad = (why: string) => new UnsupportedRouteError(`unsupported X1 extraData: ${why}`);
  let sv;
  try { sv = swapView(net); } catch (e) { throw bad((e as Error).message); }
  const usdtx = oneChain.tokens.USDTx?.address, oneusd = oneChain.tokens.ONEUSD?.address;
  if (!sv.router || !sv.pool || !usdtx || !oneusd) throw bad('the X1 router / pool / tokens are not configured');
  const x = decodeX1Struct(extraData);
  const xdex: DexKind = x.dex ?? 'v3';
  if (xdex !== sv.dex) throw bad(`exactInputSingle selector ${exactInputSelector(xdex)} is the ${dexLabel(xdex)} variant, but the configured dex is "${sv.dex}" (expected selector ${exactInputSelector(sv.dex)})`);
  if (!same(x.router, sv.router)) throw bad(`target ${x.router} is not the configured ${dexLabel(sv.dex)} router`);
  if (!same(x.tokenIn, usdtx)) throw bad(`tokenIn ${x.tokenIn} is not the hub USDTx ${usdtx}`);
  if (!same(x.tokenOut, oneusd)) throw bad(`tokenOut ${x.tokenOut} is not the configured ONEUSD ${oneusd}`);
  if (sv.dex === 'v3' && x.fee !== sv.fee) throw bad(`fee ${x.fee} != configured pool fee ${sv.fee}`);
  if (sv.dex === 'cl' && x.tickSpacing !== sv.tickSpacing) throw bad(`tickSpacing ${x.tickSpacing} != configured pool tickSpacing ${sv.tickSpacing}`);
  if (BigInt(x.amountIn) !== committedValue) throw bad(`amountIn ${x.amountIn} != the committed value ${committedValue}`);
  return x;
}

/**
 * X2, STRUCTURAL and config-free: `target(20 B) || transferToCoreFor(coreRecipient, amount)`: selector, decodable, non-zero coreRecipient, canonical encoding.
 */
export function decodeX2Struct(extraData: string): X2Expect {
  const bad = (why: string) => new UnsupportedRouteError(`unsupported X2 extraData: ${why}`);
  if (extraData.length < 2 + 48) throw bad('shorter than target + selector');
  const target = getAddress('0x' + extraData.slice(2, 42));
  if (('0x' + extraData.slice(42, 50)).toLowerCase() !== CORE_SELECTOR) throw bad('selector is not transferToCoreFor(address,uint256)');
  let d: any;
  try { d = hyperTokenIface.decodeFunctionData('transferToCoreFor', '0x' + extraData.slice(42)); } catch (e) { throw bad(`undecodable call (${(e as Error).message})`); }
  const coreRecipient = d[0] as string;
  const amount = BigInt(d[1]);
  if (same(coreRecipient, ZERO)) throw bad('coreRecipient is the zero address');
  if (encodeCoreExtraData({ token: target, coreRecipient, value: amount }).toLowerCase() !== extraData.toLowerCase()) throw bad('non-canonical encoding (trailing or extra bytes)');
  return { token: target, coreRecipient: getAddress(coreRecipient), encodedAmount: amount.toString() };
}

/**
 * X2 against a configuration (the client binding check uses the same comparison against the CLIENT's config): target is the configured HyperEVM token and
 * `amount == the committed amount` (source BridgeInitiated.value for R3, the forward payload's value for R5).
 */
export function checkX2(net: NetworkData, extraData: string, committedAmount: bigint): X2Expect {
  const bad = (why: string) => new UnsupportedRouteError(`unsupported X2 extraData: ${why}`);
  const token = net.chains.hyperevm.tokens.ONE?.address;
  if (!token) throw bad('the HyperEVM ONE token is not configured');
  const x = decodeX2Struct(extraData);
  if (!same(x.token, token)) throw bad(`target ${x.token} is not the configured HyperEVM ONE token`);
  if (BigInt(x.encodedAmount) !== committedAmount) throw bad(`encoded amount ${x.encodedAmount} != the committed amount ${committedAmount}`);
  return x;
}

export function decodeForward(extraData: string): { toChainID: bigint; to: string; value: bigint; networkFee: bigint; exFee: bigint; inner: string } {
  const sel = '0x' + extraData.slice(42, 50);
  if (sel.toLowerCase() !== FORWARD_SELECTOR) throw new BridgeSdkError(`extraData is not a bridgeTokenForwarded call (selector ${sel})`);
  const d = bridgeIface.decodeFunctionData('bridgeTokenForwarded', '0x' + extraData.slice(42));
  return { toChainID: d[0], to: d[1], value: d[2], networkFee: d[3], exFee: d[4], inner: d[5] };
}

