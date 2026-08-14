export interface SubmissionNotice {
  tone: 'idle' | 'info' | 'success' | 'error';
  message: string;
}

export interface OnchainArtworkDraft {
  recipient: string;
  title: string;
  caption: string;
  accentColor: string;
}

export interface IpfsMetadataDraft {
  recipient: string;
  name: string;
  description: string;
  imageReference: string;
  externalUrl: string;
  attributesText: string;
  metadataReference: string;
}

export interface MetadataAttribute {
  trait_type: string;
  value: string;
}

export interface NftMetadataPreview {
  name: string;
  description: string;
  image: string;
  external_url?: string;
  attributes?: MetadataAttribute[];
}

export const INITIAL_ONCHAIN_ARTWORK_DRAFT: OnchainArtworkDraft = {
  recipient: '',
  title: 'Genesis Ticket',
  caption: 'Minted from the MetaLand NFT Studio',
  accentColor: '#38bdf8',
};

export const INITIAL_IPFS_METADATA_DRAFT: IpfsMetadataDraft = {
  recipient: '',
  name: 'AI Mint Ticket Genesis',
  description: 'An IPFS-backed ERC721 minted from the MetaLand NFT Studio.',
  imageReference: '',
  externalUrl: '',
  attributesText: 'Track:Interview\nFormat:IPFS NFT',
  metadataReference: '',
};
