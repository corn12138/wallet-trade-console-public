import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  getTradingMarketTrades,
  getTradingMarketsByChain,
  getTradingOrderbook,
  getTradingHistory,
  getTradingOrders,
  getTradingStats,
} from './trading';

const okResponse = (payload: unknown) => ({
  ok: true,
  json: async () => payload,
}) as Response;

describe('trading API client', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it('accepts Go direct-array trading market responses', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(okResponse([
      {
        symbol: 'ETH-USD',
        chainId: 11155111,
        indexToken: '0x1111111111111111111111111111111111111111',
        collateralToken: '0x2222222222222222222222222222222222222222',
        longOpenInterest: '0',
        shortOpenInterest: '0',
        fundingRate: '0',
        volume24h: '0',
      },
    ]));

    await expect(getTradingMarketsByChain(11155111)).resolves.toEqual([
      expect.objectContaining({ symbol: 'ETH-USD' }),
    ]);
  });

  it('defensively unwraps a legacy {code,message,data} response envelope', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(okResponse({
      code: 0,
      message: '操作成功',
      data: [
        {
          symbol: 'BTC-USD',
          chainId: 11155111,
          indexToken: '0x3333333333333333333333333333333333333333',
          collateralToken: '0x4444444444444444444444444444444444444444',
          longOpenInterest: '0',
          shortOpenInterest: '0',
          fundingRate: '0',
          volume24h: '0',
        },
      ],
    }));

    await expect(getTradingMarketsByChain(11155111)).resolves.toEqual([
      expect.objectContaining({ symbol: 'BTC-USD' }),
    ]);
  });

  it('unwraps non-market trading endpoint envelopes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(okResponse({ code: 0, data: { totalVolume: '1', totalOpenInterest: '2' } }))
      .mockResolvedValueOnce(okResponse({ code: 0, data: { bids: [], asks: [] } }))
      .mockResolvedValueOnce(okResponse({ code: 0, data: [] }));

    await expect(getTradingStats(11155111)).resolves.toEqual({ totalVolume: '1', totalOpenInterest: '2' });
    await expect(getTradingOrderbook('ETH-USD', 11155111)).resolves.toEqual({ bids: [], asks: [] });
    await expect(getTradingMarketTrades('ETH-USD', 11155111)).resolves.toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('sends the SIWE bearer for wallet-scoped orders and history', async () => {
    window.localStorage.setItem('web3_auth_token', 'siwe-session-token');
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(okResponse([]))
      .mockResolvedValueOnce(okResponse([]));

    await getTradingOrders('0x1111111111111111111111111111111111111111', 'ETH-USD', 11155111);
    await getTradingHistory('0x1111111111111111111111111111111111111111', 'ETH-USD', 11155111);

    for (const [, init] of fetchMock.mock.calls) {
      expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer siwe-session-token');
    }
  });
});
