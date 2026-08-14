'use client';

import { useTranslations } from 'next-intl';
import type { NftMetadataPreview } from './nft-studio.types';

interface IpfsMetadataPreviewProps {
  metadata?: NftMetadataPreview;
  metadataJson: string;
  normalizedImageUri?: string;
  normalizedMetadataUri?: string;
  metadataReference: string;
  errorMessage?: string;
}

export function IpfsMetadataPreview({
  metadata,
  metadataJson,
  normalizedImageUri,
  normalizedMetadataUri,
  metadataReference,
  errorMessage,
}: IpfsMetadataPreviewProps) {
  const t = useTranslations('nftStudio');

  return (
    <div className="col gap-10" style={{ minWidth: 0 }}>
      <div className="block tight" style={{ background: 'var(--bg-2)', boxShadow: 'none' }}>
        <div className="eyebrow">{t('normalizedImageUriLabel')}</div>
        <div
          className="mono"
          style={{ marginTop: 6, fontSize: 11, wordBreak: 'break-all', color: errorMessage && !normalizedImageUri ? 'var(--neg)' : 'var(--ink-2)', lineHeight: 1.5 }}
        >
          {normalizedImageUri || errorMessage || t('normalizedImageUriPlaceholder')}
        </div>
      </div>

      <div className="block tight" style={{ background: 'var(--bg-2)', boxShadow: 'none' }}>
        <div className="eyebrow">{t('normalizedMetadataUriLabel')}</div>
        <div className="mono" style={{ marginTop: 6, fontSize: 11, wordBreak: 'break-all', color: 'var(--ink-2)', lineHeight: 1.5 }}>
          {normalizedMetadataUri ||
            (metadataReference
              ? t('invalidMetadataReference')
              : t('normalizedMetadataUriPlaceholder'))}
        </div>
      </div>

      <div className="block tight" style={{ padding: 0, overflow: 'hidden', boxShadow: 'none', background: 'var(--bg-2)' }}>
        <div
          style={{
            borderBottom: '2px solid var(--ink)',
            padding: '10px 14px',
            fontFamily: 'var(--df)',
            fontWeight: 800,
            fontSize: 13,
          }}
        >
          {t('metadataPreviewTitle')}
        </div>
        <pre
          className="mono"
          style={{
            maxHeight: 420,
            overflow: 'auto',
            margin: 0,
            padding: '12px 14px',
            fontSize: 11,
            lineHeight: 1.6,
            color: 'var(--ink-2)',
          }}
        >
          {metadata ? metadataJson : t('metadataPreviewHint')}
        </pre>
      </div>
    </div>
  );
}
