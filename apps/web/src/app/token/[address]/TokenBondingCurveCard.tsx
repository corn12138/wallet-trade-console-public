'use client';

import { useTranslations } from 'next-intl';

interface TokenBondingCurveCardProps {
    progress: number;
    reserveLabel: string;
    targetReserveLabel: string;
}

export function TokenBondingCurveCard({
    progress,
    reserveLabel,
    targetReserveLabel,
}: TokenBondingCurveCardProps) {
    const t = useTranslations('tokenDetail');
    return (
        <div className="trade-panel">
            <div className="flex items-center justify-between mb-3">
                <span className="font-medium" style={{ color: 'var(--color-text-primary)' }}>
                    {t('bondingCurveProgress')}
                </span>
                <span style={{ color: 'var(--color-accent)' }}>{progress.toFixed(2)}%</span>
            </div>

            <div className="progress-bar mb-4">
                <div className="progress-bar-fill" style={{ width: `${progress}%` }} />
            </div>

            <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                    <span style={{ color: 'var(--color-text-muted)' }}>{t('reserveBalance')}</span>
                    <span style={{ color: 'var(--color-text-primary)' }}>{reserveLabel} NATIVE</span>
                </div>
                <div className="flex justify-between">
                    <span style={{ color: 'var(--color-text-muted)' }}>{t('launchTarget')}</span>
                    <span style={{ color: 'var(--color-text-primary)' }}>{targetReserveLabel} NATIVE</span>
                </div>
            </div>

            <p className="mt-4 text-xs" style={{ color: 'var(--color-text-muted)' }}>
                {t('bondingCurveNote')}
            </p>
        </div>
    );
}
