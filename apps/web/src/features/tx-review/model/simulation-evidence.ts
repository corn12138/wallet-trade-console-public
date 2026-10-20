import type {
  SimulationObservation, SimulationObservationKind, SimulationObservationStatus, SimulationPinning,
} from '@/lib/api/tx-review.types';

export const observationKinds: SimulationObservationKind[] = ['call', 'gas', 'balance', 'allowance', 'feeQuote'];
const statuses: string[] = ['succeeded', 'reverted', 'unavailable', 'unsupported', 'not-required'];
const pinningKinds: string[] = ['hash-pinned', 'number-pinned', 'latest-only', 'unavailable'];
const record = (value: unknown): Record<string, unknown> | null =>
  typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null;

function readObservation(raw: unknown): SimulationObservation | null {
  const value = record(raw);
  if (!value || !statuses.includes(String(value.observationStatus)) || !pinningKinds.includes(String(value.pinning)) ||
    !['rpc', 'not-requested'].includes(String(value.source)) || typeof value.observedAt !== 'string' ||
    !/^\d{4}-\d{2}-\d{2}T/.test(value.observedAt) || !Number.isFinite(Date.parse(value.observedAt))) return null;
  const block = record(value.blockReference);
  const pinned = value.pinning === 'hash-pinned' || value.pinning === 'number-pinned';
  if (pinned && (!block || typeof block.number !== 'string' || !/^(0|[1-9]\d*)$/.test(block.number) ||
    typeof block.hash !== 'string' || !/^0x[\da-f]{64}$/i.test(block.hash) || block.pinning !== value.pinning)) return null;
  if (value.observationStatus === 'not-required' && value.source !== 'not-requested') return null;
  if (value.observationStatus !== 'not-required' && value.source !== 'rpc') return null;
  return {
    observationStatus: value.observationStatus as SimulationObservationStatus,
    source: value.source as SimulationObservation['source'],
    pinning: value.pinning as SimulationPinning,
    observedAt: value.observedAt,
    ...(pinned ? { blockReference: block as unknown as SimulationObservation['blockReference'] } : {}),
  };
}

// During a rolling deployment, absent or future metadata is unverified. It must
// never inherit a successful verdict from the legacy callSucceeded flag.
export function readSimulationEvidence(raw: unknown) {
  const value = record(raw);
  const scope = value?.simulationScope === 'full-envelope' || value?.simulationScope === 'legacy-read-call'
    ? value.simulationScope : 'unavailable';
  const rawObservations = record(value?.observations);
  const observations = Object.fromEntries(observationKinds.map((kind) => [kind, readObservation(rawObservations?.[kind])])) as Record<SimulationObservationKind, SimulationObservation | null>;
  const required = [observations.call, observations.gas, observations.balance];
  if (observations.allowance?.observationStatus !== 'not-required') required.push(observations.allowance);
  const first = observations.call?.blockReference;
  const consistent = scope === 'full-envelope' && value?.consistentSnapshot === true && Boolean(first) &&
    required.every((item) => item?.observationStatus === 'succeeded' && item.source === 'rpc' &&
      item.blockReference?.number === first?.number && item.blockReference?.hash.toLowerCase() === first?.hash.toLowerCase());
  return { scope, observations, consistent };
}
