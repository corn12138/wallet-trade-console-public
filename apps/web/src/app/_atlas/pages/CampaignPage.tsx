'use client';
import Link from 'next/link';
import { useCallback, useEffect, useState } from 'react';
import { formatDistanceToNowStrict } from 'date-fns';
import { zhCN } from 'date-fns/locale';
import { useAccount } from 'wagmi';
import { useTranslations, useLocale } from 'next-intl';
import { Icon } from '../Icon';
import { PageHeader } from '../Common';
import { DataSkeleton, DataStatePanel } from '../DataState';
import { useApp } from '../AppContext';
import { getCampaigns, type AtlasCampaign } from '@/lib/api/atlas';
import {
  joinCampaign,
  removeCampaignReminder,
  setCampaignReminder,
} from '@/lib/api/social';

// Map the DB status (upcoming/active/ended) onto the display tag the
// existing UI styles around (LIVE/UPCOMING/ENDED).
function toDisplayStatus(dbStatus: string): 'LIVE' | 'UPCOMING' | 'ENDED' {
  switch (dbStatus.toLowerCase()) {
    case 'active':
      return 'LIVE';
    case 'upcoming':
      return 'UPCOMING';
    default:
      return 'ENDED';
  }
}

// timeLeft was a pre-formatted string in the mock (e.g. "2d 14h").
// We compute the equivalent from endDate via date-fns (locale-aware).
function formatTimeLeft(endIso: string, displayStatus: string, endedLabel: string, locale: string): string {
  if (displayStatus === 'ENDED') {
    return endedLabel;
  }
  try {
    return formatDistanceToNowStrict(new Date(endIso), {
      addSuffix: false,
      locale: locale === 'zh' ? zhCN : undefined,
    });
  } catch {
    return '';
  }
}

export function CampaignPage() {
  const app = useApp();
  const t = useTranslations('campaignAtlas');
  const locale = useLocale();
  const { isConnected } = useAccount();
  const [items, setItems] = useState<AtlasCampaign[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      // fetchApi attaches the web3 token when present, so the response
      // carries this wallet's isParticipating/hasReminder state.
      const data = await getCampaigns();
      setItems(data);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Wallet-auth guard shared by both actions: connected wallet + SIWE
  // session required; failures surface honestly instead of faking state.
  const runInteraction = async (
    campaign: AtlasCampaign,
    action: () => Promise<unknown>,
    successToast: string,
  ) => {
    if (!isConnected || app.walletState !== 'connected') {
      app.openConnect();
      return;
    }
    setBusyId(campaign.id);
    try {
      await action();
      await load(); // reflect the durable API state, not optimistic local state
      app.toast(successToast, 'ok');
    } catch (e) {
      const status = (e as { status?: number }).status;
      app.toast(
        status === 401
          ? t('toastSiweNeeded')
          : t('toastActionFailed', { message: (e as Error).message }),
        'err',
      );
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      {loading && <DataSkeleton shape="cards" count={3} />}

      {error && !loading && (
        <DataStatePanel
          state="error"
          endpoint="GET /api/campaign"
          emptyTitle={t('errorTitle')}
          emptyBody=""
          errorDetail={error}
          icon="star"
          onRetry={() => void load()}
        />
      )}

      {!loading && !error && items.length === 0 && (
        <DataStatePanel
          state="empty"
          endpoint="GET /api/campaign"
          emptyTitle={t('emptyTitle')}
          emptyBody={t('emptyBody')}
          icon="star"
          onRetry={() => void load()}
        />
      )}

      <section className="grid-2">
        {items.map((e) => {
          const displayStatus = toDisplayStatus(e.status);
          const timeLeft = formatTimeLeft(e.endDate, displayStatus, t('ended'), locale);
          const statusLabel =
            displayStatus === 'LIVE'
              ? t('statusLive')
              : displayStatus === 'UPCOMING'
                ? t('statusUpcoming')
                : t('statusEnded');
          const prize = e.reward ?? '—';
          const busy = busyId === e.id;
          return (
            <div
              key={e.id}
              className="block"
              style={{
                background:
                  displayStatus === 'LIVE'
                    ? 'var(--y)'
                    : displayStatus === 'UPCOMING'
                    ? 'var(--paper)'
                    : 'var(--bg-2)',
                opacity: displayStatus === 'ENDED' ? 0.6 : 1,
              }}
            >
              <div className="row between">
                <span className={'pill ' + (displayStatus === 'LIVE' ? 'live' : '')}>
                  {displayStatus === 'LIVE' && <span className="dot" />}
                  {statusLabel}
                </span>
                <span className="mono" style={{ fontSize: 12, color: 'var(--ink-2)' }}>
                  {timeLeft}
                </span>
              </div>
              <div className="h-display mt-14" style={{ fontSize: 26 }}>
                {e.title}
              </div>
              <div className="row gap-10 mt-14">
                <div className="block tight bg-paper2" style={{ flex: 1, boxShadow: 'none', padding: 12 }}>
                  <div className="eyebrow">{t('prizePool')}</div>
                  <div className="h-display" style={{ fontSize: 18, marginTop: 4 }}>
                    {prize}
                  </div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--ink-2)', marginTop: 4 }}>
                    {t('joinedCount', { count: e.participantCount })}
                  </div>
                </div>
                {displayStatus === 'LIVE' && (
                  <div className="col gap-6" style={{ alignSelf: 'center' }}>
                    {e.isParticipating ? (
                      <span className="pill live"><span className="dot" /> {t('joinedPill')}</span>
                    ) : (
                      <button
                        className="btn btn-sm"
                        disabled={busy}
                        onClick={() =>
                          void runInteraction(e, () => joinCampaign(e.id), t('toastJoined'))
                        }
                      >
                        {busy ? t('joining') : t('join')}
                      </button>
                    )}
                    <Link href="/trade" className="btn btn-d btn-sm">
                      {t('enter')} <Icon name="arrowRight" size={14} />
                    </Link>
                  </div>
                )}
                {displayStatus === 'UPCOMING' && (
                  <div className="col gap-6" style={{ alignSelf: 'center' }}>
                    {e.hasReminder ? (
                      <button
                        className="btn btn-sm"
                        disabled={busy}
                        onClick={() =>
                          void runInteraction(
                            e,
                            () => removeCampaignReminder(e.id),
                            t('toastReminderRemoved'),
                          )
                        }
                      >
                        {busy ? '…' : t('reminderSaved')}
                      </button>
                    ) : (
                      <button
                        className="btn btn-sm"
                        disabled={busy}
                        onClick={() =>
                          void runInteraction(
                            e,
                            () => setCampaignReminder(e.id),
                            // Honest: intent is stored to the wallet; nothing is
                            // emailed/pushed (no delivery pipeline exists).
                            t('toastReminderSaved'),
                          )
                        }
                      >
                        {busy ? t('saving') : t('remindMe')}
                      </button>
                    )}
                    <span className="pill flat" style={{ alignSelf: 'center' }}>
                      {t('startsIn', { time: timeLeft })}
                    </span>
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </section>
    </div>
  );
}
