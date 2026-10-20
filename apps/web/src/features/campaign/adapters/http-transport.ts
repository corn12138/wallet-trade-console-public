import { buildApiUrl } from '@/lib/api/base-url';
import type { CampaignContractMap, CampaignContractName } from '../model/contract.generated';
import { decodeCampaignContract, decodeCampaignContractJSON } from '../model/decode-revision';
import { CampaignAPIError, type CampaignSession } from '../model/ports';
import { CAMPAIGN_VARIANTS_HEADER } from '../model/variants';

export type CampaignHTTPOptions = { fetch?: typeof fetch; timeoutMs?: number };
const RESPONSE_LIMIT = 1024 * 1024;
const bytes = (value: string) => new TextEncoder().encode(value).length;

export async function boundedText(response: Response, signal: AbortSignal): Promise<string> {
  if (signal.aborted) {
    void response.body?.cancel().catch(() => undefined);
    throw new CampaignAPIError('ABORTED', 0);
  }
  const declared = response.headers.get('content-length');
  if (declared && /^\d+$/.test(declared) && Number(declared) > RESPONSE_LIMIT) {
    void response.body?.cancel().catch(() => undefined);
    throw new CampaignAPIError('CAMPAIGN_RESPONSE_TOO_LARGE', 502);
  }
  if (!response.body) {
    const text = await response.text();
    if (bytes(text) > RESPONSE_LIMIT) throw new CampaignAPIError('CAMPAIGN_RESPONSE_TOO_LARGE', 502);
    return text;
  }
  const reader = response.body.getReader();
  const cancel = () => { void reader.cancel().catch(() => undefined); };
  signal.addEventListener('abort', cancel, { once: true });
  // Preserve a leading BOM so the shared JSON parser rejects the same bytes
  // as Go, instead of TextDecoder silently normalizing an invalid transport.
  const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
  let size = 0;
  let text = '';
  try {
    while (true) {
      const chunk = await reader.read();
      if (signal.aborted) throw new CampaignAPIError('ABORTED', 0);
      if (chunk.done) return text + decoder.decode();
      size += chunk.value.byteLength;
      if (size > RESPONSE_LIMIT) {
        cancel();
        throw new CampaignAPIError('CAMPAIGN_RESPONSE_TOO_LARGE', 502);
      }
      text += decoder.decode(chunk.value, { stream: true });
    }
  } catch (error) {
    if (error instanceof CampaignAPIError) throw error;
    throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
  } finally {
    signal.removeEventListener('abort', cancel);
    reader.releaseLock();
  }
}

export function encodeInput(name: CampaignContractName, input: unknown): string {
  let raw: string;
  try { raw = JSON.stringify(input); }
  catch { throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400); }
  if (typeof raw !== 'string') throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
  if (bytes(raw) > 64 * 1024) throw new CampaignAPIError('CAMPAIGN_BODY_TOO_LARGE', 413);
  const decoded = decodeCampaignContract(name, input);
  if (!decoded.supported) throw new CampaignAPIError(decoded.reason === 'unsupported' ? 'CAMPAIGN_UNSUPPORTED' : 'CAMPAIGN_INVALID_INPUT', decoded.reason === 'unsupported' ? 422 : 400);
  return JSON.stringify(decoded.value);
}

export function createTransport(options: CampaignHTTPOptions) {
  const fetcher = options.fetch ?? globalThis.fetch;
  const timeoutMs = options.timeoutMs ?? 10_000;
  if (!Number.isFinite(timeoutMs) || timeoutMs < 1 || timeoutMs > 30_000) throw new RangeError('campaign timeout must be between 1 and 30000ms');
  return async function request<Name extends CampaignContractName>(name: Name, path: string, signal?: AbortSignal, session?: CampaignSession, body?: string, method = 'GET', variants: readonly string[] = []): Promise<CampaignContractMap[Name]> {
    if (signal?.aborted) throw new CampaignAPIError('ABORTED', 0);
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (variants.length) headers[CAMPAIGN_VARIANTS_HEADER] = variants.join(', ');
    if (session) {
      if (!session.token || /[\r\n\0]/.test(session.token)) throw new CampaignAPIError('UNAUTHORIZED', 401);
      headers.Authorization = `Bearer ${session.token}`;
    }
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const controller = new AbortController();
    let rejectStop: (error: CampaignAPIError) => void = () => undefined;
    const stopped = new Promise<never>((_, reject) => { rejectStop = reject; });
    const stop = (code: string) => {
      // Reject first so an aborted body read cannot disguise a timeout. Racing
      // the work also bounds injected transports that ignore AbortSignal.
      rejectStop(new CampaignAPIError(code, 0));
      controller.abort();
    };
    const abort = () => stop('ABORTED');
    signal?.addEventListener('abort', abort, { once: true });
    const timer = setTimeout(() => stop('TIMEOUT'), timeoutMs);
    const work = async () => {
      const response = await fetcher(buildApiUrl(`/v2${path}`), {
        method, headers, body, signal: controller.signal,
        credentials: 'omit', cache: 'no-store', redirect: 'error',
      });
      const raw = await boundedText(response, controller.signal);
      if (!response.ok) {
        const failure = decodeCampaignContractJSON('ErrorResponse', raw);
        if (!failure.supported) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
        throw new CampaignAPIError(failure.value.code, response.status, failure.value.requestId);
      }
      const result = decodeCampaignContractJSON(name, raw);
      if (!result.supported) throw new CampaignAPIError(result.reason === 'unsupported' ? 'CAMPAIGN_UNSUPPORTED' : 'CAMPAIGN_RESPONSE_INVALID', 502);
      return result.value;
    };
    try { return await Promise.race([work(), stopped]); }
    catch (error) {
      if (error instanceof CampaignAPIError) throw error;
      throw new CampaignAPIError('NETWORK_ERROR', 0);
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', abort);
    }
  };
}
