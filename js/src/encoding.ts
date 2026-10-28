import { Interface, concat, getAddress, keccak256 } from 'ethers';
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
  fee: number;
  amountIn: bigint;
  amountOutMinimum: bigint;
  toChainId: number | bigint;
  recipient: string;
  extraData: string;
  deadline: number | bigint;
  native: boolean;
}
/** X3: SwapBridgeRouter.swapBridgeExactInputSingle(ETH)((...), deadline) */
export function encodeSwapBridge(i: SwapBridgeInput): string {
  const params = [i.tokenIn, i.tokenOut, i.fee, i.amountIn, i.amountOutMinimum, 0, [i.toChainId, i.recipient, i.extraData]];
  return swapBridgeRouterIface.encodeFunctionData(i.native ? 'swapBridgeExactInputSingleETH' : 'swapBridgeExactInputSingle', [params, i.deadline]);
}

export const hashExtraData = (extraData: string): string => keccak256(extraData);
