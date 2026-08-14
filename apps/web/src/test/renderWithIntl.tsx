import type { ReactElement, ReactNode } from 'react';
import { render, type RenderOptions } from '@testing-library/react';
import { LocaleHydrationProvider } from '@/i18n/provider';
// Test fixture = the SERVER-OWNED baseline catalog (services/api-go), the
// exact content the Go bootstrap seeds and publishes. apps/web no longer
// ships a runtime message catalog of its own.
import en from '../../../../services/api-go/internal/i18n/baseline/en.json';
import zh from '../../../../services/api-go/internal/i18n/baseline/zh.json';

const TEST_LOCALES = [
  { code: 'en', englishName: 'English', nativeName: 'English', isDefault: true },
  { code: 'zh', englishName: 'Chinese (Simplified)', nativeName: '简体中文', isDefault: false },
];

/**
 * Test render wrapped in the app's immutable LocaleHydrationProvider (which
 * mounts NextIntlClientProvider) with the real English server baseline, so
 * components using useTranslations/useLocale render exactly the copy the en
 * catalog publishes. Specs asserting on visible strings therefore also pin
 * the baseline values — deleting a key fails the spec.
 */
export function IntlWrapper({ children }: { children: ReactNode }) {
  return (
    <LocaleHydrationProvider locale="en" messages={en} enabledLocales={TEST_LOCALES}>
      {children}
    </LocaleHydrationProvider>
  );
}

export function renderWithIntl(ui: ReactElement, options?: Omit<RenderOptions, 'wrapper'>) {
  return render(ui, { wrapper: IntlWrapper, ...options });
}

/** Chinese-locale render for enum/copy leak assertions. */
export function ZhIntlWrapper({ children }: { children: ReactNode }) {
  return (
    <LocaleHydrationProvider locale="zh" messages={zh} enabledLocales={TEST_LOCALES}>
      {children}
    </LocaleHydrationProvider>
  );
}

export function renderWithIntlZh(ui: ReactElement, options?: Omit<RenderOptions, 'wrapper'>) {
  return render(ui, { wrapper: ZhIntlWrapper, ...options });
}
