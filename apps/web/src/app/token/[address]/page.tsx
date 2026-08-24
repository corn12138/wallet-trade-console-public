import type { Metadata } from 'next';
import { getTranslations } from 'next-intl/server';
import { buildRouteMetadata } from '@/lib/seo/metadata';
import { getTokenForMetadata } from '@/lib/seo/tokens.server';
import TokenDetailPage from './TokenDetailPage';

/*
 * No `export const revalidate` here, deliberately.
 *
 * Locale resolution reads a cookie, which makes every page route in this app
 * dynamic (`ƒ` in the build output) — a segment-level revalidate would be
 * inert and would read like caching that isn't happening. The caching that
 * DOES happen is one layer down: `getTokenForMetadata` tags its fetch with an
 * hourly revalidation window, so the token lookup behind these tags is served
 * from the data cache rather than hitting the API on every crawl.
 *
 * `/sitemap.xml` is the one segment that genuinely revalidates, because it
 * takes no cookie and therefore stays static.
 */

export async function generateMetadata({
  params,
}: {
  params: Promise<{ address: string }>;
}): Promise<Metadata> {
  const { address } = await params;
  const path = `/token/${address}`;
  const token = await getTokenForMetadata(address);

  // Unresolvable token → generic catalog copy, still canonical and indexable.
  if (!token) return buildRouteMetadata('token', { path });

  const t = await getTranslations('metadata.routes');
  return buildRouteMetadata('token', {
    path,
    title: t('token.titleNamed', { name: token.name, symbol: token.symbol }),
    description: t('token.descriptionNamed', { name: token.name, symbol: token.symbol }),
  });
}

export default function Page() {
  return <TokenDetailPage />;
}
