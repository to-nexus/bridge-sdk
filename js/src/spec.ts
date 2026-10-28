// Route specification + the ONE recipient-resolution function. quote() and the seal validator (route rules) both use these; nothing is re-implemented.
import { getAddress } from 'ethers';
import type { ChainKey } from './networks-types.js';
import { Route } from './types.js';
import type { Quote, QuoteRequest } from './types.js';

export interface Spec {
  kind: Quote['kind'];
  src: ChainKey;
  dest: ChainKey;
  hub?: ChainKey;
  /** source token: 'native' or a token key in the source chain's config */
  srcToken: 'native' | string;
  core: boolean;
  required: string[];
}

export function specFor(route: Route, req: QuoteRequest): Spec {
  switch (route) {
    case Route.BSC_TO_ONE:
      return { kind: 'single', src: 'bsc', dest: 'one', srcToken: 'ONE', core: false, required: ['bsc.bridge', 'bsc.tokens.ONE', 'one.bridge'] };
    case Route.ONE_TO_BSC:
      return { kind: 'single', src: 'one', dest: 'bsc', srcToken: 'native', core: false, required: ['one.bridge', 'bsc.bridge', 'bsc.tokens.ONE'] };
    case Route.ONE_TO_HYPEREVM:
      return { kind: 'single', src: 'one', dest: 'hyperevm', srcToken: 'native', core: false, required: ['one.bridge', 'hyperevm.bridge', 'hyperevm.tokens.ONE'] };
    case Route.ONE_TO_HYPERCORE:
      return { kind: 'single', src: 'one', dest: 'hyperevm', srcToken: 'native', core: true, required: ['one.bridge', 'hyperevm.bridge', 'hyperevm.tokens.ONE'] };
    case Route.HYPEREVM_TO_ONE:
      return { kind: 'single', src: 'hyperevm', dest: 'one', srcToken: 'ONE', core: false, required: ['hyperevm.bridge', 'hyperevm.tokens.ONE', 'one.bridge'] };
    case Route.BSC_TO_ONE_TO_HYPEREVM:
      return { kind: 'multihop', src: 'bsc', hub: 'one', dest: 'hyperevm', srcToken: 'ONE', core: !!req.hypercore, required: ['bsc.bridge', 'bsc.tokens.ONE', 'one.bridge', 'hyperevm.bridge', 'hyperevm.tokens.ONE'] };
    case Route.HYPEREVM_TO_ONE_TO_BSC:
      return { kind: 'multihop', src: 'hyperevm', hub: 'one', dest: 'bsc', srcToken: 'ONE', core: false, required: ['hyperevm.bridge', 'hyperevm.tokens.ONE', 'one.bridge', 'bsc.bridge', 'bsc.tokens.ONE'] };
    case Route.BSC_USDT_TO_ONEUSD:
      return { kind: 'x1', src: 'bsc', hub: 'one', dest: 'one', srcToken: 'USDT', core: false, required: ['bsc.bridge', 'bsc.tokens.USDT', 'one.bridge', 'one.tokens.USDTx', 'one.tokens.ONEUSD', 'one.swap.v3SwapRouter', 'one.swap.quoterV2', 'one.swap.x1Pool'] };
    case Route.SWAP_BRIDGE: {
      const to = req.x3?.toChain;
      if (to !== 'bsc' && to !== 'hyperevm') throw new RangeError('X3 requires request.x3.toChain = "bsc" | "hyperevm"');
      const req_ = ['one.bridge', 'one.swap.swapBridgeRouter', `${to}.bridge`];
      if (req.x3!.tokenIn === 'native') req_.push('one.tokens.WCROSS');
      return { kind: 'x3', src: 'one', hub: 'one', dest: to, srcToken: 'native', core: false, required: req_ };
    }
    default:
      throw new RangeError(`unknown route ${String(route)}`);
  }
}


export function resolveRecipients(req: QuoteRequest) {
  return {
    onHub: getAddress(req.recipients?.onHub ?? req.recipient),
    dest: getAddress(req.recipients?.dest ?? req.recipient),
    core: getAddress(req.recipients?.core ?? req.recipient),
  };
}

