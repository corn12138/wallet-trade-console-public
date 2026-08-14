'use client';

import Image from 'next/image';
import { useLocale, useTranslations } from 'next-intl';
import type { TokenData, TokenStats } from './token-detail.types';
import { formatAddress, formatCompactDecimal, formatPercent } from './token-detail.utils';
import { useEnumLabel } from '@/app/_atlas/enums';

interface TokenDetailHeaderProps {
    tokenAddress: string;
    token: TokenData | null;
    stats: TokenStats | null;
    currentPriceLabel: string;
}

export function TokenDetailHeader({
    tokenAddress,
    token,
    stats,
    currentPriceLabel,
}: TokenDetailHeaderProps) {
    const t = useTranslations('tokenDetail');
    const locale = useLocale();
    const statusLabel = useEnumLabel('token');
    const headerStats = [
        { labelKey: 'currentPrice', value: currentPriceLabel },
        { labelKey: 'change24h', value: formatPercent(stats?.priceChange24h ?? token?.priceChange24h) },
        { labelKey: 'marketCap', value: `${formatCompactDecimal(token?.marketCap, locale)} NATIVE` },
        { labelKey: 'volume24h', value: `${formatCompactDecimal(stats?.volume24h ?? token?.volume24h, locale)} NATIVE` },
    ] as const;

    return (
        <div
            style={{
                background: 'var(--color-background-secondary)',
                borderBottom: '1px solid var(--color-border)',
                padding: '0.75rem 1rem',
            }}
        >
            <div className="container flex flex-wrap items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <div
                        className="relative flex h-10 w-10 items-center justify-center overflow-hidden rounded-full"
                        style={{ background: 'var(--color-background-card)' }}
                    >
                        {token?.image ? (
                            <Image
                                src={token.image}
                                alt={token.symbol}
                                fill
                                className="object-cover"
                            />
                        ) : (
                            <span className="text-lg font-bold" style={{ color: 'var(--color-text-muted)' }}>
                                {token?.symbol?.charAt(0) || '?'}
                            </span>
                        )}
                    </div>

                    <div>
                        <div className="flex items-center gap-2">
                            <span className="font-bold" style={{ color: 'var(--color-text-primary)' }}>
                                {token?.symbol || t('loadingShort')}/NATIVE
                            </span>
                            <span
                                className="rounded px-2 py-0.5 text-xs"
                                style={{ background: 'var(--color-background-card)', color: 'var(--color-text-muted)' }}
                            >
                                {statusLabel(token?.status || 'SYNCING')}
                            </span>
                        </div>
                        <p className="text-sm" style={{ color: 'var(--color-text-muted)' }}>
                            {token?.name || t('loadingTokenMetadata')}
                        </p>
                    </div>

                    <span
                        className="rounded px-2 py-0.5 text-xs"
                        style={{ background: 'var(--color-background-card)', color: 'var(--color-text-muted)' }}
                    >
                        {formatAddress(token?.address || tokenAddress)}
                    </span>
                </div>

                <div className="flex flex-wrap items-center gap-6 text-sm">
                    {headerStats.map((stat) => (
                        <div key={stat.labelKey}>
                            <span style={{ color: 'var(--color-text-muted)' }}>{t(stat.labelKey)}</span>
                            <span className="ml-2" style={{ color: 'var(--color-text-primary)' }}>
                                {stat.value}
                            </span>
                        </div>
                    ))}
                </div>
            </div>
        </div>
    );
}
