/**
 * FrameLayoutEditor — 帧布局结构化编辑器（外壳）。
 *
 * 组合：FrameScalars（帧参数） + ByteStrip（字节条带） + HeaderFieldTable（字段表）
 *      + RoleLinkedForm（选中字段的 role 联动表单）。
 *
 * Props：`{ raw, schema, onEdit }`。schema 读展示，修改经 codecEdit helper 生成新 content → onEdit。
 * 单一数据源 = content 字符串（由 AdapterTab 的 setContent 回灌后重算 parsed）。
 */

import { useState } from 'react';
import { Space } from 'antd';
import type { CodecSchema, Field } from '@/types/codec';
import './codecEditor.css';
import { FrameScalars } from './FrameScalars';
import { ByteStrip } from './ByteStrip';
import { HeaderFieldTable } from './HeaderFieldTable';
import { RoleLinkedForm } from './RoleLinkedForm';

export interface FrameLayoutEditorProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  onEdit: (nextContent: string) => void;
}

export function FrameLayoutEditor({ raw, schema, onEdit }: FrameLayoutEditorProps) {
  // 当前选中字段（在 schema.header 中的 index）。null = 未选中。
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null);

  const fields: Field[] = schema.header ?? [];
  const selectedField: Field | undefined =
    selectedIndex !== null && selectedIndex >= 0 && selectedIndex < fields.length
      ? fields[selectedIndex]
      : undefined;

  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <FrameScalars raw={raw} schema={schema} onEdit={onEdit} />
      <ByteStrip schema={schema} selectedIndex={selectedIndex} onSelect={setSelectedIndex} />
      <HeaderFieldTable
        raw={raw}
        schema={schema}
        selectedIndex={selectedIndex}
        onSelect={setSelectedIndex}
        onEdit={onEdit}
      />
      {selectedField && selectedIndex !== null && (
        <RoleLinkedForm
          raw={raw}
          schema={schema}
          fieldIndex={selectedIndex}
          field={selectedField}
          onEdit={onEdit}
        />
      )}
    </Space>
  );
}
