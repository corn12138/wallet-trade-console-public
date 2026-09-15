/**
 * Localized display layer for API diagnostics (warnings, route notes, enum
 * states). The API speaks English/machine values; the UI must not leak them
 * as primary copy in non-English locales. Known strings map to message keys
 * under the `diagnostics` namespace (the Go server baseline en/zh catalogs); unknown
 * strings degrade safely:
 *   - en: the raw API message IS the copy, show it as-is;
 *   - other locales: show a localized "unrecognized diagnostic" line and keep
 *     the raw message only as a clearly-labelled technical detail.
 *
 * Extending: add the normalized string → key mapping here plus the key in
 * BOTH server baseline en.json and zh.json (services/api-go/internal/i18n/baseline) under `diagnostics.messages`
 * (or `diagnostics.values` for enum/boolean display states). The
 * diagnostics.spec.ts key-parity test fails if either locale file misses one.
 */

export type DiagnosticTranslator = (
  key: string,
  values?: Record<string, string | number | Date>,
) => string;

/** Normalize an API diagnostic for lookup: case/whitespace/dash variants
 *  ("-" vs "—" vs "–") and trailing punctuation must not defeat the match. */
function normalizeMessage(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[—–]/g, '-')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/[.!…]+$/, '');
}

/** Known API warning / route-note strings → diagnostics.messages.* keys. */
const KNOWN_MESSAGES: Record<string, string> = {
  'live quote unavailable: rpc not configured': 'swapRpcMissing',
  'live quote unavailable: invalid quote data': 'swapDataInvalid',
  'live quote unavailable: amounts timeout': 'swapAmountsTimeout',
  'live quote unavailable: amounts rpc rejected': 'swapAmountsRpcrejected',
  'live quote unavailable: amounts rpc failed': 'swapAmountsRpcfailed',
  'live quote unavailable: reserves timeout': 'swapReservesTimeout',
  'live quote unavailable: reserves rpc rejected': 'swapReservesRpcrejected',
  'live quote unavailable: reserves rpc failed': 'swapReservesRpcfailed',

  'live quote unavailable, using fallback estimate': 'swapFallbackEstimate',
  'simulated estimate - execution unavailable (no bridge adapter deployed)':
    'bridgeSimulatedNoAdapter',
  'destination gas top-up suggested': 'bridgeGasTopUp',
};

/** Known enum / boolean / status values → diagnostics.values.* keys. */
const KNOWN_VALUES: Record<string, string> = {
  fallback: 'fallback',
  router: 'router',
  live: 'live',
  'display-only': 'displayOnly',
  executable: 'executable',
  'not executable': 'notExecutable',
  simulated: 'simulated',
  managed: 'managed',
  standard: 'standard',
  low: 'low',
  medium: 'medium',
  high: 'high',
  critical: 'critical',
  true: 'true',
  false: 'false',
  watching: 'watching',
  linked: 'linked',
  'watch-only': 'watchOnly',
  authenticated: 'authenticated',
};

/** Stable product-status warning codes (GET /api/status/product) →
 *  diagnostics.status.* keys. The API sends a stable machine `code` plus an
 *  English fallback message; the UI renders the localized copy by code. */
const KNOWN_STATUS_CODES: Record<string, string> = {
  DB_UNAVAILABLE: 'dbUnavailable',
  INDEXER_NO_CURSOR: 'indexerNoCursor',
  INDEXER_BEHIND_EVENTS: 'indexerBehind',
  REALTIME_DISABLED: 'realtimeDisabled',
  SWAP_ROUTER_UNCONFIGURED: 'swapNoRouter',
  MEDIA_UNCONFIGURED: 'mediaUnconfigured',
  BRIDGE_UNAVAILABLE: 'bridgeUnavailable',
  NO_SCENARIO_DATA: 'noScenarioData',
  PAPER_TRADE_ENABLED: 'paperTradeEnabled',
};

export function statusWarningKey(code: string): string | null {
  return KNOWN_STATUS_CODES[code] ?? null;
}

/** All known status codes — used by the key-parity test to assert both locale
 *  files carry every `diagnostics.status.*` key. */
export const KNOWN_STATUS_CODE_KEYS = Object.values(KNOWN_STATUS_CODES);

export function diagnosticMessageKey(raw: string): string | null {
  return KNOWN_MESSAGES[normalizeMessage(raw)] ?? null;
}

export function diagnosticValueKey(raw: string): string | null {
  return KNOWN_VALUES[normalizeMessage(raw)] ?? null;
}

export interface DiagnosticDisplay {
  /** Primary visible line — always in the active locale for known strings. */
  text: string;
  /** Raw API string, kept ONLY when it is not already the primary text
   *  (unknown diagnostic in a non-English locale). Render it as a labelled
   *  technical detail (diagnostics.apiDetailLabel), never as main copy. */
  detail: string | null;
  known: boolean;
}

/**
 * Localize one API warning / note. `t` must be scoped to the `diagnostics`
 * namespace (useTranslations('diagnostics')).
 */
export function formatDiagnosticMessage(
  t: DiagnosticTranslator,
  locale: string,
  raw: string,
): DiagnosticDisplay {
  const key = diagnosticMessageKey(raw);
  if (key) {
    return { text: t(`messages.${key}`), detail: null, known: true };
  }
  if (locale === 'en') {
    // English is the API's own language: the raw message is honest copy.
    return { text: raw, detail: null, known: false };
  }
  return { text: t('unknownMessage'), detail: raw, known: false };
}

/**
 * Localize a product-status warning by its stable code. Known codes render
 * localized copy from `diagnostics.status.*`; unknown codes fall back to the
 * English API message (en) or a labelled unknown line (other locales), so a
 * new backend warning never leaks raw English as primary copy in Chinese mode.
 */
export function formatStatusWarning(
  t: DiagnosticTranslator,
  locale: string,
  code: string,
  apiMessage: string,
): DiagnosticDisplay {
  const key = statusWarningKey(code);
  if (key) {
    return { text: t(`status.${key}`), detail: null, known: true };
  }
  if (locale === 'en') {
    return { text: apiMessage, detail: null, known: false };
  }
  return { text: t('unknownMessage'), detail: apiMessage, known: false };
}

/**
 * Localize a machine enum/boolean when it is shown as user-facing copy
 * (risk levels, quote provenance, wallet link states, execution modes).
 * Unknown values pass through unchanged — they are metadata, not copy.
 */
export function formatDiagnosticValue(
  t: DiagnosticTranslator,
  raw: string | boolean | number | null | undefined,
): string {
  if (raw === null || raw === undefined || raw === '') return '—';
  const asString = String(raw);
  const key = diagnosticValueKey(asString);
  return key ? t(`values.${key}`) : asString;
}
