/**
 * 画布事件区的 CallbackCard 浮动卡片（紧凑形态）。
 *
 * 仅显示名字 + kind 标签 + 引用数，详情双击打开编辑器查看。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import type { CallbackDef } from '@/types/callback';
import { classifyCallback } from '@/types/callback';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { callbackKindShortLabel } from './callbackKindStyle';
import './CallbackCard.css';

interface CardData {
  callbackName: string;
  callback: CallbackDef;
}

export function CallbackCard({ data, selected }: NodeProps) {
  const { callbackName, callback } = data as unknown as CardData;
  const kind = classifyCallback(callback);
  const refCount = useFlowStore((s) => s.callbackRefCount[callbackName] ?? 0);
  const setHoveredCallback = useEditorStore((s) => s.setHoveredCallback);

  return (
    <div
      className={`callback-card kind-${kind} ${selected ? 'selected' : ''}`}
      onMouseEnter={() => setHoveredCallback(callbackName)}
      onMouseLeave={() => setHoveredCallback(null)}
    >
      <Handle type="target" position={Position.Left} id="in" style={{ background: 'var(--edge-listen)' }} />
      {/* 第一行：仅 callback 名字（与 ActionNode 的"只显示 ID"风格一致） */}
      <div className="callback-card-title-row">
        <span className="card-title" title={callbackName}>
          {callbackName}
        </span>
      </div>
      {/* 第二行：kind 标签 + 引用计数 */}
      <div className="callback-card-meta-row">
        <span className={`card-kind kind-tag-${kind}`}>{callbackKindShortLabel[kind]}</span>
        {refCount > 0 ? (
          <span className="card-ref">×{refCount}</span>
        ) : (
          <span className="card-ref orphan" title="未被任何 action 引用">
            !0
          </span>
        )}
      </div>
    </div>
  );
}
