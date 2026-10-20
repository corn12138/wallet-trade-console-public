import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { expect, it, vi } from 'vitest';
import { Web3Provider } from './provider';
import { useAuth } from './auth-provider';
import { wagmiConfig } from './config';

vi.mock('./config', async () => {
  const { createConfig, http } = await import('wagmi');
  const { mock } = await import('wagmi/connectors');
  const { sepolia } = await import('viem/chains');
  return {
    supportedChains: [sepolia],
    wagmiConfig: createConfig({
      chains: [sepolia], ssr: true,
      connectors: [mock({ accounts: ['0x000000000000000000000000000000000000dead'], features: { reconnect: true, defaultConnected: true } })],
      transports: { [sepolia.id]: http('http://127.0.0.1:1') },
    }),
  };
});
vi.mock('./contracts', () => ({ syncContractAddresses: vi.fn() }));

it('keeps SIWE through the real Wagmi SSR hydrate/reconnect lifecycle and clears it on final disconnect', async () => {
  const address = '0x000000000000000000000000000000000000dead';
  const token = `${btoa(JSON.stringify({ alg: 'HS256' }))}.${btoa(JSON.stringify({ type: 'web3', sub: address, chainId: 11155111, exp: 4070908800 }))}.test-signature`;
  localStorage.setItem('web3_auth_token', token);
  let allowReconnect!: (value: boolean) => void;
  const reconnect = new Promise<boolean>((resolve) => { allowReconnect = resolve; });
  const authorized = vi.spyOn(wagmiConfig.connectors[0], 'isAuthorized').mockReturnValueOnce(reconnect);
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ address, chainId: 11155111 }), { status: 200 }));
  vi.stubGlobal('fetch', fetch);
  try {
    // Use the production Web3Provider and real wagmi hooks so the child-before-
    // parent effect order exercises the disconnected SSR hydration regression.
    const { result } = renderHook(useAuth, {
      wrapper: ({ children }: { children: ReactNode }) => <Web3Provider>{children}</Web3Provider>,
    });
    await waitFor(() => expect(authorized).toHaveBeenCalled());
    expect(result.current.isRestoring).toBe(true);
    expect(result.current.token).toBeNull();
    expect(localStorage.getItem('web3_auth_token')).toBe(token);
    expect(fetch).not.toHaveBeenCalled();
    await act(async () => { allowReconnect(true); });
    await waitFor(() => expect(result.current.isAuthenticated).toBe(true));
    expect(result.current.authenticatedAddress).toBe(address);
    expect(result.current.authenticatedChainId).toBe(11155111);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0][0])).toMatch(/\/auth\/me$/);
    expect(localStorage.getItem('web3_auth_token')).toBe(token);
    const generation = result.current.sessionGeneration;
    act(() => { wagmiConfig.connectors[0].emitter.emit('disconnect'); });
    await waitFor(() => expect(result.current.connectorStatus).toBe('disconnected'));
    expect(result.current.isAuthenticated).toBe(false);
    expect(result.current.sessionGeneration).toBeGreaterThan(generation);
    expect(localStorage.getItem('web3_auth_token')).toBeNull();
  } finally {
    authorized.mockRestore();
    vi.unstubAllGlobals();
    localStorage.clear();
  }
});
