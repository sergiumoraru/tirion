#!/usr/bin/env node
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { isAbsolute, join } from 'node:path';

const repo = process.argv[2];
if (!repo) {
  throw new Error('Usage: node scripts/estate-fixture-check.mjs /path/to/clean/catalog-api');
}
const api = (process.env.BASE_URL || 'http://localhost:8080').replace(/\/$/, '');
const workspaceId = process.env.WORKSPACE_ID || 'default-main';
const client = process.env.HTTP_CLIENT || 'curl';
const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim();
assert.equal(git('status', '--porcelain'), '', 'fixture repository must be clean');
const baseSha = git('rev-parse', 'HEAD');
const path = 'src/CatalogController.java';
const source = git('show', `HEAD:${path}`) + '\n';
const expected = readFileSync(new URL('../testdata/estate-fixtures/catalog-api/src/CatalogController.java', import.meta.url), 'utf8');
assert.equal(source.trimEnd(), expected.trimEnd(), 'index the current fixture version first');

// Token lookup order: TIRION_API_TOKEN, TIRION_API_TOKEN_FILE, then the local
// file written by `tirion serve` (TIRION_HOME, else ~/.tirion; ~/.codebase-intel
// on older installs). The local file is read only for a loopback API.
function resolveToken() {
  if (process.env.TIRION_API_TOKEN !== undefined) return process.env.TIRION_API_TOKEN.trim();
  if (process.env.TIRION_API_TOKEN_FILE) {
    if (!isAbsolute(process.env.TIRION_API_TOKEN_FILE)) throw new Error('TIRION_API_TOKEN_FILE must be absolute');
    return readFileSync(process.env.TIRION_API_TOKEN_FILE, 'utf8').trim();
  }
  if (!['localhost', '127.0.0.1', '[::1]'].includes(new URL(api).hostname.toLowerCase())) return '';
  const candidates = process.env.TIRION_HOME
    ? [join(process.env.TIRION_HOME, 'api-token')]
    : [join(homedir(), '.tirion', 'api-token'), join(homedir(), '.codebase-intel', 'api-token')];
  const found = candidates.find(existsSync);
  return found ? readFileSync(found, 'utf8').trim() : '';
}

// The token reaches curl through a private config file, never the command line.
const token = resolveToken();
let curlConfig = '';
if (token) {
  assert.ok(!/[\r\n"\\]/.test(token), 'API token contains unsupported characters');
  const configDirectory = mkdtempSync(join(tmpdir(), 'tirion-check-'));
  process.on('exit', () => rmSync(configDirectory, { recursive: true, force: true }));
  curlConfig = join(configDirectory, 'curl-config');
  writeFileSync(curlConfig, `header = "X-Tirion-Token: ${token}"\n`, { mode: 0o600 });
} else {
  console.error('warning: no API token found (set TIRION_API_TOKEN or TIRION_API_TOKEN_FILE, or start tirion serve); the API will answer 401');
}

function request(route, payload) {
  const result = execFileSync(client, [
    '-fsS', '--max-time', '30', ...(curlConfig ? ['-K', curlConfig] : []), `${api}${route}`,
    '-H', 'Content-Type: application/json', '--data-binary', '@-',
  ], { input: JSON.stringify(payload), encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 });
  return JSON.parse(result);
}

function diffFor(before, after) {
  const start = source.indexOf(before);
  assert.ok(start >= 0, 'fixture preimage missing');
  assert.equal(source.indexOf(before, start + before.length), -1, 'ambiguous fixture preimage');
  const line = source.slice(0, start).split('\n').length;
  const oldLines = before.trimEnd().split('\n');
  const newLines = after ? after.trimEnd().split('\n') : [];
  const nextLine = newLines.length ? line : line - 1;
  return [
    `diff --git a/${path} b/${path}`,
    `--- a/${path}`,
    `+++ b/${path}`,
    `@@ -${line},${oldLines.length} +${nextLine},${newLines.length} @@`,
    ...oldLines.map(value => '-' + value),
    ...newLines.map(value => '+' + value),
    '',
  ].join('\n');
}

const signature = '    public String listItems(@RequestParam String region) {\n';
const flow = request('/api/flow', { start: 'loadCatalog', workspaceId, depth: 4 });
assert.ok(flow.hops.some(hop => hop.from.repo === 'storefront' &&
  hop.to.repo === 'catalog-api' && hop.via.type === 'http' &&
  hop.via.path === '/api/catalog/items'));
console.log('PASS flow: storefront -> catalog-api HTTP hop');

const cases = [
  { name: 'parameter_removed', before: signature, after: '    public String listItems() {\n', verdict: 'fail' },
  { name: 'parameter_type_changed', before: signature, after: '    public String listItems(@RequestParam Integer region) {\n', verdict: 'fail' },
  {
    name: 'endpoint_removed',
    before: '    @GetMapping("/items")\n' + signature + '        return loadItems(region);\n    }\n',
    after: '', verdict: 'fail',
  },
  { name: 'body_only', before: '        return loadItems(region);\n', after: '        return loadItems(region).trim();\n', verdict: 'info' },
];
for (const scenario of cases) {
  const response = request('/api/verify', {
    workspaceId, repo: 'catalog-api', baseSha,
    diff: diffFor(scenario.before, scenario.after),
  }).verify;
  assert.equal(response.verdict, scenario.verdict, JSON.stringify(response));
  assert.equal(response.changeValidation.preconditions.diffAligned, true);
  const endpoint = response.exposedSurface.endpoints.find(item => item.endpoint === 'GET /api/catalog/items');
  assert.ok(endpoint?.consumers.some(item => item.repo === 'storefront' && item.file === 'src/catalog.ts' && item.line === 4));
  if (scenario.verdict === 'fail') {
    assert.ok(response.breakingChanges.some(item => item.changeType === scenario.name));
  } else {
    assert.equal((response.breakingChanges || []).length, 0);
  }
  console.log(`PASS ${scenario.name}: ${response.verdict}, storefront source evidence present`);
}
