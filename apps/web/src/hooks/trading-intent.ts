import { erc20Abi, formatUnits, isAddress, maxUint256, parseUnits, zeroAddress, type Address } from 'viem';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import type { CloseTradingPositionParams, OpenTradingPositionParams, TradingMarketView } from './trading.types';
import { freezeStep } from './web3/txIntent/intent';
import type { TxStep } from './web3/txIntent/types';

export type TradeIntentRequest =
  | { kind: 'open'; params: OpenTradingPositionParams }
  | { kind: 'close'; params: CloseTradingPositionParams };

// PerpMarket charges 0.1% of size before its 50x leverage check. With size =
// collateral * nominal leverage, 48x exceeds that cap; 47x is the last safe integer.
export const MAX_TRADING_LEVERAGE = 47;

export interface BuildTradingStepsInput {
  owner?: Address;
  chainId: number;
  positionManager?: Address;
  market: TradingMarketView | null;
  currentPrice?: bigint;
  referencePrice?: bigint;
  allowance?: bigint;
  request: TradeIntentRequest;
  now: number;
}

/** Missing prices never disable the PositionManager guard: zero means no limit on chain. */
export function tradingPriceWithinGuard(input: {
  kind: TradeIntentRequest['kind'];
  isLong: boolean;
  currentPrice?: bigint;
  acceptablePrice?: bigint;
}): boolean {
  if (!positiveUint(input.currentPrice) || !positiveUint(input.acceptablePrice)) return false;
  const maximumPrice = input.kind === 'open' ? input.isLong : !input.isLong;
  return maximumPrice
    ? input.currentPrice <= input.acceptablePrice
    : input.currentPrice >= input.acceptablePrice;
}

/**
 * Builds a reviewable plan without wallet calls. Invalid or incomplete inputs
 * return null, including an unknown allowance. Rebuilding with the review's
 * referencePrice and now preserves bytes while the live price stays executable.
 */
export function buildTradingSteps(input: BuildTradingStepsInput): TxStep[] | null {
  const { owner, positionManager, chainId, market, request, currentPrice, allowance, now } = input;
  const referencePrice = input.referencePrice ?? currentPrice;
  if (!validAddress(owner) || !validAddress(positionManager) || !market
    || !Number.isSafeInteger(chainId) || chainId <= 0 || market.chainId !== chainId
    || !validAddress(market.indexToken) || !validAddress(market.collateralToken)
    || !market.symbol.trim() || market.pricePrecision !== 30
    || !Number.isInteger(market.collateralDecimals) || market.collateralDecimals < 0 || market.collateralDecimals > 30
    || !Number.isSafeInteger(now) || now < 0
    || !positiveUint(currentPrice) || !positiveUint(referencePrice)) return null;

  const slippagePercent = request.params.slippagePercent ?? 0.5;
  const deadlineMinutes = request.params.deadlineMinutes ?? 20;
  const slippage = exactUnits(String(slippagePercent), 2);
  if (slippage === null || slippage > 500n
    || !Number.isInteger(deadlineMinutes) || deadlineMinutes < 1 || deadlineMinutes > 120) return null;

  const isLong = request.kind === 'open' ? request.params.isLong : request.params.position.isLong;
  if (typeof isLong !== 'boolean') return null;
  const maximumPrice = request.kind === 'open' ? isLong : !isLong;
  const explicitPrice = request.kind === 'open' ? request.params.acceptablePrice : undefined;
  const scaledPrice = referencePrice * (10_000n + (maximumPrice ? slippage : -slippage));
  // Round toward a tighter guard: floor a maximum, ceil a minimum. Rounding
  // down a minimum would permit a price outside the reviewed percentage.
  const acceptablePrice = explicitPrice !== undefined
    ? exactUnits(explicitPrice, 30)
    : (scaledPrice + (maximumPrice ? 0n : 9_999n)) / 10_000n;
  if (acceptablePrice === null || !tradingPriceWithinGuard({ kind: request.kind, isLong, currentPrice, acceptablePrice })) return null;

  const deadline = BigInt(Math.floor(now / 1000) + deadlineMinutes * 60);
  const guard = { slippageBps: Number(slippage), acceptablePrice, deadline };
  const common = { owner, targetChainId: chainId, guard, now };
  const metadata = {
    market: market.symbol,
    indexToken: market.indexToken,
    collateralToken: market.collateralToken,
    collateralDecimals: market.collateralDecimals,
    pricePrecision: market.pricePrecision,
    isLong,
  };

  try {
    if (request.kind === 'close') {
      const { position } = request.params;
      if (!position.hasPosition || !positiveUint(position.size)
        || typeof position.collateral !== 'bigint' || position.collateral < 0n
        || position.market.symbol !== market.symbol
        || position.market.indexToken.toLowerCase() !== market.indexToken.toLowerCase()
        || position.market.collateralToken.toLowerCase() !== market.collateralToken.toLowerCase()
        || position.market.collateralDecimals !== market.collateralDecimals
        || position.market.pricePrecision !== market.pricePrecision) return null;
      return [freezeStep({
        ...common, kind: 'action', actionType: 'close-position', target: positionManager,
        abi: CONTRACT_ABIS.PositionManager, functionName: 'closePosition',
        args: [market.indexToken, market.collateralToken, 0n, position.size, isLong, acceptablePrice, deadline],
        expectsIndexing: true,
        metadata: { ...metadata, size: position.size.toString(), collateralAmount: formatUnits(position.collateral, 30) },
      })];
    }

    const { collateralAmount, leverage } = request.params;
    const amount = exactUnits(collateralAmount, market.collateralDecimals);
    if (!positiveUint(amount) || !Number.isInteger(leverage) || leverage < 1 || leverage > MAX_TRADING_LEVERAGE
      || typeof allowance !== 'bigint' || allowance < 0n || allowance > maxUint256) return null;
    // The configured markets use USD-stable collateral; USD size always has
    // 30 decimals. Integer scaling keeps even sub-float collateral exact.
    const size = amount * BigInt(leverage) * 10n ** BigInt(30 - market.collateralDecimals);
    if (!positiveUint(size)) return null;
    const token = { address: market.collateralToken, symbol: 'USDC', decimals: market.collateralDecimals, amount };
    const tradeMetadata = { ...metadata, collateralAmount: formatUnits(amount, market.collateralDecimals), leverage, size: size.toString() };
    const action = freezeStep({
      ...common, kind: 'action', actionType: 'open-position', target: positionManager,
      abi: CONTRACT_ABIS.PositionManager, functionName: 'openPosition',
      args: [market.indexToken, market.collateralToken, amount, size, isLong, acceptablePrice, deadline],
      token, expectsIndexing: true, metadata: tradeMetadata,
    });
    if (allowance >= amount) return [action];
    return [freezeStep({
      ...common, kind: 'approve', actionType: 'approve', target: market.collateralToken,
      abi: erc20Abi, functionName: 'approve', args: [positionManager, amount],
      token, expectsIndexing: false, metadata: tradeMetadata,
    }), action];
  } catch {
    // An unencodable uint or a deadline inside the engine's safety margin
    // must not produce a partial plan containing a signable approval.
    return null;
  }
}

function validAddress(value: Address | undefined): value is Address {
  return typeof value === 'string' && isAddress(value) && value.toLowerCase() !== zeroAddress;
}

function positiveUint(value: bigint | null | undefined): value is bigint {
  return typeof value === 'bigint' && value > 0n && value <= maxUint256;
}

function exactUnits(value: string, decimals: number): bigint | null {
  // parseUnits rounds excess decimals; user-entered amounts and guards must
  // instead be represented exactly or rejected before they become calldata.
  if (!/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(value) || (value.split('.')[1]?.length ?? 0) > decimals) return null;
  try {
    const parsed = parseUnits(value, decimals);
    return parsed <= maxUint256 ? parsed : null;
  } catch {
    return null;
  }
}
