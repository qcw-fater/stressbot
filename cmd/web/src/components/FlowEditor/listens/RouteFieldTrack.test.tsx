import { fireEvent, render, screen } from '@testing-library/react';
import { readFileSync } from 'node:fs';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { FloatingWindow } from '../panels/FloatingWindow';
import { useFloatingWindowStore } from '../store/floatingWindowStore';
import { RouteFieldTrack } from './RouteFieldTrack';

const routeFieldTrackCss = readFileSync(
  'src/components/FlowEditor/listens/RouteFieldTrack.css',
  'utf8',
);

vi.mock('react-rnd', () => ({
  Rnd: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

describe('RouteFieldTrack', () => {
  beforeEach(() => {
    useFloatingWindowStore.setState({
      windows: {
        'route-track-parent': {
          id: 'route-track-parent',
          position: { x: 0, y: 0 },
          size: { width: 640, height: 500 },
          zIndex: 1001,
        },
      },
      _nextZ: 1001,
    });
  });

  it('shows route fields as text and edits only the clicked value', () => {
    const onChange = vi.fn();

    render(
      <RouteFieldTrack
        value={{ cmd: 12, act: 3 }}
        server="tcp:logic"
        routeKeyTemplate="{cmd}:{act}"
        onChange={onChange}
        onOpenFloating={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: '编辑 route 字段 cmd' }).textContent).toBe('cmd=12');
    expect(screen.getByRole('button', { name: '编辑 route 字段 act' }).textContent).toBe('act=3');
    expect(screen.queryByLabelText('route cmd')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: '编辑 route 字段 cmd' }));

    const input = screen.getByLabelText('route cmd');
    expect((input as HTMLInputElement).value).toBe('12');
    expect(input.classList.contains('ant-input')).toBe(false);
    expect(input.parentElement?.classList.contains('route-field-track__value')).toBe(true);
    expect(input.previousSibling?.textContent).toBe('cmd=');
    expect((input as HTMLInputElement).selectionStart).toBe(2);
    expect((input as HTMLInputElement).selectionEnd).toBe(2);
    expect(screen.queryByLabelText('route act')).toBeNull();

    fireEvent.change(input, { target: { value: '18' } });
    fireEvent.keyDown(input, { key: 'Enter' });

    expect(onChange).toHaveBeenCalledWith({ cmd: 18, act: 3 });
  });

  it('uses a subtle tint and two-pixel underline to emphasize the editing state', () => {
    expect(routeFieldTrackCss).toContain('var(--color-blue) 12%');
    expect(routeFieldTrackCss).toContain(
      'box-shadow: inset 0 -2px 0 color-mix(in srgb, var(--color-blue) 55%, transparent)',
    );
    expect(routeFieldTrackCss).toContain('var(--color-error) 12%');
  });

  it('cancels edits and exposes a separate floating-editor button', () => {
    const onOpenFloating = vi.fn();

    render(
      <RouteFieldTrack
        value={{ cmd: 12, act: 3 }}
        server="tcp:logic"
        routeKeyTemplate="{cmd}:{act}"
        onChange={vi.fn()}
        onOpenFloating={onOpenFloating}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: '编辑 route 字段 cmd' }));
    const input = screen.getByLabelText('route cmd');
    fireEvent.change(input, { target: { value: '99' } });
    fireEvent.keyDown(input, { key: 'Escape' });

    expect(screen.queryByLabelText('route cmd')).toBeNull();
    expect(screen.getByRole('button', { name: '编辑 route 字段 cmd' }).textContent).toBe('cmd=12');

    fireEvent.click(screen.getByRole('button', { name: '在浮动窗口编辑 route' }));
    expect(onOpenFloating).toHaveBeenCalledOnce();
  });

  it('cancels a field edit without closing its parent floating window', () => {
    const onClose = vi.fn();

    render(
      <FloatingWindow
        windowId="route-track-parent"
        title="parent"
        defaultSize={{ width: 640, height: 500 }}
        open
        onClose={onClose}
      >
        <RouteFieldTrack
          value={{ cmd: 12, act: 3 }}
          server="tcp:logic"
          routeKeyTemplate="{cmd}:{act}"
          onChange={vi.fn()}
          onOpenFloating={vi.fn()}
        />
      </FloatingWindow>,
    );

    fireEvent.click(screen.getByRole('button', { name: '编辑 route 字段 cmd' }));
    fireEvent.keyDown(screen.getByLabelText('route cmd'), { key: 'Escape' });

    expect(onClose).not.toHaveBeenCalled();
    expect(screen.queryByLabelText('route cmd')).toBeNull();
  });
});
