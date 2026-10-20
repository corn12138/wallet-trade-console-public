'use client';

import { useEffect, useMemo, useRef } from 'react';
import { useQuery, useQueryClient, type UseQueryResult } from '@tanstack/react-query';
import { useAccount, useChainId } from 'wagmi';
import { useAuth } from '@/lib/web3/auth-provider';
import {
  getPortfolioAssets, getPortfolioSummary, PortfolioApiError,
  type AtlasPortfolioAssetsResponse, type AtlasPortfolioSummary,
} from '@/lib/api/portfolio';

export interface PortfolioQueryView<T> {
  data: T | undefined;
  isLoading: boolean;
  isFetching: boolean;
  isError: boolean;
  error: PortfolioApiError | null;
  /** Same identity, previously filtered data retained after an ordinary refresh failure. */
  isStale: boolean;
  dataUpdatedAt: number;
  refetch: () => Promise<unknown>;
}

export interface UsePortfolioDataOptions {
  targetAddress?: string;
  chainId?: number;
  includeSummary?: boolean;
}

function privateDataMustHide(error: unknown): error is PortfolioApiError {
  const failure = error as PortfolioApiError | null;
  return Boolean(failure && (failure.status === 401 || failure.status === 403
    || failure.code === 'PORTFOLIO_VISIBILITY_UNAVAILABLE'));
}

function view<T>(query: UseQueryResult<T, PortfolioApiError>, enabled: boolean, privacyError: PortfolioApiError | null): PortfolioQueryView<T> {
  const data = enabled && !privacyError ? query.data : undefined;
  const error = enabled ? privacyError ?? query.error : null;
  return { data, error, isLoading: enabled && !privacyError && query.isLoading,
    isFetching: enabled && query.isFetching, isError: Boolean(error),
    isStale: Boolean(data && error), dataUpdatedAt: data ? query.dataUpdatedAt : 0,
    refetch: () => enabled ? query.refetch() : Promise.resolve(),
  };
}

export function usePortfolioData({ targetAddress, chainId, includeSummary = false }: UsePortfolioDataOptions) {
  const { address, isConnected } = useAccount();
  const walletChainId = useChainId();
  const { isAuthenticated, authenticatedAddress } = useAuth();
  const queryClient = useQueryClient();
  const target = targetAddress?.toLowerCase() ?? '';
  const connected = address?.toLowerCase() ?? '';
  const viewer = authenticatedAddress?.toLowerCase() ?? '';
  const accessState = !isConnected || !connected ? 'disconnected'
    : target && target !== connected ? 'other-account'
      : !isAuthenticated || !viewer || viewer !== connected || !target ? 'signin-required' : 'ready';
  const isAuthorized = accessState === 'ready';
  const scope = chainId === undefined ? 'cross-chain' : 'chain';
  // The wallet chain remains an identity boundary even for Profile's cross-chain
  // inventory. Bearer credentials never become query/cache identifiers.
  const queryKey = useMemo(() => ['private-portfolio', 1, isAuthorized ? viewer : null, target,
    chainId ?? 'all-chains', walletChainId] as const, [isAuthorized, viewer, target, chainId, walletChainId]);
  const assetsQuery = useQuery<AtlasPortfolioAssetsResponse, PortfolioApiError>({
    queryKey: [...queryKey, 'assets'], queryFn: ({ signal }) => getPortfolioAssets(target, chainId, signal),
    enabled: isAuthorized, refetchInterval: 60_000, retry: false,
  });
  const summaryQuery = useQuery<AtlasPortfolioSummary, PortfolioApiError>({
    queryKey: [...queryKey, 'summary'], queryFn: ({ signal }) => getPortfolioSummary(target, chainId, signal),
    enabled: isAuthorized && includeSummary, refetchInterval: 60_000, retry: false,
  });

  useEffect(() => () => {
    // Cancel before removal so a request started for the previous viewer cannot
    // repopulate that private cache after logout, chain changes or navigation.
    void queryClient.cancelQueries({ queryKey });
    queryClient.removeQueries({ queryKey });
  }, [queryClient, queryKey]);

  const privacyLock = useRef<{ key: typeof queryKey; assets: PortfolioApiError | null; summary: PortfolioApiError | null }>({
    key: queryKey, assets: null, summary: null,
  });
  if (privacyLock.current.key !== queryKey) privacyLock.current = { key: queryKey, assets: null, summary: null };
  // A subsequent network error is not evidence that authorization/visibility
  // recovered. Keep old private data hidden until that endpoint succeeds.
  if (privateDataMustHide(assetsQuery.error)) privacyLock.current.assets = assetsQuery.error;
  else if (assetsQuery.status === 'success') privacyLock.current.assets = null;
  if (includeSummary && privateDataMustHide(summaryQuery.error)) privacyLock.current.summary = summaryQuery.error;
  else if (!includeSummary || summaryQuery.status === 'success') privacyLock.current.summary = null;
  const privacyError = privacyLock.current.assets ?? privacyLock.current.summary;
  const assets = view(assetsQuery, isAuthorized, privacyError);
  const summary = view(summaryQuery, isAuthorized && includeSummary, privacyError);
  return { isAuthorized, accessState, scope, assetsQuery: assets, summaryQuery: summary,
    refetchAll: () => Promise.all([assets.refetch(), summary.refetch()]) };
}
