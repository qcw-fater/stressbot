/**
 * algosForStepOp — 算法下拉过滤的纯函数（无 React 依赖，便于单测）。
 *
 * **关键映射 gotcha**：
 *   - PipelineStep.op ∈ {compress, encrypt, checksum, hash}
 *   - AlgoMeta.op     ∈ {cipher,   compress, checksum, hash}
 *
 * encrypt↔cipher 是唯一的语义映射差异（schema.go 用 encrypt 描述管线「加密」步，
 * registry.go 用 cipher 描述已注册的「加密」算法），其余三个同名。下拉过滤必须做这个映射，
 * 否则 encrypt 步选不到任何算法。
 */

import type { AlgoMeta } from '@/types/codec';

/** PipelineStep.op → AlgoMeta.op 映射；未知 op 映射到一个不可能命中的 sentinel（→ 空结果）。 */
const STEP_OP_TO_ALGO_OP: Record<string, AlgoMeta['op']> = {
  encrypt: 'cipher',
  cipher: 'cipher',
  compress: 'compress',
  checksum: 'checksum',
  hash: 'hash',
};

/**
 * 返回清单里 op 匹配当前步 op 的算法（保持清单原顺序，不重排）。
 * 未知 stepOp → 空数组（不抛、不兜底）。
 */
export function algosForStepOp(algos: AlgoMeta[], stepOp: string): AlgoMeta[] {
  const target = STEP_OP_TO_ALGO_OP[stepOp];
  if (!target) return [];
  return algos.filter((a) => a && a.op === target);
}
