/**
 * 引用 / 业务校验（设计文档 §13）。
 *
 * 输出 ValidationIssue[]，每个 issue 含 severity / code / message / location。
 * 覆盖原 cmd/validate 的全部校验（Lua 语法检查除外）。
 */

import type { TaskFlow } from '@/types/flow';
import type { ActionDef, BindingType, FieldBind, FilterDef } from '@/types/action';
import { ALL_ACTION_PATTERNS, ALL_BINDING_TYPES } from '@/types/action';
import { protoRegistry } from '../proto/ProtoRegistry';
import { buildRefsGraph } from '../listens/refsGraph';

export type Severity = 'error' | 'warning' | 'info';

export interface ValidationIssue {
  severity: Severity;
  code: string;
  message: string;
  /** 用于 UI 跳转：node id / action 名 / listen 名 */
  location?: { kind: 'node' | 'action' | 'listen'; id: string };
}

export interface ValidationReport {
  errors: ValidationIssue[];
  warnings: ValidationIssue[];
  infos: ValidationIssue[];
  total: number;
}

// ── pattern 必填字段映射 ──────────────────────────────────────

const PATTERNS_REQUIRE_SERVICE = ['tcpSend', 'tcpRequest', 'tcpConnect', 'tcpClose', 'tcpListen', 'udpSend', 'udpRequest', 'udpConnect', 'udpClose', 'udpListen'];
const PATTERNS_REQUIRE_ROUTE = ['tcpSend', 'tcpRequest', 'tcpListen', 'udpSend', 'udpRequest', 'udpListen'];
const PATTERNS_REQUIRE_ADDRESS = ['tcpConnect', 'udpConnect'];
const PATTERNS_REQUIRE_C2S = ['tcpSend', 'udpSend'];
const PATTERNS_REQUIRE_S2C = ['tcpRequest', 'udpRequest'];

const VALID_NODE_TYPES = new Set(['sequence', 'action', 'loop', 'boolean', 'weighted', 'wait', 'break', 'continue']);

const VALID_FILTER_OPS = new Set([
  '', '==', '!=', '>', '>=', '<', '<=',
  'eq', 'neq', 'gt', 'gte', 'lt', 'lte',
  'contains', 'notContains', 'in', 'notIn', 'timeWindow', 'dailyTimeWindow', 'notNil', 'isNil',
]);

const VALID_FILTER_MODES = new Set(['any', 'all', 'none']);

const VALID_BINDING_TYPE_SET = new Set<string>(ALL_BINDING_TYPES);

// ── 主入口 ──────────────────────────────────────────────────

export function validateFlow(flow: TaskFlow): ValidationReport {
  const issues: ValidationIssue[] = [];
  const nodes = flow.nodes ?? {};
  const actions = flow.actions ?? {};
  const callbacks = flow.listens ?? {};

  // R1：必须有 main 节点
  if (!nodes.main) {
    issues.push({ severity: 'error', code: 'NO_MAIN', message: '缺少入口节点 "main"' });
  } else if (nodes.main.type !== 'sequence') {
    issues.push({ severity: 'warning', code: 'MAIN_NOT_SEQUENCE', message: `main 节点类型应为 sequence（当前为 ${nodes.main.type}）`, location: { kind: 'node', id: 'main' } });
  }

  // 预计算 loop body 子图（break/continue 位置检测用）
  const loopBodyNodes = collectLoopBodyNodes(nodes);

  // R2/R3/R4：节点引用合法性 + 类型校验
  for (const [id, node] of Object.entries(nodes)) {
    const ref = (target: string | undefined, field: string) => {
      if (!target) return;
      if (target === id) {
        issues.push({
          severity: 'error', code: 'NODE_SELF_REF',
          message: `节点 "${id}" 的 ${field} 指向自身（会导致无限循环）`,
          location: { kind: 'node', id },
        });
        return;
      }
      if (!nodes[target]) {
        issues.push({
          severity: 'error', code: 'NODE_REF_NOT_FOUND',
          message: `节点 "${id}" 的 ${field} 指向不存在的 "${target}"`,
          location: { kind: 'node', id },
        });
      }
    };

    // 节点类型校验
    if (!VALID_NODE_TYPES.has(node.type)) {
      issues.push({
        severity: 'error', code: 'NODE_UNKNOWN_TYPE',
        message: `节点 "${id}" 的类型 "${node.type}" 不合法`,
        location: { kind: 'node', id },
      });
      continue;
    }

    if (node.type === 'sequence') {
      (node.next ?? []).forEach((t, i) => ref(t, `next[${i}]`));
      if (!node.next || node.next.length === 0) {
        issues.push({
          severity: 'warning', code: 'EMPTY_SEQUENCE',
          message: `sequence 节点 "${id}" 的 next 为空`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'loop') {
      ref(node.body, 'body');
      if (!node.body) {
        issues.push({
          severity: 'error', code: 'LOOP_BODY_MISSING',
          message: `loop 节点 "${id}" 缺少 body`,
          location: { kind: 'node', id },
        });
      }
      if (node.loopCount === 0) {
        issues.push({
          severity: 'warning', code: 'LOOP_COUNT_ZERO',
          message: `loop 节点 "${id}" loopCount=0 无意义（不会执行循环体）`,
          location: { kind: 'node', id },
        });
      }
      const hasCount = typeof node.loopCount === 'number' && node.loopCount > 0;
      const hasCond = !!node.condition?.trim();
      const hasBreak = !!node.breakCondition?.trim();
      if (!hasCount && !hasCond && !hasBreak && node.loopCount !== 0) {
        issues.push({
          severity: 'warning', code: 'LOOP_NO_TERMINATION',
          message: `loop 节点 "${id}" 既无 loopCount 也无 condition / breakCondition，将无限循环`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'boolean') {
      if (!node.condition?.trim()) {
        issues.push({
          severity: 'error', code: 'BOOLEAN_NO_CONDITION',
          message: `boolean 节点 "${id}" 缺少 condition`,
          location: { kind: 'node', id },
        });
      }
      if (!node.trueNext && !node.falseNext) {
        issues.push({
          severity: 'error', code: 'BOOLEAN_NO_BRANCH',
          message: `boolean 节点 "${id}" 必须至少配置 trueNext 或 falseNext`,
          location: { kind: 'node', id },
        });
      }
      ref(node.trueNext, 'trueNext');
      ref(node.falseNext, 'falseNext');
    } else if (node.type === 'wait') {
      const hasRandom = typeof node.waitMin === 'number' || typeof node.waitMax === 'number';
      if (hasRandom) {
        if (typeof node.waitMin !== 'number' || typeof node.waitMax !== 'number') {
          issues.push({
            severity: 'error', code: 'WAIT_RANDOM_INCOMPLETE',
            message: `wait 节点 "${id}" 随机模式需要同时设置 waitMin 和 waitMax`,
            location: { kind: 'node', id },
          });
        } else {
          if (node.waitMin <= 0 || node.waitMax <= 0) {
            issues.push({
              severity: 'error', code: 'WAIT_RANDOM_NON_POSITIVE',
              message: `wait 节点 "${id}" waitMin/waitMax 必须 > 0`,
              location: { kind: 'node', id },
            });
          }
          if (node.waitMin >= node.waitMax) {
            issues.push({
              severity: 'error', code: 'WAIT_RANDOM_MIN_GE_MAX',
              message: `wait 节点 "${id}" waitMin 必须 < waitMax`,
              location: { kind: 'node', id },
            });
          }
        }
      } else if (typeof node.waitMs !== 'number' || node.waitMs === 0) {
        issues.push({
          severity: 'warning', code: 'WAIT_NO_MS',
          message: `wait 节点 "${id}" 缺少有效的等待时长（将直接跳过）`,
          location: { kind: 'node', id },
        });
      } else if (node.waitMs < 0) {
        issues.push({
          severity: 'error', code: 'WAIT_NEGATIVE_MS',
          message: `wait 节点 "${id}" waitMs 不能为负数`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'weighted') {
      const opts = node.options ?? [];
      if (opts.length === 0) {
        issues.push({
          severity: 'error', code: 'WEIGHTED_NO_OPTIONS',
          message: `weighted 节点 "${id}" 缺少 options`,
          location: { kind: 'node', id },
        });
      }
      const total = opts.reduce((s, o) => s + Math.max(0, o.weight), 0);
      opts.forEach((o, i) => {
        if (o.weight < 0) {
          issues.push({
            severity: 'error', code: 'WEIGHTED_NEGATIVE_WEIGHT',
            message: `weighted 节点 "${id}" options[${i}] 权重不能为负数`,
            location: { kind: 'node', id },
          });
        } else if (o.weight === 0) {
          issues.push({
            severity: 'warning', code: 'WEIGHTED_ZERO_WEIGHT',
            message: `weighted 节点 "${id}" options[${i}] 权重为 0（不会被选中）`,
            location: { kind: 'node', id },
          });
        }
      });
      if (total <= 0 && opts.length > 0) {
        issues.push({
          severity: 'error', code: 'WEIGHTED_ALL_ZERO',
          message: `weighted 节点 "${id}" 所有权重和为 0`,
          location: { kind: 'node', id },
        });
      }
      opts.forEach((o, i) => ref(o.node, `options[${i}].node`));
    } else if (node.type === 'break' || node.type === 'continue') {
      if (!loopBodyNodes.has(id)) {
        issues.push({
          severity: 'error', code: 'BREAK_CONTINUE_OUTSIDE_LOOP',
          message: `${node.type} 节点 "${id}" 不在任何 loop 内，运行时将导致错误`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'action') {
      // errorStrategy 值校验
      if (node.errorStrategy && node.errorStrategy !== 'ignore' && node.errorStrategy !== 'skip' && node.errorStrategy !== 'abort') {
        issues.push({
          severity: 'warning', code: 'INVALID_ERROR_STRATEGY',
          message: `action 节点 "${id}" 的 errorStrategy "${node.errorStrategy}" 不合法（应为 ignore、skip 或 abort）`,
          location: { kind: 'node', id },
        });
      }
      if (!node.action) {
        issues.push({
          severity: 'error', code: 'ACTION_REF_EMPTY',
          message: `action 节点 "${id}" 未指定 action 名`,
          location: { kind: 'node', id },
        });
      } else if (!actions[node.action]) {
        issues.push({
          severity: 'error', code: 'ACTION_REF_NOT_FOUND',
          message: `action 节点 "${id}" 引用了不存在的 action "${node.action}"`,
          location: { kind: 'node', id },
        });
      }
      // ListenRef 校验
      (node.listenRefs ?? []).forEach((r, i) => {
        if (!r.server?.trim()) {
          issues.push({
            severity: 'error', code: 'LISTEN_NO_SERVER',
            message: `action 节点 "${id}" listenRefs[${i}] 缺少 server`,
            location: { kind: 'node', id },
          });
        } else {
          const s = r.server.trim();
          const hasPrefix = s.startsWith('tcp:') || s.startsWith('udp:');
          if (!hasPrefix) {
            issues.push({
              severity: 'error', code: 'LISTEN_SERVER_FORMAT',
              message: `action 节点 "${id}" listenRefs[${i}] server 格式错误（应为 tcp:<name> 或 udp:<name>）`,
              location: { kind: 'node', id },
            });
          } else {
            const parts = s.split(':');
            if (parts.length < 2 || !parts[1]) {
              issues.push({
                severity: 'error', code: 'LISTEN_SERVER_FORMAT',
                message: `action 节点 "${id}" listenRefs[${i}] server 缺少服务名`,
                location: { kind: 'node', id },
              });
            }
          }
        }
      });
    }
  }

  // 孤立节点检测
  issues.push(...detectOrphanNodes(nodes));

  // R5–R10：actions 校验
  const referencedActions = new Set<string>();
  for (const node of Object.values(nodes)) {
    if (node.type === 'action' && node.action) {
      referencedActions.add(node.action);
    }
  }
  for (const [name, def] of Object.entries(actions)) {
    if (referencedActions.has(name)) {
      issues.push(...checkAction(name, def));
    } else {
      issues.push({
        severity: 'warning', code: 'ACTION_ORPHAN',
        message: `action "${name}" 未被任何节点引用`,
        location: { kind: 'action', id: name },
      });
    }
  }

  // R11–R13：listens 校验 + ref graph 校验
  const graph = buildRefsGraph(flow);
  for (const [name, cb] of Object.entries(callbacks)) {
    if (cb.script !== undefined && !cb.script?.trim()) {
      issues.push({
        severity: 'error', code: 'LISTEN_LUA_NO_SCRIPT',
        message: `listen "${name}" 是 lua 模式但缺少 script`,
        location: { kind: 'listen', id: name },
      });
    }
    if (cb.s2cProto && protoRegistry.isLoaded() && !protoRegistry.lookupMessage(cb.s2cProto)) {
      issues.push({
        severity: 'error', code: 'LISTEN_S2C_NOT_FOUND',
        message: `listen "${name}" 的 s2cProto "${cb.s2cProto}" 在 proto 中不存在`,
        location: { kind: 'listen', id: name },
      });
    }
    const refCount = graph.refCount.get(name) ?? 0;
    if (refCount === 0) {
      issues.push({
        severity: 'warning', code: 'LISTEN_ORPHAN',
        message: `listen "${name}" 未被任何 action 引用（孤儿）`,
        location: { kind: 'listen', id: name },
      });
    }
  }
  for (const dr of graph.danglingRefs) {
    issues.push({
      severity: 'error', code: 'LISTEN_CB_NOT_FOUND',
      message: `节点 "${dr.nodeId}" listenRefs[${dr.refIndex}] 引用了不存在的 listen "${dr.ref.listen}"`,
      location: { kind: 'node', id: dr.nodeId },
    });
  }
  for (const dup of graph.duplicateRegisters) {
    issues.push({
      severity: 'warning', code: 'DUPLICATE_REGISTER',
      message: `${dup.server} ${dup.routeKey} 被多次注册：${dup.refs.map((r) => `${r.nodeId}→${r.cb ?? 'null'}`).join(', ')}`,
    });
  }

  return categorize(issues);
}

// ── Action 校验 ─────────────────────────────────────────────

function checkAction(name: string, def: ActionDef): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const loc = { kind: 'action' as const, id: name };

  if (!def.pattern) {
    issues.push({ severity: 'error', code: 'ACTION_NO_PATTERN', message: `action "${name}" 缺少 pattern`, location: loc });
    return issues;
  }

  // pattern 合法性
  if (!(ALL_ACTION_PATTERNS as readonly string[]).includes(def.pattern)) {
    issues.push({ severity: 'error', code: 'ACTION_UNKNOWN_PATTERN', message: `action "${name}" 的 pattern "${def.pattern}" 不合法`, location: loc });
    return issues;
  }

  const p = def.pattern;

  // service
  if (PATTERNS_REQUIRE_SERVICE.includes(p) && !def.service) {
    issues.push({ severity: 'error', code: 'ACTION_NO_SERVICE', message: `action "${name}" (pattern=${p}) 缺少 service`, location: loc });
  }
  // route
  if (PATTERNS_REQUIRE_ROUTE.includes(p) && def.route == null) {
    issues.push({ severity: 'error', code: 'ACTION_NO_ROUTE', message: `action "${name}" (pattern=${p}) 缺少 route`, location: loc });
  }
  // address
  if (PATTERNS_REQUIRE_ADDRESS.includes(p) && !def.address) {
    issues.push({ severity: 'error', code: 'ACTION_NO_ADDRESS', message: `action "${name}" (pattern=${p}) 缺少 address`, location: loc });
  }
  // c2s/s2c proto
  if (PATTERNS_REQUIRE_C2S.includes(p) && !def.c2sProto) {
    issues.push({ severity: 'error', code: 'ACTION_NO_C2S', message: `action "${name}" (pattern=${p}) 缺少 c2sProto`, location: loc });
  }
  if (PATTERNS_REQUIRE_S2C.includes(p) && !def.s2cProto) {
    issues.push({ severity: 'error', code: 'ACTION_NO_S2C', message: `action "${name}" (pattern=${p}) 缺少 s2cProto`, location: loc });
  }
  // lua script
  if (p === 'lua' && !def.script) {
    issues.push({ severity: 'error', code: 'LUA_NO_SCRIPT', message: `action "${name}" pattern=lua 缺少 script`, location: loc });
  }
  // clearState keys
  if (p === 'clearState' && (!def.keys || def.keys.length === 0)) {
    issues.push({ severity: 'error', code: 'ACTION_NO_KEYS', message: `action "${name}" pattern=clearState 缺少 keys`, location: loc });
  }
  // httpRequest url
  if (p === 'httpRequest' && !def.url) {
    issues.push({ severity: 'error', code: 'ACTION_NO_URL', message: `action "${name}" pattern=httpRequest 缺少 url`, location: loc });
  }
  // setState with no bindings is a no-op
  if (p === 'setState' && (!def.bindings || def.bindings.length === 0)) {
    issues.push({ severity: 'warning', code: 'SETSTATE_NO_BINDINGS', message: `action "${name}" pattern=setState 缺少 bindings（无实际效果）`, location: loc });
  }

  // proto 真实存在校验
  if (protoRegistry.isLoaded()) {
    if (def.c2sProto && !protoRegistry.lookupMessage(def.c2sProto)) {
      issues.push({ severity: 'error', code: 'C2S_PROTO_NOT_FOUND', message: `action "${name}" 的 c2sProto "${def.c2sProto}" 在 proto 中不存在`, location: loc });
    }
    if (def.s2cProto && !protoRegistry.lookupMessage(def.s2cProto)) {
      issues.push({ severity: 'error', code: 'S2C_PROTO_NOT_FOUND', message: `action "${name}" 的 s2cProto "${def.s2cProto}" 在 proto 中不存在`, location: loc });
    }
    if (def.c2sProto && def.bindings) {
      const msg = protoRegistry.lookupMessage(def.c2sProto);
      if (msg) {
        // Build a set of all possible paths (up to a reasonable depth) for validation
        const validPaths = new Set<string>();
        const buildPaths = (msgName: string, prefix: string, depth: number) => {
          if (depth > 3) return;
          const m = protoRegistry.lookupMessage(msgName);
          if (!m) return;
          for (const f of m.fields) {
            const nodeName = f.repeated ? `${f.name}[0]` : f.name;
            const currentPath = prefix ? `${prefix}.${nodeName}` : nodeName;
            validPaths.add(currentPath);
            // Also allow the base name without array index for repeated fields
            if (f.repeated) {
              validPaths.add(prefix ? `${prefix}.${f.name}` : f.name);
            }
            if (f.kind === 'message' && f.messageName) {
              buildPaths(f.messageName, currentPath, depth + 1);
            }
          }
        };
        buildPaths(def.c2sProto, '', 0);

        for (const b of def.bindings) {
          if (b.field) {
            // Normalize the field path for checking (replace [1], [2] etc with [0])
            const normalizedField = b.field.replace(/\[\d+\]/g, '[0]');
            if (!validPaths.has(normalizedField)) {
              issues.push({ severity: 'warning', code: 'BINDING_FIELD_NOT_FOUND', message: `action "${name}" bindings 中字段 "${b.field}" 不存在于 ${def.c2sProto} 或其嵌套结构中`, location: loc });
            }
          }
        }
      }
    }
  }

  // binding 递归校验
  if (def.bindings) {
    issues.push(...checkBindings(`action "${name}"`, def.bindings, loc));
  }

  return issues;
}

// ── Binding 递归校验 ────────────────────────────────────────

function checkBindings(prefix: string, bindings: FieldBind[], loc: { kind: 'action'; id: string }): ValidationIssue[] {
  const issues: ValidationIssue[] = [];

  for (let i = 0; i < bindings.length; i++) {
    const b = bindings[i];
    const label = `${prefix}.bindings[${i}]`;
    const t = b.type ?? '';

    // field + storeAs 都空
    if (!b.field && !b.storeAs) {
      issues.push({ severity: 'warning', code: 'BINDING_NO_FIELD', message: `${label} 缺少 field 和 storeAs`, location: loc });
    }

    // binding type 合法性
    if (t && !VALID_BINDING_TYPE_SET.has(t)) {
      issues.push({ severity: 'error', code: 'BINDING_UNKNOWN_TYPE', message: `${label} 未知的 binding type "${t}"`, location: loc });
      continue;
    }

    // 按 type 检查必填字段
    switch (t as BindingType | '') {
      case '':
      case 'fixed':
        break;
      case 'state':
      case 'stateFirst':
      case 'stateRandom':
      case 'stateMapKey':
      case 'stateMapValue':
      case 'listSize':
        if (!b.source) {
          issues.push({ severity: 'error', code: 'BINDING_NO_SOURCE', message: `${label} type=${t} 缺少 source`, location: loc });
        }
        break;
      case 'stateRandomN':
        if (!b.source) {
          issues.push({ severity: 'error', code: 'BINDING_NO_SOURCE', message: `${label} type=stateRandomN 缺少 source`, location: loc });
        }
        if (!b.count || b.count <= 0) {
          issues.push({ severity: 'error', code: 'BINDING_NO_COUNT', message: `${label} type=stateRandomN count 必须 > 0`, location: loc });
        }
        break;
      case 'randomPick':
      case 'randomPickN':
        if (!b.values || b.values.length === 0) {
          issues.push({ severity: 'error', code: 'BINDING_NO_VALUES', message: `${label} type=${t} 缺少 values`, location: loc });
        }
        if (t === 'randomPickN' && (!b.count || b.count <= 0)) {
          issues.push({ severity: 'error', code: 'BINDING_NO_COUNT', message: `${label} type=randomPickN count 必须 > 0`, location: loc });
        }
        break;
      case 'randomPickMap':
        if (!b.values || b.values.length === 0) {
          issues.push({ severity: 'error', code: 'BINDING_NO_VALUES', message: `${label} type=randomPickMap 缺少 values`, location: loc });
        }
        if (!b.keySource) {
          issues.push({ severity: 'error', code: 'BINDING_NO_KEY_SOURCE', message: `${label} type=randomPickMap 缺少 keySource`, location: loc });
        }
        break;
      case 'randomExclude':
        if ((!b.values || b.values.length === 0) && !b.source) {
          issues.push({ severity: 'error', code: 'BINDING_NO_EXCLUDE_SOURCE', message: `${label} type=randomExclude 缺少 values 和 source`, location: loc });
        }
        break;
      case 'randomInt':
        if (b.min != null && b.max != null && b.min >= b.max) {
          issues.push({ severity: 'warning', code: 'BINDING_INVALID_RANGE', message: `${label} type=randomInt min >= max`, location: loc });
        }
        break;
      case 'randomFloat':
        if (b.min != null && b.max != null && b.min >= b.max) {
          issues.push({ severity: 'warning', code: 'BINDING_INVALID_RANGE', message: `${label} type=randomFloat min >= max`, location: loc });
        }
        break;
      case 'randomBool':
        break;
      case 'randomString':
        if (!b.length || b.length <= 0) {
          issues.push({ severity: 'error', code: 'BINDING_INVALID_LENGTH', message: `${label} type=randomString length 必须 > 0`, location: loc });
        }
        break;
    }

    // filter op 校验
    if (b.filters) {
      issues.push(...checkFilters(label, b.filters, loc));
    }
  }

  return issues;
}

// ── Filter op 校验 ──────────────────────────────────────────

function checkFilters(prefix: string, filters: FilterDef[], loc: { kind: 'action'; id: string }): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  for (let i = 0; i < filters.length; i++) {
    const f = filters[i];
    if (f.op && !VALID_FILTER_OPS.has(f.op)) {
      issues.push({ severity: 'error', code: 'FILTER_UNKNOWN_OP', message: `${prefix}.filters[${i}] 未知的 op "${f.op}"`, location: loc });
    }
    if (f.mode && !VALID_FILTER_MODES.has(f.mode)) {
      issues.push({ severity: 'error', code: 'FILTER_UNKNOWN_MODE', message: `${prefix}.filters[${i}] 未知的 mode "${f.mode}"`, location: loc });
    }
    if (f.path?.includes('[]') && !f.mode) {
      issues.push({ severity: 'warning', code: 'FILTER_ARRAY_PATH_NO_MODE', message: `${prefix}.filters[${i}] 使用数组通配路径但未配置 mode，建议选择 any / all / none`, location: loc });
    }
  }
  return issues;
}

// ── 孤立节点检测 ─────────────────────────────────────────────

function detectOrphanNodes(nodes: Record<string, { type: string; next?: string[]; body?: string; trueNext?: string; falseNext?: string; options?: Array<{ node: string }> }>): ValidationIssue[] {
  const reachable = new Set<string>();

  const visit = (id: string) => {
    if (reachable.has(id)) return;
    const node = nodes[id];
    if (!node) return;
    reachable.add(id);
    (node.next ?? []).forEach(visit);
    if (node.body) visit(node.body);
    if (node.trueNext) visit(node.trueNext);
    if (node.falseNext) visit(node.falseNext);
    (node.options ?? []).forEach((o) => visit(o.node));
  };

  visit('main');

  const issues: ValidationIssue[] = [];
  for (const id of Object.keys(nodes)) {
    if (!reachable.has(id)) {
      issues.push({
        severity: 'warning', code: 'NODE_ORPHAN',
        message: `节点 "${id}" 不可达（从 main 出发无法到达）`,
        location: { kind: 'node', id },
      });
    }
  }
  return issues;
}

// ── break/continue 位置检测 ──────────────────────────────────

type NodeLike = { type: string; next?: string[]; body?: string; trueNext?: string; falseNext?: string; options?: Array<{ node: string }> };

/** 收集所有 loop body 子图中的节点 ID */
function collectLoopBodyNodes(nodes: Record<string, NodeLike>): Set<string> {
  const inLoop = new Set<string>();
  const visit = (id: string) => {
    if (inLoop.has(id)) return;
    const node = nodes[id];
    if (!node) return;
    inLoop.add(id);
    (node.next ?? []).forEach(visit);
    if (node.body) visit(node.body);
    if (node.trueNext) visit(node.trueNext);
    if (node.falseNext) visit(node.falseNext);
    (node.options ?? []).forEach((o) => visit(o.node));
  };
  for (const node of Object.values(nodes)) {
    if (node.type === 'loop' && node.body) {
      visit(node.body);
    }
  }
  return inLoop;
}

// ── 工具 ─────────────────────────────────────────────────────

function categorize(issues: ValidationIssue[]): ValidationReport {
  const errors = issues.filter((i) => i.severity === 'error');
  const warnings = issues.filter((i) => i.severity === 'warning');
  const infos = issues.filter((i) => i.severity === 'info');
  return { errors, warnings, infos, total: issues.length };
}
