'use client';

import { HolderDistribution } from '@/components/token/HolderDistribution';
import { TradeHistory } from '@/components/token/TradeHistory';
import { useTranslations } from 'next-intl';
import { useState } from 'react';
import type { TokenData } from './token-detail.types';
import { useEnumLabel } from '@/app/_atlas/enums';

interface TokenInfoTabsProps {
    tokenAddress: string;
    token: TokenData | null;
}

const tabLabels = [
    { id: 'overview', labelKey: 'tabOverview' },
    { id: 'trades', labelKey: 'tabTrades' },
    { id: 'holders', labelKey: 'tabHolders' },
    { id: 'discussion', labelKey: 'tabDiscussion' },
] as const;

export function TokenInfoTabs({ tokenAddress, token }: TokenInfoTabsProps) {
    const t = useTranslations('tokenDetail');
    const statusLabel = useEnumLabel('token');
    const [activeTab, setActiveTab] = useState<(typeof tabLabels)[number]['id']>('overview');

    return (
        <div className="chart-container">
            <div className="flex" style={{ borderBottom: '1px solid var(--color-border)' }}>
                {tabLabels.map((tab) => (
                    <button
                        key={tab.id}
                        onClick={() => setActiveTab(tab.id)}
                        className="px-6 py-3 text-sm font-medium"
                        style={{
                            color: activeTab === tab.id ? 'var(--color-text-primary)' : 'var(--color-text-muted)',
                            borderBottom: activeTab === tab.id ? '2px solid var(--color-accent)' : 'none',
                        }}
                    >
                        {t(tab.labelKey)}
                    </button>
                ))}
            </div>

            <div className="p-4">
                {activeTab === 'overview' && (
                    <div className="space-y-4">
                        <div>
                            <h4 className="font-bold mb-2">{t('description')}</h4>
                            <p className="text-sm text-(--color-text-muted)">
                                {token?.description || t('noDescription')}
                            </p>
                        </div>

                        <div className="grid gap-4 md:grid-cols-2">
                            <div>
                                <h4 className="font-bold mb-2">{t('contract')}</h4>
                                <p className="break-all text-xs font-mono text-(--color-text-muted)">
                                    {token?.address || tokenAddress}
                                </p>
                            </div>
                            <div>
                                <h4 className="font-bold mb-2">{t('creator')}</h4>
                                <p className="break-all text-xs font-mono text-(--color-text-muted)">
                                    {token?.creatorAddress || t('unavailable')}
                                </p>
                            </div>
                            <div>
                                <h4 className="font-bold mb-2">{t('bondingCurve')}</h4>
                                <p className="break-all text-xs font-mono text-(--color-text-muted)">
                                    {token?.bondingCurve || t('unavailable')}
                                </p>
                            </div>
                            <div>
                                <h4 className="font-bold mb-2">{t('status')}</h4>
                                <p className="text-sm text-(--color-text-muted)">
                                    {statusLabel(token?.status || 'SYNCING')}
                                </p>
                            </div>
                        </div>

                        <div className="flex flex-wrap gap-3 text-sm">
                            {token?.website && (
                                <a
                                    href={token.website}
                                    target="_blank"
                                    rel="noreferrer"
                                    className="text-(--color-accent) hover:underline"
                                >
                                    {t('website')}
                                </a>
                            )}
                            {token?.twitter && (
                                <a
                                    href={token.twitter}
                                    target="_blank"
                                    rel="noreferrer"
                                    className="text-(--color-accent) hover:underline"
                                >
                                    {t('twitter')}
                                </a>
                            )}
                            {token?.telegram && (
                                <a
                                    href={token.telegram}
                                    target="_blank"
                                    rel="noreferrer"
                                    className="text-(--color-accent) hover:underline"
                                >
                                    {t('telegram')}
                                </a>
                            )}
                            {token?.discord && (
                                <a
                                    href={token.discord}
                                    target="_blank"
                                    rel="noreferrer"
                                    className="text-(--color-accent) hover:underline"
                                >
                                    {t('discord')}
                                </a>
                            )}
                        </div>
                    </div>
                )}
                {activeTab === 'trades' && <TradeHistory tokenAddress={tokenAddress} chainId={token?.chainId} />}
                {activeTab === 'holders' && <HolderDistribution tokenAddress={tokenAddress} chainId={token?.chainId} />}
                {activeTab === 'discussion' && (
                    <div className="py-10 text-center text-(--color-text-muted)">
                        {t('discussionNotConnected')}
                    </div>
                )}
            </div>
        </div>
    );
}
