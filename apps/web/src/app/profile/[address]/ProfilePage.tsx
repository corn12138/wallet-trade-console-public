'use client';

/**
 * Atlas X · /profile/[address]
 * Public wallet profile — holdings, created tokens, and trade history.
 * Backed by /token?creatorAddress=… for created tokens. Holdings and trades
 * fall back to portfolio + activity endpoints so the page works for any
 * address, not just the connected one.
 */

import { useMemo, useState } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import Link from 'next/link';
import { useParams } from 'next/navigation';
import { useAccount } from 'wagmi';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { buildApiUrl } from '@/lib/api/base-url';
import { getPortfolioAssets, getActivityFeed } from '@/lib/api/atlas';
import { followProfile, getFollowState, unfollowProfile } from '@/lib/api/social';
import { useApp } from '@/app/_atlas/AppContext';
import { Icon } from '@/app/_atlas/Icon';
import { Empty, MetricCard, PageHeader, TabBar } from '@/app/_atlas/Common';
import { fmt, fmtCompact, fmtCompactDec } from '@/app/_atlas/data';
import { useEnumLabel } from '@/app/_atlas/enums';

type ProfileTab = 'holdings' | 'created' | 'trades';

interface UserToken {
  id: string;
  address?: string;
  symbol: string;
  name: string;
  description?: string;
  image?: string;
  status?: string;
  marketCap?: string | null; // normalized NATIVE decimal string
  priceChange24h?: number;
  volume24h?: string | null;
  createdAt?: string;
}

const PALETTE = ['y', 'c', 'o', 'p', 'g', 'b', 'r'];
const TRADE_TYPES = new Set(['open', 'close', 'swap', 'trade']);

function short(address: string) {
  return `${address.slice(0, 6)}…${address.slice(-4)}`;
}

function timeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

export default function ProfilePage() {
  // `tp` (not `t`) because created/trades tabs bind `t` as the map item below.
  const tp = useTranslations('profileDetail');
  const locale = useLocale();
  const tokenStatusLabel = useEnumLabel('token');
  const params = useParams<{ address: string }>();
  const address = (params?.address || '').toLowerCase();
  const { address: connected } = useAccount();
  const app = useApp();
  const isOwner = Boolean(connected && connected.toLowerCase() === address);
  const [tab, setTab] = useState<ProfileTab>('holdings');

  /* ─────────── data ─────────── */
  const { data: createdTokens = [], isLoading: tokensLoading } = useQuery({
    queryKey: ['atlas-design-profile-tokens', address],
    queryFn: async (): Promise<UserToken[]> => {
      const res = await fetch(
        buildApiUrl(`/token?creatorAddress=${address}&limit=50`),
      );
      if (!res.ok) return [];
      const data = await res.json();
      return data.data || data.items || data || [];
    },
    enabled: Boolean(address),
  });

  const { data: portfolio, isLoading: holdingsLoading, isError: holdingsError } = useQuery({
    queryKey: ['atlas-design-profile-portfolio', address],
    queryFn: () => getPortfolioAssets(address),
    enabled: Boolean(address),
  });

  const { data: activity, isLoading: tradesLoading, isError: tradesError } = useQuery({
    queryKey: ['atlas-design-profile-activity', address],
    queryFn: () => getActivityFeed({ address, limit: 50 }),
    enabled: Boolean(address),
  });

  const holdings = useMemo(() => portfolio?.items ?? [], [portfolio]);
  const trades = useMemo(
    () => (activity?.items ?? []).filter((a) => TRADE_TYPES.has((a.type || '').toLowerCase())),
    [activity],
  );

  const totalValue = useMemo(
    () => holdings.reduce((s, a) => s + (a.valueUsd || 0), 0),
    [holdings],
  );

  // Durable follow graph (profile_follows rows). fetchApi carries the web3
  // token, so isFollowing reflects THIS viewer; anonymous visitors get counts.
  const queryClient = useQueryClient();
  const [followBusy, setFollowBusy] = useState(false);
  const { data: followState } = useQuery({
    queryKey: ['atlas-design-profile-follow', address],
    queryFn: () => getFollowState(address),
    enabled: Boolean(address),
  });

  const handleFollowToggle = async () => {
    if (!connected || app.walletState !== 'connected') {
      app.openConnect();
      return;
    }
    setFollowBusy(true);
    try {
      if (followState?.isFollowing) {
        await unfollowProfile(address);
        app.toast(tp('toastUnfollowed'), 'ok');
      } else {
        await followProfile(address);
        app.toast(tp('toastFollowing'), 'ok');
      }
      await queryClient.invalidateQueries({ queryKey: ['atlas-design-profile-follow', address] });
    } catch (e) {
      const status = (e as { status?: number }).status;
      app.toast(
        status === 401
          ? tp('toastSigninRequired')
          : tp('toastFollowFailed', { message: (e as Error).message }),
        'err',
      );
    } finally {
      setFollowBusy(false);
    }
  };

  const copyAddress = () => {
    navigator.clipboard?.writeText(address);
    app.toast(tp('toastAddressCopied'), 'ok');
  };

  if (!address) {
    return (
      <div className="col gap-24">
        <PageHeader
          eyebrow={tp('headerEyebrow')}
          title={tp('noAddressTitle')}
          kicker={tp('noAddressKicker')}
        />
      </div>
    );
  }

  return (
    <div className="col gap-24">
      {/* PROFILE HEADER */}
      <section className="block" style={{ padding: 22 }}>
        <div className="row between" style={{ flexWrap: 'wrap', gap: 14 }}>
          <div className="row gap-14">
            <div
              style={{
                width: 64,
                height: 64,
                borderRadius: 999,
                background: 'var(--y)',
                border: '3px solid var(--ink)',
                boxShadow: '0 4px 0 0 var(--ink)',
                display: 'grid',
                placeItems: 'center',
                fontFamily: 'var(--df)',
                fontWeight: 900,
                fontSize: 22,
              }}
            >
              {address.slice(2, 4).toUpperCase()}
            </div>
            <div>
              <div className="row gap-6" style={{ alignItems: 'center' }}>
                <span className="eyebrow">{tp('walletProfile')}</span>
                {isOwner && (
                  <span className="pill live">
                    <span className="dot" /> {tp('you')}
                  </span>
                )}
              </div>
              <div className="h-display" style={{ fontSize: 28, marginTop: 4 }}>
                {short(address)}
              </div>
              <div className="row gap-6 mt-6">
                <button className="btn btn-xs" onClick={copyAddress}>
                  <Icon name="copy" size={12} /> {tp('copy')}
                </button>
                <a
                  className="btn btn-xs"
                  href={`https://sepolia.etherscan.io/address/${address}`}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <Icon name="ext" size={12} /> {tp('explorer')}
                </a>
                {/* Follow persists a profile_follows row via the Go API —
                    never rendered for your own profile. */}
                {!isOwner && (
                  <button
                    className={'btn btn-xs' + (followState?.isFollowing ? '' : ' btn-y')}
                    disabled={followBusy}
                    onClick={() => void handleFollowToggle()}
                  >
                    {followBusy ? '…' : followState?.isFollowing ? tp('following') : tp('follow')}
                  </button>
                )}
                <span className="mono" style={{ fontSize: 11, color: 'var(--ink-2)', alignSelf: 'center' }}>
                  {followState ? tp('followers', { count: followState.followers }) : ''}
                </span>
              </div>
            </div>
          </div>
          <div className="row gap-14">
            <div style={{ textAlign: 'right' }}>
              <div className="eyebrow">{tp('portfolio')}</div>
              <div className="h-display" style={{ fontSize: 24, marginTop: 4 }}>
                {holdingsError ? '—' : `$${fmtCompact(totalValue)}`}
              </div>
            </div>
            <div style={{ textAlign: 'right' }}>
              <div className="eyebrow">{tp('trades30d')}</div>
              <div className="h-display" style={{ fontSize: 24, marginTop: 4 }}>
                {tradesError ? '—' : trades.length}
              </div>
            </div>
          </div>
        </div>
      </section>

      <section className="grid-4">
        <MetricCard label={tp('metricHoldings')} value={String(holdings.length)} sub={tp('metricHoldingsSub')} tone="y" icon="wallet" />
        <MetricCard label={tp('metricCreated')} value={String(createdTokens.length)} sub={tp('metricCreatedSub')} tone="p" icon="rocket" />
        <MetricCard label={tp('metricRecentTrades')} value={String(trades.length)} sub={tp('metricRecentTradesSub')} tone="c" icon="activity" />
        <MetricCard label={tp('metricVisibility')} value={isOwner ? tp('visibilityPrivate') : tp('visibilityPublic')} sub={isOwner ? tp('visibilityConnected') : tp('visibilityReadOnly')} tone="g" icon="security" />
      </section>

      <TabBar
        tabs={[
          { key: 'holdings' as const, label: `${tp('tabHoldings')} · ${holdings.length}` },
          { key: 'created' as const, label: `${tp('tabCreated')} · ${createdTokens.length}` },
          { key: 'trades' as const, label: `${tp('tabTrades')} · ${trades.length}` },
        ]}
        value={tab}
        onChange={setTab}
      />

      {tab === 'holdings' && (
        <section className="block tight">
          <div
            className="tbl"
            style={{ boxShadow: 'none', border: '2px solid var(--ink)', borderRadius: 14 }}
          >
            <div
              className="tbl-head"
              style={{ gridTemplateColumns: '1.5fr 1fr 1fr 1fr auto' }}
            >
              <span>{tp('colAsset')}</span>
              <span>{tp('colBalance')}</span>
              <span>{tp('colValue')}</span>
              <span>{tp('col24h')}</span>
              <span />
            </div>
            {holdingsLoading && (
              <div className="tbl-row" style={{ gridTemplateColumns: '1fr' }}>
                <div className="skel lg" />
              </div>
            )}
            {!holdingsLoading &&
              holdings.map((a, idx) => {
                const palette = PALETTE[idx % PALETTE.length];
                return (
                  <div
                    key={a.id}
                    className="tbl-row"
                    style={{ gridTemplateColumns: '1.5fr 1fr 1fr 1fr auto' }}
                  >
                    <div className="sym">
                      <span
                        className="b"
                        style={{
                          background: `var(--${palette})`,
                          color: ['c', 'b', 'p', 'o', 'r'].includes(palette) ? '#fff' : 'var(--ink)',
                        }}
                      >
                        {a.symbol[0]}
                      </span>
                      <div>
                        <div>{a.symbol}</div>
                        <div
                          style={{
                            fontFamily: 'var(--mf)',
                            fontSize: 10,
                            color: 'var(--ink-2)',
                            fontWeight: 400,
                          }}
                        >
                          {a.name}
                        </div>
                      </div>
                    </div>
                    <div className="mono">{fmt(Number(a.rawBalance) || 0, 4)}</div>
                    <div className="mono">${fmt(a.valueUsd || 0)}</div>
                    <div className="mono" style={{ color: 'var(--ink-2)' }}>—</div>
                    <Link href="/swap" className="btn btn-xs btn-y">
                      {tp('trade')}
                    </Link>
                  </div>
                );
              })}
            {!holdingsLoading && !holdings.length && (
              <Empty
                body={holdingsError ? tp('holdingsErrorBody') : tp('holdingsEmptyBody')}
              />
            )}
          </div>
        </section>
      )}

      {tab === 'created' && (
        <section className="grid-3">
          {tokensLoading && (
            <div className="block" style={{ padding: 24 }}>
              <div className="skel lg" />
            </div>
          )}
          {!tokensLoading &&
            createdTokens.map((t, idx) => {
              const palette = PALETTE[idx % PALETTE.length];
              // A row without a contract address (pre-index sync) must not be a
              // dead link — render a non-interactive pending card instead.
              const card = (
                <>
                  <div className="row between">
                    <span className="row gap-10">
                      <span
                        style={{
                          width: 40,
                          height: 40,
                          borderRadius: 999,
                          background: `var(--${palette})`,
                          color: ['c', 'b', 'p', 'o', 'r'].includes(palette) ? '#fff' : 'var(--ink)',
                          border: '2px solid var(--ink)',
                          display: 'grid',
                          placeItems: 'center',
                          fontFamily: 'var(--df)',
                          fontWeight: 900,
                          fontSize: 16,
                        }}
                      >
                        {t.symbol[0]}
                      </span>
                      <span>
                        <div className="h-display" style={{ fontSize: 18 }}>
                          ${t.symbol}
                        </div>
                        <div className="mono" style={{ fontSize: 11, color: 'var(--ink-2)' }}>
                          {t.name}
                        </div>
                      </span>
                    </span>
                    {t.status && (
                      <span className="pill flat" style={{ padding: '3px 8px', fontSize: 10 }}>
                        {tokenStatusLabel(t.status)}
                      </span>
                    )}
                  </div>
                  <div className="row between mt-14">
                    <div>
                      <div className="eyebrow">{tp('marketCap')}</div>
                      <div className="mono" style={{ fontSize: 16, fontWeight: 700, marginTop: 4 }}>
                        {t.marketCap ? `${fmtCompactDec(t.marketCap, locale)} NATIVE` : '—'}
                      </div>
                    </div>
                    <div style={{ textAlign: 'right' }}>
                      <div className="eyebrow">{tp('col24h')}</div>
                      <div
                        className="mono"
                        style={{
                          fontSize: 16,
                          fontWeight: 700,
                          marginTop: 4,
                          color:
                            t.priceChange24h && t.priceChange24h > 0
                              ? 'var(--pos)'
                              : t.priceChange24h && t.priceChange24h < 0
                                ? 'var(--neg)'
                                : 'var(--ink-2)',
                        }}
                      >
                        {typeof t.priceChange24h === 'number' ? `${t.priceChange24h >= 0 ? '+' : ''}${t.priceChange24h.toFixed(2)}%` : '—'}
                      </div>
                    </div>
                  </div>
                </>
              );
              return t.address ? (
                <Link key={t.id} href={`/token/${t.address}`} className="block lift" style={{ display: 'block' }}>
                  {card}
                </Link>
              ) : (
                <div
                  key={t.id}
                  className="block"
                  aria-disabled="true"
                  data-testid="created-token-pending"
                  style={{ display: 'block', opacity: 0.75 }}
                >
                  {card}
                  <div className="mono" style={{ fontSize: 11, marginTop: 10, color: 'var(--ink-2)' }}>
                    {tp('tokenAddressPending')}
                  </div>
                </div>
              );
            })}
          {!tokensLoading && isOwner && (
            <Link
              href="/create-token"
              className="block lift"
              style={{
                display: 'grid',
                placeItems: 'center',
                padding: 40,
                borderStyle: 'dashed',
                textAlign: 'center',
              }}
            >
              <Icon name="plus" size={24} />
              <div className="h-display mt-14" style={{ fontSize: 16 }}>
                {tp('launchAnother')}
              </div>
              <div style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 4 }}>
                {tp('openLaunchpad')}
              </div>
            </Link>
          )}
          {!tokensLoading && !createdTokens.length && !isOwner && (
            <Empty body={tp('createdEmptyBody')} />
          )}
        </section>
      )}

      {tab === 'trades' && (
        <section className="block tight">
          <div
            className="tbl"
            style={{ boxShadow: 'none', border: '2px solid var(--ink)', borderRadius: 14 }}
          >
            <div
              className="tbl-head"
              style={{ gridTemplateColumns: 'auto 1fr 0.7fr 1.4fr auto' }}
            >
              <span>{tp('colTime')}</span>
              <span>{tp('colEvent')}</span>
              <span>{tp('colType')}</span>
              <span>{tp('colDetail')}</span>
              <span />
            </div>
            {tradesLoading && (
              <div className="tbl-row" style={{ gridTemplateColumns: '1fr' }}>
                <div className="skel lg" />
              </div>
            )}
            {!tradesLoading &&
              trades.map((t) => {
                const isShort = (t.type || '').toLowerCase() === 'close';
                return (
                  <div
                    key={t.id}
                    className="tbl-row"
                    style={{ gridTemplateColumns: 'auto 1fr 0.7fr 1.4fr auto' }}
                  >
                    <div className="mono" style={{ color: 'var(--ink-2)' }}>
                      {timeAgo(t.timestamp)}
                    </div>
                    <div className="sym">
                      <span className="b">{(t.title || t.type || '?')[0].toUpperCase()}</span>
                      <span style={{ fontSize: 13 }}>{t.title || t.type || '—'}</span>
                    </div>
                    <div>
                      <span className={'side ' + (isShort ? 'short' : 'long')}>
                        {t.type || '—'}
                      </span>
                    </div>
                    <div className="mono" style={{ color: 'var(--ink-2)' }}>
                      {t.subtitle || '—'}
                    </div>
                    {t.txHash ? (
                      <a
                        className="btn btn-xs"
                        href={`https://sepolia.etherscan.io/tx/${t.txHash}`}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        <Icon name="ext" size={12} />
                      </a>
                    ) : (
                      <span />
                    )}
                  </div>
                );
              })}
            {!tradesLoading && !trades.length && (
              <Empty
                body={tradesError ? tp('tradesErrorBody') : tp('tradesEmptyBody')}
              />
            )}
          </div>
        </section>
      )}
    </div>
  );
}
