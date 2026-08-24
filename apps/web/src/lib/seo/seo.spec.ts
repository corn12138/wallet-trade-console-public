import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import robots from '@/app/robots';
import { absoluteUrl, siteUrl } from './site';
import { SEO_ROUTES, disallowedPaths, publicStaticRoutes, seoRoute } from './routes';

/**
 * The catalogs the Go service owns. Read from disk rather than imported so
 * this fails loudly if the path ever moves — apps/web deliberately ships no
 * runtime catalog of its own.
 */
const BASELINE_DIR = join(
  __dirname, '..', '..', '..', '..', '..',
  'services', 'api-go', 'internal', 'i18n', 'baseline',
);

function catalog(locale: 'en' | 'zh'): Record<string, any> {
  return JSON.parse(readFileSync(join(BASELINE_DIR, `${locale}.json`), 'utf8'));
}

function lookup(obj: Record<string, any>, path: string): unknown {
  return path.split('.').reduce<any>((node, part) => (node == null ? undefined : node[part]), obj);
}

describe('SEO route registry', () => {
  it('has a unique key and path per route', () => {
    expect(new Set(SEO_ROUTES.map((r) => r.key)).size).toBe(SEO_ROUTES.length);
    expect(new Set(SEO_ROUTES.map((r) => r.path)).size).toBe(SEO_ROUTES.length);
  });

  it('throws on an unknown key so a typo cannot ship silently', () => {
    expect(() => seoRoute('not-a-route')).toThrow(/unknown route key/);
  });

  it('keeps every wallet-scoped surface out of the sitemap', () => {
    // The concrete list matters more than the count: adding a personal page
    // to the public set should fail here, not in a search result.
    const publicPaths = publicStaticRoutes().map((r) => r.path);
    for (const personal of ['/portfolio', '/activity', '/security', '/wallets', '/settings']) {
      expect(publicPaths).not.toContain(personal);
    }
    expect(publicStaticRoutes().every((r) => r.visibility === 'public')).toBe(true);
  });

  it('gives every sitemap entry a change frequency and priority', () => {
    for (const route of publicStaticRoutes()) {
      expect(route.changeFrequency, `${route.path} changeFrequency`).toBeTruthy();
      expect(route.priority, `${route.path} priority`).toBeGreaterThan(0);
      expect(route.priority).toBeLessThanOrEqual(1);
    }
  });

  it('translates dynamic private routes into robots prefix rules', () => {
    // robots.txt cannot express a path parameter, so `/profile/[address]`
    // must become the `/profile/` prefix rather than a literal template.
    const disallowed = disallowedPaths();
    expect(disallowed).toContain('/profile/');
    expect(disallowed.some((p) => p.includes('['))).toBe(false);
  });

  it('disallows every private route and no public one', () => {
    const disallowed = disallowedPaths();
    for (const route of SEO_ROUTES) {
      const expected = route.dynamic ? `${route.path.split('/[')[0]}/` : route.path;
      if (route.visibility === 'private') {
        expect(disallowed, `${route.path} must be disallowed`).toContain(expected);
      } else {
        expect(disallowed, `${route.path} must stay crawlable`).not.toContain(expected);
      }
    }
  });
});

describe('SEO copy is backed by the server-owned catalog', () => {
  // This is the regression guard for the failure that shipped once already:
  // a registry key with no catalog entry renders the raw key path
  // ("metadata.routes.markets.title") into <title>, which type-checks,
  // lints and passes every other test.
  const en = catalog('en');
  const zh = catalog('zh');

  it.each(SEO_ROUTES.map((r) => r.key))('has en + zh title and description for %s', (key) => {
    for (const [locale, cat] of [['en', en], ['zh', zh]] as const) {
      for (const field of ['title', 'description']) {
        const value = lookup(cat, `metadata.routes.${key}.${field}`);
        expect(typeof value, `${locale}: metadata.routes.${key}.${field}`).toBe('string');
        expect((value as string).trim().length).toBeGreaterThan(0);
      }
    }
  });

  it('carries the interpolated token variants in both locales', () => {
    for (const [locale, cat] of [['en', en], ['zh', zh]] as const) {
      for (const field of ['titleNamed', 'descriptionNamed']) {
        const value = lookup(cat, `metadata.routes.token.${field}`) as string;
        expect(typeof value, `${locale}: token.${field}`).toBe('string');
        // Both placeholders must survive translation or the title loses the
        // very thing that makes each token page distinct.
        expect(value, `${locale}: token.${field}`).toContain('{name}');
        expect(value, `${locale}: token.${field}`).toContain('{symbol}');
      }
    }
  });
});

describe('sitemap paging matches the API contract', () => {
  // The token list endpoint clamps `limit` to its own maximum and says nothing
  // about it. Asking for more than the server allows does not error — it just
  // returns fewer rows, so a sitemap built from one oversized request would
  // silently stop listing tokens past that cap. This pins the two numbers
  // together; if the Go ceiling moves, this fails instead of the sitemap
  // quietly shrinking.
  const GO_LIMITS = join(
    __dirname, '..', '..', '..', '..', '..',
    'services', 'api-go', 'internal', 'token', 'limits.go',
  );

  it('never requests more rows per page than the API will return', () => {
    const src = readFileSync(GO_LIMITS, 'utf8');
    const match = src.match(/maxTokenListLimit\s*=\s*(\d+)/);
    expect(match, 'maxTokenListLimit not found in token/limits.go').toBeTruthy();
    const serverMax = Number(match![1]);

    const client = readFileSync(join(__dirname, 'tokens.server.ts'), 'utf8');
    const pageSize = Number(client.match(/const PAGE_SIZE = (\d+);/)![1]);

    expect(pageSize).toBeLessThanOrEqual(serverMax);
  });

  it('walks pages rather than asking for every token at once', () => {
    const client = readFileSync(join(__dirname, 'tokens.server.ts'), 'utf8');
    expect(client).toContain('page=');
    // A single unbounded request is the bug this replaced.
    expect(client).not.toMatch(/limit=\$\{MAX_TOKEN_URLS\}/);
  });
});

describe('robots.txt', () => {
  const result = robots();

  it('advertises the sitemap and host from the same origin helper', () => {
    expect(result.sitemap).toBe(`${siteUrl()}/sitemap.xml`);
    expect(result.host).toBe(siteUrl());
  });

  it('allows the site but blocks the API and every private route', () => {
    const rule = Array.isArray(result.rules) ? result.rules[0] : result.rules;
    expect(rule?.allow).toBe('/');
    const disallow = rule?.disallow as string[];
    expect(disallow).toContain('/api/');
    for (const path of disallowedPaths()) {
      expect(disallow).toContain(path);
    }
  });
});

describe('origin helper', () => {
  it('never emits a double slash', () => {
    expect(absoluteUrl('/')).toBe(`${siteUrl()}/`);
    expect(absoluteUrl('/markets')).toBe(`${siteUrl()}/markets`);
    expect(siteUrl().endsWith('/')).toBe(false);
  });
});
