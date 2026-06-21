/**
 * routeKey 集成校验（§3.7）：
 *   - listenRefs 引用了 IDB 没有 codec 的 server → ROUTEKEY_CODEC_MISSING warning；
 *   - 往 routeKeyResolver 的 cache 注入 template 后，duplicateRegisters 命中真实 routeKey。
 *
 * 不触真实 IDB：直接操作 routeKeyResolver 的模块级 cache（测试 reset/手动 set）。
 */

import { describe, it, expect, beforeEach } from 'vitest';
import { validateFlow } from './refsCheck';
import {
  __resetRouteKeyTemplateCacheForTest,
  getRouteKeyTemplatesSync,
} from '../listens/routeKeyResolver';
import type { TaskFlow } from '@/types/flow';

/** 同 route 与同 server、不同 listen → 重复注册；用真实 template 时 key 应一致。 */
function flowWithDuplicateRegister(): TaskFlow {
  return {
    defaultDelayMs: 1000,
    nodes: {
      main: { type: 'sequence', next: ['n1', 'n2'] },
      n1: {
        type: 'action',
        action: 'A1',
        listenRefs: [{ server: 'tcp:logic', route: { cmd: 1, act: 2 }, listen: 'cbA' }],
      },
      n2: {
        type: 'action',
        action: 'A2',
        listenRefs: [{ server: 'tcp:logic', route: { cmd: 1, act: 2 }, listen: 'cbB' }],
      },
    },
    actions: {
      A1: { pattern: 'tcpSend', service: 'logic', route: { cmd: 1, act: 2 }, c2sProto: 'X.Foo' },
      A2: { pattern: 'tcpSend', service: 'logic', route: { cmd: 1, act: 2 }, c2sProto: 'X.Foo' },
    },
    listens: { cbA: {}, cbB: {} },
  };
}

describe('routeKey 集成校验（ROUTEKEY_CODEC_MISSING）', () => {
  beforeEach(() => {
    __resetRouteKeyTemplateCacheForTest();
  });

  it('codec 缺失：listenRefs 引用的 server 无 codec → ROUTEKEY_CODEC_MISSING warning（不静默）', () => {
    const r = validateFlow(flowWithDuplicateRegister());
    const warn = r.warnings.find((w) => w.code === 'ROUTEKEY_CODEC_MISSING');
    expect(warn).toBeTruthy();
    expect(warn!.message).toContain('tcp:logic');
    // 文案不暴露 codec/schema 术语（用「协议配置/连接」）
    expect(warn!.message).toMatch(/协议配置|连接/);
  });

  it('codec 存在：注入 template 后不再产 ROUTEKEY_CODEC_MISSING', () => {
    getRouteKeyTemplatesSync().set('tcp:logic', '{cmd}:{act}');
    const r = validateFlow(flowWithDuplicateRegister());
    expect(r.warnings.find((w) => w.code === 'ROUTEKEY_CODEC_MISSING')).toBeFalsy();
  });

  it('codec 存在：两条同 route 的 listenRefs 仍命中 DUPLICATE_REGISTER（真实 routeKey 一致）', () => {
    getRouteKeyTemplatesSync().set('tcp:logic', '{cmd}:{act}');
    const r = validateFlow(flowWithDuplicateRegister());
    const dup = r.warnings.find((w) => w.code === 'DUPLICATE_REGISTER');
    expect(dup).toBeTruthy();
    // routeKey 是真实代入后的 '1:2'（而非伪 JSON 排序）
    expect(dup!.message).toContain('1:2');
  });

  it('codec 存在但 route 缺占位字段：computeRouteKey→null，降级伪 key，不产 ROUTEKEY_CODEC_MISSING', () => {
    // template 要 act，route 只给 cmd → 不可解析 → 伪 key 降级（flow 数据问题）
    getRouteKeyTemplatesSync().set('tcp:logic', '{cmd}:{act}');
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'sequence', next: ['n1'] },
        n1: {
          type: 'action',
          action: 'A1',
          listenRefs: [{ server: 'tcp:logic', route: { cmd: 1 }, listen: 'cbA' }],
        },
      },
      actions: { A1: { pattern: 'tcpSend', service: 'logic', route: { cmd: 1 }, c2sProto: 'X.Foo' } },
      listens: { cbA: {} },
    };
    const r = validateFlow(flow);
    expect(r.warnings.find((w) => w.code === 'ROUTEKEY_CODEC_MISSING')).toBeFalsy();
    expect(r.errors.length).toBe(0);
  });
});
