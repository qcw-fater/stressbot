/**
 * T3 B1-D §3.5：flow 引用连接的 codec 覆盖校验单测。
 *
 * 覆盖三个纯函数：
 *   - collectFlowCodecConnections：从 flow 抽取 tcp/udp 动作引用的 `<proto>:<service>`
 *   - connNameToCodecFileName / codecFileNameToConnName：连接名 ↔ codec 文件名 round-trip
 *   - findMissingCodecConnections：给定引用集合与已有 codec 文件名，返回缺失的连接
 */

import { describe, expect, it } from 'vitest';
import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import {
  collectFlowCodecConnections,
  connNameToCodecFileName,
  codecFileNameToConnName,
  findMissingCodecConnections,
} from '../taskResourceDiff';

function buildFlow(actions: Record<string, unknown>): FlowJson {
  return {
    defaultDelayMs: 1000,
    nodes: {},
    actions: actions as FlowJson['actions'],
    listens: {},
  };
}

describe('collectFlowCodecConnections', () => {
  it('扫描 tcp*/udp* 动作、去重、排序，排除无 service 与非 tcp/udp', () => {
    const flow = buildFlow({
      a1: { pattern: 'tcpConnect', service: 'logic' },
      a2: { pattern: 'tcpRequest', service: 'battle' },
      a3: { pattern: 'udpSend', service: 'battle' }, // 与 a2 同名不同 proto → udp:battle
      a4: { pattern: 'udpRequest', service: 'battle' }, // 与 a3 重复 → 去重
      a5: { pattern: 'tcpListen', service: 'rank' },
      a6: { pattern: 'tcpClose', service: 'logic' }, // 与 a1 重复 → 去重
      a7: { pattern: 'setState' }, // 非 tcp/udp
      a8: { pattern: 'lua', script: 'x.lua' }, // 非 tcp/udp
      a9: { pattern: 'httpRequest', url: 'http://x' }, // 非 tcp/udp
      a10: { pattern: 'tcpSend' }, // tcp 但无 service → 排除
      a11: { pattern: 'udpConnect', service: '  ' }, // service 空白 → 排除
    });
    expect(collectFlowCodecConnections(flow)).toEqual([
      'tcp:battle',
      'tcp:logic',
      'tcp:rank',
      'udp:battle',
    ]);
  });

  it('flow.actions 缺失/为空对象时返回空数组', () => {
    expect(collectFlowCodecConnections({ defaultDelayMs: 0, nodes: {} } as FlowJson)).toEqual([]);
    expect(collectFlowCodecConnections({ defaultDelayMs: 0, nodes: {}, actions: {} } as unknown as FlowJson)).toEqual([]);
  });

  it('容忍动作缺 pattern 字段', () => {
    const flow = buildFlow({ a: { service: 'logic' } });
    expect(collectFlowCodecConnections(flow)).toEqual([]);
  });
});

describe('connNameToCodecFileName / codecFileNameToConnName', () => {
  it('连接名 → codec 文件名', () => {
    expect(connNameToCodecFileName('tcp:logic')).toBe('tcp_logic_codec.json');
    expect(connNameToCodecFileName('udp:battle')).toBe('udp_battle_codec.json');
  });

  it('codec 文件名 → 连接名', () => {
    expect(codecFileNameToConnName('tcp_logic_codec.json')).toBe('tcp:logic');
    expect(codecFileNameToConnName('udp_battle_codec.json')).toBe('udp:battle');
  });

  it('round-trip（含 udp:battle）', () => {
    const conns = ['tcp:logic', 'udp:battle', 'tcp:rank'];
    for (const c of conns) {
      expect(codecFileNameToConnName(connNameToCodecFileName(c))).toBe(c);
    }
  });

  it('codecFileNameToConnName 对无后缀名也不崩（首个 _ 换 :）', () => {
    expect(codecFileNameToConnName('tcp_logic')).toBe('tcp:logic');
  });
});

describe('findMissingCodecConnections', () => {
  it('引用连接都有对应文件 → 返回空数组', () => {
    const referenced = ['tcp:logic', 'udp:battle'];
    const files = ['tcp_logic_codec.json', 'udp_battle_codec.json'];
    expect(findMissingCodecConnections(referenced, files)).toEqual([]);
  });

  it('缺 udp:battle → 返回 [\'udp:battle\']', () => {
    const referenced = ['tcp:logic', 'udp:battle'];
    const files = ['tcp_logic_codec.json']; // 缺 udp_battle_codec.json
    expect(findMissingCodecConnections(referenced, files)).toEqual(['udp:battle']);
  });

  it('结果保持引用顺序（已排序输入 → 已排序输出）', () => {
    const referenced = ['tcp:battle', 'tcp:logic', 'udp:battle'];
    const files: string[] = [];
    expect(findMissingCodecConnections(referenced, files)).toEqual([
      'tcp:battle',
      'tcp:logic',
      'udp:battle',
    ]);
  });

  it('忽略 codec 文件名集合里的非 codec 文件（如 errors.json）', () => {
    const referenced = ['tcp:logic'];
    const files = ['errors.json']; // errors.json 不是任何连接的 codec
    expect(findMissingCodecConnections(referenced, files)).toEqual(['tcp:logic']);
  });

  it('空引用 → 空结果（无引用即无缺失）', () => {
    expect(findMissingCodecConnections([], ['tcp_logic_codec.json'])).toEqual([]);
  });
});
