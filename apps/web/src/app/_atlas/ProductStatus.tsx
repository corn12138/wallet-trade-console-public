'use client';
import { useQuery } from '@tanstack/react-query';
import { useTranslations, useLocale } from 'next-intl';
import { getProductStatus, type AtlasProductStatus } from '@/lib/api/atlas';
import { formatStatusWarning } from './diagnostics';
import { Icon } from './Icon';

/**
 * GET /api/status/product — the Go-owned diagnostics surface. Cached and shared
 * across pages so one poll explains every sparse/disabled screen.
 */
export function useProductStatus() {
  return useQuery<AtlasProductStatus>({
    queryKey: ['atlas-product-status'],
    queryFn: getProductStatus,
    staleTime: 30_000,
    refetchInterval: 60_000,
    retry: false,
  });
}

/**
 * Explains WHY a screen is empty or an action disabled, using the real backend
 * diagnostics — never a fake "all green" pill. Copy is localized by stable
 * warning code (Chinese mode never shows raw English).
 *
 * `codes` filters to the diagnostics relevant to one screen, so a healthy page
 * stays clean. OMITTING it shows every active warning, which is what a failure
 * state wants: the filter exists to avoid nagging a working page, and a page
 * that just failed to load is not one. Either way this renders nothing when
 * the backend reports no warnings.
 */
export function ProductStatusHint({
  codes,
  className,
}: {
  codes?: string[];
  className?: string;
}) {
  const { data } = useProductStatus();
  const t = useTranslations('diagnostics');
  const locale = useLocale();

  const all = data?.warnings ?? [];
  const active = codes ? all.filter((w) => codes.includes(w.code)) : all;
  if (active.length === 0) return null;

  return (
    <div
      className={`block tight ${className ?? ''}`.trim()}
      data-testid="product-status-hint"
      style={{ background: 'var(--bg-2)', padding: 12, border: '2px solid var(--ink)', boxShadow: 'none' }}
    >
      <div className="col" style={{ gap: 8 }}>
        {active.map((w) => {
          const display = formatStatusWarning(t, locale, w.code, w.message);
          return (
            <div
              key={w.code}
              className="row gap-8"
              data-status-code={w.code}
              style={{ alignItems: 'flex-start', fontSize: 12, lineHeight: 1.5 }}
            >
              <Icon name={w.severity === 'warn' ? 'warn' : 'signal'} size={14} />
              <span>
                {display.text}
                {display.detail && (
                  <span style={{ opacity: 0.7 }}>
                    {' — '}
                    {t('apiDetailLabel')}: {display.detail}
                  </span>
                )}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
