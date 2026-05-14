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

/** break/continue 节点用固定名称显示 */
const FIXED_LABEL_TYPES = new Set(['break', 'continue']);

export function NodeIdSelect({ value, onChange, placeholder, excludeId, allowClear = true }: NodeIdSelectProps) {
  const nodes = useFlowStore((s) => s.nodes);
  const options = Object.keys(nodes)
    .filter((id) => id !== excludeId)
    .sort()
    .map((id) => {
      const type = nodes[id].type;
      const label = FIXED_LABEL_TYPES.has(type) ? type : `${id} · ${type}`;
      return { label, value: id };
    });
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
