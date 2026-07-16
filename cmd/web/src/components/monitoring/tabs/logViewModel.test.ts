import { describe, expect, it } from 'vitest';
import { filterLogEntries, planLogRender, type LogViewEntry } from './logViewModel';

const entry = (level: string, message: string): LogViewEntry => ({ level, message, text: message });

describe('log view model', () => {
  it('applies level and keyword filters in one pass', () => {
    const entries = [entry('info', 'connected'), entry('error', 'connection failed'), entry('error', 'timeout')];

    expect(filterLogEntries(entries, 'error', 'connection')).toEqual([entries[1]]);
  });

  it('returns only appended entries when the previous prefix is stable', () => {
    const first = entry('info', 'first');
    const second = entry('info', 'second');

    expect(planLogRender([first], [first, second], false)).toEqual({ kind: 'append', entries: [second] });
  });

  it('requests replacement after filtering or ring-buffer truncation', () => {
    const first = entry('info', 'first');
    const second = entry('info', 'second');

    expect(planLogRender([first], [first, second], true).kind).toBe('replace');
    expect(planLogRender([first, second], [second], false).kind).toBe('replace');
  });
});
