import { Route, runExample } from './_common.js';

// R1  BSC -> ONE: the ONE token on BSC (contract symbol CROSS) becomes native ONE on the ONE chain.
await runExample('R1', (a) => ({ route: Route.BSC_TO_ONE, amount: a.amount, sender: a.sender, recipient: a.recipient }));
