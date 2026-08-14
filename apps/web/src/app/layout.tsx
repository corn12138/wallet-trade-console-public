import { LocaleHydrationProvider } from '@/i18n';
import { getEnabledLocales } from '@/i18n/server/catalog';
import { Web3Provider } from '@/lib/web3';
import type { Metadata, Viewport } from 'next';
import { getLocale, getMessages, getTranslations } from 'next-intl/server';
import './globals.css';
import './atlas-design.css';
import { AppProvider } from './_atlas/AppContext';
import { Frame } from './_atlas/Frame';

/**
 * Metadata is generated per request from the server-owned catalog, so the
 * title/description/OpenGraph/Twitter copy follows the SSR-selected locale.
 */
export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations('metadata');
  return {
    metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL || 'https://atlas-x.app'),
    title: {
      default: t('titleDefault'),
      template: t('titleTemplate'),
    },
    description: t('description'),
    keywords: ['web3', 'onchain exchange', 'perpetual trading', 'wallet', 'defi', 'nft', 'launchpad'],
    applicationName: 'Atlas X',
    manifest: '/site.webmanifest',
    icons: {
      icon: [{ url: '/favicon.svg', type: 'image/svg+xml' }],
      shortcut: '/favicon.svg',
      apple: [{ url: '/apple-touch-icon.svg', sizes: '180x180', type: 'image/svg+xml' }],
      other: [{ rel: 'mask-icon', url: '/safari-pinned-tab.svg', color: '#FFD23F' }],
    },
    openGraph: {
      type: 'website',
      siteName: 'Atlas X',
      title: t('ogTitle'),
      description: t('ogDescription'),
      url: '/',
      images: [{ url: '/og-image.svg', width: 1200, height: 630, alt: t('ogImageAlt') }],
    },
    twitter: {
      card: 'summary_large_image',
      title: t('twitterTitle'),
      description: t('twitterDescription'),
      images: ['/og-image.svg'],
    },
    appleWebApp: {
      capable: true,
      statusBarStyle: 'default',
      title: 'Atlas X',
    },
  };
}

export const viewport: Viewport = {
  themeColor: '#1A1207',
};

export default async function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // The server resolves locale + catalog (request.ts) and supplies the
  // enabled-locale metadata; the client only hydrates what SSR selected.
  const [locale, messages, enabledLocales] = await Promise.all([
    getLocale(),
    getMessages(),
    getEnabledLocales(),
  ]);

  return (
    <html lang={locale} suppressHydrationWarning>
      <body>
        <LocaleHydrationProvider
          locale={locale}
          messages={messages as Record<string, unknown>}
          enabledLocales={enabledLocales}
        >
          <Web3Provider>
            <AppProvider>
              <div className="canvas-bg" />
              <Frame>{children}</Frame>
            </AppProvider>
          </Web3Provider>
        </LocaleHydrationProvider>
      </body>
    </html>
  );
}
