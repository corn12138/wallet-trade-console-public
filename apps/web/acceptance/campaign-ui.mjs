import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { mnemonicToAccount } from 'viem/accounts';
import { SiweMessage } from 'siwe';

// This account is an unfunded local acceptance identity. The script is limited
// to an explicitly supplied loopback API and never requests a chain transaction.
const api = process.env.TEST_CAMPAIGN_API;
const output = process.env.TEST_CAMPAIGN_FIXTURE;
assert.ok(api && output, 'explicit loopback API and private output required');
assert.equal(new URL(api).hostname, '127.0.0.1');
const account = mnemonicToAccount('test test test test test test test test test test test junk');
const checks = [];
function commandID() {
  const bytes = randomBytes(16);
  const stamp = BigInt(Date.now()).toString(16).padStart(12, '0');
  bytes[6] = (bytes[6] & 15) | 112;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = stamp + bytes.subarray(6).toString('hex');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
async function call(path, body, token, method = body === undefined ? 'GET' : 'POST') {
  const res = await fetch(`${api}${path}`, { method, signal: AbortSignal.timeout(10_000),
    headers: { Origin: 'http://127.0.0.1:3002', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  const parsed = await res.json();
  assert.ok(res.ok, `HTTP ${res.status} at ${path}: ${parsed.code ?? 'unexpected response'}`);
  return parsed;
}
const nonce = await call('/api/auth/nonce', { address: account.address });
const message = new SiweMessage({ ...nonce, address: account.address, version: '1', chainId: 11155111 }).prepareMessage();
const signature = await account.signMessage({ message });
const login = await call('/api/auth/verify', { message, signature });
const profile = await call('/api/auth/me', undefined, login.token);
assert.equal(profile.address.toLowerCase(), account.address.toLowerCase());
checks.push('real SIWE nonce, EOA signature, Redis consumption and authenticated Me');
const cases = JSON.parse(await readFile(new URL('../../../packages/shared/contracts/fixtures/campaign-v2-wire-cases.json', import.meta.url), 'utf8'));
const template = JSON.parse(cases.find((item) => item.name === 'valid CreateCommandInput').raw);
const rows = [];
const iso = (offset) => new Date(Math.floor(Date.now() / 1000) * 1000 + offset).toISOString().replace('.000Z', 'Z');
for (const [title, chinese, from, until, publish] of [
  ['Active browser campaign', '进行中的浏览器验收活动', -600_000, 3_600_000, true],
  ['Upcoming browser campaign', '即将开始的浏览器验收活动', 600_000, 7_200_000, true],
  ['Private draft must stay hidden', '不得公开的草稿', -600_000, 3_600_000, false],
]) {
  const input = structuredClone(template); delete input.slug;
  input.clientCommandId = commandID(); input.reason = 'Isolated browser acceptance fixture';
  input.revision.materialTerms.startAt = iso(from); input.revision.materialTerms.endAt = iso(until);
  input.revision.materialTerms.copy.en.title = title; input.revision.materialTerms.copy.zh.title = chinese;
  const created = await call('/api/v2/campaign-admin/campaigns', input, login.token);
  const { campaignId: id, revisionId } = created.original;
  if (publish) await call(`/api/v2/campaign-admin/campaigns/${id}/publications`, {
    clientCommandId: commandID(), reason: 'Publish isolated browser fixture', operation: 'publish',
    expectedPublicationVersion: '0', candidateRevisionId: revisionId,
  }, login.token);
  rows.push({ id, revisionId, title, chinese, publish });
}
const list = await call('/api/v2/campaigns');
assert.equal(list.items.length, 2);
assert.ok(!list.items.some((item) => item.id === rows[2].id));
const legacy = await call('/api/campaign'); assert.equal(legacy.length, 2);
checks.push('full cmd/api published reads and legacy compatibility exclude the private draft');
await writeFile(output, JSON.stringify({ api, owner: account.address, token: login.token, rows, checks }, null, 2), { mode: 0o600 });
console.log(JSON.stringify({ checks: checks.length, campaigns: rows.length, tokensLogged: false }));
