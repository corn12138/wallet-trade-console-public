'use client';

import { useMemo } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import { formatUnits } from 'viem';
import {
  calculatePnl,
  formatTradingPrice,
  formatTradingUsdAmount,
  formatUsd30,
} from '@/app/trade/trade-page.utils';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
import { Empty, TabBar } from '../Common';
import type { TabKey, TradeHistoryItem, TradeOrder, TradePosition } from './tradeTypes';

type TradeTablesProps = {
  tab: TabKey;
  positions: TradePosition[];
  orders: TradeOrder[];
  history: TradeHistoryItem[];
  currentPrice: bigint | undefined;
  chainId: number;
  isWalletConnected: boolean;
  /** SIWE session covers the connected wallet — the orders/history queries run. */
  isActivityAuthorized: boolean;
  isPositionsLoading: boolean;
  isOrdersLoading: boolean;
  isHistoryLoading: boolean;
  isClosePending: boolean;
  isCloseConfirming: boolean;
  isMarketStreamConnected: boolean;
  marketStreamStatus: string;
  setTab: (tab: TabKey) => void;
  onClosePosition: (position: TradePosition) => void;
  onConnect: () => void;
};

export function TradeTables({
  tab,
  positions,
  orders,
  history,
  currentPrice,
  chainId,
  isWalletConnected,
  isActivityAuthorized,
  isPositionsLoading,
  isOrdersLoading,
  isHistoryLoading,
  isClosePending,
  isCloseConfirming,
  isMarketStreamConnected,
  marketStreamStatus,
  setTab,
  onClosePosition,
  onConnect,
}: TradeTablesProps) {
  const t = useTranslations('trade.tables');

  // Orders/history need a SIWE session; positions only need a wallet. Without
  // either, the queries never ran — so say that, not "no positions" (a claim
  // about data that was never fetched).
  const activityGate = !isActivityAuthorized;
  const positionsGate = !isWalletConnected;

  const renderGate = () => (
    <div style={{ padding: 24 }} data-testid="tables-signin-gate">
      <Empty
        body={
          <>
            {t('signInPrompt')}
            <div style={{ marginTop: 12 }}>
              <button className="btn btn-xs btn-y" onClick={onConnect}>
                {t('connectCta')}
              </button>
            </div>
          </>
        }
      />
    </div>
  );

  const renderLoading = () => (
    <div
      data-testid="tables-loading"
      style={{ padding: 24, fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--ink-2)' }}
    >
      {t('loading')}
    </div>
  );

  return (
    <div className="tbl" style={{ boxShadow: 'none', borderRadius: 14 }}>
      <div
        className="row between"
        style={{ padding: '10px 16px', borderBottom: '2px solid var(--ink)', background: 'var(--bg-2)' }}
      >
        <div className="row gap-6">
          <TabBar
            tabs={[
              { key: 'positions' as TabKey, label: `${t('positions')} · ${positions.length}` },
              { key: 'orders' as TabKey, label: `${t('orders')} · ${orders.length}` },
              { key: 'history' as TabKey, label: t('history') },
            ]}
            value={tab}
            onChange={setTab}
          />
        </div>
        <span className="eyebrow">
          {t('stream')} <b style={{ color: isMarketStreamConnected ? 'var(--pos)' : 'var(--warn)' }}>{marketStreamStatus}</b>
        </span>
      </div>

      {tab === 'positions' &&
        (positionsGate ? (
          renderGate()
        ) : isPositionsLoading && positions.length === 0 ? (
          renderLoading()
        ) : (
          <PositionsTable
            positions={positions}
            currentPrice={currentPrice}
            onClosePosition={onClosePosition}
            isClosePending={isClosePending}
            isCloseConfirming={isCloseConfirming}
          />
        ))}
      {tab === 'orders' &&
        (activityGate ? (
          renderGate()
        ) : isOrdersLoading && orders.length === 0 ? (
          renderLoading()
        ) : (
          <OrdersTable orders={orders} />
        ))}
      {tab === 'history' &&
        (activityGate ? (
          renderGate()
        ) : isHistoryLoading && history.length === 0 ? (
          renderLoading()
        ) : (
          <HistoryTable history={history} chainId={chainId} />
        ))}
    </div>
  );
}

function PositionsTable({
  positions,
  currentPrice,
  onClosePosition,
  isClosePending,
  isCloseConfirming,
}: {
  positions: TradePosition[];
  currentPrice: bigint | undefined;
  onClosePosition: (position: TradePosition) => void;
  isClosePending: boolean;
  isCloseConfirming: boolean;
}) {
  const t = useTranslations('trade.tables');
  if (!positions.length) {
    return (
      <div style={{ padding: 24 }}>
        <Empty body={t('noPositions')} />
      </div>
    );
  }

  return (
    <div>
      <div className="tbl-head" style={{ gridTemplateColumns: '1.2fr 0.7fr 0.5fr 1fr 1fr 1fr 1.2fr auto', padding: '10px 16px' }}>
        <span>{t('symbol')}</span>
        <span>{t('side')}</span>
        <span>{t('leverage')}</span>
        <span>{t('size')}</span>
        <span>{t('entry')}</span>
        <span>{t('mark')}</span>
        <span style={{ textAlign: 'right' }}>{t('pnl')}</span>
        <span />
      </div>
      {positions.map((p, i) => {
        const pnl = currentPrice ? calculatePnl(p.size, p.averagePrice, currentPrice, p.isLong) : { hasProfit: false, delta: 0n };
        const pnlString = pnl.delta === 0n ? '$0.00' : `${pnl.hasProfit ? '+' : '−'} $${formatUsd30(pnl.delta)}`;
        // Leverage and return-on-equity both derive from the on-chain
        // collateral: lev = size/collateral, ROE = pnl/collateral.
        const collateralNumber = Number(formatUnits(p.collateral, 30));
        const leverage = collateralNumber > 0 ? Number(formatUnits(p.size, 30)) / collateralNumber : null;
        const roePct =
          collateralNumber > 0 && pnl.delta !== 0n
            ? (Number(formatUnits(pnl.delta, 30)) / collateralNumber) * 100
            : null;
        return (
          <div
            key={i}
            className="tbl-row"
            style={{ gridTemplateColumns: '1.2fr 0.7fr 0.5fr 1fr 1fr 1fr 1.2fr auto', padding: '10px 16px' }}
          >
            <div className="sym">
              <span className="b">{p.market.symbol[0]}</span>
              {p.market.symbol}
            </div>
            <div>
              <span className={'side ' + (p.isLong ? 'long' : 'short')}>{p.isLong ? t('long') : t('short')}</span>
            </div>
            <div className="mono">{leverage !== null ? `${leverage.toFixed(1)}×` : '—'}</div>
            <div className="mono">${formatUsd30(p.size)}</div>
            <div className="mono">${formatUsd30(p.averagePrice)}</div>
            <div className="mono">{currentPrice ? '$' + formatUsd30(currentPrice) : '—'}</div>
            <div
              className="mono"
              style={{
                textAlign: 'right',
                color: pnl.hasProfit ? 'var(--pos)' : pnl.delta === 0n ? 'var(--ink-2)' : 'var(--neg)',
              }}
            >
              {pnlString}
              {roePct !== null && (
                <span style={{ opacity: 0.75, marginLeft: 4, fontSize: 11 }}>
                  ({pnl.hasProfit ? '+' : '−'}
                  {Math.abs(roePct).toFixed(1)}%)
                </span>
              )}
            </div>
            <button
              className="btn btn-xs btn-o"
              onClick={() => onClosePosition(p)}
              disabled={isClosePending || isCloseConfirming}
            >
              {t('close')}
            </button>
          </div>
        );
      })}
    </div>
  );
}

// Orders are read-only: rows come from the indexer/order book seed and the
// venue has no cancel endpoint or on-chain cancel flow, so no cancel action
// is rendered (a cancel button here used to be a toast-only placeholder).
function OrdersTable({ orders }: { orders: TradeOrder[] }) {
  const t = useTranslations('trade.tables');
  if (!orders.length) {
    return (
      <div style={{ padding: 24 }}>
        <Empty body={t('noOrders')} />
      </div>
    );
  }

  return (
    <div>
      <div className="tbl-head" style={{ gridTemplateColumns: '1fr 0.7fr 0.7fr 0.9fr 0.9fr auto', padding: '10px 16px' }}>
        <span>{t('token')}</span>
        <span>{t('type')}</span>
        <span>{t('side')}</span>
        <span>{t('size')}</span>
        <span>{t('trigger')}</span>
        <span>{t('status')}</span>
      </div>
      {orders.map((o) => (
        <div
          key={o.id}
          className="tbl-row"
          style={{ gridTemplateColumns: '1fr 0.7fr 0.7fr 0.9fr 0.9fr auto', padding: '10px 16px' }}
        >
          <div className="mono" style={{ fontSize: 12 }}>
            {o.token.slice(0, 6)}…{o.token.slice(-4)}
          </div>
          <div style={{ textTransform: 'uppercase', fontFamily: 'var(--df)', fontWeight: 700, fontSize: 11 }}>{o.orderType}</div>
          <div>
            <span className={'side ' + (o.isLong ? 'long' : 'short')}>{o.isLong ? t('long') : t('short')}</span>
          </div>
          <div className="mono">${formatTradingUsdAmount(o.sizeDelta)}</div>
          <div className="mono">{o.triggerPrice ? '$' + formatTradingPrice(o.triggerPrice) : '—'}</div>
          <div style={{ textTransform: 'lowercase', fontFamily: 'var(--df)', fontWeight: 700, fontSize: 11, color: 'var(--ink-2)' }}>
            {o.status}
          </div>
        </div>
      ))}
    </div>
  );
}

function HistoryTable({ history, chainId }: { history: TradeHistoryItem[]; chainId: number }) {
  const t = useTranslations('trade.tables');
  const locale = useLocale();
  const whenFormatter = useMemo(
    () =>
      new Intl.DateTimeFormat(locale, {
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      }),
    [locale],
  );

  if (!history.length) {
    return (
      <div style={{ padding: 24 }}>
        <Empty body={t('noHistory')} />
      </div>
    );
  }

  return (
    <div>
      <div
        className="tbl-head"
        style={{ gridTemplateColumns: 'auto 1fr 0.6fr 0.7fr 0.9fr 0.9fr auto', padding: '10px 16px' }}
      >
        <span>{t('when')}</span>
        <span>{t('token')}</span>
        <span>{t('side')}</span>
        <span>{t('type')}</span>
        <span>{t('size')}</span>
        <span>{t('price')}</span>
        <span style={{ textAlign: 'right' }}>{t('pnl')}</span>
      </div>
      {history.slice(0, 30).map((h) => {
        const pnlNumber = h.pnl ? Number(formatUnits(BigInt(h.pnl), 30)) : null;
        const explorerUrl = h.txHash ? buildTransactionExplorerUrl(h.txHash, chainId) : null;
        const when = whenFormatter.format(new Date(h.createdAt));
        return (
          <div
            key={h.id}
            className="tbl-row"
            style={{ gridTemplateColumns: 'auto 1fr 0.6fr 0.7fr 0.9fr 0.9fr auto', padding: '10px 16px' }}
          >
            <div className="mono" style={{ color: 'var(--ink-2)' }}>
              {explorerUrl ? (
                <a
                  href={explorerUrl}
                  target="_blank"
                  rel="noreferrer"
                  style={{ color: 'inherit', textDecoration: 'underline dotted' }}
                  aria-label={t('viewTx', { hash: h.txHash })}
                >
                  {when} ↗
                </a>
              ) : (
                when
              )}
            </div>
            <div className="mono" style={{ fontSize: 12 }}>
              {h.token.slice(0, 6)}…{h.token.slice(-4)}
            </div>
            <div>
              <span className={'side ' + (h.isLong ? 'long' : 'short')}>{h.isLong ? t('long') : t('short')}</span>
            </div>
            <div style={{ textTransform: 'uppercase', fontFamily: 'var(--df)', fontWeight: 700, fontSize: 11 }}>{h.tradeType}</div>
            <div className="mono">${formatTradingUsdAmount(h.sizeDelta)}</div>
            <div className="mono">${formatTradingPrice(h.price)}</div>
            <div
              className="mono"
              style={{
                textAlign: 'right',
                color: pnlNumber === null ? 'var(--ink-2)' : pnlNumber >= 0 ? 'var(--pos)' : 'var(--neg)',
              }}
            >
              {pnlNumber === null ? '—' : `${pnlNumber >= 0 ? '+' : '−'} $${Math.abs(pnlNumber).toFixed(2)}`}
            </div>
          </div>
        );
      })}
    </div>
  );
}
