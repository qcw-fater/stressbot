/**
 * 分支边：boolean 的 true 绿 / false 红；switch 等其他分支跟随源节点色（如 switch 品红）。
 * 默认虚线，选中加粗变实线（保留各色，加深）。
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle, colorOfNodeType, deepColorOfNodeType } from './edgeStyle';

export function BranchEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  markerEnd,
  selected,
}: EdgeProps) {
  const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition });
  const d = data as { branch?: string; caseIndex?: number; sourceNodeType?: string } | undefined;
  const branch = d?.branch;
  // case 边显示序号（case 1/2/…，与节点行序号、编辑器 Case N 对齐）；true/false/default 用本身
  const label = branch === 'case' && typeof d?.caseIndex === 'number' ? `case ${d.caseIndex + 1}` : branch;
  // boolean true/false 走固定绿/红；其余（switch case/default 等）跟随源节点配色
  const isBooleanBranch = branch === 'true' || branch === 'false';
  const color = isBooleanBranch
    ? branch === 'true' ? 'var(--edge-true)' : 'var(--edge-false)'
    : colorOfNodeType(d?.sourceNodeType);
  const deep = isBooleanBranch
    ? branch === 'true' ? 'var(--edge-true-deep)' : 'var(--edge-false-deep)'
    : deepColorOfNodeType(d?.sourceNodeType);
  const style = buildEdgeStyle({
    width: 1.6,
    dash: '6 3',
    selected,
    colorOverride: color,
    selectedColorOverride: deep,
  });
  return (
    <>
      <BaseEdge id={id} path={path} style={style} markerEnd={markerEnd} />
      <EdgeLabelRenderer>
        <div
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            fontSize: 10,
            padding: '1px 6px',
            borderRadius: 8,
            background: 'var(--bg-panel)',
            border: `1px solid ${color}`,
            color,
            fontWeight: 600,
            pointerEvents: 'all',
          }}
          className="nodrag nopan"
        >
          {label}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
