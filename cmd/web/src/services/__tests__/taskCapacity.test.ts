/**
 * 容量预检口径单测。
 *
 * 这两个函数是「弹窗预检」与「提交前预检」的唯一实现，口径必须与服务端
 * `admin/task/assignment.go` 一致，否则会出现 UI 放行 / 服务端拒绝的错位。
 */

import { describe, expect, it } from 'vitest';
import type { AgentStatus } from '@/types/api';
import { assignableCapacity, requestedBots } from '../taskCapacity';

function agent(status: AgentStatus, maxBots: number) {
  return { status, maxBots };
}

describe('assignableCapacity', () => {
  it('只累加空闲节点，忙碌 / 不健康 / 离线都不算容量', () => {
    expect(
      assignableCapacity([
        agent('idle', 100),
        agent('idle', 50),
        agent('busy', 800),
        agent('unhealthy', 400),
        agent('offline', 200),
      ]),
    ).toEqual({ capacity: 150, idleAgents: 2 });
  });

  it('maxBots 为 0 的空闲节点不计入节点数（服务端同样跳过）', () => {
    expect(assignableCapacity([agent('idle', 0)])).toEqual({ capacity: 0, idleAgents: 0 });
  });

  it('agents 未就绪（undefined）时容量为 0，而非乐观放行', () => {
    expect(assignableCapacity(undefined)).toEqual({ capacity: 0, idleAgents: 0 });
  });
});

describe('requestedBots', () => {
  it('未开启渐进加压时就是填写的机器人数', () => {
    expect(requestedBots({ rampUpEnabled: false, rampUpStages: [], totalBots: 120 })).toBe(120);
  });

  it('渐进加压取各阶段之和，忽略表单里填写的总数', () => {
    expect(
      requestedBots({
        rampUpEnabled: true,
        rampUpStages: [{ count: 100 }, { count: 200 }],
        totalBots: 7,
      }),
    ).toBe(300);
  });

  it('reset 阶段不减少申请数：服务端按总和分配账号编号并校验容量', () => {
    expect(
      requestedBots({
        rampUpEnabled: true,
        rampUpStages: [{ count: 100 }, { count: 100, reset: true }],
        totalBots: 0,
      }),
    ).toBe(200);
  });

  it('阶段未填数量时按 0 计，避免 NaN 传染预检', () => {
    expect(
      requestedBots({
        rampUpEnabled: true,
        rampUpStages: [{ count: undefined as unknown as number }, { count: 10 }],
        totalBots: 0,
      }),
    ).toBe(10);
  });
});
