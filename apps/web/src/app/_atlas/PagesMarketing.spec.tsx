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
 * real destination/capability. The public copy must avoid unsupported capabilities:
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

  it('describes the deployed testnet and supported wallet connector', () => {
    renderWithIntl(<HomePage />);
    const body = Array.from(document.querySelectorAll('p, h1, h2, h3, a, span')).map((el) => el.textContent).join(' ');
    expect(body).not.toMatch(/three clicks/i);
    expect(body).not.toMatch(/any chain/i);
    expect(body).not.toMatch(/multi-chain/i);
    expect(body).toMatch(/Sepolia testnet/);
    expect(body).toMatch(/injected browser wallet/i);
    expect(screen.queryByText(/WalletConnect|Coinbase|\bReown\b/i)).not.toBeInTheDocument();
    expect(body).not.toMatch(/roadmap|under 60s|Realtime matching/i);
  });

  it('renders honestly in Chinese too (no English CTA leakage)', () => {
    renderWithIntlZh(<HomePage />);
    const body = Array.from(document.querySelectorAll('p, h1, h2, h3, a, span')).map((el) => el.textContent).join(' ');
    expect(body).toContain('浏览应用目录');
    expect(screen.getByText('实时行情。')).toBeInTheDocument();
    expect(body).not.toMatch(/实时撮合|60 秒|仍未上线|WalletConnect|Coinbase|\bReown\b/);
    expect(body).not.toMatch(/open faucet|whitepaper|three clicks/i);
  });
});
