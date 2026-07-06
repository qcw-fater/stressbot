/**
 * React Flow 节点类型注册表。
 *
 * 把节点 type 字符串映射到具体组件，传给 ReactFlow 的 nodeTypes prop。
 */

import type { NodeTypes } from '@xyflow/react';
import { SequenceNode } from './SequenceNode';
import { ActionNode } from './ActionNode';
import { LoopNode } from './LoopNode';
import { BooleanNode } from './BooleanNode';
import { SwitchNode } from './SwitchNode';
import { WeightedNode } from './WeightedNode';
import { WaitNode } from './WaitNode';
import { BreakNode } from './BreakNode';
import { ContinueNode } from './ContinueNode';
import { ListenCard } from '../listens/ListenCard';

export const nodeTypes: NodeTypes = {
  sequence: SequenceNode,
  action: ActionNode,
  loop: LoopNode,
  boolean: BooleanNode,
  switch: SwitchNode,
  weighted: WeightedNode,
  wait: WaitNode,
  break: BreakNode,
  continue: ContinueNode,
  listenCard: ListenCard,
};
