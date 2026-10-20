import { describe, expect, it } from 'vitest';
import { erc20Abi } from 'viem';
import { freezeStep } from '@/hooks/web3/txIntent/intent';
import { buildTradePreflightInput } from './tradePreflight';

describe('trade preflight binds to the frozen request', () => {
  it('preserves exact bytes and target identity without re-encoding', () => {
    const step = freezeStep({ kind: 'approve', actionType: 'approve',
      owner: '0x0000000000000000000000000000000000000001', targetChainId: 11155111,
      target: '0x0000000000000000000000000000000000000002',
      abi: erc20Abi, functionName: 'approve',
      args: ['0x0000000000000000000000000000000000000003', 7n], expectsIndexing: false, now: Date.now() });
    expect(buildTradePreflightInput(step)).toEqual({ operationType: 'custom',
      fromAddress: step.owner, chainId: step.targetChainId,
      tx: { to: step.target, value: '0', data: step.calldata } });
  });
});
