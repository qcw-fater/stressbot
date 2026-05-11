/**
 * action → ListenCard 监听边：灰色虚线（与 action/listen 同灰）。
 *
 * 受 editorStore.showListenEdges 开关控制：关闭时不渲染。
 * 选中时加粗变深灰。
 */

import { BaseEdge, getBezierPath, type EdgeProps } from '@xyflow/react';
import { useEditorStore } from '../store/editorStore';
import { buildEdgeStyle } from './edgeStyle';

export function ListenEdge({
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
  const show = useEditorStore((s) => s.showListenEdges);
  if (!show) return null;
  const [path] = getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition });
  const style = buildEdgeStyle({
    sourceNodeType: 'action',
    width: 1.2,
    dash: '5 4',
    selected,
    colorOverride: 'var(--edge-listen)',
  });
  return <BaseEdge id={id} path={path} style={selected ? style : { ...style, opacity: 0.7 }} markerEnd={markerEnd} />;
}
