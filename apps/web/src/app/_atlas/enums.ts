'use client';

/**
 * Centralized localized labels for machine enum values coming out of the Go
 * API (token status, product types, staking/campaign states, tx kinds).
 * Raw enum codes must never render as primary user copy: an unknown value
 * falls back to a localized unknown-status message that carries the raw code
 * only as secondary technical detail — the UI keeps working when the API
 * grows a new state, without silently showing machine text.
 *
 * Catalog namespace: statusEnums.<family>.<RAW_VALUE> (server-owned).
 */

import { useTranslations } from 'next-intl';

export type EnumFamily =
  | 'token'        // PENDING | LAUNCHED | GRADUATED | FAILED …
  | 'productType'  // stablecoin | staking | liquidity | yield | perpetual | options
  | 'staking'      // active | paused | ended | upcoming
  | 'campaign'     // active | upcoming | ended
  | 'txType'       // BUY | SELL | STAKE | UNSTAKE | CLAIM …
  | 'severity';    // low | medium | high | critical

/**
 * Returns a translator for one enum family. Usage:
 *   const tokenStatus = useEnumLabel('token');
 *   <span>{tokenStatus(token?.status)}</span>
 */
export function useEnumLabel(family: EnumFamily): (raw?: string | null) => string {
  const t = useTranslations('statusEnums');
  return (raw?: string | null) => {
    if (!raw || !raw.trim()) {
      return t('none');
    }
    const key = `${family}.${raw.trim()}`;
    if (t.has(key)) {
      return t(key);
    }
    // Localized unknown-state copy; the raw code stays visible only as a
    // parenthesised technical detail.
    return t('unknown', { raw: raw.trim() });
  };
}
