export interface DeploymentContractInfo {
    address?: string;
    [key: string]: unknown;
}

export interface DeploymentChainConfig {
    network?: string;
    deployer?: string;
    timestamp?: string;
    contracts: Record<string, string | DeploymentContractInfo>;
    sources: string[];
}

export interface ContractConfigPayload {
    defaultChainId: number;
    chains: Record<number, DeploymentChainConfig>;
    abis: Record<string, readonly unknown[]>;
}
