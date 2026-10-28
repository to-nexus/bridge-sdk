import { Route, runExample } from './_common.js';

// R1  BSC -> ONE (CROSS): CROSS ERC20 on BSC becomes native ONE on the CROSS chain.
await runExample('R1', (a) => ({ route: Route.BSC_TO_ONE, amount: a.amount, sender: a.sender, recipient: a.recipient }));
