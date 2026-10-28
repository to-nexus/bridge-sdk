import { Route, runExample } from './_common.js';

// R2  ONE -> BSC: native ONE becomes the CROSS ERC20 on BSC.
await runExample('R2', (a) => ({ route: Route.ONE_TO_BSC, amount: a.amount, sender: a.sender, recipient: a.recipient }));
