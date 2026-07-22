import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useFloatingWindowStore } from '../store/floatingWindowStore';
import { RouteFloatingEditor } from './RouteFloatingEditor';

vi.mock('react-rnd', () => ({
  Rnd: ({
    children,
    minWidth,
    minHeight,
    position,
    size,
  }: {
    children: React.ReactNode;
    minWidth: number;
    minHeight: number;
    position: { x: number; y: number };
    size: { width: number; height: number };
  }) => (
    <div
      data-min-height={minHeight}
      data-min-width={minWidth}
      data-position={`${position.x},${position.y}`}
      data-size={`${size.width}x${size.height}`}
    >
      {children}
    </div>
  ),
}));

describe('RouteFloatingEditor', () => {
  beforeEach(() => {
    useFloatingWindowStore.setState({
      windows: {
        parent: {
          id: 'parent',
          position: { x: 20, y: 20 },
          size: { width: 640, height: 500 },
          zIndex: 1001,
        },
      },
      _nextZ: 1001,
    });
  });

  it('opens a thin non-modal horizontal editor and updates fields immediately', async () => {
    const onChange = vi.fn();
    render(
      <div data-testid="route-editor-host">
        <RouteFloatingEditor
          windowId="route-test"
          open
          value={{ cmd: 12, act: 3 }}
          server="tcp:logic"
          routeKeyTemplate="{cmd}:{act}"
          onChange={onChange}
          onClose={vi.fn()}
        />
      </div>,
    );

    expect(await screen.findByText('编辑 route')).toBeTruthy();
    expect(screen.getByLabelText('route cmd')).toBeTruthy();
    expect(screen.getByLabelText('route act')).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByRole('button', { name: '保存' })).toBeNull();
    expect(
      screen.getByTestId('route-editor-host').querySelector('.route-floating-editor'),
    ).toBeNull();
    expect(document.body.querySelector('.route-floating-editor')).toBeTruthy();

    const shell = document.body.querySelector('[data-size]');
    expect(shell?.getAttribute('data-size')).toBe('560x112');
    expect(shell?.getAttribute('data-min-width')).toBe('360');
    expect(shell?.getAttribute('data-min-height')).toBe('96');
    expect(shell?.getAttribute('data-position')).toBe(
      `${Math.max(0, (window.innerWidth - 560) / 2)},${Math.max(0, (window.innerHeight - 112) / 2)}`,
    );

    fireEvent.change(screen.getByLabelText('route cmd'), { target: { value: '18' } });
    expect(onChange).toHaveBeenCalledWith({ cmd: 18, act: 3 });
  });
});
