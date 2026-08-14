import { describe, expect, it } from 'vitest';
import en from '../../../../../services/api-go/internal/i18n/baseline/en.json';
import zh from '../../../../../services/api-go/internal/i18n/baseline/zh.json';
import {
  diagnosticMessageKey,
  diagnosticValueKey,
  formatDiagnosticMessage,
  formatDiagnosticValue,
  formatStatusWarning,
  KNOWN_STATUS_CODE_KEYS,
  statusWarningKey,
  type DiagnosticTranslator,
} from './diagnostics';

/**
 * The diagnostics layer is the contract that keeps raw English API
 * warnings/notes/enums out of Chinese-mode primary copy:
 *  - every known message/value key must exist in BOTH locale files;
 *  - known strings render the locale's translation (zh copy must not contain
 *    the raw English message);
 *  - unknown strings never become primary zh copy — they degrade to the
 *    localized "unrecognized diagnostic" line + labelled raw detail.
 */

type Messages = { diagnostics: Record<string, unknown> };

function translatorFor(messages: Messages): DiagnosticTranslator {
  return (key) => {
    let node: unknown = messages.diagnostics;
    for (const part of key.split('.')) {
      if (typeof node !== 'object' || node === null || !(part in node)) {
        throw new Error(`missing diagnostics key: ${key}`);
      }
      node = (node as Record<string, unknown>)[part];
    }
    if (typeof node !== 'string') throw new Error(`non-string diagnostics key: ${key}`);
    return node;
  };
}

const tEn = translatorFor(en as unknown as Messages);
const tZh = translatorFor(zh as unknown as Messages);

const KNOWN_MESSAGES = [
  'Live quote unavailable, using fallback estimate',
  'Simulated estimate - execution unavailable (no bridge adapter deployed)',
  'Simulated estimate — execution unavailable (no bridge adapter deployed)',
  'Destination gas top-up suggested',
];

const KNOWN_VALUES = [
  'fallback',
  'router',
  'live',
  'display-only',
  'executable',
  'not executable',
  'simulated',
  'managed',
  'standard',
  'low',
  'medium',
  'high',
  'critical',
  'true',
  'false',
  'watching',
  'linked',
  'watch-only',
  'authenticated',
];

describe('diagnostics message localization', () => {
  it('maps every required known API message in both locales', () => {
    for (const raw of KNOWN_MESSAGES) {
      const key = diagnosticMessageKey(raw);
      expect(key, raw).not.toBeNull();
      // Both locale files must carry the key (throws if missing).
      expect(tEn(`messages.${key}`)).toBeTruthy();
      expect(tZh(`messages.${key}`)).toBeTruthy();
    }
  });

  it('renders the swap fallback warning in Chinese without the raw English', () => {
    const display = formatDiagnosticMessage(
      tZh,
      'zh',
      'Live quote unavailable, using fallback estimate',
    );
    expect(display.known).toBe(true);
    expect(display.text).toContain('兜底估算');
    expect(display.text).not.toContain('Live quote unavailable');
    expect(display.detail).toBeNull();
  });

  it('matches the bridge note across hyphen and em-dash variants', () => {
    for (const raw of [
      'Simulated estimate - execution unavailable (no bridge adapter deployed)',
      'Simulated estimate — execution unavailable (no bridge adapter deployed)',
    ]) {
      const display = formatDiagnosticMessage(tZh, 'zh', raw);
      expect(display.known).toBe(true);
      expect(display.text).toContain('适配器');
      expect(display.text).not.toContain('execution unavailable');
    }
  });

  it('keeps English mode equivalent to the raw API copy for known warnings', () => {
    const display = formatDiagnosticMessage(
      tEn,
      'en',
      'Live quote unavailable, using fallback estimate',
    );
    expect(display.text).toBe('Live quote unavailable, using fallback estimate');
  });

  it('never promotes an unknown English diagnostic to primary Chinese copy', () => {
    const raw = 'Router inventory rebalancing in progress';
    const display = formatDiagnosticMessage(tZh, 'zh', raw);
    expect(display.known).toBe(false);
    expect(display.text).toBe(tZh('unknownMessage'));
    expect(display.text).not.toContain('Router inventory');
    expect(display.detail).toBe(raw);
  });

  it('shows unknown diagnostics raw in English mode (no information loss)', () => {
    const raw = 'Router inventory rebalancing in progress';
    const display = formatDiagnosticMessage(tEn, 'en', raw);
    expect(display.text).toBe(raw);
    expect(display.detail).toBeNull();
  });
});

describe('diagnostics value localization', () => {
  it('maps every required enum/boolean value in both locales', () => {
    for (const raw of KNOWN_VALUES) {
      const key = diagnosticValueKey(raw);
      expect(key, raw).not.toBeNull();
      expect(tEn(`values.${key}`)).toBeTruthy();
      expect(tZh(`values.${key}`)).toBeTruthy();
    }
  });

  it('localizes user-facing states into Chinese', () => {
    expect(formatDiagnosticValue(tZh, 'fallback')).toBe('兜底估算');
    expect(formatDiagnosticValue(tZh, 'managed')).toBe('托管');
    expect(formatDiagnosticValue(tZh, 'simulated')).toBe('仿真');
    expect(formatDiagnosticValue(tZh, true)).toBe('是');
    expect(formatDiagnosticValue(tZh, false)).toBe('否');
    expect(formatDiagnosticValue(tZh, 'watch-only')).toBe('仅观察');
  });

  it('keeps English display values equal to the machine values', () => {
    expect(formatDiagnosticValue(tEn, 'fallback')).toBe('fallback');
    expect(formatDiagnosticValue(tEn, 'router')).toBe('router');
    expect(formatDiagnosticValue(tEn, false)).toBe('false');
  });

  it('passes unknown metadata values through unchanged', () => {
    expect(formatDiagnosticValue(tEn, 'hop-v3')).toBe('hop-v3');
    expect(formatDiagnosticValue(tZh, 'hop-v3')).toBe('hop-v3');
    expect(formatDiagnosticValue(tZh, null)).toBe('—');
    expect(formatDiagnosticValue(tZh, undefined)).toBe('—');
  });
});

describe('product-status warning localization', () => {
  it('maps every product-status code in both locales', () => {
    for (const key of KNOWN_STATUS_CODE_KEYS) {
      expect(tEn(`status.${key}`)).toBeTruthy();
      expect(tZh(`status.${key}`)).toBeTruthy();
    }
  });

  it('renders a known status code in Chinese without raw English', () => {
    // MEDIA_UNCONFIGURED → localized copy, not the English API message.
    const display = formatStatusWarning(
      tZh,
      'zh',
      'MEDIA_UNCONFIGURED',
      'Media storage is not configured; uploads return 503.',
    );
    expect(display.known).toBe(true);
    expect(display.text).toContain('媒体存储');
    expect(display.text).not.toContain('Media storage is not configured');
    expect(display.detail).toBeNull();
  });

  it('never promotes an unknown status code to primary Chinese copy', () => {
    expect(statusWarningKey('SOME_FUTURE_CODE')).toBeNull();
    const display = formatStatusWarning(tZh, 'zh', 'SOME_FUTURE_CODE', 'A brand new warning');
    expect(display.known).toBe(false);
    expect(display.text).toBe(tZh('unknownMessage'));
    expect(display.text).not.toContain('A brand new warning');
    expect(display.detail).toBe('A brand new warning');
  });

  it('keeps English mode equal to the raw API message for unknown codes', () => {
    const display = formatStatusWarning(tEn, 'en', 'SOME_FUTURE_CODE', 'A brand new warning');
    expect(display.text).toBe('A brand new warning');
    expect(display.detail).toBeNull();
  });
});
