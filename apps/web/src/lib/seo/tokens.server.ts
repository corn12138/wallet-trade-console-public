import 'server-only';
import { cache } from 'react';

/**
 * Server-only token index for the sitemap.
 *
 * Reads the same internal API base as the i18n catalog client, and for the
 * same reason: the browser must never be handed an internal origin. What
 * differs is the FAILURE POLICY, and the difference is deliberate.
 *
 * `i18n/server/catalog.ts` refuses to degrade — serving stale or wrong copy
 * is worse than serving an error, so an unreachable catalog fails the render.
 * A sitemap inverts that: the static product routes are always correct on
 * their own, and answering a crawler with HTTP 500 is strictly worse than
 * answering with a shorter but valid sitemap. So an unreachable token index
 * degrades to an empty list, loudly.
 *
 * "Loudly" is the operative word — the warn line is what separates this from
 * a silent truncation that reads as "there are no tokens".
 */

const INTERNAL_API_BASE = (
  process.env.INTERNAL_API_BASE_URL ||
  process.env.NEXT_PUBLIC_API_URL ||
  'http://127.0.0.1:8090/api'
).replace(/\/$/, '');

/** Upper bound on sitemap token entries. The protocol caps a sitemap at 50k. */
const MAX_TOKEN_URLS = 5000;

/**
 * The API's own ceiling on `limit` (token.maxTokenListLimit), not a preference.
 * Asking for more is silently clamped, so a single request could never return
 * more than this many rows however large MAX_TOKEN_URLS is — which is why the
 * fetch below walks pages instead of asking for everything at once.
 */
const PAGE_SIZE = 100;

/** Shared revalidation window for the token index (1 hour). */
const TOKEN_INDEX_REVALIDATE_SECONDS = 3600;

export interface SitemapToken {
  address: string;
  /** Present only when the API supplies a usable timestamp. */
  lastModified?: Date;
}

function isEvmAddress(value: unknown): value is string {
  return typeof value === 'string' && /^0x[0-9a-fA-F]{40}$/.test(value);
}

function parseDate(value: unknown): Date | undefined {
  if (typeof value !== 'string' || value === '') return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed;
}

/**
 * Launched tokens that have a resolvable contract address.
 *
 * Rows without an address are dropped rather than linked: `/token/[address]`
 * cannot resolve them, so listing them would advertise URLs that 404 — the
 * one thing a sitemap must never do.
 */
/** One page of the token index, or null when the index cannot be read. */
async function fetchTokenPage(page: number): Promise<unknown[] | null> {
  const url =
    `${INTERNAL_API_BASE}/token?status=LAUNCHED&sortBy=marketCap` +
    `&limit=${PAGE_SIZE}&page=${page}`;
  try {
    const response = await fetch(url, {
      headers: { accept: 'application/json' },
      next: { revalidate: TOKEN_INDEX_REVALIDATE_SECONDS },
    });
    if (!response.ok) {
      console.warn(`sitemap: token index page ${page} unavailable (HTTP ${response.status})`);
      return null;
    }
    const payload: unknown = await response.json();
    // The Go list endpoints answer either `{data: [...]}` or a bare array.
    if (Array.isArray(payload)) return payload;
    const data = (payload as { data?: unknown })?.data;
    if (Array.isArray(data)) return data;
    console.warn(`sitemap: token index page ${page} returned an unexpected shape`);
    return null;
  } catch (error) {
    console.warn(`sitemap: token index page ${page} unreachable (${(error as Error).message})`);
    return null;
  }
}

export const getSitemapTokens = cache(async (): Promise<SitemapToken[]> => {
  const seen = new Set<string>();
  const tokens: SitemapToken[] = [];
  let truncated = false;

  for (let page = 1; ; page++) {
    const rows = await fetchTokenPage(page);
    // A failed page ends the walk with whatever came before it. Pages already
    // collected are still correct URLs; dropping them would punish a partial
    // outage harder than a total one.
    if (rows === null) break;

    for (const row of rows) {
      const address = (row as { address?: unknown })?.address;
      if (!isEvmAddress(address)) continue;
      const canonical = address.toLowerCase();
      if (seen.has(canonical)) continue;
      seen.add(canonical);
      tokens.push({
        address: canonical,
        lastModified:
          parseDate((row as { launchedAt?: unknown }).launchedAt) ??
          parseDate((row as { createdAt?: unknown }).createdAt),
      });
    }

    if (rows.length < PAGE_SIZE) break; // short page = last page
    if (tokens.length >= MAX_TOKEN_URLS) {
      truncated = true;
      break;
    }
  }

  if (truncated) {
    // Say so rather than letting a capped sitemap read as a complete one.
    console.warn(
      `sitemap: stopped at the ${MAX_TOKEN_URLS}-URL cap; some token pages are not listed`,
    );
  }
  if (tokens.length === 0) {
    console.warn('sitemap: no token URLs resolved; emitting static routes only');
  }
  return tokens;
});

export interface TokenMetadataSubject {
  name: string;
  symbol: string;
}

/**
 * One token's name + symbol, for `generateMetadata` on /token/[address].
 *
 * This is the payoff of doing token metadata server-side: a crawler or a chat
 * unfurler reads `<title>`/`og:` from the initial HTML and never runs the
 * client query, so without this every token URL would share one generic
 * title. Resolving it here is what makes each token page a distinct document.
 *
 * Returns null — never throws — on a malformed address, a miss, or an
 * unreachable API. The caller then falls back to the generic catalog copy,
 * which is correct-but-unspecific rather than broken.
 */
export const getTokenForMetadata = cache(
  async (address: string): Promise<TokenMetadataSubject | null> => {
    if (!isEvmAddress(address)) return null;

    try {
      const response = await fetch(
        `${INTERNAL_API_BASE}/token/address/${encodeURIComponent(address)}`,
        {
          headers: { accept: 'application/json' },
          next: { revalidate: TOKEN_INDEX_REVALIDATE_SECONDS },
        },
      );
      if (!response.ok) return null;

      const payload = await response.json();
      const row = (payload?.data ?? payload) as { name?: unknown; symbol?: unknown } | null;
      const name = typeof row?.name === 'string' ? row.name.trim() : '';
      const symbol = typeof row?.symbol === 'string' ? row.symbol.trim() : '';
      // Both are required: "undefined (WBTC)" in a page title is worse than
      // the generic fallback, so a partial row is treated as a miss.
      if (!name || !symbol) return null;
      return { name, symbol };
    } catch {
      return null;
    }
  },
);
