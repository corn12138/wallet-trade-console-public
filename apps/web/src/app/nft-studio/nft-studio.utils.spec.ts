import { describe, expect, it } from 'vitest';
import {
  buildIpfsMetadataPreview,
  buildMetadataFileName,
  normalizeIpfsReference,
  parseMetadataAttributes,
} from './nft-studio.utils';

describe('nft-studio.utils', () => {
  it('normalizes a gateway URL into ipfs URI', () => {
    expect(
      normalizeIpfsReference(
        'https://tiny-amethyst-sawfish.myfilebase.com/ipfs/QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2'
      )
    ).toBe('ipfs://QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2');
  });

  it('parses metadata attributes from textarea input', () => {
    expect(parseMetadataAttributes('Track:Interview\nFormat:IPFS NFT')).toEqual([
      { trait_type: 'Track', value: 'Interview' },
      { trait_type: 'Format', value: 'IPFS NFT' },
    ]);
  });

  it('builds ipfs metadata preview with normalized image uri', () => {
    expect(
      buildIpfsMetadataPreview({
        recipient: '0x0',
        name: 'AI Mint Ticket Genesis',
        description: 'An interview-ready NFT.',
        imageReference: 'QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2',
        externalUrl: 'https://example.com/nft/1',
        attributesText: 'Track:Interview',
        metadataReference: '',
      })
    ).toEqual({
      name: 'AI Mint Ticket Genesis',
      description: 'An interview-ready NFT.',
      image: 'ipfs://QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2',
      external_url: 'https://example.com/nft/1',
      attributes: [{ trait_type: 'Track', value: 'Interview' }],
    });
  });

  it('creates a safe metadata filename', () => {
    expect(buildMetadataFileName('AI Mint Ticket Genesis')).toBe(
      'ai-mint-ticket-genesis.json'
    );
  });
});
