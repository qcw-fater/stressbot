import { fireEvent, render } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { JsonDraftInput } from './JsonDraftInput';

describe('JsonDraftInput interactions', () => {
  it('keeps an incomplete JSON draft across a parent rerender and blur', async () => {
    const onChange = vi.fn();
    const view = render(
      <JsonDraftInput value={{ saved: 1 }} onChange={onChange} mode="json" />,
    );
    const input = view.getByRole('textbox') as HTMLInputElement;

    fireEvent.change(input, { target: { value: '{' } });
    view.rerender(
      <JsonDraftInput value={{ external: 2 }} onChange={onChange} mode="json" />,
    );
    input.blur();

    expect(input.value).toBe('{');
    expect(input.className).toContain('ant-input-status-error');
  });

  it('commits structured data once an incomplete draft becomes valid', async () => {
    const onChange = vi.fn();
    const view = render(
      <JsonDraftInput value={undefined} onChange={onChange} mode="json" />,
    );
    const input = view.getByRole('textbox') as HTMLInputElement;

    fireEvent.change(input, { target: { value: '{' } });
    fireEvent.change(input, { target: { value: '{"a":1}' } });

    expect(onChange).toHaveBeenLastCalledWith({ a: 1 });
    expect(input.value).toBe('{"a":1}');
  });
});
