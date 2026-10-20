'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useApp } from '../AppContext';
import { parseTradeLink, tradeLinkState, type TradeLink } from '@/features/trade';
import { useTrading } from '@/hooks/useTrading';
import { useTradingTransactions } from '@/hooks/useTradingTransactions';
import { TradeIntentDialog } from './TradeIntentDialog';
import { useTradingDefaults } from '@/lib/trading-defaults';
import {
  evaluateTradeOrder,
  type TradeOrderType,
} from '@/app/trade/trade-page.utils';
import { evaluateTradeExecutionSettings } from '@/app/trade/trade-execution.utils';
import { resolveTradeNotice } from '@/app/trade/trade-order-messages';
import { TradeMarketPanel } from './TradeMarketPanel';
import { TradeMarketRail } from './TradeMarketRail';
import { TradeOrderbook } from './TradeOrderbook';
import { TradeTables } from './TradeTables';
import { TradeTicket } from './TradeTicket';
import type { TabKey, TradePosition, TradeSide } from './tradeTypes';
import { formatBalanceDisplay, priceFlash } from './tradeUtils';

export function TradePage() {
  const app = useApp();
  const t = useTranslations('trading');
  const tTerm = useTranslations('trade.terminal');
  const tTx = useTranslations('trade.tx');
  const tIntent = useTranslations('trade.intent');
  const tError = useTranslations('atlasShell.modals.intent.err');
  const trading = useTrading();
  const {
    markets,
    selectedMarket,
    selectedMarketSymbol,
    setSelectedMarketSymbol,
    currentPrice,
    formattedPrice,
    orderbook,
    isOrderbookLoading,
    isOrderbookError,
    hasLiveOrderbook,
    refetchOrderbook,
    recentTrades,
    isRecentTradesLoading,
    isMarketStreamConnected,
    marketStreamStatus,
    formattedBalance,
    portfolioPositions,
    isPortfolioLoading,
    orders,
    history,
    isOrdersLoading,
    isHistoryLoading,
    address,
    chainId,
    perpAddresses,
  } = trading;

  const [showIntent, setShowIntent] = useState(false);
  const [side, setSide] = useState<TradeSide>('long');
  const [orderType, setOrderType] = useState<TradeOrderType>('market');
  const [size, setSize] = useState('');
  const [limitPrice, setLimitPrice] = useState('');
  const [lev, setLev] = useState(10);
  const [tab, setTab] = useState<TabKey>('positions');
  const [marketSearch, setMarketSearch] = useState('');

  // Deep link: /trade?symbol=ETH-USD[&chainId=N] selects that market once the
  // list has it, and the current selection is written back so the terminal URL
  // is shareable. Read via window.location (not useSearchParams) to keep this
  // client-only — the markets list arrives async, so the wish is held until it
  // can be honored. A link naming a chain stays pending until a connected
  // wallet on that chain resolves it or a manual choice replaces it: the
  // chain the terminal merely displays (a default while disconnected) never
  // resolves it, and the URL keeps it meanwhile. On another wallet chain the
  // page asks for the explicit network switch and selects nothing, so a
  // same-symbol market elsewhere is never substituted.
  // undefined until the URL has been read: synchronization waits for it, so
  // the first render cannot overwrite a target it has not seen yet.
  const [link, setLink] = useState<TradeLink | null | undefined>(undefined);
  useEffect(() => {
    setLink(parseTradeLink(window.location.search));
  }, []);
  const linkState = tradeLinkState(link ?? null, chainId, app.wallet?.chainId ?? null);
  const linkResolves = linkState.kind === 'ready' || linkState.kind === 'symbolOnly';
  const linkWanted = linkResolves || (linkState.kind === 'connectRequired' && linkState.displayable) ? linkState.symbol : null;
  const linkChain = linkState.kind === 'ready' || linkState.kind === 'connectRequired' ? linkState.chainId : null;
  // A disconnected visitor may see the target read-only; that selection is
  // withdrawn if the wallet then connects on another chain.
  const shownFromLink = useRef<string | null>(null);
  useEffect(() => {
    if (linkWanted && markets.some((m) => m.symbol === linkWanted && (linkChain === null || m.chainId === linkChain))) {
      setSelectedMarketSymbol(linkWanted);
      if (linkResolves) {
        shownFromLink.current = null;
        setLink(null);
      } else shownFromLink.current = linkWanted;
    }
  }, [markets, linkWanted, linkChain, linkResolves, setSelectedMarketSymbol]);
  useEffect(() => {
    if (linkState.kind === 'switchRequired' && shownFromLink.current) {
      shownFromLink.current = null;
      setSelectedMarketSymbol(null);
    }
  }, [linkState.kind, setSelectedMarketSymbol]);
  // Choosing a market by hand replaces the link's wish.
  const selectMarket = (symbol: string | null) => {
    shownFromLink.current = null;
    setLink(null);
    setSelectedMarketSymbol(symbol);
  };
  useEffect(() => {
    if (!selectedMarketSymbol || link !== null) return;
    const url = new URL(window.location.href);
    if (url.searchParams.get('symbol') === selectedMarketSymbol && url.searchParams.get('chainId') === String(chainId)) return;
    url.searchParams.set('symbol', selectedMarketSymbol);
    url.searchParams.set('chainId', String(chainId));
    window.history.replaceState(null, '', url);
  }, [selectedMarketSymbol, link, chainId]);

  // Slippage/deadline are shared with Settings → "Trading defaults" via a
  // browser-local store, and genuinely applied to every open/close below.
  const {
    slippagePercent,
    deadlineMinutes,
    setSlippagePercent,
    setDeadlineMinutes,
  } = useTradingDefaults();

  const prevPriceRef = useRef<bigint | undefined>(currentPrice);
  const [flash, setFlash] = useState<'up' | 'down' | null>(null);
  useEffect(() => {
    const next = currentPrice;
    const f = priceFlash(prevPriceRef.current, next);
    prevPriceRef.current = next;
    if (f) {
      setFlash(f);
      const t = setTimeout(() => setFlash(null), 600);
      return () => clearTimeout(t);
    }
  }, [currentPrice]);

  const displayLast = formattedPrice && formattedPrice !== '—' ? formattedPrice : '—';

  const balanceNumeric = Number(formattedBalance);
  const hasBalance = Number.isFinite(balanceNumeric) && balanceNumeric > 0;
  const balanceDisplay = formatBalanceDisplay(formattedBalance);

  // Order + execution validation shared with the (former) trade workspace:
  // "limit" is an acceptable-price guard on immediate execution, so a long
  // whose guard is below mark (or a short above mark) is blocked as it would
  // revert on-chain, and a guard already satisfied by mark executes now.
  const tradeState = useMemo(
    () =>
      evaluateTradeOrder({
        address: app.walletState === 'connected' ? (app.wallet?.address as `0x${string}` | undefined) : undefined,
        amount: size,
        balance: formattedBalance,
        leverage: lev,
        markPrice: displayLast,
        isLong: side === 'long',
        orderType,
        limitPrice,
      }),
    [app.walletState, app.wallet?.address, size, formattedBalance, lev, displayLast, side, orderType, limitPrice],
  );

  const executionState = useMemo(
    () => evaluateTradeExecutionSettings({ slippagePercent, deadlineMinutes }),
    [slippagePercent, deadlineMinutes],
  );

  const notice = useMemo(
    () => resolveTradeNotice(tradeState.notice, tradeState.validationCode, executionState.validationCode, t),
    [tradeState.notice, tradeState.validationCode, executionState.validationCode, t],
  );

  const canSubmit = tradeState.canSubmit && !executionState.validationCode;

  const transaction = useTradingTransactions({
    address, addrs: perpAddresses, chainId, currentPrice, selectedMarket,
    positions: trading.positions,
    viewerKey: trading.viewerKey,
    openParams: { collateralAmount: size, leverage: lev, isLong: side === 'long',
      acceptablePrice: orderType === 'limit' ? limitPrice : undefined,
      slippagePercent: executionState.slippagePercentValue,
      deadlineMinutes: executionState.deadlineMinutesValue },
    refetchAll: trading.refetchAll,
    refetchActivity: trading.refetchActivity,
  });
  const { intent } = transaction;
  const closeIntent = () => {
    setShowIntent(false);
    if (!intent.hash && intent.state !== 'AWAITING_WALLET') intent.reset();
  };

  // Fee/position figures come from evaluateTradeOrder — the same module the
  // validation reads — so the ticket can't show a different rate than the
  // notice copy reasons about (this page used to hardcode a second constant).
  const collateral = Number(size) || 0;
  const margin = collateral;

  const filteredMarkets = useMemo(() => {
    if (!marketSearch) return markets;
    const q = marketSearch.toLowerCase();
    return markets.filter((m) => m.symbol.toLowerCase().includes(q));
  }, [markets, marketSearch]);

  const handleSubmit = () => {
    if (app.walletState !== 'connected') {
      app.openConnect();
      return;
    }
    if (!selectedMarket || !canSubmit) {
      return;
    }
    const error = transaction.reviewOpen();
    if (error) app.toast(tError(error), 'err');
    else setShowIntent(true);
  };

  const handleClosePosition = (position: TradePosition) => {
    if (executionState.validationCode) {
      app.toast(tTx('fixSlippage'), 'warn');
      return;
    }
    const error = transaction.reviewClose(position);
    if (error) app.toast(tError(error), 'err');
    else setShowIntent(true);
  };

  const handleMax = () => {
    if (!hasBalance) return;
    setSize(balanceNumeric.toString());
  };

  // Percent presets floor to cents so the result can never exceed the balance
  // it was derived from (a rounded-up value would fail validation).
  const handlePercent = (percent: number) => {
    if (!hasBalance) return;
    if (percent >= 100) {
      handleMax();
      return;
    }
    const amount = Math.floor(balanceNumeric * percent) / 100;
    setSize(amount > 0 ? String(amount) : '');
  };

  // A tapped book level becomes the acceptable-price guard — the standard
  // terminal interaction — so the ticket flips to limit mode to show it.
  const handleBookPriceSelect = (price: string) => {
    setOrderType('limit');
    setLimitPrice(price);
  };

  return (
    <div className="terminal" style={{ minHeight: 'calc(100vh - 130px)' }}>
      {showIntent && <TradeIntentDialog transaction={transaction} close={closeIntent} />}
      {!showIntent && intent.activeStep && (
        <button className="btn btn-sm" onClick={() => setShowIntent(true)}>{tIntent('viewTransaction')}</button>
      )}
      {linkState.kind === 'switchRequired' ? <div className="block row gap-10" role="status" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
        <span>{tTerm('linkOtherChain', { symbol: linkState.symbol, target: linkState.chainId, current: linkState.walletChainId })}</span>
        <button type="button" className="btn btn-sm" onClick={() => app.switchChain({ id: linkState.chainId, name: tTerm('chain', { chainId: linkState.chainId }) })}>{tTerm('linkSwitch')}</button>
      </div> : null}
      {linkState.kind === 'connectRequired' ? <div className="block row gap-10" role="status" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
        <span>{tTerm('linkConnectTarget', { symbol: linkState.symbol, target: linkState.chainId })}</span>
        <button type="button" className="btn btn-sm" onClick={app.openConnect}>{tTerm('linkConnect')}</button>
      </div> : null}
      {linkState.kind === 'unsupported' ? <div className="block" role="status">{tTerm('linkUnsupported', { symbol: linkState.symbol })}</div> : null}
      <div className="term-bar">
        <div className="lights">
          <b />
          <b />
          <b />
        </div>
        <div className="ttl">atlas-x · /trade</div>
        <div className="right">
          <span className={`status-dot ${isMarketStreamConnected ? 'live' : 'warn'}`} />
          {isMarketStreamConnected ? tTerm('live') : marketStreamStatus === 'connecting' ? tTerm('syncing') : tTerm('restFallback')}
          <span style={{ opacity: 0.5 }}>·</span>
          <span>{tTerm('chain', { chainId: selectedMarket?.chainId ?? '—' })}</span>
        </div>
      </div>

      <div className="term-grid">
        <TradeMarketRail
          markets={filteredMarkets}
          marketSearch={marketSearch}
          selectedMarketSymbol={selectedMarketSymbol}
          setMarketSearch={setMarketSearch}
          setSelectedMarketSymbol={selectMarket}
        />

        <div className="term-center">
          <TradeMarketPanel market={selectedMarket} displayLast={displayLast} flash={flash} />
        </div>

        {/* Own grid area (not inside term-center) so the mobile stack can put
            the ticket directly under the chart and the tables after it. */}
        <div className="term-tables">
          <TradeTables
            tab={tab}
            positions={portfolioPositions}
            orders={orders}
            history={history}
            currentPrice={currentPrice}
            chainId={chainId}
            isWalletConnected={app.walletState === 'connected'}
            isActivityAuthorized={trading.isActivityAuthorized}
            isPositionsLoading={isPortfolioLoading}
            isOrdersLoading={isOrdersLoading}
            isHistoryLoading={isHistoryLoading}
            isClosePending={transaction.locked}
            isCloseConfirming={false}
            isMarketStreamConnected={isMarketStreamConnected}
            marketStreamStatus={marketStreamStatus}
            setTab={setTab}
            onClosePosition={handleClosePosition}
            onSelectMarket={selectMarket}
            onConnect={app.openConnect}
          />
        </div>

        <div className="right-stack" data-testid="trade-right-stack">
          <TradeOrderbook
            orderbook={orderbook}
            displayLast={displayLast}
            flash={flash}
            isMarketStreamConnected={isMarketStreamConnected}
            marketStreamStatus={marketStreamStatus}
            isLoading={isOrderbookLoading}
            isError={isOrderbookError}
            hasLiveOrderbook={hasLiveOrderbook}
            onRetry={() => refetchOrderbook()}
            recentTrades={recentTrades}
            isRecentTradesLoading={isRecentTradesLoading}
            onPriceSelect={handleBookPriceSelect}
          />
          <TradeTicket
            app={app}
            side={side}
            orderType={orderType}
            size={size}
            limitPrice={limitPrice}
            lev={lev}
            balanceDisplay={balanceDisplay}
            displayLast={displayLast}
            liquidationPrice={tradeState.liquidationPrice}
            fee={tradeState.feeEstimate}
            margin={margin}
            positionSize={tradeState.positionSize}
            slippagePercent={slippagePercent}
            deadlineMinutes={deadlineMinutes}
            notice={notice}
            preflight={undefined}
            canSubmit={canSubmit}
            isApproving={false}
            isOpenPending={transaction.locked}
            isOpenConfirming={false}
            setSide={setSide}
            setOrderType={setOrderType}
            setSize={setSize}
            setLimitPrice={setLimitPrice}
            setLev={setLev}
            setSlippagePercent={setSlippagePercent}
            setDeadlineMinutes={setDeadlineMinutes}
            handlePercent={handlePercent}
            handleSubmit={handleSubmit}
          />
        </div>
      </div>
    </div>
  );
}
