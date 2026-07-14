/**
 * SetStateEditor 组件测试。
 *
 - 通过 vi.mock 把 useStateKeyOptions 替换为确定返回，避免依赖 IndexedDB / 流程图实时状态。
 - Harness 把 bindings 放进 React state，便于断言 onChange 的回写。
 */

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import type { FieldBind } from '@/types/action';

vi.mock('./useStateKeyOptions', () => ({
  useStateKeyOptions: vi.fn(() => ({
    keys: [
      { key: 'matchInfo', sourceType: 'store', sourceName: 'test' },
      { key: 'rankedMatchStarted', sourceType: 'store', sourceName: 'test' },
      // 内置 key 计为「已存在」
      { key: 'id', sourceType: 'builtin', sourceName: '内置' },
      { key: 'index', sourceType: 'builtin', sourceName: '内置' },
      { key: 'account', sourceType: 'builtin', sourceName: '内置' },
    ],
    ready: true,
  })),
}));

import { SetStateEditor } from './SetStateEditor';

function Harness({ initial }: { initial: FieldBind[] }) {
  const [bindings, setBindings] = useState<FieldBind[]>(initial);
  return <SetStateEditor value={bindings} onChange={setBindings} />;
}

describe('SetStateEditor', () => {
  it('摘要显示目标状态、取值方式和值', () => {
    render(
      <Harness
        initial={[{ field: 'battleId', type: 'state', source: 'matchInfo', path: 'id' }]}
      />,
    );
    expect(screen.getByText('battleId')).toBeTruthy();
    expect(screen.getByText('state')).toBeTruthy();
    expect(screen.getByText('matchInfo.id')).toBeTruthy();
  });

  it('添加状态创建空目标的 fixed binding', async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<SetStateEditor value={[]} onChange={onChange} />);
    await user.click(screen.getByRole('button', { name: /添加状态/ }));
    expect(onChange).toHaveBeenLastCalledWith([{ type: 'fixed', field: '' }]);
  });

  it('可输入候选中不存在的新状态名称', async () => {
    const user = userEvent.setup();
    render(<Harness initial={[{ field: '', type: 'fixed', value: 1 }]} />);
    // 点摘要标签展开当前卡片
    await user.click(screen.getByText('(未指定目标状态)'));
    // AntD AutoComplete 把 placeholder 渲染成 <span>（而非 input 的 placeholder 属性），
    // getByPlaceholderText 在 jsdom 下取不到，这里通过 placeholder span 定位到所属
    // .ant-select，再取其内部 combobox <input> 来模拟键盘输入。
    const placeholderSpan = screen.getByText('选择已有状态或输入新名称');
    const selectRoot = placeholderSpan.closest('.ant-select');
    expect(selectRoot).toBeTruthy();
    const target = selectRoot!.querySelector('input')!;
    await user.type(target, 'newBattleState');
    expect(screen.getByText('新状态')).toBeTruthy();
  });

  it('已配置高级字段时显示标签和配置数量', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        initial={[
          {
            field: 'battleId',
            type: 'state',
            source: 'matchInfo',
            path: 'id',
            required: true,
            condition: 'state:ready',
          },
        ]}
      />,
    );
    // required 标签在折叠摘要里始终渲染
    expect(screen.getByText('required')).toBeTruthy();
    // 展开后才能看到嵌套「高级设置（N）」折叠标题
    await user.click(screen.getByText('battleId'));
    expect(screen.getByText(/高级设置（3）/)).toBeTruthy();
  });
});
