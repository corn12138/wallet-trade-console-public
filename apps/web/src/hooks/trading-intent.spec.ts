import { describe, expect, it } from 'vitest';
import { decodeFunctionData, erc20Abi, getAddress, keccak256, maxUint256, parseUnits, zeroAddress } from 'viem';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import type { TradingMarketView } from './trading.types';
import type { AggregatedPosition } from './web3/usePerpPositions';
import { buildTradingSteps, MAX_TRADING_LEVERAGE, tradingPriceWithinGuard, type BuildTradingStepsInput, type TradeIntentRequest } from './trading-intent';

const OWNER = '0x000000000000000000000000000000000000dead';
const MANAGER = '0x000000000000000000000000000000000000cafe';
const TOKEN = '0x000000000000000000000000000000000000beef';
const INDEX = '0x000000000000000000000000000000000000aaaa';
const NOW = 1_800_000_000_123;
const PRICE = parseUnits('100', 30);
const market: TradingMarketView = {
  symbol: 'ETH-USD', indexToken: INDEX, collateralToken: TOKEN,
  indexDecimals: 18, collateralDecimals: 6, pricePrecision: 30, chainId: 31337,
  fundingRate: '0', longOpenInterest: '0', shortOpenInterest: '0', volume24h: '0',
};
const position: AggregatedPosition = {
  market, isLong: true, hasPosition: true, size: parseUnits('987.123456789012345678901234567891', 30),
  collateral: parseUnits('123.45', 30), averagePrice: PRICE, entryFundingRate: 0n,
  reserveAmount: 0n, realisedPnl: 0n, lastUpdatedAt: 1_799_000_000n,
};

function open(params: Partial<Extract<TradeIntentRequest, { kind: 'open' }>['params']> = {}): TradeIntentRequest {
  return { kind: 'open', params: { collateralAmount: '1.234567', leverage: 7, isLong: true, ...params } };
}
function input(overrides: Partial<BuildTradingStepsInput> = {}): BuildTradingStepsInput {
  return { owner: OWNER, positionManager: MANAGER, chainId: 31337, market,
    currentPrice: PRICE, allowance: maxUint256, request: open(), now: NOW, ...overrides };
}
function close(isLong = true): TradeIntentRequest {
  return { kind: 'close', params: { position: { ...position, isLong } } };
}

describe('buildTradingSteps frozen bytes', () => {
  it('encodes exact approval then opening against market collateral decimals', () => {
    const [approval, action] = buildTradingSteps(input({ allowance: 1_234_566n }))!;
    expect(decodeFunctionData({ abi: erc20Abi, data: approval.calldata })).toEqual({
      functionName: 'approve', args: [getAddress(MANAGER), 1_234_567n],
    });
    const deadline = 1_800_001_200n;
    expect(decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: action.calldata })).toEqual({
      functionName: 'openPosition',
      args: [getAddress(INDEX), getAddress(TOKEN), 1_234_567n, parseUnits('8.641969', 30), true, parseUnits('100.5', 30), deadline],
    });
    expect(approval).toMatchObject({ kind: 'approve', target: TOKEN, targetChainId: 31337, expectsIndexing: false });
    expect(action).toMatchObject({ kind: 'action', actionType: 'open-position', target: MANAGER, expectsIndexing: true });
    expect(action.review.calldataHash).toBe(keccak256(action.calldata));
    expect(action.review.token).toEqual({ address: TOKEN, amount: 1_234_567n, symbol: 'USDC', decimals: 6 });
    expect(action.review.guard).toEqual({ acceptablePrice: parseUnits('100.5', 30), slippageBps: 50, deadline });
    expect(action.expiresAt).toBe(Number(deadline) * 1_000 - 60_000);
    expect(approval.expiresAt).toBe(action.expiresAt);
    expect(action.metadata).toMatchObject({ market: 'ETH-USD', isLong: true, leverage: 7,
      collateralAmount: '1.234567', collateralDecimals: 6, size: parseUnits('8.641969', 30).toString() });
  });

  it('skips approval when allowance exactly covers the collateral', () => {
    expect(buildTradingSteps(input({ allowance: 1_234_567n }))?.map((step) => step.kind)).toEqual(['action']);
  });

  it('does not round collateral or notional through floating point', () => {
    const amount = '9007199254740993.123456789012345678';
    const [action] = buildTradingSteps(input({ market: { ...market, collateralDecimals: 18 }, request: open({ collateralAmount: amount }) }))!;
    const decoded = decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: action.calldata });
    expect(decoded.args?.[2]).toBe(9_007_199_254_740_993_123_456_789_012_345_678n);
    expect(decoded.args?.[3]).toBe(parseUnits('63050394783186951.864197523086419746', 30));
  });

  it('closes the exact position size without requesting collateral withdrawal or approval', () => {
    const [action] = buildTradingSteps(input({ request: close(), allowance: undefined }))!;
    expect(decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: action.calldata })).toEqual({
      functionName: 'closePosition',
      args: [getAddress(INDEX), getAddress(TOKEN), 0n, position.size, true, parseUnits('99.5', 30), 1_800_001_200n],
    });
    expect(action.review.token).toBeUndefined();
    expect(action.metadata).toMatchObject({ size: position.size.toString(), collateralAmount: '123.45', isLong: true });
  });

  it('freezes an explicit nonzero acceptable price exactly', () => {
    const price = '100.123456789012345678901234567891';
    const [action] = buildTradingSteps(input({ request: open({ acceptablePrice: price }) }))!;
    expect(decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: action.calldata }).args?.[5]).toBe(parseUnits(price, 30));
  });

  it('preserves bytes with the frozen reference when current price moves inside its guard', () => {
    const original = buildTradingSteps(input())![0];
    const candidate = buildTradingSteps(input({ referencePrice: PRICE, currentPrice: parseUnits('100.4', 30) }))![0];
    expect(candidate.calldata).toBe(original.calldata);
    expect(candidate.fingerprint).toBe(original.fingerprint);
    expect(buildTradingSteps(input({ referencePrice: PRICE, currentPrice: parseUnits('100.500000000000000000000000000001', 30) }))).toBeNull();
  });

  it.each([
    ['open', true, 10_051n], ['open', false, 9_951n],
    ['close', true, 9_951n], ['close', false, 10_051n],
  ] as const)('rounds %s %s guard inward without floats', (kind, isLong, guard) => {
    const request = kind === 'open' ? open({ isLong }) : close(isLong);
    const [action] = buildTradingSteps(input({ request, currentPrice: 10_001n }))!;
    expect(action.review.guard?.acceptablePrice).toBe(guard);
  });

  it('allows zero slippage and exact hundredths of a percent', () => {
    expect(buildTradingSteps(input({ request: open({ slippagePercent: 0 }) }))![0].review.guard?.acceptablePrice).toBe(PRICE);
    expect(buildTradingSteps(input({ request: open({ slippagePercent: 0.29 }) }))![0].review.guard?.slippageBps).toBe(29);
  });

  it('accepts 47x and refuses 48x because leverage is checked after the size fee', () => {
    expect(MAX_TRADING_LEVERAGE).toBe(47);
    expect(buildTradingSteps(input({ request: open({ leverage: 47 }) }))).not.toBeNull();
    expect(buildTradingSteps(input({ request: open({ leverage: 48 }) }))).toBeNull();
  });
});

describe('tradingPriceWithinGuard contract boundaries', () => {
  it.each([
    ['open', true, 100n, true], ['open', true, 99n, true], ['open', true, 101n, false],
    ['open', false, 100n, true], ['open', false, 101n, true], ['open', false, 99n, false],
    ['close', true, 100n, true], ['close', true, 101n, true], ['close', true, 99n, false],
    ['close', false, 100n, true], ['close', false, 99n, true], ['close', false, 101n, false],
  ] as const)('%s isLong=%s price=%s executable=%s', (kind, isLong, currentPrice, expected) => {
    expect(tradingPriceWithinGuard({ kind, isLong, currentPrice, acceptablePrice: 100n })).toBe(expected);
  });

  it.each([undefined, 0n, -1n, maxUint256 + 1n])('rejects unavailable or invalid prices: %s', (price) => {
    expect(tradingPriceWithinGuard({ kind: 'open', isLong: true, currentPrice: price, acceptablePrice: 100n })).toBe(false);
    expect(tradingPriceWithinGuard({ kind: 'close', isLong: true, currentPrice: 100n, acceptablePrice: price })).toBe(false);
  });
});

describe('buildTradingSteps fail closed', () => {
  it.each<Partial<BuildTradingStepsInput>>([
    { owner: undefined }, { owner: zeroAddress }, { positionManager: undefined }, { positionManager: zeroAddress },
    { owner: '0x1234' }, { market: null }, { chainId: 1 }, { chainId: -1 }, { chainId: NaN },
    { market: { ...market, indexToken: zeroAddress } }, { market: { ...market, collateralToken: zeroAddress } },
    { market: { ...market, collateralDecimals: 31 } }, { market: { ...market, collateralDecimals: -1 } },
    { market: { ...market, collateralDecimals: 6.5 } }, { market: { ...market, pricePrecision: 18 } },
    { currentPrice: undefined }, { currentPrice: 0n }, { currentPrice: -1n }, { referencePrice: 0n },
    { allowance: undefined }, { allowance: -1n }, { allowance: maxUint256 + 1n }, { now: NaN }, { now: -1 },
  ])('refuses missing or incompatible market and wallet input case %#', (overrides) => {
    expect(buildTradingSteps(input(overrides))).toBeNull();
  });

  it.each(['', '0', '-1', '1e3', ' 1', '1.2345678', 'NaN', maxUint256.toString()])('refuses invalid or unrepresentable collateral %s', (collateralAmount) => {
    expect(buildTradingSteps(input({ request: open({ collateralAmount }) }))).toBeNull();
  });

  it.each([0, -1, 1.5, 48, 50, 100, NaN, Infinity])('refuses invalid leverage %s', (leverage) => {
    expect(buildTradingSteps(input({ request: open({ leverage }) }))).toBeNull();
  });

  it.each([-1, 5.01, 0.001, NaN, Infinity])('refuses invalid slippage %s', (slippagePercent) => {
    expect(buildTradingSteps(input({ request: open({ slippagePercent }) }))).toBeNull();
  });

  it.each([0, 1, 1.5, 121, NaN, Infinity])('refuses invalid or unsafe deadline %s without producing an approval', (deadlineMinutes) => {
    expect(buildTradingSteps(input({ allowance: 0n, request: open({ deadlineMinutes }) }))).toBeNull();
  });

  it.each(['', '0', '-1', '1e2', '100.0000000000000000000000000000001', '99'])('refuses invalid or violated explicit guard %s', (acceptablePrice) => {
    expect(buildTradingSteps(input({ request: open({ acceptablePrice }) }))).toBeNull();
  });

  it.each<Partial<AggregatedPosition>>([
    { hasPosition: false }, { size: 0n }, { size: -1n }, { size: maxUint256 + 1n }, { collateral: -1n },
    { market: { ...market, symbol: 'BTC-USD' } }, { market: { ...market, indexToken: TOKEN } },
    { market: { ...market, collateralToken: INDEX } }, { market: { ...market, collateralDecimals: 18 } },
    { market: { ...market, pricePrecision: 18 } },
  ])('refuses absent or mismatched close position case %#', (overrides) => {
    expect(buildTradingSteps(input({ request: { kind: 'close', params: { position: { ...position, ...overrides } } } }))).toBeNull();
  });
});
