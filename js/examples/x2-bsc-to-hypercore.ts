import { Route, runExample } from './_common.js';

// X2 on R5  BSC -> ONE hub -> HyperEVM -> HyperCore with one bridge request (first-time ERC20 approve is separate). --recipient-core = HyperCore account (defaults to --recipient); --recipient-hub / --buffer <ONE> are optional.
import { parseUnits } from '../src/index.js';
await runExample('R5+X2', (a) => ({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: a.amount, sender: a.sender, recipient: a.recipient, recipients: { onHub: a.extra['recipient-hub'], dest: a.recipient, core: a.extra['recipient-core'] }, hypercore: true, hop2FeeBuffer: a.extra.buffer ? parseUnits(a.extra.buffer) : 0n }));
