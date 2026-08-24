import { describe, expect, it, vi } from 'vitest';
import { parseUnits } from 'viem';
import { buildPerpApprovalPreflightInput, PERP_COLLATERAL_DECIMALS } from './tradePreflight';

/**
 * `/trade`'s submit button sends one of two different transactions depending on
 * the current allowance, and `openPosition` makes that choice internally. The
 * property pinned here is that the review follows the same branch: it describes
 * the approval only while an approval is what the next click sends, and stays
 * silent once the click would open a position instead.
 */

const USER = '0x1111111111111111111111111111111111111111' as const;
const USDC = '0x00000000000000000000000000000000000000aa' as const;
const POSITION_MANAGER = '0x00000000000000000000000000000000000000bb' as const;
const CHAIN = 11155111;

const never = () => false;
const always = () => true;

const base = {
    userAddress: USER,
    usdc: USDC,
    positionManager: POSITION_MANAGER,
    chainId: CHAIN,
} as const;

describe('buildPerpApprovalPreflightInput', () => {
    it('reviews the bounded collateral approval the ticket actually signs', () => {
        const input = buildPerpApprovalPreflightInput({
            ...base,
            collateralAmount: '250.5',
            isCollateralApproved: never,
        });

        expect(input).toEqual({
            operationType: 'approve',
            fromAddress: USER,
            chainId: CHAIN,
            tokenAddress: USDC,
            spender: POSITION_MANAGER,
            amount: '250.5',
            tokenDecimals: PERP_COLLATERAL_DECIMALS,
        });
    });

    it('passes the collateral through so the server re-encodes the same amount', () => {
        const isCollateralApproved = vi.fn(() => false);
        const input = buildPerpApprovalPreflightInput({
            ...base,
            collateralAmount: '7',
            isCollateralApproved,
        });

        // The allowance question is asked in base units — the same conversion
        // `openPosition` performs before calling approve.
        expect(isCollateralApproved).toHaveBeenCalledWith(parseUnits('7', PERP_COLLATERAL_DECIMALS));
        // A bounded amount, never an unlimited allowance smuggled in.
        expect(input?.amount).toBe('7');
    });

    it('goes silent once the allowance covers the trade', () => {
        // The next click opens a position; an approval verdict would describe a
        // transaction the wallet will never be handed.
        expect(
            buildPerpApprovalPreflightInput({
                ...base,
                collateralAmount: '10',
                isCollateralApproved: always,
            }),
        ).toBeNull();
    });

    it('reviews nothing until the ticket describes a real transaction', () => {
        const cases = [
            { ...base, collateralAmount: '', isCollateralApproved: never },
            { ...base, collateralAmount: '0', isCollateralApproved: never },
            { ...base, collateralAmount: 'not-a-number', isCollateralApproved: never },
            { ...base, userAddress: undefined, collateralAmount: '5', isCollateralApproved: never },
            { ...base, usdc: undefined, collateralAmount: '5', isCollateralApproved: never },
            { ...base, positionManager: undefined, collateralAmount: '5', isCollateralApproved: never },
        ];

        for (const c of cases) {
            expect(buildPerpApprovalPreflightInput(c)).toBeNull();
        }
    });
});
