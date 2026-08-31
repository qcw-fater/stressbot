import { App as AntApp } from 'antd';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { HistoryRecord } from '@/types/api';
import { HistoryModal } from './HistoryModal';

const mocks = vi.hoisted(() => ({
  listHistory: vi.fn(),
  showApiError: vi.fn(),
}));

vi.mock('@/services', () => ({
  ApiError: class ApiError extends Error {
    code = 'TEST_ERROR';
  },
  historyApi: {
    listHistory: mocks.listHistory,
    updateHistory: vi.fn(),
    deleteHistory: vi.fn(),
  },
  showApiError: mocks.showApiError,
}));

vi.mock('@/components/FlowEditor/panels/FloatingWindow', () => ({
  FloatingWindow: ({
    open,
    title,
    extra,
    children,
  }: {
    open: boolean;
    title: ReactNode;
    extra?: ReactNode;
    children: ReactNode;
  }) =>
    open ? (
      <section>
        {title}
        {extra}
        {children}
      </section>
    ) : null,
}));

vi.mock('./HistoryDetailView', () => ({ HistoryDetailView: () => null }));
vi.mock('./HistoryCompareView', () => ({ HistoryCompareView: () => null }));

const record: HistoryRecord = {
  id: 'history-1',
  name: '第一页记录',
  state: 'stopped',
  totalBots: 10,
  agentCount: 1,
  activeAgentCount: 1,
  createdAt: '2026-08-31T00:00:00Z',
  startedAt: '2026-08-31T00:00:00Z',
  stoppedAt: '2026-08-31T00:01:00Z',
  durationSec: 60,
  starred: false,
  tags: [],
  debugMode: false,
  configSummary: {
    concurrency: 10,
    timeoutSec: 60,
    flowSizeKB: 1,
    protoCount: 1,
    scriptCount: 1,
  },
};

function renderModal() {
  return render(
    <AntApp>
      <HistoryModal open onClose={vi.fn()} />
    </AntApp>,
  );
}

beforeEach(() => {
  mocks.listHistory.mockResolvedValue({ total: 45, items: [record] });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('HistoryModal server pagination', () => {
  it('requests the first page from the server without a client-side record cap', async () => {
    renderModal();

    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(1));
    expect(mocks.listHistory.mock.calls[0][0]).toEqual({
      search: undefined,
      starred: undefined,
      limit: 20,
      offset: 0,
      includeStages: true,
    });
  });

  it('uses the server total and requests the selected page from the server', async () => {
    const user = userEvent.setup();
    renderModal();

    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(1));
    await user.click(await screen.findByTitle('2'));

    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(2));
    expect(mocks.listHistory.mock.calls[1][0]).toMatchObject({ limit: 20, offset: 20 });
  });

  it('keeps typing local and submits the search only when confirmed', async () => {
    const user = userEvent.setup();
    renderModal();
    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(1));

    const input = screen.getByPlaceholderText('名称、ID、标签或备注');
    await user.type(input, 'boss');
    await act(async () => Promise.resolve());
    expect(mocks.listHistory).toHaveBeenCalledTimes(1);

    await user.keyboard('{Enter}');
    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(2));
    expect(mocks.listHistory.mock.calls[1][0]).toMatchObject({ search: 'boss', offset: 0 });
  });

  it('aborts the previous page request when pagination changes', async () => {
    const user = userEvent.setup();
    renderModal();

    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(1));
    const firstSignal = mocks.listHistory.mock.calls[0][1]?.signal as AbortSignal | undefined;
    expect(firstSignal?.aborted).toBe(false);

    await user.click(await screen.findByTitle('2'));
    await waitFor(() => expect(mocks.listHistory).toHaveBeenCalledTimes(2));
    expect(firstSignal?.aborted).toBe(true);
  });
});
