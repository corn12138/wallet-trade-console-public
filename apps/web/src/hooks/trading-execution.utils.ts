import { parseUnits } from 'viem';

interface TradingExecutionInput {
  currentPrice?: bigint;
  isLong: boolean;
  slippagePercent: number;
  deadlineMinutes: number;
  explicitAcceptablePrice?: string;
}

interface TradingExecutionParams {
  acceptablePrice: bigint;
  deadline: bigint;
}

export function buildOpenExecutionParams(input: TradingExecutionInput): TradingExecutionParams {
  return {
    acceptablePrice: resolveAcceptablePrice({
      currentPrice: input.currentPrice,
      explicitAcceptablePrice: input.explicitAcceptablePrice,
      slippagePercent: input.slippagePercent,
      isLong: input.isLong,
      mode: 'open',
    }),
    deadline: buildDeadline(input.deadlineMinutes),
  };
}

export function buildCloseExecutionParams(input: Omit<TradingExecutionInput, 'explicitAcceptablePrice'>): TradingExecutionParams {
  return {
    acceptablePrice: resolveAcceptablePrice({
      currentPrice: input.currentPrice,
      slippagePercent: input.slippagePercent,
      isLong: input.isLong,
      mode: 'close',
    }),
    deadline: buildDeadline(input.deadlineMinutes),
  };
}

function resolveAcceptablePrice(input: {
  currentPrice?: bigint;
  explicitAcceptablePrice?: string;
  slippagePercent: number;
  isLong: boolean;
  mode: 'open' | 'close';
}) {
  if (input.explicitAcceptablePrice) {
    return parseUnits(input.explicitAcceptablePrice, 30);
  }

  if (!input.currentPrice || input.currentPrice <= 0n) {
    return 0n;
  }

  const slippageBps = BigInt(Math.max(0, Math.round(input.slippagePercent * 100)));

  if (slippageBps === 0n) {
    return input.currentPrice;
  }

  const denominator = 10_000n;
  const shouldMoveUp = input.mode === 'open' ? input.isLong : !input.isLong;
  const multiplier = shouldMoveUp ? denominator + slippageBps : denominator - slippageBps;

  return (input.currentPrice * multiplier) / denominator;
}

function buildDeadline(deadlineMinutes: number) {
  const safeDeadlineMinutes = Math.max(1, Math.round(deadlineMinutes));
  return BigInt(Math.floor(Date.now() / 1000) + safeDeadlineMinutes * 60);
}
