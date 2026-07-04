/**
 * action → 普通节点的 onError handler 调用边。
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle } from './edgeStyle';

export function ErrorEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  selected,
}: EdgeProps) {
  const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition });
  const style = buildEdgeStyle({
    sourceNodeType: 'action',
    width: 1.4,
    dash: '3 3',
    selected,
    colorOverride: 'var(--color-error)',
  });
  return (
    <>
      <BaseEdge id={id} path={path} style={selected ? style : { ...style, opacity: 0.82 }} markerEnd={markerEnd} />
      <EdgeLabelRenderer>
        <div
          title="onError.handler 调用边"
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            fontSize: 10,
            padding: '1px 6px',
            borderRadius: 8,
            background: 'var(--bg-panel)',
            border: '1px solid var(--color-error)',
            color: 'var(--color-error)',
            fontWeight: 600,
            pointerEvents: 'all',
          }}
          className="nodrag nopan"
        >
          handler
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
