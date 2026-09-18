/**
 * BindingsTable 组件测试。
 *
 * 守的不变量：切换取值方式后，binding 上只能留下目标 type 与通用高级配置，旧类型的专属参数
 * （min/max/values/entries/source/filters…）必须清掉，否则导出的 flow.json 会带着与当前
 * 类型无关的字段。剪枝规则本身在 stateActionEditorModel.test.ts 里已有单测，这里验证的是
 * 下拉框确实接到了那条规则上。
 *
 * 用 fireEvent 而非 userEvent：这里只需要「展开行 + 选一个下拉项」两步确定性交互，
 * 走 fireEvent 可避开 userEvent 的指针事件流水线，跑得更快也不受机器负载影响。
 */

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import type { BindingType, FieldBind } from '@/types/action';
import { BindingsTable } from './BindingsTable';

/** 按元素文本精确取一个，顺带断言唯一，避免误点到同名的另一处。 */
function uniqueByText(selector: string, text: string): HTMLElement {
  const found = Array.from(document.querySelectorAll<HTMLElement>(selector)).filter(
    (el) => el.textContent === text,
  );
  expect(found).toHaveLength(1);
  return found[0];
}

/**
 * 展开 `field` 所在的绑定行，把它的取值方式从 `from` 改成 `to`。
 *
 * 这里只能按 antd 的类名走，不能用 role 查询：rc-select 开虚拟滚动（默认）时，`role="option"`
 * 落在一组隐藏的无障碍镜像 div 上（`OptionList.js` 里 `height:0/width:0` 的那个 listbox），
 * 真正带 onClick 的可见项 `.ant-select-item-option` 反而没有 role —— 按 role 取到的是镜像，
 * 点了不会触发选择。下拉框本身按「当前选中值的文本」定位：展开行里字段输入框与类型下拉都是
 * combobox，按下标取更脆；选中项也没有 title 属性可用（取值方式的 label 是带 Tooltip 的 ReactNode）。
 */
function switchBindingType(field: string, from: BindingType, to: BindingType) {
  fireEvent.click(screen.getByText(field));

  const select = uniqueByText('.ant-select-selection-item', from).closest('.ant-select');
  fireEvent.mouseDown(select!.querySelector('.ant-select-selector')!);

  // 点 option 容器而不是内层 content：onClick 挂在容器上（内层点击靠冒泡，同样有效但绕一层）
  fireEvent.click(uniqueByText('.ant-select-item-option', to));
}

describe('BindingsTable', () => {
  it('切换取值方式时丢弃旧类型参数，保留字段与通用高级配置', () => {
    const onChange = vi.fn();
    const binding: FieldBind = {
      field: 'battleId',
      type: 'randomInt',
      min: 1,
      max: 99,
      required: true,
      storeAs: 'picked',
      condition: 'state:ready',
    };
    render(<BindingsTable value={[binding]} onChange={onChange} />);

    switchBindingType('battleId', 'randomInt', 'fixed');

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0][0]).toEqual([
      {
        field: 'battleId',
        type: 'fixed',
        required: true,
        storeAs: 'picked',
        condition: 'state:ready',
      },
    ]);
  });

  it('切换取值方式不影响同列表中的其他绑定', () => {
    const onChange = vi.fn();
    const untouched: FieldBind = { field: 'keep', type: 'randomPick', values: [1, 2] };
    render(
      <BindingsTable
        value={[untouched, { field: 'battleId', type: 'randomInt', min: 1, max: 9 }]}
        onChange={onChange}
      />,
    );

    switchBindingType('battleId', 'randomInt', 'fixed');

    expect(onChange.mock.calls[0][0]).toEqual([untouched, { field: 'battleId', type: 'fixed' }]);
  });
});
