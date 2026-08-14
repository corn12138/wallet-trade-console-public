'use client';

import { useTranslations } from 'next-intl';
import { formatTradingPrice } from '@/app/trade/trade-page.utils';
import type { PriceFlash, TradeOrderbook } from './tradeTypes';

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
};

/**
 * Order book column. States are typed, not collapsed:
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
}: TradeOrderbookProps) {
  const t = useTranslations('trade.orderbook');
  const askCount = orderbook?.asks?.length ?? 0;
  const bidCount = orderbook?.bids?.length ?? 0;
  const isEmpty = !isLoading && !isError && askCount === 0 && bidCount === 0;

  return (
    <div className="book" data-testid="trade-orderbook">
      <h4>
        {t('title')} <span style={{ opacity: 0.6, fontWeight: 700 }}>0.10</span>
      </h4>
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
      {orderbook?.asks
        ?.slice()
        .reverse()
        .map(([p, s], i) => {
          const sz = Number(s);
          return (
            <div key={'a' + i} className="b-row ask">
              <div className="fill" style={{ width: Math.min(100, sz * 50).toFixed(0) + '%' }} />
              <div className="px a">{formatTradingPrice(p)}</div>
              <div className="sz">{sz.toFixed(3)}</div>
              <div className="tot">{(Number(p) * sz).toFixed(0)}</div>
            </div>
          );
        })}
      <div className="b-mid">
        <span>{displayLast}</span>
        <span
          className="arr"
          style={{ color: flash === 'up' ? 'var(--pos)' : flash === 'down' ? 'var(--neg)' : 'var(--ink-2)' }}
        >
          {flash === 'up' ? '↑' : flash === 'down' ? '↓' : '·'}{' '}
          {isMarketStreamConnected ? t('live') : marketStreamStatus}
        </span>
      </div>
      {orderbook?.bids?.map(([p, s], i) => {
        const sz = Number(s);
        return (
          <div key={'b' + i} className="b-row bid">
            <div className="fill" style={{ width: Math.min(100, sz * 50).toFixed(0) + '%' }} />
            <div className="px b">{formatTradingPrice(p)}</div>
            <div className="sz">{sz.toFixed(3)}</div>
            <div className="tot">{(Number(p) * sz).toFixed(0)}</div>
          </div>
        );
      })}
      <div style={{ marginTop: 'auto', padding: '8px 14px' }} className="src-meta">
        <span className="src-chip">GET /api/trading/orderbook</span>
        <span className="src-chip">{hasLiveOrderbook ? t('sourceStream') : t('sourceRest')}</span>
      </div>
    </div>
  );
}
