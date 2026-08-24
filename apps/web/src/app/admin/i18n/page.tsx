import type { Metadata } from 'next';
import { buildRouteMetadata } from '@/lib/seo/metadata';
import AdminI18nPage from './AdminI18nPage';

// Server component on purpose: `generateMetadata` only runs in one, and the
// interactive page below stays a client component. Splitting here costs one
// wrapper and buys a per-route <title>, description and canonical.
export async function generateMetadata(): Promise<Metadata> {
  return buildRouteMetadata('adminI18n');
}

export default function Page() {
  return <AdminI18nPage />;
}
