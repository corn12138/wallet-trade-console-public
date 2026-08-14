'use client';

/**
 * Client hydration boundary for the server-owned i18n system.
 *
 * The server resolves the locale and loads the published Go catalog during
 * SSR; this provider only carries that SSR-selected locale/messages pair into
 * client components (which may consume `useTranslations` / `useLocale` from
 * next-intl as rendering consumers). It is immutable for the life of the
 * render: no locale state, no setLocale, no cookie access, no message
 * fetching, no dynamic imports. Language switching goes through the server
 * action in `@/i18n/actions` (server-set cookie + redirect → new SSR pass).
 */

import { createContext, useContext, type ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';

export interface EnabledLocale {
  code: string;
  englishName: string;
  nativeName: string;
  isDefault: boolean;
}

const EnabledLocalesContext = createContext<EnabledLocale[]>([]);

/** Read-only list of enabled locales (SSR-provided; no setter exists). */
export function useEnabledLocales(): EnabledLocale[] {
  return useContext(EnabledLocalesContext);
}

interface LocaleHydrationProviderProps {
  locale: string;
  messages: Record<string, unknown>;
  enabledLocales: EnabledLocale[];
  children: ReactNode;
}

export function LocaleHydrationProvider({
  locale,
  messages,
  enabledLocales,
  children,
}: LocaleHydrationProviderProps) {
  return (
    <EnabledLocalesContext.Provider value={enabledLocales}>
      <NextIntlClientProvider locale={locale} timeZone="UTC" messages={messages as never}>
        {children}
      </NextIntlClientProvider>
    </EnabledLocalesContext.Provider>
  );
}
