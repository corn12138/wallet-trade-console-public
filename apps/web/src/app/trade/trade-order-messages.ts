import type { TradeOrderNoticeTone } from './trade-page.utils';

interface TradeNoticePayload {
    tone: TradeOrderNoticeTone;
    message: string;
}

type TradingMessageGetter = (key: string) => string;

export function resolveTradeNotice(
    notice: { tone: TradeOrderNoticeTone; code: string } | null,
    validationCode: string | null,
    executionValidationCode: string | null,
    t: TradingMessageGetter,
): TradeNoticePayload | null {
    const blockingCode = validationCode ?? executionValidationCode;

    if (blockingCode) {
        return {
            tone: 'error',
            message: mapTradeMessage(blockingCode, t),
        };
    }

    if (!notice) {
        return null;
    }

    return {
        tone: notice.tone,
        message: mapTradeMessage(notice.code, t),
    };
}

function mapTradeMessage(code: string, t: TradingMessageGetter) {
    const messageMap: Record<string, string> = {
        market_execution: t('marketExecutionHint'),
        limit_price_required: t('limitPriceRequiredHint'),
        limit_guard: t('limitGuardHint'),
        limit_executes_now: t('limitExecutesNowHint'),
        oracle_unavailable: t('oracleUnavailable'),
        invalid_amount: t('invalidAmount'),
        insufficient_balance: t('insufficientBalance'),
        invalid_limit_price: t('invalidLimitPrice'),
        limit_long_would_revert: t('limitLongWouldRevert'),
        limit_short_would_revert: t('limitShortWouldRevert'),
        execution_settings_required: t('executionSettingsRequired'),
        invalid_slippage: t('invalidSlippage'),
        invalid_deadline: t('invalidDeadline'),
    };

    return messageMap[code] ?? '';
}
