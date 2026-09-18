/**
 * 任务容量预检口径（纯函数，前端唯一实现）。
 *
 * 必须与服务端 `admin/task/assignment.go` 的 `Assign` 保持一致，否则会出现「弹窗说能起、
 * 提交被拒」：
 *   - 容量只算**空闲**节点的 maxBots。忙碌 / 不健康 / 离线节点都不参与分配，把它们算进
 *     容量会让预检虚高；
 *   - 申请数取**提交给服务端的 totalBots**。渐进加压下提交的是各阶段之和，服务端按这个
 *     数校验容量并分配账号编号——即便存在 reset 阶段使瞬时并发低于总和，也不能拿峰值去
 *     预检，否则预检通过而服务端仍会拒。
 *
 * 前端预检只为体验，服务端仍是单一权威。
 */

import type { AgentStatus, RampUpStage } from '@/types/api';

/** 容量计算只关心这两个字段，用结构化子集避免绑死某一种节点响应类型。 */
export interface CapacityAgent {
  status: AgentStatus;
  maxBots: number;
}

/** 可分配容量：空闲节点 maxBots 之和 + 空闲节点数。 */
export function assignableCapacity(agents: readonly CapacityAgent[] | undefined): {
  capacity: number;
  idleAgents: number;
} {
  let capacity = 0;
  let idleAgents = 0;
  for (const a of agents ?? []) {
    if (a.status !== 'idle' || a.maxBots <= 0) continue;
    capacity += a.maxBots;
    idleAgents += 1;
  }
  return { capacity, idleAgents };
}

/** 实际提交给服务端的机器人总数：渐进加压时为各阶段之和。 */
export function requestedBots(input: {
  rampUpEnabled: boolean;
  rampUpStages: readonly RampUpStage[];
  totalBots: number;
}): number {
  if (!input.rampUpEnabled) return input.totalBots;
  return input.rampUpStages.reduce((sum, st) => sum + (st.count || 0), 0);
}

/** 容量不足提示文案，弹窗预检与服务端预检共用，保证两处口径与措辞一致。 */
export function capacityExceededMessage(capacity: number, requested: number): string {
  return `可分配容量 ${capacity}（仅空闲节点），本次申请 ${requested}`;
}
