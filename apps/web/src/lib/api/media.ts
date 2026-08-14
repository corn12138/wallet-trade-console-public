import { fetchApi } from './auth-fetch';

/**
 * Client for the Go media upload service (POST /api/media/*). Uploads are
 * SIWE-guarded server-side; fetchApi attaches the web3 auth token.
 *
 * These calls throw with the server's message on failure — callers must block
 * their submit flow on that error instead of substituting a placeholder URL.
 */

export interface UploadedMedia {
  key: string;
  url: string;
  contentType: string;
  size: number;
  provider: string;
}

export interface NftMetadataUpload extends Pick<UploadedMedia, 'key' | 'url'> {
  metadata: Record<string, unknown>;
}

async function readError(response: Response, fallback: string): Promise<string> {
  try {
    const body = (await response.json()) as { message?: string };
    if (body?.message) return body.message;
  } catch {
    // non-JSON error body — fall through
  }
  return fallback;
}

/** Upload a selected image file; returns the durable stored URL. */
export async function uploadMediaFile(file: File | Blob): Promise<UploadedMedia> {
  const form = new FormData();
  form.append('file', file);
  const response = await fetchApi('/media/upload', {
    method: 'POST',
    body: form,
  });
  if (!response.ok) {
    throw new Error(await readError(response, `Media upload failed (${response.status})`));
  }
  return (await response.json()) as UploadedMedia;
}

/** Build + store an ERC-721 metadata JSON document server-side. */
export async function uploadNftMetadata(input: {
  name: string;
  description?: string;
  image: string;
  externalUrl?: string;
  attributes?: Array<{ trait_type: string; value: string }>;
}): Promise<NftMetadataUpload> {
  const response = await fetchApi('/media/nft-metadata', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!response.ok) {
    throw new Error(await readError(response, `Metadata upload failed (${response.status})`));
  }
  return (await response.json()) as NftMetadataUpload;
}
