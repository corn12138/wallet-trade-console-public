'use client';

import { useTranslations } from 'next-intl';
import { ConnectButton } from '@/components/web3';
import type { TradeNotice } from './token-detail.types';
import { TokenTradeStatusNotice } from './TokenTradeStatusNotice';

interface TokenTradePanelProps {
    tokenSymbol: string;
    activeTab: 'buy' | 'sell';
    amount: string;
    canSubmit: boolean;
    isBusy: boolean;
    isConnected: boolean;
    needsApproval: boolean;
    currentPriceLabel: string;
    notice: TradeNotice | null;
    onAmountChange: (value: string) => void;
    onTabChange: (nextTab: 'buy' | 'sell') => void;
    onSubmit: () => void;
}

export function TokenTradePanel({
    tokenSymbol,
    activeTab,
    amount,
    canSubmit,
    isBusy,
    isConnected,
    needsApproval,
    currentPriceLabel,
    notice,
    onAmountChange,
    onTabChange,
    onSubmit,
}: TokenTradePanelProps) {
    const t = useTranslations('tokenDetail');
    const sideLabel = activeTab === 'buy' ? t('buy') : t('sell');
    let submitLabel = activeTab === 'buy'
        ? t('buyToken', { symbol: tokenSymbol })
        : t('sellToken', { symbol: tokenSymbol });

    if (needsApproval) {
        submitLabel = t('approveToken', { symbol: tokenSymbol });
    }

    if (isBusy) {
        submitLabel = needsApproval ? t('approving') : t('submittingOrder', { side: sideLabel });
    }

    const amountAssetLabel = activeTab === 'buy' ? 'NATIVE' : tokenSymbol;
    let allowanceStateLabel = t('readyToSell');

    if (activeTab === 'buy') {
        allowanceStateLabel = t('notRequired');
    } else if (needsApproval) {
        allowanceStateLabel = t('approvalRequired');
    }

    return (
        <div className="trade-panel space-y-4">
            <div className="flex rounded-lg overflow-hidden" style={{ background: 'var(--color-background-secondary)' }}>
                <button
                    onClick={() => onTabChange('buy')}
                    className="flex-1 py-2.5 text-sm font-medium"
                    style={{
                        background: activeTab === 'buy' ? 'var(--color-success)' : 'transparent',
                        color: activeTab === 'buy' ? '#fff' : 'var(--color-text-muted)',
                    }}
                >
                    {t('buy')}
                </button>
                <button
                    onClick={() => onTabChange('sell')}
                    className="flex-1 py-2.5 text-sm font-medium"
                    style={{
                        background: activeTab === 'sell' ? 'var(--color-error)' : 'transparent',
                        color: activeTab === 'sell' ? '#fff' : 'var(--color-text-muted)',
                    }}
                >
                    {t('sell')}
                </button>
            </div>

            <TokenTradeStatusNotice notice={notice} />

            <div>
                <label className="block text-sm mb-2" style={{ color: 'var(--color-text-secondary)' }}>
                    {t('amount')}
                </label>
                <div
                    className="flex rounded-lg overflow-hidden"
                    style={{ background: 'var(--color-background-input)', border: '1px solid var(--color-border)' }}
                >
                    <input
                        type="number"
                        min="0"
                        step="any"
                        value={amount}
                        onChange={(event) => onAmountChange(event.target.value)}
                        placeholder="0.00"
                        className="flex-1 bg-transparent px-3 py-2.5 outline-none"
                        style={{ color: 'var(--color-text-primary)' }}
                    />
                    <div className="flex items-center px-3 text-sm" style={{ color: 'var(--color-text-muted)' }}>
                        {amountAssetLabel}
                    </div>
                </div>
            </div>

            <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                    <span style={{ color: 'var(--color-text-muted)' }}>{t('executionPath')}</span>
                    <span style={{ color: 'var(--color-text-primary)' }}>
                        {activeTab === 'buy' ? t('executionBuy') : t('executionSell')}
                    </span>
                </div>
                <div className="flex justify-between">
                    <span style={{ color: 'var(--color-text-muted)' }}>{t('currentPrice')}</span>
                    <span style={{ color: 'var(--color-text-primary)' }}>{currentPriceLabel}</span>
                </div>
                <div className="flex justify-between">
                    <span style={{ color: 'var(--color-text-muted)' }}>{t('allowanceState')}</span>
                    <span style={{ color: 'var(--color-text-primary)' }}>{allowanceStateLabel}</span>
                </div>
            </div>

            {isConnected ? (
                <button
                    disabled={!canSubmit || isBusy}
                    onClick={onSubmit}
                    className="w-full rounded-lg py-3 font-medium disabled:opacity-50"
                    style={{
                        background: activeTab === 'buy' ? 'var(--color-success)' : 'var(--color-error)',
                        color: '#fff',
                    }}
                >
                    {submitLabel}
                </button>
            ) : (
                <ConnectButton />
            )}
        </div>
    );
}
