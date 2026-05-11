/**
 * Proto 字段选择器：基于已选 c2sProto / s2cProto，列出该 message 的字段下拉。
 *
 * Proto 未加载或未指定 message 时，退化为普通 Input。
 */

import { Input, Select, Tag } from 'antd';
import { protoRegistry } from '../../proto/ProtoRegistry';

export interface ProtoFieldPickerProps {
  messageFullName?: string;
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
  /** 仅显示某种 kind 的字段（如 'message' 仅嵌套字段） */
  filterKind?: 'scalar' | 'enum' | 'message' | 'map';
}

export function ProtoFieldPicker({
  messageFullName,
  value,
  onChange,
  placeholder,
  filterKind,
}: ProtoFieldPickerProps) {
  if (!messageFullName || !protoRegistry.isLoaded()) {
    return (
      <Input
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
        placeholder={placeholder ?? '字段名'}
      />
    );
  }
  const msg = protoRegistry.lookupMessage(messageFullName);
  if (!msg) {
    return (
      <Input
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
        placeholder={placeholder ?? `字段名（未找到 ${messageFullName}）`}
      />
    );
  }
  const fields = filterKind ? msg.fields.filter((f) => f.kind === filterKind) : msg.fields;
  const options = fields.map((f) => ({
    value: f.name,
    label: (
      <span>
        <code>{f.name}</code>
        <Tag style={{ marginLeft: 6 }} color={protoFieldKindColor(f.kind)}>
          {f.repeated ? '[]' : ''}
          {f.kind === 'map' ? `map<${f.mapKey},${f.mapValue}>` : f.type}
        </Tag>
      </span>
    ),
  }));
  return (
    <Select
      showSearch
      allowClear
      value={value}
      onChange={(v) => onChange?.(v ?? '')}
      options={options}
      style={{ width: '100%' }}
      placeholder={placeholder ?? `${msg.shortName} 的字段`}
      optionFilterProp="value"
    />
  );
}

/** Proto 字段类型 → Ant Tag 颜色（注意：与 listen kind 颜色无关，命名特意区分以免混淆）。 */
function protoFieldKindColor(k: string): string {
  switch (k) {
    case 'scalar':
      return 'blue';
    case 'enum':
      return 'gold';
    case 'message':
      return 'purple';
    case 'map':
      return 'cyan';
    default:
      return 'default';
  }
}
