import { Interface, concat, dataLength, dataSlice, getAddress, keccak256, toBeHex } from 'ethers';
import { bridgeAbi, hyperTokenAbi, swapBridgeRouterAbi, v3RouterAbi } from './abi/index.js';

export const FORWARD_SELECTOR = '0x55fd0776'; // bridgeTokenForwarded(uint256,address,uint256,uint256,uint256,bytes)
export const CORE_SELECTOR = '0x9438568d'; // transferToCoreFor(address,uint256)
export const EXACT_INPUT_SINGLE_SELECTOR = '0x414bf389'; // Uniswap V3 SwapRouter (deadline-in-struct variant)

export const bridgeIface = new Interface(bridgeAbi as any);
export const hyperTokenIface = new Interface(hyperTokenAbi as any);
export const swapBridgeRouterIface = new Interface(swapBridgeRouterAbi as any);
export const v3RouterIface = new Interface(v3RouterAbi as any);

export interface BridgeTokenCall {
  toChainId: number | bigint;
  fromToken: string;
  to: string;
  value: bigint;
  networkFee: bigint;
  exFee: bigint;
  extraData: string;
}

export function encodeBridgeToken(i: BridgeTokenCall): string {
  return bridgeIface.encodeFunctionData('bridgeToken', [i.toChainId, i.fromToken, i.to, i.value, i.networkFee, i.exFee, i.extraData]);
}

/** extraData = target (20 B) || calldata */
export function packExtraData(target: string, calldata: string): string {
  return concat([getAddress(target), calldata]);
}

export interface ForwardInput {
  hub: string;
  destChainId: number | bigint;
  toDest: string;
  value2: bigint;
  networkFee2: bigint;
  exFee2Declared: bigint;
  inner: string; // '0x' or the X2 extraData
}

/** hop-1 extraData: hub bridge || bridgeTokenForwarded(destChainId, toDest, v2, nf2, ef2', inner) */
export function encodeForwardExtraData(i: ForwardInput): string {
  const call = bridgeIface.encodeFunctionData('bridgeTokenForwarded', [i.destChainId, i.toDest, i.value2, i.networkFee2, i.exFee2Declared, i.inner]);
  return packExtraData(i.hub, call);
}

export interface CoreInput {
  token: string;
  coreRecipient: string;
  value: bigint;
}
/** X2 innermost extraData: HyperEVM ONE token || transferToCoreFor(coreRecipient, value). Never use transferToCore(uint256). */
export function encodeCoreExtraData(i: CoreInput): string {
  return packExtraData(i.token, hyperTokenIface.encodeFunctionData('transferToCoreFor', [i.coreRecipient, i.value]));
}

export interface X1Input {
  router: string;
  tokenIn: string;
  tokenOut: string;
  fee: number;
  recipient: string;
  deadline: number | bigint;
  amountIn: bigint;
  amountOutMinimum: bigint;
}
/** X1 extraData: V3 SwapRouter || exactInputSingle((tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum, 0)) */
export function encodeX1ExtraData(i: X1Input): string {
  const call = v3RouterIface.encodeFunctionData('exactInputSingle', [[i.tokenIn, i.tokenOut, i.fee, i.recipient, i.deadline, i.amountIn, i.amountOutMinimum, 0]]);
  return packExtraData(i.router, call);
}

export interface SwapBridgeInput {
  tokenIn: string;
  tokenOut: string;
  /** 3-byte pool key of the 1-hop path: the V3 fee tier, or the tick spacing on a Slipstream CL router */
  fee: number;
  amountIn: bigint;
  amountOutMinimum: bigint;
  toChainId: number | bigint;
  recipient: string;
  extraData: string;
  deadline: number | bigint;
  native: boolean;
}
const MAX_POOL_KEY = 0xffffff;

/**
 * The 1-hop SwapBridgeRouter path `tokenIn (20 B) | pool key (3 B) | tokenOut (20 B)`. The pool key is the V3 fee tier, or the tick spacing when
 * the router is deployed against a Slipstream CL swap router: the router passes the 3 bytes through unchanged.
 */
export function x3Path(tokenIn: string, poolKey: number, tokenOut: string): string {
  if (!Number.isInteger(poolKey) || poolKey < 0 || poolKey > MAX_POOL_KEY) throw new RangeError(`pool key ${poolKey} does not fit in 3 bytes`);
  return concat([getAddress(tokenIn), toBeHex(poolKey, 3), getAddress(tokenOut)]);
}

/** Inverse of x3Path; any length other than one hop (43 B) gives undefined. */
export function splitX3Path(path: string): { tokenIn: string; poolKey: number; tokenOut: string } | undefined {
  if (dataLength(path) !== 43) return undefined;
  return { tokenIn: getAddress(dataSlice(path, 0, 20)), poolKey: Number(BigInt(dataSlice(path, 20, 23))), tokenOut: getAddress(dataSlice(path, 23)) };
}

/** The SwapBridgeRouter entry point X3 uses: the path variant (the single-pool variants do not exist on a CL router). */
export const x3Method = (native: boolean): string => (native ? 'swapBridgeExactInputETH' : 'swapBridgeExactInput');

/** X3: SwapBridgeRouter.swapBridgeExactInput(ETH)((path, amountIn, amountOutMinimum, (toChainId, recipient, extraData)), deadline), path = x3Path(tokenIn, fee, tokenOut) */
export function encodeSwapBridge(i: SwapBridgeInput): string {
  const params = [x3Path(i.tokenIn, i.fee, i.tokenOut), i.amountIn, i.amountOutMinimum, [i.toChainId, i.recipient, i.extraData]];
  return swapBridgeRouterIface.encodeFunctionData(x3Method(i.native), [params, i.deadline]);
}

/** A decoded X3 SwapBridgeRouter call (path variant, one hop). */
export interface X3Call {
  tokenIn: string;
  tokenOut: string;
  poolKey: number;
  amountIn: bigint;
  amountOutMinimum: bigint;
  toChainId: bigint;
  recipient: string;
  extraData: string;
  deadline: bigint;
}

/** Decodes `swapBridgeExactInput(ETH)` calldata with a 1-hop path; a string is the reason it is not one. */
export function decodeX3Call(data: string, native: boolean): X3Call | string {
  let d;
  try { d = swapBridgeRouterIface.parseTransaction({ data }); } catch { return 'not a SwapBridgeRouter call'; }
  if (!d || d.name !== x3Method(native)) return 'unexpected swap function';
  const p = d.args[0];
  const path = splitX3Path(p[0]);
  if (!path) return 'path is not a single pool (tokenIn | 3-byte pool key | tokenOut)';
  return { ...path, amountIn: p[1], amountOutMinimum: p[2], toChainId: p[3][0], recipient: p[3][1], extraData: String(p[3][2]), deadline: d.args[1] };
}

export const hashExtraData = (extraData: string): string => keccak256(extraData);
