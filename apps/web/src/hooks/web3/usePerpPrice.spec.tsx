import { renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useReadContract } from 'wagmi';
import { usePerpPrice } from './usePerpPrice';

vi.mock('wagmi', () => ({ useReadContract: vi.fn() }));
vi.mock('@/lib/web3/contracts', () => ({
  getPerpAddresses: () => ({ oracle: '0x0000000000000000000000000000000000000001' }),
  CONTRACT_ABIS: { PerpOracle: [] },
}));
const token = '0x0000000000000000000000000000000000000002';
function response(data: bigint | undefined, error: Error | null = null) {
  vi.mocked(useReadContract).mockReturnValue({ data, error, isLoading: false } as ReturnType<typeof useReadContract>);
}
describe('market oracle reads before wallet connection', () => {
  beforeEach(() => vi.clearAllMocks());
  it('pins the read to the selected market chain', () => {
    response(67000n * 10n ** 30n);
    const { result } = renderHook(() => usePerpPrice(token, 11155111));
    expect(useReadContract).toHaveBeenCalledWith(expect.objectContaining({ chainId: 11155111 }));
    expect(result.current.formatted).toBe('67000');
  });
  it.each([undefined, 0n])('does not invent a price for %s', (data) => {
    response(data);
    const { result } = renderHook(() => usePerpPrice(token, 11155111));
    expect(result.current.price).toBeUndefined();
    expect(result.current.formatted).toBe('—');
  });
  it('does not present stale data as a valid price after a read error', () => {
    response(67000n * 10n ** 30n, new Error('RPC unavailable'));
    const { result } = renderHook(() => usePerpPrice(token, 11155111));
    expect(result.current.price).toBeUndefined();
    expect(result.current.formatted).toBe('—');
  });
});
