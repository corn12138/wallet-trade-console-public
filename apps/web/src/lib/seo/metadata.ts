import type { Metadata } from 'next';
import { getTranslations } from 'next-intl/server';
import { seoRoute, type SeoRoute } from './routes';

/**
 * Per-route metadata, derived from the registry and the server-owned catalog.
 *
 * Copy lives in the Go catalog under `metadata.routes.<key>`, the same place
 * every other user-visible string lives. Titles and descriptions are copy —
 * putting them in a TS literal here would fork the translation pipeline and
 * ship an English-only <title> to a reader who picked 中文.
 *
 * The page title is the bare page name: the root layout owns the
 * `%s · Atlas X` template, so appending the brand here would double it.
 */

// Origin resolution lives in ./site so robots.ts and sitemap.ts can use it
// without importing next-intl. Re-exported here for existing callers.
export { siteUrl, absoluteUrl } from './site';

export interface RouteMetadataOverrides {
  /** Replaces the catalog title (dynamic routes that name their subject). */
  title?: string;
  /** Replaces the catalog description. */
  description?: string;
  /** Concrete path for a dynamic route, e.g. '/token/0xabc…'. */
  path?: string;
}

/**
 * `noindex, nofollow` plus the archive opt-outs.
 *
 * `nocache`/`noarchive` matter here beyond plain `noindex`: a wallet page that
 * is merely de-listed can still sit in a search engine's cache or in the
 * Wayback Machine. For a page rendering someone's balances, "not in results"
 * is not the same as "not retained", so both are refused.
 */
const PRIVATE_ROBOTS: Metadata['robots'] = {
  index: false,
  follow: false,
  nocache: true,
  googleBot: { index: false, follow: false, noarchive: true, nosnippet: true },
};

export async function buildRouteMetadata(
  key: string,
  overrides: RouteMetadataOverrides = {},
): Promise<Metadata> {
  const route: SeoRoute = seoRoute(key);
  const t = await getTranslations('metadata.routes');

  const title = overrides.title ?? t(`${route.key}.title`);
  const description = overrides.description ?? t(`${route.key}.description`);
  const path = overrides.path ?? route.path;

  if (route.visibility === 'private') {
    // No canonical and no OpenGraph on purpose. A canonical URL invites a
    // crawler to treat the page as a citable entity, and OG tags exist to
    // make a link render richly when shared — both are the opposite of what
    // a wallet-scoped page wants.
    return { title, description, robots: PRIVATE_ROBOTS };
  }

  return {
    title,
    description,
    alternates: { canonical: path },
    robots: { index: true, follow: true },
    openGraph: {
      type: 'website',
      siteName: 'Atlas X',
      title,
      description,
      url: path,
    },
    twitter: { card: 'summary_large_image', title, description },
  };
}
