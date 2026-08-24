import { formatUnits } from 'viem';

export type TradeOrderType = 'market' | 'limit';
export type TradeOrderNoticeTone = 'info' | 'warning' | 'error';

export function calculatePnl(
    size: bigint,
    averagePrice: bigint,
    currentPrice: bigint,
    isLong: boolean
): { hasProfit: boolean; delta: bigint } {
    if (averagePrice === 0n) {
        return { hasProfit: false, delta: 0n };
    }

    if (currentPrice > averagePrice) {
        const delta = ((currentPrice - averagePrice) * size) / averagePrice;
        return { hasProfit: isLong, delta };
    }

    const delta = ((averagePrice - currentPrice) * size) / averagePrice;
    return { hasProfit: !isLong, delta };
}

export function formatUsd30(value: bigint) {
    return Number(formatUnits(value, 30)).toLocaleString(undefined, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
    });
}

export function formatTradingPrice(value: string) {
    return formatTradingAmount(value, 30, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
    });
}

/**
 * A trading price as a plain input-ready decimal string ("1999.00"), without
 * locale grouping — for pre-filling the limit-price field from a book row.
 */
export function formatTradingPriceInput(value: string) {
    const numericValue = parseTradingNumericString(value, 30);
    return Number.isFinite(numericValue) ? numericValue.toFixed(2) : '';
}

/** A raw 30-decimal (or human) trading amount string as a plain number. */
export function tradingAmountToNumber(value: string) {
    const numericValue = parseTradingNumericString(value, 30);
    return Number.isFinite(numericValue) ? numericValue : 0;
}

export function formatTradingUsdAmount(value: string) {
    return formatTradingAmount(value, 30, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
    });
}

export function formatTradingCompactUsdAmount(value: string) {
    return formatTradingAmount(value, 30, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
        notation: 'compact',
    });
}

export function formatTradingPercentRate(value: string, decimals = 25) {
    const numericValue = parseTradingNumericString(value, decimals);

    if (!Number.isFinite(numericValue)) {
        return '0.00';
    }

    return numericValue.toFixed(2);
}

export function formatTradingTokenAmount(value: string) {
    return formatTradingAmount(value, 30, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 4,
    });
}

export function evaluateTradeOrder(input: {
    address?: `0x${string}`;
    amount: string;
    balance: string;
    leverage: number;
    markPrice: string;
    isLong: boolean;
    orderType: TradeOrderType;
    limitPrice: string;
}) {
    const amountNumber = parseHumanNumericString(input.amount);
    const balanceNumber = parseHumanNumericString(input.balance);
    const markPriceNumber = parseHumanNumericString(input.markPrice);
    const limitPriceNumber = parseHumanNumericString(input.limitPrice);

    return {
        displayPrice: formatHumanUsd(markPriceNumber),
        liquidationPrice: calculateLiquidationPrice(markPriceNumber, input.leverage, input.isLong),
        feeEstimate: amountNumber > 0 ? (amountNumber * input.leverage * 0.001).toFixed(2) : '0.00',
        positionSize: amountNumber > 0 ? formatHumanUsd(amountNumber * input.leverage) : '0.00',
        validationCode: getTradeValidationCode({
            ...input,
            amountNumber,
            balanceNumber,
            markPriceNumber,
            limitPriceNumber,
        }),
        notice: getTradeOrderNotice({
            ...input,
            amountNumber,
            markPriceNumber,
            limitPriceNumber,
        }),
        canSubmit: Boolean(
            input.address &&
            amountNumber > 0 &&
            markPriceNumber > 0 &&
            amountNumber <= balanceNumber &&
            isTradeLimitSatisfied(input.orderType, input.isLong, markPriceNumber, limitPriceNumber),
        ),
    };
}

function formatTradingAmount(
    value: string,
    decimals: number,
    options: Intl.NumberFormatOptions,
) {
    const numericValue = parseTradingNumericString(value, decimals);

    if (!Number.isFinite(numericValue)) {
        return '0.00';
    }

    return numericValue.toLocaleString(undefined, options);
}

function parseTradingNumericString(value: string, decimals: number) {
    const trimmedValue = value.trim();

    if (!trimmedValue) {
        return 0;
    }

    if (looksLikeRawIntegerAmount(trimmedValue)) {
        return Number(formatUnits(BigInt(trimmedValue), decimals));
    }

    const numericValue = Number(trimmedValue);
    return Number.isFinite(numericValue) ? numericValue : 0;
}

function looksLikeRawIntegerAmount(value: string) {
    const normalizedValue = value.startsWith('-') ? value.slice(1) : value;
    return /^\d+$/.test(normalizedValue) && normalizedValue.length > 18;
}

function parseHumanNumericString(value: string) {
    const numericValue = Number(value);
    return Number.isFinite(numericValue) ? numericValue : 0;
}

function formatHumanUsd(value: number) {
    if (!Number.isFinite(value) || value <= 0) {
        return '—';
    }

    return value.toLocaleString(undefined, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
    });
}

function calculateLiquidationPrice(price: number, leverage: number, isLong: boolean) {
    if (!Number.isFinite(price) || price <= 0 || leverage <= 1) {
        return '—';
    }

    const liquidationPrice = isLong
        ? price * (1 - 1 / leverage)
        : price * (1 + 1 / leverage);

    return liquidationPrice.toFixed(2);
}

function getTradeValidationCode(input: {
    amount: string;
    balance: string;
    markPrice: string;
    limitPrice: string;
    isLong: boolean;
    orderType: TradeOrderType;
    amountNumber: number;
    balanceNumber: number;
    markPriceNumber: number;
    limitPriceNumber: number;
}) {
    if (input.markPriceNumber <= 0) {
        return 'oracle_unavailable';
    }

    if (input.amount.trim().length > 0 && input.amountNumber <= 0) {
        return 'invalid_amount';
    }

    if (input.amountNumber > input.balanceNumber) {
        return 'insufficient_balance';
    }

    if (input.orderType === 'limit') {
        if (input.limitPrice.trim().length > 0 && input.limitPriceNumber <= 0) {
            return 'invalid_limit_price';
        }

        if (input.limitPrice.trim().length > 0) {
            if (input.isLong && input.limitPriceNumber < input.markPriceNumber) {
                return 'limit_long_would_revert';
            }

            if (!input.isLong && input.limitPriceNumber > input.markPriceNumber) {
                return 'limit_short_would_revert';
            }
        }
    }

    return null;
}

function getTradeOrderNotice(input: {
    orderType: TradeOrderType;
    amountNumber: number;
    limitPrice: string;
    markPriceNumber: number;
    limitPriceNumber: number;
    isLong: boolean;
}) {
    if (input.orderType === 'market') {
        return {
            tone: 'info' as TradeOrderNoticeTone,
            code: 'market_execution',
        };
    }

    if (input.limitPrice.trim().length === 0) {
        return {
            tone: 'info' as TradeOrderNoticeTone,
            code: 'limit_price_required',
        };
    }

    if (input.limitPriceNumber > 0 && input.amountNumber > 0) {
        if (isTradeLimitSatisfied(input.orderType, input.isLong, input.markPriceNumber, input.limitPriceNumber)) {
            return {
                tone: 'warning' as TradeOrderNoticeTone,
                code: 'limit_executes_now',
            };
        }
    }

    return {
        tone: 'info' as TradeOrderNoticeTone,
        code: 'limit_guard',
    };
}

function isTradeLimitSatisfied(
    orderType: TradeOrderType,
    isLong: boolean,
    markPriceNumber: number,
    limitPriceNumber: number,
) {
    if (orderType === 'market') {
        return true;
    }

    if (limitPriceNumber <= 0 || markPriceNumber <= 0) {
        return false;
    }

    return isLong
        ? limitPriceNumber >= markPriceNumber
        : limitPriceNumber <= markPriceNumber;
}
