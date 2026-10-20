import type { EventFeedResponse } from './contract.generated';
import { decodeCampaignContract } from './decode-revision';
import { CampaignAPIError } from './ports';
import { timestamp, type ReadRecovery, type SnapshotRead } from './read-state';

type Family = EventFeedResponse['family'];
export type CampaignEventStream = {
  campaignId: string;
  family: Family;
  read(signal: AbortSignal, cursor?: string): Promise<EventFeedResponse>;
};
type Position = { cursor: string; version: bigint; recent: Map<string, string> };
type Advance = { position: Position; changed: boolean; anchored: boolean; backlog: boolean };
function invalid(): never { throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502); }
const version = (value: string) => {
  const parsed = BigInt(value);
  if (parsed > 9223372036854775807n) invalid();
  return parsed;
};

function advance(stream: CampaignEventStream, previous: Position | null, raw: EventFeedResponse): Advance {
  const decoded = decodeCampaignContract('EventFeedResponse', raw);
  if (!decoded.supported) invalid();
  const page = decoded.value;
  if (page.campaignId !== stream.campaignId || page.family !== stream.family) invalid();
  timestamp(page.serverNow);
  timestamp(page.asOf);
  const head = version(page.headVersion);
  if (page.resetRequired) {
    if (page.events.length || page.hasMore) invalid();
    return { position: { cursor: page.cursor, version: head, recent: new Map() }, changed: true, anchored: true, backlog: false };
  }
  if (!previous || head < previous.version) invalid();
  let last = previous.version;
  let changed = false;
  const recent = new Map(previous.recent);
  for (const event of page.events) {
    timestamp(event.occurredAt);
    version(event.publicationVersion);
    version(event.interactionVersion);
    const next = version(event.streamVersion);
    if (event.eventId === '00000000-0000-0000-0000-000000000000' || next > head || next === 0n) invalid();
    // Repeated delivery is harmless only when both identity and version agree.
    // An unseen old version or a hole must be repaired from authoritative REST.
    if (recent.has(event.eventId)) {
      if (recent.get(event.eventId) !== event.streamVersion) invalid();
      continue;
    }
    if (next !== last + 1n) invalid();
    recent.set(event.eventId, event.streamVersion);
    last = next;
    changed = true;
  }
  if (page.hasMore !== (last < head) || last > previous.version && page.cursor === previous.cursor) invalid();
  while (recent.size > 100) recent.delete(recent.keys().next().value!);
  return { position: { cursor: page.cursor, version: last, recent }, changed, anchored: false, backlog: page.hasMore };
}

/** Feed cursors are local to one mounted read scope. Hints only invalidate;
 * every returned business value comes from the existing REST snapshot reader. */
export function createCampaignRecovery<Wire>(snapshot: SnapshotRead<Wire>, streams: readonly CampaignEventStream[]): ReadRecovery<Wire> {
  let positions: (Position | null)[] = streams.map(() => null);
  let generation = 0;
  return {
    reset() { generation++; positions = streams.map(() => null); },
    async read(signal, cursor) {
      const turn = ++generation;
      const active = () => { if (signal.aborted || turn !== generation) throw new CampaignAPIError('ABORTED', 0); };
      const next = [...positions];
      const poll = async (index: number): Promise<Advance | null> => {
        const stream = streams[index];
        try {
          active();
          let previous = next[index];
          let page: EventFeedResponse;
          try { page = await stream.read(signal, previous?.cursor); }
          catch (error) {
            active();
            if (!previous || !(error instanceof CampaignAPIError) || error.status !== 400) throw error;
            previous = null;
            page = await stream.read(signal);
          }
          active();
          let result: Advance | null;
          try { result = advance(stream, previous, page); }
          catch { result = null; }
          if (!result || result.backlog) {
            // A bounded read cannot chase an unbounded backlog. Anchor at the
            // current head, then use REST for the complete business projection.
            const anchor = await stream.read(signal);
            active();
            result = advance(stream, null, anchor);
          }
          next[index] = result.position;
          return result;
        } catch (error) {
          active();
          next[index] = null;
          if (error instanceof CampaignAPIError && error.status === 401) throw error;
          // Feed rollout/outage/schema failures do not invent business state;
          // the bounded snapshot read still decides availability and authority.
          return null;
        }
      };
      const anchored: number[] = [];
      for (let index = 0; index < streams.length; index++) {
        if ((await poll(index))?.anchored) anchored.push(index);
      }
      active();
      let value = await snapshot(signal, cursor);
      active();
      let repair = false;
      // Anchor before REST, then close the bootstrap window once. Later polls
      // reuse these cursors, so a mutation during either snapshot is not lost.
      for (const index of anchored) {
        const result = await poll(index);
        repair ||= result === null || result.changed;
      }
      if (repair) {
        active();
        value = await snapshot(signal, cursor);
        active();
      }
      positions = next;
      return value;
    },
  };
}
