/**
 * loop → body 边：颜色随起点 loop 节点（绿色），选中加深。标 "body"。
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle, colorOfNodeType } from './edgeStyle';

export function LoopBodyEdge({
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
  const sourceType = (data as { sourceNodeType?: string } | undefined)?.sourceNodeType ?? 'loop';
  const style = buildEdgeStyle({ sourceNodeType: sourceType, width: 1.8, dash: '5 4', selected });
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
            border: `1px solid ${colorOfNodeType(sourceType)}`,
            color: colorOfNodeType(sourceType),
            fontWeight: 600,
            pointerEvents: 'all',
          }}
          className="nodrag nopan"
        >
          body
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
