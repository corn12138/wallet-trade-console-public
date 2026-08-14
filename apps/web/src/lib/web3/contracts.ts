import { erc20Abi as viemErc20Abi } from 'viem';
import {
  applyContractConfigChains,
  CONTRACT_ABIS,
  CONTRACT_ADDRESSES,
  getContractAddress,
  getOptionalContractAddress,
  getPerpAddresses,
  getTokenFactoryAddress,
  TOKENS,
} from '@wallet-trade/shared';
import { buildApiUrl } from '../api/base-url';

export const erc20Abi = viemErc20Abi;
export const routerAbi = CONTRACT_ABIS.Router;
export const bridgeGatewayAbi = CONTRACT_ABIS.BridgeGateway;
export const tradingPairAbi = CONTRACT_ABIS.TradingPair;
export const tradingPairFactoryAbi = CONTRACT_ABIS.TradingPairFactory;

export {
  CONTRACT_ABIS,
  CONTRACT_ADDRESSES,
  getContractAddress,
  getOptionalContractAddress,
  getPerpAddresses,
  getTokenFactoryAddress,
  TOKENS,
};

/**
 * Sync contract addresses from server API.
 * Call this once on app startup to update addresses from deployment files.
 */
export async function syncContractAddresses(): Promise<void> {
  try {
    const res = await fetch(buildApiUrl('/contracts/config'));
    if (!res.ok) return;

    const config = await res.json();
    applyContractConfigChains(config?.chains);
  } catch {
    // Server not available, use hardcoded defaults
  }
}
