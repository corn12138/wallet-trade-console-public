import { mainnet, sepolia } from 'wagmi/chains';

const EXPLORER_BASE_URL_BY_CHAIN_ID: Record<number, string> = {
  [mainnet.id]: mainnet.blockExplorers.default.url,
  [sepolia.id]: sepolia.blockExplorers.default.url,
  534352: 'https://scrollscan.com',
  534351: 'https://sepolia.scrollscan.com',
};

function getExplorerBaseUrl(chainId?: number): string | null {
  if (!chainId) {
    return EXPLORER_BASE_URL_BY_CHAIN_ID[sepolia.id];
  }

  return EXPLORER_BASE_URL_BY_CHAIN_ID[chainId] ?? null;
}

function buildExplorerUrl(pathname: string, value: string, chainId?: number): string | null {
  const baseUrl = getExplorerBaseUrl(chainId);
  if (!baseUrl) {
    return null;
  }

  return `${baseUrl}/${pathname}/${value}`;
}

export function buildAddressExplorerUrl(address: string, chainId?: number): string | null {
  return buildExplorerUrl('address', address, chainId);
}

export function buildTransactionExplorerUrl(txHash: string, chainId?: number): string | null {
  return buildExplorerUrl('tx', txHash, chainId);
}
