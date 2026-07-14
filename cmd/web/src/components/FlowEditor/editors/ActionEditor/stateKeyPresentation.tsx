/**
 * state key 候选项的统一展示组件与来源标签映射。
 *
 * - STATE_SOURCE_LABEL：来源类型 → { label, color }，覆盖 StateKeySourceType 全集
 *   （用户可见文本避免暴露实现技术：S2C→「响应」、Lua→「脚本」）。
 * - StateKeyOptionLabel：AutoComplete / 下拉项使用的单行渲染。
 * 此前 StateKeyInput 与 shared/StateExprInput 各自维护一份来源标签映射（且 lua 标签不一致），
 * 现统一从此处导入。
 */

import { Space, Tag } from 'antd';
import { resolveStateKeyDisplayType, type StateKeyInfo, type StateKeySourceType } from './stateRegistry';

export const STATE_SOURCE_LABEL = {
  store: { label: '响应', color: 'blue' },
  listenStore: { label: '推送', color: 'orange' },
  stateExtra: { label: '启动', color: 'volcano' },
  storeAs: { label: '中间值', color: 'green' },
  setState: { label: '状态动作', color: 'geekblue' },
  lua: { label: '脚本', color: 'purple' },
  builtin: { label: '内置', color: 'cyan' },
} satisfies Record<StateKeySourceType, { label: string; color: string }>;

/** 解析 state key 的可读类型：内置字段用 builtinType，其余走 proto 解析 */
export function stateKeyTypeLabel(info: StateKeyInfo): string | undefined {
  return info.builtinType ?? resolveStateKeyDisplayType(info);
}

export function StateKeyOptionLabel({ info }: { info: StateKeyInfo }) {
  const source = STATE_SOURCE_LABEL[info.sourceType];
  const type = stateKeyTypeLabel(info);
  return (
    <Space size={4}>
      <code style={{ fontSize: 12 }}>{info.key}</code>
      <Tag color={source.color} style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', marginRight: 0 }}>
        {source.label}
      </Tag>
      {type && <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>← {type}</span>}
      {!['stateExtra', 'storeAs', 'builtin'].includes(info.sourceType) && (
        <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>({info.sourceName})</span>
      )}
    </Space>
  );
}
