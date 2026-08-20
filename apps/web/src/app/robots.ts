import type { MetadataRoute } from 'next';
import { disallowedPaths } from '@/lib/seo/routes';
import { siteUrl } from '@/lib/seo/site';

/**
 * robots.txt, generated from the route registry.
 *
 * The disallow list is derived rather than typed out, so a new wallet-scoped
 * route becomes uncrawlable by being registered as `private` — nobody has to
 * remember to edit this file too. `/api/` is added here because it is not a
 * page route and has no registry entry.
 *
 * Note what robots.txt does and does not do: it asks crawlers not to FETCH a
 * path, which is not the same as asking them not to INDEX it. A URL that is
 * merely disallowed can still appear in results if something links to it.
 * That is why every private route ALSO emits `noindex` in its metadata
 * (see lib/seo/metadata.ts) — the two mechanisms cover different halves, and
 * a wallet page needs both.
 */
export default function robots(): MetadataRoute.Robots {
  return {
    rules: [
      {
        userAgent: '*',
        allow: '/',
        disallow: ['/api/', ...disallowedPaths()],
      },
    ],
    sitemap: `${siteUrl()}/sitemap.xml`,
    host: siteUrl(),
  };
}
