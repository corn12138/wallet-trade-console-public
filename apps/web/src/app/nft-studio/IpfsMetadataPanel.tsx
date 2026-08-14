'use client';

import { ConnectButton } from '@/components/web3';
import { uploadMediaFile, uploadNftMetadata } from '@/lib/api/media';
import { useTranslations } from 'next-intl';
import { useEffect, useState } from 'react';
import { IpfsMetadataPreview } from './IpfsMetadataPreview';
import { NftStudioStatusNotice } from './NftStudioStatusNotice';
import {
  INITIAL_IPFS_METADATA_DRAFT,
  type IpfsMetadataDraft,
} from './nft-studio.types';
import {
  buildIpfsMetadataPreview,
  buildMetadataFileName,
  normalizeIpfsReference,
  parseMetadataAttributes,
} from './nft-studio.utils';
import { useIpfsArtworkMint } from './useIpfsArtworkMint';

interface IpfsMetadataPanelProps {
  contractAddress?: `0x${string}`;
  walletAddress?: `0x${string}`;
  collectionOwner?: string;
  isConnected: boolean;
}

export function IpfsMetadataPanel({
  contractAddress,
  walletAddress,
  collectionOwner,
  isConnected,
}: IpfsMetadataPanelProps) {
  const t = useTranslations('nftStudio');
  const [draft, setDraft] = useState<IpfsMetadataDraft>(INITIAL_IPFS_METADATA_DRAFT);
  const [helperMessage, setHelperMessage] = useState<string | null>(null);
  const [artworkFile, setArtworkFile] = useState<File | null>(null);
  const [isUploading, setIsUploading] = useState(false);

  const { mintIpfsArtwork, mintedTokenId, notice, isBusy } = useIpfsArtworkMint({
    contractAddress,
    isConnected,
    walletAddress,
    t,
  });

  useEffect(() => {
    if (!walletAddress) {
      return;
    }

    setDraft((current) => {
      if (current.recipient) {
        return current;
      }

      return {
        ...current,
        recipient: walletAddress,
      };
    });
  }, [walletAddress]);

  const isOwner =
    !collectionOwner ||
    !walletAddress ||
    collectionOwner.toLowerCase() === walletAddress.toLowerCase();

  const metadata = buildIpfsMetadataPreview(draft);
  const metadataJson = metadata ? JSON.stringify(metadata, null, 2) : '';
  const normalizedImageUri = normalizeIpfsReference(draft.imageReference);
  const normalizedMetadataUri = normalizeIpfsReference(draft.metadataReference);
  const imageError =
    draft.imageReference.trim() && !normalizedImageUri
      ? t('invalidIpfsImageReference')
      : undefined;

  const downloadMetadata = () => {
    if (!metadata) {
      setHelperMessage(t('metadataPreviewUnavailable'));
      return;
    }

    const blob = new Blob([metadataJson], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = buildMetadataFileName(draft.name);
    link.click();
    URL.revokeObjectURL(url);
    setHelperMessage(t('downloadReady'));
  };

  const copyMetadata = async () => {
    if (!metadata) {
      setHelperMessage(t('metadataPreviewUnavailable'));
      return;
    }

    await navigator.clipboard.writeText(metadataJson);
    setHelperMessage(t('copiedMetadata'));
  };

  // Normal path: upload the selected artwork, let the server build + store
  // the ERC-721 metadata JSON, and fill the mint reference with the returned
  // durable URI. The manual metadataReference field stays as an advanced
  // override for externally pinned metadata.
  const uploadArtworkAndMetadata = async () => {
    if (!artworkFile || !draft.name.trim()) {
      setHelperMessage(t('artworkFileRequired'));
      return;
    }
    setIsUploading(true);
    setHelperMessage(t('uploadingArtwork'));
    try {
      const artwork = await uploadMediaFile(artworkFile);
      const metadataUpload = await uploadNftMetadata({
        name: draft.name.trim(),
        description: draft.description.trim(),
        image: artwork.url,
        externalUrl: draft.externalUrl.trim() || undefined,
        attributes: parseMetadataAttributes(draft.attributesText),
      });
      setDraft((current) => ({
        ...current,
        imageReference: artwork.url,
        metadataReference: metadataUpload.url,
      }));
      setHelperMessage(t('uploadComplete'));
    } catch (error) {
      setHelperMessage(
        t('uploadFailed', {
          message: error instanceof Error ? error.message : 'unknown error',
        })
      );
    } finally {
      setIsUploading(false);
    }
  };

  const canMint = Boolean(
    contractAddress &&
      isOwner &&
      draft.recipient.trim() &&
      normalizedMetadataUri &&
      !isBusy &&
      !isUploading
  );

  return (
    <section className="block">
      <div className="builder-grid">
        <div style={{ minWidth: 0 }}>
          <div style={{ marginBottom: 14 }}>
            <span className="pill flat" style={{ fontSize: 10 }}>{t('ipfsLabel')}</span>
            <h2 className="h-display" style={{ fontSize: 26, marginTop: 8 }}>{t('ipfsTitle')}</h2>
            <p style={{ marginTop: 6, fontSize: 13, color: 'var(--ink-2)', lineHeight: 1.55 }}>
              {t('ipfsDescription')}
            </p>
          </div>

          <div className="col gap-10">
            <div className="field">
              <div className="l"><span>{t('recipientLabel')}</span></div>
              <input
                value={draft.recipient}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, recipient: event.target.value }))
                }
                placeholder="0x..."
                style={{ fontFamily: 'var(--mf)', fontSize: 14, fontWeight: 500 }}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('metadataNameLabel')}</span></div>
              <input
                value={draft.name}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, name: event.target.value }))
                }
                placeholder={t('metadataNamePlaceholder')}
                style={{ fontSize: 16 }}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('metadataDescriptionLabel')}</span></div>
              <textarea
                value={draft.description}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, description: event.target.value }))
                }
                placeholder={t('metadataDescriptionPlaceholder')}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('artworkFileLabel')}</span></div>
              <input
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif"
                onChange={(event) => setArtworkFile(event.target.files?.[0] ?? null)}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('imageReferenceLabel')}</span></div>
              <input
                value={draft.imageReference}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, imageReference: event.target.value }))
                }
                placeholder="ipfs://... or https://.../ipfs/Qm..."
                style={{ fontFamily: 'var(--mf)', fontSize: 13, fontWeight: 500 }}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('externalUrlLabel')}</span></div>
              <input
                value={draft.externalUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, externalUrl: event.target.value }))
                }
                placeholder="https://your.site/nft/amt-1"
                style={{ fontFamily: 'var(--mf)', fontSize: 13, fontWeight: 500 }}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('attributesLabel')}</span></div>
              <textarea
                value={draft.attributesText}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, attributesText: event.target.value }))
                }
                placeholder={t('attributesPlaceholder')}
                style={{ fontFamily: 'var(--mf)', fontSize: 12 }}
              />
            </div>

            <button
              onClick={() => void uploadArtworkAndMetadata()}
              disabled={isUploading || !artworkFile || !draft.name.trim()}
              className="btn btn-c"
              style={{ width: '100%' }}
              data-testid="upload-artwork-metadata"
            >
              {isUploading && <span className="spinner" />}
              {isUploading ? t('uploadingArtwork') : t('uploadArtworkAndMetadata')}
            </button>

            <div className="field">
              <div className="l"><span>{t('metadataReferenceLabel')}</span></div>
              <input
                value={draft.metadataReference}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, metadataReference: event.target.value }))
                }
                placeholder="ipfs://metadataCID"
                style={{ fontFamily: 'var(--mf)', fontSize: 13, fontWeight: 500 }}
              />
              <span className="mono" style={{ display: 'block', marginTop: 4, fontSize: 10.5, color: 'var(--ink-2)' }}>
                {t('advancedReferenceHint')}
              </span>
            </div>
          </div>

          <div className="row gap-6 mt-14" style={{ flexWrap: 'wrap' }}>
            <button onClick={() => void copyMetadata()} className="btn btn-xs" type="button">
              {t('copyMetadata')}
            </button>
            <button onClick={downloadMetadata} className="btn btn-xs" type="button">
              {t('downloadMetadata')}
            </button>
          </div>

          {helperMessage && (
            <div
              className="block tight mt-14"
              style={{ background: 'var(--bg-2)', boxShadow: 'none', padding: '12px 14px', fontFamily: 'var(--mf)', fontSize: 12.5, lineHeight: 1.5 }}
              data-testid="ipfs-helper-message"
            >
              {helperMessage}
            </div>
          )}

          <div className="col gap-10 mt-14">
            {!isOwner && (
              <div className="block tight" style={{ background: 'var(--y)', boxShadow: 'none', padding: '12px 14px', fontSize: 12.5, lineHeight: 1.5 }}>
                {t('ownerOnlyHint')}
              </div>
            )}

            <NftStudioStatusNotice notice={notice} />

            {mintedTokenId && (
              <div className="block tight" style={{ background: 'var(--g)', boxShadow: 'none', padding: '12px 14px', fontFamily: 'var(--mf)', fontSize: 12.5 }}>
                {t('latestTokenId', { tokenId: mintedTokenId })}
              </div>
            )}

            {isConnected ? (
              <button
                onClick={() =>
                  void mintIpfsArtwork({
                    recipient: draft.recipient,
                    metadataReference: draft.metadataReference,
                  })
                }
                disabled={!canMint}
                className="btn btn-g"
                style={{ width: '100%' }}
                data-testid="mint-ipfs"
              >
                {isBusy && <span className="spinner" />}
                {isBusy ? t('mintingIpfs') : t('mintIpfs')}
              </button>
            ) : (
              <ConnectButton />
            )}
          </div>
        </div>

        <IpfsMetadataPreview
          metadata={metadata}
          metadataJson={metadataJson}
          normalizedImageUri={normalizedImageUri}
          normalizedMetadataUri={normalizedMetadataUri}
          metadataReference={draft.metadataReference}
          errorMessage={imageError}
        />
      </div>
    </section>
  );
}
