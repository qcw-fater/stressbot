/**
 * React Flow 边类型注册表。
 */

import type { EdgeTypes } from '@xyflow/react';
import { SeqEdge } from './SeqEdge';
import { BranchEdge } from './BranchEdge';
import { WeightEdge } from './WeightEdge';
import { LoopBodyEdge } from './LoopBodyEdge';
import { ListenEdge } from './ListenEdge';
import { ErrorEdge } from './ErrorEdge';
import { WaitThenEdge } from './WaitThenEdge';

export const edgeTypes: EdgeTypes = {
  seq: SeqEdge,
  waitThen: WaitThenEdge,
  branch: BranchEdge,
  weight: WeightEdge,
  loopBody: LoopBodyEdge,
  listen: ListenEdge,
  error: ErrorEdge,
};
