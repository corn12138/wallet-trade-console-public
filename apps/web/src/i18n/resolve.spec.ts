import { describe, expect, it } from 'vitest';
import {
  parseAcceptLanguage,
  resolveLocale,
  sanitizeReturnPath,
  validateMessagesShape,
} from './resolve';

const ENABLED = [
  { code: 'en', isDefault: true },
  { code: 'zh', isDefault: false },
];

describe('resolveLocale (server-side contract)', () => {
  it('prefers a validated locale cookie', () => {
    expect(resolveLocale('zh', 'en-US,en;q=0.9', ENABLED)).toBe('zh');
    expect(resolveLocale('ZH', undefined, ENABLED)).toBe('zh'); // normalized
  });

  it('rejects a disabled/unknown cookie and falls to Accept-Language', () => {
    expect(resolveLocale('fr', 'zh-CN,zh;q=0.9,en;q=0.5', ENABLED)).toBe('zh');
    expect(resolveLocale('tlh', 'zh-CN', ENABLED)).toBe('zh'); // region maps to base
  });

  it('uses the enabled default when nothing matches', () => {
    expect(resolveLocale(undefined, 'fr-FR,de;q=0.8', ENABLED)).toBe('en');
    expect(resolveLocale(undefined, undefined, ENABLED)).toBe('en');
  });

  it("falls back to 'en' only when metadata has no default (bootstrap)", () => {
    expect(resolveLocale(undefined, undefined, [{ code: 'zh', isDefault: false }])).toBe('en');
  });

  it('orders Accept-Language by q-value', () => {
    expect(parseAcceptLanguage('en;q=0.5, zh-CN;q=0.9, *;q=0.1')).toEqual(['zh-cn', 'en']);
    expect(parseAcceptLanguage('zh, en;q=0')).toEqual(['zh']);
  });
});

describe('sanitizeReturnPath (open-redirect guard)', () => {
  it('keeps same-origin absolute paths with query', () => {
    expect(sanitizeReturnPath('/trade?tab=1')).toBe('/trade?tab=1');
    expect(sanitizeReturnPath('/')).toBe('/');
  });

  it('blocks external and protocol-relative URLs', () => {
    expect(sanitizeReturnPath('https://evil.example/phish')).toBe('/');
    expect(sanitizeReturnPath('//evil.example')).toBe('/');
    expect(sanitizeReturnPath('javascript:alert(1)')).toBe('/');
    expect(sanitizeReturnPath('/\\evil')).toBe('/');
    expect(sanitizeReturnPath('')).toBe('/');
  });
});

describe('validateMessagesShape (malformed catalog gate)', () => {
  it('accepts a namespaced object', () => {
    expect(validateMessagesShape({ nav: { home: 'Home' } })).toBeTruthy();
  });
  it('rejects arrays, null, primitives and empty catalogs', () => {
    expect(() => validateMessagesShape(null)).toThrow();
    expect(() => validateMessagesShape([])).toThrow();
    expect(() => validateMessagesShape('x')).toThrow();
    expect(() => validateMessagesShape({})).toThrow();
  });
});
