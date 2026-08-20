/**
 * Canonical origin resolution.
 *
 * Its own module so `robots.ts` and `sitemap.ts` can reach it without pulling
 * in next-intl, which `metadata.ts` needs but they do not.
 */

/** Canonical origin, no trailing slash. */
export function siteUrl(): string {
  const raw = process.env.NEXT_PUBLIC_SITE_URL || 'https://atlas-x.app';
  return raw.replace(/\/$/, '');
}

/** Absolute URL for a site-relative path. */
export function absoluteUrl(path: string): string {
  return path === '/' ? `${siteUrl()}/` : `${siteUrl()}${path}`;
}
