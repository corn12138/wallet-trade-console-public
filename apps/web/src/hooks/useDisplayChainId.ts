'use client';

import { useAccount, useChainId } from 'wagmi';
import { TOKENS } from '@wallet-trade/shared';
import { wagmiConfig } from '@/lib/web3/config';
import { DEFAULT_TRADING_CHAIN_ID } from '@/lib/web3/trading-chain';

/** Chain ids actually configured in the wagmi config (1 | 11155111 | 31337). */
export type DisplayChainId = (typeof wagmiConfig)['chains'][number]['id'];

/**
 * Chain id that read/display surfaces should use.
 *
 * With no wallet connected, wagmi reports the first configured chain
 * (mainnet), where none of the product contracts or indexed data exist — so
 * pages looked "empty" for a reason that had nothing to do with the product.
 * Reads default to Sepolia (the deployed testnet) in that case, matching the
 * resolveTradingChainId policy already used by /trade and /markets.
 *
 * Once a wallet IS connected we always honor its real chain: pages must show
 * an explicit switch-network action instead of silently reading another
 * chain's data, and writes always target the wallet's chain.
 */
export function useDisplayChainId(): { chainId: DisplayChainId; isFallback: boolean } {
  // This wagmi version types useChainId() as number even with the Register
  // augmentation, but at runtime it always reports one of the configured
  // chain ids, so a single narrow here is sound.
  const walletChainId = useChainId() as DisplayChainId;
  const { isConnected } = useAccount();

  if (isConnected) {
    return { chainId: walletChainId, isFallback: false };
  }

  const hasDeployedTokens = Boolean(TOKENS[walletChainId]?.length);
  if (hasDeployedTokens) {
    return { chainId: walletChainId, isFallback: false };
  }

  return { chainId: DEFAULT_TRADING_CHAIN_ID, isFallback: true };
}
