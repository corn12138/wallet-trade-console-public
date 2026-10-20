import { parseCampaignJSON } from './decode-contract-json';
import { decodeCampaignContract } from './decode-revision';
import { CampaignAPIError } from './ports';
import type { CommandRecord } from './command-types';

export interface CommandStorage { getItem(key: string): string | null; setItem(key: string, value: string): void }
export const COMMAND_STORAGE_KEY = 'campaign:commands:v1';
const MAX_RECORDS = 20;
const MAX_BYTES = 32 * 1024;
export function commandKey(owner: string, campaignId: string): string { return `${owner}:${campaignId}`; }
function invalid(): never { throw new CampaignAPIError('CAMPAIGN_RECOVERY_STORAGE_INVALID', 0); }
function object(value: unknown, keys: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalid();
  const result = value as Record<string, unknown>;
  if (Object.keys(result).length !== keys.length || keys.some((key) => !Object.hasOwn(result, key))) invalid();
  return result;
}
function decodeRecord(value: unknown): CommandRecord {
  const record = object(value, ['version', 'owner', 'campaignId', 'command', 'createdAt', 'disposition']);
  if (record.version !== 1 || typeof record.owner !== 'string' || !/^0x[0-9a-f]{40}$/.test(record.owner)
    || typeof record.campaignId !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(record.campaignId)
    || typeof record.createdAt !== 'number' || !Number.isSafeInteger(record.createdAt) || record.createdAt < 0
    || !['pending', 'review'].includes(String(record.disposition))) invalid();
  const command = object(record.command, ['operation', 'input']);
  const operation = command.operation;
  if (!['join', 'leave', 'set-reminder', 'remove-reminder'].includes(String(operation))) invalid();
  const decoded = operation === 'join' || operation === 'set-reminder'
    ? decodeCampaignContract('PositiveInteractionInput', command.input)
    : decodeCampaignContract('CancelInteractionInput', command.input);
  if (!decoded.supported) invalid();
  return Object.freeze({ ...record, command: Object.freeze({ operation, input: Object.freeze(decoded.value) }) }) as CommandRecord;
}
export function readCommandRecords(storage: CommandStorage): Map<string, CommandRecord> {
  const raw = storage.getItem(COMMAND_STORAGE_KEY);
  if (raw === null) return new Map();
  let parsed: unknown;
  try { parsed = parseCampaignJSON(raw, MAX_BYTES); } catch { return invalid(); }
  const envelope = object(parsed, ['version', 'records']);
  if (envelope.version !== 1 || !Array.isArray(envelope.records) || envelope.records.length > MAX_RECORDS) invalid();
  const records = new Map<string, CommandRecord>();
  for (const item of envelope.records) {
    const record = decodeRecord(item); const key = commandKey(record.owner, record.campaignId);
    if (records.has(key)) invalid(); records.set(key, record);
  }
  return records;
}
export function writeCommandRecords(storage: CommandStorage, records: ReadonlyMap<string, CommandRecord>): void {
  if (records.size > MAX_RECORDS) throw new CampaignAPIError('CAMPAIGN_COMMAND_CAPACITY', 0);
  const raw = JSON.stringify({ version: 1, records: [...records.values()] });
  if (new TextEncoder().encode(raw).length > MAX_BYTES) throw new CampaignAPIError('CAMPAIGN_COMMAND_CAPACITY', 0);
  storage.setItem(COMMAND_STORAGE_KEY, raw);
}
export function browserCommandStorage(): CommandStorage | null {
  return typeof window === 'undefined' ? null : window.sessionStorage;
}
