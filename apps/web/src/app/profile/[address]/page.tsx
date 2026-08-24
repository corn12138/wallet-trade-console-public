import type { Metadata } from 'next';
import { buildRouteMetadata } from '@/lib/seo/metadata';
import ProfilePage from './ProfilePage';

/**
 * Wallet profiles are registered `private`, so this emits `noindex, nofollow`
 * plus `noarchive`/`nosnippet` and never reaches the sitemap. The page is
 * publicly reachable by URL — that is the point of a shareable profile — but
 * a wallet's holdings and trade history should not become a search result for
 * its owner's name, and should not be retained in a crawler's cache.
 *
 * The title deliberately stays generic rather than embedding the address:
 * the address is already in the URL, and there is nothing to gain from
 * copying it into the places page titles travel.
 */
export async function generateMetadata(): Promise<Metadata> {
  return buildRouteMetadata('profile');
}

export default function Page() {
  return <ProfilePage />;
}
