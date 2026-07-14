/**
 * codec 圆周等价测试：
 *   1. 读取 conf/flow/flow.json
 *   2. flowToJson(原数据) → 导出
 *   3. 字段集与节点/动作/回调数量必须一致
 *   4. 嵌套结构（bindings、listenRefs、store）在导出时仍存在
 */

import { describe, it, expect } from 'vitest';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { flowToJson } from './flowToJson';
import { jsonToFlow } from './jsonToFlow';
import type { FlowJsonInput } from './jsonToFlow';
import type { FlowJson } from './flowToJson';

// web 移到 cmd/web 后，从 codec.test.ts 到仓库根需要 6 层 ..
const flowPath = path.resolve(__dirname, '../../../../../../conf/flow/flow.json');

describe('codec round-trip', () => {
  const raw = JSON.parse(fs.readFileSync(flowPath, 'utf-8')) as FlowJsonInput;

  it('节点 / 动作 / 回调数量与原文件一致', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      listens: raw.listens,
    });
    expect(Object.keys(exported.nodes).length).toBe(Object.keys(raw.nodes).length);
    expect(Object.keys(exported.actions).length).toBe(Object.keys(raw.actions).length);
    expect(Object.keys(exported.listens).length).toBe(Object.keys(raw.listens).length);
    expect(exported.defaultDelayMs).toBe(raw.defaultDelayMs);
  });

  it('jsonToFlow 生成的 React Flow 节点数 = nodes + listens', () => {
    const { rfNodes, listenRefCount } = jsonToFlow(raw);
    const expected = Object.keys(raw.nodes).length + Object.keys(raw.listens).length;
    expect(rfNodes.length).toBe(expected);
    // 至少有一些 callback 是被引用的
    expect(Object.keys(listenRefCount).length).toBeGreaterThan(0);
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

  it('导出 onError 且不导出旧 errorStrategy 字段', () => {
    const exported = flowToJson({
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'action', action: 'A1', onError: { strategy: 'abort', ignoreCodes: [1001], retry: { maxRetries: 2, retryDelayMs: 500 } } },
      },
      actions: { A1: { pattern: 'clearState', keys: ['x'] } },
      listens: {},
    });
    expect(exported.nodes.main).toEqual({
      type: 'action',
      action: 'A1',
      onError: { ignoreCodes: [1001], retry: { maxRetries: 2, retryDelayMs: 500 }, strategy: 'abort' },
    });
    expect('errorStrategy' in (exported.nodes.main as unknown as Record<string, unknown>)).toBe(false);
  });

  it('空 onError 在导出时被裁剪', () => {
    const exported = flowToJson({
      defaultDelayMs: 1000,
      nodes: { main: { type: 'action', action: 'A1', onError: { strategy: 'resume', retry: { maxRetries: 0, retryDelayMs: 0 }, ignoreCodes: [] } } },
      actions: { A1: { pattern: 'clearState', keys: ['x'] } },
      listens: {},
    });
    expect(exported.nodes.main.onError).toBeUndefined();
  });

  it('jsonToFlow 为 onError.handler 生成错误处理边', () => {
    const { rfEdges } = jsonToFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'sequence', next: ['act'] },
        act: { type: 'action', action: 'A1', onError: { handler: 'cleanup' } },
        cleanup: { type: 'action', action: 'Cleanup' },
      },
      actions: { A1: { pattern: 'clearState', keys: ['x'] }, Cleanup: { pattern: 'clearState', keys: ['y'] } },
      listens: {},
    });
    const edge = rfEdges.find((e) => e.type === 'error');
    expect(edge).toMatchObject({ source: 'act', target: 'cleanup', sourceHandle: 'error' });
  });

  it('jsonToFlow 不迁移旧 breakOff / errorStrategy 字段', () => {
    const legacy = {
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'action', action: 'A1', breakOff: true, errorStrategy: 'abort' },
      },
      actions: { A1: { pattern: 'clearState', keys: ['x'] } },
      listens: {},
    } as unknown as FlowJsonInput;
    const { rfNodes, rfEdges } = jsonToFlow(legacy);
    const node = rfNodes.find((n) => n.id === 'main')?.data as { node?: Record<string, unknown> };
    expect(node.node?.onError).toBeUndefined();
    expect(rfEdges.filter((e) => e.type === 'error')).toHaveLength(0);
  });

  it('listenRefs 在导出时被保留', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      listens: raw.listens,
    });
    let count = 0;
    for (const node of Object.values(exported.nodes)) {
      if (node.type === 'action' && node.listenRefs) count += node.listenRefs.length;
    }
    let countOriginal = 0;
    for (const node of Object.values(raw.nodes)) {
      if (node.type === 'action' && node.listenRefs) countOriginal += node.listenRefs.length;
    }
    expect(count).toBe(countOriginal);
  });

  it('action.bindings 和 listen.store 在导出时被保留', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      listens: raw.listens,
    });

    let bindingsRaw = 0;
    let bindingsOut = 0;
    for (const a of Object.values(raw.actions)) bindingsRaw += a.bindings?.length ?? 0;
    for (const a of Object.values(exported.actions)) bindingsOut += a.bindings?.length ?? 0;
    expect(bindingsOut).toBe(bindingsRaw);

    let storeRaw = 0;
    let storeOut = 0;
    for (const c of Object.values(raw.listens)) storeRaw += c.store?.length ?? 0;
    for (const c of Object.values(exported.listens)) storeOut += c.store?.length ?? 0;
    expect(storeOut).toBe(storeRaw);
  });

  it('filter mode 在导出时被保留', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: {
        A1: {
          pattern: 'tcpSend',
          bindings: [{ type: 'stateRandom', source: 'shopList', filters: [{ path: 'shopData[].ID', op: 'eq', value: 1, mode: 'none' }] }],
        },
      },
      listens: raw.listens,
    });
    expect(exported.actions.A1.bindings?.[0].filters?.[0].mode).toBe('none');
  });

  it('导出时按 pattern 裁剪无关字段', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: {
        A1: {
          pattern: 'tcpConnect',
          service: 'logic',
          address: '127.0.0.1:9000',
          route: { cmd: 1 },
          c2sProto: 'Game.TestC2S',
          s2cProto: 'Game.TestS2C',
          bindings: [{ type: 'fixed', field: 'id', value: 1 }],
          store: [{ field: 'id', setter: 'id' }],
          timeout: 10,
        },
      },
      listens: raw.listens,
    });
    expect(exported.actions.A1).toEqual({
      pattern: 'tcpConnect',
      service: 'logic',
      address: '127.0.0.1:9000',
    });
  });

  it('导出 JSON 写到磁盘，结构稳定', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: raw.actions,
      listens: raw.listens,
    });
    // 写到系统临时目录，避免翻动仓库内 tracked 文件（非 hermetic 修正）
    const outPath = path.join(os.tmpdir(), `stressbot-codec-export-${process.pid}-${Date.now()}.json`);
    fs.writeFileSync(outPath, JSON.stringify(exported, null, 2), 'utf-8');
    // 文件能读回
    const reread = JSON.parse(fs.readFileSync(outPath, 'utf-8')) as FlowJson;
    expect(Object.keys(reread.nodes).length).toBe(Object.keys(raw.nodes).length);
  });

  it('map binding entries 在导出时被保留，entry value 的 field/storeAs 被剥离，condition/wrap 保留', () => {
    const exported = flowToJson({
      defaultDelayMs: raw.defaultDelayMs,
      nodes: raw.nodes,
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: { cmd: 1 },
          c2sProto: 'Game.TestC2S',
          bindings: [
            {
              field: 'heroMap',
              type: 'map',
              entries: [
                { key: 'name', value: { type: 'fixed', value: 'test', field: 'should_be_stripped', storeAs: 'x', condition: 'lua:foo', wrap: true } },
                { key: 'level', value: { type: 'randomInt', min: 1, max: 100 } },
              ],
            },
          ],
        },
      },
      listens: raw.listens,
    });
    const binding = exported.actions.A1.bindings![0];
    expect(binding.type).toBe('map');
    expect(binding.entries).toHaveLength(2);

    // entry key preserved
    expect(binding.entries![0].key).toBe('name');
    expect(binding.entries![1].key).toBe('level');

    // entry value.field / storeAs stripped（map entry value 不消费这两个字段）；
    // condition / wrap 保留（后端 resolveMapValueStrict 会求值 condition、通用分支按 wrap 包裹）。
    const v0 = binding.entries![0].value!;
    expect(v0.field).toBeUndefined();
    expect(v0.storeAs).toBeUndefined();
    expect(v0.condition).toBe('lua:foo');
    expect(v0.wrap).toBe(true);
    expect(v0.value).toBe('test');
    expect(v0.type).toBe('fixed');

    // non-map fields in entry value preserved
    const v1 = binding.entries![1].value!;
    expect(v1.type).toBe('randomInt');
    expect(v1.min).toBe(1);
    expect(v1.max).toBe(100);
  });
});
