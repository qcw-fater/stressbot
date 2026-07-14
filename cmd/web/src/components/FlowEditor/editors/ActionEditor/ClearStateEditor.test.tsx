/**
 * ClearStateEditor 组件测试。
 *
 * 通过 vi.mock 把 useStateKeyOptions 替换为确定返回，避免依赖 IndexedDB / 流程图实时状态。
 * 覆盖核心安全不变量：
 *   - 内置 key（id/index/account）可见但禁用，且点击不可选中
 *   - 批量选择保持顺序、去重
 *   - 导入的未知 key 保留并标注「当前流程未识别」，可单独移除，但不出现在候选项中
 *   - ready=false 时不标记未知、提示加载中
 *   - 无非内置候选时提示「当前流程没有可清除的状态」
 *
 * jsdom + AntD Select 注意事项：
 *   - 候选项真实 DOM 是 .ant-select-item-option（本版本未带 role/aria-disabled），
 *     禁用通过类名 ant-select-item-option-disabled 标记。因此禁用断言走类名 + 行为
 *     （点击禁用项不应触发 onChange），比依赖 aria-disabled 更稳。
 *   - 下拉打开后多次选择不会自动关闭，故顺序测试无需重复点击 combobox。
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import type { StateKeyInfo } from './stateRegistry';

vi.mock('./useStateKeyOptions', () => ({
  useStateKeyOptions: vi.fn(),
}));

import { useStateKeyOptions } from './useStateKeyOptions';
import { ClearStateEditor } from './ClearStateEditor';

const mocked = vi.mocked(useStateKeyOptions);

const DEFAULT_KEYS: StateKeyInfo[] = [
  { key: 'id', sourceType: 'builtin', sourceName: '内置', builtinType: 'int' },
  { key: 'index', sourceType: 'builtin', sourceName: '内置', builtinType: 'int' },
  { key: 'account', sourceType: 'builtin', sourceName: '内置', builtinType: 'string' },
  { key: 'battleId', sourceType: 'store', sourceName: 'loginResp', s2cProto: 'Game.LoginResp' },
  { key: 'battleSession', sourceType: 'store', sourceName: 'matchResp', s2cProto: 'Game.MatchResp' },
];

describe('ClearStateEditor', () => {
  beforeEach(() => {
    mocked.mockReturnValue({ keys: DEFAULT_KEYS, ready: true });
  });

  /** 受控多选 Harness：用 state 回写 value，同时把每次 onChange 透传给 spy 便于断言顺序。 */
  function SelectionHarness({ spy }: { spy: (keys: string[]) => void }) {
    const [value, setValue] = useState<string[]>([]);
    return <ClearStateEditor value={value} onChange={(next) => { setValue(next); spy(next); }} />;
  }

  it.each(['id', 'index', 'account'])('内置状态 %s 可见但禁用且不可选中', async (key) => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<ClearStateEditor value={[]} onChange={onChange} />);
    await user.click(screen.getByRole('combobox'));
    // 通过稳定的 title 包装定位真实候选项，断言其携带禁用类名
    const opt = screen.getByTitle(key).closest('.ant-select-item-option');
    expect(opt?.className).toMatch(/ant-select-item-option-disabled/);
    // 点击禁用项不应触发选中
    await user.click(screen.getByTitle(key));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('内置禁用项标注「内置状态不可清除」', async () => {
    const user = userEvent.setup();
    render(<ClearStateEditor value={[]} onChange={vi.fn()} />);
    await user.click(screen.getByRole('combobox'));
    expect(screen.getAllByText('内置状态不可清除').length).toBe(3);
  });

  it('批量选择保持选择顺序且不产生重复项', async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<SelectionHarness spy={onChange} />);
    await user.click(screen.getByRole('combobox'));
    // 多选下拉打开后不会因选择而关闭，直接连续点击两个候选项
    await user.click(await screen.findByTitle('battleSession'));
    await user.click(await screen.findByTitle('battleId'));
    expect(onChange).toHaveBeenLastCalledWith(['battleSession', 'battleId']);
  });

  it('保留并标记导入的未知 key', () => {
    render(<ClearStateEditor value={['legacyBattle']} onChange={vi.fn()} />);
    expect(screen.getByText('legacyBattle')).toBeTruthy();
    expect(screen.getByText('当前流程未识别')).toBeTruthy();
  });

  it('未知 key 可移除但不出现在候选项中', async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<ClearStateEditor value={['legacyBattle']} onChange={onChange} />);
    await user.click(screen.getByLabelText('移除 legacyBattle'));
    expect(onChange).toHaveBeenLastCalledWith([]);
    await user.click(screen.getByRole('combobox'));
    // 直接断言真实候选项文本中不含未知 key
    const optionTexts = Array.from(document.querySelectorAll('.ant-select-item-option'))
      .map((o) => o.textContent || '');
    expect(optionTexts.some((t) => t.includes('legacyBattle'))).toBe(false);
  });

  it('ready=false 时不标记未知并提示加载中', () => {
    mocked.mockReturnValue({ keys: DEFAULT_KEYS, ready: false });
    render(<ClearStateEditor value={['legacyBattle']} onChange={vi.fn()} />);
    expect(screen.getByText('legacyBattle')).toBeTruthy();
    // 脚本尚未加载完，不应急于标记未知
    expect(screen.queryByText('当前流程未识别')).toBeNull();
    expect(screen.getByText('正在加载状态列表…')).toBeTruthy();
  });

  it('无非内置候选时提示「当前流程没有可清除的状态」', () => {
    mocked.mockReturnValue({
      keys: [
        { key: 'id', sourceType: 'builtin', sourceName: '内置', builtinType: 'int' },
        { key: 'index', sourceType: 'builtin', sourceName: '内置', builtinType: 'int' },
        { key: 'account', sourceType: 'builtin', sourceName: '内置', builtinType: 'string' },
      ],
      ready: true,
    });
    render(<ClearStateEditor value={[]} onChange={vi.fn()} />);
    expect(screen.getByText('当前流程没有可清除的状态')).toBeTruthy();
  });
});
