/**
 * boolean 分支边：true 绿 / false 红。默认虚线，选中加粗变实线（保留绿/红，加深）。
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle } from './edgeStyle';

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
  const branch = (data as { branch?: 'true' | 'false' } | undefined)?.branch;
  const isTrue = branch === 'true';
  const color = isTrue ? 'var(--edge-true)' : 'var(--edge-false)';
  const deep = isTrue ? '#237804' : '#a8071a';
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
            border: `1px solid ${isTrue ? 'var(--edge-true)' : 'var(--edge-false)'}`,
            color: isTrue ? 'var(--edge-true)' : 'var(--edge-false)',
            fontWeight: 600,
            pointerEvents: 'all',
          }}
          className="nodrag nopan"
        >
          {branch}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
