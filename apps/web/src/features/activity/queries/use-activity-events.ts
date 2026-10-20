'use client';

import { useQuery } from '@tanstack/react-query';
import { getUserEvents, type Web3Event } from '@/lib/api/events';

export interface ActivityScope {
  address?: string;
  chainId: number;
  sessionGeneration: number;
  isRestoring: boolean;
}

const emptyEvents: Web3Event[] = [];

export function useActivityEvents(scope: ActivityScope) {
  const address = scope.address?.toLowerCase();
  const enabled = !scope.isRestoring && Boolean(address && /^0x[\da-f]{40}$/.test(address)) &&
    Number.isSafeInteger(scope.chainId) && scope.chainId > 0;
  const query = useQuery({
    // These are public chain facts scoped to a selected wallet, never campaign
    // participation. A→B→A must create a fresh session query, even for the same A.
    queryKey: ['transaction-activity', 'chain', 'wallet-events', address, scope.chainId, scope.sessionGeneration],
    queryFn: ({ signal }) => getUserEvents(address!, scope.chainId, 100, signal),
    enabled,
    refetchInterval: 20_000,
    refetchOnWindowFocus: true,
    refetchOnReconnect: true,
  });
  return {
    ...query,
    data: enabled ? query.data ?? emptyEvents : emptyEvents,
    isLoading: scope.isRestoring || (enabled && query.isLoading),
    isError: enabled && query.isError,
    error: enabled ? query.error : null,
    // Query.refetch deliberately bypasses enabled; the public facade must not
    // let a toolbar click query a disconnected or half-restored identity.
    refetch: () => enabled ? query.refetch() : Promise.resolve(undefined),
  };
}
