import { Route, runExample } from './_common.js';

// X2  ONE -> HyperEVM with a HyperCore deposit (transferToCoreFor). --recipient-core = HyperCore account.
await runExample('X2', (a) => ({ route: Route.ONE_TO_HYPERCORE, amount: a.amount, sender: a.sender, recipient: a.recipient, recipients: { core: a.extra['recipient-core'] } }));
