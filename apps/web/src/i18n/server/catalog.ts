import 'server-only';
import { cache } from 'react';
import { parseAcceptLanguage, resolveLocale as resolveLocalePure, validateMessagesShape } from '../resolve';

export { parseAcceptLanguage };

/**
 * Server-only Go catalog client. The Go API + PostgreSQL own translations;
 * Next only ever reads the published catalog here, during server rendering.
 *
 * - The base URL comes from the server-only INTERNAL_API_BASE_URL (never a
 *   NEXT_PUBLIC_* variable — the browser must not know or use a catalog URL).
 * - React cache() gives request-scoped deduplication; the fetch layer adds a
 *   short shared revalidation window keyed by the endpoint.
 * - Responses are shape-validated before next-intl ever sees them; failures
 *   surface as typed errors for the SSR error boundary. There is NO fallback
 *   import of a frontend JSON catalog.
 */

const INTERNAL_API_BASE = (
  process.env.INTERNAL_API_BASE_URL ||
  process.env.NEXT_PUBLIC_API_URL || // deployment convenience: same origin as the public API
  'http://127.0.0.1:8090/api'
).replace(/\/$/, '');

export class CatalogUnavailableError extends Error {
  constructor(detail: string) {
    super(`i18n catalog unavailable: ${detail}`);
    this.name = 'CatalogUnavailableError';
  }
}

export interface PublicLocaleMeta {
  code: string;
  englishName: string;
  nativeName: string;
  isDefault: boolean;
}

export interface ServerCatalog {
  locale: string;
  version: number;
  checksum: string;
  source: 'database' | 'embedded-baseline';
  messages: Record<string, unknown>;
}


/** Enabled locales + default, as published by the Go service. */
export const getEnabledLocales = cache(async (): Promise<PublicLocaleMeta[]> => {
  let res: Response;
  try {
    res = await fetch(`${INTERNAL_API_BASE}/i18n/locales`, {
      next: { revalidate: 60 },
    });
  } catch (e) {
    throw new CatalogUnavailableError(`locales fetch failed (${(e as Error).message})`);
  }
  if (!res.ok) {
    throw new CatalogUnavailableError(`locales endpoint returned ${res.status}`);
  }
  const body = (await res.json()) as { locales?: PublicLocaleMeta[] };
  const locales = Array.isArray(body.locales) ? body.locales : [];
  if (locales.length === 0 || !locales.some((l) => l.isDefault)) {
    throw new CatalogUnavailableError('locale metadata is empty or has no default');
  }
  return locales;
});

/** The published catalog for one enabled locale (validated shape). */
export const getCatalog = cache(async (locale: string): Promise<ServerCatalog> => {
  let res: Response;
  try {
    res = await fetch(`${INTERNAL_API_BASE}/i18n/catalog/${encodeURIComponent(locale)}`, {
      next: { revalidate: 60 },
    });
  } catch (e) {
    throw new CatalogUnavailableError(`catalog fetch failed (${(e as Error).message})`);
  }
  if (res.status === 404) {
    throw new CatalogUnavailableError(`locale ${locale} is not enabled`);
  }
  if (!res.ok) {
    throw new CatalogUnavailableError(`catalog endpoint returned ${res.status}`);
  }
  const body = (await res.json()) as Partial<ServerCatalog>;
  if (typeof body.locale !== 'string' || typeof body.checksum !== 'string') {
    throw new CatalogUnavailableError('catalog response missing locale/checksum');
  }
  const messages = validateMessagesShape(body.messages);
  return {
    locale: body.locale,
    version: typeof body.version === 'number' ? body.version : 0,
    checksum: body.checksum,
    source: body.source === 'database' ? 'database' : 'embedded-baseline',
    messages,
  };
});



/**
 * Resolve the request locale against the enabled Go locale metadata
 * (cookie → Accept-Language → default → 'en'). Pure logic in ../resolve.
 */
export function resolveLocale(
  cookieLocale: string | undefined,
  acceptLanguage: string | undefined,
  enabled: PublicLocaleMeta[],
): string {
  return resolveLocalePure(cookieLocale, acceptLanguage, enabled);
}
