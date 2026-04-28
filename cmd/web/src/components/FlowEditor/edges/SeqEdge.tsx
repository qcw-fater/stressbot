/**
 * 顺序执行边：默认色随起点节点类型；选中加粗变实线 + drop-shadow 同色发光。
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle, colorOfNodeType } from './edgeStyle';

export function SeqEdge({
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
  const d = data as { order?: number; sourceNodeType?: string } | undefined;
  const order = d?.order;
  const sourceColor = colorOfNodeType(d?.sourceNodeType);
  const style = buildEdgeStyle({ sourceNodeType: d?.sourceNodeType, width: 1.5, dash: '5 4', selected });
  return (
    <>
      <BaseEdge id={id} path={path} style={style} markerEnd={markerEnd} />
      {typeof order === 'number' && (
        <EdgeLabelRenderer>
          <div
            style={{
              position: 'absolute',
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
              fontSize: 10,
              padding: '1px 5px',
              borderRadius: 8,
              background: 'var(--bg-panel)',
              border: `1px solid ${sourceColor}`,
              color: sourceColor,
              fontWeight: 600,
              pointerEvents: 'all',
            }}
            className="nodrag nopan"
          >
            {order + 1}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
}
