import { describe, expect, it } from 'vitest';
import { formatJsonDraftValue, parseJsonDraftValue } from './JsonDraftInput';

describe('JsonDraftInput helpers', () => {
  it('parses JSON arrays in jsonArray mode', () => {
    expect(parseJsonDraftValue('[1,2,3]', 'jsonArray')).toEqual({ ok: true, value: [1, 2, 3] });
  });

  it('rejects incomplete and non-array JSON in jsonArray mode', () => {
    expect(parseJsonDraftValue('[1,2', 'jsonArray')).toEqual({ ok: false });
    expect(parseJsonDraftValue('{"a":1}', 'jsonArray')).toEqual({ ok: false });
  });

  it('parses JSON and empty values in json mode', () => {
    expect(parseJsonDraftValue('{"a":1}', 'json', undefined)).toEqual({ ok: true, value: { a: 1 } });
    expect(parseJsonDraftValue('', 'json', undefined)).toEqual({ ok: true, value: undefined });
  });

  it('parses JSON literals and leaves invalid text unparsed in jsonOrString mode', () => {
    expect(parseJsonDraftValue('{"a":1}', 'jsonOrString')).toEqual({ ok: true, value: { a: 1 } });
    expect(parseJsonDraftValue('5', 'jsonOrString')).toEqual({ ok: true, value: 5 });
    expect(parseJsonDraftValue('true', 'jsonOrString')).toEqual({ ok: true, value: true });
    expect(parseJsonDraftValue('hello', 'jsonOrString')).toEqual({ ok: false });
  });

  it('formats structured values as JSON instead of object strings', () => {
    expect(formatJsonDraftValue({ a: 1 }, 'jsonOrString')).toBe('{"a":1}');
    expect(formatJsonDraftValue([1, 2], 'jsonOrString')).toBe('[1,2]');
    expect(formatJsonDraftValue('hello', 'jsonOrString')).toBe('hello');
  });
});
