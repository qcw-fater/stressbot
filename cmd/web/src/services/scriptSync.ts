/**
 * 脚本与本地存储的"按 flow 同步"工具。
 *
 * 设计目标：
 *   - 扫描 flow 中引用的脚本名，将本地存储中缺失的从基线 `/sbot/baseline/scripts/<name>` 拉回；
 *   - **不覆盖**本地存储中已存在的脚本（保护用户编辑稿）；
 *   - 基线更新检测由 `syncResourcesFromBaseline` 负责，此模块仅做 gap-fill。
 *
 * 调用点：
 *   - FlowEditor 初始化、Toolbar 加载、TaskStartModal 打开、startTask 提交前。
 */

import { addScriptFromBaseline, getScript } from './resourcesStore';
import { fetchBaselineScript } from './baselineApi';
import { BASELINE_PREFIX } from './env';
import { collectFlowScriptNames } from './flowScriptRefs';
import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';

// 扫描器本身在 flowScriptRefs.ts（纯函数）；这里继续导出，保持历史调用点不变。
export { collectFlowScriptNames, type ScriptScanSource } from './flowScriptRefs';

export interface ScriptSyncResult {
  /** 这次同步真正写入本地存储的脚本名（之前本地没有，从基线拉回的） */
  added: string[];
  /** flow 引用、本地存储已有，本次未做任何操作（保护用户编辑稿） */
  skipped: string[];
  /** flow 引用、本地存储没有、基线 `/sbot/baseline/scripts/<name>` 也拉不到 */
  missing: string[];
}

/**
 * 把 flow 引用、但本地存储里还没有的脚本从默认基线 `/sbot/baseline/scripts/<name>` 拉回来并写本地存储。
 *
 * 不并发限流：lua 脚本通常 < 几十个、< 几 KB，浏览器一次性 fetch 完全可以承受。
 *
 * @param flow      要扫描的 TaskFlow
 */
export async function syncFlowScriptsToIdb(
  flow: FlowJson,
  baseUrl = `${BASELINE_PREFIX}/scripts/`,
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
        const text =
          baseUrl === `${BASELINE_PREFIX}/scripts/`
            ? await fetchBaselineScript(name)
            : await fetch(baseUrl + encodeURIComponent(name)).then((r) => (r.ok ? r.text() : null));
        if (text === null) {
          missing.push(name);
          return;
        }
        await addScriptFromBaseline(name, text);
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

/**
 * 收集 flow 中引用的所有 proto 全名。
 * 覆盖 actions[].c2sProto / s2cProto 和 listens[].s2cProto。
 */
export function collectFlowProtoNames(flow: FlowJson): Set<string> {
  const set = new Set<string>();
  for (const a of Object.values(flow.actions ?? {})) {
    if (a?.c2sProto) set.add(a.c2sProto);
    if (a?.s2cProto) set.add(a.s2cProto);
  }
  for (const c of Object.values(flow.listens ?? {})) {
    if (c?.s2cProto) set.add(c.s2cProto);
  }
  return set;
}
