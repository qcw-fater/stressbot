/**
 * React Flow 节点类型注册表。
 *
 * 把节点 type 字符串映射到具体组件，传给 ReactFlow 的 nodeTypes prop。
 *
 * 统一在这里 memo 而不是各组件自己包：漏一个就是一个每次画布重算都重渲的节点类型，
 * 而这件事从组件源码里看不出来。配合 flowStore.syncDerived 的对象复用才有效——
 * data 每次换新引用的话，memo 一样拦不住。
 *
 * 注意 memo 只挡住"父级传下来的 props 没变"这一种重渲；节点内部的实时指标、
 * 校验角标走 NodeShell 自己的 store 订阅，不受影响。
 */

import { memo } from 'react';
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
  sequence: memo(SequenceNode),
  action: memo(ActionNode),
  loop: memo(LoopNode),
  boolean: memo(BooleanNode),
  switch: memo(SwitchNode),
  weighted: memo(WeightedNode),
  wait: memo(WaitNode),
  break: memo(BreakNode),
  continue: memo(ContinueNode),
  listenCard: memo(ListenCard),
};
