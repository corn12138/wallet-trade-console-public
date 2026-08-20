import { render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Icon, ICON_PATHS, type IconName } from './Icon';
import { NAV_ITEMS } from './nav';

/**
 * Guards for a class of bug that ships silently: an icon that is WRONG rather
 * than missing. Nothing throws, nothing fails to render, and a screenshot
 * review has to spot that two rows drew the same picture — which is exactly
 * how the sidebar shipped with home/trade, portfolio/wallets and earn/staking
 * sharing a glyph, and with `nft` byte-identical to the `cube` fallback.
 */

describe('icon table', () => {
  it('draws every icon with a distinct path', () => {
    const byPath = new Map<string, string[]>();
    for (const [name, d] of Object.entries(ICON_PATHS)) {
      if (!byPath.has(d)) byPath.set(d, []);
      byPath.get(d)!.push(name);
    }
    const duplicates = [...byPath.values()].filter((names) => names.length > 1);
    expect(duplicates, `icons sharing one path: ${JSON.stringify(duplicates)}`).toEqual([]);
  });

  it('keeps the fallback glyph out of the ordinary icon vocabulary', () => {
    // `missing` must not be reachable as a normal choice, or "no icon" and
    // "this icon" become indistinguishable again.
    expect(ICON_PATHS.missing).toBeTruthy();
    expect(ICON_PATHS.missing).not.toBe(ICON_PATHS.cube);
  });
});

describe('unknown icon name', () => {
  afterEach(() => vi.restoreAllMocks());

  it('renders the missing glyph and warns instead of drawing a plausible icon', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    // Cast on purpose: the type makes this unreachable from real call sites,
    // so this asserts the runtime net under the compile-time guarantee.
    const { container } = render(<Icon name={'no-such-icon' as IconName} />);
    expect(container.querySelector('path')?.getAttribute('d')).toBe(ICON_PATHS.missing);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('no-such-icon'));
  });
});

describe('sidebar navigation config', () => {
  const items = NAV_ITEMS;

  it('gives every destination its own icon', () => {
    const byIcon = new Map<string, string[]>();
    for (const [path, icon] of items) {
      if (!byIcon.has(icon)) byIcon.set(icon, []);
      byIcon.get(icon)!.push(path || '/');
    }
    const shared = [...byIcon.entries()]
      .filter(([, paths]) => paths.length > 1)
      .map(([icon, paths]) => `${icon} → ${paths.join(', ')}`);
    expect(shared, `nav routes sharing an icon: ${shared.join(' | ')}`).toEqual([]);
  });

  it('points every nav entry at an icon that exists', () => {
    for (const [path, icon] of items) {
      expect(ICON_PATHS[icon], `${path || '/'} uses missing icon "${icon}"`).toBeTruthy();
    }
  });

  it('lists each route once', () => {
    const paths = items.map(([p]) => p);
    expect(new Set(paths).size).toBe(paths.length);
  });
});
