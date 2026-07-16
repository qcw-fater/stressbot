import { render } from '@testing-library/react';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';
import { BaselineSyncModal } from './BaselineSyncModal';
import type { BaselineSyncResult } from '@/services/resourcesStore';

vi.mock('@monaco-editor/react', () => ({
  DiffEditor: () => <div data-testid="diff-editor" />,
}));

const nativeGetComputedStyle = window.getComputedStyle.bind(window);
let getComputedStyleSpy: ReturnType<typeof vi.spyOn>;

beforeAll(() => {
  getComputedStyleSpy = vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => (
    nativeGetComputedStyle(element)
  ));
});

afterAll(() => getComputedStyleSpy.mockRestore());

const empty: BaselineSyncResult = {
  added: [],
  unchanged: [],
  conflicts: [],
  removed: [],
};

const conflicted: BaselineSyncResult = {
  ...empty,
  conflicts: [{
    type: 'script',
    name: 'demo.lua',
    localContent: 'return 1',
    baselineContent: 'return 2',
  }],
};

describe('BaselineSyncModal', () => {
  it('keeps a stable Hook order when conflicts appear after an empty result', () => {
    const onClose = vi.fn();
    const view = render(<BaselineSyncModal open result={empty} onClose={onClose} />);

    expect(() => {
      view.rerender(<BaselineSyncModal open result={conflicted} onClose={onClose} />);
    }).not.toThrow();
  });
});
