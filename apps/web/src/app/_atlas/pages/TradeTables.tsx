'use client';

import { useMemo } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import { formatUnits } from 'viem';
import {
  calculatePnl,
  formatTradingPrice,
  formatTradingUsdAmount,
  formatUsd30,
  tradingAmountToNumber,
} from '@/app/trade/trade-page.utils';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
import { Empty, TabBar } from '../Common';
import type { PortfolioPositionRow } from '@/hooks/trading.types';
import type { TabKey, TradeHistoryItem, TradeOrder, TradePosition } from './tradeTypes';

type TradeTablesProps = {
  tab: TabKey;
  positions: PortfolioPositionRow[];
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
  /** Point the terminal at another market (indexed rows manage from there). */
  onSelectMarket: (symbol: string) => void;
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
  onSelectMarket,
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
            onSelectMarket={onSelectMarket}
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

const POSITIONS_GRID = '1.2fr 0.7fr 0.5fr 1fr 1fr 1fr 1.2fr auto';

type PositionCells = {
  symbol: string | null;
  isLong: boolean;
  leverage: number | null;
  size: string;
  entry: string;
  mark: string;
  pnlText: string;
  pnlTone: 'pos' | 'neg' | 'flat';
  roePct: number | null;
  source: 'chain' | 'indexed';
};

// Both row kinds normalize into the same cells; only the trailing action
// differs (close for chain rows, switch-to-market for indexed ones).
function chainRowCells(p: TradePosition, currentPrice: bigint | undefined): PositionCells {
  const pnl = currentPrice
    ? calculatePnl(p.size, p.averagePrice, currentPrice, p.isLong)
    : { hasProfit: false, delta: 0n };
  const collateralNumber = Number(formatUnits(p.collateral, 30));
  const pnlNumber = Number(formatUnits(pnl.delta, 30)) * (pnl.hasProfit ? 1 : -1);
  return {
    symbol: p.market.symbol,
    isLong: p.isLong,
    leverage: collateralNumber > 0 ? Number(formatUnits(p.size, 30)) / collateralNumber : null,
    size: formatUsd30(p.size),
    entry: formatUsd30(p.averagePrice),
    mark: currentPrice ? formatUsd30(currentPrice) : '—',
    pnlText: pnl.delta === 0n ? '$0.00' : `${pnl.hasProfit ? '+' : '−'} $${formatUsd30(pnl.delta)}`,
    pnlTone: pnl.delta === 0n ? 'flat' : pnl.hasProfit ? 'pos' : 'neg',
    roePct: collateralNumber > 0 && pnl.delta !== 0n ? (Math.abs(pnlNumber) / collateralNumber) * 100 : null,
    source: 'chain',
  };
}

function indexedRowCells(
  row: Extract<PortfolioPositionRow, { kind: 'indexed' }>,
): PositionCells {
  const p = row.position;
  const collateralNumber = tradingAmountToNumber(p.collateral);
  const sizeNumber = tradingAmountToNumber(p.size);
  const pnlNumber = tradingAmountToNumber(p.pnl);
  return {
    symbol: row.marketSymbol,
    isLong: p.isLong,
    leverage: collateralNumber > 0 ? sizeNumber / collateralNumber : null,
    size: formatTradingUsdAmount(p.size),
    entry: formatTradingPrice(p.entryPrice),
    mark: formatTradingPrice(p.markPrice),
    pnlText: pnlNumber === 0 ? '$0.00' : `${pnlNumber > 0 ? '+' : '−'} $${Math.abs(pnlNumber).toFixed(2)}`,
    pnlTone: pnlNumber === 0 ? 'flat' : pnlNumber > 0 ? 'pos' : 'neg',
    roePct: collateralNumber > 0 && pnlNumber !== 0 ? (Math.abs(pnlNumber) / collateralNumber) * 100 : null,
    source: 'indexed',
  };
}

function PositionsTable({
  positions,
  currentPrice,
  onClosePosition,
  onSelectMarket,
  isClosePending,
  isCloseConfirming,
}: {
  positions: PortfolioPositionRow[];
  currentPrice: bigint | undefined;
  onClosePosition: (position: TradePosition) => void;
  onSelectMarket: (symbol: string) => void;
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
    <div className="tbl-scroll">
      <div className="tbl-head" style={{ gridTemplateColumns: POSITIONS_GRID, padding: '10px 16px' }}>
        <span>{t('symbol')}</span>
        <span>{t('side')}</span>
        <span>{t('leverage')}</span>
        <span>{t('size')}</span>
        <span>{t('entry')}</span>
        <span>{t('mark')}</span>
        <span style={{ textAlign: 'right' }}>{t('pnl')}</span>
        <span />
      </div>
      {positions.map((row) => {
        const cells = row.kind === 'chain' ? chainRowCells(row.position, currentPrice) : indexedRowCells(row);
        const key = row.kind === 'chain'
          ? `chain-${row.position.market.symbol}-${row.position.isLong ? 'l' : 's'}`
          : `indexed-${row.position.id}`;
        return (
          <div
            key={key}
            className="tbl-row"
            data-testid={`position-row-${cells.source}`}
            style={{ gridTemplateColumns: POSITIONS_GRID, padding: '10px 16px' }}
          >
            <div className="sym">
              <span className="b">{cells.symbol ? cells.symbol[0] : '?'}</span>
              <span>
                {cells.symbol ?? t('unknownMarket')}
                <span
                  className="mono"
                  style={{ display: 'block', fontSize: 9, opacity: 0.6, textTransform: 'uppercase', letterSpacing: '0.1em' }}
                >
                  {cells.source === 'chain' ? t('sourceChain') : t('sourceIndexed')}
                </span>
              </span>
            </div>
            <div>
              <span className={'side ' + (cells.isLong ? 'long' : 'short')}>{cells.isLong ? t('long') : t('short')}</span>
            </div>
            <div className="mono">{cells.leverage !== null ? `${cells.leverage.toFixed(1)}×` : '—'}</div>
            <div className="mono">${cells.size}</div>
            <div className="mono">${cells.entry}</div>
            <div className="mono">{cells.mark === '—' ? '—' : `$${cells.mark}`}</div>
            <div
              className="mono"
              style={{
                textAlign: 'right',
                color: cells.pnlTone === 'pos' ? 'var(--pos)' : cells.pnlTone === 'flat' ? 'var(--ink-2)' : 'var(--neg)',
              }}
            >
              {cells.pnlText}
              {cells.roePct !== null && (
                <span style={{ opacity: 0.75, marginLeft: 4, fontSize: 11 }}>
                  ({cells.pnlTone === 'pos' ? '+' : '−'}
                  {cells.roePct.toFixed(1)}%)
                </span>
              )}
            </div>
            {row.kind === 'chain' ? (
              <button
                className="btn btn-xs btn-o"
                onClick={() => onClosePosition(row.position)}
                disabled={isClosePending || isCloseConfirming}
              >
                {t('close')}
              </button>
            ) : row.marketSymbol ? (
              <button
                className="btn btn-xs btn-y"
                onClick={() => onSelectMarket(row.marketSymbol!)}
                aria-label={t('switchToAria', { symbol: row.marketSymbol })}
              >
                {t('switchTo')}
              </button>
            ) : (
              <span />
            )}
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
    <div className="tbl-scroll">
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
    <div className="tbl-scroll">
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
