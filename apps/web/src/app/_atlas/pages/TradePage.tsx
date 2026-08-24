'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useApp } from '../AppContext';
import { useTrading } from '@/hooks/useTrading';
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
import { TxPreflight } from '../TxPreflight';
import { buildPerpApprovalPreflightInput } from './tradePreflight';
import type { TabKey, TradePosition, TradeSide } from './tradeTypes';
import { formatBalanceDisplay, priceFlash } from './tradeUtils';

export function TradePage() {
  const app = useApp();
  const t = useTranslations('trading');
  const tTerm = useTranslations('trade.terminal');
  const tTx = useTranslations('trade.tx');
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
    positions,
    positionsLoading,
    orders,
    history,
    isOrdersLoading,
    isHistoryLoading,
    openPosition,
    closePosition,
    isApproving,
    isApproveSuccess,
    isOpenPending,
    isOpenConfirming,
    isOpenSuccess,
    openError,
    resetOpen,
    isClosePending,
    isCloseConfirming,
    isCloseSuccess,
    closeError,
    resetClose,
    address,
    chainId,
    perpAddresses,
    isCollateralApproved,
  } = trading;

  const [side, setSide] = useState<TradeSide>('long');
  const [orderType, setOrderType] = useState<TradeOrderType>('market');
  const [size, setSize] = useState('');
  const [limitPrice, setLimitPrice] = useState('');
  const [lev, setLev] = useState(10);
  const [tab, setTab] = useState<TabKey>('positions');
  const [marketSearch, setMarketSearch] = useState('');

  // Deep link: /trade?symbol=ETH-USD selects that market once the list has it,
  // and the current selection is written back so the terminal URL is shareable.
  // Read via window.location (not useSearchParams) to keep this client-only —
  // the markets list arrives async, so the wish is held until it can be honored.
  const pendingSymbolRef = useRef<string | null>(null);
  useEffect(() => {
    pendingSymbolRef.current = new URLSearchParams(window.location.search).get('symbol');
  }, []);
  useEffect(() => {
    const wanted = pendingSymbolRef.current;
    if (wanted && markets.some((m) => m.symbol === wanted)) {
      pendingSymbolRef.current = null;
      setSelectedMarketSymbol(wanted);
    }
  }, [markets, setSelectedMarketSymbol]);
  useEffect(() => {
    if (!selectedMarketSymbol || pendingSymbolRef.current) return;
    const url = new URL(window.location.href);
    if (url.searchParams.get('symbol') === selectedMarketSymbol) return;
    url.searchParams.set('symbol', selectedMarketSymbol);
    window.history.replaceState(null, '', url);
  }, [selectedMarketSymbol]);

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

  useEffect(() => {
    if (isApproving) {
      app.setModal({
        kind: 'tx',
        props: { stage: 'pending', title: tTx('approvingTitle'), summary: tTx('approvingSummary') },
      });
    }
  }, [isApproving, app, tTx]);

  useEffect(() => {
    if (isOpenPending || isOpenConfirming) {
      app.setModal({
        kind: 'tx',
        props: {
          stage: 'pending',
          title: tTx('openingTitle'),
          summary: tTx('openSummary', {
            side: side === 'long' ? tTx('long') : tTx('short'),
            size: size || '0',
            lev,
            symbol: selectedMarket?.symbol ?? '—',
          }),
        },
      });
    }
  }, [isOpenPending, isOpenConfirming, side, size, lev, selectedMarket, app, tTx]);

  useEffect(() => {
    if (isOpenSuccess && selectedMarket) {
      const sideLabel = side === 'long' ? tTx('long') : tTx('short');
      app.setModal({
        kind: 'tx',
        props: {
          stage: 'confirmed',
          title: tTx('openConfirmedTitle'),
          summary: tTx('openSummary', { side: sideLabel, size: size || '0', lev, symbol: selectedMarket.symbol }),
        },
      });
      app.toast(tTx('openConfirmedToast', { side: sideLabel, symbol: selectedMarket.symbol }), 'ok');
      resetOpen();
    }
  }, [isOpenSuccess, side, size, lev, selectedMarket, app, resetOpen, tTx]);

  useEffect(() => {
    if (openError) {
      app.toast(openError.message?.slice(0, 80) || tTx('openFailed'), 'err');
      resetOpen();
    }
  }, [openError, app, resetOpen, tTx]);

  // The approve is a separate transaction: openPosition sends it and returns.
  // Tell the user the next submit will actually open the position, instead of
  // leaving the confirmed approval as a silent dead end.
  useEffect(() => {
    if (isApproveSuccess) {
      app.closeModal();
      app.toast(tTx('approvedToast'), 'ok');
    }
  }, [isApproveSuccess, app, tTx]);

  useEffect(() => {
    if (isClosePending || isCloseConfirming) {
      app.setModal({
        kind: 'tx',
        props: { stage: 'pending', title: tTx('closingTitle'), summary: tTx('closingSummary') },
      });
    }
  }, [isClosePending, isCloseConfirming, app, tTx]);

  useEffect(() => {
    if (isCloseSuccess) {
      app.setModal({ kind: 'tx', props: { stage: 'confirmed', title: tTx('closedTitle'), summary: tTx('closedSummary') } });
      app.toast(tTx('closedToast'), 'ok');
      resetClose();
    }
  }, [isCloseSuccess, app, resetClose, tTx]);

  useEffect(() => {
    if (closeError) {
      app.toast(closeError.message?.slice(0, 80) || tTx('closeFailed'), 'err');
      resetClose();
    }
  }, [closeError, app, resetClose, tTx]);

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

  // Only renders while the collateral allowance is short — that is when the
  // submit below actually sends an approve. Once approved it returns null and
  // the strip disappears, rather than describing a transaction that is no
  // longer the next one.
  const preflightInput = useMemo(
    () =>
      buildPerpApprovalPreflightInput({
        userAddress: address,
        usdc: perpAddresses.usdc,
        positionManager: perpAddresses.positionManager,
        chainId,
        collateralAmount: size,
        isCollateralApproved,
      }),
    [address, perpAddresses.usdc, perpAddresses.positionManager, chainId, size, isCollateralApproved],
  );

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
    openPosition({
      collateralAmount: size,
      leverage: lev,
      isLong: side === 'long',
      acceptablePrice: orderType === 'limit' ? limitPrice : undefined,
      slippagePercent: executionState.slippagePercentValue,
      deadlineMinutes: executionState.deadlineMinutesValue,
    });
  };

  const handleClosePosition = (position: TradePosition) => {
    if (executionState.validationCode) {
      app.toast(tTx('fixSlippage'), 'warn');
      return;
    }
    closePosition({
      position,
      slippagePercent: executionState.slippagePercentValue,
      deadlineMinutes: executionState.deadlineMinutesValue,
    });
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
          setSelectedMarketSymbol={setSelectedMarketSymbol}
        />

        <div className="term-center">
          <TradeMarketPanel market={selectedMarket} displayLast={displayLast} flash={flash} />
          <TradeTables
            tab={tab}
            positions={positions}
            orders={orders}
            history={history}
            currentPrice={currentPrice}
            chainId={chainId}
            isWalletConnected={app.walletState === 'connected'}
            isActivityAuthorized={trading.isActivityAuthorized}
            isPositionsLoading={positionsLoading}
            isOrdersLoading={isOrdersLoading}
            isHistoryLoading={isHistoryLoading}
            isClosePending={isClosePending}
            isCloseConfirming={isCloseConfirming}
            isMarketStreamConnected={isMarketStreamConnected}
            marketStreamStatus={marketStreamStatus}
            setTab={setTab}
            onClosePosition={handleClosePosition}
            onConnect={app.openConnect}
          />
        </div>

        <div className="right-stack">
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
            preflight={<TxPreflight input={preflightInput} />}
            canSubmit={canSubmit}
            isApproving={isApproving}
            isOpenPending={isOpenPending}
            isOpenConfirming={isOpenConfirming}
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
