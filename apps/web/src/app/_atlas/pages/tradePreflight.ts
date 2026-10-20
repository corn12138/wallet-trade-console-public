import type { AtlasTxReviewInput } from '@/lib/api/atlas';
import type { TxStep } from '@/hooks/web3/txIntent/types';

/** Simulate the reviewed bytes, including their frozen deadline and exact approval amount. */
export function buildTradePreflightInput(step: TxStep): AtlasTxReviewInput {
  return {
    operationType: 'custom',
    fromAddress: step.owner,
    chainId: step.targetChainId,
    tx: { to: step.target, value: step.value.toString(), data: step.calldata },
  };
}
