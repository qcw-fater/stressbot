/**
 * weighted 权重边：颜色随起点 weighted 节点（紫色），粗细随权重比例变化。
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle, colorOfNodeType } from './edgeStyle';

export function WeightEdge({
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
  const d = data as { weight?: number; ratio?: number; sourceNodeType?: string } | undefined;
  const weight = d?.weight ?? 0;
  const ratio = d?.ratio ?? 0;
  const sourceType = d?.sourceNodeType ?? 'weighted';
  const baseWidth = 1.5 + ratio * 2.5;
  const style = buildEdgeStyle({ sourceNodeType: sourceType, width: baseWidth, dash: '4 2', selected });
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
            pointerEvents: 'all',
            fontWeight: 600,
          }}
          className="nodrag nopan"
        >
          w={weight} ({(ratio * 100).toFixed(1)}%)
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
