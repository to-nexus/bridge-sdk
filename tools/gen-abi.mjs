#!/usr/bin/env node
// Extracts ABIs from the forge artifacts (out/) of a checkout of github.com/to-nexus/bridge-contracts and generates:
//   js/src/abi/*.ts, js/src/errors.generated.ts   (ABIs + selector -> error table, all custom errors)
//   go/bridgesdk/abi_extra.json                   (Uniswap V3 fragments + forward-lib events, not in binding/go)
//   go/bridgesdk/errors_table.json                (same error table for the Go SDK)
// BRIDGE_CONTRACTS_DIR (required) = that checkout, already built (`forge build`). Selectors are computed with `cast sig`.
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const kit = resolve(here, '..');
const repo = process.env.BRIDGE_CONTRACTS_DIR ? resolve(process.env.BRIDGE_CONTRACTS_DIR) : '';
if (!repo || !existsSync(resolve(repo, 'out'))) {
  console.error('BRIDGE_CONTRACTS_DIR must point to a built checkout of github.com/to-nexus/bridge-contracts (it needs an out/ directory: run `forge build` there).');
  process.exit(2);
}
const art = (file, name) => {
  const p = resolve(repo, 'out', file, `${name}.json`);
  if (!existsSync(p)) throw new Error(`missing artifact ${p} - run \`forge build\` in ${repo}`);
  return JSON.parse(readFileSync(p, 'utf8')).abi;
};
const pick = (abi, { fn = [], ev = [], errors = true }) =>
  abi.filter((x) => (x.type === 'function' && fn.includes(x.name)) || (x.type === 'event' && ev.includes(x.name)) || (x.type === 'error' && errors));
const sigOf = (x) => `${x.name}(${x.inputs.map(typeStr).join(',')})`;
const typeStr = (i) => (i.type.startsWith('tuple') ? `(${i.components.map(typeStr).join(',')})${i.type.slice(5)}` : i.type);
const cast = (...a) => execFileSync('cast', a, { encoding: 'utf8' }).trim();

const bridgeFull = art('CrossBridge.sol', 'CrossBridge');
const forwardLib = art('ForwardLib.sol', 'ForwardLib');
const abis = {
  bridge: [
    ...pick(bridgeFull, {
      fn: ['bridgeToken', 'bridgeTokenForwarded', 'getTokenPair', 'allTokenPairs', 'allChainIDs', 'paused', 'bridgeVerifier', 'bridgeExecutor', 'maxExtraDataLength', 'getNextInitiateIndex', 'getNextFinalizeIndex', 'allPendingIndex', 'getPendingArguments', 'finalizeBridgeBatch', 'releasePending', 'manualReleasePending', 'manualReleasePendingWithRecipient'],
      ev: ['BridgeInitiated', 'BridgeFinalized', 'BridgePending', 'ManualReleased', 'PendingRemoved', 'ExtraCallExecuted'],
    }),
    ...pick(forwardLib, { ev: ['ForwardInitiated', 'ForwardFailed'], errors: false }),
  ],
  verifier: pick(art('BridgeVerifier.sol', 'BridgeVerifier'), { fn: ['calculateFee', 'getTokenConfig', 'denominator'] }),
  executor: pick(art('BridgeExecutor.sol', 'BridgeExecutor'), { fn: ['isWhitelistedTarget', 'isMethodCheckEnabled', 'isWhitelistedMethod'] }),
  hyperToken: pick(art('HyperMintableERC20.sol', 'HyperMintableERC20'), {
    fn: ['isCoreTokenIndexSet', 'coreUnit', 'coreTransferableAmount', 'coreSystemAddress', 'transferToCoreFor', 'symbol', 'decimals', 'balanceOf'],
  }),
  erc20: pick(art('ERC20.sol', 'ERC20'), { fn: ['balanceOf', 'allowance', 'approve', 'decimals', 'symbol', 'transfer'] }),
  swapBridgeRouter: pick(art('SwapBridgeRouter.sol', 'SwapBridgeRouter'), {
    fn: ['swapBridgeExactInputSingle', 'swapBridgeExactInputSingleETH', 'getAmountSwapBridgeOut', 'getExpectedBridgeAmount', 'calculateBridgeFees', 'bridge', 'swapRouter', 'quoter', 'WETH9'],
    ev: ['SwapBridge'],
  }),
  v3Router: pick(art('ISwapRouter.sol', 'ISwapRouter'), { fn: ['exactInputSingle'], errors: false }),
  quoterV2: pick(art('IQuoterV2.sol', 'IQuoterV2'), { fn: ['quoteExactInputSingle'], errors: false }),
  // not shipped as artifacts (the v3-core interfaces are not compiled by bridge-contracts): minimal fragments, verified by selector below
  v3Factory: [{ type: 'function', name: 'getPool', stateMutability: 'view', inputs: [{ name: 'tokenA', type: 'address' }, { name: 'tokenB', type: 'address' }, { name: 'fee', type: 'uint24' }], outputs: [{ name: 'pool', type: 'address' }] }],
  v3Pool: [
    { type: 'function', name: 'liquidity', stateMutability: 'view', inputs: [], outputs: [{ name: '', type: 'uint128' }] },
    { type: 'function', name: 'fee', stateMutability: 'view', inputs: [], outputs: [{ name: '', type: 'uint24' }] },
    { type: 'function', name: 'token0', stateMutability: 'view', inputs: [], outputs: [{ name: '', type: 'address' }] },
    { type: 'function', name: 'token1', stateMutability: 'view', inputs: [], outputs: [{ name: '', type: 'address' }] },
  ],
  v3Periphery: [
    { type: 'function', name: 'factory', stateMutability: 'view', inputs: [], outputs: [{ name: '', type: 'address' }] },
    { type: 'function', name: 'WETH9', stateMutability: 'view', inputs: [], outputs: [{ name: '', type: 'address' }] },
  ],
};

// selector sanity checks: hand-written fragments are verified against their selectors
const expectSel = [
  ['v3Router', 'exactInputSingle', '0x414bf389'],
  ['v3Factory', 'getPool', '0x1698ee82'],
  ['bridge', 'bridgeTokenForwarded', '0x55fd0776'],
  ['hyperToken', 'transferToCoreFor', '0x9438568d'],
  ['v3Periphery', 'factory', '0xc45a0155'],
];
for (const [k, n, sel] of expectSel) {
  const f = abis[k].find((x) => x.type === 'function' && x.name === n);
  const got = cast('sig', sigOf(f));
  if (got !== sel) throw new Error(`selector mismatch ${k}.${n}: ${got} != ${sel}`);
}

// error table: every custom error of every relevant contract
const hints = JSON.parse(readFileSync(resolve(here, 'error-hints.json'), 'utf8'));
const errSources = [
  ['CrossBridge.sol', 'CrossBridge'], ['BridgeExecutor.sol', 'BridgeExecutor'], ['BridgeVerifier.sol', 'BridgeVerifier'],
  ['HyperMintableERC20.sol', 'HyperMintableERC20'], ['SwapBridgeRouter.sol', 'SwapBridgeRouter'], ['ForwardLib.sol', 'ForwardLib'],
  ['ERC20.sol', 'ERC20'], ['BSCBridgeV2.sol', 'BSCBridgeV2'],
];
const table = {};
for (const [f, n] of errSources) {
  for (const e of art(f, n).filter((x) => x.type === 'error')) {
    const sig = sigOf(e);
    const sel = cast('sig', sig);
    table[sel] = { name: e.name, signature: sig, inputs: e.inputs.map((i) => ({ name: i.name, type: typeStr(i) })), hint: hints[e.name] || null };
  }
}
for (const k of Object.keys(hints)) if (!Object.values(table).some((t) => t.name === k)) throw new Error(`error-hints.json has unknown error ${k}`);

// ---- write JS
const header = '// GENERATED by tools/gen-abi.mjs from the bridge-contracts forge artifacts (out/) - DO NOT EDIT.\n';
mkdirSync(resolve(kit, 'js/src/abi'), { recursive: true });
const names = Object.keys(abis);
for (const k of names) writeFileSync(resolve(kit, `js/src/abi/${k}.ts`), `${header}export const ${k}Abi = ${JSON.stringify(abis[k], null, 2)} as const;\n`);
writeFileSync(resolve(kit, 'js/src/abi/index.ts'), header + names.map((k) => `export { ${k}Abi } from './${k}.js';`).join('\n') + '\n');
writeFileSync(resolve(kit, 'js/src/errors.generated.ts'), `${header}export interface ErrorTableEntry { name: string; signature: string; inputs: { name: string; type: string }[]; hint: string | null }\nexport const errorTable: Record<string, ErrorTableEntry> = ${JSON.stringify(table, null, 2)};\n`);

// ---- write Go
const goExtra = { v3Router: abis.v3Router, quoterV2: abis.quoterV2, v3Factory: abis.v3Factory, v3Pool: abis.v3Pool, v3Periphery: abis.v3Periphery, forwardLibEvents: pick(forwardLib, { ev: ['ForwardInitiated', 'ForwardFailed'], errors: false }) };
writeFileSync(resolve(kit, 'go/bridgesdk/abi_extra.json'), JSON.stringify(goExtra, null, 2) + '\n');
writeFileSync(resolve(kit, 'go/bridgesdk/errors_table.json'), JSON.stringify(table, null, 2) + '\n');
console.log(`abis: ${names.join(', ')}; error selectors: ${Object.keys(table).length}`);
