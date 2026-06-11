export const RANDOM_STRING_CHARSET_ALIASES = {
  lower: 'abcdefghijklmnopqrstuvwxyz',
  upper: 'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
  alpha: 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ',
  numeric: '0123456789',
  alphanum: 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789',
} as const;

export type RandomStringCharsetAlias = keyof typeof RANDOM_STRING_CHARSET_ALIASES;

export const DEFAULT_RANDOM_STRING_CHARSET_ALIAS: RandomStringCharsetAlias = 'alphanum';

export const RANDOM_STRING_CHARSET_OPTIONS: Array<{ value: RandomStringCharsetAlias; label: string }> = [
  { value: 'lower', label: 'lower (a-z)' },
  { value: 'upper', label: 'upper (A-Z)' },
  { value: 'alpha', label: 'alpha (a-zA-Z)' },
  { value: 'numeric', label: 'numeric (0-9)' },
  { value: 'alphanum', label: 'alphanum (a-zA-Z0-9)' },
];

export function isKnownRandomStringCharsetAlias(charset?: string): charset is RandomStringCharsetAlias {
  if (!charset) return false;
  return Object.prototype.hasOwnProperty.call(RANDOM_STRING_CHARSET_ALIASES, charset);
}

export function resolveRandomStringCharset(charset?: string): string {
  const normalized = charset?.trim();
  if (!normalized) return RANDOM_STRING_CHARSET_ALIASES[DEFAULT_RANDOM_STRING_CHARSET_ALIAS];
  if (isKnownRandomStringCharsetAlias(normalized)) return RANDOM_STRING_CHARSET_ALIASES[normalized];
  return normalized;
}

export function randomStringCharsetLabel(charset?: string): string {
  const normalized = charset?.trim();
  if (!normalized) return `${DEFAULT_RANDOM_STRING_CHARSET_ALIAS} (default)`;
  if (isKnownRandomStringCharsetAlias(normalized)) return `${normalized} alias`;
  return 'custom literal';
}
