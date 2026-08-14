'use client';

import type { TradeNotice } from './token-detail.types';

interface TokenTradeStatusNoticeProps {
    notice: TradeNotice | null;
}

const toneStyles = {
    success: {
        borderColor: 'rgba(34, 197, 94, 0.35)',
        background: 'rgba(34, 197, 94, 0.08)',
        titleColor: 'var(--color-success)',
    },
    error: {
        borderColor: 'rgba(239, 68, 68, 0.35)',
        background: 'rgba(239, 68, 68, 0.08)',
        titleColor: 'var(--color-error)',
    },
    info: {
        borderColor: 'var(--color-border)',
        background: 'var(--color-background-secondary)',
        titleColor: 'var(--color-accent)',
    },
} as const;

export function TokenTradeStatusNotice({ notice }: TokenTradeStatusNoticeProps) {
    if (!notice) {
        return null;
    }

    const styles = toneStyles[notice.tone];

    return (
        <div
            className="rounded-lg border px-3 py-2"
            style={{ borderColor: styles.borderColor, background: styles.background }}
        >
            <p className="text-sm font-medium" style={{ color: styles.titleColor }}>
                {notice.title}
            </p>
            <p className="mt-1 text-sm" style={{ color: 'var(--color-text-secondary)' }}>
                {notice.message}
            </p>
        </div>
    );
}
