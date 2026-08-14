'use client';

/**
 * @file useContractConfig Hook
 * @description Fetches deployed contract addresses and ABIs from the server
 *              so the frontend doesn't hardcode them.
 *
 * Usage:
 *   const { config, getAddress, isLoading } = useContractConfig();
 *   const tokenFactory = getAddress('TokenFactory');
 */

import { useQuery } from '@tanstack/react-query';
import { buildApiUrl } from '@/lib/api/base-url';

interface ContractInfo {
    address: string;
    [key: string]: any;
}

interface ChainConfig {
    network: string;
    deployer?: string;
    timestamp?: string;
    contracts: Record<string, ContractInfo>;
}

interface ContractConfig {
    defaultChainId: number;
    chains: Record<number, ChainConfig>;
    abis: Record<string, any[]>;
}

export function useContractConfig(chainId: number = 11155111) {
    const { data: config, isLoading, error } = useQuery<ContractConfig>({
        queryKey: ['contractConfig'],
        queryFn: async () => {
            const res = await fetch(buildApiUrl('/contracts/config'));
            if (!res.ok) {
                throw new Error('Failed to fetch contract config');
            }
            return res.json();
        },
        staleTime: 5 * 60 * 1000, // Cache for 5 minutes
        retry: 2,
    });

    /**
     * Get contract address for current chain
     */
    const getAddress = (contractName: string): `0x${string}` | undefined => {
        const chain = config?.chains?.[chainId];
        if (!chain?.contracts?.[contractName]) return undefined;

        const addr = chain.contracts[contractName].address || chain.contracts[contractName];
        return typeof addr === 'string' ? (addr as `0x${string}`) : undefined;
    };

    /**
     * Get ABI for a contract
     */
    const getAbi = (contractName: string): any[] | undefined => {
        return config?.abis?.[contractName];
    };

    /**
     * Get all contracts for current chain
     */
    const getContracts = (): Record<string, ContractInfo> => {
        return config?.chains?.[chainId]?.contracts || {};
    };

    return {
        config,
        isLoading,
        error,
        getAddress,
        getAbi,
        getContracts,
    };
}
