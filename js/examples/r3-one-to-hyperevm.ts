import { Route, runExample } from './_common.js';

// R3  ONE -> HyperEVM: native ONE becomes the ONE ERC20 on HyperEVM.
await runExample('R3', (a) => ({ route: Route.ONE_TO_HYPEREVM, amount: a.amount, sender: a.sender, recipient: a.recipient }));
