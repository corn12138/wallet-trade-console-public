import { describe, expect, it } from 'vitest';
import { useEnumLabel } from './enums';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import { TokenDetailHeader } from '@/app/token/[address]/TokenDetailHeader';
import type { TokenData } from '@/app/token/[address]/token-detail.types';

function Probe({ family, raw }: { family: Parameters<typeof useEnumLabel>[0]; raw: string | null }) {
  const label = useEnumLabel(family);
  return <span data-testid="probe">{label(raw)}</span>;
}

const LAUNCHED_TOKEN = {
  id: 't1',
  address: '0xcfb011c568cc3a0a58e088df63be2cfa49ef807d',
  symbol: 'ATL',
  name: 'Atlas Scenario',
  status: 'LAUNCHED',
  marketCap: '12345.678901234',
  volume24h: '0.0075',
  priceChange24h: 4.2,
} as unknown as TokenData;

describe('enum translation layer', () => {
  it('maps known machine enums to localized copy (zh)', () => {
    const { getByTestId, unmount } = renderWithIntlZh(<Probe family="token" raw="LAUNCHED" />);
    expect(getByTestId('probe').textContent).toBe('已发射');
    unmount();
    const { getByTestId: g2 } = renderWithIntlZh(<Probe family="txType" raw="BUY" />);
    expect(g2.call(null, 'probe').textContent).toBe('买入');
  });

  it('renders unknown values as localized unknown-state with the raw code as detail', () => {
    const first = renderWithIntl(<Probe family="token" raw="SOME_NEW_STATE" />);
    expect(first.getByTestId('probe').textContent).toBe('Unknown state (SOME_NEW_STATE)');
    first.unmount();
    const second = renderWithIntlZh(<Probe family="token" raw="SOME_NEW_STATE" />);
    expect(second.getByTestId('probe').textContent).toBe('未知状态（SOME_NEW_STATE）');
  });

  it('Chinese token header shows no raw English enum values', () => {
    renderWithIntlZh(
      <TokenDetailHeader
        tokenAddress={LAUNCHED_TOKEN.address as string}
        token={LAUNCHED_TOKEN}
        stats={null}
        currentPriceLabel="0.000012 NATIVE"
      />,
    );
    const body = document.body.textContent ?? '';
    expect(body).toContain('已发射');
    expect(body).not.toContain('LAUNCHED');
    expect(body).not.toContain('PENDING');
    // Market cap renders the normalized NATIVE unit, compact-formatted.
    expect(body).toMatch(/NATIVE/);
    expect(body).not.toMatch(/12345\.678901234/); // raw decimal never shown unformatted
  });
});
