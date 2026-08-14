'use client';

import { buildApiUrl } from '@/lib/api/base-url';
import { useQuery } from '@tanstack/react-query';
import { useLocale, useTranslations } from 'next-intl';
import Image from 'next/image';
import Link from 'next/link';
import { useState } from 'react';
import type { SidebarToken } from './token-detail.types';
import { formatCompactDecimal, formatPercent } from './token-detail.utils';

type SidebarTab = 'trending' | 'newest';

const sidebarTabs: Record<SidebarTab, { labelKey: 'sidebarTrending' | 'sidebarNewest'; query: string }> = {
    trending: {
        labelKey: 'sidebarTrending',
        query: 'sortBy=trending&limit=6',
    },
    newest: {
        labelKey: 'sidebarNewest',
        query: 'sortBy=newest&limit=6',
    },
};

interface TokenDetailSidebarProps {
    currentAddress: string;
}

interface TokenListResponse {
    data: SidebarToken[];
}

export function TokenDetailSidebar({ currentAddress }: TokenDetailSidebarProps) {
    const t = useTranslations('tokenDetail');
    const locale = useLocale();
    const [activeTab, setActiveTab] = useState<SidebarTab>('trending');

    const { data, isLoading } = useQuery({
        queryKey: ['token-detail-sidebar', activeTab, currentAddress],
        queryFn: async () => {
            const response = await fetch(buildApiUrl(`/token?${sidebarTabs[activeTab].query}`));

            if (!response.ok) {
                return [] as SidebarToken[];
            }

            const payload = await response.json() as TokenListResponse;

            return payload.data.filter((token) => token.address && token.address !== currentAddress);
        },
    });

    return (
        <aside
            className="token-sidebar"
            style={{ maxHeight: 'calc(100vh - 200px)', overflowY: 'auto' }}
        >
            <div className="flex" style={{ borderBottom: '1px solid var(--color-border)' }}>
                {(Object.entries(sidebarTabs) as Array<[SidebarTab, typeof sidebarTabs[SidebarTab]]>).map(([key, item]) => (
                    <button
                        key={key}
                        onClick={() => setActiveTab(key)}
                        className="flex-1 py-3 text-sm font-medium"
                        style={{
                            color: activeTab === key ? 'var(--color-text-primary)' : 'var(--color-text-muted)',
                            borderBottom: activeTab === key ? '2px solid var(--color-accent)' : 'none',
                        }}
                    >
                        {t(item.labelKey)}
                    </button>
                ))}
            </div>

            <div className="px-3 py-2 text-xs" style={{ color: 'var(--color-text-muted)', borderBottom: '1px solid var(--color-border)' }}>
                {t('sidebarSourceNote')}
            </div>

            {isLoading ? (
                <div className="px-4 py-6 text-sm" style={{ color: 'var(--color-text-muted)' }}>
                    {t('loadingRelatedMarkets')}
                </div>
            ) : data && data.length > 0 ? (
                data.map((token, index) => (
                    <Link
                        key={token.id}
                        href={`/token/${token.address}`}
                        className="token-sidebar-item"
                    >
                        <span className="text-xs" style={{ color: 'var(--color-text-muted)' }}>
                            {index + 1}
                        </span>
                        <div
                            className="relative flex h-8 w-8 items-center justify-center overflow-hidden rounded-full"
                            style={{ background: 'var(--color-background-hover)' }}
                        >
                            {token.image ? (
                                <Image
                                    src={token.image}
                                    alt={token.symbol}
                                    fill
                                    className="object-cover"
                                />
                            ) : (
                                <span className="text-sm font-bold" style={{ color: 'var(--color-text-muted)' }}>
                                    {token.symbol.charAt(0)}
                                </span>
                            )}
                        </div>
                        <div className="min-w-0 flex-1">
                            <p className="truncate text-sm font-medium" style={{ color: 'var(--color-text-primary)' }}>
                                {token.symbol}
                            </p>
                            <p className="truncate text-xs" style={{ color: 'var(--color-text-muted)' }}>
                                {token.name}
                            </p>
                        </div>
                        <div className="text-right text-xs">
                            <div
                                style={{
                                    color: (token.priceChange24h || 0) >= 0
                                        ? 'var(--color-success)'
                                        : 'var(--color-error)',
                                }}
                            >
                                {formatPercent(token.priceChange24h, 1)}
                            </div>
                            <div style={{ color: 'var(--color-text-muted)' }}>
                                MC {formatCompactDecimal(token.marketCap, locale)} NATIVE
                            </div>
                        </div>
                    </Link>
                ))
            ) : (
                <div className="px-4 py-6 text-sm" style={{ color: 'var(--color-text-muted)' }}>
                    {t('noRelatedMarkets')}
                </div>
            )}
        </aside>
    );
}
