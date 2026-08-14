import { describe, expect, it, vi } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useDisplayChainId } from './useDisplayChainId';

const mockUseChainId = vi.fn();
const mockUseAccount = vi.fn();

vi.mock('wagmi', () => ({
    useChainId: () => mockUseChainId(),
    useAccount: () => mockUseAccount(),
}));

vi.mock('@wallet-trade/shared', async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    TOKENS: {
        11155111: [{ symbol: 'USDC' }, { symbol: 'WETH' }],
        31337: [{ symbol: 'USDC' }],
    },
}));

describe('useDisplayChainId', () => {
    it('falls back to Sepolia when no wallet is connected and the default chain has no deployed tokens', () => {
        mockUseChainId.mockReturnValue(1);
        mockUseAccount.mockReturnValue({ isConnected: false });

        const { result } = renderHook(() => useDisplayChainId());

        expect(result.current).toEqual({ chainId: 11155111, isFallback: true });
    });

    it('keeps a deployed chain when no wallet is connected', () => {
        mockUseChainId.mockReturnValue(31337);
        mockUseAccount.mockReturnValue({ isConnected: false });

        const { result } = renderHook(() => useDisplayChainId());

        expect(result.current).toEqual({ chainId: 31337, isFallback: false });
    });

    it('always honors the connected wallet chain, even without deployed tokens', () => {
        mockUseChainId.mockReturnValue(1);
        mockUseAccount.mockReturnValue({ isConnected: true });

        const { result } = renderHook(() => useDisplayChainId());

        expect(result.current).toEqual({ chainId: 1, isFallback: false });
    });
});
