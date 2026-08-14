import type { ContractAddressesByChain } from './contract-addresses.generated';

export interface ChainTokenConfig {
  symbol: string;
  name: string;
  decimals: number;
  address: `0x${string}`;
  logoURI: string;
}

interface TokenTemplate {
  contractName: string;
  symbol: string;
  name: string;
  decimals: number;
  logoURI: string;
}

const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000';

const TOKEN_TEMPLATES: readonly TokenTemplate[] = [
  {
    contractName: 'MockUSDC',
    symbol: 'mUSDC',
    name: 'Mock USDC',
    decimals: 18,
    logoURI:
      'https://assets.coingecko.com/coins/images/6319/thumb/USD_Coin_icon.png?1547042389',
  },
  {
    contractName: 'MockWETH',
    symbol: 'mWETH',
    name: 'Mock Wrapped Ether',
    decimals: 18,
    logoURI:
      'https://assets.coingecko.com/coins/images/2518/thumb/weth.png?1628852295',
  },
  {
    contractName: 'MockWBTC',
    symbol: 'mWBTC',
    name: 'Mock Wrapped Bitcoin',
    decimals: 8,
    logoURI:
      'https://assets.coingecko.com/coins/images/7598/thumb/wrapped_bitcoin_wbtc.png?1548822744',
  },
  {
    contractName: 'tokenA',
    symbol: 'TKA',
    name: 'Token A',
    decimals: 18,
    logoURI: 'https://api.dicebear.com/7.x/identicon/svg?seed=TKA',
  },
  {
    contractName: 'tokenB',
    symbol: 'TKB',
    name: 'Token B',
    decimals: 18,
    logoURI: 'https://api.dicebear.com/7.x/identicon/svg?seed=TKB',
  },
] as const;

function isConfiguredAddress(address: string | undefined): address is `0x${string}` {
  return Boolean(address && address !== ZERO_ADDRESS && /^0x[a-fA-F0-9]{40}$/.test(address));
}

function buildChainTokenList(chainAddresses: Record<string, string> | undefined): ChainTokenConfig[] {
  if (!chainAddresses) {
    return [];
  }

  const seen = new Set<string>();
  const tokens: ChainTokenConfig[] = [];

  for (const template of TOKEN_TEMPLATES) {
    const address = chainAddresses[template.contractName];
    if (!isConfiguredAddress(address)) {
      continue;
    }

    const normalizedAddress = address.toLowerCase();
    if (seen.has(normalizedAddress)) {
      continue;
    }

    seen.add(normalizedAddress);
    tokens.push({
      symbol: template.symbol,
      name: template.name,
      decimals: template.decimals,
      address,
      logoURI: template.logoURI,
    });
  }

  return tokens;
}

export function buildTokensByChain(
  contractAddressesByChain: ContractAddressesByChain,
): Record<number, ChainTokenConfig[]> {
  const tokensByChain: Record<number, ChainTokenConfig[]> = {};

  for (const [chainIdValue, chainAddresses] of Object.entries(contractAddressesByChain)) {
    tokensByChain[Number(chainIdValue)] = buildChainTokenList(chainAddresses);
  }

  return tokensByChain;
}

export function populateTokensByChain(
  target: Record<number, ChainTokenConfig[]>,
  contractAddressesByChain: ContractAddressesByChain,
): Record<number, ChainTokenConfig[]> {
  const nextValue = buildTokensByChain(contractAddressesByChain);

  for (const key of Object.keys(target)) {
    delete target[Number(key)];
  }

  for (const [chainIdValue, chainTokens] of Object.entries(nextValue)) {
    target[Number(chainIdValue)] = chainTokens;
  }

  return target;
}
