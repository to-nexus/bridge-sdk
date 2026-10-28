#!/usr/bin/env node
// Consistency gate:
//   1. generated copies match networks.json            (node tools/gen.mjs --check)
//   2. shared vectors are identical in tools/, js/test, go/bridgesdk/testdata
//   3. every non-null address in networks.json has provenance, and every provenance id has a row in SOURCES.md
//   4. every unconfirmed-value (`todo`) entry of networks.json is listed in SOURCES.md
//   5. stray-literal check: every 0x-address / URL in a file comes from networks.json or the allowlist
//      (optional extra patterns: tools/consistency-deny.local.json, a gitignored array of [regex, label])
//   6. all 9 routes appear in GUIDE.md, README.md, the JS SDK, the Go SDK and the cast scripts
// Exit code 1 on any failure.
import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { dirname, relative, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const kit = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const failures = [];
const fail = (m) => failures.push(m);
const read = (p) => readFileSync(resolve(kit, p), 'utf8');
const nets = JSON.parse(read('networks.json'));

// ---- 1 generated copies
try { execFileSync('node', [resolve(kit, 'tools/gen.mjs'), '--check'], { stdio: 'pipe' }); } catch (e) { fail(`generated copies are stale:\n${e.stderr?.toString() || e.message}`); }

// ---- 2 vectors
const v0 = read('tools/vectors/vectors.json');
for (const p of ['js/test/vectors.json', 'go/bridgesdk/testdata/vectors.json']) if (read(p) !== v0) fail(`${p} differs from tools/vectors/vectors.json (run tools/vectors/build-vectors.mjs)`);

// ---- 3 provenance
const sources = read('SOURCES.md');
const addrRe = /0x[0-9a-fA-F]{40}\b/g;
const configAddrs = new Set();
const walk = (o, path) => {
  if (o && typeof o === 'object') for (const [k, v] of Object.entries(o)) walk(v, path ? `${path}.${k}` : k);
  else if (typeof o === 'string' && /^0x[0-9a-fA-F]{40}$/.test(o)) configAddrs.add(o.toLowerCase());
};
walk(Object.fromEntries(Object.entries(nets.networks).map(([n, d]) => [n, Object.fromEntries(Object.entries(d.chains).map(([c, cfg]) => { const { todo, ...rest } = cfg; return [c, rest]; }))])), '');
const NATIVE = '0x0000000000000000000000000000000000000001';
for (const [net, nd] of Object.entries(nets.networks)) {
  for (const [ck, c] of Object.entries(nd.chains)) {
    const need = [];
    for (const f of ['bridge', 'bridgeVerifier', 'bridgeExecutor', 'coreSystemAddress']) if (c[f]) need.push(`${net}.${ck}.${f}`);
    for (const [tk, t] of Object.entries(c.tokens || {})) if (t) need.push(`${net}.${ck}.tokens.${tk}`);
    for (const [sk, sv] of Object.entries(c.swap || {})) if (sv) need.push(`${net}.${ck}.swap.${sk}`);
    for (const f of ['rpc', 'explorer', 'nativeSymbol', 'coreInfoApi']) if (c[f]) need.push(`${net}.${ck}.${f}`);
    for (const p of need) {
      const id = nets.provenance[p];
      if (!id) { fail(`provenance missing for ${p}`); continue; }
      if (!sources.includes(`| ${id} |`)) fail(`SOURCES.md has no row for ${id} (${p})`);
    }
  }
}
for (const id of new Set(Object.values(nets.provenance))) if (!sources.includes(`| ${id} |`)) fail(`SOURCES.md has no row for ${id}`);

// ---- 4 todo list
for (const [net, nd] of Object.entries(nets.networks)) for (const [ck, c] of Object.entries(nd.chains)) for (const [f, t] of Object.entries(c.todo || {})) {
  const marker = /TODO\((PENDING|UNCONFIRMED)\)/.exec(t)?.[0];
  if (marker && !sources.includes(`${net}.${ck}.${f}`)) fail(`SOURCES.md list of unconfirmed values is missing ${net}.${ck}.${f}`);
}

// ---- 5 stray literals
// '_internal': local-only, gitignored
const SKIP_DIRS = new Set(['node_modules', 'dist', 'dist-test', 'dist-examples', '.git', '_internal']);
const SKIP_FILES = new Set(['go.sum', 'package-lock.json', 'vectors.json', 'seal.vectors.json', 'seal_vectors.json', 'errors_table.json', 'abi_extra.json', 'probe-output.txt', 'consistency-deny.local.json']);
const TEXT_EXT = /\.(md|json|ts|mjs|js|go|sh|sol|mod|txt|yml|yaml)$/;
const files = [];
(function scan(dir) {
  for (const n of readdirSync(dir)) {
    if (SKIP_DIRS.has(n)) continue;
    const p = join(dir, n);
    const s = statSync(p);
    if (s.isDirectory()) scan(p);
    else if (TEXT_EXT.test(n) && !SKIP_FILES.has(n)) files.push(p);
  }
})(kit);

const urlAllowHosts = new Set(['hyperliquid.gitbook.io', 'docs.bnbchain.org', 'proxy.golang.org', 'sum.golang.org', 'pkg.go.dev', 'localhost', '127.0.0.1', 'json-schema.org']);
for (const [, nd] of Object.entries(nets.networks)) for (const c of Object.values(nd.chains)) for (const k of ['rpc', 'explorer', 'coreInfoApi']) if (c[k]) urlAllowHosts.add(new URL(c[k]).hostname);
const dummy = (a) => /^0x0{28,}[0-9a-f]{1,12}$/i.test(a) || /^0x(.)\1{39}$/i.test(a) || /^0x0{36}d[eE]a[dD]$/i.test(a) || a.toLowerCase() === '0x000000000000000000000000000000000000dead' || a.toLowerCase() === NATIVE;
// Extra deny patterns kept on a maintainer machine only: tools/consistency-deny.local.json (gitignored) = [["regex", "label"], ...]
const FORBIDDEN = [];
const denyFile = resolve(kit, 'tools/consistency-deny.local.json');
if (existsSync(denyFile)) for (const [re, label] of JSON.parse(readFileSync(denyFile, 'utf8'))) FORBIDDEN.push([new RegExp(re, 'i'), label]);
const SELF = new Set(['tools/check-consistency.mjs']);
for (const f of files) {
  const rel = relative(kit, f).split('\\').join('/');
  const t = readFileSync(f, 'utf8');
  if (!SELF.has(rel)) for (const [re, what] of FORBIDDEN) if (re.test(t)) fail(`${rel}: forbidden ${what} (${re})`);
  if (rel.startsWith('tools/vectors/ForwardMath.s.sol')) continue; // local-EVM harness, numbers only
  for (const m of t.match(addrRe) ?? []) {
    if (configAddrs.has(m.toLowerCase()) || dummy(m)) continue;
    if (rel === 'tools/probe.sh' || rel === 'tools/check-consistency.mjs') continue;
    fail(`${rel}: stray address ${m} (not in networks.json, not an allowlisted placeholder)`);
  }
  for (const m of t.match(/https?:\/\/[^\s"'`)>\]]+/g) ?? []) {
    let host; try { host = new URL(m.replace(/[.,;]+$/, '')).hostname; } catch { continue; }
    if (!urlAllowHosts.has(host) && !/^https?:\/\/(?:github\.com\/to-nexus|www\.npmjs\.com)/.test(m)) fail(`${rel}: stray URL ${m}`);
  }
}

// ---- 6 route coverage
const routes = ['R1', 'R2', 'R3', 'R4', 'R5', 'R6', 'X1', 'X2', 'X3'];
const cover = {
  'GUIDE.md': read('GUIDE.md'), 'README.md': read('README.md'),
  'js/src/types.ts': read('js/src/types.ts'), 'go/bridgesdk/types.go': read('go/bridgesdk/types.go'),
  'cast scripts': readdirSync(resolve(kit, 'cast')).filter((n) => n.endsWith('.sh') || n.endsWith('.md')).map((n) => read(`cast/${n}`)).join('\n'),
};
for (const [where, txt] of Object.entries(cover)) for (const r of routes) if (!new RegExp(`\\b${r}\\b`).test(txt)) fail(`route ${r} is missing from ${where}`);
for (const sdk of ['js/examples', 'go/examples']) {
  const names = readdirSync(resolve(kit, sdk)).join(' ').toLowerCase();
  for (const r of ['r1', 'r2', 'r3', 'r4', 'r5', 'r6', 'x1', 'x2', 'x3']) if (!new RegExp(`(^|[ /-])${r}-`).test(names)) fail(`${sdk} has no example for ${r}`);
}

if (failures.length) { console.error(`consistency gate FAILED (${failures.length}):`); for (const f of failures) console.error(' - ' + f); process.exit(1); }
console.log(`consistency gate passed: ${files.length} files scanned, ${configAddrs.size} configured addresses, ${Object.keys(nets.provenance).length} provenance entries`);
