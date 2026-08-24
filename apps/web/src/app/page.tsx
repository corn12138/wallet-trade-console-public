import type { Metadata } from 'next';
import { buildRouteMetadata } from '@/lib/seo/metadata';
import PagesMarketing from './_atlas/PagesMarketing';

export async function generateMetadata(): Promise<Metadata> {
  return buildRouteMetadata('home');
}

export default function Page() {
  return <PagesMarketing />;
}
