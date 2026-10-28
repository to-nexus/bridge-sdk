import { getNetwork } from '../src/index.js';
import { Route, runExample } from './_common.js';

// X3  Swap on the ONE chain (Uniswap V3 via SwapBridgeRouter) and bridge the output in ONE tx. Atomic: any failure reverts everything.
//     Representative combination (assumption A6): ONEUSD -> USDTx -> BSC USDT (addresses come from the built-in config).
//     Override with --tokenIn/--tokenOut/--fee/--to.  --tokenIn native = swap native ONE (through WCROSS) instead of an ERC20.
await runExample('X3', (a) => {
  const oneChain = getNetwork(a.network).chains.one;
  const tokenIn = a.extra.tokenIn ?? oneChain.tokens.ONEUSD?.address;
  const tokenOut = a.extra.tokenOut ?? oneChain.tokens.USDTx?.address;
  if (!tokenIn || !tokenOut) throw new Error(`no default token pair on ${a.network} (ONEUSD is not configured): pass --tokenIn and --tokenOut`);
  return {
    route: Route.SWAP_BRIDGE, amount: a.amount, sender: a.sender, recipient: a.recipient,
    swap: { slippageBps: a.extra['slippage-bps'] ? Number(a.extra['slippage-bps']) : 50, deadlineSec: 1800 },
    x3: { tokenIn, tokenOut, poolFee: a.extra.fee ? Number(a.extra.fee) : 0, toChain: (a.extra.to ?? 'bsc') as 'bsc' | 'hyperevm' },
  };
});
