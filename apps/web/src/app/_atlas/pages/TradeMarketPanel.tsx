'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { formatTradingCompactUsdAmount, formatTradingPercentRate } from '@/app/trade/trade-page.utils';
import type { TradingCandleResolution } from '@/lib/api';
import { TradeCandleChart } from './TradeCandleChart';
import type { PriceFlash, TradeMarket } from './tradeTypes';

type TradeMarketPanelProps = {
  market: TradeMarket | null;
  displayLast: string;
  flash: PriceFlash;
};

export function TradeMarketPanel({ market, displayLast, flash }: TradeMarketPanelProps) {
  const t = useTranslations('trade.panel');
  const [resolution, setResolution] = useState<TradingCandleResolution>('15m');

  return (
    <>
      <div className="sym-bar">
        <div className="sym-name">
          <span className="b">{market?.symbol ? market.symbol[0] : '—'}</span>
          <div>
            <div className="t">{market?.symbol ?? t('noMarket')}</div>
            <div className="sub">{t('marketMeta', { chainId: market?.chainId ?? '—' })}</div>
          </div>
        </div>
        <div className="sym-stats">
          <div className="sst">
            <div className="lbl">{t('last')}</div>
            <div
              className={'v ' + (flash === 'up' ? 'flash-up' : flash === 'down' ? 'flash-down' : '')}
              style={{ color: flash === 'down' ? '#FFD27A' : '#5BD66B' }}
            >
              {displayLast}
            </div>
          </div>
          <div className="sst">
            <div className="lbl">{t('funding')}</div>
            <div className="v">{market ? formatTradingPercentRate(market.fundingRate) + '%' : '—'}</div>
          </div>
          <div className="sst">
            <div className="lbl">{t('vol24h')}</div>
            <div className="v">${market ? formatTradingCompactUsdAmount(market.volume24h) : '—'}</div>
          </div>
          <div className="sst">
            <div className="lbl">{t('longOI')}</div>
            <div className="v">${market ? formatTradingCompactUsdAmount(market.longOpenInterest) : '—'}</div>
          </div>
          <div className="sst">
            <div className="lbl">{t('shortOI')}</div>
            <div className="v">${market ? formatTradingCompactUsdAmount(market.shortOpenInterest) : '—'}</div>
          </div>
        </div>
      </div>

      <TradeCandleChart
        symbol={market?.symbol}
        chainId={market?.chainId}
        resolution={resolution}
        onResolutionChange={setResolution}
      />
    </>
  );
}
