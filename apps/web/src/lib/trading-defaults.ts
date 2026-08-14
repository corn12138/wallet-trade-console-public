'use client';

import { useCallback, useEffect, useState } from 'react';

/**
 * Browser-local trading defaults (slippage % + tx deadline minutes).
 *
 * These are deliberately local: the settings service has no slippage/deadline
 * fields, so instead of pretending to persist them server-side we store them in
 * localStorage and genuinely apply them to the Swap and Trade transaction
 * builders. The Settings page labels them as device-local.
 */
export const TRADING_DEFAULTS_STORAGE_KEY = 'atlas-x.trading-defaults.v1';

export const DEFAULT_SLIPPAGE_PERCENT = '0.5';
export const DEFAULT_DEADLINE_MINUTES = '20';

export interface TradingDefaults {
  slippagePercent: string;
  deadlineMinutes: string;
}

const FALLBACK: TradingDefaults = {
  slippagePercent: DEFAULT_SLIPPAGE_PERCENT,
  deadlineMinutes: DEFAULT_DEADLINE_MINUTES,
};

export function readTradingDefaults(): TradingDefaults {
  if (typeof window === 'undefined') return FALLBACK;
  try {
    const raw = window.localStorage.getItem(TRADING_DEFAULTS_STORAGE_KEY);
    if (!raw) return FALLBACK;
    const parsed = JSON.parse(raw) as Partial<TradingDefaults>;
    return {
      slippagePercent:
        typeof parsed.slippagePercent === 'string' && parsed.slippagePercent.trim() !== ''
          ? parsed.slippagePercent
          : FALLBACK.slippagePercent,
      deadlineMinutes:
        typeof parsed.deadlineMinutes === 'string' && parsed.deadlineMinutes.trim() !== ''
          ? parsed.deadlineMinutes
          : FALLBACK.deadlineMinutes,
    };
  } catch {
    return FALLBACK;
  }
}

function writeTradingDefaults(next: TradingDefaults) {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(TRADING_DEFAULTS_STORAGE_KEY, JSON.stringify(next));
    window.dispatchEvent(new CustomEvent(TRADING_DEFAULTS_EVENT, { detail: next }));
  } catch {
    // Private-mode storage failures degrade to in-memory defaults.
  }
}

const TRADING_DEFAULTS_EVENT = 'atlas-x:trading-defaults';

/**
 * React hook over the local trading defaults. Stays in sync across components
 * in the same tab (custom event) and across tabs (storage event).
 */
export function useTradingDefaults() {
  const [defaults, setDefaults] = useState<TradingDefaults>(FALLBACK);

  useEffect(() => {
    setDefaults(readTradingDefaults());

    const onLocalChange = (event: Event) => {
      const detail = (event as CustomEvent<TradingDefaults>).detail;
      if (detail) setDefaults(detail);
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key === TRADING_DEFAULTS_STORAGE_KEY) setDefaults(readTradingDefaults());
    };

    window.addEventListener(TRADING_DEFAULTS_EVENT, onLocalChange);
    window.addEventListener('storage', onStorage);
    return () => {
      window.removeEventListener(TRADING_DEFAULTS_EVENT, onLocalChange);
      window.removeEventListener('storage', onStorage);
    };
  }, []);

  const setSlippagePercent = useCallback((slippagePercent: string) => {
    setDefaults((prev) => {
      const next = { ...prev, slippagePercent };
      writeTradingDefaults(next);
      return next;
    });
  }, []);

  const setDeadlineMinutes = useCallback((deadlineMinutes: string) => {
    setDefaults((prev) => {
      const next = { ...prev, deadlineMinutes };
      writeTradingDefaults(next);
      return next;
    });
  }, []);

  return {
    slippagePercent: defaults.slippagePercent,
    deadlineMinutes: defaults.deadlineMinutes,
    setSlippagePercent,
    setDeadlineMinutes,
  };
}
