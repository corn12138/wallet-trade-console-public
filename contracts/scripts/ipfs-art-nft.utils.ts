import {
  getContractsDeploymentPath,
  persistDeploymentRecord,
  readDeploymentRecord,
} from "./deployment-artifacts.js";

type IpfsArtworkDeployment = {
  network: string;
  chainId: number;
  deployer: string;
  timestamp: string;
  contracts: {
    IpfsArtworkNFT: {
      address: string;
      name: string;
      symbol: string;
    };
  };
};

const IPFS_CID_PATTERN = /^(Qm[1-9A-HJ-NP-Za-km-z]{44}|bafy[1-9A-HJ-NP-Za-km-z]+)(\/.*)?$/;

export function getIpfsNftOption(name: string, fallback = ""): string {
  const flag = `--${name}`;
  const index = process.argv.indexOf(flag);
  if (index >= 0 && process.argv[index + 1]) {
    return process.argv[index + 1];
  }

  return process.env[`NFT_${name.toUpperCase()}`] || fallback;
}

export function normalizeIpfsUri(value: string): string {
  const input = value.trim();
  if (!input) {
    throw new Error("IPFS value is required");
  }

  if (input.startsWith("ipfs://")) {
    return input;
  }

  if (IPFS_CID_PATTERN.test(input)) {
    return `ipfs://${input.replace(/^\/+/, "")}`;
  }

  try {
    const url = new URL(input);
    const match = url.pathname.match(/\/ipfs\/(.+)$/);
    if (match?.[1]) {
      return `ipfs://${match[1]}`;
    }
  } catch {
    throw new Error(`Unsupported IPFS reference: ${value}`);
  }

  throw new Error(`Unsupported IPFS reference: ${value}`);
}

export function getIpfsDeploymentFilePath(chainId: bigint): string {
  const suffix = chainId === 11155111n ? "sepolia" : String(chainId);
  return getContractsDeploymentPath(`ipfs-art-nft-${suffix}.json`);
}

export function saveIpfsDeployment(chainId: bigint, deployment: IpfsArtworkDeployment): string {
  const suffix = chainId === 11155111n ? "sepolia" : String(chainId);
  return persistDeploymentRecord(`ipfs-art-nft-${suffix}.json`, deployment).contractsPath;
}

export function resolveIpfsDeploymentAddress(chainId: bigint): string {
  const explicitAddress = getIpfsNftOption("contract");
  if (explicitAddress) {
    return explicitAddress;
  }

  const suffix = chainId === 11155111n ? "sepolia" : String(chainId);
  const deployment = readDeploymentRecord<IpfsArtworkDeployment>(`ipfs-art-nft-${suffix}.json`);
  if (!deployment) {
    throw new Error(`Deployment file not found for chain ${suffix}`);
  }

  const address = deployment.contracts?.IpfsArtworkNFT?.address;
  if (!address) {
    throw new Error("IpfsArtworkNFT address missing in deployment file");
  }

  return address;
}
