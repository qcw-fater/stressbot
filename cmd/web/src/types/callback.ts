/**
 * CallbackDef + 三态判别。
 *
 * 一个 callback 在引擎里有三种形态：
 *   - silent       : `{}`         空对象，收到推送后丢弃
 *   - declarative  : `{ s2cProto, store }`  解析 + 写 state
 *   - lua          : `{ script }` （可选 s2cProto）  执行脚本
 *
 * 需要严格区分：
 *   - ListenRef.callback = null            : 不调用 dispatcher
 *   - ListenRef.callback = "x", callbacks.x = {}  : 经 dispatcher 后 silent 消费
 */

import type { StoreMapping } from './action';

export interface CallbackDef {
  s2cProto?: string;
  store?: StoreMapping[];
  script?: string;
  description?: string;
}

export type CallbackKind = 'silent' | 'declarative' | 'lua';

/**
 * 形态判别（设计文档 §8.5）：
 *   - 显式存在 script 字段       → lua
 *   - 显式存在 s2cProto / store  → declarative
 *   - 否则                       → silent
 *
 * 注意：用"字段是否存在"判断而非 truthy。
 * 编辑器里用户从 silent 切到 declarative 时初始 `s2cProto: ''`/`store: []` 都是 falsy，
 * 若用 truthy 判断会被立刻判回 silent，导致表单与画布显示来回跳变。
 * 导出时 flowToJson.cleanCallback 会把空字段清理掉，所以最终 flow.json 仍按"内容形态"语义。
 */
export function classifyCallback(cb: CallbackDef | null | undefined): CallbackKind {
  if (!cb) return 'silent';
  if (cb.script !== undefined) return 'lua';
  if (cb.s2cProto !== undefined || cb.store !== undefined) return 'declarative';
  return 'silent';
}
