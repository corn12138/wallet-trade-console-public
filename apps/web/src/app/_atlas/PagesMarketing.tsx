'use client';
import React from 'react';
import Link from 'next/link';
import { useTranslations } from 'next-intl';
import { useApp } from './AppContext';
import { Icon, type IconName } from './Icon';
import { HeroIsoArt } from './MiniChart';
import { HomeLivePreview } from './HomeLivePreview';

/* ============================================================================
   HOME — marketing landing (illustrative preview data is labelled as such;
   the live terminal is /trade)
   ============================================================================ */
export default function HomePage() {
  const app = useApp();
  const t = useTranslations('marketing');

  const steps: Array<{
    i: number;
    t: string;
    b: string;
    a: string;
    href: string;
    action?: () => void;
    tone: 'y' | 'o' | 'c';
  }> = [
    {
      i: 1,
      t: t('step1Title'),
      b: t('step1Body'),
      a: t('step1Action'),
      href: '/',
      action: app.openConnect,
      tone: 'y',
    },
    {
      i: 2,
      t: t('step2Title'),
      b: t('step2Body'),
      a: t('step2Action'),
      // Honest destination: funding lives in the wallet manager. There is no
      // in-app faucet, so no control may claim one.
      href: '/wallets',
      tone: 'o',
    },
    {
      i: 3,
      t: t('step3Title'),
      b: t('step3Body'),
      a: t('step3Action'),
      href: '/trade',
      tone: 'c',
    },
  ];

  return (
    <div className="col gap-24">
      {/* HERO */}
      <section className="block" style={{ padding: '44px 36px', overflow: 'hidden', position: 'relative' }}>
        <div className="hero-shell">
          <div>
            <span className="pill"><span className="dot" /> {t('heroPill')}</span>
            <h1>
              {t('heroLine1')}<br />{t('heroLine2')} <em>{t('heroEm')}</em><br />
              <em className="alt">{t('heroAltEm')}</em>
            </h1>
            <p>{t('heroBody')}</p>
            <div className="row gap-14 mt-22">
              <Link href="/trade" className="btn btn-o">
                <Icon name="trade" size={16} /> {t('launchTerminal')}
              </Link>
              <Link href="/discover" className="btn" data-testid="home-cta-discover">
                {t('exploreDiscover')}
              </Link>
            </div>
            <div className="row gap-10 mt-22">
              <span className="pill flat">{t('badgeNoKyc')}</span>
              <span className="pill flat">{t('badgeRealtime')}</span>
              <span className="pill flat">{t('badgeMultiChain')}</span>
              <span className="pill flat">{t('badgeOpenSource')}</span>
            </div>
          </div>
          <div className="iso-stage">
            <HeroIsoArt />
          </div>
        </div>
      </section>

      {/* FEATURES */}
      <section className="grid-3">
        <div className="block bg-y" style={{ padding: 28 }}>
          <FeatureIcon name="security" />
          <div className="h-display" style={{ fontSize: 28, marginTop: 16 }}>
            {t('feature1Title')}
          </div>
          <p style={{ marginTop: 10, color: 'var(--ink-2)', lineHeight: 1.55 }}>{t('feature1Body')}</p>
        </div>
        <div className="block" style={{ padding: 28 }}>
          <FeatureIcon name="bolt" bg="o" />
          <div className="h-display" style={{ fontSize: 28, marginTop: 16 }}>
            {t('feature2Title')}
          </div>
          <p style={{ marginTop: 10, color: 'var(--ink-2)', lineHeight: 1.55 }}>{t('feature2Body')}</p>
        </div>
        <div className="block" style={{ padding: 28 }}>
          <FeatureIcon name="wallet" bg="c" />
          <div className="h-display" style={{ fontSize: 28, marginTop: 16 }}>
            {t('feature3Title')}
          </div>
          <p style={{ marginTop: 10, color: 'var(--ink-2)', lineHeight: 1.55 }}>{t('feature3Body')}</p>
        </div>
      </section>

      {/* TERMINAL PREVIEW — real, read-only, backed by the live trading service */}
      <section>
        <div className="sec-head">
          <div>
            <span className="pill live"><span className="dot" /> {t('livePreviewPill')}</span>
            <h2>
              {t('deskTitle1')}<br />
              <em>{t('deskTitleEm')}</em>
            </h2>
          </div>
          <Link href="/trade" className="btn btn-sm btn-y">
            <Icon name="trade" size={14} /> {t('openTerminal')}
          </Link>
        </div>
        <HomeLivePreview />
      </section>

      {/* ONBOARDING STEPS */}
      <section>
        <div className="sec-head">
          <div>
            <span className="pill">{t('onboardingPill')}</span>
            <h2>
              {t('onboardingTitle1')}<br />{t('onboardingTitle2')} <em>{t('onboardingTitleEm')}</em>
            </h2>
          </div>
        </div>
        <div className="grid-3">
          {steps.map((s) => (
            <div key={s.i} className="block lift" style={{ padding: 24 }}>
              <div
                style={{
                  width: 42,
                  height: 42,
                  borderRadius: 999,
                  background: `var(--${s.tone})`,
                  color: s.tone === 'c' ? '#fff' : 'var(--ink)',
                  border: '3px solid var(--ink)',
                  boxShadow: '0 3px 0 0 var(--ink)',
                  display: 'grid',
                  placeItems: 'center',
                  fontFamily: 'var(--df)',
                  fontWeight: 900,
                  fontSize: 18,
                }}
              >
                {s.i}
              </div>
              <div className="h-display" style={{ fontSize: 22, marginTop: 18 }}>
                {s.t}
              </div>
              <p style={{ marginTop: 8, color: 'var(--ink-2)', lineHeight: 1.55 }}>{s.b}</p>
              {s.action ? (
                <button className="btn btn-sm btn-y mt-14" onClick={s.action}>
                  {s.a} <Icon name="arrowRight" size={14} />
                </button>
              ) : (
                <Link href={s.href} className="btn btn-sm btn-y mt-14" style={{ display: 'inline-flex' }}>
                  {s.a} <Icon name="arrowRight" size={14} />
                </Link>
              )}
            </div>
          ))}
        </div>
      </section>

      {/* CTA BLOCK */}
      <section className="block bg-d" style={{ padding: 36, position: 'relative', overflow: 'hidden' }}>
        <div style={{ position: 'absolute', right: -50, top: -40, opacity: 0.15 }}>
          <HeroIsoArt small />
        </div>
        <div
          className="row between"
          style={{ alignItems: 'flex-end', flexWrap: 'wrap', gap: 18, position: 'relative' }}
        >
          <div>
            <div className="eyebrow" style={{ color: 'var(--y)' }}>
              {t('ctaEyebrow')}
            </div>
            <div className="h-display" style={{ fontSize: 42, marginTop: 6, color: 'var(--bg)' }}>
              {t('ctaTitle1')}<br />{t('ctaTitle2')}
            </div>
          </div>
          <Link href="/trade" className="btn btn-y">
            {t('openTerminal')} <Icon name="arrowRight" size={16} />
          </Link>
        </div>
      </section>
    </div>
  );
}

/* feature icon helper */
function FeatureIcon({ name, bg = 'y' }: { name: IconName; bg?: string }) {
  const inverseInk = bg === 'c' || bg === 'o' || bg === 'p';
  return (
    <div
      style={{
        width: 56,
        height: 56,
        borderRadius: 16,
        background: `var(--${bg})`,
        border: '3px solid var(--ink)',
        boxShadow: '0 4px 0 0 var(--ink)',
        display: 'grid',
        placeItems: 'center',
        color: inverseInk ? '#fff' : 'var(--ink)',
      }}
    >
      <Icon name={name} size={26} sw={2.4} />
    </div>
  );
}
