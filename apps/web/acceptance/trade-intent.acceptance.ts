import { expect, test } from 'vitest';
import { readFileSync, writeFileSync, openSync, closeSync } from 'node:fs';
import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { createPublicClient, createWalletClient, http, parseEther, parseUnits, keccak256, type Abi, type Address, type Hex } from 'viem';
import { foundry } from 'viem/chains';
import { buildTradingSteps } from '../src/hooks/trading-intent';
import type { TradingMarketView } from '../src/hooks/trading.types';
import type { TxStep } from '../src/hooks/web3/txIntent/types';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const run = process.env.TRADE_ACCEPTANCE_DIR;
const rpc = process.env.TRADE_ACCEPTANCE_RPC;
const api = process.env.TRADE_ACCEPTANCE_API;
const reportPath = process.env.TRADE_ACCEPTANCE_REPORT;
if (!run || !rpc || !api || !reportPath) throw new Error('Run scripts/acceptance/trade-intent-local.sh');
for (const url of [rpc, api, process.env.DATABASE_URL!, process.env.REDIS_URL!]) {
  if (new URL(url).hostname !== '127.0.0.1') throw new Error('Acceptance requires isolated loopback services');
}
const client = createPublicClient({ chain: foundry, transport: http(rpc), pollingInterval: 100 });
const wallet = createWalletClient({ chain: foundry, transport: http(rpc) });
const processes: ChildProcess[] = [];
const records: object[] = [];
const artifacts = new Map<string, { abi: Abi; bytecode: { object: Hex } }>();
function artifact(name: string) {
  if (!artifacts.has(name)) artifacts.set(name, JSON.parse(readFileSync(`${root}/contracts-foundry/out/${name}.sol/${name}.json`, 'utf8')));
  return artifacts.get(name)!;
}
async function receipt(hash: Hex, status: 'success' | 'reverted' = 'success') {
  const result = await client.waitForTransactionReceipt({ hash, timeout: 15_000 });
  expect(result.status).toBe(status);
  return result;
}
async function deploy(name: string, owner: Address, args: readonly unknown[] = []) {
  const { abi, bytecode } = artifact(name);
  const result = await receipt(await wallet.deployContract({ account: owner, abi, bytecode: bytecode.object, args }));
  expect(result.contractAddress).toBeTruthy();
  return result.contractAddress!;
}
async function write(name: string, address: Address, owner: Address, functionName: string, args: readonly unknown[]) {
  return receipt(await wallet.writeContract({ address, account: owner, abi: artifact(name).abi, functionName, args }));
}
async function read(name: string, address: Address, functionName: string, args: readonly unknown[] = []) {
  return client.readContract({ address, abi: artifact(name).abi, functionName, args });
}
async function send(step: TxStep, expected: 'success' | 'reverted' = 'success') {
  expect(step.targetChainId).toBe(31337);
  expect(keccak256(step.calldata)).toBe(step.review.calldataHash);
  // Send the production builder's frozen bytes; no ABI re-encoding in this path.
  const hash = await wallet.sendTransaction({ account: step.owner, to: step.target, data: step.calldata, value: step.value, gas: 2_000_000n });
  const mined = await receipt(hash, expected);
  const tx = await client.getTransaction({ hash });
  expect(tx.input).toBe(step.calldata);
  expect(tx.from.toLowerCase()).toBe(step.owner);
  expect(tx.to?.toLowerCase()).toBe(step.target);
  expect(tx.value).toBe(step.value);
  records.push({ action: step.actionType, hash, status: mined.status, blockNumber: mined.blockNumber.toString(), calldataHash: step.review.calldataHash });
  return hash;
}
function startService(name: 'api' | 'indexer') {
  const fd = openSync(`${run}/${name}.log`, 'w');
  // Explicit environment prevents inherited production endpoints and contract overrides.
  const child = spawn(`${run}/${name}`, [], { cwd: run, stdio: ['ignore', fd, fd], env: {
    PATH: process.env.PATH, HOME: process.env.HOME, NODE_ENV: 'test', API_GO_LISTEN_ADDR: new URL(api!).host,
    DATABASE_URL: process.env.DATABASE_URL, REDIS_URL: process.env.REDIS_URL,
    CONTRACTS_DEPLOYMENTS_DIR: `${run}/deployments`, INDEXER_LOCAL: '1',
    SEPOLIA_RPC_URL: rpc, INDEXER_CONFIRMATION_DEPTH: '0', INDEXER_DEPLOY_BLOCK: '0',
    INDEXER_POLL_INTERVAL_MS: '100', JWT_SECRET: 'disposable-local-acceptance-only',
  } });
  closeSync(fd);
  processes.push(child);
}
async function rows(path: string): Promise<Record<string, unknown>[]> {
  const response = await fetch(`${api}/api${path}`, { signal: AbortSignal.timeout(2000) });
  expect(response.status).toBe(200);
  const data = await response.json();
  expect(Array.isArray(data)).toBe(true);
  return data;
}
async function eventually(check: () => Promise<void>) {
  let last: unknown;
  for (let n = 0; n < 80; n++) {
    try { await check(); return; } catch (error) { last = error; }
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  throw last;
}

test('frozen trade bytes execute on fresh contracts and appear in the Go indexed API', async () => {
  let passed = false;
  try {
    expect(await client.getChainId()).toBe(31337);
    expect(await client.getBlockNumber()).toBe(0n);
    const [owner, trader] = await wallet.getAddresses();
    expect(owner).toBeTruthy(); expect(trader).toBeTruthy();
    const usdc = await deploy('MockERC20', owner, ['Mock USDC', 'USDC', 18]);
    const weth = await deploy('MockERC20', owner, ['Mock WETH', 'WETH', 18]);
    const oracle = await deploy('PerpOracle', owner);
    const vault = await deploy('PerpVault', owner);
    const marketAddress = await deploy('PerpMarket', owner, [vault, oracle, owner]);
    const manager = await deploy('PositionManager', owner, [marketAddress, oracle]);
    await write('PerpVault', vault, owner, 'setMarket', [marketAddress]);
    await write('PerpMarket', marketAddress, owner, 'setPositionManager', [manager]);
    const price = parseUnits('3450', 30);
    await write('PerpOracle', oracle, owner, 'setManualPrice', [usdc, parseUnits('1', 30)]);
    await write('PerpOracle', oracle, owner, 'setManualPrice', [weth, price]);
    await write('MockERC20', usdc, owner, 'mint', [owner, parseEther('500000')]);
    await write('MockERC20', usdc, owner, 'approve', [vault, parseEther('500000')]);
    await write('PerpVault', vault, owner, 'addLiquidity', [usdc, parseEther('500000')]);
    // Long positions reserve index tokens; USDC liquidity alone only funds shorts.
    await write('MockERC20', weth, owner, 'mint', [owner, parseEther('500')]);
    await write('MockERC20', weth, owner, 'approve', [vault, parseEther('500')]);
    await write('PerpVault', vault, owner, 'addLiquidity', [weth, parseEther('500')]);
    await write('MockERC20', usdc, owner, 'mint', [trader, parseEther('1000')]);
    const contracts = { MockUSDC: { address: usdc, decimals: 18 }, MockWETH: { address: weth, decimals: 18 },
      PerpOracle: { address: oracle }, PerpVault: { address: vault }, PerpMarket: { address: marketAddress }, PositionManager: { address: manager } };
    writeFileSync(`${run}/deployments/local.json`, JSON.stringify({ chainId: 31337, timestamp: new Date().toISOString(), contracts }));
    startService('api'); startService('indexer');
    const market: TradingMarketView = { symbol: 'ETH-USD', indexToken: weth, collateralToken: usdc,
      indexDecimals: 18, collateralDecimals: 18, pricePrecision: 30, chainId: 31337,
      fundingRate: '0', longOpenInterest: '0', shortOpenInterest: '0', volume24h: '0' };
    const input = { owner: trader, chainId: 31337, positionManager: manager, market, currentPrice: price, allowance: 0n };
    const successfulHashes: Hex[] = [];
    for (const isLong of [true, false]) {
      const before = await read('MockERC20', usdc, 'balanceOf', [trader]) as bigint;
      const steps = buildTradingSteps({ ...input, now: Date.now(), request: { kind: 'open', params: { collateralAmount: '100', leverage: 47, isLong } } });
      expect(steps?.map(s => s.kind)).toEqual(['approve', 'action']);
      await send(steps![0]);
      expect(await read('MockERC20', usdc, 'allowance', [trader, manager])).toBe(parseEther('100'));
      successfulHashes.push(await send(steps![1]));
      expect(await read('MockERC20', usdc, 'allowance', [trader, manager])).toBe(0n);
      const key = await read('PerpMarket', marketAddress, 'getPositionKey', [trader, weth, usdc, isLong]);
      const data = await read('PerpMarket', marketAddress, 'positions', [key]) as readonly bigint[];
      expect(data[0]).toBe(parseUnits('4700', 30));
      expect(data[1]).toBe(parseUnits('95.3', 30));
      const close = buildTradingSteps({ ...input, now: Date.now(), request: { kind: 'close', params: { position: {
        market, isLong, size: data[0], collateral: data[1], averagePrice: data[2], entryFundingRate: data[3],
        reserveAmount: data[4], realisedPnl: data[5], lastUpdatedAt: data[6], hasPosition: true,
      } } } });
      expect(close).toHaveLength(1);
      successfulHashes.push(await send(close![0]));
      expect((await read('PerpMarket', marketAddress, 'positions', [key]) as readonly bigint[])[0]).toBe(0n);
      expect(await read('MockERC20', usdc, 'balanceOf', [trader])).toBe(before - parseEther('9.4'));
    }
    // A mined revert is evidence of the on-chain guard, distinct from builder rejection.
    const stale = buildTradingSteps({ ...input, now: Date.now(), request: { kind: 'open', params: { collateralAmount: '100', leverage: 2, isLong: true } } })!;
    await send(stale[0]);
    const balance = await read('MockERC20', usdc, 'balanceOf', [trader]);
    await write('PerpOracle', oracle, owner, 'setManualPrice', [weth, parseUnits('4000', 30)]);
    await expect(client.call({ account: stale[1].owner, to: stale[1].target, data: stale[1].calldata })).rejects.toThrow('PRICE_TOO_HIGH');
    const revertedHash = await send(stale[1], 'reverted');
    expect(await read('MockERC20', usdc, 'balanceOf', [trader])).toBe(balance);
    expect(await read('MockERC20', usdc, 'allowance', [trader, manager])).toBe(parseEther('100'));
    const finalBlock = await client.getBlockNumber({ cacheTime: 0 });
    await eventually(async () => {
      const checkpoint = execFileSync('psql', [process.env.DATABASE_URL!, '-Atqc',
        `SELECT COALESCE(MAX(last_processed_block),0) FROM web3_indexer_state WHERE chain_id=31337 AND contract_address='${marketAddress.toLowerCase()}'`],
      { encoding: 'utf8', timeout: 2000 }).trim();
      expect(BigInt(checkpoint)).toBeGreaterThanOrEqual(finalBlock);
      const trades = await rows('/trading/market-trades?symbol=ETH-USD&chainId=31337&limit=50');
      expect(trades).toHaveLength(4);
      expect(trades.map(t => t.txHash).sort()).toEqual([...successfulHashes].sort());
      expect(trades.some(t => t.txHash === revertedHash)).toBe(false);
    });
    const candles = await rows('/trading/candles?symbol=ETH-USD&chainId=31337&resolution=15m&limit=50');
    expect(candles.length).toBeGreaterThan(0);
    expect(candles.reduce((sum, candle) => sum + BigInt(String(candle.volume)), 0n)).toBe(parseUnits('18800', 30));
    expect(candles.reduce((sum, candle) => sum + Number(candle.trades), 0)).toBe(4);
    expect(await rows('/trading/market-trades?symbol=ETH-USD&chainId=11155111&limit=50')).toEqual([]);
    records.push({ indexedHashes: successfulHashes, indexerCaughtUpThrough: finalBlock.toString(), candles, contracts });
    passed = true;
  } finally {
    await Promise.all(processes.map(child => new Promise<void>(resolve => {
      if (child.exitCode !== null || child.signalCode !== null) return resolve();
      child.once('exit', () => resolve());
      child.kill('SIGTERM');
      const timer = setTimeout(() => child.kill('SIGKILL'), 3000);
      timer.unref(); child.once('exit', () => clearTimeout(timer));
    })));
    writeFileSync(reportPath!, JSON.stringify({ result: passed ? 'pass' : 'fail', scope: 'local-builder-chain-indexer-api',
      browserWallet: 'not-run', chainId: 31337, createdAt: new Date().toISOString(),
      source: JSON.parse(readFileSync(`${run}/source.json`, 'utf8')), records }, null, 2));
    if (!passed) for (const name of ['api', 'indexer']) {
      try { console.error(`${name}: ${readFileSync(`${run}/${name}.log`, 'utf8').slice(-6000)}`); } catch { /* Service may not have started. */ }
    }
  }
});
