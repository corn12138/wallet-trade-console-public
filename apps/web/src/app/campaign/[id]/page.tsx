import { notFound } from 'next/navigation';
import { CampaignPage } from '../../_atlas/pages/CampaignPage';

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  if (!/^[a-zA-Z0-9_-]{1,128}$/.test(id)) notFound();
  return <CampaignPage id={id} />;
}
