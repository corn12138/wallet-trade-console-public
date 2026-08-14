import { describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import HomePage from './PagesMarketing';

vi.mock('./AppContext', () => ({
  useApp: () => ({ openConnect: vi.fn() }),
}));
vi.mock('./HomeLivePreview', () => ({
  HomeLivePreview: () => <div data-testid="home-live-preview-stub" />,
}));
vi.mock('./MiniChart', () => ({
  HeroIsoArt: () => <div data-testid="hero-art-stub" />,
}));

/**
 * Semantic homepage-action regressions: a control's LABEL must match its
 * real destination/capability. These pin the 2026-07-10 truth fixes:
 * no whitepaper claim, no faucet claim, no bridge-in-three-clicks claim,
 * no multi-chain badge beyond deployed capability.
 */
describe('homepage CTA semantics', () => {
  it('the discover CTA is labeled as the app directory, not a whitepaper', () => {
    renderWithIntl(<HomePage />);
    const cta = screen.getByTestId('home-cta-discover');
    expect(cta).toHaveAttribute('href', '/discover');
    expect(cta.textContent).toMatch(/app directory/i);
    expect(document.body.textContent).not.toMatch(/whitepaper/i);
  });

  it('no control or copy claims an in-app faucet', () => {
    renderWithIntl(<HomePage />);
    expect(document.body.textContent).not.toMatch(/open faucet/i);
    expect(document.body.textContent).not.toMatch(/replenishes every/i);
    // The funding step routes to the real wallet manager surface.
    const walletLinks = Array.from(document.querySelectorAll('a[href="/wallets"]'));
    expect(walletLinks.length).toBeGreaterThan(0);
  });

  it('no bridge-capability or multi-chain overclaim while bridge is roadmap', () => {
    renderWithIntl(<HomePage />);
    const body = document.body.textContent ?? '';
    expect(body).not.toMatch(/three clicks/i);
    expect(body).not.toMatch(/any chain/i);
    expect(body).not.toMatch(/multi-chain/i);
    expect(body).toMatch(/Sepolia testnet/);
    expect(body).toMatch(/roadmap/i); // bridging honestly described as roadmap
  });

  it('renders honestly in Chinese too (no English CTA leakage)', () => {
    renderWithIntlZh(<HomePage />);
    const body = document.body.textContent ?? '';
    expect(body).toContain('浏览应用目录');
    expect(body).not.toMatch(/open faucet|whitepaper|three clicks/i);
  });
});
