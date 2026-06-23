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
import { Tag, Typography } from 'antd';
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

  const headerSize = schema.frame?.headerSize ?? 0;
  const trailerSize = schema.frame?.trailerSize ?? 0;

  return (
    <section className="pce-bench frame-bench">
      <div className="pce-bench-header">
        <div>
          <Typography.Text className="pce-bench-title">FRAME</Typography.Text>
          <Typography.Text className="pce-bench-meta">
            header {headerSize} bytes · trailer {trailerSize} bytes · {fields.length} fields
          </Typography.Text>
        </div>
        {selectedField && <Tag className="frame-selected-tag">{selectedField.name || '未命名'}</Tag>}
      </div>

      <FrameScalars raw={raw} schema={schema} onEdit={onEdit} />
      <ByteStrip schema={schema} selectedIndex={selectedIndex} onSelect={setSelectedIndex} />

      <div className="frame-edit-stack">
        <div className="frame-table-pane">
          <HeaderFieldTable
            raw={raw}
            schema={schema}
            selectedIndex={selectedIndex}
            onSelect={setSelectedIndex}
            onEdit={onEdit}
          />
        </div>
        <div className="frame-inspector-pane">
          {selectedField && selectedIndex !== null ? (
            <RoleLinkedForm
              raw={raw}
              schema={schema}
              fieldIndex={selectedIndex}
              field={selectedField}
              onEdit={onEdit}
            />
          ) : (
            <div className="frame-empty-inspector">
              <Typography.Text className="pce-bench-title">FIELD PROBE</Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                从字节尺或表格选择一个字段，查看它的 role 配置。
              </Typography.Text>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
