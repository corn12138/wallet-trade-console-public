'use client';

import type { ReactNode } from 'react';
import { useTranslations } from 'next-intl';
import type { AppCtx } from '../AppContext';
import { Icon } from '../Icon';
import type { TradeOrderNoticeTone, TradeOrderType } from '@/app/trade/trade-page.utils';
import type { TradeSide } from './tradeTypes';

type TradeTicketNotice = { tone: TradeOrderNoticeTone; message: string } | null;

type TradeTicketProps = {
  app: AppCtx;
  side: TradeSide;
  orderType: TradeOrderType;
  size: string;
  limitPrice: string;
  lev: number;
  balanceDisplay: string;
  displayLast: string;
  liquidationPrice: string;
  fee: number;
  margin: number;
  positionSize: number;
  slippagePercent: string;
  deadlineMinutes: string;
  notice: TradeTicketNotice;
  /** Pre-sign review strip for the collateral approval; null once approved. */
  preflight?: ReactNode;
  canSubmit: boolean;
  isApproving: boolean;
  isOpenPending: boolean;
  isOpenConfirming: boolean;
  setSide: (side: TradeSide) => void;
  setOrderType: (orderType: TradeOrderType) => void;
  setSize: (size: string) => void;
  setLimitPrice: (price: string) => void;
  setLev: (lev: number) => void;
  setSlippagePercent: (value: string) => void;
  setDeadlineMinutes: (value: string) => void;
  handleMax: () => void;
  handleSubmit: () => void;
};

// Order types the venue actually supports: immediate market execution, or
// immediate execution guarded by an acceptable price ("limit" guard). The
// PositionManager contract has no resting-order book, so no stop orders and
// no pending-order placement are offered here.
const ORDER_TYPES: Array<{ value: TradeOrderType; labelKey: 'orderMarket' | 'orderLimitGuard' }> = [
  { value: 'market', labelKey: 'orderMarket' },
  { value: 'limit', labelKey: 'orderLimitGuard' },
];

export function TradeTicket({
  app,
  side,
  orderType,
  size,
  limitPrice,
  lev,
  balanceDisplay,
  displayLast,
  liquidationPrice,
  fee,
  margin,
  positionSize,
  slippagePercent,
  deadlineMinutes,
  notice,
  preflight,
  canSubmit,
  isApproving,
  isOpenPending,
  isOpenConfirming,
  setSide,
  setOrderType,
  setSize,
  setLimitPrice,
  setLev,
  setSlippagePercent,
  setDeadlineMinutes,
  handleMax,
  handleSubmit,
}: TradeTicketProps) {
  const t = useTranslations('trade.ticket');
  const busy = isApproving || isOpenPending || isOpenConfirming;

  return (
    <div className="ticket">
      <div className="seg s2">
        <button className={'' + (side === 'long' ? 'on long' : '')} onClick={() => setSide('long')}>
          {t('openLong')}
        </button>
        <button className={'' + (side === 'short' ? 'on short' : '')} onClick={() => setSide('short')}>
          {t('openShort')}
        </button>
      </div>

      <div className="seg s2">
        {ORDER_TYPES.map((o) => (
          <button
            key={o.value}
            className={orderType === o.value ? 'on' : ''}
            onClick={() => setOrderType(o.value)}
          >
            {t(o.labelKey)}
          </button>
        ))}
      </div>

      <div className="field">
        <div className="l">
          <span>{t('collateral')}</span>
          <span className="right">{t('bal', { balance: balanceDisplay })}</span>
        </div>
        <input
          value={size}
          onChange={(e) => setSize(e.target.value)}
          type="number"
          min="0"
          step="0.01"
          placeholder="0.00"
          aria-label={t('collateral')}
        />
      </div>

      {orderType === 'limit' && (
        <div className="field">
          <div className="l">
            <span>{t('acceptablePrice')}</span>
            <span className="right">{t('usd')}</span>
          </div>
          <input
            value={limitPrice}
            onChange={(e) => setLimitPrice(e.target.value)}
            placeholder={displayLast}
            aria-label={t('acceptablePrice')}
          />
        </div>
      )}

      <div>
        <div className="row between" style={{ marginBottom: 6 }}>
          <span className="eyebrow">{t('leverage')}</span>
          <span className="pill flat" style={{ padding: '3px 9px', fontSize: 11, boxShadow: 'none' }}>
            {lev}×
          </span>
        </div>
        <div className="lev-row">
          {[1, 5, 10, 20, 50].map((L) => (
            <button key={L} className={lev === L ? 'on' : ''} onClick={() => setLev(L)}>
              {L}×
            </button>
          ))}
        </div>
      </div>

      <div className="row gap-6">
        <div className="field" style={{ flex: 1 }}>
          <div className="l">
            <span>{t('slippage')}</span>
            <span className="right">%</span>
          </div>
          <input
            value={slippagePercent}
            onChange={(e) => setSlippagePercent(e.target.value)}
            type="number"
            min="0"
            max="5"
            step="0.1"
            aria-label={t('slippage')}
          />
        </div>
        <div className="field" style={{ flex: 1 }}>
          <div className="l">
            <span>{t('deadline')}</span>
            <span className="right">{t('minutes')}</span>
          </div>
          <input
            value={deadlineMinutes}
            onChange={(e) => setDeadlineMinutes(e.target.value)}
            type="number"
            min="1"
            max="120"
            step="1"
            aria-label={t('deadline')}
          />
        </div>
      </div>

      <div className="meta-row">
        <span>
          {t('liq')} · <b className={lev >= 20 ? 'tone-warn' : ''}>${liquidationPrice}</b>
        </span>
        <span>
          {t('fee')} · <b>${fee.toFixed(2)}</b>
        </span>
      </div>
      <div className="meta-row">
        <span>
          {t('margin')} · <b>${margin.toFixed(2)}</b>
        </span>
        <span>
          {t('position')} · <b>${positionSize.toFixed(2)}</b>
        </span>
      </div>

      <div className="row gap-6">
        <button className="btn btn-xs" onClick={handleMax} style={{ flex: 1 }}>
          <Icon name="plus" size={12} /> {t('useBalance')}
        </button>
      </div>

      {notice && (
        <div
          role="note"
          className="meta-row"
          style={{
            background:
              notice.tone === 'error' ? 'var(--o)' : notice.tone === 'warning' ? 'var(--y)' : 'var(--bg-2)',
            color: notice.tone === 'error' ? '#fff' : 'var(--ink)',
            border: '2px solid var(--ink)',
            borderRadius: 10,
            padding: '8px 12px',
            lineHeight: 1.4,
          }}
        >
          <span>{notice.message}</span>
        </div>
      )}

      {/* Pre-sign review of the collateral approval, rendered where the user
          is about to sign. Advisory: it never gates the CTA below. */}
      {preflight}

      {app.walletState !== 'connected' ? (
        <button className="btn btn-y" style={{ width: '100%' }} onClick={app.openConnect}>
          {t('connectToTrade')}
        </button>
      ) : (
        <button
          className={'btn ' + (side === 'long' ? 'btn-g' : 'btn-o')}
          style={{ width: '100%' }}
          onClick={handleSubmit}
          disabled={busy || !canSubmit}
        >
          {isApproving ? (
            <>
              <span className="spinner" /> {t('approvingUsdc')}
            </>
          ) : isOpenPending || isOpenConfirming ? (
            <>
              <span className="spinner" /> {t('submitting')}
            </>
          ) : side === 'long' ? (
            t('openLongCta', { lev })
          ) : (
            t('openShortCta', { lev })
          )}
        </button>
      )}

      {lev >= 20 && (
        <div
          className="meta-row"
          style={{
            background: 'var(--y)',
            border: '2px solid var(--ink)',
            borderRadius: 10,
            padding: '8px 12px',
            color: 'var(--ink)',
          }}
        >
          <span>
            <Icon name="warn" size={14} style={{ verticalAlign: '-2px' }} /> {t('highLeverage')}
          </span>
        </div>
      )}
    </div>
  );
}
