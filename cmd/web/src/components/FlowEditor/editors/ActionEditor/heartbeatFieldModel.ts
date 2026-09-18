/**
 * 心跳字段（HeartbeatField）的编辑规则，纯函数。
 *
 * 规则来源是 Go 端 `engine/heartbeat.go` 的 resolveHeartbeatField / resolveFloatField：
 * 每个 source 只认自己那组参数，浮点类型（f32/f64）只认 fixed / state 两个 source——
 * 计数器、时间戳、随机整数这些整型语义对浮点无意义，引擎直接报错而不是兜底。
 *
 * 引擎报的是**运行期**错误：心跳每个 tick 打一条 warn、body 组不出来，压测照跑但对端收不到
 * 保活包。所以这些约束必须在表单这一层就成立，不能指望用户从日志里发现。
 */

import type { HeartbeatField, HeartbeatFieldSource, HeartbeatFieldType } from '@/types/action';

export function isFloatHeartbeatType(type: HeartbeatFieldType): boolean {
  return type === 'f32' || type === 'f64';
}

/** 浮点字段允许的 source（与 resolveFloatField 的 switch 分支一致）。 */
const FLOAT_SOURCES: HeartbeatFieldSource[] = ['fixed', 'state'];

export function isSourceAllowedForType(
  source: HeartbeatFieldSource,
  type: HeartbeatFieldType,
): boolean {
  return !isFloatHeartbeatType(type) || FLOAT_SOURCES.includes(source);
}

/** 各 source 独占的参数字段。切换后不在列表里的必须清掉，否则会留在 codec JSON 里。 */
const SOURCE_PARAMS: Record<HeartbeatFieldSource, readonly (keyof HeartbeatField)[]> = {
  fixed: ['value', 'floatValue'],
  state: ['key'],
  stateCounter: ['key'],
  counter: ['start', 'step'],
  timestamp: ['unit'],
  randomInt: ['min', 'max'],
};

const ALL_PARAMS = Object.values(SOURCE_PARAMS).flat();

/**
 * 按目标 (type, source) 组合裁剪参数字段：保留 type / source 与该组合用得上的参数，其余丢弃。
 *
 * fixed 下整型用 value、浮点用 floatValue，两者互斥——不裁剪的话，u16 改成 f32 后 JSON 里会
 * 同时留着 value 和 floatValue，读配置的人无从判断哪个生效（引擎只看 floatValue）。
 */
function pruneParams(field: HeartbeatField): HeartbeatField {
  const keep = new Set<keyof HeartbeatField>(SOURCE_PARAMS[field.source] ?? []);
  if (field.source === 'fixed') {
    keep.delete(isFloatHeartbeatType(field.type) ? 'value' : 'floatValue');
  }
  const next = { ...field };
  for (const param of ALL_PARAMS) {
    if (!keep.has(param)) delete next[param];
  }
  return next;
}

/**
 * 切换字段类型。整型 → 浮点时若当前 source 浮点不支持，顺带落到最接近的合法 source：
 * stateCounter 仍从 state 取值、语义最近，落到 state；其余落到 fixed。
 */
export function changeHeartbeatFieldType(
  field: HeartbeatField,
  type: HeartbeatFieldType,
): HeartbeatField {
  let source = field.source;
  if (!isSourceAllowedForType(source, type)) {
    source = source === 'stateCounter' ? 'state' : 'fixed';
  }
  return pruneParams({ ...field, type, source });
}

/** 切换取值来源，丢弃上一个 source 的专属参数。 */
export function changeHeartbeatFieldSource(
  field: HeartbeatField,
  source: HeartbeatFieldSource,
): HeartbeatField {
  return pruneParams({ ...field, source });
}
