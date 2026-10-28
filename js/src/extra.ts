// Full decoding + validation of the extraData the SDK supports (X1 exactInputSingle, X2 transferToCoreFor). Anything else is unsupported.
import { getAddress } from 'ethers';
import { FORWARD_SELECTOR, bridgeIface, CORE_SELECTOR, EXACT_INPUT_SINGLE_SELECTOR, encodeCoreExtraData, encodeX1ExtraData, hyperTokenIface, v3RouterIface } from './encoding.js';
import { BridgeSdkError, UnsupportedRouteError } from './errors.js';
import type { NetworkData } from './networks-types.js';
import type { X1Expect, X2Expect } from './types.js';

const ZERO = '0x0000000000000000000000000000000000000000';
const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

const U24_MAX = 0xffffff;

/**
 * X1, STRUCTURAL and config-free: `target(20 B) || exactInputSingle(params)`. Requires selector exactInputSingle, a decodable call, a well-formed non-zero
 * tokenOut, a valid uint24 fee, sqrtPriceLimitX96 == 0, a non-zero recipient and a canonical encoding (no trailing bytes). Nothing is compared to any configuration.
 */
export function decodeX1Struct(extraData: string): X1Expect {
  const bad = (why: string) => new UnsupportedRouteError(`unsupported X1 extraData: ${why}`);
  if (extraData.length < 2 + 48) throw bad('shorter than target + selector');
  const target = getAddress('0x' + extraData.slice(2, 42));
  if (('0x' + extraData.slice(42, 50)).toLowerCase() !== EXACT_INPUT_SINGLE_SELECTOR) throw bad('selector is not exactInputSingle');
  let p: any;
  try { p = v3RouterIface.decodeFunctionData('exactInputSingle', '0x' + extraData.slice(42))[0]; } catch (e) { throw bad(`undecodable call (${(e as Error).message})`); }
  const [tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum, sqrtLimit] = [p[0], p[1], Number(p[2]), p[3], BigInt(p[4]), BigInt(p[5]), BigInt(p[6]), BigInt(p[7])];
  if (same(tokenOut, ZERO)) throw bad('tokenOut is the zero address');
  if (!Number.isInteger(fee) || fee < 0 || fee > U24_MAX) throw bad(`fee ${fee} is not a valid uint24`);
  if (sqrtLimit !== 0n) throw bad('sqrtPriceLimitX96 must be 0');
  if (same(recipient, ZERO)) throw bad('recipient is the zero address');
  const canonical = encodeX1ExtraData({ router: target, tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum });
  if (canonical.toLowerCase() !== extraData.toLowerCase()) throw bad('non-canonical encoding (trailing or extra bytes)');
  return { router: target, tokenIn: getAddress(tokenIn), tokenOut: getAddress(tokenOut), fee, recipient: getAddress(recipient), deadline: deadline.toString(), amountIn: amountIn.toString(), amountOutMinimum: amountOutMinimum.toString() };
}

/**
 * X1 against a configuration (handle build / inference; the client binding check uses the same comparisons against the CLIENT's config): target is the configured V3 router,
 * tokenIn == hub USDTx, tokenOut == ONEUSD, fee == the configured pool fee, amountIn == the committed value.
 */
export function checkX1(net: NetworkData, extraData: string, committedValue: bigint): X1Expect {
  const oneChain = net.chains.one;
  const bad = (why: string) => new UnsupportedRouteError(`unsupported X1 extraData: ${why}`);
  const router = oneChain.swap?.v3SwapRouter, pool = oneChain.swap?.x1Pool, usdtx = oneChain.tokens.USDTx?.address, oneusd = oneChain.tokens.ONEUSD?.address;
  if (!router || !pool || !usdtx || !oneusd) throw bad('the X1 router / pool / tokens are not configured');
  const x = decodeX1Struct(extraData);
  if (!same(x.router, router)) throw bad(`target ${x.router} is not the configured V3 router`);
  if (!same(x.tokenIn, usdtx)) throw bad(`tokenIn ${x.tokenIn} is not the hub USDTx ${usdtx}`);
  if (!same(x.tokenOut, oneusd)) throw bad(`tokenOut ${x.tokenOut} is not the configured ONEUSD ${oneusd}`);
  if (x.fee !== pool.fee) throw bad(`fee ${x.fee} != configured pool fee ${pool.fee}`);
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

