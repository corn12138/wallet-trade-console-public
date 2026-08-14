/**
 * Server-owned i18n exports. The Go API + PostgreSQL are the source of
 * truth; the client only receives the SSR-selected locale/messages via
 * LocaleHydrationProvider and the read-only enabled-locale metadata.
 */
export { bootstrapDefaultLocale } from './config';
export { LocaleHydrationProvider, useEnabledLocales, type EnabledLocale } from './provider';
