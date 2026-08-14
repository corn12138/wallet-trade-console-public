import Image from 'next/image';
import type React from 'react';
import { Icon } from '@/app/_atlas/Icon';
import type { TokenForm } from './create-token.types';

interface CreateTokenBasicInfoSectionProps {
  form: TokenForm;
  imageInputRef: React.MutableRefObject<HTMLInputElement | null>;
  bannerInputRef: React.MutableRefObject<HTMLInputElement | null>;
  onFormChange: (updater: (current: TokenForm) => TokenForm) => void;
  onImageChange: (event: React.ChangeEvent<HTMLInputElement>, type: 'image' | 'banner') => void;
  t: (key: string) => string;
}

export function CreateTokenBasicInfoSection({
  form,
  imageInputRef,
  bannerInputRef,
  onFormChange,
  onImageChange,
  t,
}: CreateTokenBasicInfoSectionProps) {
  return (
    <section className="block">
      <h2 className="h-display" style={{ fontSize: 20, marginBottom: 14 }}>
        {t('createToken.basicInfo')}
      </h2>

      <div className="row gap-14" style={{ alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <div style={{ flex: '0 0 148px' }}>
          <div className="eyebrow" style={{ marginBottom: 6 }}>
            {t('createToken.tokenImage')} *
          </div>
          <button
            type="button"
            className="upload-box"
            style={{ width: 148, height: 148, padding: 10 }}
            onClick={() => imageInputRef.current?.click()}
            data-testid="token-image-upload"
          >
            {form.imagePreview ? (
              <>
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <Image src={form.imagePreview} alt={t('createToken.altTokenImage')} width={148} height={148} style={{ objectFit: 'cover' }} />
                <span className="up-file">{form.image?.name ?? t('createToken.uploadChange')}</span>
              </>
            ) : (
              <>
                <Icon name="image" size={26} />
                <span className="up-hint">{t('createToken.uploadChoose')}</span>
              </>
            )}
          </button>
          <input
            ref={(node) => {
              imageInputRef.current = node;
            }}
            type="file"
            accept="image/*"
            className="hidden"
            style={{ display: 'none' }}
            onChange={(event) => onImageChange(event, 'image')}
          />
          <p className="mono" style={{ marginTop: 6, fontSize: 10.5, color: 'var(--ink-2)', lineHeight: 1.4 }}>
            {t('createToken.tokenImageHint')}
          </p>
        </div>

        <div style={{ flex: 1, minWidth: 220 }}>
          <div className="field">
            <div className="l"><span>{t('createToken.symbol')} *</span></div>
            <input
              type="text"
              placeholder={t('createToken.symbolPlaceholder')}
              value={form.symbol}
              onChange={(event) =>
                onFormChange((current) => ({
                  ...current,
                  symbol: event.target.value.toUpperCase(),
                }))
              }
              maxLength={10}
            />
          </div>

          <div className="field mt-14">
            <div className="l"><span>{t('createToken.coinName')} *</span></div>
            <input
              type="text"
              placeholder={t('createToken.coinNamePlaceholder')}
              value={form.name}
              onChange={(event) =>
                onFormChange((current) => ({ ...current, name: event.target.value }))
              }
            />
          </div>
        </div>
      </div>

      <div className="field mt-14">
        <div className="l"><span>{t('createToken.description')}</span></div>
        <textarea
          placeholder={t('createToken.descriptionPlaceholder')}
          value={form.description}
          onChange={(event) =>
            onFormChange((current) => ({
              ...current,
              description: event.target.value,
            }))
          }
        />
      </div>

      <div className="mt-14">
        <div className="eyebrow" style={{ marginBottom: 6 }}>
          {t('createToken.banner')}
        </div>
        <button
          type="button"
          className="upload-box"
          style={{ width: '100%', height: 96, padding: 10 }}
          onClick={() => bannerInputRef.current?.click()}
          data-testid="token-banner-upload"
        >
          {form.bannerPreview ? (
            <>
              <Image src={form.bannerPreview} alt={t('createToken.altBannerImage')} width={640} height={96} style={{ objectFit: 'cover' }} />
              <span className="up-file">{form.banner?.name ?? t('createToken.uploadChange')}</span>
            </>
          ) : (
            <>
              <Icon name="upload" size={22} />
              <span className="up-hint">{t('createToken.uploadBannerChoose')}</span>
            </>
          )}
        </button>
        <input
          ref={(node) => {
            bannerInputRef.current = node;
          }}
          type="file"
          accept="image/*"
          className="hidden"
          style={{ display: 'none' }}
          onChange={(event) => onImageChange(event, 'banner')}
        />
        <p className="mono" style={{ marginTop: 6, fontSize: 10.5, color: 'var(--ink-2)' }}>
          {t('createToken.bannerHint')}
        </p>
      </div>
    </section>
  );
}
