import { formatEther, parseEther } from 'viem';

export function formatAddress(address?: string | null) {
    if (!address) {
        return '—';
    }

    return `${address.slice(0, 6)}...${address.slice(-4)}`;
}

export function formatCompactNumber(value?: number | null, maximumFractionDigits = 2) {
    if (value === undefined || value === null || Number.isNaN(value)) {
        return '0';
    }

    return new Intl.NumberFormat('en-US', {
        notation: 'compact',
        maximumFractionDigits,
    }).format(value);
}

export function formatCompactDecimal(value: string | null | undefined, locale = 'en', maximumFractionDigits = 2) {
    if (!value) {
        return '0';
    }
    const n = Number(value);
    if (!Number.isFinite(n)) {
        return '0';
    }
    return new Intl.NumberFormat(locale === 'zh' ? 'zh-CN' : 'en-US', {
        notation: 'compact',
        maximumFractionDigits,
    }).format(n);
}

export function formatPercent(value?: number | null, maximumFractionDigits = 2) {
    if (value === undefined || value === null || Number.isNaN(value)) {
        return '0.00%';
    }

    const sign = value >= 0 ? '+' : '';

    return `${sign}${value.toFixed(maximumFractionDigits)}%`;
}

export function formatNativeAmount(value?: bigint | null, maximumFractionDigits = 4) {
    if (!value) {
        return '0';
    }

    const asNumber = Number(formatEther(value));

    return asNumber.toLocaleString('en-US', {
        maximumFractionDigits,
    });
}

export function parseOptionalEtherAmount(amount: string) {
    if (!amount) {
        return null;
    }

    const normalizedAmount = Number(amount);

    if (!Number.isFinite(normalizedAmount) || normalizedAmount <= 0) {
        return null;
    }

    try {
        return parseEther(amount);
    } catch {
        return null;
    }
}

export function shortenErrorMessage(message: string | null | undefined, fallback: string) {
    if (!message) {
        return fallback;
    }

    return message.length > 140 ? `${message.slice(0, 137)}...` : message;
}
