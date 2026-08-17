import { encodeFunctionData, maxUint256 } from 'viem';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';

/**
 * Builds the tx-review input for the bonding-curve trade panel.
 *
 * The hard rule this file exists to enforce: the review must describe the
 * transaction the page actually sends. `/token` encodes its own calldata
 * client-side via `writeContract`, so the buy/sell reviews are submitted as
 * `custom` with calldata encoded from the SAME abi + args — not as a
 * server-rebuilt approximation that could drift from what the wallet signs.
 *
 * `buy` and `sell` are both overloaded on BondingCurve (a 3-arg form carrying
 * deadline/price guards and a short form without). `encodeFunctionData`
 * resolves the overload from the arity of `args` exactly as `writeContract`
 * does, so passing the page's args verbatim reproduces its selector.
 */
export interface TokenPreflightParams {
    userAddress?: `0x${string}`;
    tokenAddress?: string;
    curveAddress?: `0x${string}`;
    chainId: number;
    /** Trade size in base units — null while the form is incomplete. */
    parsedAmount: bigint | null;
    activeTab: 'buy' | 'sell';
    /** True when the sell path must grant an allowance before it can trade. */
    needsApproval: boolean;
}

export function buildTokenPreflightInput({
    userAddress,
    tokenAddress,
    curveAddress,
    chainId,
    parsedAmount,
    activeTab,
    needsApproval,
}: TokenPreflightParams): AtlasTxReviewInput | null {
    if (!userAddress || !tokenAddress || !curveAddress || !parsedAmount || parsedAmount <= 0n) {
        return null;
    }

    if (activeTab === 'buy') {
        return {
            operationType: 'custom',
            fromAddress: userAddress,
            chainId,
            tx: {
                to: curveAddress,
                value: parsedAmount.toString(),
                data: encodeFunctionData({
                    abi: CONTRACT_ABIS.BondingCurve,
                    functionName: 'buy',
                    args: [0n],
                }),
            },
        };
    }

    if (needsApproval) {
        return {
            operationType: 'approve',
            fromAddress: userAddress,
            chainId,
            tokenAddress,
            spender: curveAddress,
            // The page approves maxUint256, so that — not the trade size — is
            // what the review is told. `tokenDecimals: 0` means "amount is
            // already in base units", which makes the server encode the exact
            // same approve(spender, 2^256-1) the wallet is asked to sign. Any
            // rescaling here would produce a verdict about a smaller allowance
            // than the one actually granted.
            amount: maxUint256.toString(),
            tokenDecimals: 0,
        };
    }

    return {
        operationType: 'custom',
        fromAddress: userAddress,
        chainId,
        tx: {
            to: curveAddress,
            value: '0',
            data: encodeFunctionData({
                abi: CONTRACT_ABIS.BondingCurve,
                functionName: 'sell',
                args: [parsedAmount, 0n],
            }),
        },
    };
}
