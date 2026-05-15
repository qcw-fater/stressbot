import { Cascader, Input, Space, Tooltip } from 'antd';
import { ProfileOutlined } from '@ant-design/icons';
import { useMemo } from 'react';
import { protoRegistry } from '../../proto/ProtoRegistry';

export interface ProtoPathInputProps {
  messageFullName?: string;
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
  style?: React.CSSProperties;
}

interface Option {
  value: string;
  label: string | React.ReactNode;
  isLeaf?: boolean;
  children?: Option[];
  isRepeated?: boolean;
}

function shortType(type: string): string {
  if (['string', 'int32', 'int64', 'uint32', 'uint64', 'sint32', 'sint64',
       'fixed32', 'fixed64', 'sfixed32', 'sfixed64', 'float', 'double',
       'bool', 'bytes'].includes(type)) {
    return type;
  }
  return type.split('.').pop() ?? type;
}

export function ProtoPathInput({
  messageFullName,
  value,
  onChange,
  placeholder,
  style,
}: ProtoPathInputProps) {
  const options = useMemo(() => {
    if (!messageFullName || !protoRegistry.isLoaded()) return [];

    const buildTree = (msgName: string, depth: number): Option[] => {
      if (depth > 4) return [];
      const msg = protoRegistry.lookupMessage(msgName);
      if (!msg) return [];

      return msg.fields.map((f) => {
        const isMsg = f.kind === 'message' && f.messageName;
        const children = isMsg ? buildTree(f.messageName!, depth + 1) : undefined;
        const typeLabel = shortType(f.type);

        return {
          value: f.name,
          label: (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, maxWidth: '100%' }}>
              <span>{f.name}{f.repeated ? '[]' : ''}</span>
              <span style={{ fontSize: 10, color: 'var(--text-terti)' }}>{typeLabel}</span>
              {f.comment && (
                <span style={{ fontSize: 10, color: 'var(--text-quaternary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  — {f.comment}
                </span>
              )}
            </span>
          ),
          isLeaf: !isMsg || !children || children.length === 0,
          children: children?.length ? children : undefined,
          isRepeated: f.repeated,
        };
      });
    };

    return buildTree(messageFullName, 0);
  }, [messageFullName]);

  const input = (
    <Input
      style={style}
      value={value}
      onChange={(e) => onChange?.(e.target.value)}
      placeholder={placeholder ?? '字段路径 (如 a.b[0].c)'}
    />
  );

  if (options.length === 0) return input;

  return (
    <Space.Compact style={style ?? { width: '100%' }}>
      {input}
      <Cascader
        options={options}
        changeOnSelect
        value={[]}
        onChange={(_, selectedOptions) => {
          if (!selectedOptions || selectedOptions.length === 0) return;
          const dataPathParts = selectedOptions.map((opt, index) => {
            const isLast = index === selectedOptions.length - 1;
            return (opt.isRepeated && !isLast) ? `${opt.value}[0]` : String(opt.value);
          });
          onChange?.(dataPathParts.join('.'));
        }}
      >
        <Tooltip title="从 Proto 结构树中选择">
          <button
            style={{
              height: 32,
              border: '1px solid var(--border-color)',
              borderLeft: 0,
              borderRadius: '0 6px 6px 0',
              background: 'var(--bg-panel)',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              padding: '0 8px',
            }}
          >
            <ProfileOutlined style={{ color: 'var(--text-secondary)' }} />
          </button>
        </Tooltip>
      </Cascader>
    </Space.Compact>
  );
}
