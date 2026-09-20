/**
 * 动作级校验规则：单条 action 定义的完整性，以及 action 节点的 onError 配置。
 *
 * 从 refsCheck 拆出：这里只看一条 action 自己（加上状态注册表与 proto 注册表），
 * 跨节点的引用关系仍由 refsCheck 负责。
 */

import type { ActionDef } from '@/types/action';
import { ALL_ACTION_PATTERNS } from '@/types/action';
import type { OnErrorStrategy, TaskFlow } from '@/types/flow';
import { protoRegistry } from '../proto/ProtoRegistry';
import { isBuiltinStateKey } from '../editors/ActionEditor/stateRegistry';
import { checkBindings } from './bindingRules';
import type { ValidationIssue } from './issues';

// ── pattern 必填字段映射 ──────────────────────────────────────

const PATTERNS_REQUIRE_SERVICE = [
  'tcpSend',
  'tcpRequest',
  'tcpConnect',
  'tcpClose',
  'tcpListen',
  'udpSend',
  'udpRequest',
  'udpConnect',
  'udpClose',
  'udpListen',
];
const PATTERNS_REQUIRE_ROUTE = [
  'tcpSend',
  'tcpRequest',
  'tcpListen',
  'udpSend',
  'udpRequest',
  'udpListen',
];
const PATTERNS_REQUIRE_ADDRESS = ['tcpConnect', 'udpConnect'];
const PATTERNS_REQUIRE_C2S = ['tcpSend', 'udpSend'];
const PATTERNS_REQUIRE_S2C = ['tcpRequest', 'udpRequest'];

const REMOVED_HEARTBEAT_PATTERNS = new Set(['tcpHeartbeat', 'udpHeartbeat']);

const VALID_ON_ERROR_STRATEGIES = new Set<OnErrorStrategy>(['resume', 'skip', 'abort']);

/** 一次校验内复用的「proto 消息 → 合法字段路径集」缓存，见 fieldPathsOf。 */
export type FieldPathCache = Map<string, Set<string>>;

/**
 * 展开消息的合法字段路径（含嵌套，深度上限 3）。
 *
 * 结果按消息名缓存：同一个 c2sProto 常被多个 action 共用，逐 action 重新递归整棵
 * 消息树是纯浪费。缓存只活在一次校验里（由调用方持有），proto 重新加载后自然失效。
 */
function fieldPathsOf(msgName: string, cache: FieldPathCache): Set<string> {
  const cached = cache.get(msgName);
  if (cached) return cached;

  const paths = new Set<string>();
  const walk = (current: string, prefix: string, depth: number) => {
    if (depth > 3) return;
    const m = protoRegistry.lookupMessage(current);
    if (!m) return;
    for (const f of m.fields) {
      const nodeName = f.repeated ? `${f.name}[0]` : f.name;
      const currentPath = prefix ? `${prefix}.${nodeName}` : nodeName;
      paths.add(currentPath);
      // repeated 字段同时允许不带下标的写法
      if (f.repeated) {
        paths.add(prefix ? `${prefix}.${f.name}` : f.name);
      }
      if (f.kind === 'message' && f.messageName) {
        walk(f.messageName, currentPath, depth + 1);
      }
    }
  };
  walk(msgName, '', 0);

  cache.set(msgName, paths);
  return paths;
}

// ── Action 校验 ─────────────────────────────────────────────

export function checkAction(
  name: string,
  def: ActionDef,
  knownStateKeys: Set<string> | undefined,
  fieldPathCache: FieldPathCache = new Map(),
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const loc = { kind: 'action' as const, id: name };

  if (!def.pattern) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_PATTERN',
      message: `action "${name}" 缺少 pattern`,
      location: loc,
    });
    return issues;
  }

  // pattern 合法性
  if (!(ALL_ACTION_PATTERNS as readonly string[]).includes(def.pattern)) {
    const removedHint = REMOVED_HEARTBEAT_PATTERNS.has(def.pattern)
      ? '。心跳已迁移到协议连接配置的 heartbeat 中，请在对应连接里配置'
      : '';
    issues.push({
      severity: 'error',
      code: 'ACTION_UNKNOWN_PATTERN',
      message: `action "${name}" 的 pattern "${def.pattern}" 不合法${removedHint}`,
      location: loc,
    });
    return issues;
  }

  const p = def.pattern;

  // service
  if (PATTERNS_REQUIRE_SERVICE.includes(p) && !def.service) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_SERVICE',
      message: `action "${name}" (pattern=${p}) 缺少 service`,
      location: loc,
    });
  }
  // route
  if (PATTERNS_REQUIRE_ROUTE.includes(p) && def.route == null) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_ROUTE',
      message: `action "${name}" (pattern=${p}) 缺少 route`,
      location: loc,
    });
  }
  // address
  if (PATTERNS_REQUIRE_ADDRESS.includes(p) && !def.address) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_ADDRESS',
      message: `action "${name}" (pattern=${p}) 缺少 address`,
      location: loc,
    });
  }
  // c2s/s2c proto
  if (PATTERNS_REQUIRE_C2S.includes(p) && !def.c2sProto) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_C2S',
      message: `action "${name}" (pattern=${p}) 缺少 c2sProto`,
      location: loc,
    });
  }
  if (PATTERNS_REQUIRE_S2C.includes(p) && !def.s2cProto) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_S2C',
      message: `action "${name}" (pattern=${p}) 缺少 s2cProto`,
      location: loc,
    });
  }
  // lua script
  if (p === 'lua' && !def.script) {
    issues.push({
      severity: 'error',
      code: 'LUA_NO_SCRIPT',
      message: `action "${name}" pattern=lua 缺少 script`,
      location: loc,
    });
  }
  // clearState keys
  if (p === 'clearState' && (!def.keys || def.keys.length === 0)) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_KEYS',
      message: `action "${name}" pattern=clearState 缺少 keys`,
      location: loc,
    });
  }
  // httpRequest url
  if (p === 'httpRequest' && !def.url) {
    issues.push({
      severity: 'error',
      code: 'ACTION_NO_URL',
      message: `action "${name}" pattern=httpRequest 缺少 url`,
      location: loc,
    });
  }
  // httpRequest method / contentType 白名单：导入路径只做 JSON.parse cast，这里兜住非法值，
  // 否则 method 被原样透传、未知 contentType 让请求体为空，到运行期才失败。
  if (p === 'httpRequest') {
    const method = typeof def.method === 'string' && def.method ? def.method.toUpperCase() : 'GET';
    if (!['GET', 'POST'].includes(method)) {
      issues.push({
        severity: 'error',
        code: 'HTTP_METHOD_INVALID',
        message: `action "${name}" pattern=httpRequest 的 method "${def.method}" 不合法（仅支持 GET / POST）`,
        location: loc,
      });
    }
    // 导入的 JSON 经 cast 后类型是 'json'|'form'，但运行时可能是任意字符串；这里兜住非法值。
    const ct = def.contentType as string | undefined;
    if (ct !== undefined && ct !== null && ct !== '' && !['json', 'form'].includes(ct)) {
      issues.push({
        severity: 'error',
        code: 'HTTP_CONTENT_TYPE_INVALID',
        message: `action "${name}" pattern=httpRequest 的 contentType "${ct}" 不合法（仅支持 json / form）`,
        location: loc,
      });
    }
  }
  // setState with no bindings is a no-op
  if (p === 'setState' && (!def.bindings || def.bindings.length === 0)) {
    issues.push({
      severity: 'warning',
      code: 'SETSTATE_NO_BINDINGS',
      message: `action "${name}" pattern=setState 缺少 bindings（无实际效果）`,
      location: loc,
    });
  }
  // timeout 整数契约（Go 端为 int）：小数会让后端 json.Unmarshal 失败。
  if (typeof def.timeout === 'number' && !Number.isInteger(def.timeout)) {
    issues.push({
      severity: 'error',
      code: 'ACTION_TIMEOUT_NON_INTEGER',
      message: `action "${name}" 的 timeout 必须是整数（当前 ${def.timeout}）`,
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
    if (def.c2sProto && def.bindings) {
      const msg = protoRegistry.lookupMessage(def.c2sProto);
      if (msg) {
        const validPaths = fieldPathsOf(def.c2sProto, fieldPathCache);

        for (const b of def.bindings) {
          if (b.field) {
            // Normalize the field path for checking (replace [1], [2] etc with [0])
            const normalizedField = b.field.replace(/\[\d+\]/g, '[0]');
            if (!validPaths.has(normalizedField)) {
              issues.push({
                severity: 'warning',
                code: 'BINDING_FIELD_NOT_FOUND',
                message: `action "${name}" bindings 中字段 "${b.field}" 不存在于 ${def.c2sProto} 或其嵌套结构中`,
                location: loc,
              });
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
        issues.push({
          severity: 'error',
          code: 'SETSTATE_TARGET_MISSING',
          message: `action "${name}" 的第 ${index + 1} 条状态写入缺少目标状态`,
          location: loc,
        });
        return;
      }
      const previous = firstByTarget.get(target);
      if (previous !== undefined) {
        issues.push({
          severity: 'warning',
          code: 'SETSTATE_TARGET_DUPLICATE',
          message: `action "${name}" 的第 ${index + 1} 条写入会覆盖第 ${previous + 1} 条对状态 "${target}" 的写入`,
          location: loc,
        });
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
        issues.push({
          severity: 'error',
          code: 'CLEARSTATE_PROTECTED_KEY',
          message: `action "${name}" 不允许清除内置状态 "${key}"`,
          location: loc,
        });
      }
      if (seen.has(key)) {
        issues.push({
          severity: 'warning',
          code: 'CLEARSTATE_DUPLICATE_KEY',
          message: `action "${name}" 重复清除状态 "${key}"`,
          location: loc,
        });
      }
      seen.add(key);
      if (knownStateKeys && !knownStateKeys.has(key) && !isBuiltinStateKey(key)) {
        issues.push({
          severity: 'warning',
          code: 'CLEARSTATE_UNKNOWN_KEY',
          message: `action "${name}" 要清除的状态 "${key}" 当前流程未识别`,
          location: loc,
        });
      }
    }
  }

  // binding 递归校验：setState 已由上方 SETSTATE_TARGET_MISSING 专门检查空目标，
  // 这里通过 requireTargetField=false 抑制通用 BINDING_NO_FIELD warning，避免重复告警；
  // 其余 pattern 保持默认（requireTargetField=true），缺 field 且无 storeAs 仍告警。
  if (def.bindings) {
    issues.push(
      ...checkBindings(`action "${name}"`, def.bindings, loc, false, {
        requireTargetField: p !== 'setState',
      }),
    );
  }

  return issues;
}

// ── action 节点 onError 校验 ───────────────────────────────

export function checkNodeOnError(
  id: string,
  node: TaskFlow['nodes'][string],
  nodes: TaskFlow['nodes'],
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const onError = node.onError;
  if (!onError) return issues;

  if (onError.strategy && !VALID_ON_ERROR_STRATEGIES.has(onError.strategy)) {
    issues.push({
      severity: 'error',
      code: 'ON_ERROR_STRATEGY_INVALID',
      message: `action 节点 "${id}" 的 onError.strategy "${onError.strategy}" 不合法（应为 resume、skip 或 abort）`,
      location: { kind: 'node', id },
    });
  }

  (onError.ignoreCodes ?? []).forEach((code, i) => {
    if (!Number.isInteger(code) || code <= 0) {
      issues.push({
        severity: 'error',
        code: 'ON_ERROR_IGNORE_CODE_INVALID',
        message: `action 节点 "${id}" 的 onError.ignoreCodes[${i}] 必须是正整数（当前 ${code}）`,
        location: { kind: 'node', id },
      });
    }
  });

  if (onError.handler) {
    if (onError.handler === id) {
      issues.push({
        severity: 'error',
        code: 'ON_ERROR_HANDLER_SELF',
        message: `action 节点 "${id}" 的 onError.handler 不能指向自身`,
        location: { kind: 'node', id },
      });
    } else if (!nodes[onError.handler]) {
      issues.push({
        severity: 'error',
        code: 'ON_ERROR_HANDLER_NOT_FOUND',
        message: `action 节点 "${id}" 的 onError.handler 指向不存在的 "${onError.handler}"`,
        location: { kind: 'node', id },
      });
    }
  }

  if (onError.retry) {
    if (onError.retry.maxRetries !== undefined && onError.retry.maxRetries < 0) {
      issues.push({
        severity: 'error',
        code: 'ON_ERROR_RETRY_MAX_INVALID',
        message: `action 节点 "${id}" 的 onError.retry.maxRetries 必须 >= 0`,
        location: { kind: 'node', id },
      });
    }
    if (onError.retry.retryDelayMs !== undefined && onError.retry.retryDelayMs < 0) {
      issues.push({
        severity: 'error',
        code: 'ON_ERROR_RETRY_DELAY_INVALID',
        message: `action 节点 "${id}" 的 onError.retry.retryDelayMs 必须 >= 0`,
        location: { kind: 'node', id },
      });
    }
  }

  return issues;
}
