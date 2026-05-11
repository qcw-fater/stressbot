/**
 * 通用节点外壳：提供标题栏、状态徽章、监控槽、选中态描边。
 *
 * 节点配色取自 CSS token（设计文档 §5.2 / tokens.css）。
 */

import type { ReactNode } from 'react';
import { useEditorStore } from '../../store/editorStore';
import { useFlowStore } from '../../store/flowStore';
import { MetricsBadge, useNodeApdexLevel, useNodeMetrics } from './MetricsBadge';
import './NodeShell.css';

/** 节点视觉形状：决定额外的 className（圆角矩形 / 胶囊 / 菱形 / 六边形 / 圆 / 标签） */
export type NodeShape = 'rect' | 'pill' | 'diamond' | 'hex' | 'circle' | 'tag';

export interface NodeShellProps {
  /** 节点 ID（与 React Flow id 一致） */
  nodeId: string;
  /** 节点类型（决定配色 token） */
  nodeType: string;
  /** 标题文字（一般是节点 ID） */
  title: string;
  /** 副标题（可选，如 action 名 / pattern） */
  subtitle?: ReactNode;
  /** 内容区（紧凑模式下可省略） */
  children?: ReactNode;
  /** 是否选中 */
  selected?: boolean;
  /** 节点形状 */
  shape?: NodeShape;
  /** 节点最小宽度 */
  minWidth?: number;
  /** 是否紧凑模式（无 padding，无内容区） */
  compact?: boolean;
  /** 节点描述（注释）。非空时显示在 node-body 顶部一行，超长截断。 */
  description?: string;
}

export function NodeShell({
  nodeId,
  nodeType,
  title,
  subtitle,
  children,
  selected,
  shape = 'rect',
  minWidth,
  compact,
  description,
}: NodeShellProps) {
  const hoveredListen = useEditorStore((s) => s.hoveredListen);
  const edgeHighlight = useEditorStore((s) =>
    s.edgeHighlightNodeIds.includes(nodeId) ? 'edge-highlight' : '',
  );
  const edgeHighlightColor = useEditorStore((s) => s.edgeHighlightColor);
  const issues = useFlowStore((s) => s.issuesByNodeId[nodeId]);
  const errCount = issues?.filter((i) => i.severity === 'error').length ?? 0;
  const warnCount = issues?.filter((i) => i.severity === 'warning').length ?? 0;
  const issueClass = errCount > 0 ? 'has-error' : warnCount > 0 ? 'has-warning' : '';

  // 仅当本节点真的在 listenCallbacks 中注册了 hoveredListen 时高亮，
  // 而非 hoveredListen 非空时把所有节点都高亮。
  const isRegisteringHoveredListen = useFlowStore((s) =>
    hoveredListen ? (s.nodesByListen[hoveredListen] ?? []).includes(nodeId) : false,
  );
  const listenHighlight = isRegisteringHoveredListen ? 'highlight-by-listen' : '';
  const shapeClass = `shape-${shape}`;
  const compactClass = compact ? 'compact' : '';

  // 运行态 Apdex 染色：未注入 metrics 时返回 'unknown'，CSS 不覆盖原配色
  const apdexLevel = useNodeApdexLevel(nodeId);
  const apdexClass = apdexLevel !== 'unknown' ? `apdex-${apdexLevel}` : '';
  const metrics = useNodeMetrics(nodeId);
  const executing = metrics?.executing ?? 0;

  const style: React.CSSProperties = { minWidth };
  if (edgeHighlight && edgeHighlightColor) {
    (style as Record<string, string>)['--edge-highlight-color'] = edgeHighlightColor;
  }

  return (
    <div
      className={`node-shell node-${nodeType} ${selected ? 'selected' : ''} ${listenHighlight} ${edgeHighlight} ${shapeClass} ${compactClass} ${issueClass} ${apdexClass}`}
      style={style}
    >
      {(errCount > 0 || warnCount > 0) && (
        <div
          className="node-issue-badge"
          title={(issues ?? []).map((i) => `[${i.severity}] ${i.message}`).join('\n')}
          data-severity={errCount > 0 ? 'error' : 'warning'}
        >
          {errCount > 0 ? '!' : '?'}
        </div>
      )}
      {executing > 0 && (
        <div
          className="node-executing-badge"
          title={`当前正在执行：${executing} 个机器人`}
        >
          {executing}
        </div>
      )}
      <div className="node-header">
        <span className="node-title">{title}</span>
        {subtitle && <span className="node-subtitle">{subtitle}</span>}
      </div>
      {description && (
        <div className="node-description">
          {description}
        </div>
      )}
      {children && <div className="node-body">{children}</div>}
      <MetricsBadge nodeId={nodeId} />
    </div>
  );
}
