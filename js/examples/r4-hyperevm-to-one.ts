import { Route, runExample } from './_common.js';

// R4  HyperEVM -> ONE: ONE ERC20 on HyperEVM becomes native ONE.
await runExample('R4', (a) => ({ route: Route.HYPEREVM_TO_ONE, amount: a.amount, sender: a.sender, recipient: a.recipient }));
