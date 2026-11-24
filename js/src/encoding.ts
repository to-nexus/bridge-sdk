import { Interface, concat, getAddress, keccak256 } from 'ethers';
import { bridgeAbi, clQuoterV2Abi, clRouterAbi, hyperTokenAbi, swapBridgeRouterAbi, swapBridgeRouterCLAbi, v3RouterAbi } from './abi/index.js';
import type { DexKind } from './networks-types.js';

export const FORWARD_SELECTOR = '0x55fd0776'; // bridgeTokenForwarded(uint256,address,uint256,uint256,uint256,bytes)
export const CORE_SELECTOR = '0x9438568d'; // transferToCoreFor(address,uint256)
export const EXACT_INPUT_SINGLE_SELECTOR = '0x414bf389'; // Uniswap V3 SwapRouter (deadline-in-struct variant)
/** exactInputSingle on the Slipstream CL SwapRouter (int24 tickSpacing in place of uint24 fee) */
export const CL_EXACT_INPUT_SINGLE_SELECTOR = '0xa026383e';
/** quoteExactInputSingle on the Slipstream CL QuoterV2 */
export const CL_QUOTE_EXACT_INPUT_SINGLE_SELECTOR = '0x9e7defe6';

export const bridgeIface = new Interface(bridgeAbi as any);
export const hyperTokenIface = new Interface(hyperTokenAbi as any);
export const swapBridgeRouterIface = new Interface(swapBridgeRouterAbi as any);
/** SwapBridgeRouterCL: SwapBridgeRouter with int24 tickSpacing in the single-pool functions */
export const swapBridgeRouterCLIface = new Interface(swapBridgeRouterCLAbi as any);
export const v3RouterIface = new Interface(v3RouterAbi as any);
export const clRouterIface = new Interface(clRouterAbi as any);
export const clQuoterIface = new Interface(clQuoterV2Abi as any);

export const INT24_MIN = -(2 ** 23);
export const INT24_MAX = 2 ** 23 - 1;

/** exactInputSingle selector of the DEX's swap router */
export const exactInputSelector = (dex: DexKind | undefined): string => (dex === 'cl' ? CL_EXACT_INPUT_SINGLE_SELECTOR : EXACT_INPUT_SINGLE_SELECTOR);

/** RangeError unless `tickSpacing` is an integer in the int24 range of the ABI (the encoder never truncates). */
export function checkInt24(tickSpacing: number): void {
  if (!Number.isInteger(tickSpacing) || tickSpacing < INT24_MIN || tickSpacing > INT24_MAX) {
    throw new RangeError(`tickSpacing ${tickSpacing} is outside the int24 range [${INT24_MIN}, ${INT24_MAX}]`);
  }
}

/** checkInt24 plus the rule for a real pool: its tick spacing is > 0. */
export function checkPoolTickSpacing(tickSpacing: number): void {
  checkInt24(tickSpacing);
  if (tickSpacing <= 0) throw new RangeError(`tickSpacing ${tickSpacing} must be > 0`);
}

function checkDex(dex: DexKind | undefined): void {
  if (dex !== undefined && dex !== 'v3' && dex !== 'cl') throw new RangeError(`unknown dex "${String(dex)}" (expected "v3" or "cl")`);
}

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
  /** swap router that is called (V3 SwapRouter, or the Slipstream CL SwapRouter when dex is 'cl') */
  router: string;
  tokenIn: string;
  tokenOut: string;
  /** V3 pool fee tier (ignored when dex is 'cl') */
  fee: number;
  recipient: string;
  deadline: number | bigint;
  amountIn: bigint;
  amountOutMinimum: bigint;
  /** undefined or 'v3' = Uniswap V3 (selector 0x414bf389); 'cl' = Slipstream CL (selector 0xa026383e) */
  dex?: DexKind;
  /** CL pool tick spacing, int24 range (used only when dex is 'cl') */
  tickSpacing?: number;
}
/**
 * X1 extraData: swap router || exactInputSingle((tokenIn, tokenOut, fee | tickSpacing, recipient, deadline, amountIn, amountOutMinimum, 0)).
 * V3 (default) encodes uint24 fee; dex 'cl' encodes int24 tickSpacing (two's complement for negative values; the int24 range is enforced).
 */
export function encodeX1ExtraData(i: X1Input): string {
  checkDex(i.dex);
  if (i.dex === 'cl') {
    const ts = i.tickSpacing ?? NaN;
    checkInt24(ts);
    const call = clRouterIface.encodeFunctionData('exactInputSingle', [[i.tokenIn, i.tokenOut, ts, i.recipient, i.deadline, i.amountIn, i.amountOutMinimum, 0]]);
    return packExtraData(i.router, call);
  }
  const call = v3RouterIface.encodeFunctionData('exactInputSingle', [[i.tokenIn, i.tokenOut, i.fee, i.recipient, i.deadline, i.amountIn, i.amountOutMinimum, 0]]);
  return packExtraData(i.router, call);
}

export interface SwapBridgeInput {
  tokenIn: string;
  tokenOut: string;
  /** V3 pool fee tier (ignored when dex is 'cl') */
  fee: number;
  amountIn: bigint;
  amountOutMinimum: bigint;
  toChainId: number | bigint;
  recipient: string;
  extraData: string;
  deadline: number | bigint;
  native: boolean;
  /** undefined or 'v3' = SwapBridgeRouter (uint24 fee); 'cl' = SwapBridgeRouterCL (int24 tickSpacing) */
  dex?: DexKind;
  /** CL pool tick spacing, int24 range (used only when dex is 'cl') */
  tickSpacing?: number;
}
/** X3: SwapBridgeRouter(CL).swapBridgeExactInputSingle(ETH)((...), deadline) */
export function encodeSwapBridge(i: SwapBridgeInput): string {
  checkDex(i.dex);
  const fn = i.native ? 'swapBridgeExactInputSingleETH' : 'swapBridgeExactInputSingle';
  if (i.dex === 'cl') {
    const ts = i.tickSpacing ?? NaN;
    checkInt24(ts);
    const params = [i.tokenIn, i.tokenOut, ts, i.amountIn, i.amountOutMinimum, 0, [i.toChainId, i.recipient, i.extraData]];
    return swapBridgeRouterCLIface.encodeFunctionData(fn, [params, i.deadline]);
  }
  const params = [i.tokenIn, i.tokenOut, i.fee, i.amountIn, i.amountOutMinimum, 0, [i.toChainId, i.recipient, i.extraData]];
  return swapBridgeRouterIface.encodeFunctionData(fn, [params, i.deadline]);
}

export const hashExtraData = (extraData: string): string => keccak256(extraData);
