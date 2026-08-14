'use client';

import { useTranslations } from 'next-intl';
import { Icon } from '../Icon';
import { formatTradingCompactUsdAmount, formatTradingPercentRate } from '@/app/trade/trade-page.utils';
import type { TradeMarket } from './tradeTypes';

type TradeMarketRailProps = {
  markets: TradeMarket[];
  marketSearch: string;
  selectedMarketSymbol: string | null;
  setMarketSearch: (value: string) => void;
  setSelectedMarketSymbol: (value: string) => void;
};

export function TradeMarketRail({
  markets,
  marketSearch,
  selectedMarketSymbol,
  setMarketSearch,
  setSelectedMarketSymbol,
}: TradeMarketRailProps) {
  const t = useTranslations('trade.rail');
  return (
    <aside className="term-rail" style={{ padding: 0, display: 'flex', flexDirection: 'column' }}>
      <div style={{ padding: 12 }}>
        <div className="input-row" style={{ padding: '8px 10px', borderWidth: 2 }}>
          <Icon name="search" size={14} />
          <input
            value={marketSearch}
            onChange={(e) => setMarketSearch(e.target.value)}
            placeholder={t('searchPlaceholder')}
            style={{ fontSize: 12 }}
          />
        </div>
      </div>
      <div
        style={{
          padding: '6px 14px',
          display: 'grid',
          gridTemplateColumns: '1fr auto',
          fontFamily: 'var(--df)',
          fontWeight: 700,
          fontSize: 10,
          letterSpacing: '0.14em',
          color: 'var(--ink-2)',
          textTransform: 'uppercase',
        }}
      >
        <span>{t('market')}</span>
        <span>{t('funding')}</span>
      </div>
      <div style={{ flex: 1, overflow: 'auto', padding: '0 8px 8px' }}>
        {markets.length === 0 && (
          <div style={{ padding: 14, fontSize: 12, color: 'var(--ink-2)', fontFamily: 'var(--mf)', lineHeight: 1.5 }}>
            <b style={{ fontFamily: 'var(--df)', display: 'block', marginBottom: 4 }}>{t('noMarketsTitle')}</b>
            {t('noMarketsBody')}
          </div>
        )}
        {markets.map((m) => {
          const active = m.symbol === selectedMarketSymbol;
          const fundingPct = Number(formatTradingPercentRate(m.fundingRate));
          return (
            <button
              key={m.symbol}
              onClick={() => setSelectedMarketSymbol(m.symbol)}
              style={{
                width: '100%',
                display: 'grid',
                gridTemplateColumns: '30px 1fr auto',
                gap: 10,
                alignItems: 'center',
                padding: '8px 10px',
                borderRadius: 10,
                background: active ? 'var(--ink)' : 'transparent',
                color: active ? 'var(--bg)' : 'var(--ink-2)',
                border: '2px solid ' + (active ? 'var(--ink)' : 'transparent'),
                fontFamily: 'var(--df)',
                fontWeight: 700,
                fontSize: 12,
                textAlign: 'left',
                cursor: 'pointer',
                marginBottom: 4,
              }}
            >
              <span
                style={{
                  width: 24,
                  height: 24,
                  borderRadius: 999,
                  background: active ? 'var(--y)' : 'var(--bg-3)',
                  border: '2px solid ' + (active ? 'var(--ink)' : 'var(--ink-3)'),
                  display: 'grid',
                  placeItems: 'center',
                  fontSize: 11,
                  fontWeight: 900,
                  color: 'var(--ink)',
                }}
              >
                {m.symbol[0]}
              </span>
              <div>
                <div style={{ letterSpacing: '-0.02em' }}>{m.symbol}</div>
                <div className="mono" style={{ fontSize: 10, opacity: 0.78, marginTop: 2 }}>
                  {t('vol')} ${formatTradingCompactUsdAmount(m.volume24h)}
                </div>
              </div>
              <span
                className="mono"
                style={{
                  fontSize: 11,
                  color:
                    fundingPct >= 0
                      ? active
                        ? '#9ff7c4'
                        : 'var(--pos)'
                      : active
                        ? '#ffb1c1'
                        : 'var(--neg)',
                }}
              >
                {fundingPct >= 0 ? '+' : ''}
                {fundingPct.toFixed(3)}%
              </span>
            </button>
          );
        })}
      </div>
    </aside>
  );
}
