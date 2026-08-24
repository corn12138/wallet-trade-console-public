import { describe, expect, it } from 'vitest';
import { encodeFunctionData, maxUint256, parseEther } from 'viem';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import { buildTokenPreflightInput } from './token-preflight';

/**
 * The property under test is not "a review is requested" — it is that the
 * review describes the SAME transaction `/token`'s CTA submits. A verdict about
 * different calldata, a different value, or a smaller allowance than the one
 * actually granted is worse than no verdict at all, so each case re-encodes the
 * page's exact `writeContract` arguments and demands a byte-for-byte match.
 */

const USER = '0x1111111111111111111111111111111111111111' as const;
const TOKEN = '0x00000000000000000000000000000000000000aa';
const CURVE = '0x00000000000000000000000000000000000000bb' as const;
const CHAIN = 11155111;

const base = {
    userAddress: USER,
    tokenAddress: TOKEN,
    curveAddress: CURVE,
    chainId: CHAIN,
    needsApproval: false,
} as const;

describe('buildTokenPreflightInput', () => {
    it('reviews a buy as the exact payable buy(minTokens) call the page signs', () => {
        const parsedAmount = parseEther('0.5');
        const input = buildTokenPreflightInput({ ...base, parsedAmount, activeTab: 'buy' });

        expect(input).toEqual({
            operationType: 'custom',
            fromAddress: USER,
            chainId: CHAIN,
            tx: {
                to: CURVE,
                // Buying spends native value; a review with value 0 would
                // simulate a free call and miss an insufficient-balance revert.
                value: parsedAmount.toString(),
                data: encodeFunctionData({
                    abi: CONTRACT_ABIS.BondingCurve,
                    functionName: 'buy',
                    args: [0n],
                }),
            },
        });
    });

    it('reviews a sell as the exact sell(tokenAmount, minEth) call the page signs', () => {
        const parsedAmount = parseEther('12');
        const input = buildTokenPreflightInput({ ...base, parsedAmount, activeTab: 'sell' });

        expect(input).toEqual({
            operationType: 'custom',
            fromAddress: USER,
            chainId: CHAIN,
            tx: {
                to: CURVE,
                value: '0',
                data: encodeFunctionData({
                    abi: CONTRACT_ABIS.BondingCurve,
                    functionName: 'sell',
                    args: [parsedAmount, 0n],
                }),
            },
        });
    });

    it('reports the UNLIMITED allowance the sell approval actually grants', () => {
        const parsedAmount = parseEther('12');
        const input = buildTokenPreflightInput({
            ...base,
            parsedAmount,
            activeTab: 'sell',
            needsApproval: true,
        });

        // The page signs approve(curve, maxUint256). Sending the trade size
        // instead would let the strip call a bounded approval "safe" while the
        // wallet grants an uncapped one.
        expect(input).toEqual({
            operationType: 'approve',
            fromAddress: USER,
            chainId: CHAIN,
            tokenAddress: TOKEN,
            spender: CURVE,
            amount: maxUint256.toString(),
            tokenDecimals: 0,
        });
        expect(input?.amount).not.toBe(parsedAmount.toString());
    });

    it('reviews nothing until the form describes a real transaction', () => {
        const parsedAmount = parseEther('1');
        expect(buildTokenPreflightInput({ ...base, parsedAmount: null, activeTab: 'buy' })).toBeNull();
        expect(buildTokenPreflightInput({ ...base, parsedAmount: 0n, activeTab: 'buy' })).toBeNull();
        expect(
            buildTokenPreflightInput({ ...base, userAddress: undefined, parsedAmount, activeTab: 'buy' }),
        ).toBeNull();
        expect(
            buildTokenPreflightInput({ ...base, curveAddress: undefined, parsedAmount, activeTab: 'buy' }),
        ).toBeNull();
    });

    it('re-targets the review when the user flips buy/sell on the same amount', () => {
        const parsedAmount = parseEther('3');
        const buy = buildTokenPreflightInput({ ...base, parsedAmount, activeTab: 'buy' });
        const sell = buildTokenPreflightInput({ ...base, parsedAmount, activeTab: 'sell' });

        expect(buy?.tx?.data).not.toBe(sell?.tx?.data);
        expect(buy?.tx?.value).not.toBe(sell?.tx?.value);
    });
});
