import { Route, runExample } from './_common.js';

// R2  ONE -> BSC: native ONE becomes the ONE token on BSC (contract symbol CROSS).
await runExample('R2', (a) => ({ route: Route.ONE_TO_BSC, amount: a.amount, sender: a.sender, recipient: a.recipient }));
