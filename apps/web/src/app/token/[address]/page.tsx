'use client';

import { PriceChart } from '@/components/token/PriceChart';
import { TrendingTicker } from '@/components/token/TrendingTicker';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { useTokenEventsStream } from '@/hooks/useTokenEventsStream';
import { usePersistTransactionLifecycle } from '@/hooks/web3/usePersistTransactionLifecycle';
import { buildApiUrl } from '@/lib/api/base-url';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import { TxPreflight } from '@/app/_atlas/TxPreflight';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { useParams } from 'next/navigation';
import { useEffect, useMemo, useRef, useState } from 'react';
import { formatEther, maxUint256 } from 'viem';
import { useAccount, useChainId, useReadContract, useWaitForTransactionReceipt, useWriteContract } from 'wagmi';
import { buildTokenPreflightInput } from './token-preflight';
import { TokenBondingCurveCard } from './TokenBondingCurveCard';
import { TokenDetailHeader } from './TokenDetailHeader';
import { TokenDetailSidebar } from './TokenDetailSidebar';
import { TokenInfoTabs } from './TokenInfoTabs';
import { TokenTradePanel } from './TokenTradePanel';
import type { TokenData, TokenStats, TradeNotice } from './token-detail.types';
import { formatNativeAmount, parseOptionalEtherAmount, shortenErrorMessage } from './token-detail.utils';

export default function TokenDetailPage() {
    const t = useTranslations('tokenDetail');
    const params = useParams();
    const { isConnected, address: userAddress } = useAccount();
    // Writes + transaction persistence always follow the connected wallet's
    // chain; on-chain READS use the display-chain policy so a no-wallet visit
    // reads the Sepolia contracts instead of silently querying mainnet.
    const walletChainId = useChainId();
    const { chainId: readChainId } = useDisplayChainId();
    const address = params.address as string;

    const [amount, setAmount] = useState('');
    const [activeTab, setActiveTab] = useState<'buy' | 'sell'>('buy');
    const [isTrading, setIsTrading] = useState(false);
    const [tradeNotice, setTradeNotice] = useState<TradeNotice | null>(null);
    const [trackedTransaction, setTrackedTransaction] = useState<{
        txType: string;
        toAddress: `0x${string}`;
        contractAddress: `0x${string}`;
        value: bigint | string | null;
        metadata: Record<string, unknown> | null;
    } | null>(null);
    const lastSubmittedActionRef = useRef<'approval' | 'trade' | null>(null);
    const lastTradeTabRef = useRef<'buy' | 'sell'>('buy');

    const { data: tokenData, isLoading: isTokenLoading, isError: isTokenError } = useQuery({
        queryKey: ['token-detail', address],
        enabled: Boolean(address),
        queryFn: async () => {
            const response = await fetch(buildApiUrl(`/token/address/${address}`));

            if (!response.ok) {
                throw new Error('Failed to fetch token detail');
            }

            return response.json() as Promise<TokenData>;
        },
    });

    const { data: tokenStats } = useQuery({
        queryKey: ['token-detail-stats', address],
        enabled: Boolean(address),
        refetchInterval: 15_000,
        queryFn: async () => {
            const response = await fetch(buildApiUrl(`/token/address/${address}/stats`));

            if (!response.ok) {
                return null;
            }

            return response.json() as Promise<TokenStats>;
        },
    });

    const { data: bondingCurveAddress } = useReadContract({
        address: address as `0x${string}`,
        abi: CONTRACT_ABIS.LaunchToken,
        functionName: 'bondingCurve',
        chainId: readChainId,
    });

    const targetCurveAddress = (tokenData?.bondingCurve || bondingCurveAddress) as `0x${string}`;

    const { data: marketData, refetch: refetchMarket } = useReadContract({
        address: targetCurveAddress,
        abi: CONTRACT_ABIS.BondingCurve,
        functionName: 'currentPrice',
        chainId: readChainId,
        query: { enabled: !!targetCurveAddress },
    });

    // Realtime: the `/token-events` namespace is fed by a PRODUCTION producer
    // now — the indexer's DB sink publishes committed Buy/Sell/Graduated
    // projections over Postgres NOTIFY and the API broadcasts them (see
    // services/api-go/internal/eventbus). On events we refresh the affected
    // queries + on-chain reads; the 15s REST stats polling stays as fallback
    // when the socket is unavailable.
    const queryClient = useQueryClient();
    const refreshTokenData = () => {
        void queryClient.invalidateQueries({ queryKey: ['token-detail-stats', address] });
        void queryClient.invalidateQueries({ queryKey: ['token-candles', address] });
        void queryClient.invalidateQueries({ queryKey: ['token-detail', address] });
    };
    const tokenEvents = useTokenEventsStream(address, {
        onTrade: () => {
            refreshTokenData();
            void refetchMarket();
        },
        onPriceUpdate: () => {
            void refetchMarket();
        },
        onGraduation: () => {
            refreshTokenData();
        },
    });

    const { data: marketCap } = useReadContract({
        address: targetCurveAddress,
        abi: CONTRACT_ABIS.BondingCurve,
        functionName: 'marketCap',
        chainId: readChainId,
        query: { enabled: !!targetCurveAddress },
    });

    const { data: ethReserve } = useReadContract({
        address: targetCurveAddress,
        abi: CONTRACT_ABIS.BondingCurve,
        functionName: 'reserveBalance',
        chainId: readChainId,
        query: { enabled: !!targetCurveAddress },
    });

    const { writeContract, data: hash, isPending: isConfirming, error: writeError } = useWriteContract();
    const { isLoading: isWaiting, isSuccess: isConfirmed, data: receipt } = useWaitForTransactionReceipt({ hash });

    usePersistTransactionLifecycle({
        chainId: walletChainId,
        hash,
        fromAddress: userAddress,
        toAddress: trackedTransaction?.toAddress,
        contractAddress: trackedTransaction?.contractAddress,
        value: trackedTransaction?.value,
        txType: trackedTransaction?.txType,
        metadata: trackedTransaction?.metadata,
        receipt,
        enabled: Boolean(userAddress && trackedTransaction && hash),
    });

    // Allowance is a per-wallet read gating the sell write path, so it stays
    // on the wallet chain (it is only enabled once a wallet is connected).
    const { data: allowance, refetch: refetchAllowance } = useReadContract({
        address: address as `0x${string}`,
        abi: CONTRACT_ABIS.LaunchToken,
        functionName: 'allowance',
        args: userAddress && targetCurveAddress ? [userAddress, targetCurveAddress] : undefined,
        chainId: walletChainId,
        query: { enabled: !!userAddress && !!targetCurveAddress },
    });

    const isSelling = activeTab === 'sell';
    const parsedAmount = parseOptionalEtherAmount(amount);
    const needsApproval = isSelling && parsedAmount !== null && allowance !== undefined && allowance < parsedAmount;

    // Pre-sign review of the exact transaction the CTA below will submit.
    // Advisory only — it never gates the CTA; `canSubmit` stays the gate.
    const preflightInput = useMemo(
        () =>
            buildTokenPreflightInput({
                userAddress,
                tokenAddress: address,
                curveAddress: targetCurveAddress,
                chainId: walletChainId,
                parsedAmount,
                activeTab,
                needsApproval,
            }),
        [userAddress, address, targetCurveAddress, walletChainId, parsedAmount, activeTab, needsApproval],
    );

    const handleTrade = async () => {
        if (!isConnected || !targetCurveAddress || !parsedAmount) {
            setTradeNotice({
                tone: 'info',
                title: t('amountRequiredTitle'),
                message: t('amountRequiredMessage'),
            });
            return;
        }

        setIsTrading(true);
        setTradeNotice(null);
        lastTradeTabRef.current = activeTab;

        try {
            if (activeTab === 'buy') {
                lastSubmittedActionRef.current = 'trade';
                setTrackedTransaction({
                    txType: 'buy-token',
                    toAddress: targetCurveAddress,
                    contractAddress: targetCurveAddress,
                    value: parsedAmount,
                    metadata: {
                        tokenAddress: address,
                        action: 'buy',
                        amount: parsedAmount.toString(),
                    },
                });
                writeContract({
                    address: targetCurveAddress,
                    abi: CONTRACT_ABIS.BondingCurve,
                    functionName: 'buy',
                    args: [0n],
                    value: parsedAmount,
                });
            } else {
                if (needsApproval) {
                    lastSubmittedActionRef.current = 'approval';
                    setTrackedTransaction({
                        txType: 'approve',
                        toAddress: address as `0x${string}`,
                        contractAddress: address as `0x${string}`,
                        value: '0',
                        metadata: {
                            tokenAddress: address,
                            spender: targetCurveAddress,
                            // The signed call below grants maxUint256, so that is
                            // what the persisted record says. Recording the trade
                            // size here made the activity history claim a bounded
                            // allowance the chain never received.
                            requestedAllowance: maxUint256.toString(),
                        },
                    });
                    writeContract({
                        address: address as `0x${string}`,
                        abi: CONTRACT_ABIS.LaunchToken,
                        functionName: 'approve',
                        args: [targetCurveAddress, maxUint256],
                    });
                } else {
                    lastSubmittedActionRef.current = 'trade';
                    setTrackedTransaction({
                        txType: 'sell-token',
                        toAddress: targetCurveAddress,
                        contractAddress: targetCurveAddress,
                        value: '0',
                        metadata: {
                            tokenAddress: address,
                            action: 'sell',
                            amount: parsedAmount.toString(),
                        },
                    });
                    writeContract({
                        address: targetCurveAddress,
                        abi: CONTRACT_ABIS.BondingCurve,
                        functionName: 'sell',
                        args: [parsedAmount, 0n],
                    });
                }
            }
        } catch (error) {
            console.error('Trade failed', error);
            setIsTrading(false);
        }
    };

    useEffect(() => {
        if (isConfirmed) {
            setIsTrading(false);

            if (lastSubmittedActionRef.current === 'approval') {
                lastSubmittedActionRef.current = null;
                refetchAllowance();
                setTradeNotice({
                    tone: 'success',
                    title: t('approvalConfirmedTitle'),
                    message: t('approvalConfirmedMessage'),
                });
            } else {
                const completedTab = lastTradeTabRef.current;

                lastSubmittedActionRef.current = null;
                setAmount('');
                refetchMarket();
                refetchAllowance();
                setTradeNotice({
                    tone: 'success',
                    title: t('tradeConfirmedTitle'),
                    message: completedTab === 'buy' ? t('buyConfirmedMessage') : t('sellConfirmedMessage'),
                });
            }
        }
    }, [isConfirmed, refetchMarket, refetchAllowance, t]);

    useEffect(() => {
        if (!writeError) {
            return;
        }

        lastSubmittedActionRef.current = null;
        setIsTrading(false);
        setTradeNotice({
            tone: 'error',
            title: t('transactionFailedTitle'),
            message: shortenErrorMessage(writeError.message, t('walletRejectedFallback')),
        });
    }, [writeError, t]);

    const currentPriceLabel = marketData ? `${formatNativeAmount(marketData, 6)} NATIVE` : t('unavailable');
    const reserveValue = ethReserve ? Number(formatEther(ethReserve)) : 0;
    const targetReserve = 24;
    const bondingCurveProgress = Math.min(100, (reserveValue / targetReserve) * 100);
    const canSubmit = Boolean(parsedAmount && targetCurveAddress);

    if (isTokenLoading) {
        return (
            <div className="min-h-screen bg-(--color-background)">
                <div className="container py-16 text-sm" style={{ color: 'var(--color-text-muted)' }}>
                    {t('loadingTokenDetail')}
                </div>
            </div>
        );
    }

    if (isTokenError || !tokenData) {
        return (
            <div className="min-h-screen bg-(--color-background)">
                <div className="container py-16">
                    <h1 className="text-xl font-bold text-(--color-text-primary)">{t('tokenNotFound')}</h1>
                    <p className="mt-2 text-sm text-(--color-text-muted)">
                        {t('tokenNotFoundBody')}
                    </p>
                </div>
            </div>
        );
    }

    return (
        <div className="min-h-screen" style={{ background: 'var(--color-background)' }}>
            <div className="border-b border-(--color-border) bg-(--color-background-secondary)">
                <TrendingTicker />
            </div>

            <TokenDetailHeader
                tokenAddress={address}
                token={tokenData}
                stats={tokenStats ?? null}
                currentPriceLabel={currentPriceLabel}
            />

            <div className="container py-4">
                <div className="three-column-layout">
                    <TokenDetailSidebar currentAddress={address} />

                    <div className="space-y-4">
                        <div className="mb-4">
                            {/* Honest live badge: shown only while the token-events
                                socket is actually connected (producer = indexer). */}
                            {tokenEvents.isLive && (
                                <div className="mb-2 inline-flex items-center gap-2 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-3 py-1 text-xs text-emerald-300">
                                    <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-400" />
                                    {t('liveOnchainEvents')}
                                </div>
                            )}
                            <PriceChart tokenAddress={address} />
                        </div>

                        <TokenInfoTabs tokenAddress={address} token={tokenData} />
                    </div>

                    <div className="space-y-4">
                        <TokenTradePanel
                            tokenSymbol={tokenData.symbol}
                            activeTab={activeTab}
                            amount={amount}
                            canSubmit={canSubmit}
                            isBusy={isConfirming || isWaiting || isTrading}
                            isConnected={isConnected}
                            needsApproval={needsApproval}
                            currentPriceLabel={currentPriceLabel}
                            notice={tradeNotice}
                            onAmountChange={setAmount}
                            onTabChange={setActiveTab}
                            onSubmit={handleTrade}
                        />

                        <TxPreflight input={preflightInput} />

                        <TokenBondingCurveCard
                            progress={bondingCurveProgress}
                            reserveLabel={formatNativeAmount(ethReserve)}
                            targetReserveLabel={targetReserve.toString()}
                        />
                    </div>
                </div>
            </div>
        </div>
    );
}
