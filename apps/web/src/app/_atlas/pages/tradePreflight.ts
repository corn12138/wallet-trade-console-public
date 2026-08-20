import { parseUnits } from 'viem';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';

/**
 * Pre-sign review input for `/trade`.
 *
 * Only the collateral approval is reviewable here, and that is a deliberate
 * limit rather than a partial job. `openPosition` / `closePosition` encode a
 * `deadline` derived from `Date.now()`, so their calldata changes on every
 * render: a review keyed on the transaction would re-run continuously and each
 * verdict would describe a deadline the wallet is never handed. Reviewing the
 * approval instead describes a transaction that is byte-stable and is the one
 * the next click actually sends whenever the allowance is short.
 *
 * `openPosition` silently diverts to an approve when the allowance does not
 * cover the collateral, which is why `isCollateralApproved` is load-bearing:
 * without it the strip would claim to review a position open while the wallet
 * was being asked for an ERC20 approval.
 */
export interface PerpApprovalPreflightParams {
    userAddress?: `0x${string}`;
    /** Collateral token — the perp USDC on this chain. */
    usdc?: `0x${string}`;
    /** Spender — the PositionManager that pulls the collateral. */
    positionManager?: `0x${string}`;
    chainId: number;
    /** Collateral as typed in the ticket, in display units. */
    collateralAmount: string;
    isCollateralApproved: (amount: bigint) => boolean;
}

export const PERP_COLLATERAL_DECIMALS = 18;

export function buildPerpApprovalPreflightInput({
    userAddress,
    usdc,
    positionManager,
    chainId,
    collateralAmount,
    isCollateralApproved,
}: PerpApprovalPreflightParams): AtlasTxReviewInput | null {
    if (!userAddress || !usdc || !positionManager) {
        return null;
    }

    let collateralWei: bigint;
    try {
        collateralWei = parseUnits(collateralAmount || '0', PERP_COLLATERAL_DECIMALS);
    } catch {
        return null;
    }
    if (collateralWei <= 0n) {
        return null;
    }

    // Already approved → the next click opens a position, and the approval
    // review would be about a transaction that will not be sent.
    if (isCollateralApproved(collateralWei)) {
        return null;
    }

    return {
        operationType: 'approve',
        fromAddress: userAddress,
        chainId,
        tokenAddress: usdc,
        spender: positionManager,
        // The ticket approves exactly the collateral — not an unlimited
        // allowance — so the review is told the same bounded amount and the
        // server re-encodes the identical approve(positionManager, collateral).
        amount: collateralAmount,
        tokenDecimals: PERP_COLLATERAL_DECIMALS,
    };
}
