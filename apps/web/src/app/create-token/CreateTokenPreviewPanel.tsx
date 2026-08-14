import Image from 'next/image';
import type { TokenForm } from './create-token.types';

interface CreateTokenPreviewPanelProps {
  form: TokenForm;
  t: (key: string) => string;
}

export function CreateTokenPreviewPanel({
  form,
  t,
}: CreateTokenPreviewPanelProps) {
  return (
    <div className="block tight" style={{ padding: 0, overflow: 'hidden' }}>
      <div style={{ padding: '12px 16px 0' }}>
        <span className="eyebrow">{t('createToken.previewEyebrow')}</span>
      </div>
      <div
        style={{
          position: 'relative',
          aspectRatio: '16 / 7',
          margin: 12,
          borderRadius: 12,
          overflow: 'hidden',
          background: 'var(--bg-2)',
          border: '2px solid var(--ink)',
        }}
      >
        {form.bannerPreview ? (
          <Image src={form.bannerPreview} alt={t('createToken.altBannerPreview')} fill style={{ objectFit: 'cover' }} />
        ) : (
          <div
            style={{
              height: '100%',
              display: 'grid',
              placeItems: 'center',
              fontFamily: 'var(--df)',
              fontWeight: 900,
              fontSize: 52,
              color: 'var(--ink-3)',
            }}
          >
            {form.symbol.charAt(0) || '?'}
          </div>
        )}

        <div
          style={{
            position: 'absolute',
            bottom: 10,
            left: 10,
            width: 56,
            height: 56,
            overflow: 'hidden',
            borderRadius: 999,
            border: '3px solid var(--ink)',
            background: 'var(--paper)',
            boxShadow: '0 3px 0 0 var(--ink)',
          }}
        >
          {form.imagePreview ? (
            <Image src={form.imagePreview} alt={t('createToken.altTokenImage')} fill style={{ objectFit: 'cover' }} />
          ) : (
            <div
              style={{
                height: '100%',
                display: 'grid',
                placeItems: 'center',
                fontFamily: 'var(--df)',
                fontWeight: 900,
                fontSize: 20,
                color: 'var(--ink-3)',
              }}
            >
              ?
            </div>
          )}
        </div>
      </div>

      <div style={{ padding: '0 16px 16px' }}>
        <div className="h-display" style={{ fontSize: 22 }}>
          {form.symbol || t('createToken.previewSymbolPlaceholder')}
        </div>
        <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 2 }}>
          {form.name || t('createToken.previewNamePlaceholder')}
        </div>
        <p style={{ marginTop: 8, fontSize: 13, color: 'var(--ink-2)', lineHeight: 1.5 }}>
          {form.description || t('createToken.previewDescriptionPlaceholder')}
        </p>

        {form.tags.length > 0 && (
          <div className="row gap-6 mt-14" style={{ flexWrap: 'wrap' }}>
            {form.tags.map((tag) => (
              <span key={tag} className="pill flat" style={{ padding: '3px 10px', fontSize: 10 }}>
                {t(`tags.${tag}`)}
              </span>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
