/**
 * 状态动作编辑器共享纯模型（setState / clearState 复用）。
 *
 * 这里集中存放取值方式分组、类型切换时的通用字段保留规则、值摘要、
 * 高级配置项计数、列表项移动等纯函数，便于 SetStateEditor / ClearStateEditor
 * 以及表格摘要复用，且不耦合任何 React 细节。
 */

import type { BindingType, FieldBind } from '@/types/action';

/**
 * setState 编辑器的取值方式分组（按使用频率排序，覆盖全部 17 种 binding type）。
 *
 * 注意：分组刻意与 BindingsTable 内部为 tcp* / http* 等普通动作准备的 TYPE_GROUPS
 * 不同 —— 状态动作更偏向「先选来源，再选具体取值方式」，分组语义需独立维护。
 */
export const SET_STATE_TYPE_GROUPS: Array<{ label: string; types: BindingType[] }> = [
  { label: '常用', types: ['fixed', 'state', 'randomInt', 'randomFloat', 'randomBool', 'randomString'] },
  { label: '从已有状态取值', types: ['stateFirst', 'stateRandom', 'stateRandomN', 'stateMapKey', 'stateMapValue', 'listSize'] },
  { label: '从候选值取值', types: ['randomPick', 'randomPickN', 'randomPickMap', 'randomExclude'] },
  { label: '复合值', types: ['map'] },
];

/**
 * 切换 binding type 时需要保留的「通用字段」。
 *
 * - field：proto 字段路径，与取值方式正交。
 * - required / optional / wrap / storeAs / condition：通用高级配置，与 type 无关。
 *
 * 其他字段（source / path / values / count / min / max / filters / entries / ...）
 * 都是某一种 type 的专属参数，切换类型时一律丢弃，由用户重新填写。
 */
const COMMON_BINDING_KEYS = ['field', 'required', 'optional', 'wrap', 'storeAs', 'condition'] as const;

/** 切换 binding 类型：保留通用字段与目标 type，丢弃所有 type 专属字段。 */
export function changeBindingType(binding: FieldBind, type: BindingType): FieldBind {
  const next: FieldBind = { type };
  // 通用字段名是 FieldBind 的合法字面子集，但 TS 不允许直接把字面量 key 当索引塞回去；
  // 这里通过 unknown 中转一次，保留运行时语义同时让类型系统满意。
  const sink = next as unknown as Record<string, unknown>;
  for (const key of COMMON_BINDING_KEYS) {
    const value = binding[key];
    if (value !== undefined) {
      sink[key] = value;
    }
  }
  return next;
}

/**
 * 给出 binding 当前取值的人类可读摘要（用于列表行、tag、tooltip 等）。
 *
 * 注意：不允许隐式 object→string，每个 type 显式处理；缺省值统一用 '?' / '未设置' / '未选择来源'。
 */
export function bindingValueSummary(binding: FieldBind): string {
  switch (binding.type) {
    case 'fixed':
      return typeof binding.value === 'string'
        ? binding.value
        : (JSON.stringify(binding.value) ?? '未设置');
    case 'state':
    case 'stateFirst':
    case 'stateRandom':
    case 'stateRandomN':
    case 'stateMapKey':
    case 'stateMapValue':
    case 'listSize':
      return [binding.source, binding.path].filter(Boolean).join('.') || '未选择来源';
    case 'randomInt':
    case 'randomFloat':
      return `${binding.min ?? '?'} ~ ${binding.max ?? '?'}`;
    case 'randomBool':
      return '每次随机 true / false';
    case 'randomString':
      return `长度 ${binding.length ?? '?'}`;
    case 'randomPick':
    case 'randomPickN':
    case 'randomPickMap':
    case 'randomExclude':
      return `${binding.values?.length ?? 0} 个候选值`;
    case 'map':
      return `${binding.entries?.length ?? 0} 个键值对`;
  }
}

/** path 字段是哪些 binding type 的「类型高级字段」（来自设计表的 advanced 列）。 */
const PATH_ADVANCED_TYPES: ReadonlySet<BindingType> = new Set<BindingType>([
  'state',
  'stateFirst',
  'stateRandom',
  'stateRandomN',
  'stateMapValue',
]);

/** filters 字段是哪些 binding type 的「类型高级字段」（来自设计表的 advanced 列）。 */
const FILTERS_ADVANCED_TYPES: ReadonlySet<BindingType> = new Set<BindingType>([
  'stateRandom',
  'stateRandomN',
  'stateMapKey',
  'stateMapValue',
]);

/**
 * 统计 binding 在「高级配置」折叠区中实际渲染的项数。
 *
 * 由两部分组成：
 *   1. 通用高级字段（所有 type 共享）：required / optional / wrap / storeAs / condition —— 每项配置 +1。
 *   2. 类型高级字段（按设计表 advanced 列）：
 *      - path：state / stateFirst / stateRandom / stateRandomN / stateMapValue 配置时 +1。
 *      - filters（非空数组）：stateRandom / stateRandomN / stateMapKey / stateMapValue +1。
 *      - excludeSource：randomExclude 配置时 +1。
 *
 * 注意：keySource（randomPickMap）与 entries（map）属于 primary 段，不计入此处；
 * values / min / max / precision / length / count / charset / source 也都是 primary，不计入。
 */
export function bindingAdvancedCount(binding: FieldBind): number {
  let count = 0;

  if (binding.required) count += 1;
  if (binding.optional) count += 1;
  if (binding.wrap) count += 1;
  if (binding.storeAs) count += 1;
  if (binding.condition) count += 1;

  const t = binding.type;
  if (PATH_ADVANCED_TYPES.has(t) && binding.path) count += 1;
  if (FILTERS_ADVANCED_TYPES.has(t) && binding.filters && binding.filters.length > 0) count += 1;
  if (t === 'randomExclude' && binding.excludeSource) count += 1;

  return count;
}

/**
 * 把列表的第 `from` 项移动到 `to` 位置（remove+insert 语义，非 swap）。
 *
 * 边界：
 *   - `to < 0` 或 `to >= list.length`（或 `from` 越界）视为非法移动，原数组引用直接返回。
 *   - 合法移动始终返回新数组（即使 from === to 也返回拷贝），不影响原数组。
 */
export function moveBinding<T>(list: T[], from: number, to: number): T[] {
  if (from < 0 || from >= list.length) return list;
  if (to < 0 || to >= list.length) return list;
  const next = [...list];
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
}
