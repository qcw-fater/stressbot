import { describe, expect, it } from 'vitest';
import {
  RANDOM_STRING_CHARSET_ALIASES,
  randomStringCharsetLabel,
  resolveRandomStringCharset,
} from './randomStringCharset';

describe('randomStringCharset', () => {
  it('resolves known aliases', () => {
    expect(resolveRandomStringCharset('lower')).toBe(RANDOM_STRING_CHARSET_ALIASES.lower);
    expect(resolveRandomStringCharset('upper')).toBe(RANDOM_STRING_CHARSET_ALIASES.upper);
    expect(resolveRandomStringCharset('alpha')).toBe(RANDOM_STRING_CHARSET_ALIASES.alpha);
    expect(resolveRandomStringCharset('numeric')).toBe(RANDOM_STRING_CHARSET_ALIASES.numeric);
    expect(resolveRandomStringCharset('alphanum')).toBe(RANDOM_STRING_CHARSET_ALIASES.alphanum);
  });

  it('defaults empty charset to alphanum', () => {
    expect(resolveRandomStringCharset()).toBe(RANDOM_STRING_CHARSET_ALIASES.alphanum);
    expect(resolveRandomStringCharset('')).toBe(RANDOM_STRING_CHARSET_ALIASES.alphanum);
    expect(resolveRandomStringCharset('   ')).toBe(RANDOM_STRING_CHARSET_ALIASES.alphanum);
  });

  it('keeps unknown non-empty values as custom literal charsets', () => {
    expect(resolveRandomStringCharset('ABC-123_')).toBe('ABC-123_');
  });

  it('labels aliases and custom literals', () => {
    expect(randomStringCharsetLabel()).toBe('alphanum (default)');
    expect(randomStringCharsetLabel('upper')).toBe('upper alias');
    expect(randomStringCharsetLabel('ABC')).toBe('custom literal');
  });
});
