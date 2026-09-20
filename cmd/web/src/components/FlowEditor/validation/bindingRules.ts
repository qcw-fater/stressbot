/**
 * 字段绑定与过滤器级校验规则。
 *
 * 从 refsCheck 拆出：这两个函数只看单条 binding / filter 自身的完整性，
 * 不需要流程图、节点表或 proto 注册表，和流程级规则是两个粒度。
 */

import type { BindingType, FieldBind, FilterDef } from '@/types/action';
import { ALL_BINDING_TYPES } from '@/types/action';
import { resolveRandomStringCharset } from '../editors/ActionEditor/randomStringCharset';
import { conditionIssue, type ValidationIssue } from './issues';

const VALID_FILTER_OPS = new Set([
  '',
  '==',
  '!=',
  '>',
  '>=',
  '<',
  '<=',
  'eq',
  'neq',
  'gt',
  'gte',
  'lt',
  'lte',
  'contains',
  'notContains',
  'in',
  'notIn',
  'notNil',
  'isNil',
]);

const VALID_FILTER_MODES = new Set(['any', 'all', 'none']);

const VALID_BINDING_TYPE_SET = new Set<string>(ALL_BINDING_TYPES);

// ── Binding 递归校验 ────────────────────────────────────────

export function checkBindings(
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
      issues.push({
        severity: 'warning',
        code: 'BINDING_NO_FIELD',
        message: `${label} 缺少 field 和 storeAs`,
        location: loc,
      });
    }

    // binding type 合法性
    if (t && !VALID_BINDING_TYPE_SET.has(t)) {
      issues.push({
        severity: 'error',
        code: 'BINDING_UNKNOWN_TYPE',
        message: `${label} 未知的 binding type "${t}"`,
        location: loc,
      });
      continue;
    }

    // 整数契约：min/max/length/count/precision 在 Go 端为 int，前端不得存小数。
    // 小数会导致后端 json.Unmarshal 到 int 失败、任务无法加载。
    for (const [fname, fval] of [
      ['min', b.min],
      ['max', b.max],
      ['length', b.length],
      ['count', b.count],
      ['precision', b.precision],
    ] as const) {
      if (typeof fval === 'number' && !Number.isInteger(fval)) {
        issues.push({
          severity: 'error',
          code: 'BINDING_NON_INTEGER',
          message: `${label} 的 ${fname} 必须是整数（当前 ${fval}）`,
          location: loc,
        });
      }
    }
    // 绑定条件与节点条件走同一个编译器（flow/prepare.go 的 prepareBinding），
    // map entry 里的子绑定也会被递归编译——本函数对 entries 递归调用自身，一并覆盖。
    const condIssue = conditionIssue(label, b.condition, loc);
    if (condIssue) issues.push(condIssue);

    // required 与 optional 互斥：Go 在缺值时 optional 优先（静默跳过），二者同开会让 UI 的 required 承诺失效。
    if (b.required && b.optional) {
      issues.push({
        severity: 'warning',
        code: 'BINDING_REQUIRED_OPTIONAL_CONFLICT',
        message: `${label} 的 required 与 optional 互斥，运行时按 optional 处理（缺失时跳过）`,
        location: loc,
      });
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
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_SOURCE',
            message: `${label} type=${t} 缺少 source`,
            location: loc,
          });
        }
        break;
      case 'stateRandomN':
        if (!b.source) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_SOURCE',
            message: `${label} type=stateRandomN 缺少 source`,
            location: loc,
          });
        }
        if (!b.count || b.count <= 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_COUNT',
            message: `${label} type=stateRandomN count 必须 > 0`,
            location: loc,
          });
        }
        break;
      case 'randomPick':
      case 'randomPickN':
        if (!b.values || b.values.length === 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_VALUES',
            message: `${label} type=${t} 缺少 values`,
            location: loc,
          });
        }
        if (t === 'randomPickN' && (!b.count || b.count <= 0)) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_COUNT',
            message: `${label} type=randomPickN count 必须 > 0`,
            location: loc,
          });
        }
        break;
      case 'randomPickMap':
        if (!b.values || b.values.length === 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_VALUES',
            message: `${label} type=randomPickMap 缺少 values`,
            location: loc,
          });
        }
        if (!b.keySource) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_KEY_SOURCE',
            message: `${label} type=randomPickMap 缺少 keySource`,
            location: loc,
          });
        }
        break;
      case 'randomExclude':
        if ((!b.values || b.values.length === 0) && !b.source) {
          issues.push({
            severity: 'error',
            code: 'BINDING_NO_EXCLUDE_SOURCE',
            message: `${label} type=randomExclude 缺少 values 和 source`,
            location: loc,
          });
        }
        break;
      case 'randomInt':
        if (b.min != null && b.max != null && b.min >= b.max) {
          issues.push({
            severity: 'warning',
            code: 'BINDING_INVALID_RANGE',
            message: `${label} type=randomInt min >= max`,
            location: loc,
          });
        }
        break;
      case 'randomFloat':
        if (b.min != null && b.max != null && b.min >= b.max) {
          issues.push({
            severity: 'warning',
            code: 'BINDING_INVALID_RANGE',
            message: `${label} type=randomFloat min >= max`,
            location: loc,
          });
        }
        break;
      case 'randomBool':
        break;
      case 'randomString':
        if (!b.length || b.length <= 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_INVALID_LENGTH',
            message: `${label} type=randomString length 必须 > 0`,
            location: loc,
          });
        }
        if (b.charset != null && b.charset.trim().length === 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_INVALID_CHARSET',
            message: `${label} type=randomString charset 不能为空`,
            location: loc,
          });
        } else if (b.charset != null && resolveRandomStringCharset(b.charset).length === 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_INVALID_CHARSET',
            message: `${label} type=randomString charset 不能为空`,
            location: loc,
          });
        }
        break;
      case 'map':
        if (!b.entries || b.entries.length === 0) {
          issues.push({
            severity: 'error',
            code: 'BINDING_MAP_NO_ENTRIES',
            message: `${label} type=map 缺少 entries`,
            location: loc,
          });
        } else {
          for (let ei = 0; ei < b.entries.length; ei++) {
            const entry = b.entries[ei];
            const entryLabel = `${label}.entries[${ei}]`;
            if (entry.key === undefined || entry.key === null || entry.key === '') {
              issues.push({
                severity: 'error',
                code: 'BINDING_MAP_ENTRY_NO_KEY',
                message: `${entryLabel} 缺少 key`,
                location: loc,
              });
            }
            if (!entry.value) {
              issues.push({
                severity: 'error',
                code: 'BINDING_MAP_ENTRY_NO_VALUE',
                message: `${entryLabel} 缺少 value`,
                location: loc,
              });
            } else if (entry.value.type === 'map') {
              issues.push({
                severity: 'error',
                code: 'BINDING_MAP_ENTRY_VALUE_MAP',
                message: `${entryLabel} value 不允许嵌套 map 类型`,
                location: loc,
              });
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

export function checkFilters(
  prefix: string,
  filters: FilterDef[],
  loc: { kind: 'action'; id: string },
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  for (let i = 0; i < filters.length; i++) {
    const f = filters[i];
    if (f.op && !VALID_FILTER_OPS.has(f.op)) {
      issues.push({
        severity: 'error',
        code: 'FILTER_UNKNOWN_OP',
        message: `${prefix}.filters[${i}] 未知的 op "${f.op}"`,
        location: loc,
      });
    }
    if (f.mode && !VALID_FILTER_MODES.has(f.mode)) {
      issues.push({
        severity: 'error',
        code: 'FILTER_UNKNOWN_MODE',
        message: `${prefix}.filters[${i}] 未知的 mode "${f.mode}"`,
        location: loc,
      });
    }
    if (f.path?.includes('[]') && !f.mode) {
      issues.push({
        severity: 'warning',
        code: 'FILTER_ARRAY_PATH_NO_MODE',
        message: `${prefix}.filters[${i}] 使用数组通配路径但未配置 mode，建议选择 any / all / none`,
        location: loc,
      });
    }
  }
  return issues;
}
