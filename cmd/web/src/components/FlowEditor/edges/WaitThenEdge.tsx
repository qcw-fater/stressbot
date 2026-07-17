/** wait → then 边：颜色跟随 wait 节点，固定显示 then 标签。 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@xyflow/react';
import { buildEdgeStyle, colorOfNodeType } from './edgeStyle';

export const WAIT_THEN_LABEL = 'then';

export function WaitThenEdge({
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
  const sourceType = (data as { sourceNodeType?: string } | undefined)?.sourceNodeType ?? 'wait';
  const sourceColor = colorOfNodeType(sourceType);
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
            border: `1px solid ${sourceColor}`,
            color: sourceColor,
            fontWeight: 600,
            pointerEvents: 'all',
          }}
          className="nodrag nopan"
        >
          {WAIT_THEN_LABEL}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
