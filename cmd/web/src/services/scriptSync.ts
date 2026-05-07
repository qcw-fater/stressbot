/**
 * Lua 脚本与 IndexedDB 的"按 flow 同步"工具。
 *
 * 设计目标（用户语义）：
 *   - 「flow → 引用的脚本」是单一事实源，IDB 只是用户编辑稿/本地副本；
 *   - 用户**不需要**关心脚本是从哪里来的（手写 / 导入 / 默认基线）—— 启动任务时所有
 *     被引用到的脚本都必须存在；
 *   - 同时保护用户已经编辑过的本地稿不被默认基线覆盖。
 *
 * 三个调用点：
 *   1. Toolbar 导入 JSON / 加载 conf/flow.json 后 → 自动把"引用了但 IDB 没有"的脚本
 *      从 `/conf/scripts/<name>` 拉回来写 IDB（开发期由 Vite confMountPlugin 提供）；
 *   2. EditorPage 初始化默认 flow 后 → 同上；
 *   3. taskActions.startTask 提交前 → 最后一道兜底；这一步如果还有缺失文件就抛
 *      ApiError，避免 Agent 端报"脚本未预编译"。
 *
 * 不做的事：
 *   - **不覆盖** IDB 中已经存在的脚本（保护用户编辑稿，即使内容与基线不同）；
 *   - 不动 proto：proto 是按 messageType 名字引用的，flow 没声明依赖哪些 .proto 文件，
 *     无法静态推断；proto 仍由「资源管理」入口手动管理。
 */

import { addScript, getScript } from './resourcesStore';
import type { TaskFlow } from '@/types/flow';

export interface ScriptSyncResult {
  /** 这次同步真正写入 IDB 的脚本名（之前 IDB 没有，从基线拉回的） */
  added: string[];
  /** flow 引用、IDB 已有，本次未做任何操作（保护用户编辑稿） */
  skipped: string[];
  /** flow 引用、IDB 没有、基线 `/conf/scripts/<name>` 也拉不到 */
  missing: string[];
}

/**
 * 扫描 flow 中所有被引用的 lua 脚本名。覆盖：
 *   - actions[].script                       动作节点 lua 模式
 *   - callbacks[].script                     listen 回调 lua 模式
 *   - nodes[].condition (lua: 前缀)          boolean / loop 前置条件
 *   - nodes[].breakCondition (lua: 前缀)     loop 后置条件
 *
 * 为什么仅扫静态字段：
 *   - flow.json 是声明式的，所有"会被引擎执行的脚本"必然在上述字段里出现；
 *   - 脚本内部的 `require('xxx')` / `dofile()` 是动态的，无法静态分析 → 由用户在
 *     「资源管理」中手动上传，或写在主脚本里 inline。
 */
export function collectFlowScriptNames(flow: TaskFlow): string[] {
  const set = new Set<string>();
  for (const a of Object.values(flow.actions ?? {})) {
    if (a?.script) set.add(a.script);
  }
  for (const c of Object.values(flow.callbacks ?? {})) {
    if (c?.script) set.add(c.script);
  }
  for (const n of Object.values(flow.nodes ?? {})) {
    const cond = parseLuaCondition(n?.condition);
    if (cond) set.add(cond);
    const brk = parseLuaCondition(n?.breakCondition);
    if (brk) set.add(brk);
  }
  return Array.from(set).sort();
}

/**
 * 解析 condition / breakCondition 字段，返回 lua 脚本名；不是 lua 模式则返回 null。
 *
 * 与 robot/robot.go ExecuteBoolean 的协议保持一致：仅 "lua:" 前缀走脚本求值，
 * "state:" / 原文形态都不依赖外部脚本。
 */
function parseLuaCondition(raw: string | undefined): string | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed.startsWith('lua:')) return null;
  const name = trimmed.slice(4).trim();
  return name || null;
}

/**
 * 把 flow 引用、但 IDB 里还没有的脚本从默认基线 `/conf/scripts/<name>` 拉回来并写 IDB。
 *
 * 不并发限流：lua 脚本通常 < 几十个、< 几 KB，浏览器一次性 fetch 完全可以承受。
 *
 * @param flow      要扫描的 TaskFlow
 * @param baseUrl   基线脚本目录 URL，默认 `/conf/scripts/`（开发期由 Vite 中间件提供）
 */
export async function syncFlowScriptsToIdb(
  flow: TaskFlow,
  baseUrl = '/conf/scripts/',
): Promise<ScriptSyncResult> {
  const names = collectFlowScriptNames(flow);
  const added: string[] = [];
  const skipped: string[] = [];
  const missing: string[] = [];

  await Promise.all(
    names.map(async (name) => {
      const existed = await getScript(name);
      if (existed) {
        skipped.push(name);
        return;
      }
      try {
        const r = await fetch(baseUrl + encodeURIComponent(name));
        if (!r.ok) {
          missing.push(name);
          return;
        }
        const text = await r.text();
        await addScript(name, text);
        added.push(name);
      } catch {
        missing.push(name);
      }
    }),
  );

  added.sort();
  skipped.sort();
  missing.sort();
  return { added, skipped, missing };
}
