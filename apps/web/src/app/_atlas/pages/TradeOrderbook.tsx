'use client';

import { useMemo, useState } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import {
  formatTradingPrice,
  formatTradingPriceInput,
  tradingAmountToNumber,
} from '@/app/trade/trade-page.utils';
import type { PriceFlash, TradeHistoryItem, TradeOrderbook } from './tradeTypes';

type BookView = 'book' | 'trades';

type TradeOrderbookProps = {
  orderbook: TradeOrderbook;
  displayLast: string;
  flash: PriceFlash;
  isMarketStreamConnected: boolean;
  marketStreamStatus: string;
  isLoading: boolean;
  isError: boolean;
  hasLiveOrderbook: boolean;
  onRetry: () => void;
  recentTrades: TradeHistoryItem[];
  isRecentTradesLoading: boolean;
  /** Called with a plain numeric price string when a book row is clicked. */
  onPriceSelect?: (price: string) => void;
};

type DepthRow = {
  price: string;
  size: number;
  /** Running size from the touch (best bid/ask) outward. */
  cumulativeSize: number;
  /** Running notional (price × size) from the touch outward. */
  cumulativeNotional: number;
};

// Cumulative depth from the best price outward — the fill bar and "total"
// column describe how much can be swept up to that level, matching what the
// number is used for (sizing an order), instead of a per-row product.
function buildDepth(levels: Array<[string, string]> | undefined): DepthRow[] {
  const rows: DepthRow[] = [];
  let cumulativeSize = 0;
  let cumulativeNotional = 0;
  for (const [price, size] of levels ?? []) {
    const sizeNumber = tradingAmountToNumber(size);
    cumulativeSize += sizeNumber;
    cumulativeNotional += tradingAmountToNumber(price) * sizeNumber;
    rows.push({ price, size: sizeNumber, cumulativeSize, cumulativeNotional });
  }
  return rows;
}

/**
 * Market-data side panel: order book + recent trades tape, tabbed.
 * Book states are typed, not collapsed:
 *   loading  — first REST poll in flight
 *   error    — REST poll failed and no socket frame covers it
 *   empty    — API answered with an empty book (real, honest emptiness)
 *   data     — rows from the socket stream or REST poll
 */
export function TradeOrderbook({
  orderbook,
  displayLast,
  flash,
  isMarketStreamConnected,
  marketStreamStatus,
  isLoading,
  isError,
  hasLiveOrderbook,
  onRetry,
  recentTrades,
  isRecentTradesLoading,
  onPriceSelect,
}: TradeOrderbookProps) {
  const t = useTranslations('trade.orderbook');
  const [view, setView] = useState<BookView>('book');

  const bidDepth = useMemo(() => buildDepth(orderbook?.bids), [orderbook?.bids]);
  const askDepth = useMemo(() => buildDepth(orderbook?.asks), [orderbook?.asks]);
  const maxCumulative = Math.max(
    bidDepth.at(-1)?.cumulativeSize ?? 0,
    askDepth.at(-1)?.cumulativeSize ?? 0,
  );

  const bestBid = bidDepth[0] ? tradingAmountToNumber(bidDepth[0].price) : null;
  const bestAsk = askDepth[0] ? tradingAmountToNumber(askDepth[0].price) : null;
  const spread =
    bestBid !== null && bestAsk !== null && bestAsk >= bestBid ? bestAsk - bestBid : null;
  const spreadPct =
    spread !== null && bestBid !== null && bestBid > 0
      ? (spread / ((bestAsk! + bestBid) / 2)) * 100
      : null;

  const isEmpty = !isLoading && !isError && bidDepth.length === 0 && askDepth.length === 0;

  const renderRow = (row: DepthRow, sideClass: 'ask' | 'bid') => {
    const width = maxCumulative > 0 ? Math.min(100, (row.cumulativeSize / maxCumulative) * 100) : 0;
    return (
      <button
        key={sideClass + row.price}
        type="button"
        className={`b-row b-row-btn ${sideClass}`}
        onClick={() => onPriceSelect?.(formatTradingPriceInput(row.price))}
        aria-label={t('useAsLimit', { price: formatTradingPrice(row.price) })}
      >
        <div className="fill" style={{ width: width.toFixed(0) + '%' }} />
        <div className={`px ${sideClass === 'ask' ? 'a' : 'b'}`}>{formatTradingPrice(row.price)}</div>
        <div className="sz">{row.size.toFixed(3)}</div>
        <div className="tot">{row.cumulativeNotional.toFixed(0)}</div>
      </button>
    );
  };

  return (
    <div className="book" data-testid="trade-orderbook">
      <h4>
        <span>{t('title')}</span>
        <span className="book-tabs">
          <button
            type="button"
            className={view === 'book' ? 'on' : ''}
            onClick={() => setView('book')}
          >
            {t('tabBook')}
          </button>
          <button
            type="button"
            className={view === 'trades' ? 'on' : ''}
            onClick={() => setView('trades')}
            data-testid="book-tab-trades"
          >
            {t('tabTrades')}
          </button>
        </span>
      </h4>

      {view === 'trades' ? (
        <RecentTradesTape trades={recentTrades} isLoading={isRecentTradesLoading} />
      ) : (
        <>
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '1fr 1fr 1fr',
              padding: '4px 16px 6px',
              fontFamily: 'var(--df)',
              fontWeight: 700,
              fontSize: 9.5,
              letterSpacing: '0.14em',
              color: 'var(--ink-2)',
              textTransform: 'uppercase',
            }}
          >
            <span>{t('price')}</span>
            <span style={{ textAlign: 'right' }}>{t('size')}</span>
            <span style={{ textAlign: 'right' }}>{t('total')}</span>
          </div>
          {isLoading && !orderbook && (
            <div style={{ padding: 14, fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--ink-2)' }}>
              {t('loading')}
            </div>
          )}
          {isError && !orderbook && (
            <div
              data-testid="orderbook-error"
              style={{ padding: 14, fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--ink-2)' }}
            >
              {t('error')}
              <button
                type="button"
                className="src-refresh"
                style={{ marginTop: 8, display: 'inline-flex' }}
                onClick={onRetry}
              >
                ↻
              </button>
            </div>
          )}
          {isEmpty && (
            <div
              data-testid="orderbook-empty"
              style={{ padding: 14, fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--ink-2)', lineHeight: 1.5 }}
            >
              <b style={{ fontFamily: 'var(--df)', display: 'block', marginBottom: 4 }}>{t('emptyTitle')}</b>
              {t('emptyBody')}
            </div>
          )}
          {askDepth
            .slice()
            .reverse()
            .map((row) => renderRow(row, 'ask'))}
          <div className="b-mid">
            <span>{displayLast}</span>
            <span
              className="b-spread"
              data-testid="orderbook-spread"
              title={t('spreadLabel')}
            >
              {spread !== null && spreadPct !== null
                ? `${t('spreadLabel')} ${spread.toFixed(2)} · ${spreadPct.toFixed(3)}%`
                : '—'}
            </span>
            <span
              className="arr"
              style={{ color: flash === 'up' ? 'var(--pos)' : flash === 'down' ? 'var(--neg)' : 'var(--ink-2)' }}
            >
              {flash === 'up' ? '↑' : flash === 'down' ? '↓' : '·'}{' '}
              {isMarketStreamConnected ? t('live') : marketStreamStatus}
            </span>
          </div>
          {bidDepth.map((row) => renderRow(row, 'bid'))}
        </>
      )}

      <div style={{ marginTop: 'auto', padding: '8px 14px' }} className="src-meta">
        <span className="src-chip">
          {view === 'trades' ? t('tradesSource') : 'GET /api/trading/orderbook'}
        </span>
        <span className="src-chip">{hasLiveOrderbook ? t('sourceStream') : t('sourceRest')}</span>
      </div>
    </div>
  );
}

function RecentTradesTape({ trades, isLoading }: { trades: TradeHistoryItem[]; isLoading: boolean }) {
  const t = useTranslations('trade.orderbook');
  const locale = useLocale();
  const timeFormatter = useMemo(
    () => new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
    [locale],
  );

  if (isLoading && trades.length === 0) {
    return (
      <div style={{ padding: 14, fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--ink-2)' }}>
        {t('tradesLoading')}
      </div>
    );
  }

  if (trades.length === 0) {
    return (
      <div
        data-testid="trades-empty"
        style={{ padding: 14, fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--ink-2)', lineHeight: 1.5 }}
      >
        <b style={{ fontFamily: 'var(--df)', display: 'block', marginBottom: 4 }}>{t('noTradesTitle')}</b>
        {t('noTradesBody')}
      </div>
    );
  }

  return (
    <div data-testid="trades-tape">
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: '1fr 1fr 1fr',
          padding: '4px 16px 6px',
          fontFamily: 'var(--df)',
          fontWeight: 700,
          fontSize: 9.5,
          letterSpacing: '0.14em',
          color: 'var(--ink-2)',
          textTransform: 'uppercase',
        }}
      >
        <span>{t('price')}</span>
        <span style={{ textAlign: 'right' }}>{t('size')}</span>
        <span style={{ textAlign: 'right' }}>{t('time')}</span>
      </div>
      {trades.slice(0, 24).map((trade) => (
        <div key={trade.id} className="b-row">
          <div className={`px ${trade.isLong ? 'b' : 'a'}`}>{formatTradingPrice(trade.price)}</div>
          <div className="sz">{tradingAmountToNumber(trade.sizeDelta).toFixed(2)}</div>
          <div className="tot">{timeFormatter.format(new Date(trade.createdAt))}</div>
        </div>
      ))}
    </div>
  );
}
