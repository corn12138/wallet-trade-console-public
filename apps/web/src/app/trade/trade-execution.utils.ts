export function evaluateTradeExecutionSettings(input: {
    slippagePercent: string;
    deadlineMinutes: string;
}) {
    const slippagePercentValue = parseExecutionNumber(input.slippagePercent);
    const deadlineMinutesValue = parseExecutionNumber(input.deadlineMinutes);

    return {
        slippagePercentValue,
        deadlineMinutesValue,
        validationCode: getExecutionValidationCode({
            slippagePercent: input.slippagePercent,
            deadlineMinutes: input.deadlineMinutes,
            slippagePercentValue,
            deadlineMinutesValue,
        }),
    };
}

function getExecutionValidationCode(input: {
    slippagePercent: string;
    deadlineMinutes: string;
    slippagePercentValue: number;
    deadlineMinutesValue: number;
}) {
    if (input.slippagePercent.trim().length === 0 || input.deadlineMinutes.trim().length === 0) {
        return 'execution_settings_required';
    }

    if (!Number.isFinite(input.slippagePercentValue) || input.slippagePercentValue < 0 || input.slippagePercentValue > 5) {
        return 'invalid_slippage';
    }

    if (!Number.isFinite(input.deadlineMinutesValue) || input.deadlineMinutesValue < 1 || input.deadlineMinutesValue > 120) {
        return 'invalid_deadline';
    }

    return null;
}

function parseExecutionNumber(value: string) {
    const parsedValue = Number(value);
    return Number.isFinite(parsedValue) ? parsedValue : Number.NaN;
}
