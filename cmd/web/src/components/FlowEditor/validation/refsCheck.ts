/**
 * 引用 / 业务校验（设计文档 §13）。
 *
 * 输出 ValidationIssue[]，每个 issue 含 severity / code / message / location。
 */

import type { TaskFlow } from '@/types/flow';
import type { ActionDef } from '@/types/action';
import { protoRegistry } from '../proto/ProtoRegistry';
import { buildRefsGraph } from '../callbacks/refsGraph';

export type Severity = 'error' | 'warning' | 'info';

export interface ValidationIssue {
  severity: Severity;
  code: string;
  message: string;
  /** 用于 UI 跳转：node id / action 名 / callback 名 */
  location?: { kind: 'node' | 'action' | 'callback'; id: string };
}

export interface ValidationReport {
  errors: ValidationIssue[];
  warnings: ValidationIssue[];
  infos: ValidationIssue[];
  total: number;
}

const PATTERNS_REQUIRE_C2S = ['tcpSend', 'tcpRequest', 'udpSendProto'];
const PATTERNS_REQUIRE_S2C = ['tcpRequest', 'waitListen'];
const PATTERNS_REQUIRE_SERVICE = ['tcpSend', 'tcpRequest', 'udpSendProto', 'connect', 'connectUDP', 'exchangeKey', 'close', 'waitListen'];

export function validateFlow(flow: TaskFlow): ValidationReport {
  const issues: ValidationIssue[] = [];
  const nodes = flow.nodes ?? {};
  const actions = flow.actions ?? {};
  const callbacks = flow.callbacks ?? {};

  // 必须有 main 节点（设计 §13 R1）
  if (!nodes.main) {
    issues.push({
      severity: 'error',
      code: 'NO_MAIN',
      message: '缺少入口节点 "main"',
    });
  }

  // R2/R3/R4：节点引用合法性
  for (const [id, node] of Object.entries(nodes)) {
    const ref = (target: string | undefined, field: string) => {
      if (!target) return;
      if (!nodes[target]) {
        issues.push({
          severity: 'error',
          code: 'NODE_REF_NOT_FOUND',
          message: `节点 "${id}" 的 ${field} 指向不存在的 "${target}"`,
          location: { kind: 'node', id },
        });
      }
    };
    if (node.type === 'sequence') {
      (node.next ?? []).forEach((t, i) => ref(t, `next[${i}]`));
      if (!node.next || node.next.length === 0) {
        issues.push({
          severity: 'warning',
          code: 'EMPTY_SEQUENCE',
          message: `sequence 节点 "${id}" 的 next 为空`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'loop') {
      ref(node.body, 'body');
      if (!node.body) {
        issues.push({
          severity: 'error',
          code: 'LOOP_BODY_MISSING',
          message: `loop 节点 "${id}" 缺少 body`,
          location: { kind: 'node', id },
        });
      }
      // loopCount 与 condition 至少一个：否则没有终止条件，会无限循环
      const hasCount = typeof node.loopCount === 'number' && node.loopCount > 0;
      const hasCond = !!node.condition?.trim();
      const hasBreak = !!node.breakCondition?.trim();
      if (!hasCount && !hasCond && !hasBreak) {
        issues.push({
          severity: 'warning',
          code: 'LOOP_NO_TERMINATION',
          message: `loop 节点 "${id}" 既无 loopCount 也无 condition / breakCondition，将无限循环`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'boolean') {
      if (!node.condition?.trim()) {
        issues.push({
          severity: 'error',
          code: 'BOOLEAN_NO_CONDITION',
          message: `boolean 节点 "${id}" 缺少 condition`,
          location: { kind: 'node', id },
        });
      }
      if (!node.trueNext && !node.falseNext) {
        issues.push({
          severity: 'error',
          code: 'BOOLEAN_NO_BRANCH',
          message: `boolean 节点 "${id}" 必须至少配置 trueNext 或 falseNext`,
          location: { kind: 'node', id },
        });
      }
      ref(node.trueNext, 'trueNext');
      ref(node.falseNext, 'falseNext');
    } else if (node.type === 'wait') {
      if (typeof node.waitMs !== 'number' || node.waitMs <= 0) {
        issues.push({
          severity: 'error',
          code: 'WAIT_NO_MS',
          message: `wait 节点 "${id}" 缺少有效的 waitMs（必须为正整数）`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'weighted') {
      const opts = node.options ?? [];
      if (opts.length === 0) {
        issues.push({
          severity: 'error',
          code: 'WEIGHTED_NO_OPTIONS',
          message: `weighted 节点 "${id}" 缺少 options`,
          location: { kind: 'node', id },
        });
      }
      const total = opts.reduce((s, o) => s + Math.max(0, o.weight), 0);
      if (total <= 0 && opts.length > 0) {
        issues.push({
          severity: 'error',
          code: 'WEIGHTED_ALL_ZERO',
          message: `weighted 节点 "${id}" 所有权重和为 0`,
          location: { kind: 'node', id },
        });
      }
      opts.forEach((o, i) => ref(o.node, `options[${i}].node`));
    } else if (node.type === 'action') {
      if (!node.action) {
        issues.push({
          severity: 'error',
          code: 'ACTION_REF_EMPTY',
          message: `action 节点 "${id}" 未指定 action 名`,
          location: { kind: 'node', id },
        });
      } else if (!actions[node.action]) {
        issues.push({
          severity: 'error',
          code: 'ACTION_REF_NOT_FOUND',
          message: `action 节点 "${id}" 引用了不存在的 action "${node.action}"`,
          location: { kind: 'node', id },
        });
      }
      // 监听项 server 必填（callback 可为 null 表示静默丢弃）
      (node.listenCallbacks ?? []).forEach((r, i) => {
        if (!r.server?.trim()) {
          issues.push({
            severity: 'error',
            code: 'LISTEN_NO_SERVER',
            message: `action 节点 "${id}" listenCallbacks[${i}] 缺少 server（如 tcp:logic / udp:battle）`,
            location: { kind: 'node', id },
          });
        }
      });
    }
  }

  // R5–R10：actions 校验
  for (const [name, def] of Object.entries(actions)) {
    issues.push(...checkAction(name, def));
  }

  // R11–R13：callbacks 校验 + ref graph 校验
  const graph = buildRefsGraph(flow);
  for (const [name, cb] of Object.entries(callbacks)) {
    if (cb.s2cProto && protoRegistry.isLoaded() && !protoRegistry.lookupMessage(cb.s2cProto)) {
      issues.push({
        severity: 'error',
        code: 'CALLBACK_S2C_NOT_FOUND',
        message: `callback "${name}" 的 s2cProto "${cb.s2cProto}" 在 proto 中不存在`,
        location: { kind: 'callback', id: name },
      });
    }
    const refCount = graph.refCount.get(name) ?? 0;
    if (refCount === 0) {
      issues.push({
        severity: 'warning',
        code: 'CALLBACK_ORPHAN',
        message: `callback "${name}" 未被任何 action 引用（孤儿）`,
        location: { kind: 'callback', id: name },
      });
    }
  }
  // R14：listenCallbacks 中引用了不存在的 callback
  for (const dr of graph.danglingRefs) {
    issues.push({
      severity: 'error',
      code: 'LISTEN_CB_NOT_FOUND',
      message: `节点 "${dr.nodeId}" listenCallbacks[${dr.refIndex}] 引用了不存在的 callback "${dr.ref.callback}"`,
      location: { kind: 'node', id: dr.nodeId },
    });
  }
  // R15：相同 server+route 被多个 action 注册了不同 callback
  for (const dup of graph.duplicateRegisters) {
    issues.push({
      severity: 'warning',
      code: 'DUPLICATE_REGISTER',
      message: `${dup.server} ${dup.routeKey} 被多次注册：${dup.refs.map((r) => `${r.nodeId}→${r.cb ?? 'null'}`).join(', ')}`,
    });
  }

  return categorize(issues);
}

function checkAction(name: string, def: ActionDef): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const loc = { kind: 'action' as const, id: name };
  // pattern 必填
  if (!def.pattern) {
    issues.push({ severity: 'error', code: 'ACTION_NO_PATTERN', message: `action "${name}" 缺少 pattern`, location: loc });
    return issues;
  }
  // service 必填
  if (PATTERNS_REQUIRE_SERVICE.includes(def.pattern) && !def.service) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_SERVICE',
      message: `action "${name}" (pattern=${def.pattern}) 缺少 service`,
      location: loc,
    });
  }
  // c2s/s2c proto 必填
  if (PATTERNS_REQUIRE_C2S.includes(def.pattern) && !def.c2sProto) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_C2S',
      message: `action "${name}" (pattern=${def.pattern}) 缺少 c2sProto`,
      location: loc,
    });
  }
  if (PATTERNS_REQUIRE_S2C.includes(def.pattern) && !def.s2cProto) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_S2C',
      message: `action "${name}" (pattern=${def.pattern}) 缺少 s2cProto`,
      location: loc,
    });
  }
  // proto 真实存在校验
  if (protoRegistry.isLoaded()) {
    if (def.c2sProto && !protoRegistry.lookupMessage(def.c2sProto)) {
      issues.push({
        severity: 'error',
        code: 'C2S_PROTO_NOT_FOUND',
        message: `action "${name}" 的 c2sProto "${def.c2sProto}" 在 proto 中不存在`,
        location: loc,
      });
    }
    if (def.s2cProto && !protoRegistry.lookupMessage(def.s2cProto)) {
      issues.push({
        severity: 'error',
        code: 'S2C_PROTO_NOT_FOUND',
        message: `action "${name}" 的 s2cProto "${def.s2cProto}" 在 proto 中不存在`,
        location: loc,
      });
    }
    // 字段绑定的 field 是否存在
    if (def.c2sProto && def.bindings) {
      const msg = protoRegistry.lookupMessage(def.c2sProto);
      if (msg) {
        const fieldSet = new Set(msg.fields.map((f) => f.name));
        for (const b of def.bindings) {
          if (b.field && !fieldSet.has(b.field)) {
            issues.push({
              severity: 'warning',
              code: 'BINDING_FIELD_NOT_FOUND',
              message: `action "${name}" bindings 中字段 "${b.field}" 不存在于 ${def.c2sProto}`,
              location: loc,
            });
          }
        }
      }
    }
  }
  // lua 必须有 script
  if (def.pattern === 'lua' && !def.script) {
    issues.push({
      severity: 'error',
      code: 'LUA_NO_SCRIPT',
      message: `action "${name}" pattern=lua 缺少 script`,
      location: loc,
    });
  }
  return issues;
}

function categorize(issues: ValidationIssue[]): ValidationReport {
  const errors = issues.filter((i) => i.severity === 'error');
  const warnings = issues.filter((i) => i.severity === 'warning');
  const infos = issues.filter((i) => i.severity === 'info');
  return { errors, warnings, infos, total: issues.length };
}
