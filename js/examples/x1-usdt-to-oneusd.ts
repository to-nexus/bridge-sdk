import { Route, runExample } from './_common.js';

// X1  BSC USDT -> ONE chain ONEUSD (BridgeExecutor extraCall: Uniswap V3 swap on the hub). Outcome: ONEUSD, or plain USDTx if the swap cannot run (never a loss).
await runExample('X1', (a) => ({ route: Route.BSC_USDT_TO_ONEUSD, amount: a.amount, sender: a.sender, recipient: a.recipient, swap: { slippageBps: a.extra['slippage-bps'] ? Number(a.extra['slippage-bps']) : 50, deadlineSec: 1800 } }));
