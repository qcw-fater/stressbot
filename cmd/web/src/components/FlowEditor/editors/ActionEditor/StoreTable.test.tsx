import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import type { StoreMapping } from '@/types/action';
import { StoreTable } from './StoreTable';

vi.mock('./useStateKeyOptions', () => ({
  useStateKeyOptions: () => ({
    keys: [
      { key: 'loginResp', sourceType: 'store', sourceName: 'login' },
      { key: 'battleId', sourceType: 'setState', sourceName: 'setBattle' },
    ],
    ready: true,
  }),
}));

describe('StoreTable', () => {
  it('渲染 setter 行并在输入 setter 时回调', async () => {
    const calls: StoreMapping[][] = [];
    function Harness() {
      const [val, setVal] = useState<StoreMapping[]>([{ field: 'token', setter: 'loginResp' }]);
      return (
        <StoreTable
          s2cProto="X.S2C"
          value={val}
          onChange={(next) => { setVal(next); calls.push(next); }}
        />
      );
    }
    render(<Harness />);

    // Collapse 默认折叠，setter 输入框在面板 children 内 —— 先展开面板
    await userEvent.click(screen.getByText('token'));
    // setter 输入框存在并可编辑（证明数据 props 移除后仍正常渲染）
    const setterInput = await screen.findByDisplayValue('loginResp');
    await userEvent.type(setterInput, '.token');
    expect(calls.length).toBeGreaterThan(0);
    expect(calls[calls.length - 1]).toEqual(
      [expect.objectContaining({ field: 'token', setter: 'loginResp.token' })],
    );
  });
});
