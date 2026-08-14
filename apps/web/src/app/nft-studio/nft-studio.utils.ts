import type {
  IpfsMetadataDraft,
  MetadataAttribute,
  NftMetadataPreview,
} from './nft-studio.types';

const IPFS_CID_PATTERN = /^(Qm[1-9A-HJ-NP-Za-km-z]{44}|bafy[1-9A-HJ-NP-Za-km-z]+)(\/.*)?$/;

export function normalizeIpfsReference(value: string): string | undefined {
  const input = value.trim();
  if (!input) {
    return undefined;
  }

  if (input.startsWith('ipfs://')) {
    return input;
  }

  if (IPFS_CID_PATTERN.test(input)) {
    return `ipfs://${input.replace(/^\/+/, '')}`;
  }

  try {
    const url = new URL(input);
    const match = url.pathname.match(/\/ipfs\/(.+)$/);
    if (match?.[1]) {
      return `ipfs://${match[1]}`;
    }
    // Plain http(s) URLs are valid ERC-721 token/image URIs too — the media
    // upload service returns them (local/S3 providers). Pass them through
    // unchanged; only gateway /ipfs/ paths canonicalize to ipfs://.
    if (url.protocol === 'https:' || url.protocol === 'http:') {
      return url.toString();
    }
  } catch {
    return undefined;
  }

  return undefined;
}

export function parseMetadataAttributes(input: string): MetadataAttribute[] {
  return input
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const separatorIndex = line.indexOf(':');
      if (separatorIndex === -1) {
        return null;
      }

      const traitType = line.slice(0, separatorIndex).trim();
      const value = line.slice(separatorIndex + 1).trim();
      if (!traitType || !value) {
        return null;
      }

      return {
        trait_type: traitType,
        value,
      };
    })
    .filter((item): item is MetadataAttribute => item !== null);
}

export function buildIpfsMetadataPreview(
  draft: IpfsMetadataDraft
): NftMetadataPreview | undefined {
  const normalizedImage = normalizeIpfsReference(draft.imageReference);
  if (!normalizedImage) {
    return undefined;
  }

  const metadata: NftMetadataPreview = {
    name: draft.name.trim(),
    description: draft.description.trim(),
    image: normalizedImage,
  };

  const externalUrl = draft.externalUrl.trim();
  if (externalUrl) {
    metadata.external_url = externalUrl;
  }

  const attributes = parseMetadataAttributes(draft.attributesText);
  if (attributes.length > 0) {
    metadata.attributes = attributes;
  }

  return metadata;
}

export function buildMetadataFileName(name: string): string {
  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');

  return `${slug || 'nft-metadata'}.json`;
}

export function getDeployCommand(
  kind: 'onchain' | 'ipfs',
  chainId: number
): string {
  const isLocal = chainId === 31337;
  if (kind === 'onchain') {
    return isLocal
      ? 'pnpm --filter web3-contracts exec hardhat run scripts/deploy-onchain-art-nft.ts --network localhost'
      : 'pnpm --filter web3-contracts deploy:nft:sepolia';
  }

  return isLocal
    ? 'pnpm --filter web3-contracts exec hardhat run scripts/deploy-ipfs-art-nft.ts --network localhost'
    : 'pnpm --filter web3-contracts deploy:ipfs-nft:sepolia';
}

export function getTransactionErrorMessage(
  error: unknown,
  fallback: string
): string {
  if (!error) {
    return fallback;
  }

  if (error instanceof Error && error.message) {
    return error.message;
  }

  return fallback;
}
