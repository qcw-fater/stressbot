/**
 * ListenDef + 三态判别。
 *
 * 一个 listen 在引擎里有三种形态：
 *   - silent       : `{}`         空对象，收到推送后丢弃
 *   - declarative  : `{ s2cProto, store }`  解析 + 写 state
 *   - lua          : `{ script }` （可选 s2cProto）  执行脚本 —— **已下线**
 *
 * ⚠️ `script` 字段在 T2 后端已改为 fail-loud：配置即报错。
 * 此处保留 `script?` 类型仅为旧 flow.json round-trip（读入/导出不丢字段），
 * 编辑器不再提供该形态入口（classifyListen 的 'lua' 分支在 UI 不可达），
 * 校验会对存在的 script 字段报 LISTEN_SCRIPT_DISABLED。
 *
 * 需要严格区分：
 *   - ListenRef.listen = null            : 不调用 dispatcher
 *   - ListenRef.listen = "x", listens.x = {}  : 经 dispatcher 后 silent 消费
 */

import type { StoreMapping } from './action';

export interface ListenDef {
  s2cProto?: string;
  store?: StoreMapping[];
  script?: string;
  description?: string;
}

export type ListenKind = 'silent' | 'declarative' | 'lua';

/**
 * 形态判别（设计文档 §8.5）：
 *   - 显式存在 script 字段       → lua
 *   - 显式存在 s2cProto / store  → declarative
 *   - 否则                       → silent
 *
 * 注意：用"字段是否存在"判断而非 truthy。
 * 编辑器里用户从 silent 切到 declarative 时初始 `s2cProto: ''`/`store: []` 都是 falsy，
 * 若用 truthy 判断会被立刻判回 silent，导致表单与画布显示来回跳变。
 * 导出时 flowToJson.cleanListen 会把空字段清理掉，所以最终 flow.json 仍按"内容形态"语义。
 */
export function classifyListen(cb: ListenDef | null | undefined): ListenKind {
  if (!cb) return 'silent';
  if (cb.script !== undefined) return 'lua';
  if (cb.s2cProto !== undefined || cb.store !== undefined) return 'declarative';
  return 'silent';
}
