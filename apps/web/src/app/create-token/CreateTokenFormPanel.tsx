import type React from 'react';
import { CreateTokenBasicInfoSection } from './CreateTokenBasicInfoSection';
import { availableTags, launchTypes, toggleFields } from './create-token.constants';
import type { LaunchType, TokenForm } from './create-token.types';

const LAUNCH_TYPE_TAB_KEYS: Record<LaunchType, string> = {
  newcoin: 'createToken.tabNewCoin',
  ido: 'createToken.tabIDO',
  burning: 'createToken.tabBurning',
};

interface CreateTokenFormPanelProps {
  form: TokenForm;
  launchType: LaunchType;
  imageInputRef: React.MutableRefObject<HTMLInputElement | null>;
  bannerInputRef: React.MutableRefObject<HTMLInputElement | null>;
  onLaunchTypeChange: (type: LaunchType) => void;
  onFormChange: (updater: (current: TokenForm) => TokenForm) => void;
  onImageChange: (event: React.ChangeEvent<HTMLInputElement>, type: 'image' | 'banner') => void;
  onToggleTag: (tag: string) => void;
  t: (key: string) => string;
}

export function CreateTokenFormPanel({
  form,
  launchType,
  imageInputRef,
  bannerInputRef,
  onLaunchTypeChange,
  onFormChange,
  onImageChange,
  onToggleTag,
  t,
}: CreateTokenFormPanelProps) {
  return (
    <div className="col gap-24" style={{ minWidth: 0 }}>
      <div className="seg s3">
        {launchTypes.map((type) => (
          <button
            key={type}
            type="button"
            onClick={() => onLaunchTypeChange(type)}
            className={launchType === type ? 'on' : ''}
          >
            {t(LAUNCH_TYPE_TAB_KEYS[type])}
          </button>
        ))}
      </div>

      <CreateTokenBasicInfoSection
        form={form}
        imageInputRef={imageInputRef}
        bannerInputRef={bannerInputRef}
        onFormChange={onFormChange}
        onImageChange={onImageChange}
        t={t}
      />

      <section className="block">
        <h2 className="h-display" style={{ fontSize: 20, marginBottom: 4 }}>
          {t('createToken.tags')} *
        </h2>
        <p style={{ marginBottom: 12, fontSize: 13, color: 'var(--ink-2)' }}>
          {t('createToken.tagsHint')}
        </p>
        <div className="row gap-6" style={{ flexWrap: 'wrap' }}>
          {availableTags.map((tag) => {
            const active = form.tags.includes(tag);
            return (
              <button
                key={tag}
                type="button"
                onClick={() => onToggleTag(tag)}
                className={'pill' + (active ? '' : ' flat')}
                style={{
                  cursor: 'pointer',
                  background: active ? 'var(--y)' : 'var(--paper)',
                }}
              >
                {t(`tags.${tag}`)}
              </button>
            );
          })}
        </div>
      </section>

      <section className="block">
        <h2 className="h-display" style={{ fontSize: 20, marginBottom: 14 }}>
          {t('createToken.socialPlatform')}
        </h2>
        <div className="col gap-10">
          {(['twitter', 'discord', 'telegram', 'website', 'whitepaper'] as const).map((field) => (
            <div key={field} className="field">
              <div className="l"><span>{t(`createToken.${field}`)}</span></div>
              <input
                type="url"
                placeholder={t(`createToken.${field}Placeholder`)}
                value={form[field]}
                onChange={(event) =>
                  onFormChange((current) => ({
                    ...current,
                    [field]: event.target.value,
                  }))
                }
                style={{ fontFamily: 'var(--mf)', fontSize: 14, fontWeight: 500 }}
              />
            </div>
          ))}
        </div>
      </section>

      <section className="block">
        <h2 className="h-display" style={{ fontSize: 20, marginBottom: 14 }}>
          {t('createToken.essentialInfo')}
        </h2>

        <div style={{ marginBottom: 18 }}>
          <div className="eyebrow" style={{ marginBottom: 4 }}>{t('createToken.preBuy')}</div>
          <p className="mono" style={{ marginBottom: 10, fontSize: 11, color: 'var(--ink-2)' }}>
            {t('createToken.preBuyHint')}
          </p>
          <div className="row gap-6" style={{ flexWrap: 'wrap' }}>
            <div className="seg s3" style={{ flex: 1, minWidth: 180 }}>
              {[10, 25, 50].map((percent) => (
                <button
                  key={percent}
                  type="button"
                  onClick={() =>
                    onFormChange((current) => ({ ...current, preBuyPercent: percent }))
                  }
                  className={form.preBuyPercent === percent ? 'on' : ''}
                >
                  {percent}%
                </button>
              ))}
            </div>
            <div className="field" style={{ width: 130, padding: '8px 12px' }}>
              <input
                type="number"
                placeholder="%"
                value={form.preBuyPercent || ''}
                onChange={(event) =>
                  onFormChange((current) => ({
                    ...current,
                    preBuyPercent: Math.min(99.9, parseFloat(event.target.value) || 0),
                  }))
                }
                max={99.9}
                style={{ fontSize: 16 }}
              />
            </div>
          </div>
        </div>

        <div className="col">
          {toggleFields.map(({ key, label }) => {
            const on = Boolean(form[key]);
            return (
              <div
                key={key}
                className="row between"
                style={{ padding: '12px 0', borderBottom: '2px solid var(--bg-2)' }}
              >
                <span style={{ fontFamily: 'var(--df)', fontWeight: 700, fontSize: 13 }}>
                  {t(`createToken.${label}`)}
                </span>
                <button
                  type="button"
                  onClick={() =>
                    onFormChange((current) => ({ ...current, [key]: !current[key] }))
                  }
                  aria-pressed={on}
                  style={{
                    width: 52,
                    height: 28,
                    borderRadius: 999,
                    border: '3px solid var(--ink)',
                    background: on ? 'var(--g)' : 'var(--bg-3)',
                    padding: 0,
                    position: 'relative',
                    boxShadow: '0 2px 0 0 var(--ink)',
                    cursor: 'pointer',
                  }}
                >
                  <span
                    style={{
                      position: 'absolute',
                      top: 1,
                      left: on ? 24 : 1,
                      width: 20,
                      height: 20,
                      background: 'var(--ink)',
                      borderRadius: 999,
                      transition: 'left .2s',
                    }}
                  />
                </button>
              </div>
            );
          })}
        </div>
      </section>

      <section className="block">
        <h2 className="h-display" style={{ fontSize: 20, marginBottom: 14 }}>
          {t('createToken.contractPreview')}
        </h2>
        <div className="seg s2">
          <button
            type="button"
            onClick={() =>
              onFormChange((current) => ({ ...current, useCustomAddress: false }))
            }
            className={!form.useCustomAddress ? 'on' : ''}
          >
            {t('createToken.randomDigits')}
          </button>
          <button
            type="button"
            onClick={() =>
              onFormChange((current) => ({ ...current, useCustomAddress: true }))
            }
            className={form.useCustomAddress ? 'on' : ''}
          >
            {t('createToken.customDigits')}
          </button>
        </div>

        {form.useCustomAddress && (
          <div className="field mt-14">
            <div className="l"><span>{t('createToken.customDigits')}</span></div>
            <input
              type="text"
              placeholder="CAFE"
              maxLength={4}
              value={form.customAddress}
              onChange={(event) =>
                onFormChange((current) => ({
                  ...current,
                  customAddress: event.target.value.toUpperCase(),
                }))
              }
              style={{ fontFamily: 'var(--mf)' }}
            />
          </div>
        )}

        {/* No preview button: the preview panel beside the form updates
            live as fields change, so a "Click to Preview" control was a
            no-op and has been removed. */}
      </section>
    </div>
  );
}
