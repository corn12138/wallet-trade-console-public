import { getRequestConfig } from 'next-intl/server';
import { cookies, headers } from 'next/headers';
import { getCatalog, getEnabledLocales, resolveLocale } from './server/catalog';

/**
 * Server-side i18n request config (2026-07-10 cutover).
 *
 * Locale resolution: validated `locale` cookie → Accept-Language → the
 * enabled default from the Go locale metadata → 'en' bootstrap default.
 * Messages come from the Go published catalog (PostgreSQL revisions; the
 * embedded Go baseline only on a fresh/degraded database). There is no
 * dynamic import of a frontend JSON catalog — if the catalog service is
 * unreachable the request fails to the SSR error boundary instead of
 * silently rendering stale copy.
 */
export default getRequestConfig(async () => {
  const [cookieStore, headerStore] = await Promise.all([cookies(), headers()]);
  const enabled = await getEnabledLocales();
  const locale = resolveLocale(
    cookieStore.get('locale')?.value,
    headerStore.get('accept-language') ?? undefined,
    enabled,
  );
  const catalog = await getCatalog(locale);

  return {
    locale,
    // Fixed UTC default avoids next-intl's ENVIRONMENT_FALLBACK warning and
    // keeps server/client markup deterministic (all timestamps render in UTC).
    timeZone: 'UTC',
    messages: catalog.messages as never,
  };
});
