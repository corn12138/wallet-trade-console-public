import type { Metadata } from 'next';
import { buildRouteMetadata } from '@/lib/seo/metadata';
import { WalletsPage } from '../_atlas/pages/WalletsPage';

// Server component on purpose: `generateMetadata` only runs in one, and the
// interactive page below stays a client component. Splitting here costs one
// wrapper and buys a per-route <title>, description and canonical.
export async function generateMetadata(): Promise<Metadata> {
  return buildRouteMetadata('wallets');
}

export default function Page() {
  return <WalletsPage />;
}
