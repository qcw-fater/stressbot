import type { FlowNode } from '@/types/flow';

export type WaitConnectionResult =
  | { matched: false }
  | { matched: true; patch: Partial<FlowNode> }
  | { matched: true; error: string };

export function resolveWaitConnection(
  source: FlowNode,
  sourceId: string,
  sourceHandle: string,
  targetNodeId: string | null,
): WaitConnectionResult {
  if (source.type !== 'wait' || sourceHandle !== 'out') return { matched: false };
  if (!targetNodeId) return { matched: true, error: 'wait 节点只能连接普通节点' };
  if (targetNodeId === sourceId) return { matched: true, error: 'wait 节点不能指向自身' };
  return { matched: true, patch: { then: targetNodeId } };
}
