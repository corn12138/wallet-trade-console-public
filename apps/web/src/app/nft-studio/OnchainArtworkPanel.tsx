'use client';

import { ConnectButton } from '@/components/web3';
import { useTranslations } from 'next-intl';
import { useEffect, useState } from 'react';
import { NftStudioStatusNotice } from './NftStudioStatusNotice';
import { OnchainArtworkPreview } from './OnchainArtworkPreview';
import {
  INITIAL_ONCHAIN_ARTWORK_DRAFT,
  type OnchainArtworkDraft,
} from './nft-studio.types';
import { useOnchainArtworkMint } from './useOnchainArtworkMint';

interface OnchainArtworkPanelProps {
  contractAddress?: `0x${string}`;
  walletAddress?: `0x${string}`;
  collectionOwner?: string;
  isConnected: boolean;
}

export function OnchainArtworkPanel({
  contractAddress,
  walletAddress,
  collectionOwner,
  isConnected,
}: OnchainArtworkPanelProps) {
  const t = useTranslations('nftStudio');
  const [draft, setDraft] = useState<OnchainArtworkDraft>(INITIAL_ONCHAIN_ARTWORK_DRAFT);

  const { mintArtwork, mintedTokenId, notice, isBusy } = useOnchainArtworkMint({
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

  const canSubmit = Boolean(
    contractAddress &&
      isOwner &&
      draft.recipient.trim() &&
      draft.title.trim() &&
      draft.caption.trim() &&
      !isBusy
  );

  return (
    <section className="block">
      <div className="builder-grid">
        <div style={{ minWidth: 0 }}>
          <div style={{ marginBottom: 14 }}>
            <span className="pill flat" style={{ fontSize: 10 }}>{t('onchainLabel')}</span>
            <h2 className="h-display" style={{ fontSize: 26, marginTop: 8 }}>{t('onchainTitle')}</h2>
            <p style={{ marginTop: 6, fontSize: 13, color: 'var(--ink-2)', lineHeight: 1.55 }}>
              {t('onchainDescription')}
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
              <div className="l"><span>{t('artworkTitleLabel')}</span></div>
              <input
                value={draft.title}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, title: event.target.value }))
                }
                placeholder={t('artworkTitlePlaceholder')}
                style={{ fontSize: 16 }}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('captionLabel')}</span></div>
              <textarea
                value={draft.caption}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, caption: event.target.value }))
                }
                placeholder={t('captionPlaceholder')}
              />
            </div>

            <div className="field">
              <div className="l"><span>{t('accentColorLabel')}</span></div>
              <div className="row gap-10" style={{ alignItems: 'center' }}>
                <input
                  value={draft.accentColor}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, accentColor: event.target.value }))
                  }
                  placeholder="#38bdf8"
                  style={{ flex: 1, fontFamily: 'var(--mf)', fontSize: 14, fontWeight: 500 }}
                />
                <input
                  type="color"
                  value={draft.accentColor}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, accentColor: event.target.value }))
                  }
                  aria-label={t('accentColorLabel')}
                />
              </div>
            </div>
          </div>

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
                onClick={() => void mintArtwork(draft)}
                disabled={!canSubmit}
                className="btn btn-g"
                style={{ width: '100%' }}
                data-testid="mint-onchain"
              >
                {isBusy && <span className="spinner" />}
                {isBusy ? t('mintingOnchain') : t('mintOnchain')}
              </button>
            ) : (
              <ConnectButton />
            )}
          </div>
        </div>

        <div style={{ minWidth: 0 }}>
          <div className="eyebrow" style={{ marginBottom: 8 }}>
            {t('previewTitle')}
          </div>
          <OnchainArtworkPreview
            title={draft.title}
            caption={draft.caption}
            accentColor={draft.accentColor}
          />
        </div>
      </div>
    </section>
  );
}
