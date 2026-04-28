/**
 * codec 圆周等价测试：
 *   1. 读取 conf/flow.json
 *   2. flowToJson(原数据) → 导出
 *   3. 字段集与节点/动作/回调数量必须一致
 *   4. 嵌套结构（bindings、listenCallbacks、store）在导出时仍存在
 */

import { describe, it, expect } from 'vitest';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { flowToJson } from './flowToJson';
import { jsonToFlow } from './jsonToFlow';
import type { TaskFlow } from '@/types/flow';

const flowPath = path.resolve(__dirname, '../../../../../conf/flow.json');

describe('codec round-trip', () => {
  const raw = JSON.parse(fs.readFileSync(flowPath, 'utf-8')) as TaskFlow;

  it('节点 / 动作 / 回调数量与原文件一致', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      callbacks: raw.callbacks,
    });
    expect(Object.keys(exported.nodes).length).toBe(Object.keys(raw.nodes).length);
    expect(Object.keys(exported.actions).length).toBe(Object.keys(raw.actions).length);
    expect(Object.keys(exported.callbacks).length).toBe(Object.keys(raw.callbacks).length);
    expect(exported.defaultDelayMs).toBe(raw.defaultDelayMs);
  });

  it('jsonToFlow 生成的 React Flow 节点数 = nodes + callbacks', () => {
    const { rfNodes, callbackRefCount } = jsonToFlow(raw);
    const expected = Object.keys(raw.nodes).length + Object.keys(raw.callbacks).length;
    expect(rfNodes.length).toBe(expected);
    // 至少有一些 callback 是被引用的
    expect(Object.keys(callbackRefCount).length).toBeGreaterThan(0);
  });

  it('jsonToFlow 生成的边能覆盖所有 sequence next / boolean trueNext / weighted options', () => {
    const { rfEdges } = jsonToFlow(raw);
    let expectedSeq = 0;
    let expectedBranch = 0;
    let expectedWeight = 0;
    let expectedLoopBody = 0;
    for (const node of Object.values(raw.nodes)) {
      if (node.type === 'sequence') expectedSeq += node.next?.length ?? 0;
      else if (node.type === 'boolean') expectedBranch += (node.trueNext ? 1 : 0) + (node.falseNext ? 1 : 0);
      else if (node.type === 'weighted') expectedWeight += node.options?.length ?? 0;
      else if (node.type === 'loop' && node.body) expectedLoopBody += 1;
    }
    expect(rfEdges.filter((e) => e.type === 'seq').length).toBe(expectedSeq);
    expect(rfEdges.filter((e) => e.type === 'branch').length).toBe(expectedBranch);
    expect(rfEdges.filter((e) => e.type === 'weight').length).toBe(expectedWeight);
    expect(rfEdges.filter((e) => e.type === 'loopBody').length).toBe(expectedLoopBody);
  });

  it('listenCallbacks 在导出时被保留', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      callbacks: raw.callbacks,
    });
    let count = 0;
    for (const node of Object.values(exported.nodes)) {
      if (node.type === 'action' && node.listenCallbacks) count += node.listenCallbacks.length;
    }
    let countOriginal = 0;
    for (const node of Object.values(raw.nodes)) {
      if (node.type === 'action' && node.listenCallbacks) countOriginal += node.listenCallbacks.length;
    }
    expect(count).toBe(countOriginal);
  });

  it('action.bindings 和 callback.store 在导出时被保留', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      callbacks: raw.callbacks,
    });

    let bindingsRaw = 0;
    let bindingsOut = 0;
    for (const a of Object.values(raw.actions)) bindingsRaw += a.bindings?.length ?? 0;
    for (const a of Object.values(exported.actions)) bindingsOut += a.bindings?.length ?? 0;
    expect(bindingsOut).toBe(bindingsRaw);

    let storeRaw = 0;
    let storeOut = 0;
    for (const c of Object.values(raw.callbacks)) storeRaw += c.store?.length ?? 0;
    for (const c of Object.values(exported.callbacks)) storeOut += c.store?.length ?? 0;
    expect(storeOut).toBe(storeRaw);
  });

  it('导出 JSON 写到磁盘，结构稳定', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      callbacks: raw.callbacks,
    });
    const outPath = path.resolve(__dirname, '../../../../tmp_codec_export.json');
    fs.writeFileSync(outPath, JSON.stringify(exported, null, 2), 'utf-8');
    // 文件能读回
    const reread = JSON.parse(fs.readFileSync(outPath, 'utf-8')) as TaskFlow;
    expect(Object.keys(reread.nodes).length).toBe(Object.keys(raw.nodes).length);
  });
});
