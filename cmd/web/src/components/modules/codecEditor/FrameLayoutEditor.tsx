/**
 * FrameLayoutEditor — 帧布局结构化编辑器（外壳）。
 *
 * 组合：FrameScalars（帧参数） + ByteStrip（字节尺 hero） + HeaderFieldTable（字段表，
 * 选中字段的 role 联动表单经行内展开渲染）。
 *
 * Props：`{ raw, schema, onEdit }`。schema 读展示，修改经 codecEdit helper 生成新 content → onEdit。
 * 单一数据源 = content 字符串（由 AdapterTab 的 setContent 回灌后重算 parsed）。
 */

import { useState } from 'react';
import type { CodecSchema } from '@/types/codec';
import './codecEditor.css';
import { FrameScalars } from './FrameScalars';
import { ByteStrip } from './ByteStrip';
import { HeaderFieldTable } from './HeaderFieldTable';

export interface FrameLayoutEditorProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  onEdit: (nextContent: string) => void;
}

export function FrameLayoutEditor({ raw, schema, onEdit }: FrameLayoutEditorProps) {
  // 当前选中字段（在 schema.header 中的 index）。null = 未选中。
  // 选中 → HeaderFieldTable 该行行内展开 RoleLinkedForm；字节尺同步高亮。
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null);

  return (
    <section className="frame-tab">
      {/* 帧参数 */}
      <FrameScalars raw={raw} schema={schema} onEdit={onEdit} />

      {/* 字节尺 hero（帧布局视觉中心） */}
      <div className="byte-hero">
        <ByteStrip schema={schema} selectedIndex={selectedIndex} onSelect={setSelectedIndex} />
      </div>

      {/* 字段表（全宽独占；选中行行内展开字段详情） */}
      <div className="frame-table-wrap">
        <HeaderFieldTable
          raw={raw}
          schema={schema}
          selectedIndex={selectedIndex}
          onSelect={setSelectedIndex}
          onEdit={onEdit}
        />
      </div>
    </section>
  );
}
