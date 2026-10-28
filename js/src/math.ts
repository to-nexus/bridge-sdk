import type { FeeTriple } from './types.js';

export const DENOMINATOR = 10_000n;

/** floor(x * (10000 - slippageBps) / 10000) */
export function floorBps(x: bigint, slippageBps: number): bigint {
  if (!Number.isInteger(slippageBps) || slippageBps < 0 || slippageBps > 10_000) throw new RangeError(`slippageBps must be an integer in [0, 10000], got ${slippageBps}`);
  return (x * (10_000n - BigInt(slippageBps))) / 10_000n;
}

/**
 * Multihop backward computation:  v1 = v2 + nf2 + (ef2 + buffer).
 * The buffer sits in the declared exFee, never in `value`.
 */
export function multihopAmounts(v2: bigint, hubFee: FeeTriple, buffer: bigint): { ef2Declared: bigint; v1: bigint } {
  if (buffer < 0n) throw new RangeError('hop2FeeBuffer must be >= 0');
  if (v2 < hubFee.minimumValue) throw new RangeError(`v2 ${v2} is below the hub minimumValue ${hubFee.minimumValue}`);
  const ef2Declared = hubFee.exFee + buffer;
  return { ef2Declared, v1: v2 + hubFee.networkFee + ef2Declared };
}

export type ForwardSettlement =
  | { reverted: false; value: bigint; exFee: bigint }
  | { reverted: true; reason: 'BaseBridgeForwardStagedInsufficient'; required: bigint; staged: bigint };

/** Mirror of CrossBridge._resolveForwardAmounts (verified against the Solidity-generated vectors). */
export function resolveForwardAmounts(ctxValue: bigint, quotedValue: bigint, networkFee: bigint, rate: bigint, denom: bigint = DENOMINATOR): ForwardSettlement {
  if (quotedValue + networkFee > ctxValue) return { reverted: true, reason: 'BaseBridgeForwardStagedInsufficient', required: quotedValue + networkFee, staged: ctxValue };
  const avail = ctxValue - networkFee;
  const v0 = (avail * denom) / (denom + rate);
  const value = v0 > quotedValue ? v0 : quotedValue;
  return { reverted: false, value, exFee: avail - value };
}

/** SwapBridgeRouter._getExpectedBridgeAmount */
export function expectedBridgeAmount(total: bigint, networkFee: bigint, exFeeRate: bigint, minimumValue: bigint): { status: 'Success' | 'InsufficientForFee' | 'InsufficientValue'; bridgeValue: bigint; networkFee: bigint; exFee: bigint } {
  if (total <= networkFee) return { status: 'InsufficientForFee', bridgeValue: 0n, networkFee, exFee: 0n };
  const bridgeValue = ((total - networkFee) * DENOMINATOR) / (DENOMINATOR + exFeeRate);
  const exFee = (bridgeValue * exFeeRate) / DENOMINATOR;
  return { status: bridgeValue >= minimumValue ? 'Success' : 'InsufficientValue', bridgeValue, networkFee, exFee };
}

/** HyperMintableERC20.coreTransferableAmount: floor to coreUnit; the remainder goes back to `toDest`. */
export function coreAmounts(amount: bigint, coreUnit: bigint): { coreAmount: bigint; coreAmountEvm: bigint; dust: bigint } {
  if (coreUnit <= 0n) throw new RangeError('coreUnit must be > 0');
  const coreAmount = amount / coreUnit;
  const coreAmountEvm = coreAmount * coreUnit;
  return { coreAmount, coreAmountEvm, dust: amount - coreAmountEvm };
}

/** Decimal string -> wei helper ("1.5" -> 1500000000000000000n). */
export function parseUnits(s: string, decimals = 18): bigint {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(s.trim());
  if (!m) throw new RangeError(`invalid decimal amount "${s}"`);
  const frac = (m[2] ?? '').padEnd(decimals, '0');
  if (frac.length > decimals) throw new RangeError(`too many decimals in "${s}"`);
  return BigInt(m[1]) * 10n ** BigInt(decimals) + BigInt(frac || '0');
}

export function formatUnits(v: bigint, decimals = 18): string {
  const base = 10n ** BigInt(decimals);
  const whole = v / base;
  const frac = (v % base).toString().padStart(decimals, '0').replace(/0+$/, '');
  return frac ? `${whole}.${frac}` : `${whole}`;
}
