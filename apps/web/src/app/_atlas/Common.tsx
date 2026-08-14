'use client';
import React, { useEffect, useRef, useState } from 'react';
import { useConnect } from 'wagmi';
import { useTranslations } from 'next-intl';
import { Icon } from './Icon';
import { useApp } from './AppContext';
import { useTokenBalance } from '@/hooks/web3/useTokenBalance';
import { getPerpAddresses } from '@/lib/web3/contracts';
import { buildAddressExplorerUrl } from '@/lib/web3/explorer';
import { supportedChains as web3SupportedChains } from '@/lib/web3';

/* ─────────── helpers to map real chains → design palette ─────────── */
function chainSwatch(id: number) {
  // Sepolia (11155111) → cyan, Mainnet (1) → blue, Anvil (31337) → yellow
  if (id === 11155111) return { color: 'c', icon: 'S' };
  if (id === 1) return { color: 'b', icon: 'M' };
  if (id === 31337) return { color: 'y', icon: 'L' };
  if (id === 84532) return { color: 'b', icon: 'B' };
  if (id === 421614) return { color: 'p', icon: 'A' };
  return { color: 'p', icon: '?' };
}

function chainList() {
  return web3SupportedChains.map((c) => {
    const s = chainSwatch(c.id);
    return { id: c.id, name: c.name, ...s };
  });
}

export function ConnectPill({ size = 'md' }: { size?: 'sm' | 'md' }) {
  const app = useApp();
  const t = useTranslations('atlasShell.connect');
  const { walletState, wallet, openConnect, openChain, disconnect, chainId } = app;
  const [menuOpen, setMenuOpen] = useState(false);

  const swatch = chainSwatch(chainId);
  const chain = web3SupportedChains.find((c) => c.id === chainId);

  /* Real USDC balance on the active chain */
  const addrs = getPerpAddresses(chainId);
  const { formatted: usdcFormatted } = useTokenBalance(addrs?.usdc || undefined);

  if (walletState === 'disconnected') {
    return (
      <button className={'btn btn-y ' + (size === 'sm' ? 'btn-sm' : '')} onClick={openConnect} data-testid="connect-wallet">
        {t('connectWallet')}
      </button>
    );
  }
  if (walletState === 'connecting') {
    return (
      <button className={'btn ' + (size === 'sm' ? 'btn-sm' : '')} disabled>
        <span className="spinner" /> {t('connecting')}
      </button>
    );
  }
  if (walletState === 'siwe') {
    return (
      <button className={'btn btn-o ' + (size === 'sm' ? 'btn-sm' : '')} onClick={app.signSiwe}>
        {t('signInToContinue')}
      </button>
    );
  }
  if (walletState === 'wrong-network') {
    return (
      <button className={'btn btn-o ' + (size === 'sm' ? 'btn-sm' : '')} onClick={openChain}>
        {t('switchNetwork')}
      </button>
    );
  }

  const inverseInk = swatch.color === 'c' || swatch.color === 'b' || swatch.color === 'p';

  return (
    <div style={{ display: 'inline-flex', gap: 8, alignItems: 'center', position: 'relative' }}>
      <button
        className="btn btn-sm"
        onClick={openChain}
        data-tip={t('switchNetworkTip')}
        style={{ background: `var(--${swatch.color})`, color: inverseInk ? '#fff' : 'var(--ink)' }}
      >
        <span
          style={{
            width: 18,
            height: 18,
            borderRadius: 999,
            background: 'rgba(255,255,255,0.3)',
            display: 'grid',
            placeItems: 'center',
            fontFamily: 'var(--df)',
            fontWeight: 900,
            fontSize: 10,
            color: 'var(--ink)',
          }}
        >
          {swatch.icon}
        </span>
        {chain?.name || t('chainFallback', { chainId })}
      </button>
      <button className="btn btn-sm" onClick={() => setMenuOpen((o) => !o)}>
        <span className="status-dot live" />
        <span className="mono">{wallet!.address.slice(0, 6)}…{wallet!.address.slice(-4)}</span>
        {usdcFormatted && usdcFormatted !== '—' && (
          <span
            style={{
              background: 'var(--y)',
              padding: '3px 8px',
              borderRadius: 999,
              border: '2px solid var(--ink)',
              fontFamily: 'var(--df)',
              fontWeight: 800,
              fontSize: 11,
            }}
          >
            {usdcFormatted} USDC
          </span>
        )}
      </button>
      {menuOpen && <AccountMenu close={() => setMenuOpen(false)} disconnect={disconnect} />}
    </div>
  );
}

function AccountMenu({ close, disconnect }: { close: () => void; disconnect: () => void }) {
  const app = useApp();
  const t = useTranslations('atlasShell.connect');
  const ref = useRef<HTMLDivElement>(null);
  const addrs = getPerpAddresses(app.chainId);
  const { formatted: usdcFormatted } = useTokenBalance(addrs?.usdc || undefined);

  useEffect(() => {
    const h = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) close();
    };
    const t = setTimeout(() => document.addEventListener('click', h), 0);
    return () => {
      clearTimeout(t);
      document.removeEventListener('click', h);
    };
  }, [close]);

  const copy = () => {
    if (app.wallet) navigator.clipboard?.writeText(app.wallet.address);
    app.toast(t('addressCopied'));
  };

  return (
    <div
      ref={ref}
      className="block"
      style={{
        position: 'absolute',
        right: 0,
        top: 'calc(100% + 8px)',
        width: 280,
        zIndex: 50,
        padding: 16,
        boxShadow: '0 10px 0 0 var(--ink)',
      }}
    >
      <div className="eyebrow">{t('connected')}</div>
      <div style={{ fontFamily: 'var(--mf)', fontSize: 13, marginTop: 6, marginBottom: 12, wordBreak: 'break-all' }}>
        {app.wallet?.address}
      </div>
      <div className="row" style={{ gap: 6 }}>
        <button className="btn btn-xs" onClick={copy}>
          <Icon name="copy" size={14} /> {t('copy')}
        </button>
        {(() => {
          const explorerUrl = app.wallet
            ? buildAddressExplorerUrl(app.wallet.address, app.chainId)
            : null;
          return explorerUrl ? (
            <a className="btn btn-xs" href={explorerUrl} target="_blank" rel="noopener noreferrer">
              <Icon name="ext" size={14} /> {t('explorer')}
            </a>
          ) : null;
        })()}
        <button
          className="btn btn-xs"
          onClick={() => {
            close();
            app.openChain();
          }}
        >
          <Icon name="globe" size={14} /> {t('switch')}
        </button>
      </div>
      <div className="mt-14">
        <div className="eyebrow">{t('usdcBalance')}</div>
        <div style={{ fontFamily: 'var(--df)', fontWeight: 900, fontSize: 24, marginTop: 4 }}>
          {usdcFormatted || '—'} <span style={{ fontSize: 12, color: 'var(--ink-2)' }}>USDC</span>
        </div>
      </div>
      <button
        className="btn btn-sm btn-o"
        style={{ width: '100%', marginTop: 14 }}
        onClick={() => {
          disconnect();
          close();
        }}
      >
        {t('disconnect')}
      </button>
    </div>
  );
}

/* Token picker over the real per-chain token list passed by the caller —
   no mock fallback: an empty list renders an honest empty state. */
function TokenPickerModal({
  tokens,
  exclude,
  onPick,
  onBg,
  close,
}: {
  tokens: Array<{ symbol: string; name: string; address?: string }>;
  exclude?: string;
  onPick?: (token: unknown) => void;
  onBg: (e: React.MouseEvent) => void;
  close: () => void;
}) {
  const t = useTranslations('atlasShell.modals');
  const [query, setQuery] = useState('');
  const palette = ['c', 'b', 'o', 'p', 'y', 'g', 'r'];
  const list = tokens
    .filter((t) => t.address !== exclude && t.symbol !== exclude)
    .map((t, i) => ({
      key: t.address || t.symbol,
      symbol: t.symbol,
      name: t.name,
      address: t.address,
      color: palette[i % palette.length],
      raw: t,
    }))
    .filter((t) => {
      const q = query.trim().toLowerCase();
      if (!q) return true;
      return (
        t.symbol.toLowerCase().includes(q) ||
        t.name.toLowerCase().includes(q) ||
        (t.address || '').toLowerCase().includes(q)
      );
    });

  return (
    <div className="modal-bg" onClick={onBg}>
      <div className="modal-box">
        <div className="modal-head">
          <h3>{t('tokenPickerTitle')}</h3>
          <button className="modal-x" onClick={close}>
            <Icon name="close" size={16} />
          </button>
        </div>
        <div className="input-row" style={{ marginBottom: 10 }}>
          <Icon name="search" size={16} />
          <input
            placeholder={t('tokenSearchPlaceholder')}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            autoFocus
          />
        </div>
        {list.map((t) => {
          const addrLabel = t.address && t.address.length > 12 ? `${t.address.slice(0, 6)}…${t.address.slice(-4)}` : t.address;
          return (
            <div
              key={t.key}
              className="wal-opt"
              onClick={() => {
                onPick && onPick(t.raw);
                close();
              }}
            >
              <span
                className="g"
                style={{
                  background: `var(--${t.color})`,
                  color: ['c', 'b', 'p', 'o'].includes(t.color) ? '#fff' : 'var(--ink)',
                }}
              >
                {t.symbol[0]}
              </span>
              <div style={{ flex: 1 }}>
                <div className="n">{t.symbol}</div>
                <div className="s">
                  {t.name}
                  {addrLabel ? ` · ${addrLabel}` : ''}
                </div>
              </div>
            </div>
          );
        })}
        {!list.length && (
          <div className="mono" style={{ padding: 14, fontSize: 12, color: 'var(--ink-2)' }}>
            {query ? t('noTokenMatches', { query }) : t('noTokensDeployed')}
          </div>
        )}
      </div>
    </div>
  );
}

export function GlobalModals() {
  const app = useApp();
  const t = useTranslations('atlasShell.modals');
  const { connectors } = useConnect();
  if (!app.modal) return null;
  const onBg = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) app.closeModal();
  };

  if (app.modal.kind === 'wallet') {
    /* Real wagmi connectors. Falls back to a single "Browser wallet" entry
       when only `injected` is configured (current setup). */
    const real = connectors.length
      ? connectors.map((c) => ({ id: c.id, n: c.name || c.id, s: c.type || 'injected', g: (c.name || c.id)[0].toUpperCase() }))
      : [{ id: 'injected', n: t('browserWallet'), s: 'MetaMask / Rabby / OKX', g: 'B' }];
    return (
      <div className="modal-bg" onClick={onBg}>
        <div className="modal-box">
          <div className="modal-head">
            <h3>{t('connectTitle')}</h3>
            <button className="modal-x" onClick={app.closeModal}>
              <Icon name="close" size={16} />
            </button>
          </div>
          {real.map((w) => (
            <div key={w.id} className="wal-opt" onClick={() => app.pickWallet(w)} data-testid={`wallet-opt-${w.id}`}>
              <span className="g">{w.g}</span>
              <div style={{ flex: 1 }}>
                <div className="n">{w.n}</div>
                <div className="s">{w.s}</div>
              </div>
              <Icon name="chevRight" size={16} />
            </div>
          ))}
          <div className="mt-14" style={{ fontFamily: 'var(--mf)', fontSize: 11, color: 'var(--ink-2)' }}>
            {t('walletSignsNote')}
          </div>
        </div>
      </div>
    );
  }
  if (app.modal.kind === 'siwe') {
    const addr = app.wallet?.address;
    const shortAddr = addr ? `${addr.slice(0, 6)}…${addr.slice(-4)}` : '0x…';
    const host = typeof window !== 'undefined' ? window.location.host : 'atlas-x.app';
    const uri = typeof window !== 'undefined' ? window.location.origin : 'https://atlas-x.app';
    return (
      <div className="modal-bg" onClick={onBg}>
        <div className="modal-box">
          <div className="modal-head">
            <h3>{t('siweTitle')}</h3>
            <button className="modal-x" onClick={app.closeModal}>
              <Icon name="close" size={16} />
            </button>
          </div>
          <div style={{ padding: 14, background: 'var(--bg-2)', border: '2px dashed var(--ink)', borderRadius: 14 }}>
            <div className="eyebrow">{t('messageToSign')}</div>
            <pre
              style={{
                margin: '8px 0 0',
                fontFamily: 'var(--mf)',
                fontSize: 12,
                whiteSpace: 'pre-wrap',
                lineHeight: 1.5,
              }}
            >{`${host} wants you to sign in
with your Ethereum account:

${shortAddr}

Statement: Authenticate to Atlas X
URI: ${uri}
Chain ID: ${app.chainId}
Nonce: (signed at submission)`}</pre>
          </div>
          <div className="row" style={{ marginTop: 16 }}>
            <button className="btn btn-sm" onClick={app.disconnect}>
              {t('cancel')}
            </button>
            <button className="btn btn-sm btn-y" style={{ flex: 1 }} onClick={app.signSiwe} data-testid="siwe-sign">
              {t('signMessage')}
            </button>
          </div>
        </div>
      </div>
    );
  }
  if (app.modal.kind === 'chain') {
    const chains = chainList();
    return (
      <div className="modal-bg" onClick={onBg}>
        <div className="modal-box">
          <div className="modal-head">
            <h3>{t('switchNetworkTitle')}</h3>
            <button className="modal-x" onClick={app.closeModal}>
              <Icon name="close" size={16} />
            </button>
          </div>
          {chains.map((c) => (
            <div
              key={c.id}
              className="wal-opt"
              data-active={c.id === app.chainId ? '1' : '0'}
              onClick={() => app.switchChain(c)}
            >
              <span
                className="g"
                style={{
                  background: `var(--${c.color})`,
                  color: ['c', 'b', 'p'].includes(c.color) ? '#fff' : 'var(--ink)',
                }}
              >
                {c.icon}
              </span>
              <div style={{ flex: 1 }}>
                <div className="n">{c.name}</div>
                <div className="s">{t('chainIdLabel', { chainId: c.id })}</div>
              </div>
              {c.id === app.chainId && <Icon name="check" size={18} />}
            </div>
          ))}
        </div>
      </div>
    );
  }
  if (app.modal.kind === 'tokenPicker') {
    const { onPick, exclude, tokens: realTokens } = app.modal.props || {};
    return (
      <TokenPickerModal
        tokens={realTokens || []}
        exclude={exclude}
        onPick={onPick}
        onBg={onBg}
        close={app.closeModal}
      />
    );
  }
  if (app.modal.kind === 'tx') {
    const { stage, title, summary, hash, explorerUrl, error } = app.modal.props || {};
    return (
      <div className="modal-bg" onClick={onBg}>
        <div className="modal-box">
          <div className="modal-head">
            <h3>{title || t('txTitle')}</h3>
            <button className="modal-x" onClick={app.closeModal}>
              <Icon name="close" size={16} />
            </button>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 6, marginBottom: 14 }}>
            {[t('stageSubmit'), t('stagePending'), t('stageConfirmed')].map((s, i) => {
              const cur = stage === 'pending' ? 1 : stage === 'confirmed' ? 2 : 0;
              const past = i <= cur;
              return (
                <div
                  key={s}
                  style={{
                    padding: 12,
                    borderRadius: 12,
                    border: '3px solid var(--ink)',
                    background: past ? 'var(--y)' : 'var(--bg-2)',
                    textAlign: 'center',
                    fontFamily: 'var(--df)',
                    fontWeight: 800,
                    fontSize: 11,
                    letterSpacing: '0.06em',
                  }}
                >
                  <div style={{ fontSize: 16 }}>
                    {i === cur && stage !== 'confirmed' ? <span className="spinner" /> : past ? '✓' : '·'}
                  </div>
                  {s.toUpperCase()}
                </div>
              );
            })}
          </div>
          <div
            style={{
              background: 'var(--bg-2)',
              border: '2px solid var(--ink)',
              borderRadius: 12,
              padding: 14,
              fontFamily: 'var(--mf)',
              fontSize: 13,
              lineHeight: 1.6,
              whiteSpace: 'pre-wrap',
            }}
          >
            {summary || '—'}
            {hash && (
              <div style={{ marginTop: 10, color: 'var(--ink-2)', wordBreak: 'break-all' }} data-testid="tx-modal-hash">
                tx: <span style={{ color: 'var(--ink)' }}>{hash}</span>
              </div>
            )}
            {error && (
              <div style={{ marginTop: 10, color: 'var(--neg)' }} data-testid="tx-modal-error">
                {error}
              </div>
            )}
          </div>
          {explorerUrl && (
            <a
              className="btn btn-xs mt-14"
              href={explorerUrl}
              target="_blank"
              rel="noopener noreferrer"
              data-testid="tx-modal-explorer"
              style={{ width: '100%', justifyContent: 'center' }}
            >
              <Icon name="ext" size={14} /> {t('viewOnExplorer')}
            </a>
          )}
          {stage === 'confirmed' && (
            <button className="btn btn-sm btn-g" style={{ width: '100%', marginTop: 14 }} onClick={app.closeModal}>
              {t('done')}
            </button>
          )}
        </div>
      </div>
    );
  }
  return null;
}

export function ToastHost() {
  const app = useApp();
  return (
    <div className="toast-host">
      {app.toasts.map((t) => (
        <div key={t.id} className={'toast ' + (t.kind === 'warn' ? 'warn' : t.kind === 'err' ? 'err' : '')}>
          <span className="dot" />
          <span>{t.msg}</span>
        </div>
      ))}
    </div>
  );
}

export function StreamStatus() {
  // No global stream-health probe is wired to this pill, so it must not fabricate a
  // "Live · 12ms" latency (the old hardcoded value was not measured). Per-symbol realtime
  // health is surfaced where the market stream is actually subscribed (useMarketStream).
  // Here we show neutral, honest copy: this deployment targets the Sepolia testnet.
  const t = useTranslations('atlasShell.stream');
  return (
    <span className="pill" data-tip={t('sepoliaTip')}>
      <span className="dot" /> {t('sepoliaTestnet')}
    </span>
  );
}

export type PageHeaderProps = {
  eyebrow: string;
  title: string;
  kicker?: string;
  actions?: React.ReactNode;
};

export function PageHeader({ eyebrow, title, kicker, actions }: PageHeaderProps) {
  return (
    <div className="block" style={{ padding: 24, background: 'var(--paper)' }}>
      <div className="row between" style={{ alignItems: 'flex-end', gap: 24, flexWrap: 'wrap' }}>
        <div style={{ minWidth: 0, flex: 1 }}>
          <div className="eyebrow">{eyebrow}</div>
          <div className="h-display" style={{ fontSize: 40, marginTop: 6 }}>
            {title}
          </div>
          {kicker && (
            <div style={{ marginTop: 10, color: 'var(--ink-2)', maxWidth: 720, lineHeight: 1.5 }}>{kicker}</div>
          )}
        </div>
        {actions && <div className="row gap-10">{actions}</div>}
      </div>
    </div>
  );
}

export type Tone = 'paper' | 'y' | 'o' | 'p' | 'c' | 'g' | 'd';

export function MetricCard({
  label,
  value,
  sub,
  tone = 'paper',
  icon,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  tone?: Tone;
  icon?: string;
}) {
  const bg = ({ paper: '', y: 'bg-y', o: 'bg-o', p: 'bg-p', c: 'bg-c', g: 'bg-g', d: 'bg-d' } as Record<Tone, string>)[tone];
  return (
    <div className={'block ' + bg} style={{ padding: 18 }}>
      <div className="row between">
        <div className="eyebrow" style={{ opacity: 0.78 }}>
          {label}
        </div>
        {icon && <Icon name={icon} size={18} />}
      </div>
      <div className="h-display" style={{ fontSize: 32, marginTop: 10 }}>
        {value}
      </div>
      {sub && <div style={{ fontFamily: 'var(--mf)', fontSize: 12, marginTop: 6, opacity: 0.85 }}>{sub}</div>}
    </div>
  );
}

export function Empty({
  icon = 'cube',
  title,
  body,
}: {
  icon?: string;
  title?: string;
  body?: React.ReactNode;
}) {
  const t = useTranslations('commonAtlas');
  const resolvedTitle = title ?? t('nothingHereYet');
  return (
    <div className="block" style={{ padding: 36, textAlign: 'center' }}>
      <div
        style={{
          width: 56,
          height: 56,
          borderRadius: 16,
          background: 'var(--bg-2)',
          border: '3px solid var(--ink)',
          display: 'grid',
          placeItems: 'center',
          margin: '0 auto 14px',
        }}
      >
        <Icon name={icon} size={26} />
      </div>
      <div className="h-display" style={{ fontSize: 22 }}>
        {resolvedTitle}
      </div>
      {body && (
        <div style={{ marginTop: 8, color: 'var(--ink-2)', maxWidth: 380, margin: '8px auto 0', lineHeight: 1.5 }}>
          {body}
        </div>
      )}
    </div>
  );
}

export function TabBar<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: { key: T; label: string }[];
  value: T;
  onChange: (k: T) => void;
}) {
  return (
    <div
      className="row"
      style={{
        background: 'var(--paper)',
        border: '3px solid var(--ink)',
        borderRadius: 14,
        padding: 5,
        gap: 4,
        width: 'fit-content',
      }}
    >
      {tabs.map((t) => (
        <button
          key={t.key}
          onClick={() => onChange(t.key)}
          style={{
            padding: '9px 14px',
            borderRadius: 10,
            fontFamily: 'var(--df)',
            fontWeight: 700,
            fontSize: 12,
            letterSpacing: '0.04em',
            color: value === t.key ? 'var(--ink)' : 'var(--ink-2)',
            background: value === t.key ? 'var(--y)' : 'transparent',
            boxShadow: value === t.key ? '0 2px 0 0 var(--ink)' : 'none',
          }}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

export function BlockBtn({
  icon,
  children,
  tone,
  sm,
  onClick,
  disabled,
  full,
  tip,
  type = 'button',
}: {
  icon?: string;
  children: React.ReactNode;
  tone?: 'y' | 'o' | 'p' | 'c' | 'g' | 'd';
  sm?: boolean;
  onClick?: () => void;
  disabled?: boolean;
  full?: boolean;
  tip?: string;
  type?: 'button' | 'submit';
}) {
  const cls = ['btn', sm ? 'btn-sm' : '', tone ? 'btn-' + tone : ''].filter(Boolean).join(' ');
  return (
    <button type={type} className={cls} onClick={onClick} disabled={disabled} data-tip={tip} style={full ? { width: '100%' } : undefined}>
      {icon && <Icon name={icon} size={sm ? 14 : 16} />}
      {children}
    </button>
  );
}

export function PageShell({ children }: { children: React.ReactNode }) {
  return <div className="page-in">{children}</div>;
}
