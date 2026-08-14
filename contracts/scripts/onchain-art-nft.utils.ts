import {
  getContractsDeploymentPath,
  persistDeploymentRecord,
  readDeploymentRecord,
} from "./deployment-artifacts.js";

export type OnchainArtDeployment = {
  network: string;
  chainId: number;
  deployer: string;
  timestamp: string;
  contracts: {
    OnchainArtworkNFT: {
      address: string;
      name: string;
      symbol: string;
      description: string;
      canvasColor: string;
    };
  };
};

export function getNftOption(name: string, fallback = ""): string {
  const flag = `--${name}`;
  const index = process.argv.indexOf(flag);
  if (index >= 0 && process.argv[index + 1]) {
    return process.argv[index + 1];
  }

  return process.env[`NFT_${name.toUpperCase()}`] || fallback;
}

export function getDeploymentFilePath(chainId: bigint): string {
  const suffix = chainId === 11155111n ? "sepolia" : String(chainId);
  return getContractsDeploymentPath(`onchain-art-nft-${suffix}.json`);
}

export function saveDeployment(chainId: bigint, deployment: OnchainArtDeployment): string {
  const suffix = chainId === 11155111n ? "sepolia" : String(chainId);
  return persistDeploymentRecord(`onchain-art-nft-${suffix}.json`, deployment).contractsPath;
}

export function resolveDeploymentAddress(chainId: bigint): string {
  const explicitAddress = getNftOption("contract");
  if (explicitAddress) {
    return explicitAddress;
  }

  const suffix = chainId === 11155111n ? "sepolia" : String(chainId);
  const deployment = readDeploymentRecord<OnchainArtDeployment>(`onchain-art-nft-${suffix}.json`);
  if (!deployment) {
    throw new Error(`Deployment file not found for chain ${suffix}`);
  }

  const address = deployment.contracts?.OnchainArtworkNFT?.address;
  if (!address) {
    throw new Error("OnchainArtworkNFT address missing in deployment file");
  }

  return address;
}
