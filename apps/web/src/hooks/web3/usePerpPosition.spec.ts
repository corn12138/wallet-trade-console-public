import { renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { usePerpPosition } from './usePerpPosition';

const read = { keyFailed: false, positionFailed: false };
vi.mock('wagmi', () => ({
  useChainId: () => 11155111,
  useReadContract: ({ functionName }: { functionName: string }) => functionName === 'getPositionKey'
    ? { data: `0x${'a'.repeat(64)}`, isError: read.keyFailed }
    : { data: [100n, 10n, 20n, 0n, 1n, 0n, 3n], isError: read.positionFailed, refetch: vi.fn() },
}));
vi.mock('@/lib/web3/contracts', () => ({ getPerpAddresses: () => ({ market: '0x0000000000000000000000000000000000000001' }), CONTRACT_ABIS: { PerpMarket: [] } }));
beforeEach(() => { read.keyFailed = false; read.positionFailed = false; });
function renderPosition() {
  return renderHook(() => usePerpPosition({ account: '0x0000000000000000000000000000000000000001',
    indexToken: '0x0000000000000000000000000000000000000002',
    collateralToken: '0x0000000000000000000000000000000000000003', isLong: true }));
}
describe('position reads used by close review', () => {
  it('exposes a successfully observed position', () => {
    expect(renderPosition().result.current.position?.size).toBe(100n);
  });
  it.each(['keyFailed', 'positionFailed'] as const)('does not expose cached data after a %s refetch failure', (field) => {
    const { result, rerender } = renderPosition();
    expect(result.current.position?.hasPosition).toBe(true);
    read[field] = true;
    rerender();
    expect(result.current.position).toBeNull();
    read[field] = false;
    rerender();
    expect(result.current.position?.size).toBe(100n);
  });
});
