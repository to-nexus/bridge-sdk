import { Route, runExample } from './_common.js';

// R5  BSC -> ONE hub -> HyperEVM with ONE bridge request (first-time ERC20 approve is separate). --recipient-hub / --recipient (dest) / --recipient-core / --buffer <ONE> are optional.
import { parseUnits } from '../src/index.js';
await runExample('R5', (a) => ({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: a.amount, sender: a.sender, recipient: a.recipient, recipients: { onHub: a.extra['recipient-hub'], dest: a.recipient, core: a.extra['recipient-core'] }, hypercore: !!a.extra['recipient-core'], hop2FeeBuffer: a.extra.buffer ? parseUnits(a.extra.buffer) : 0n }));
