'use client';

import { useLocale, useTranslations } from 'next-intl';
import { useCampaignMe } from '../hooks/use-campaign-personal';
import { useCampaignDetail, useCampaignList } from '../hooks/use-campaign-public';
import type { QueryPublicCampaignView } from '../model/contract.generated';
import type { CampaignAPI, CampaignSession } from '../model/ports';
import { CampaignCard } from './campaign-card';
import { CampaignPaging, CampaignReadStatus } from './campaign-read-status';

export type CampaignViewProps = {
  api: CampaignAPI; session: CampaignSession | null; onConnect(): void; onUnauthorized?(): void;
};

function CampaignGroup(props: CampaignViewProps & {
  items: QueryPublicCampaignView[]; publicReady: boolean; freshIds: string[]; detailed?: boolean; refresh(): void;
}) {
  const t = useTranslations('campaignV2');
  const me = useCampaignMe({ api: props.api, session: props.session, ids: props.items.map((item) => item.id) });
  const personal = new Map(me.items.map((item) => [item.id, item]));
  const refresh = () => { props.refresh(); me.refresh(); };
  return <>
    {props.session && me.error?.status === 401 ? <button type="button" className="btn btn-sm" onClick={props.onUnauthorized ?? props.onConnect}>{t('signIn')}</button> : null}
    {props.items.map((item) => <CampaignCard key={item.id} {...props} campaignId={item.id} item={item} personal={personal.get(item.id) ?? null}
      publicReady={props.publicReady && props.freshIds.includes(item.id)} personalReady={me.writesReady && me.freshIds.includes(item.id)} onRefresh={refresh} />)}
  </>;
}

export function CampaignListView(props: CampaignViewProps) {
  const locale = useLocale();
  const list = useCampaignList({ api: props.api, locale });
  const groups: QueryPublicCampaignView[][] = [];
  // Me is bounded to 50 IDs even after the public list has multiple pages.
  for (let index = 0; index < list.items.length; index += 50) groups.push(list.items.slice(index, index + 50));
  return <div className="col gap-24">
    <CampaignReadStatus {...list} empty={list.items.length === 0} />
    <div className="grid-2">
      {groups.map((items, index) => <CampaignGroup key={index} {...props} items={items} publicReady={list.writesReady && !list.hasUpdates} freshIds={list.freshIds} refresh={list.refresh} />)}
    </div>
    <CampaignPaging {...list} />
  </div>;
}

export function CampaignDetailView(props: CampaignViewProps & { id: string }) {
  const locale = useLocale();
  const detail = useCampaignDetail({ api: props.api, locale, id: props.id });
  // A public 404 does not prove the wallet has nothing here: an unpublished
  // import keeps a recorded state, and this route stays its cancellation entry.
  const unpublished = detail.status === 'error' && detail.error?.status === 404 && !!props.session;
  return <div className="col gap-24">
    {unpublished ? <CampaignPersonalOnly {...props} /> : <CampaignReadStatus {...detail} empty={!detail.item} />}
    {detail.item ? <CampaignGroup {...props} items={[detail.item]} detailed publicReady={detail.writesReady} freshIds={detail.freshIds} refresh={detail.refresh} /> : null}
  </div>;
}

function CampaignPersonalOnly(props: CampaignViewProps & { id: string }) {
  const t = useTranslations('campaignV2');
  const me = useCampaignMe({ api: props.api, session: props.session, ids: [props.id] });
  const personal = me.items.find((item) => item.id === props.id) ?? null;
  if (!personal) {
    return <>
      <CampaignReadStatus {...me} privateData={me.error?.status !== 404} empty={false} />
      {me.error?.status === 401 ? <button type="button" className="btn btn-sm" onClick={props.onUnauthorized ?? props.onConnect}>{t('signIn')}</button> : null}
    </>;
  }
  return <CampaignCard {...props} campaignId={props.id} item={null} personal={personal} publicReady={false}
    personalReady={me.writesReady && me.freshIds.includes(props.id)} detailed onRefresh={me.refresh} />;
}
