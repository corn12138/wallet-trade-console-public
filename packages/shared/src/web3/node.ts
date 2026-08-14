import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { DeploymentChainConfig, DeploymentContractInfo } from './types';

interface RawDeploymentFile {
    network?: string;
    chainId?: number;
    deployer?: string;
    timestamp?: string;
    deployedAt?: string;
    contracts?: Record<string, string | DeploymentContractInfo>;
}

interface DeploymentRecord {
    fileName: string;
    chainId: number;
    timestampMs: number;
    data: RawDeploymentFile;
}

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_DEPLOYMENTS_DIR = path.resolve(__dirname, '../../deployments');

function getTimestampMs(filePath: string, deployment: RawDeploymentFile): number {
    const rawTimestamp = deployment.timestamp || deployment.deployedAt;
    if (rawTimestamp) {
        const parsed = Date.parse(rawTimestamp);
        if (!Number.isNaN(parsed)) {
            return parsed;
        }
    }

    try {
        return fs.statSync(filePath).mtimeMs;
    } catch {
        return 0;
    }
}

function readDeploymentRecords(deploymentsDir: string): DeploymentRecord[] {
    if (!fs.existsSync(deploymentsDir)) {
        return [];
    }

    return fs
        .readdirSync(deploymentsDir)
        .filter((fileName: string) => fileName.endsWith('.json'))
        .map((fileName: string) => {
            try {
                const filePath = path.join(deploymentsDir, fileName);
                const data = JSON.parse(fs.readFileSync(filePath, 'utf-8')) as RawDeploymentFile;
                const chainId = Number(data.chainId);

                return {
                    fileName,
                    chainId,
                    timestampMs: getTimestampMs(filePath, data),
                    data,
                };
            } catch {
                return null;
            }
        })
        .filter((record: DeploymentRecord | null): record is DeploymentRecord => record !== null)
        .filter((record: DeploymentRecord) => Number.isInteger(record.chainId) && record.chainId > 0);
}

export function loadMergedDeployments(
    deploymentsDir: string = DEFAULT_DEPLOYMENTS_DIR,
): Record<number, DeploymentChainConfig> {
    const merged: Record<number, DeploymentChainConfig> = {};
    const records = readDeploymentRecords(deploymentsDir).sort((left, right) => {
        if (left.timestampMs !== right.timestampMs) {
            return left.timestampMs - right.timestampMs;
        }
        return left.fileName.localeCompare(right.fileName);
    });

    for (const record of records) {
        const current = merged[record.chainId] ?? {
            contracts: {},
            sources: [],
        };

        merged[record.chainId] = {
            network: record.data.network || current.network,
            deployer: record.data.deployer || current.deployer,
            timestamp: record.data.timestamp || record.data.deployedAt || current.timestamp,
            contracts: {
                ...current.contracts,
                ...(record.data.contracts || {}),
            },
            sources: [...current.sources, record.fileName],
        };
    }

    return merged;
}

export function getDeploymentAddress(
    chainConfig: DeploymentChainConfig | undefined,
    ...contractNames: string[]
): string | undefined {
    if (!chainConfig) {
        return undefined;
    }

    for (const contractName of contractNames) {
        const entry = chainConfig.contracts[contractName];
        if (typeof entry === 'string' && entry.length > 0) {
            return entry;
        }
        if (
            entry &&
            typeof entry === 'object' &&
            typeof entry.address === 'string' &&
            entry.address.length > 0
        ) {
            return entry.address;
        }
    }

    return undefined;
}

function toAddressLiteral(
    contractInfo: string | DeploymentContractInfo,
): string | undefined {
    if (typeof contractInfo === 'string' && contractInfo.length > 0) {
        return contractInfo;
    }

    if (
        contractInfo &&
        typeof contractInfo === 'object' &&
        typeof contractInfo.address === 'string' &&
        contractInfo.address.length > 0
    ) {
        return contractInfo.address;
    }

    return undefined;
}

export function buildGeneratedContractAddressesSource(
    deployments: Record<number, DeploymentChainConfig>,
): string {
    const chainEntries = Object.entries(deployments)
        .sort(([leftChainId], [rightChainId]) => Number(leftChainId) - Number(rightChainId))
        .map(([chainId, deployment]) => {
            const contractLines = Object.entries(deployment.contracts)
                .map(([contractName, contractInfo]) => {
                    const address = toAddressLiteral(contractInfo);
                    return address ? `    ${contractName}: '${address}',` : null;
                })
                .filter((line): line is string => line !== null)
                .sort((left, right) => left.localeCompare(right));

            const sourceComment = deployment.sources.join(', ');
            return `  ${chainId}: {\n${contractLines.join('\n')}\n  },${
                sourceComment ? ` // ${sourceComment}` : ''
            }`;
        });

    return `export type ContractAddressesByChain = Record<number, Record<string, string>>;\n\nexport const GENERATED_CONTRACT_ADDRESSES: ContractAddressesByChain = {\n${chainEntries.join(
        '\n',
    )}\n};\n`;
}

export function getDefaultDeploymentsDir(): string {
    return DEFAULT_DEPLOYMENTS_DIR;
}

export type { DeploymentChainConfig, DeploymentContractInfo } from './types';
