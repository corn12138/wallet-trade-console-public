import { decodeCampaignContract } from './decode-revision';
import type { CampaignCommandIntent, CommandError } from './command-types';
import { CampaignAPIError, type InteractionCommand } from './ports';

export function newCommandID(): string {
  const now = Date.now();
  if (!Number.isSafeInteger(now) || now < 0 || now >= 2 ** 48 || !globalThis.crypto?.getRandomValues) {
    throw new CampaignAPIError('CAMPAIGN_COMMAND_ID_UNAVAILABLE', 0);
  }
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  let timestamp = BigInt(now);
  for (let index = 5; index >= 0; index--) { bytes[index] = Number(timestamp & 255n); timestamp >>= 8n; }
  bytes[6] = (bytes[6] & 15) | 0x70;
  bytes[8] = (bytes[8] & 63) | 0x80;
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
export function freezeCommand(intent: CampaignCommandIntent, clientCommandId: string): InteractionCommand {
  const positive = intent.operation === 'join' || intent.operation === 'set-reminder';
  const raw = positive
    ? { clientCommandId, expectedStateVersion: intent.expectedStateVersion, expectedPublicationVersion: intent.expectedPublicationVersion }
    : { clientCommandId, expectedStateVersion: intent.expectedStateVersion };
  const decoded = positive ? decodeCampaignContract('PositiveInteractionInput', raw) : decodeCampaignContract('CancelInteractionInput', raw);
  if (!decoded.supported || !['join', 'leave', 'set-reminder', 'remove-reminder'].includes(intent.operation)) {
    throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
  }
  const command = { operation: intent.operation, input: Object.freeze(decoded.value) } as InteractionCommand;
  return Object.freeze(command);
}
export function commandError(error: unknown): CommandError {
  if (error instanceof CampaignAPIError) return { code: error.code, status: error.status };
  return { code: error instanceof Error && error.name === 'AbortError' ? 'ABORTED' : 'NETWORK_ERROR', status: 0 };
}
export function requiresReview(error: CommandError): boolean {
  // A key collision may refer to a different committed intent. Only ledger
  // recovery can resolve it; a fresh key would conceal the ambiguity.
  return error.code !== 'IDEMPOTENCY_CONFLICT' && error.code !== 'CAMPAIGN_RESPONSE_INVALID'
    && error.status !== null && [400, 403, 404, 409, 410, 413, 415, 422].includes(error.status);
}
