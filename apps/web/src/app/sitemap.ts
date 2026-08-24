import type { MetadataRoute } from 'next';
import { publicStaticRoutes, seoRoute } from '@/lib/seo/routes';
import { absoluteUrl } from '@/lib/seo/site';
import { getSitemapTokens } from '@/lib/seo/tokens.server';

/**
 * sitemap.xml — static product routes plus every indexable token page.
 *
 * This segment is the app's one genuine ISR surface. The token index changes
 * on the order of hours, a crawler re-fetches a sitemap on its own schedule,
 * and regenerating this on every request would put an uncached database query
 * behind a public URL that bots hit freely. An hourly revalidate matches the
 * data's real rate of change and bounds that cost.
 */
export const revalidate = 3600;

export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  const now = new Date();

  const staticEntries: MetadataRoute.Sitemap = publicStaticRoutes().map((route) => ({
    url: absoluteUrl(route.path),
    lastModified: now,
    changeFrequency: route.changeFrequency,
    priority: route.priority,
  }));

  // Token pages come from live data and may legitimately be absent — an
  // unreachable index degrades to zero entries rather than failing the route.
  const tokenRoute = seoRoute('token');
  const tokenEntries: MetadataRoute.Sitemap = (await getSitemapTokens()).map((token) => ({
    url: absoluteUrl(`/token/${token.address}`),
    lastModified: token.lastModified ?? now,
    changeFrequency: tokenRoute.changeFrequency,
    priority: tokenRoute.priority,
  }));

  return [...staticEntries, ...tokenEntries];
}
