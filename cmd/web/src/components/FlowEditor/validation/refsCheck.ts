/**
 * 引用 / 业务校验（设计文档 §13）。
 *
 * 输出 ValidationIssue[]，每个 issue 含 severity / code / message / location。
 * 覆盖原 cmd/validate 的全部校验（Lua 语法检查除外）。
 */

import type { OnErrorStrategy, TaskFlow } from '@/types/flow';
import type { ActionDef, BindingType, FieldBind, FilterDef } from '@/types/action';
import { ALL_ACTION_PATTERNS, ALL_BINDING_TYPES } from '@/types/action';
import { protoRegistry } from '../proto/ProtoRegistry';
import { buildRefsGraph } from '../listens/refsGraph';
import { resolveRouteKeyForServer } from '../listens/routeKeyResolver';
import { resolveRandomStringCharset } from '../editors/ActionEditor/randomStringCharset';
import { isBuiltinStateKey, type StateKeyInfo } from '../editors/ActionEditor/stateRegistry';
import { validateFlowStructure } from '@/services/schemaValidator';

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

const REMOVED_HEARTBEAT_PATTERNS = new Set(['tcpHeartbeat', 'udpHeartbeat']);

const VALID_NODE_TYPES = new Set(['sequence', 'action', 'loop', 'boolean', 'switch', 'weighted', 'wait', 'break', 'continue']);
const VALID_ON_ERROR_STRATEGIES = new Set<OnErrorStrategy>(['resume', 'skip', 'abort']);

const VALID_FILTER_OPS = new Set([
  '', '==', '!=', '>', '>=', '<', '<=',
  'eq', 'neq', 'gt', 'gte', 'lt', 'lte',
  'contains', 'notContains', 'in', 'notIn', 'notNil', 'isNil',
]);

const VALID_FILTER_MODES = new Set(['any', 'all', 'none']);

const VALID_BINDING_TYPE_SET = new Set<string>(ALL_BINDING_TYPES);

// ── 主入口 ──────────────────────────────────────────────────

/**
 * 校验上下文：携带校验所需的运行时外部数据（可选）。
 * - stateKeys / stateKeysReady：状态注册表候选。仅当 ready（脚本异步加载完成）时
 *   才用于 CLEARSTATE_UNKNOWN_KEY 检测，避免加载途中误报未知 key。
 */
export interface FlowValidationContext {
  stateKeys?: StateKeyInfo[];
  stateKeysReady?: boolean;
}

export function validateFlow(flow: TaskFlow, context: FlowValidationContext = {}): ValidationReport {
  const issues: ValidationIssue[] = [];
  const structuralIssues = validateFlowStructure(flow);
  if (structuralIssues.length > 0) {
    issues.push(...structuralIssues.map((issue) => ({
      severity: 'error' as const,
      code: 'FLOW_SCHEMA_INVALID',
      message: `${issue.path}：${issue.message}`,
    })));
  }
  const nodes = flow.nodes ?? {};
  const actions = flow.actions ?? {};
  const listens = flow.listens ?? {};

  // 仅当状态注册表 ready 时才建立已知 key 集合，供 clearState 未知 key 检测；
  // 未 ready 时为 undefined，CLEARSTATE_UNKNOWN_KEY 检测整体跳过（避免误报）。
  const knownStateKeys = context.stateKeysReady
    ? new Set((context.stateKeys ?? []).map((info) => info.key))
    : undefined;

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
      const next = node.next ?? [];
      next.forEach((t, i) => ref(t, `next[${i}]`));
      for (let i = 0; i < next.length - 1; i++) {
        const waitId = next[i];
        const targetId = next[i + 1];
        const waitNode = nodes[waitId];
        if (waitNode?.type === 'wait' && waitNode.then?.trim() === targetId) {
          issues.push({
            severity: 'warning',
            code: 'WAIT_THEN_DUPLICATE_SEQUENCE_NEXT',
            message: `wait 节点 "${waitId}" 的 then 与 sequence "${id}" 中的下一项相同，目标 "${targetId}" 将执行两次`,
            location: { kind: 'node', id: waitId },
          });
        }
      }
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
    } else if (node.type === 'switch') {
      const cases = node.cases ?? [];
      if (cases.length === 0) {
        issues.push({
          severity: 'error', code: 'SWITCH_NO_CASES',
          message: `switch 节点 "${id}" 缺少 cases`,
          location: { kind: 'node', id },
        });
      }
      cases.forEach((c, i) => {
        if (!c.condition?.trim()) {
          issues.push({
            severity: 'error', code: 'SWITCH_CASE_NO_CONDITION',
            message: `switch 节点 "${id}" cases[${i}] 缺少 condition`,
            location: { kind: 'node', id },
          });
        }
        if (!c.next?.trim()) {
          issues.push({
            severity: 'error', code: 'SWITCH_CASE_NO_NEXT',
            message: `switch 节点 "${id}" cases[${i}] 缺少 next`,
            location: { kind: 'node', id },
          });
        }
        ref(c.next?.trim(), `cases[${i}].next`);
      });
      ref(node.defaultNext?.trim(), 'defaultNext');
      if (!node.defaultNext?.trim()) {
        issues.push({
          severity: 'warning', code: 'SWITCH_NO_DEFAULT',
          message: `switch 节点 "${id}" 未配置 defaultNext，所有条件未命中时将直接结束`,
          location: { kind: 'node', id },
        });
      }
    } else if (node.type === 'wait') {
      ref(node.then?.trim(), 'then');
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
      issues.push(...checkNodeOnError(id, node, nodes));
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
        // listen:null / undefined 是合法的「静默预注册」语义（仅注册缓存队列、不回调），
        // 后端 ref.Listen=="" 时 cb=nil 并照常入队（engine/flow.go RegisterListen）。
        // 因此不再把「未指定 listen」判为错误；只有显式写了 listen 但引用不存在的定义才算错（下方）。
        const silent = r.listen === null || r.listen === undefined || r.listen === '';
        if (!silent) {
          const ln = r.listen as string;
          if (!(ln in listens)) {
            issues.push({
              severity: 'error', code: 'LISTEN_REF_NOT_FOUND',
              message: `action 节点 "${id}" listenRefs[${i}] 引用了不存在的 listen "${ln}"`,
              location: { kind: 'node', id },
            });
          }
        }
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
        // queueSize：未写合法（缺省 1）；显式 <=0 报错（不静默 clamp，与后端 fail-loud 一致）
        if (r.queueSize !== undefined && r.queueSize !== null && r.queueSize <= 0) {
          issues.push({
            severity: 'error', code: 'LISTEN_QUEUE_INVALID',
            message: `action 节点 "${id}" listenRefs[${i}] queueSize 必须 > 0（当前 ${r.queueSize}，缺省 1）`,
            location: { kind: 'node', id },
          });
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
      issues.push(...checkAction(name, def, knownStateKeys));
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
  for (const [name, listenDef] of Object.entries(listens)) {
    if (listenDef.script !== undefined && !listenDef.script?.trim()) {
      issues.push({
        severity: 'error', code: 'LISTEN_LUA_NO_SCRIPT',
        message: `listen "${name}" 是 lua 模式但缺少 script`,
        location: { kind: 'listen', id: name },
      });
    }
    if (listenDef.s2cProto && protoRegistry.isLoaded() && !protoRegistry.lookupMessage(listenDef.s2cProto)) {
      issues.push({
        severity: 'error', code: 'LISTEN_S2C_NOT_FOUND',
        message: `listen "${name}" 的 s2cProto "${listenDef.s2cProto}" 在 proto 中不存在`,
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

  // 连接的协议配置缺失/有误 → listen 去重只能用近似 routeKey，显式提示先修复协议配置。
  // 不静默伪 key（设计 §3.7）：每个缺失 server 一条 warning。
  for (const server of graph.missingCodecServers) {
    issues.push({
      severity: 'warning',
      code: 'ROUTEKEY_CODEC_MISSING',
      message: `连接 ${server} 的协议配置缺失或有误，listen 去重使用近似匹配，请先在协议配置面板修复`,
    });
  }

  // R14：tcpListen/udpListen 的 route 必须在某个节点的 listenRefs 中预注册
  const LISTEN_ACTION_PATTERNS = new Set(['tcpListen', 'udpListen']);
  const preRegisteredKeys = new Set<string>();
  for (const node of Object.values(nodes)) {
    if (node.type !== 'action') continue;
    for (const ref of node.listenRefs ?? []) {
      if (ref.server && ref.route != null) {
        // 与 buildRefsGraph 用同一个 server 感知解析器，保证 key 一致
        preRegisteredKeys.add(`${ref.server}|${resolveRouteKeyForServer(ref.server, ref.route)}`);
      }
    }
  }
  for (const [actionName, def] of Object.entries(actions)) {
    if (!LISTEN_ACTION_PATTERNS.has(def.pattern)) continue;
    if (!referencedActions.has(actionName)) continue;
    if (!def.service || def.route == null) continue;
    const proto = def.pattern === 'tcpListen' ? 'tcp' : 'udp';
    const server = `${proto}:${def.service}`;
    const key = `${server}|${resolveRouteKeyForServer(server, def.route)}`;
    if (!preRegisteredKeys.has(key)) {
      issues.push({
        severity: 'warning',
        code: 'LISTEN_NO_PREREG',
        message: `action "${actionName}" (${def.pattern}) 的 route 未通过 listenRefs 预注册，运行时将始终超时`,
        location: { kind: 'action', id: actionName },
      });
    }
  }

  return categorize(issues);
}

// ── Action 校验 ─────────────────────────────────────────────

function checkAction(name: string, def: ActionDef, knownStateKeys: Set<string> | undefined): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const loc = { kind: 'action' as const, id: name };

  if (!def.pattern) {
    issues.push({ severity: 'error', code: 'ACTION_NO_PATTERN', message: `action "${name}" 缺少 pattern`, location: loc });
    return issues;
  }

  // pattern 合法性
  if (!(ALL_ACTION_PATTERNS as readonly string[]).includes(def.pattern)) {
    const removedHint = REMOVED_HEARTBEAT_PATTERNS.has(def.pattern)
      ? '。心跳已迁移到协议连接配置的 heartbeat 中，请在对应连接里配置'
      : '';
    issues.push({ severity: 'error', code: 'ACTION_UNKNOWN_PATTERN', message: `action "${name}" 的 pattern "${def.pattern}" 不合法${removedHint}`, location: loc });
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
  // httpRequest method / contentType 白名单：导入路径只做 JSON.parse cast，这里兜住非法值，
  // 否则 method 被原样透传、未知 contentType 让请求体为空，到运行期才失败。
  if (p === 'httpRequest') {
    const method = typeof def.method === 'string' && def.method ? def.method.toUpperCase() : 'GET';
    if (!['GET', 'POST'].includes(method)) {
      issues.push({ severity: 'error', code: 'HTTP_METHOD_INVALID', message: `action "${name}" pattern=httpRequest 的 method "${def.method}" 不合法（仅支持 GET / POST）`, location: loc });
    }
    // 导入的 JSON 经 cast 后类型是 'json'|'form'，但运行时可能是任意字符串；这里兜住非法值。
    const ct = def.contentType as string | undefined;
    if (ct !== undefined && ct !== null && ct !== '' && !['json', 'form'].includes(ct)) {
      issues.push({ severity: 'error', code: 'HTTP_CONTENT_TYPE_INVALID', message: `action "${name}" pattern=httpRequest 的 contentType "${ct}" 不合法（仅支持 json / form）`, location: loc });
    }
  }
  // setState with no bindings is a no-op
  if (p === 'setState' && (!def.bindings || def.bindings.length === 0)) {
    issues.push({ severity: 'warning', code: 'SETSTATE_NO_BINDINGS', message: `action "${name}" pattern=setState 缺少 bindings（无实际效果）`, location: loc });
  }
  // timeout / pollMs 整数契约（Go 端为 int）：小数会让后端 json.Unmarshal 失败。
  if (typeof def.timeout === 'number' && !Number.isInteger(def.timeout)) {
    issues.push({ severity: 'error', code: 'ACTION_TIMEOUT_NON_INTEGER', message: `action "${name}" 的 timeout 必须是整数（当前 ${def.timeout}）`, location: loc });
  }
  if (typeof def.pollMs === 'number' && !Number.isInteger(def.pollMs)) {
    issues.push({ severity: 'error', code: 'ACTION_POLLMS_NON_INTEGER', message: `action "${name}" 的 pollMs 必须是整数（当前 ${def.pollMs}）`, location: loc });
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

  // setState 语义：每条 binding 必须有目标 field（空目标是编辑器里未填的脏数据），
  // 重复目标给出 warning（后项会覆盖前项，合法但可疑）。
  // 这一组检查在通用 checkBindings 之前：专门用 SETSTATE_TARGET_MISSING 取代对该场景的
  // BINDING_NO_FIELD 通用 warning（下方 checkBindings 通过 requireTargetField=false 抑制）。
  if (p === 'setState' && def.bindings) {
    const firstByTarget = new Map<string, number>();
    def.bindings.forEach((binding, index) => {
      const target = binding.field?.trim();
      if (!target) {
        issues.push({ severity: 'error', code: 'SETSTATE_TARGET_MISSING', message: `action "${name}" 的第 ${index + 1} 条状态写入缺少目标状态`, location: loc });
        return;
      }
      const previous = firstByTarget.get(target);
      if (previous !== undefined) {
        issues.push({ severity: 'warning', code: 'SETSTATE_TARGET_DUPLICATE', message: `action "${name}" 的第 ${index + 1} 条写入会覆盖第 ${previous + 1} 条对状态 "${target}" 的写入`, location: loc });
      } else {
        firstByTarget.set(target, index);
      }
    });
  }

  // clearState 语义：内置 key（id/index/account）由后端 Task 2 守卫保护、禁止清除；
  // 重复 key 与当前流程未识别的 key 给出 warning（未识别检测仅在状态注册表 ready 时启用）。
  if (p === 'clearState' && def.keys) {
    const seen = new Set<string>();
    for (const key of def.keys) {
      if (isBuiltinStateKey(key)) {
        issues.push({ severity: 'error', code: 'CLEARSTATE_PROTECTED_KEY', message: `action "${name}" 不允许清除内置状态 "${key}"`, location: loc });
      }
      if (seen.has(key)) {
        issues.push({ severity: 'warning', code: 'CLEARSTATE_DUPLICATE_KEY', message: `action "${name}" 重复清除状态 "${key}"`, location: loc });
      }
      seen.add(key);
      if (knownStateKeys && !knownStateKeys.has(key) && !isBuiltinStateKey(key)) {
        issues.push({ severity: 'warning', code: 'CLEARSTATE_UNKNOWN_KEY', message: `action "${name}" 要清除的状态 "${key}" 当前流程未识别`, location: loc });
      }
    }
  }

  // binding 递归校验：setState 已由上方 SETSTATE_TARGET_MISSING 专门检查空目标，
  // 这里通过 requireTargetField=false 抑制通用 BINDING_NO_FIELD warning，避免重复告警；
  // 其余 pattern 保持默认（requireTargetField=true），缺 field 且无 storeAs 仍告警。
  if (def.bindings) {
    issues.push(...checkBindings(`action "${name}"`, def.bindings, loc, false, { requireTargetField: p !== 'setState' }));
  }

  return issues;
}

// ── action 节点 onError 校验 ───────────────────────────────

function checkNodeOnError(id: string, node: TaskFlow['nodes'][string], nodes: TaskFlow['nodes']): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const onError = node.onError;
  if (!onError) return issues;

  if (onError.strategy && !VALID_ON_ERROR_STRATEGIES.has(onError.strategy)) {
    issues.push({
      severity: 'error', code: 'ON_ERROR_STRATEGY_INVALID',
      message: `action 节点 "${id}" 的 onError.strategy "${onError.strategy}" 不合法（应为 resume、skip 或 abort）`,
      location: { kind: 'node', id },
    });
  }

  (onError.ignoreCodes ?? []).forEach((code, i) => {
    if (!Number.isInteger(code) || code <= 0) {
      issues.push({
        severity: 'error', code: 'ON_ERROR_IGNORE_CODE_INVALID',
        message: `action 节点 "${id}" 的 onError.ignoreCodes[${i}] 必须是正整数（当前 ${code}）`,
        location: { kind: 'node', id },
      });
    }
  });

  if (onError.handler) {
    if (onError.handler === id) {
      issues.push({
        severity: 'error', code: 'ON_ERROR_HANDLER_SELF',
        message: `action 节点 "${id}" 的 onError.handler 不能指向自身`,
        location: { kind: 'node', id },
      });
    } else if (!nodes[onError.handler]) {
      issues.push({
        severity: 'error', code: 'ON_ERROR_HANDLER_NOT_FOUND',
        message: `action 节点 "${id}" 的 onError.handler 指向不存在的 "${onError.handler}"`,
        location: { kind: 'node', id },
      });
    }
  }

  if (onError.retry) {
    if (onError.retry.maxRetries !== undefined && onError.retry.maxRetries < 0) {
      issues.push({
        severity: 'error', code: 'ON_ERROR_RETRY_MAX_INVALID',
        message: `action 节点 "${id}" 的 onError.retry.maxRetries 必须 >= 0`,
        location: { kind: 'node', id },
      });
    }
    if (onError.retry.retryDelayMs !== undefined && onError.retry.retryDelayMs < 0) {
      issues.push({
        severity: 'error', code: 'ON_ERROR_RETRY_DELAY_INVALID',
        message: `action 节点 "${id}" 的 onError.retry.retryDelayMs 必须 >= 0`,
        location: { kind: 'node', id },
      });
    }
  }

  return issues;
}

// ── Binding 递归校验 ────────────────────────────────────────

function checkBindings(
  prefix: string,
  bindings: FieldBind[],
  loc: { kind: 'action'; id: string },
  isMapEntryValue = false,
  options: { requireTargetField?: boolean } = {},
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];

  for (let i = 0; i < bindings.length; i++) {
    const b = bindings[i];
    const label = `${prefix}.bindings[${i}]`;
    const t = b.type ?? '';

    // field + storeAs 都空（map entry 内的 value binding 是纯值生成器，不需要 field/storeAs；
    // setState 由 SETSTATE_TARGET_MISSING 专门检查，requireTargetField=false 时跳过）。
    if (!isMapEntryValue && options.requireTargetField !== false && !b.field && !b.storeAs) {
      issues.push({ severity: 'warning', code: 'BINDING_NO_FIELD', message: `${label} 缺少 field 和 storeAs`, location: loc });
    }

    // binding type 合法性
    if (t && !VALID_BINDING_TYPE_SET.has(t)) {
      issues.push({ severity: 'error', code: 'BINDING_UNKNOWN_TYPE', message: `${label} 未知的 binding type "${t}"`, location: loc });
      continue;
    }

    // 整数契约：min/max/length/count/precision 在 Go 端为 int，前端不得存小数。
    // 小数会导致后端 json.Unmarshal 到 int 失败、任务无法加载。
    for (const [fname, fval] of [
      ['min', b.min], ['max', b.max], ['length', b.length], ['count', b.count], ['precision', b.precision],
    ] as const) {
      if (typeof fval === 'number' && !Number.isInteger(fval)) {
        issues.push({ severity: 'error', code: 'BINDING_NON_INTEGER', message: `${label} 的 ${fname} 必须是整数（当前 ${fval}）`, location: loc });
      }
    }
    // required 与 optional 互斥：Go 在缺值时 optional 优先（静默跳过），二者同开会让 UI 的 required 承诺失效。
    if (b.required && b.optional) {
      issues.push({ severity: 'warning', code: 'BINDING_REQUIRED_OPTIONAL_CONFLICT', message: `${label} 的 required 与 optional 互斥，运行时按 optional 处理（缺失时跳过）`, location: loc });
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
        if (b.charset != null && b.charset.trim().length === 0) {
          issues.push({ severity: 'error', code: 'BINDING_INVALID_CHARSET', message: `${label} type=randomString charset 不能为空`, location: loc });
        } else if (b.charset != null && resolveRandomStringCharset(b.charset).length === 0) {
          issues.push({ severity: 'error', code: 'BINDING_INVALID_CHARSET', message: `${label} type=randomString charset 不能为空`, location: loc });
        }
        break;
      case 'map':
        if (!b.entries || b.entries.length === 0) {
          issues.push({ severity: 'error', code: 'BINDING_MAP_NO_ENTRIES', message: `${label} type=map 缺少 entries`, location: loc });
        } else {
          for (let ei = 0; ei < b.entries.length; ei++) {
            const entry = b.entries[ei];
            const entryLabel = `${label}.entries[${ei}]`;
            if (entry.key === undefined || entry.key === null || entry.key === '') {
              issues.push({ severity: 'error', code: 'BINDING_MAP_ENTRY_NO_KEY', message: `${entryLabel} 缺少 key`, location: loc });
            }
            if (!entry.value) {
              issues.push({ severity: 'error', code: 'BINDING_MAP_ENTRY_NO_VALUE', message: `${entryLabel} 缺少 value`, location: loc });
            } else if (entry.value.type === 'map') {
              issues.push({ severity: 'error', code: 'BINDING_MAP_ENTRY_VALUE_MAP', message: `${entryLabel} value 不允许嵌套 map 类型`, location: loc });
            } else {
              issues.push(...checkBindings(entryLabel, [entry.value], loc, true));
            }
          }
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

function detectOrphanNodes(nodes: Record<string, NodeLike>): ValidationIssue[] {
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
    if (node.onError?.handler) visit(node.onError.handler);
    (node.cases ?? []).forEach((c) => {
      if (c.next) visit(c.next);
    });
    if (node.defaultNext) visit(node.defaultNext);
    if (node.then) visit(node.then);
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

type NodeLike = { type: string; next?: string[]; body?: string; trueNext?: string; falseNext?: string; options?: Array<{ node: string }>; onError?: { handler?: string }; cases?: Array<{ next: string }>; defaultNext?: string; then?: string };

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
    if (node.onError?.handler) visit(node.onError.handler);
    (node.cases ?? []).forEach((c) => {
      if (c.next) visit(c.next);
    });
    if (node.defaultNext) visit(node.defaultNext);
    if (node.then) visit(node.then);
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
