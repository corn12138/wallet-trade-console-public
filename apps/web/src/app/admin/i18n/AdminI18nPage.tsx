'use client';

/**
 * /admin/i18n — the translation management console (SIWE-admin only).
 *
 * A dense operational surface over the Go i18n admin API: locale metadata,
 * side-by-side draft editing with optimistic-concurrency conflict handling,
 * ICU validation, atomic publish with revision/checksum confirmation,
 * immutable revision history with rollback, and the audit trail. Hidden from
 * product navigation; the browser calls the protected admin endpoints with
 * the existing SIWE bearer. Product pages do NOT render from this surface —
 * normal catalogs load during SSR.
 */

import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocale, useTranslations } from 'next-intl';
import { useAccount } from 'wagmi';
import { useAuth } from '@/lib/web3';
import { fetchApi } from '@/lib/api/auth-fetch';
import { useApp } from '@/app/_atlas/AppContext';
import { TabBar } from '@/app/_atlas/Common';

/* ── API client (thin, typed, status-aware) ─────────────────────────────── */

class AdminApiError extends Error {
  status: number;
  issues?: ValidationIssue[];
  constructor(status: number, message: string, issues?: ValidationIssue[]) {
    super(message);
    this.status = status;
    this.issues = issues;
  }
}

interface ValidationIssue {
  namespace?: string;
  key?: string;
  code: string;
  detail: string;
}

interface AdminLocale {
  code: string;
  englishName: string;
  nativeName: string;
  enabled: boolean;
  isDefault: boolean;
  updatedBy?: string;
  updatedAt?: string;
}

interface DraftRowT {
  namespace: string;
  key: string;
  value: string;
  version: number;
  updatedBy?: string;
  updatedAt: string;
  defaultValue?: string | null;
}

interface NamespaceStat {
  namespace: string;
  keys: number;
  missing: number;
}

interface RevisionT {
  id: string;
  locale: string;
  version: number;
  checksum: string;
  publishedBy: string;
  publishedAt: string;
  sourceRevisionId?: string;
}

interface AuditRowT {
  id: string;
  actor: string;
  action: string;
  locale?: string;
  namespace?: string;
  key?: string;
  createdAt: string;
}

async function adminFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetchApi(path, {
    ...init,
    headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    let issues: ValidationIssue[] | undefined;
    try {
      const body = (await res.json()) as { message?: string; issues?: ValidationIssue[] };
      message = body.message ?? message;
      issues = body.issues;
    } catch {
      /* non-JSON error body */
    }
    throw new AdminApiError(res.status, message, issues);
  }
  return (await res.json()) as T;
}

/* ── page ────────────────────────────────────────────────────────────────── */

type Tab = 'editor' | 'revisions' | 'audit' | 'locales';

export default function AdminI18nPage() {
  const t = useTranslations('adminI18n');
  const activeLocale = useLocale();
  const app = useApp();
  const { address, isConnected } = useAccount();
  // The admin API is SIWE-gated: the access probe must run against a real
  // signed session, and must RE-RUN once the session is established (the
  // token key changes on sign-in) rather than caching a pre-auth 401.
  const { isAuthenticated, token } = useAuth();
  const [tab, setTab] = useState<Tab>('editor');
  const [target, setTarget] = useState('zh');

  // Access probe: distinguishes 401 (no/expired SIWE) from 403 (not admin).
  const access = useQuery({
    queryKey: ['i18n-admin-access', address, token],
    queryFn: () => adminFetch<{ locales: AdminLocale[] }>('/i18n/admin/locales'),
    retry: false,
    enabled: isConnected && isAuthenticated,
  });
  const accessErr = access.error as AdminApiError | null;

  if (!isConnected) {
    return (
      <Gate title={t('gateConnectTitle')} body={t('gateConnectBody')}>
        <button className="btn btn-y" onClick={() => app.openConnect()} data-testid="admin-i18n-connect">
          {t('gateConnectCta')}
        </button>
      </Gate>
    );
  }
  // Connected but no signed session: the SIWE-gated admin API is unreachable.
  if (!isAuthenticated) {
    return (
      <Gate title={t('gate401Title')} body={t('gate401Body')} testid="admin-i18n-401">
        <button className="btn btn-y" onClick={() => app.openConnect()}>{t('gate401Cta')}</button>
      </Gate>
    );
  }
  if (access.isLoading || access.isPending) {
    return <Gate title={t('gateCheckingTitle')} body={t('gateCheckingBody')} testid="admin-i18n-loading" />;
  }
  if (accessErr?.status === 401) {
    return (
      <Gate title={t('gate401Title')} body={t('gate401Body')} testid="admin-i18n-401">
        <button className="btn btn-y" onClick={() => app.openConnect()}>{t('gate401Cta')}</button>
      </Gate>
    );
  }
  if (accessErr?.status === 403) {
    return <Gate title={t('gate403Title')} body={t('gate403Body')} testid="admin-i18n-403" />;
  }
  if (accessErr) {
    return <Gate title={t('gateErrorTitle')} body={`${t('gateErrorBody')} — ${accessErr.message}`} testid="admin-i18n-error" />;
  }

  const locales = access.data?.locales ?? [];
  const defaultLocale = locales.find((l) => l.isDefault)?.code ?? 'en';

  return (
    <div className="container" style={{ paddingTop: 18, paddingBottom: 40 }} data-testid="admin-i18n-console">
      <div className="row between" style={{ flexWrap: 'wrap', gap: 10, marginBottom: 12 }}>
        <div>
          <div className="eyebrow">{t('kicker')}</div>
          <h1 className="h-display" style={{ fontSize: 26, margin: '2px 0 4px' }}>{t('title')}</h1>
          <div className="mono" style={{ fontSize: 11, color: 'var(--ink-2)' }}>
            {t('signedInAs')} {address} · {t('uiLocale')} {activeLocale}
          </div>
        </div>
        <TabBar
          tabs={[
            { key: 'editor' as const, label: t('tabEditor') },
            { key: 'locales' as const, label: `${t('tabLocales')} · ${locales.length}` },
            { key: 'revisions' as const, label: t('tabRevisions') },
            { key: 'audit' as const, label: t('tabAudit') },
          ]}
          value={tab}
          onChange={setTab}
        />
      </div>

      {tab === 'editor' && (
        <Editor locales={locales} defaultLocale={defaultLocale} target={target} setTarget={setTarget} />
      )}
      {tab === 'locales' && <LocalesPanel locales={locales} />}
      {tab === 'revisions' && <RevisionsPanel locales={locales} target={target} setTarget={setTarget} />}
      {tab === 'audit' && <AuditPanel />}
    </div>
  );
}

function Gate({ title, body, children, testid }: { title: string; body: string; children?: React.ReactNode; testid?: string }) {
  return (
    <div className="container" style={{ paddingTop: 60, maxWidth: 560 }} data-testid={testid}>
      <div className="block" style={{ padding: 28, textAlign: 'center' }}>
        <h1 className="h-display" style={{ fontSize: 22, marginBottom: 8 }}>{title}</h1>
        <p style={{ color: 'var(--ink-2)', marginBottom: 16 }}>{body}</p>
        {children}
      </div>
    </div>
  );
}

/* ── editor tab ──────────────────────────────────────────────────────────── */

function Editor({ locales, defaultLocale, target, setTarget }: {
  locales: AdminLocale[]; defaultLocale: string; target: string; setTarget: (l: string) => void;
}) {
  const t = useTranslations('adminI18n');
  const qc = useQueryClient();
  const [namespace, setNamespace] = useState('');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const pageSize = 25;

  const nsQ = useQuery({
    queryKey: ['i18n-ns', target],
    queryFn: () => adminFetch<{ namespaces: NamespaceStat[] }>(`/i18n/admin/namespaces?locale=${target}`),
  });
  const msgsQ = useQuery({
    queryKey: ['i18n-msgs', target, namespace, search, page],
    queryFn: () => adminFetch<{ items: DraftRowT[]; total: number }>(
      `/i18n/admin/messages?locale=${target}&namespace=${encodeURIComponent(namespace)}&search=${encodeURIComponent(search)}&page=${page}&pageSize=${pageSize}`),
  });
  const validateM = useMutation({
    mutationFn: () => adminFetch<{ valid: boolean; issues: ValidationIssue[] }>(`/i18n/admin/validate/${target}`, { method: 'POST' }),
  });
  const publishM = useMutation({
    mutationFn: () => adminFetch<RevisionT>(`/i18n/admin/publish/${target}`, { method: 'POST' }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['i18n-revisions'] });
      qc.invalidateQueries({ queryKey: ['i18n-audit'] });
    },
  });
  const publishErr = publishM.error as AdminApiError | null;
  const missingTotal = (nsQ.data?.namespaces ?? []).reduce((s, n) => s + n.missing, 0);
  const total = msgsQ.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / pageSize));

  return (
    <div>
      {/* control strip */}
      <div className="block" style={{ padding: 12, marginBottom: 12 }}>
        <div className="row" style={{ gap: 10, flexWrap: 'wrap', alignItems: 'center' }}>
          <label className="eyebrow" htmlFor="i18n-target">{t('targetLocale')}</label>
          <select id="i18n-target" className="mono" value={target}
            onChange={(e) => { setTarget(e.target.value); setPage(1); setNamespace(''); }}
            data-testid="admin-i18n-target"
            style={{ padding: '6px 10px', border: '2px solid var(--ink)', borderRadius: 8, background: 'var(--paper)' }}>
            {locales.map((l) => (
              <option key={l.code} value={l.code}>{l.code} — {l.nativeName}{l.isDefault ? ` · ${t('defaultBadge')}` : ''}</option>
            ))}
          </select>
          <select className="mono" value={namespace} onChange={(e) => { setNamespace(e.target.value); setPage(1); }}
            aria-label={t('namespaceFilter')} data-testid="admin-i18n-namespace"
            style={{ padding: '6px 10px', border: '2px solid var(--ink)', borderRadius: 8, background: 'var(--paper)' }}>
            <option value="">{t('allNamespaces')}</option>
            {(nsQ.data?.namespaces ?? []).map((n) => (
              <option key={n.namespace} value={n.namespace}>
                {n.namespace} ({n.keys}{n.missing > 0 ? ` · ${t('missingCount', { count: n.missing })}` : ''})
              </option>
            ))}
          </select>
          <input className="mono" value={search} placeholder={t('searchPlaceholder')}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            aria-label={t('searchLabel')} data-testid="admin-i18n-search"
            style={{ padding: '6px 10px', border: '2px solid var(--ink)', borderRadius: 8, minWidth: 200 }} />
          <span className="pill flat" data-testid="admin-i18n-missing-total">
            {missingTotal > 0 ? t('missingTotal', { count: missingTotal }) : t('noMissing')}
          </span>
          <span style={{ flex: 1 }} />
          <button className="btn btn-xs" onClick={() => validateM.mutate()} disabled={validateM.isPending} data-testid="admin-i18n-validate">
            {validateM.isPending ? t('validating') : t('validateCta')}
          </button>
          <PublishButton
            target={target} disabled={publishM.isPending}
            onConfirm={() => publishM.mutate()} />
        </div>

        {/* validation + publish feedback */}
        {validateM.data && (
          <div style={{ marginTop: 10 }} data-testid="admin-i18n-validate-result">
            {validateM.data.valid ? (
              <span className="pill live">{t('validationClean')}</span>
            ) : (
              <IssueList issues={validateM.data.issues} title={t('validationIssues', { count: validateM.data.issues.length })} />
            )}
          </div>
        )}
        {publishM.data && (
          <div className="mono" style={{ marginTop: 10, fontSize: 12 }} data-testid="admin-i18n-publish-result">
            {t('publishedAs', { version: publishM.data.version })} · {t('checksum')} {publishM.data.checksum.slice(0, 16)}…
          </div>
        )}
        {publishErr && (
          <div style={{ marginTop: 10 }} data-testid="admin-i18n-publish-error">
            {publishErr.issues
              ? <IssueList issues={publishErr.issues} title={t('publishBlocked', { count: publishErr.issues.length })} />
              : <span className="pill" style={{ background: 'var(--neg)', color: '#fff' }}>{publishErr.message}</span>}
          </div>
        )}
      </div>

      {/* message table */}
      {msgsQ.isLoading && <div className="block" style={{ padding: 20 }}><div className="skel lg" /></div>}
      {msgsQ.isError && (
        <div className="block" style={{ padding: 20 }} data-testid="admin-i18n-msgs-error">
          {t('messagesError')} — {(msgsQ.error as Error).message}
        </div>
      )}
      {msgsQ.data && msgsQ.data.items.length === 0 && (
        <div className="block" style={{ padding: 20 }} data-testid="admin-i18n-msgs-empty">{t('messagesEmpty')}</div>
      )}
      {msgsQ.data && msgsQ.data.items.length > 0 && (
        <div className="block" style={{ padding: 0, overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }} data-testid="admin-i18n-msgs-table">
            <thead>
              <tr style={{ borderBottom: '2px solid var(--ink)', textAlign: 'left' }}>
                <th style={{ padding: '8px 10px' }} className="eyebrow">{t('colKey')}</th>
                <th style={{ padding: '8px 10px' }} className="eyebrow">{t('colDefault', { locale: defaultLocale })}</th>
                <th style={{ padding: '8px 10px' }} className="eyebrow">{t('colTarget', { locale: target })}</th>
                <th style={{ padding: '8px 10px' }} />
              </tr>
            </thead>
            <tbody>
              {msgsQ.data.items.map((row) => (
                <DraftEditorRow key={`${row.namespace}.${row.key}`} row={row} target={target} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* pagination */}
      <div className="row" style={{ gap: 8, marginTop: 10, alignItems: 'center' }}>
        <button className="btn btn-xs" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>{t('prevPage')}</button>
        <span className="mono" style={{ fontSize: 12 }}>{t('pageOf', { page, pages, total })}</span>
        <button className="btn btn-xs" disabled={page >= pages} onClick={() => setPage((p) => p + 1)}>{t('nextPage')}</button>
      </div>
    </div>
  );
}

function PublishButton({ target, disabled, onConfirm }: { target: string; disabled: boolean; onConfirm: () => void }) {
  const t = useTranslations('adminI18n');
  const [arming, setArming] = useState(false);
  if (!arming) {
    return (
      <button className="btn btn-xs btn-y" onClick={() => setArming(true)} disabled={disabled} data-testid="admin-i18n-publish">
        {t('publishCta', { locale: target })}
      </button>
    );
  }
  return (
    <span className="row" style={{ gap: 6 }}>
      <span className="mono" style={{ fontSize: 11 }}>{t('publishConfirm', { locale: target })}</span>
      <button className="btn btn-xs btn-y" onClick={() => { setArming(false); onConfirm(); }} data-testid="admin-i18n-publish-confirm">
        {t('confirmYes')}
      </button>
      <button className="btn btn-xs" onClick={() => setArming(false)}>{t('confirmNo')}</button>
    </span>
  );
}

function IssueList({ issues, title }: { issues: ValidationIssue[]; title: string }) {
  return (
    <div>
      <div className="pill" style={{ background: 'var(--neg)', color: '#fff', marginBottom: 6 }}>{title}</div>
      <ul className="mono" style={{ fontSize: 11, paddingLeft: 18, maxHeight: 160, overflowY: 'auto' }}>
        {issues.slice(0, 50).map((i, idx) => (
          <li key={idx}>
            {i.namespace ? `${i.namespace}.${i.key ?? ''}: ` : ''}[{i.code}] {i.detail}
          </li>
        ))}
      </ul>
    </div>
  );
}

function DraftEditorRow({ row, target }: { row: DraftRowT; target: string }) {
  const t = useTranslations('adminI18n');
  const qc = useQueryClient();
  const [value, setValue] = useState(row.value);
  const [version, setVersion] = useState(row.version);
  const dirty = value !== row.value || version !== row.version;

  const saveM = useMutation({
    mutationFn: () => adminFetch<DraftRowT>(
      `/i18n/admin/messages/${target}/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.key)}`,
      { method: 'PATCH', body: JSON.stringify({ value, expectedVersion: version }) }),
    onSuccess: (saved) => {
      setVersion(saved.version);
      qc.invalidateQueries({ queryKey: ['i18n-audit'] });
    },
  });
  const err = saveM.error as AdminApiError | null;

  return (
    <tr style={{ borderBottom: '1px solid var(--ink-3, #ddd)', verticalAlign: 'top' }}>
      <td className="mono" style={{ padding: '8px 10px', fontSize: 11, whiteSpace: 'nowrap' }}>
        {row.namespace}.{row.key}
        <div style={{ color: 'var(--ink-2)' }}>v{version}</div>
      </td>
      <td style={{ padding: '8px 10px', maxWidth: 320, color: 'var(--ink-2)' }}>{row.defaultValue ?? '—'}</td>
      <td style={{ padding: '8px 10px', minWidth: 260 }}>
        <textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          rows={Math.min(4, Math.max(1, Math.ceil(value.length / 60)))}
          aria-label={`${row.namespace}.${row.key}`}
          style={{ width: '100%', border: '2px solid var(--ink)', borderRadius: 6, padding: 6, fontSize: 12 }}
        />
        {err?.status === 409 && (
          <div className="mono" style={{ fontSize: 11, color: 'var(--neg)' }} data-testid="admin-i18n-conflict">
            {t('conflictBody')}{' '}
            <button className="btn btn-xs" onClick={() => qc.invalidateQueries({ queryKey: ['i18n-msgs'] })}>{t('conflictReload')}</button>
          </div>
        )}
        {err && err.status === 422 && err.issues && (
          <div className="mono" style={{ fontSize: 11, color: 'var(--neg)' }}>{err.issues[0]?.detail}</div>
        )}
        {err && err.status !== 409 && err.status !== 422 && (
          <div className="mono" style={{ fontSize: 11, color: 'var(--neg)' }}>{err.message}</div>
        )}
      </td>
      <td style={{ padding: '8px 10px', whiteSpace: 'nowrap' }}>
        <button className="btn btn-xs btn-y" disabled={!dirty || saveM.isPending}
          onClick={() => saveM.mutate()} data-testid={`admin-i18n-save-${row.namespace}-${row.key}`}>
          {saveM.isPending ? t('saving') : t('saveCta')}
        </button>
        {saveM.isSuccess && !dirty && <span className="mono" style={{ fontSize: 10, marginLeft: 6 }}>{t('saved')}</span>}
      </td>
    </tr>
  );
}

/* ── locales tab ─────────────────────────────────────────────────────────── */

function LocalesPanel({ locales }: { locales: AdminLocale[] }) {
  const t = useTranslations('adminI18n');
  const qc = useQueryClient();
  const patchM = useMutation({
    mutationFn: (input: { code: string; body: Record<string, unknown> }) =>
      adminFetch<AdminLocale>(`/i18n/admin/locales/${input.code}`, { method: 'PATCH', body: JSON.stringify(input.body) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['i18n-admin-access'] }),
  });
  const patchErr = patchM.error as AdminApiError | null;

  return (
    <div className="block" style={{ padding: 0, overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }} data-testid="admin-i18n-locales-table">
        <thead>
          <tr style={{ borderBottom: '2px solid var(--ink)', textAlign: 'left' }}>
            <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colCode')}</th>
            <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colName')}</th>
            <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colState')}</th>
            <th className="eyebrow" style={{ padding: '8px 10px' }} />
          </tr>
        </thead>
        <tbody>
          {locales.map((l) => (
            <tr key={l.code} style={{ borderBottom: '1px solid var(--ink-3, #ddd)' }}>
              <td className="mono" style={{ padding: '8px 10px' }}>{l.code}</td>
              <td style={{ padding: '8px 10px' }}>{l.nativeName} <span style={{ color: 'var(--ink-2)' }}>({l.englishName})</span></td>
              <td style={{ padding: '8px 10px' }}>
                {l.isDefault && <span className="pill live" style={{ marginRight: 6 }}>{t('defaultBadge')}</span>}
                <span className={'pill ' + (l.enabled ? 'live' : 'flat')}>{l.enabled ? t('enabledBadge') : t('disabledBadge')}</span>
              </td>
              <td style={{ padding: '8px 10px', whiteSpace: 'nowrap' }}>
                {!l.isDefault && (
                  <>
                    <button className="btn btn-xs" disabled={patchM.isPending}
                      onClick={() => patchM.mutate({ code: l.code, body: { enabled: !l.enabled } })}>
                      {l.enabled ? t('disableCta') : t('enableCta')}
                    </button>{' '}
                    <button className="btn btn-xs" disabled={patchM.isPending || !l.enabled}
                      onClick={() => patchM.mutate({ code: l.code, body: { isDefault: true } })}>
                      {t('makeDefaultCta')}
                    </button>
                  </>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {patchErr && (
        <div style={{ padding: 10 }} data-testid="admin-i18n-locale-error">
          {patchErr.issues ? <IssueList issues={patchErr.issues} title={t('localeChangeBlocked')} /> :
            <span className="pill" style={{ background: 'var(--neg)', color: '#fff' }}>{patchErr.message}</span>}
        </div>
      )}
    </div>
  );
}

/* ── revisions tab ───────────────────────────────────────────────────────── */

function RevisionsPanel({ locales, target, setTarget }: { locales: AdminLocale[]; target: string; setTarget: (l: string) => void }) {
  const t = useTranslations('adminI18n');
  const qc = useQueryClient();
  const [page, setPage] = useState(1);
  const revsQ = useQuery({
    queryKey: ['i18n-revisions', target, page],
    queryFn: () => adminFetch<{ items: RevisionT[]; total: number }>(`/i18n/admin/revisions?locale=${target}&page=${page}&pageSize=20`),
  });
  const rollbackM = useMutation({
    mutationFn: (revisionId: string) => adminFetch<RevisionT>(`/i18n/admin/rollback/${target}/${revisionId}`, { method: 'POST' }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['i18n-revisions'] });
      qc.invalidateQueries({ queryKey: ['i18n-msgs'] });
      qc.invalidateQueries({ queryKey: ['i18n-audit'] });
    },
  });
  const [confirming, setConfirming] = useState<string | null>(null);

  return (
    <div>
      <div className="row" style={{ gap: 10, marginBottom: 10 }}>
        <select className="mono" value={target} onChange={(e) => { setTarget(e.target.value); setPage(1); }}
          aria-label={t('targetLocale')}
          style={{ padding: '6px 10px', border: '2px solid var(--ink)', borderRadius: 8, background: 'var(--paper)' }}>
          {locales.map((l) => <option key={l.code} value={l.code}>{l.code}</option>)}
        </select>
        {rollbackM.isSuccess && (
          <span className="pill live" data-testid="admin-i18n-rollback-result">
            {t('rolledBackTo', { version: rollbackM.data.version })}
          </span>
        )}
        {rollbackM.isError && (
          <span className="pill" style={{ background: 'var(--neg)', color: '#fff' }}>
            {(rollbackM.error as AdminApiError).message}
          </span>
        )}
      </div>
      {revsQ.isLoading && <div className="block" style={{ padding: 20 }}><div className="skel lg" /></div>}
      {revsQ.isError && <div className="block" style={{ padding: 20 }}>{t('revisionsError')}</div>}
      {revsQ.data && revsQ.data.items.length === 0 && (
        <div className="block" style={{ padding: 20 }} data-testid="admin-i18n-revisions-empty">{t('revisionsEmpty')}</div>
      )}
      {revsQ.data && revsQ.data.items.length > 0 && (
        <div className="block" style={{ padding: 0, overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }} data-testid="admin-i18n-revisions-table">
            <thead>
              <tr style={{ borderBottom: '2px solid var(--ink)', textAlign: 'left' }}>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colVersion')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colChecksum')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colPublishedBy')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colPublishedAt')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }} />
              </tr>
            </thead>
            <tbody>
              {revsQ.data.items.map((r) => (
                <tr key={r.id} style={{ borderBottom: '1px solid var(--ink-3, #ddd)' }}>
                  <td className="mono" style={{ padding: '8px 10px' }}>
                    v{r.version}{r.sourceRevisionId ? ` (${t('rollbackOf')})` : ''}
                  </td>
                  <td className="mono" style={{ padding: '8px 10px', fontSize: 11 }}>{r.checksum.slice(0, 16)}…</td>
                  <td className="mono" style={{ padding: '8px 10px', fontSize: 11 }}>{r.publishedBy.slice(0, 10)}…</td>
                  <td className="mono" style={{ padding: '8px 10px', fontSize: 11 }}>{new Date(r.publishedAt).toISOString()}</td>
                  <td style={{ padding: '8px 10px', whiteSpace: 'nowrap' }}>
                    {confirming === r.id ? (
                      <>
                        <span className="mono" style={{ fontSize: 11 }}>{t('rollbackConfirm', { version: r.version })} </span>
                        <button className="btn btn-xs btn-y" onClick={() => { setConfirming(null); rollbackM.mutate(r.id); }}
                          data-testid={`admin-i18n-rollback-confirm-${r.version}`}>
                          {t('confirmYes')}
                        </button>{' '}
                        <button className="btn btn-xs" onClick={() => setConfirming(null)}>{t('confirmNo')}</button>
                      </>
                    ) : (
                      <button className="btn btn-xs" onClick={() => setConfirming(r.id)} disabled={rollbackM.isPending}
                        data-testid={`admin-i18n-rollback-${r.version}`}>
                        {t('rollbackCta')}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="row" style={{ gap: 8, marginTop: 10 }}>
        <button className="btn btn-xs" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>{t('prevPage')}</button>
        <button className="btn btn-xs" disabled={(revsQ.data?.total ?? 0) <= page * 20} onClick={() => setPage((p) => p + 1)}>{t('nextPage')}</button>
      </div>
    </div>
  );
}

/* ── audit tab ───────────────────────────────────────────────────────────── */

function AuditPanel() {
  const t = useTranslations('adminI18n');
  const [page, setPage] = useState(1);
  const [action, setAction] = useState('');
  const auditQ = useQuery({
    queryKey: ['i18n-audit', page, action],
    queryFn: () => adminFetch<{ items: AuditRowT[]; total: number }>(
      `/i18n/admin/audit?action=${encodeURIComponent(action)}&page=${page}&pageSize=50`),
  });

  const actions = useMemo(() => ['', 'draft.update', 'publish', 'rollback', 'locale.enable', 'locale.disable', 'locale.update', 'bootstrap.publish'], []);

  return (
    <div>
      <div className="row" style={{ gap: 10, marginBottom: 10 }}>
        <select className="mono" value={action} onChange={(e) => { setAction(e.target.value); setPage(1); }}
          aria-label={t('auditActionFilter')}
          style={{ padding: '6px 10px', border: '2px solid var(--ink)', borderRadius: 8, background: 'var(--paper)' }}>
          {actions.map((a) => <option key={a} value={a}>{a === '' ? t('allActions') : a}</option>)}
        </select>
      </div>
      {auditQ.isLoading && <div className="block" style={{ padding: 20 }}><div className="skel lg" /></div>}
      {auditQ.isError && <div className="block" style={{ padding: 20 }}>{t('auditError')}</div>}
      {auditQ.data && auditQ.data.items.length === 0 && (
        <div className="block" style={{ padding: 20 }} data-testid="admin-i18n-audit-empty">{t('auditEmpty')}</div>
      )}
      {auditQ.data && auditQ.data.items.length > 0 && (
        <div className="block" style={{ padding: 0, overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }} data-testid="admin-i18n-audit-table">
            <thead>
              <tr style={{ borderBottom: '2px solid var(--ink)', textAlign: 'left' }}>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colTime')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colActor')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colAction')}</th>
                <th className="eyebrow" style={{ padding: '8px 10px' }}>{t('colSubject')}</th>
              </tr>
            </thead>
            <tbody>
              {auditQ.data.items.map((a) => (
                <tr key={a.id} style={{ borderBottom: '1px solid var(--ink-3, #ddd)' }}>
                  <td className="mono" style={{ padding: '6px 10px', whiteSpace: 'nowrap' }}>{new Date(a.createdAt).toISOString()}</td>
                  <td className="mono" style={{ padding: '6px 10px' }}>{a.actor.slice(0, 10)}…</td>
                  <td style={{ padding: '6px 10px' }}><span className="pill flat">{a.action}</span></td>
                  <td className="mono" style={{ padding: '6px 10px' }}>
                    {[a.locale, a.namespace && a.key ? `${a.namespace}.${a.key}` : a.namespace].filter(Boolean).join(' · ') || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="row" style={{ gap: 8, marginTop: 10 }}>
        <button className="btn btn-xs" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>{t('prevPage')}</button>
        <button className="btn btn-xs" disabled={(auditQ.data?.total ?? 0) <= page * 50} onClick={() => setPage((p) => p + 1)}>{t('nextPage')}</button>
      </div>
    </div>
  );
}
