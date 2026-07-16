import { render } from '@testing-library/react';
import { useEffect } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { FloatingWindow } from './FloatingWindow';
import { useFloatingWindowStore } from '../store/floatingWindowStore';

vi.mock('react-rnd', () => ({
  Rnd: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

describe('FloatingWindow lifecycle', () => {
  beforeEach(() => {
    useFloatingWindowStore.setState({
      windows: {
        test: {
          id: 'test',
          position: { x: 0, y: 0 },
          size: { width: 400, height: 300 },
          zIndex: 1001,
        },
      },
      _nextZ: 1001,
    });
  });

  it('unmounts heavy children when closed by default', () => {
    const cleanup = vi.fn();
    function Child() {
      useEffect(() => cleanup, []);
      return <div>content</div>;
    }
    const view = render(
      <FloatingWindow windowId="test" title="test" defaultSize={{ width: 400, height: 300 }} open onClose={vi.fn()}>
        <Child />
      </FloatingWindow>,
    );

    view.rerender(
      <FloatingWindow windowId="test" title="test" defaultSize={{ width: 400, height: 300 }} open={false} onClose={vi.fn()}>
        <Child />
      </FloatingWindow>,
    );

    expect(cleanup).toHaveBeenCalledOnce();
  });

  it('can explicitly keep draft editors mounted', () => {
    const cleanup = vi.fn();
    function Child() {
      useEffect(() => cleanup, []);
      return <div>draft</div>;
    }
    const view = render(
      <FloatingWindow keepMounted windowId="test" title="test" defaultSize={{ width: 400, height: 300 }} open onClose={vi.fn()}>
        <Child />
      </FloatingWindow>,
    );

    view.rerender(
      <FloatingWindow keepMounted windowId="test" title="test" defaultSize={{ width: 400, height: 300 }} open={false} onClose={vi.fn()}>
        <Child />
      </FloatingWindow>,
    );

    expect(cleanup).not.toHaveBeenCalled();
  });
});
