/**
 * i18n bootstrap constants. The Go service + PostgreSQL own the real locale
 * metadata (enabled set, names, default) — this value exists only as the
 * final bootstrap fallback the locale-resolution contract names ('en').
 * Do NOT add a hardcoded enabled-locale list here; use useEnabledLocales()
 * (client) or getEnabledLocales() (server).
 */
export const bootstrapDefaultLocale = 'en';
