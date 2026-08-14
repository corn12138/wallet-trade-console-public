import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { TradingChainId } from '@/lib/web3/trading-chain';
import { useTradingTransactions } from './useTradingTransactions';
import type { AggregatedPosition } from './web3/usePerpPositions';
import type { TradingMarketView } from './trading.types';

const writeContractMock = vi.fn();

vi.mock('wagmi', () => ({
  useWriteContract: () => ({
    writeContract: writeContractMock,
    data: undefined,
    isPending: false,
    error: null,
    reset: vi.fn(),
  }),
  useWaitForTransactionReceipt: () => ({
    isLoading: false,
    isSuccess: false,
    data: undefined,
  }),
}));

vi.mock('./web3/useTokenApproval', () => ({
  useTokenApproval: () => ({
    isApproved: () => true,
    approve: vi.fn(),
    isPending: false,
    isSuccess: false,
  }),
}));

vi.mock('./web3/usePersistTransactionLifecycle', () => ({
  usePersistTransactionLifecycle: () => undefined,
}));

const market: TradingMarketView = {
  symbol: 'ETH-USD',
  indexToken: '0xfFeA240Cd1EB135C8aA2597Ca203EFD629Ac5FCD',
  collateralToken: '0x57E554D795A18f3cA0A0e9e03a17AC3C509C3bF8',
  indexDecimals: 18,
  collateralDecimals: 18,
  pricePrecision: 30,
  chainId: 11155111,
  fundingRate: '0',
  longOpenInterest: '0',
  shortOpenInterest: '0',
  volume24h: '0',
};

const position = {
  size: 1_000n,
  isLong: true,
  market,
} as unknown as AggregatedPosition;

function renderTradingTransactions(initialChainId: TradingChainId) {
  return renderHook(
    ({ chainId }: { chainId: TradingChainId }) =>
      useTradingTransactions({
        address: '0x1111111111111111111111111111111111111111',
        addrs: {
          positionManager: '0x2222222222222222222222222222222222222222',
          usdc: '0x3333333333333333333333333333333333333333',
        },
        chainId,
        currentPrice: 2_000n * 10n ** 30n,
        selectedMarket: market,
        refetchAll: () => {},
        refetchActivity: () => {},
      }),
    { initialProps: { chainId: initialChainId } },
  );
}

describe('useTradingTransactions chain switching', () => {
  beforeEach(() => {
    writeContractMock.mockClear();
  });

  it('openPosition uses the latest chainId after a chain switch', () => {
    const { result, rerender } = renderTradingTransactions(31337);

    act(() => {
      result.current.openPosition({ collateralAmount: '100', leverage: 2, isLong: true });
    });
    expect(writeContractMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ chainId: 31337, functionName: 'openPosition' }),
    );

    rerender({ chainId: 11155111 });
    act(() => {
      result.current.openPosition({ collateralAmount: '100', leverage: 2, isLong: true });
    });
    expect(writeContractMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ chainId: 11155111, functionName: 'openPosition' }),
    );
  });

  it('closePosition uses the latest chainId after a chain switch', () => {
    const { result, rerender } = renderTradingTransactions(31337);

    act(() => {
      result.current.closePosition({ position });
    });
    expect(writeContractMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ chainId: 31337, functionName: 'closePosition' }),
    );

    rerender({ chainId: 11155111 });
    act(() => {
      result.current.closePosition({ position });
    });
    expect(writeContractMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ chainId: 11155111, functionName: 'closePosition' }),
    );
  });
});
