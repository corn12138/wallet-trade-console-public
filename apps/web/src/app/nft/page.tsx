import type { Metadata } from 'next';
import { buildRouteMetadata } from '@/lib/seo/metadata';
import NftStudioPage from '../nft-studio/NftStudioPage';

// `/nft` and `/nft-studio` render the same real studio (the former
// `_atlas/pages/NftStudioPage` was a mock that faked mint success). They are
// separate URLs with separate metadata, so this imports the client component
// directly instead of re-exporting the sibling route's default — re-exporting
// a page module would have silently inherited that route's metadata too.
export async function generateMetadata(): Promise<Metadata> {
  return buildRouteMetadata('nft');
}

export default function Page() {
  return <NftStudioPage />;
}
