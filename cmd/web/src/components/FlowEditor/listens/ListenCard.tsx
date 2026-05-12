/**
 * 画布事件区的 ListenCard 浮动卡片（紧凑形态）。
 *
 * 仅显示名字 + kind 标签 + 引用数，详情双击打开编辑器查看。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tooltip } from 'antd';
import type { ListenDef, ListenKind } from '@/types/listen';
import { classifyListen } from '@/types/listen';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { listenKindShortLabel } from './listenKindStyle';
import './ListenCard.css';

const LISTEN_KIND_DESC: Record<ListenKind, string> = {
  silent: '收到推送后静默处理',
  declarative: '解析 S2C 消息并写入 state',
  lua: '收到推送后执行 Lua 脚本',
};

interface CardData {
  listenName: string;
  listen: ListenDef;
}

export function ListenCard({ data, selected }: NodeProps) {
  const { listenName, listen } = data as unknown as CardData;
  const kind = classifyListen(listen);
  const refCount = useFlowStore((s) => s.listenRefCount[listenName] ?? 0);
  const setHoveredListen = useEditorStore((s) => s.setHoveredListen);

  return (
    <div
      className={`listen-card kind-${kind} ${selected ? 'selected' : ''}`}
      onMouseEnter={() => setHoveredListen(listenName)}
      onMouseLeave={() => setHoveredListen(null)}
    >
      <Handle type="target" position={Position.Left} id="in" style={{ background: 'var(--edge-listen)' }} />
      <div className="listen-card-title-row">
        <span className="card-title" title={listenName}>
          {listenName}
        </span>
      </div>
      {listen.description && (
        <div className="card-description">
          {listen.description}
        </div>
      )}
      <div className="listen-card-meta-row">
        <Tooltip title={LISTEN_KIND_DESC[kind]}>
          <span className={`card-kind kind-tag-${kind}`}>
            {listenKindShortLabel[kind]}
          </span>
        </Tooltip>
        {refCount > 0 ? (
          <Tooltip title={`被 ${refCount} 个 action 引用`}>
            <span className="card-ref">ref {refCount}</span>
          </Tooltip>
        ) : (
          <Tooltip title="未被任何 action 引用">
            <span className="card-ref orphan">ref 0</span>
          </Tooltip>
        )}
      </div>
    </div>
  );
}
