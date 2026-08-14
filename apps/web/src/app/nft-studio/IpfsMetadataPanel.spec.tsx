import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { IpfsMetadataPanel } from './IpfsMetadataPanel';
import { normalizeIpfsReference } from './nft-studio.utils';

/**
 * Upload → metadata → mint chain for NFT Studio: choosing a real artwork file
 * uploads it, the server-built metadata URL fills the mint reference, and the
 * mint call receives that returned URI (no manual pasting required).
 */

const mockUploadMediaFile = vi.fn();
const mockUploadNftMetadata = vi.fn();
const mockMint = vi.fn();

vi.mock('@/lib/api/media', () => ({
    uploadMediaFile: (f: File) => mockUploadMediaFile(f),
    uploadNftMetadata: (input: unknown) => mockUploadNftMetadata(input),
}));

vi.mock('@/components/web3', () => ({
    ConnectButton: () => null,
}));

vi.mock('next-intl', () => ({
    useTranslations: () => (key: string, values?: Record<string, string | number>) =>
        values?.message ? `${key}:${values.message}` : key,
}));

vi.mock('./useIpfsArtworkMint', () => ({
    useIpfsArtworkMint: () => ({
        mintIpfsArtwork: mockMint,
        mintedTokenId: null,
        notice: null,
        isBusy: false,
    }),
}));

vi.mock('./IpfsMetadataPreview', () => ({
    IpfsMetadataPreview: () => null,
}));

vi.mock('./NftStudioStatusNotice', () => ({
    NftStudioStatusNotice: () => null,
}));

const WALLET = '0x1111111111111111111111111111111111111111' as `0x${string}`;
const CONTRACT = '0x2222222222222222222222222222222222222222' as `0x${string}`;

describe('IpfsMetadataPanel upload-to-mint flow', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mockUploadMediaFile.mockResolvedValue({
            url: 'https://api.test/api/media/files/artwork.png',
        });
        mockUploadNftMetadata.mockResolvedValue({
            url: 'https://api.test/api/media/files/meta.json',
            metadata: { name: 'Piece' },
        });
    });

    function renderPanel() {
        return render(
            <IpfsMetadataPanel
                contractAddress={CONTRACT}
                walletAddress={WALLET}
                collectionOwner={WALLET}
                isConnected
            />,
        );
    }

    it('uploads artwork, stores server-built metadata, and mints with the returned URI', async () => {
        const { container } = renderPanel();

        fireEvent.change(screen.getByPlaceholderText('metadataNamePlaceholder'), {
            target: { value: 'Piece' },
        });
        const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
        const file = new File(['png'], 'artwork.png', { type: 'image/png' });
        fireEvent.change(fileInput, { target: { files: [file] } });

        fireEvent.click(screen.getByRole('button', { name: 'uploadArtworkAndMetadata' }));

        await waitFor(() => expect(mockUploadNftMetadata).toHaveBeenCalledTimes(1));
        expect(mockUploadMediaFile).toHaveBeenCalledWith(file);
        expect(mockUploadNftMetadata).toHaveBeenCalledWith(
            expect.objectContaining({
                name: 'Piece',
                image: 'https://api.test/api/media/files/artwork.png',
            }),
        );

        // The mint reference was filled with the RETURNED metadata URL…
        const metadataInput = screen.getByPlaceholderText('ipfs://metadataCID') as HTMLInputElement;
        await waitFor(() =>
            expect(metadataInput.value).toBe('https://api.test/api/media/files/meta.json'),
        );

        // …and minting passes exactly that URI through.
        fireEvent.click(screen.getByRole('button', { name: 'mintIpfs' }));
        expect(mockMint).toHaveBeenCalledWith({
            recipient: WALLET,
            metadataReference: 'https://api.test/api/media/files/meta.json',
        });
    });

    it('surfaces upload failures and never fills a fake reference', async () => {
        mockUploadMediaFile.mockRejectedValue(new Error('media storage is not configured'));
        const { container } = renderPanel();

        fireEvent.change(screen.getByPlaceholderText('metadataNamePlaceholder'), {
            target: { value: 'Piece' },
        });
        const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
        fireEvent.change(fileInput, {
            target: { files: [new File(['png'], 'artwork.png', { type: 'image/png' })] },
        });
        fireEvent.click(screen.getByRole('button', { name: 'uploadArtworkAndMetadata' }));

        await waitFor(() =>
            expect(
                screen.getByText('uploadFailed:media storage is not configured'),
            ).toBeInTheDocument(),
        );
        const metadataInput = screen.getByPlaceholderText('ipfs://metadataCID') as HTMLInputElement;
        expect(metadataInput.value).toBe('');
        expect(mockUploadNftMetadata).not.toHaveBeenCalled();
    });
});

describe('normalizeIpfsReference', () => {
    it('accepts ipfs URIs, CIDs, gateway paths, and plain https URLs', () => {
        expect(normalizeIpfsReference('ipfs://QmX')).toBe('ipfs://QmX');
        expect(
            normalizeIpfsReference('https://gw.test/ipfs/QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG'),
        ).toBe('ipfs://QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG');
        expect(normalizeIpfsReference('https://api.test/api/media/files/meta.json')).toBe(
            'https://api.test/api/media/files/meta.json',
        );
        expect(normalizeIpfsReference('ftp://nope')).toBeUndefined();
        expect(normalizeIpfsReference('not a uri')).toBeUndefined();
    });
});
