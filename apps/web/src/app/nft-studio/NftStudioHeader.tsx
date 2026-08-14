import { useTranslations } from 'next-intl';

export function NftStudioHeader() {
  const t = useTranslations('nftStudio');

  return (
    <section className="block" style={{ padding: 24, background: 'var(--paper)' }}>
      <div className="row between" style={{ alignItems: 'flex-end', gap: 24, flexWrap: 'wrap' }}>
        <div style={{ minWidth: 0, flex: 1 }}>
          <span className="pill flat" style={{ fontSize: 10 }}>{t('heroBadge')}</span>
          <div className="h-display" style={{ fontSize: 40, marginTop: 10 }}>
            {t('title')}
          </div>
          <p style={{ marginTop: 10, color: 'var(--ink-2)', maxWidth: 720, lineHeight: 1.5 }}>
            {t('subtitle')}
          </p>
        </div>

        <div className="row gap-10" style={{ flexWrap: 'wrap' }}>
          {(
            [
              ['highlights.onchainTitle', 'highlights.onchainValue'],
              ['highlights.ipfsTitle', 'highlights.ipfsValue'],
              ['highlights.interviewTitle', 'highlights.interviewValue'],
            ] as const
          ).map(([titleKey, valueKey]) => (
            <div key={titleKey} className="block tight" style={{ boxShadow: 'none', background: 'var(--bg-2)', padding: '10px 14px' }}>
              <div className="eyebrow" style={{ fontSize: 9 }}>{t(titleKey)}</div>
              <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 13, marginTop: 4 }}>
                {t(valueKey)}
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
