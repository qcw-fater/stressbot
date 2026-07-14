/**
 * clearState 专用编辑器：以可搜索多选下拉框编辑 ActionDef.keys（已知 state key）。
 *
 * 与 free-text tags 输入不同，本编辑器禁止创建未知 key：
 *   - mode="multiple"（非 "tags"），候选项仅来自 useStateKeyOptions 的已知 key，
 *     因此无法通过键盘输入创建任意 key。
 *   - 内置 key（id/index/account）在候选中可见但禁用（disabled），标注「内置状态不可清除」；
 *     后端同样会拒绝，UI 层先保证不可勾选。
 *   - 导入 value 中存在但未注册的 key（未知 key）予以保留（不静默丢弃），标注「当前流程未识别」，
 *     支持单独移除，但不会出现在候选项中。
 *   - ready=false（脚本仍在加载）时不标记未知（key 可能随后变为已知），提示「正在加载状态列表…」。
 *   - ready=true 且无非内置候选时，提示「当前流程没有可清除的状态」，不退化为自由输入。
 */

import { Select, Tag } from 'antd';
import { CloseOutlined } from '@ant-design/icons';
import { isBuiltinStateKey, type StateKeyInfo } from './stateRegistry';
import { StateKeyOptionLabel } from './stateKeyPresentation';
import { useStateKeyOptions } from './useStateKeyOptions';

export interface ClearStateEditorProps {
  value?: string[];
  onChange: (keys: string[]) => void;
}

export function ClearStateEditor({ value, onChange }: ClearStateEditorProps) {
  const { keys, ready } = useStateKeyOptions();
  const selected = value ?? [];

  const knownByKey = new Map<string, StateKeyInfo>(keys.map((info) => [info.key, info]));
  const nonBuiltinCandidates = keys.filter((info) => !isBuiltinStateKey(info.key));

  const options = keys.map((info) => {
    const builtin = isBuiltinStateKey(info.key);
    return {
      value: info.key,
      disabled: builtin,
      // 包一层带 title 的 span：下拉项 DOM 结构随 antd 版本变化，title 让测试用
      // findByTitle(info.key) 稳定命中真实可点击的选项节点。
      label: (
        <span title={info.key}>
          <StateKeyOptionLabel info={info} />
          {builtin && (
            <span style={{ fontSize: 10, color: 'var(--text-tertiary)', marginLeft: 4 }}>
              内置状态不可清除
            </span>
          )}
        </span>
      ),
    };
  });

  const handleChange = (next: unknown) => {
    // antd multiple Select 回传新完整数组；统一转字符串并保留顺序去重。
    const arr = (Array.isArray(next) ? next : []).map(String);
    onChange([...new Set(arr)]);
  };

  return (
    <div>
      <Select
        mode="multiple"
        value={selected}
        onChange={handleChange}
        options={options}
        placeholder="选择要清除的状态"
        style={{ width: '100%' }}
        tagRender={(props) => {
          const tagValue = String(props.value);
          // ready=false 时不标记未知：脚本尚未加载完，key 随后可能变为已知。
          const known = !ready || knownByKey.has(tagValue);
          if (known) {
            return (
              <Tag closable={props.closable} onClose={props.onClose}>
                {tagValue}
              </Tag>
            );
          }
          // 未知 key：保留显示、可移除，但不进入候选项。
          return (
            <Tag>
              <code>{tagValue}</code>
              <span style={{ fontSize: 10, color: 'var(--text-tertiary)', marginLeft: 4 }}>
                当前流程未识别
              </span>
              <CloseOutlined
                aria-label={`移除 ${tagValue}`}
                style={{ marginInlineStart: 4, cursor: 'pointer' }}
                onClick={(e) => {
                  e.stopPropagation();
                  props.onClose();
                }}
              />
            </Tag>
          );
        }}
      />
      {ready && nonBuiltinCandidates.length === 0 && (
        <div style={{ marginTop: 4, color: 'var(--text-tertiary)', fontSize: 12 }}>
          当前流程没有可清除的状态
        </div>
      )}
      {!ready && (
        <div style={{ marginTop: 4, color: 'var(--text-tertiary)', fontSize: 12 }}>
          正在加载状态列表…
        </div>
      )}
    </div>
  );
}
