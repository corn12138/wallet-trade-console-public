'use client';
import React, { useCallback, useEffect, useRef } from 'react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { Icon, LogoCube } from './Icon';
import { GROUPS } from './nav';
import { useApp } from './AppContext';
import { ConnectPill, StreamStatus, GlobalModals, ToastHost } from './Common';
import { LanguageSwitcher } from '@/components/LanguageSwitcher';

const BASE = '';


/* Dwell time before a hover expands the rail. Long enough that crossing the
   rail on the way to the page does not trigger it, short enough that a
   deliberate move still feels immediate. */
const RAIL_OPEN_DELAY_MS = 140;

/* Route heads that have a crumb entry in atlasShell.crumbs. */
const CRUMB_KEYS = new Set([
  '',
  'trade',
  'markets',
  'swap',
  'bridge',
  'portfolio',
  'earn',
  'advanced-earn',
  'activity',
  'security',
  'discover',
  'wallets',
  'create-token',
  'nft-studio',
  'settings',
  'ranking',
  'campaign',
  'token',
  'profile',
  'nft',
  'events',
]);

function relativePath(pathname: string | null): string {
  if (!pathname) return '';
  const tail = pathname.startsWith(BASE) ? pathname.slice(BASE.length) : pathname;
  return tail.replace(/^\/+/, '');
}

function Sidebar() {
  const app = useApp();
  const t = useTranslations('atlasShell');
  const pathname = usePathname();
  const rel = relativePath(pathname);
  const segs = rel.split('/').filter(Boolean);
  const head = segs[0] || '';
  // When the labels are on screen, a `title` would just re-state the word
  // sitting next to the cursor — the native tooltip that used to pop up over
  // the expanded rail. Collapsed, it is the only thing naming the icon.
  const labelsVisible = app.railOpen || app.drawerOpen;

  // Hover intent: open only after the cursor has DWELLED on the rail. The rail
  // overlays the page, so opening on the first mouseenter meant a cursor
  // travelling from the left edge toward the content covered what the user was
  // reading, every time. Closing stays instant — a delay on the way out is
  // what makes a menu feel sticky.
  const openTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const setRailOpen = app.setRailOpen;

  const cancelOpen = useCallback(() => {
    if (openTimer.current !== null) {
      clearTimeout(openTimer.current);
      openTimer.current = null;
    }
  }, []);

  const handleEnter = useCallback(() => {
    cancelOpen();
    openTimer.current = setTimeout(() => {
      openTimer.current = null;
      setRailOpen(true);
    }, RAIL_OPEN_DELAY_MS);
  }, [cancelOpen, setRailOpen]);

  const handleLeave = useCallback(() => {
    cancelOpen();
    setRailOpen(false);
  }, [cancelOpen, setRailOpen]);

  // A pending timer must not fire after unmount, or it calls setState on a
  // component that is gone.
  useEffect(() => cancelOpen, [cancelOpen]);

  return (
    <aside className="rail" onMouseEnter={handleEnter} onMouseLeave={handleLeave}>
      <div className="rail-mark">
        <LogoCube size={32} />
        <span className="word">ATLAS·X</span>
      </div>

      {GROUPS.map((g) => (
        <React.Fragment key={g.key}>
          <div className="rail-group">{t(`groups.${g.key}`)}</div>
          {g.items.map(([path, icon, navKey]) => {
            const targetHead = path.replace(/^\/+/, '').split('/')[0] || '';
            const active = targetHead === head;
            const href = BASE + (path || '/');
            const label = t(`nav.${navKey}.label`);
            return (
              <Link key={path || 'home'} href={href} className={'rail-it ' + (active ? 'on' : '')} title={labelsVisible ? undefined : label}>
                <Icon name={icon} size={18} />
                <span className="lbl">{label}</span>
              </Link>
            );
          })}
        </React.Fragment>
      ))}
    </aside>
  );
}

function AppHeader() {
  const app = useApp();
  const t = useTranslations('atlasShell');
  const pathname = usePathname();
  const rel = relativePath(pathname);
  const segs = rel.split('/').filter(Boolean);
  const head = segs[0] || '';

  const crumbKey = head === '' ? 'home' : head;
  const crumb: [string, string] =
    head === 'token'
      ? [t('crumbs.token.eyebrow'), segs[1] || t('crumbs.token.title')]
      : head === 'profile'
        ? [
            t('crumbs.profile.eyebrow'),
            segs[1] ? `${segs[1].slice(0, 6)}…${segs[1].slice(-4)}` : t('crumbs.profile.title'),
          ]
        : CRUMB_KEYS.has(head)
          ? [t(`crumbs.${crumbKey}.eyebrow`), t(`crumbs.${crumbKey}.title`)]
          : [t('crumbs.fallbackEyebrow'), head];

  return (
    <header className="head">
      <div className="crumbs">
        <button
          className="btn btn-xs"
          onClick={() => app.setDrawerOpen(true)}
          data-tip={t('header.menuTip')}
          aria-label={t('header.openMenu')}
          id="ax-burger"
        >
          <Icon name="menu" size={16} />
        </button>
        <div>
          <div className="eyebrow">{crumb[0]}</div>
          <div className="ttl">{crumb[1]}</div>
        </div>
      </div>
      <div className="head-actions">
        <StreamStatus />
        <LanguageSwitcher variant="compact" />
        <ConnectPill />
      </div>
    </header>
  );
}

export function Frame({ children }: { children: React.ReactNode }) {
  const app = useApp();
  const setDrawerOpen = app.setDrawerOpen;
  const pathname = usePathname();
  const lastPathnameRef = useRef(pathname);

  useEffect(() => {
    document.body.style.overflow = app.drawerOpen ? 'hidden' : '';
  }, [app.drawerOpen]);

  // Close mobile drawer on route change
  useEffect(() => {
    if (lastPathnameRef.current !== pathname) {
      setDrawerOpen(false);
      lastPathnameRef.current = pathname;
    }
  }, [pathname, setDrawerOpen]);

  return (
    <div className="app" data-rail={app.railOpen ? 'open' : 'closed'} data-drawer={app.drawerOpen ? '1' : '0'}>
      <Sidebar />
      {app.drawerOpen && <div className="modal-bg" style={{ zIndex: 25 }} onClick={() => setDrawerOpen(false)} />}
      <main className="main">
        <AppHeader />
        <div className="content">
          <div key={pathname || '/'} className="page-in">
            {children}
          </div>
        </div>
      </main>
      <GlobalModals />
      <ToastHost />
    </div>
  );
}
