/**
 * flow.json 里的 Lua 脚本引用扫描（纯函数，无 IO / 无存储依赖）。
 *
 * 单独成模块的原因：既被任务上传链路（scriptSync / taskActions）使用，也被编辑器的
 * state key 注册表使用。放在这里两边都能引，且不会把本地存储、基线请求这些副作用
 * 拖进纯数据模块。
 */

import type { ActionDef, FieldBind } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowNode } from '@/types/flow';

/**
 * 脚本扫描输入：`FlowJson` 可直接传入，编辑器里也可以只传 store 的三张表。
 * 用结构化子集而非 FlowJson，是为了让 flow 编辑态与导出态共用同一个扫描器，
 * 不再各自维护一份「哪些字段能引用脚本」的清单。
 */
export interface ScriptScanSource {
  nodes?: Record<string, FlowNode | undefined>;
  actions?: Record<string, ActionDef | undefined>;
  listens?: Record<string, ListenDef | undefined>;
}

/**
 * 扫描 flow 中所有被引用的 lua 脚本名。覆盖：
 *   - actions[].script                       动作节点 lua 模式
 *   - actions[].bindings[].condition         字段绑定条件（含 map entries 递归）
 *   - listens[].script                       listen 回调 lua 模式
 *   - nodes[].condition (lua: 前缀)          boolean / loop 前置条件
 *   - nodes[].breakCondition (lua: 前缀)     loop 后置条件
 *   - nodes[].cases[].condition (lua: 前缀)  switch 分支条件
 *
 * 扫描面必须与 Go 端条件编译点一一对应（`flow/prepare.go` 的 prepare / prepareBinding）：
 * 那里对节点条件、switch case 条件和 FieldBind.Condition 走的是同一个编译器，都接受
 * `lua:` 前缀。少扫一处，脚本就不会被上传，任务能启动但运行期求值时才失败。
 *
 * 为什么仅扫静态字段：
 *   - flow.json 是声明式的，所有「会被引擎执行的脚本」必然在上述字段里出现；
 *   - 脚本内部的 `require('xxx')` / `dofile()` 是动态的，无法静态分析 → 由用户在
 *     「资源管理」中手动上传，或写在主脚本里 inline。
 */
export function collectFlowScriptNames(flow: ScriptScanSource): string[] {
  const set = new Set<string>();
  forEachFlowScriptRef(flow, (ref) => set.add(ref.name));
  return Array.from(set).sort();
}

/** 一处脚本引用：名字 + 它在 flow 里的出处（用于把问题定位回画布）。 */
export interface FlowScriptRef {
  name: string;
  /** 引用所在位置，与校验报告的 location 同形 */
  location: { kind: 'node' | 'action' | 'listen'; id: string };
  /** 人读的出处描述，如 `动作 "login" 的字段绑定条件` */
  where: string;
}

/**
 * 遍历 flow 里的每一处脚本引用（同名多处会多次回调）。
 *
 * 与 collectFlowScriptNames 共用这一个遍历器，扫描面就只有一份：上传链路要的是去重名单，
 * 校验报告要的是逐处定位，两者不会再各自漏掉某个字段。
 */
export function forEachFlowScriptRef(
  flow: ScriptScanSource,
  visit: (ref: FlowScriptRef) => void,
): void {
  for (const [name, a] of Object.entries(flow.actions ?? {})) {
    const location = { kind: 'action', id: name } as const;
    if (a?.script) visit({ name: a.script, location, where: `动作 "${name}" 的脚本` });
    visitBindingConditions(a?.bindings, (script) =>
      visit({ name: script, location, where: `动作 "${name}" 的字段绑定条件` }),
    );
  }
  for (const [name, c] of Object.entries(flow.listens ?? {})) {
    if (c?.script) {
      visit({
        name: c.script,
        location: { kind: 'listen', id: name },
        where: `监听 "${name}" 的脚本`,
      });
    }
  }
  for (const [id, n] of Object.entries(flow.nodes ?? {})) {
    const location = { kind: 'node', id } as const;
    const lua = (raw: string | undefined, field: string) => {
      const script = parseLuaCondition(raw);
      if (script) visit({ name: script, location, where: `节点 "${id}" 的 ${field}` });
    };
    lua(n?.condition, 'condition');
    lua(n?.breakCondition, 'breakCondition');
    (n?.cases ?? []).forEach((c, i) => lua(c?.condition, `cases[${i}].condition`));
  }
}

/** 递归遍历字段绑定上的条件：map 类型的 entries[].value 本身又是一个 FieldBind。 */
function visitBindingConditions(
  bindings: FieldBind[] | undefined,
  visit: (script: string) => void,
): void {
  for (const b of bindings ?? []) {
    if (!b) continue;
    const script = parseLuaCondition(b.condition);
    if (script) visit(script);
    for (const entry of b.entries ?? []) {
      if (entry?.value) visitBindingConditions([entry.value], visit);
    }
  }
}

/**
 * 解析条件字段，返回 lua 脚本名；不是 lua 模式则返回 null。
 *
 * 与 Go 端 `flow/condition_compile.go` 一致：`lua:` 必须是整条条件的前缀，
 * 不存在「复合表达式里嵌一个 lua: 子项」的形态，因此整串去前缀即脚本名。
 */
export function parseLuaCondition(raw: string | undefined): string | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed.startsWith('lua:')) return null;
  const name = trimmed.slice(4).trim();
  return name || null;
}
