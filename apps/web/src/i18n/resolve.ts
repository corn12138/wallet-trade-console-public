/**
 * Pure locale-resolution helpers shared by the server catalog client and its
 * tests. Resolution contract: validated `locale` cookie → first supported
 * Accept-Language match → enabled default → 'en' bootstrap fallback.
 */

export interface LocaleMetaLike {
  code: string;
  isDefault: boolean;
}

export function resolveLocale(
  cookieLocale: string | undefined,
  acceptLanguage: string | undefined,
  enabled: LocaleMetaLike[],
): string {
  const enabledCodes = new Set(enabled.map((l) => l.code));
  if (cookieLocale && enabledCodes.has(cookieLocale.toLowerCase())) {
    return cookieLocale.toLowerCase();
  }
  if (acceptLanguage) {
    for (const entry of parseAcceptLanguage(acceptLanguage)) {
      if (enabledCodes.has(entry)) return entry;
      const base = entry.split('-')[0];
      if (enabledCodes.has(base)) return base;
    }
  }
  const def = enabled.find((l) => l.isDefault);
  return def ? def.code : 'en';
}

/** Accept-Language parsed to lowercase codes ordered by q-value. */
export function parseAcceptLanguage(header: string): string[] {
  return header
    .split(',')
    .map((part) => {
      const [tag, ...params] = part.trim().split(';');
      const qParam = params.find((p) => p.trim().startsWith('q='));
      const q = qParam ? Number.parseFloat(qParam.trim().slice(2)) : 1;
      return { tag: tag.trim().toLowerCase(), q: Number.isFinite(q) ? q : 0 };
    })
    .filter((e) => e.tag && e.tag !== '*' && e.q > 0)
    .sort((a, b) => b.q - a.q)
    .map((e) => e.tag);
}

/**
 * Same-origin path guard for the language-switch redirect: only absolute
 * paths without a scheme/host ("/trade?x=1" ✓; "https://evil" ✗; "//evil" ✗).
 */
export function sanitizeReturnPath(raw: string): string {
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) {
    return '/';
  }
  try {
    const parsed = new URL(raw, 'http://localhost');
    if (parsed.origin !== 'http://localhost') return '/';
    return parsed.pathname + parsed.search;
  } catch {
    return '/';
  }
}

/** Catalog messages shape gate shared by the server client and tests. */
export function validateMessagesShape(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('catalog messages are not an object');
  }
  const namespaces = Object.keys(value as Record<string, unknown>);
  if (namespaces.length === 0) {
    throw new Error('catalog has no namespaces');
  }
  return value as Record<string, unknown>;
}
