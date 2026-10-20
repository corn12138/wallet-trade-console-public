import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { createPublicClient, createWalletClient, encodeFunctionData, http, parseAbi } from 'viem';
import { mnemonicToAccount } from 'viem/accounts';
import { foundry } from 'viem/chains';
import { SiweMessage } from 'siwe';

// A cloud API behind an SSH forward uses the isolated local EVM through a
// reverse forward. No testnet/mainnet transaction or production identity is used.
const api = process.env.TEST_API_URL;
const rpc = process.env.TEST_RPC_URL;
const bytecodePath = process.env.TEST_FIXTURE_BYTECODE;
const evidencePath = process.env.TEST_EVIDENCE_PATH;
if (!api || !rpc || !bytecodePath || !evidencePath) throw new Error('Explicit test endpoints, bytecode and evidence path required');
for (const endpoint of [api, rpc]) assert.equal(new URL(endpoint).hostname, '127.0.0.1');
const account = mnemonicToAccount('test test test test test test test test test test test junk');
const other = mnemonicToAccount('test test test test test test test test test test test junk', { addressIndex: 1 });
const client = createPublicClient({ chain: foundry, transport: http(rpc) });
assert.equal(await client.getChainId(), 31337);
const wallet = createWalletClient({ account, chain: foundry, transport: http(rpc) });
const abi = parseAbi([
  'function verify(address expectedSender, uint256 expectedValue) payable returns (uint256)',
  'function noReturn() payable', 'function forceRevert() pure',
]);
const report = { capturedAt: new Date().toISOString(), environment: 'Tencent VM isolated Go API + PG16 + Redis7; local Anvil EVM over SSH', checks: [] };
const response = async (path, body, token, headers = {}) => {
  const res = await fetch(`${api}${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { Origin: 'http://localhost:3002', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}), ...headers },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(25000),
  });
  return { status: res.status, body: await res.json() };
};
function record(name, body = {}) { report.checks.push({ name, passed: true, ...body }); }
try {
  const health = await response('/healthz'); assert.equal(health.status, 200); record('cloud-health');
  const status = await response('/api/status/product'); assert.equal(status.status, 200);
  report.runtime = status.body.runtime; assert.match(report.runtime.version, /^spec-b0-/);
  const deployHash = await wallet.deployContract({ abi, bytecode: (await readFile(bytecodePath, 'utf8')).trim() });
  const receipt = await client.waitForTransactionReceipt({ hash: deployHash }); assert.equal(receipt.status, 'success');
  const to = receipt.contractAddress; assert.ok(to);
  const nonce = await response('/api/auth/nonce', { address: account.address }); assert.equal(nonce.status, 200);
  const message = new SiweMessage({ ...nonce.body, address: account.address, version: '1', chainId: 31337 }).prepareMessage();
  const signature = await account.signMessage({ message });
  const login = await response('/api/auth/verify', { message, signature }); assert.equal(login.status, 200);
  const token = login.body.token; assert.ok(token);
  const me = await response('/api/auth/me', undefined, token);
  assert.equal(me.status, 200); assert.equal(me.body.address.toLowerCase(), account.address.toLowerCase()); assert.equal(me.body.chainId, 31337);
  record('real-siwe-signature-and-me');
  const replay = await response('/api/auth/verify', { message, signature }); assert.notEqual(replay.status, 200); record('siwe-nonce-replay-rejected', { status: replay.status });
  const input = { operationType: 'custom', fromAddress: account.address, chainId: 31337,
    tx: { to, value: '7', data: encodeFunctionData({ abi, functionName: 'verify', args: [account.address, 7n] }) } };
  const anonymous = await response('/api/security/tx-review', input, undefined, { 'X-Wallet-Address': account.address });
  assert.equal(anonymous.status, 401); record('wallet-header-not-authentication');
  const mismatchedOwner = await response('/api/security/tx-review', { ...input, fromAddress: other.address }, token);
  assert.equal(mismatchedOwner.status, 403); record('owner-isolation');
  for (const [name, value, functionName, args, expected] of [
    ['sender-value', '7', 'verify', [account.address, 7n], 'succeeded'],
    ['wrong-sender', '7', 'verify', [other.address, 7n], 'reverted'],
    ['wrong-value', '6', 'verify', [account.address, 7n], 'reverted'],
    ['contract-revert', '0', 'forceRevert', [], 'reverted'],
    ['empty-return', '1', 'noReturn', [], 'succeeded'],
  ]) {
    const result = await response('/api/security/tx-review', { ...input, tx: { to, value, data: encodeFunctionData({ abi, functionName, args }) } }, token);
    assert.equal(result.status, 200, JSON.stringify(result.body));
    const sim = result.body.simulation; assert.equal(sim.simulationScope, 'full-envelope');
    assert.equal(sim.observations.call.observationStatus, expected); assert.equal(sim.observations.gas.observationStatus, expected);
    if (expected === 'succeeded') assert.equal(sim.consistentSnapshot, true);
    else assert.equal(result.body.reviewStatus, 'blocked');
    assert.match(sim.observations.call.blockReference.number, /^\d+$/);
    assert.equal(sim.observations.feeQuote.pinning, 'latest-only');
    record(name, { reviewStatus: result.body.reviewStatus, simulation: sim });
  }
  for (const [name, body, expected] of [
    ['client-balance-rejected', { ...input, nativeBalanceWei: '999999999999999999999999' }, 400],
    ['access-list-rejected', { ...input, tx: { ...input.tx, accessList: [] } }, 400],
    ['calldata-budget', { ...input, tx: { ...input.tx, data: `0x${'00'.repeat(16385)}` } }, 400],
    ['body-budget', { ...input, tx: { ...input.tx, data: `0x${'00'.repeat(32768)}` } }, 413],
  ]) {
    const result = await response('/api/security/tx-review', body, token); assert.equal(result.status, expected); record(name, { status: result.status });
  }
  const explanation = await response('/api/security/tx-review/explain', { ...input, locale: 'en' }, token);
  assert.equal(explanation.status, 200); assert.equal(explanation.body.source, 'static'); assert.equal(explanation.body.explanation, null);
  assert.equal(explanation.body.review.simulation.simulationScope, 'full-envelope'); record('AI-disabled-retains-deterministic-evidence');
  report.passed = true;
} catch (error) {
  report.passed = false; report.failure = error.message; throw error;
} finally {
  await writeFile(evidencePath, `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, evidencePath }));
}
