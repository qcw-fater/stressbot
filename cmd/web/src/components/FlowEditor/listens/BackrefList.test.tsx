import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useFlowStore } from '../store/flowStore';
import { BackrefList } from './BackrefList';

const originalGetComputedStyle = window.getComputedStyle;
vi.spyOn(window, 'getComputedStyle').mockImplementation((element) =>
  originalGetComputedStyle.call(window, element),
);

vi.mock('../codec/useCodecConnections', () => ({
  useCodecConnections: () => ({
    connections: [{ conn: 'tcp:logic', protocol: 'tcp', service: 'logic' }],
    loading: false,
    error: null,
  }),
  useCodecRouteSpecs: () => ({
    specs: new Map([['tcp:logic', { routeKeyTemplate: '{cmd}:{act}' }]]),
    loading: false,
    error: null,
  }),
}));

describe('BackrefList', () => {
  beforeEach(() => {
    useFlowStore.setState({
      defaultDelayMs: 1000,
      nodes: {
        connect_logic: {
          type: 'action',
          action: 'connect_logic',
          listenRefs: [
            {
              server: 'tcp:logic',
              route: { cmd: 12, act: 3 },
              listen: 'battle_push',
            },
          ],
        },
      },
      actions: { connect_logic: { pattern: 'tcpConnect' } },
      listens: { battle_push: {} },
    });
  });

  it('edits the referenced action route through the compact summary', async () => {
    const { container } = render(<BackrefList listenName="battle_push" />);

    expect(screen.getByRole('columnheader', { name: 'route' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '编辑 route 字段 cmd' }).textContent).toBe('cmd=12');
    expect(screen.getByRole('button', { name: '在浮动窗口编辑 route' })).toBeTruthy();
    expect(container.querySelector('table')?.style.tableLayout).toBe('fixed');

    fireEvent.click(screen.getByRole('button', { name: '编辑 route 字段 cmd' }));
    const input = screen.getByLabelText('route cmd');
    fireEvent.change(input, { target: { value: '18' } });
    fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() => {
      expect(useFlowStore.getState().nodes.connect_logic.listenRefs?.[0].route).toEqual({
        cmd: 18,
        act: 3,
      });
    });
  });

  it('updates queue size on the referenced action listen ref', async () => {
    render(<BackrefList listenName="battle_push" />);

    expect(screen.getByRole('columnheader', { name: '队列容量' })).toBeTruthy();
    fireEvent.change(screen.getByLabelText('队列容量'), { target: { value: '128' } });

    await waitFor(() => {
      expect(useFlowStore.getState().nodes.connect_logic.listenRefs?.[0].queueSize).toBe(128);
    });
  });

  it('uses the same narrow target connection column as the action registration table', () => {
    const { container } = render(<BackrefList listenName="battle_push" />);

    const columnWidths = Array.from(container.querySelectorAll('colgroup col')).map(
      (column) => (column as HTMLElement).style.width,
    );
    expect(columnWidths).toEqual(['150px', '120px', '150px', '90px', '90px']);
  });
});
