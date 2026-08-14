'use client';
import { useTranslations } from 'next-intl';
import { useAccount } from 'wagmi';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  getNotificationPreferences,
  getUserPreferences,
  updateNotificationPreferences,
  updateUserPreferences,
} from '@/lib/api/atlas';
import { useTradingDefaults } from '@/lib/trading-defaults';
import { useApp } from '../AppContext';
import { PageHeader } from '../Common';

export function SettingsPage() {
  const app = useApp();
  const t = useTranslations('settingsAtlas');
  const { address } = useAccount();
  const qc = useQueryClient();

  // The settings service is SIWE-guarded: without a connected wallet these
  // endpoints 401, so don't fire them — the page renders read-only defaults
  // with an explicit "connect to persist" explanation instead.
  const prefsQ = useQuery({
    queryKey: ['atlas-design-prefs', address],
    queryFn: () => getUserPreferences(address),
    refetchInterval: 60_000,
    enabled: Boolean(address),
  });

  const notifQ = useQuery({
    queryKey: ['atlas-design-notif', address],
    queryFn: () => getNotificationPreferences(address),
    refetchInterval: 60_000,
    enabled: Boolean(address),
  });

  const updatePrefs = useMutation({
    mutationFn: (patch: {
      theme?: string;
      fiatCurrency?: string;
      testnetEnabled?: boolean;
      notificationsEnabled?: boolean;
      spamFilterLevel?: string;
    }) =>
      updateUserPreferences({
        ownerAddress: address || '',
        ...patch,
      }),
    onSuccess: (next) => {
      qc.setQueryData(['atlas-design-prefs', address], next);
      app.toast(t('toastPrefsUpdated'), 'ok');
    },
    onError: (e: any) => app.toast(e?.message || t('toastUpdateFailed'), 'err'),
  });

  const updateNotif = useMutation({
    mutationFn: (patch: {
      securityAlerts?: boolean;
      txUpdates?: boolean;
      marketAlerts?: boolean;
      productUpdates?: boolean;
    }) =>
      updateNotificationPreferences({
        ownerAddress: address || '',
        ...patch,
      }),
    onSuccess: (next) => {
      qc.setQueryData(['atlas-design-notif', address], next);
      app.toast(t('toastNotifUpdated'), 'ok');
    },
    onError: (e: any) => app.toast(e?.message || t('toastUpdateFailed'), 'err'),
  });

  const prefs = prefsQ.data;
  const notif = notifQ.data;
  const readOnly = !address || prefs?.source === 'default';

  // Device-local defaults, persisted in localStorage and genuinely consumed by
  // the Swap and Trade transaction builders (see lib/trading-defaults.ts).
  const {
    slippagePercent,
    deadlineMinutes,
    setSlippagePercent,
    setDeadlineMinutes,
  } = useTradingDefaults();

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={readOnly ? t('kickerReadOnly') : t('kickerConnected')}
      />

      <section className="page-split" style={{ '--split-l': '1.4fr', '--split-r': '0.6fr' } as React.CSSProperties}>
        <div className="col">
          <div className="block">
            <h3 className="h-display" style={{ fontSize: 20, marginBottom: 6 }}>{t('workspace')}</h3>
            <div className="grid-2 mt-14">
              <div>
                <div className="eyebrow">{t('theme')}</div>
                <div className="seg s2 mt-6">
                  <button
                    className={prefs?.theme === 'block-party' ? 'on' : ''}
                    onClick={() => !readOnly && updatePrefs.mutate({ theme: 'block-party' })}
                    disabled={readOnly}
                  >
                    Block Party
                  </button>
                  <button
                    className={prefs?.theme === 'atlas-dark' ? 'on' : ''}
                    onClick={() => !readOnly && updatePrefs.mutate({ theme: 'atlas-dark' })}
                    disabled={readOnly}
                  >
                    Atlas Dark
                  </button>
                </div>
              </div>
              <div>
                <div className="eyebrow">{t('fiat')}</div>
                <div className="seg s2 mt-6">
                  <button
                    className={prefs?.fiatCurrency === 'USD' ? 'on' : ''}
                    onClick={() => !readOnly && updatePrefs.mutate({ fiatCurrency: 'USD' })}
                    disabled={readOnly}
                  >
                    USD
                  </button>
                  <button
                    className={prefs?.fiatCurrency === 'CNY' ? 'on' : ''}
                    onClick={() => !readOnly && updatePrefs.mutate({ fiatCurrency: 'CNY' })}
                    disabled={readOnly}
                  >
                    CNY
                  </button>
                </div>
              </div>
            </div>
            <SettingsToggleRow
              label={t('showTestnets')}
              hint={t('showTestnetsHint')}
              value={Boolean(prefs?.testnetEnabled)}
              disabled={readOnly}
              onChange={(v) => updatePrefs.mutate({ testnetEnabled: v })}
            />
            <SettingsToggleRow
              label={t('allNotifications')}
              hint={t('allNotificationsHint')}
              value={Boolean(prefs?.notificationsEnabled)}
              disabled={readOnly}
              onChange={(v) => updatePrefs.mutate({ notificationsEnabled: v })}
            />
            <div className="row between" style={{ padding: '14px 0', borderBottom: '2px solid var(--bg-2)' }}>
              <div>
                <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 14 }}>{t('spamFilter')}</div>
                <div style={{ color: 'var(--ink-2)', fontSize: 12, marginTop: 4 }}>
                  {t('spamFilterHint')}
                </div>
              </div>
              <div className="seg s3" style={{ minWidth: 240 }}>
                {(['off', 'low', 'high'] as const).map((level) => (
                  <button
                    key={level}
                    className={prefs?.spamFilterLevel === level ? 'on' : ''}
                    onClick={() => !readOnly && updatePrefs.mutate({ spamFilterLevel: level })}
                    disabled={readOnly}
                  >
                    {level}
                  </button>
                ))}
              </div>
            </div>
          </div>

          <div className="block">
            <h3 className="h-display" style={{ fontSize: 20, marginBottom: 6 }}>{t('tradingDefaults')}</h3>
            <div className="grid-2 mt-14">
              <div className="field">
                <div className="l"><span>{t('defaultSlippage')}</span><span>%</span></div>
                <input
                  type="number"
                  min="0"
                  max="5"
                  step="0.1"
                  value={slippagePercent}
                  onChange={(e) => setSlippagePercent(e.target.value)}
                />
              </div>
              <div className="field">
                <div className="l"><span>{t('deadline')}</span><span>min</span></div>
                <input
                  type="number"
                  min="1"
                  max="120"
                  step="1"
                  value={deadlineMinutes}
                  onChange={(e) => setDeadlineMinutes(e.target.value)}
                />
              </div>
            </div>
            <div className="mono mt-14" style={{ fontSize: 11, color: 'var(--ink-2)' }}>
              {t('deviceNote')}
            </div>
          </div>

          <div className="block">
            <h3 className="h-display" style={{ fontSize: 20, marginBottom: 6 }}>{t('notifications')}</h3>
            <SettingsToggleRow
              label={t('securityAlerts')}
              hint={t('securityAlertsHint')}
              value={Boolean(notif?.securityAlerts)}
              disabled={readOnly}
              onChange={(v) => updateNotif.mutate({ securityAlerts: v })}
            />
            <SettingsToggleRow
              label={t('txUpdates')}
              hint={t('txUpdatesHint')}
              value={Boolean(notif?.txUpdates)}
              disabled={readOnly}
              onChange={(v) => updateNotif.mutate({ txUpdates: v })}
            />
            <SettingsToggleRow
              label={t('marketAlerts')}
              hint={t('marketAlertsHint')}
              value={Boolean(notif?.marketAlerts)}
              disabled={readOnly}
              onChange={(v) => updateNotif.mutate({ marketAlerts: v })}
            />
            <SettingsToggleRow
              label={t('productUpdates')}
              hint={t('productUpdatesHint')}
              value={Boolean(notif?.productUpdates)}
              disabled={readOnly}
              onChange={(v) => updateNotif.mutate({ productUpdates: v })}
            />
          </div>
        </div>

        <div className="col">
          <div className="block bg-paper2">
            <div className="eyebrow">{t('connectedWallet')}</div>
            {address ? (
              <>
                <div className="mono mt-14" style={{ fontSize: 13, wordBreak: 'break-all' }}>{address}</div>
                <button className="btn btn-sm btn-o mt-14" style={{ width: '100%' }} onClick={app.disconnect}>
                  {t('disconnect')}
                </button>
              </>
            ) : (
              <button className="btn btn-y mt-14" style={{ width: '100%' }} onClick={app.openConnect}>
                {t('connectWallet')}
              </button>
            )}
          </div>
          <div className="block bg-d" style={{ padding: 20 }}>
            <div className="eyebrow" style={{ color: 'var(--y)' }}>{t('source')}</div>
            <div className="mono mt-14" style={{ fontSize: 12, color: 'var(--bg)', lineHeight: 1.7 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ opacity: 0.7 }}>{t('prefs')}</span>
                <b style={{ color: 'var(--bg)' }}>{prefs?.source ?? '—'}</b>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ opacity: 0.7 }}>{t('notif')}</span>
                <b style={{ color: 'var(--bg)' }}>{notif?.source ?? '—'}</b>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ opacity: 0.7 }}>{t('updated')}</span>
                <b style={{ color: 'var(--bg)' }}>
                  {prefs?.updatedAt ? new Date(prefs.updatedAt).toLocaleString() : '—'}
                </b>
              </div>
            </div>
            <button
              className="btn btn-sm mt-14"
              style={{
                width: '100%',
                background: 'transparent',
                color: 'var(--bg)',
                borderColor: 'var(--bg)',
                boxShadow: '0 3px 0 0 #000',
              }}
              onClick={() => {
                qc.invalidateQueries({ queryKey: ['atlas-design-prefs'] });
                qc.invalidateQueries({ queryKey: ['atlas-design-notif'] });
              }}
            >
              {t('reload')}
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}

function SettingsToggleRow({
  label,
  hint,
  value,
  disabled,
  onChange,
}: {
  label: string;
  hint: string;
  value: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="row between" style={{ padding: '14px 0', borderBottom: '2px solid var(--bg-2)' }}>
      <div>
        <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 14 }}>{label}</div>
        <div style={{ color: 'var(--ink-2)', fontSize: 12, marginTop: 4 }}>{hint}</div>
      </div>
      <button
        onClick={() => !disabled && onChange(!value)}
        disabled={disabled}
        style={{
          width: 56,
          height: 30,
          borderRadius: 999,
          border: '3px solid var(--ink)',
          background: value ? 'var(--g)' : 'var(--bg-3)',
          padding: 0,
          position: 'relative',
          boxShadow: '0 3px 0 0 var(--ink)',
          opacity: disabled ? 0.5 : 1,
          cursor: disabled ? 'not-allowed' : 'pointer',
        }}
      >
        <span
          style={{
            position: 'absolute',
            top: 1,
            left: value ? 24 : 1,
            width: 22,
            height: 22,
            background: 'var(--ink)',
            borderRadius: 999,
            transition: 'left .2s',
          }}
        />
      </button>
    </div>
  );
}
