/**
 * 节点 ID 下拉选择器：从当前 flowStore.nodes 中选。
 */

import { Select } from 'antd';
import { useFlowStore } from '../../store/flowStore';

export interface NodeIdSelectProps {
  value?: string;
  onChange?: (v: string | undefined) => void;
  placeholder?: string;
  /** 排除自己（避免节点引用自己） */
  excludeId?: string;
  /** 允许清空 */
  allowClear?: boolean;
}

export function NodeIdSelect({ value, onChange, placeholder, excludeId, allowClear = true }: NodeIdSelectProps) {
  const nodes = useFlowStore((s) => s.nodes);
  const options = Object.keys(nodes)
    .filter((id) => id !== excludeId)
    .sort()
    .map((id) => ({ label: `${id} · ${nodes[id].type}`, value: id }));
  return (
    <Select
      showSearch
      allowClear={allowClear}
      value={value}
      onChange={onChange}
      options={options}
      placeholder={placeholder ?? '选择节点'}
      style={{ width: '100%' }}
      optionFilterProp="label"
    />
  );
}
