/**
 * The route SEO registry — ONE source of truth for indexability.
 *
 * `sitemap.ts`, `robots.ts` and every page's `generateMetadata` read this
 * table. That matters because the three of them disagreeing is the classic
 * SEO bug: a page carries `noindex` while the sitemap advertises it, or
 * robots.txt disallows a path that the sitemap still lists. Here a route can
 * only be described once, so the three surfaces cannot drift apart.
 *
 * Visibility is deliberately a two-value enum rather than a set of flags.
 * This product is a wallet console: a route either describes the product
 * (indexable) or reflects one person's holdings, approvals and settings
 * (never indexable). There is no useful third state, and a flag soup would
 * invite someone to tick "sitemap: yes" on a personal page.
 */

export type RouteVisibility = 'public' | 'private';

export type ChangeFrequency =
  | 'always'
  | 'hourly'
  | 'daily'
  | 'weekly'
  | 'monthly'
  | 'yearly'
  | 'never';

export interface SeoRoute {
  /** URL path. The home route is '/'. Dynamic routes carry their template. */
  path: string;
  /** Key under the `metadata.routes` i18n namespace (server-owned catalog). */
  key: string;
  /**
   * 'public'  — indexable, listed in sitemap.xml, canonical self-referencing.
   * 'private' — `noindex, nofollow`, absent from the sitemap, and disallowed
   *             in robots.txt. Used for anything scoped to one wallet.
   */
  visibility: RouteVisibility;
  /** Sitemap hint. Omitted for private routes, which never reach the sitemap. */
  changeFrequency?: ChangeFrequency;
  /** Sitemap priority, 0–1. Relative weight only; crawlers treat it loosely. */
  priority?: number;
  /**
   * True when the path is a Next.js dynamic segment template. Templates are
   * never emitted verbatim — the sitemap expands them from live data and
   * `robots` matches them by prefix.
   */
  dynamic?: boolean;
}

export const SEO_ROUTES: readonly SeoRoute[] = [
  // ── Product surfaces: these describe what Atlas X is and what it does. ──
  { path: '/', key: 'home', visibility: 'public', changeFrequency: 'daily', priority: 1.0 },
  { path: '/trade', key: 'trade', visibility: 'public', changeFrequency: 'hourly', priority: 0.9 },
  { path: '/markets', key: 'markets', visibility: 'public', changeFrequency: 'hourly', priority: 0.9 },
  { path: '/swap', key: 'swap', visibility: 'public', changeFrequency: 'daily', priority: 0.8 },
  { path: '/bridge', key: 'bridge', visibility: 'public', changeFrequency: 'daily', priority: 0.8 },
  { path: '/earn', key: 'earn', visibility: 'public', changeFrequency: 'daily', priority: 0.7 },
  { path: '/advanced-earn', key: 'advancedEarn', visibility: 'public', changeFrequency: 'daily', priority: 0.6 },
  { path: '/discover', key: 'discover', visibility: 'public', changeFrequency: 'daily', priority: 0.7 },
  { path: '/ranking', key: 'ranking', visibility: 'public', changeFrequency: 'hourly', priority: 0.8 },
  { path: '/campaign', key: 'campaign', visibility: 'public', changeFrequency: 'daily', priority: 0.6 },
  { path: '/nft', key: 'nft', visibility: 'public', changeFrequency: 'weekly', priority: 0.5 },
  { path: '/create-token', key: 'createToken', visibility: 'public', changeFrequency: 'weekly', priority: 0.6 },
  { path: '/nft-studio', key: 'nftStudio', visibility: 'public', changeFrequency: 'weekly', priority: 0.5 },

  // Token detail is the one dynamic route worth indexing: each address is a
  // distinct, long-lived, publicly meaningful page (a token's price, supply
  // and onchain activity). The sitemap expands it from the token index.
  { path: '/token/[address]', key: 'token', visibility: 'public', changeFrequency: 'hourly', priority: 0.7, dynamic: true },

  // ── Wallet-scoped surfaces. Never indexed. ──
  // These render one person's balances, approvals, transaction history or
  // preferences. Indexing them would publish a wallet's activity into search
  // results, so they are excluded at every layer rather than just one.
  { path: '/portfolio', key: 'portfolio', visibility: 'private' },
  { path: '/activity', key: 'activity', visibility: 'private' },
  { path: '/security', key: 'security', visibility: 'private' },
  { path: '/wallets', key: 'wallets', visibility: 'private' },
  { path: '/settings', key: 'settings', visibility: 'private' },
  { path: '/profile/[address]', key: 'profile', visibility: 'private', dynamic: true },
  { path: '/admin/i18n', key: 'adminI18n', visibility: 'private' },
] as const;

/** Lookup by registry key. Throws on an unknown key so a typo fails the build. */
export function seoRoute(key: string): SeoRoute {
  const found = SEO_ROUTES.find((r) => r.key === key);
  if (!found) {
    throw new Error(
      `seoRoute: unknown route key "${key}". Add it to SEO_ROUTES in lib/seo/routes.ts.`,
    );
  }
  return found;
}

/** Static public routes, ready to emit into the sitemap verbatim. */
export function publicStaticRoutes(): SeoRoute[] {
  return SEO_ROUTES.filter((r) => r.visibility === 'public' && !r.dynamic);
}

/**
 * robots.txt disallow patterns for every private route.
 *
 * A dynamic template becomes a prefix rule (`/profile/[address]` →
 * `/profile/`) because robots.txt has no notion of a path parameter; matching
 * the parent segment is the only correct translation. `/api/` is added by the
 * caller — it is not a page route and does not belong in this table.
 */
export function disallowedPaths(): string[] {
  return SEO_ROUTES.filter((r) => r.visibility === 'private').map((r) =>
    r.dynamic ? `${r.path.split('/[')[0]}/` : r.path,
  );
}
