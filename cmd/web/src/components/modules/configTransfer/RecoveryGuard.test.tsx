import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import type { RestoreResult } from '@/services/configTransfer/types';
import { RecoveryGuard } from './RecoveryGuard';

describe('RecoveryGuard', () => {
  it('automatically retries an unfinished rollback and stays hidden on success', async () => {
    const recover = vi.fn(async (): Promise<RestoreResult> => ({
      ok: true,
      stats: {},
      pendingSections: [],
      rolledBack: true,
    }));

    render(<RecoveryGuard recover={recover} />);

    await waitFor(() => expect(recover).toHaveBeenCalledTimes(1));
    expect(screen.queryByText('配置恢复未完成')).toBeNull();
  });

  it('keeps a persistent retry action while rollback is pending and clears after retry', async () => {
    const user = userEvent.setup();
    const recover = vi.fn()
      .mockResolvedValueOnce({
        ok: false,
        stats: {},
        pendingSections: ['flows'],
        rolledBack: false,
      } satisfies RestoreResult)
      .mockResolvedValueOnce({
        ok: true,
        stats: {},
        pendingSections: [],
        rolledBack: true,
      } satisfies RestoreResult);

    render(<RecoveryGuard recover={recover} />);

    expect(await screen.findByText('配置恢复未完成')).toBeTruthy();
    expect(screen.getByText(/已保存流程/)).toBeTruthy();
    await user.click(screen.getByRole('button', { name: '重试恢复' }));

    await waitFor(() => expect(recover).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByText('配置恢复未完成')).toBeNull());
  });
});
