'use client';

/**
 * Server-backed language switcher (2026-07-10 cutover).
 *
 * Renders the enabled locales the SERVER supplied (Go locale metadata via the
 * SSR hydration provider) and submits the selection to the setLocaleAction
 * server action, which validates it, sets the locale cookie server-side and
 * redirects to the current path — producing a fresh SSR response in the new
 * language. No document.cookie, no localStorage, no client message fetch, no
 * dynamic import, no client locale state beyond open/highlight UI state.
 */

import { useEffect, useRef, useState } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import { usePathname, useSearchParams } from 'next/navigation';
import { setLocaleAction } from '@/i18n/actions';
import { useEnabledLocales } from '@/i18n/provider';

const LOCALE_GLYPH: Record<string, string> = {
  en: 'EN',
  zh: '中',
};

const LOCALE_TONE: Record<string, { bg: string; fg: string }> = {
  en: { bg: 'var(--y)', fg: 'var(--ink)' },
  zh: { bg: 'var(--p)', fg: '#fff' },
};

const FALLBACK_TONE = { bg: 'var(--paper)', fg: 'var(--ink)' };

function glyphFor(code: string): string {
  return LOCALE_GLYPH[code] ?? code.slice(0, 2).toUpperCase();
}

type Variant = 'default' | 'compact';

interface LanguageSwitcherProps {
  variant?: Variant;
  className?: string;
}

export function LanguageSwitcher({ variant = 'default', className = '' }: LanguageSwitcherProps) {
  const t = useTranslations('atlasShell.language');
  const locale = useLocale();
  const locales = useEnabledLocales();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const [open, setOpen] = useState(false);
  const [hover, setHover] = useState<string | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  const codes = locales.map((l) => l.code);
  const active = locales.find((l) => l.code === locale);
  const returnPath = searchParams?.size
    ? `${pathname}?${searchParams.toString()}`
    : pathname || '/';

  useEffect(() => {
    setHover(open ? locale : null);
  }, [locale, open]);

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        return;
      }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const idx = hover ? codes.indexOf(hover) : codes.indexOf(locale);
        const next = (idx + (e.key === 'ArrowDown' ? 1 : -1) + codes.length) % codes.length;
        setHover(codes[next]);
      }
    };
    const timer = setTimeout(() => document.addEventListener('mousedown', onClick), 0);
    document.addEventListener('keydown', onKey);
    return () => {
      clearTimeout(timer);
      document.removeEventListener('mousedown', onClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [open, hover, locale, codes]);

  const tone = LOCALE_TONE[locale] ?? FALLBACK_TONE;
  const isCompact = variant === 'compact';

  return (
    <div
      ref={rootRef}
      className={'ls-root ' + className}
      style={{ position: 'relative', display: 'inline-block' }}
    >
      <button
        type="button"
        className={'ls-chip' + (isCompact ? ' ls-compact' : '')}
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t('ariaLabel', { name: active?.nativeName ?? locale })}
        data-active-locale={locale}
      >
        <span className="ls-glyph" style={{ background: tone.bg, color: tone.fg }}>
          {glyphFor(locale)}
        </span>
        {!isCompact && <span className="ls-label">{active?.nativeName ?? locale}</span>}
        {!isCompact && (
          <svg
            className="ls-chev"
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            style={{ transform: open ? 'rotate(180deg)' : 'none', transition: 'transform .2s ease' }}
          >
            <path d="m19.5 8.25-7.5 7.5-7.5-7.5" />
          </svg>
        )}
      </button>

      {open && (
        <form
          action={setLocaleAction}
          className="ls-pop"
          role="listbox"
          aria-label={t('listLabel')}
          data-testid="language-switcher-form"
        >
          <input type="hidden" name="returnPath" value={returnPath} />
          {locales.map((loc) => {
            const isActive = loc.code === locale;
            const highlighted = hover ? hover === loc.code : isActive;
            const optTone = LOCALE_TONE[loc.code] ?? FALLBACK_TONE;
            return (
              <button
                key={loc.code}
                type="submit"
                name="locale"
                value={loc.code}
                role="option"
                aria-selected={isActive}
                className={'ls-pop-row' + (isActive ? ' on' : '') + (highlighted ? ' hl' : '')}
                onMouseEnter={() => setHover(loc.code)}
                onMouseLeave={() => setHover(null)}
                data-testid={`language-option-${loc.code}`}
              >
                <span
                  className="ls-glyph ls-glyph-sm"
                  style={{ background: optTone.bg, color: optTone.fg }}
                >
                  {glyphFor(loc.code)}
                </span>
                <span className="ls-pop-stack">
                  <span className="ls-pop-label">{loc.nativeName}</span>
                  <span className="ls-pop-sub">{loc.code} · {loc.englishName}</span>
                </span>
                {isActive && (
                  <svg
                    className="ls-check"
                    width="16"
                    height="16"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="3"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  >
                    <path d="m4.5 12.75 6 6 9-13.5" />
                  </svg>
                )}
              </button>
            );
          })}
          <div className="ls-pop-foot">{t('footNote')}</div>
        </form>
      )}
    </div>
  );
}

export default LanguageSwitcher;
