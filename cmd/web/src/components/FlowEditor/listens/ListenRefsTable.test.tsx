import { App as AntApp } from 'antd';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useFlowStore } from '../store/flowStore';
import { ListenRefsTable } from './ListenRefsTable';

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

describe('ListenRefsTable', () => {
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
              queueSize: 128,
            },
          ],
        },
      },
      actions: { connect_logic: { pattern: 'tcpConnect' } },
      listens: { battle_push: {} },
    });
  });

  it('uses a compact route summary column and edits the underlying listen ref', async () => {
    const { container } = render(
      <AntApp>
        <ListenRefsTable nodeId="connect_logic" />
      </AntApp>,
    );

    expect(screen.getByRole('columnheader', { name: '目标连接' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'route' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: '队列容量' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '编辑 route 字段 cmd' }).textContent).toBe('cmd=12');
    expect(screen.getByRole('button', { name: '编辑 route 字段 act' }).textContent).toBe('act=3');
    expect(screen.getByRole('button', { name: '在浮动窗口编辑 route' })).toBeTruthy();
    expect(screen.queryByLabelText('route cmd')).toBeNull();
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

  it('keeps compact controls inside the narrower registration columns', () => {
    const { container } = render(
      <AntApp>
        <ListenRefsTable nodeId="connect_logic" />
      </AntApp>,
    );

    const columnWidths = Array.from(container.querySelectorAll('colgroup col')).map(
      (column) => (column as HTMLElement).style.width,
    );
    expect(columnWidths).toEqual(['150px', '120px', '150px', '90px', '90px']);

    const listenSelect = screen.getByRole('combobox', { name: '监听' }).closest('.ant-select');
    const targetSelect = screen.getByRole('combobox', { name: '目标连接' }).closest('.ant-select');
    expect((listenSelect as HTMLElement | null)?.style.minWidth).toBe('0');
    expect((targetSelect as HTMLElement | null)?.style.minWidth).toBe('0');
  });
});
