import { Route, runExample } from './_common.js';

// R6  HyperEVM -> ONE hub -> BSC with ONE bridge request (first-time ERC20 approve is separate).
import { parseUnits } from '../src/index.js';
await runExample('R6', (a) => ({ route: Route.HYPEREVM_TO_ONE_TO_BSC, amount: a.amount, sender: a.sender, recipient: a.recipient, recipients: { onHub: a.extra['recipient-hub'], dest: a.recipient }, hop2FeeBuffer: a.extra.buffer ? parseUnits(a.extra.buffer) : 0n }));
