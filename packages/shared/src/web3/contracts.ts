import { BondingCurveABI } from './abis/BondingCurve';
import { BridgeGatewayABI } from './abis/BridgeGateway';
import { IpfsArtworkNFTABI } from './abis/IpfsArtworkNFT';
import { LaunchTokenABI } from './abis/LaunchToken';
import { OnchainArtworkNFTABI } from './abis/OnchainArtworkNFT';
import { PerpMarketABI } from './abis/PerpMarket';
import { PerpOracleABI } from './abis/PerpOracle';
import { PerpVaultABI } from './abis/PerpVault';
import { PositionManagerABI } from './abis/PositionManager';
import { RouterABI } from './abis/Router';
import { StakingPoolABI } from './abis/StakingPool';
import { TokenFactoryABI } from './abis/TokenFactory';
import { TradingPairABI } from './abis/TradingPair';
import { TradingPairFactoryABI } from './abis/TradingPairFactory';
import {
    GENERATED_CONTRACT_ADDRESSES,
    type ContractAddressesByChain,
} from './contract-addresses.generated';
import type { DeploymentContractInfo } from './types';
import { populateTokensByChain, type ChainTokenConfig } from './tokens';

const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000';

const LOCALHOST_FALLBACK_ADDRESSES: Record<string, string> = {
    TokenFactory: '',
    StakingPool: '',
    StakingToken: '',
    router: '',
    PerpOracle: '',
    PerpVault: '',
    PerpMarket: '',
    PositionManager: '',
    MockUSDC: '',
    MockWETH: '',
    MockWBTC: '',
    OnchainArtworkNFT: '',
    IpfsArtworkNFT: '',
};

export const CONTRACT_ABIS = {
    TokenFactory: TokenFactoryABI,
    BridgeGateway: BridgeGatewayABI,
    LaunchToken: LaunchTokenABI,
    BondingCurve: BondingCurveABI,
    PositionManager: PositionManagerABI,
    PerpMarket: PerpMarketABI,
    PerpOracle: PerpOracleABI,
    PerpVault: PerpVaultABI,
    OnchainArtworkNFT: OnchainArtworkNFTABI,
    IpfsArtworkNFT: IpfsArtworkNFTABI,
    Router: RouterABI,
    StakingPool: StakingPoolABI,
    TradingPair: TradingPairABI,
    TradingPairFactory: TradingPairFactoryABI,
} as const;

function cloneGeneratedAddresses(): ContractAddressesByChain {
    return Object.fromEntries(
        Object.entries(GENERATED_CONTRACT_ADDRESSES).map(([chainId, contracts]) => [
            Number(chainId),
            { ...contracts },
        ]),
    ) as ContractAddressesByChain;
}

function readConfiguredAddress(
    value: string | DeploymentContractInfo | undefined,
): string | undefined {
    if (typeof value === 'string' && value.length > 0) {
        return value;
    }

    if (
        value &&
        typeof value === 'object' &&
        typeof value.address === 'string' &&
        value.address.length > 0
    ) {
        return value.address;
    }

    return undefined;
}

const clonedGeneratedAddresses = cloneGeneratedAddresses();

export const CONTRACT_ADDRESSES: ContractAddressesByChain = {
    ...clonedGeneratedAddresses,
    31337: {
        ...LOCALHOST_FALLBACK_ADDRESSES,
        ...(clonedGeneratedAddresses[31337] || {}),
    },
};

export const TOKENS: Record<number, ChainTokenConfig[]> = {};
populateTokensByChain(TOKENS, CONTRACT_ADDRESSES);

export function applyContractConfigChains(
    chains: Record<string | number, { contracts?: Record<string, string | DeploymentContractInfo> }> | undefined,
    target: ContractAddressesByChain = CONTRACT_ADDRESSES,
): ContractAddressesByChain {
    if (!chains) {
        return target;
    }

    for (const [chainIdValue, chainConfig] of Object.entries(chains)) {
        const chainId = Number(chainIdValue);
        if (!Number.isInteger(chainId)) {
            continue;
        }

        if (!target[chainId]) {
            target[chainId] = {};
        }

        for (const [contractName, contractInfo] of Object.entries(chainConfig.contracts || {})) {
            const address = readConfiguredAddress(contractInfo);
            if (address) {
                target[chainId][contractName] = address;
            }
        }
    }

    if (target === CONTRACT_ADDRESSES) {
        populateTokensByChain(TOKENS, CONTRACT_ADDRESSES);
    }

    return target;
}

function isConfiguredAddress(address: string | undefined): address is `0x${string}` {
    return Boolean(address && address !== ZERO_ADDRESS);
}

export function getOptionalContractAddress(
    chainId: number,
    name: string,
): `0x${string}` | undefined {
    const address = CONTRACT_ADDRESSES[chainId]?.[name];
    if (isConfiguredAddress(address)) {
        return address;
    }

    return undefined;
}

export function getTokenFactoryAddress(chainId: number): `0x${string}` | undefined {
    return getOptionalContractAddress(chainId, 'TokenFactory');
}

export function getContractAddress(chainId: number, name: string): `0x${string}` {
    return (getOptionalContractAddress(chainId, name) || ZERO_ADDRESS) as `0x${string}`;
}

export function getPerpAddresses(chainId: number) {
    const addresses = CONTRACT_ADDRESSES[chainId] || CONTRACT_ADDRESSES[31337] || {};

    return {
        oracle: (addresses.PerpOracle || '') as `0x${string}`,
        vault: (addresses.PerpVault || '') as `0x${string}`,
        market: (addresses.PerpMarket || '') as `0x${string}`,
        positionManager: (addresses.PositionManager || '') as `0x${string}`,
        usdc: (addresses.MockUSDC || '') as `0x${string}`,
        weth: (addresses.MockWETH || '') as `0x${string}`,
        wbtc: (addresses.MockWBTC || '') as `0x${string}`,
    };
}

export { ZERO_ADDRESS };
export type { ContractAddressesByChain } from './contract-addresses.generated';
