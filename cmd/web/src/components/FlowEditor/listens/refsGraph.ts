/**
 * 监听 ↔ 回调引用图。
 *
 * 设计文档 §8.8：
 *   - 反查：listen name → 哪些 (nodeId, refIndex) 注册了它
 *   - 重复注册检测：同一 (server, routeKey) 在不同 action 中绑了不同 listen
 *   - 孤儿 listen：引用计数为 0
 *
 * T3 Batch-4 任务 B（§3.7）：routeKey 从「总伪 JSON 排序」升级为
 * 「codec.json routeKeyTemplate 代入 route 字段值」的真实计算（server 感知）。
 *   - server 在 templates Map 有 template → computeRouteKey(template, route)；
 *   - server 无 codec → 伪 routeKey 降级，**收集到 missingCodecServers** 供
 *     refsCheck 显式产 ROUTEKEY_CODEC_MISSING warning（不静默伪 key）；
 *   - codec 有但 route 缺占位字段（computeRouteKey → null）→ 伪 routeKey 降级
 *     （flow 数据问题，不产 codec warning）。
 *
 * templates 经 routeKeyResolver 的模块级 cache 同步读取（调用方 sync，无法 await IDB）。
 */

import type { ListenRef, TaskFlow } from '@/types/flow';
import {
  computeRouteKey,
  pseudoRouteKey,
  getRouteKeyTemplatesSync,
} from './routeKeyResolver';

export interface RefRecord {
  nodeId: string;
  refIndex: number;
  ref: ListenRef;
}

export interface DuplicateGroup {
  server: string;
  routeKey: string;
  refs: Array<{ nodeId: string; cb: string | null }>;
}

export interface RefsGraph {
  /** node id → 该 action 注册的所有 ListenRef */
  nodeToRefs: Map<string, ListenRef[]>;
  /** listen name → 反向引用列表 */
  listenToRefs: Map<string, RefRecord[]>;
  /** 重复注册（同一 server+routeKey 被多个 action 绑了不同 listen） */
  duplicateRegisters: DuplicateGroup[];
  /** 引用计数（listen name → count） */
  refCount: Map<string, number>;
  /** 引用了不存在 listen 的悬空记录 */
  danglingRefs: RefRecord[];
  /** 被 listenRefs 引用但 IDB 无对应 codec 的 server 集合（供 refsCheck 产 warning） */
  missingCodecServers: Set<string>;
}

/**
 * server 感知的 routeKey 解析。
 *
 * @param server listenRefs[].server（如 'tcp:logic'）
 * @param route  listenRefs[].route（字段值 map）
 * @returns 解析后的 routeKey 字符串（真实 / 伪降级）
 */
function resolveRouteKey(server: string | undefined, route: unknown): string {
  const s = server?.trim();
  if (s) {
    const template = getRouteKeyTemplatesSync().get(s);
    if (template !== undefined) {
      const real = computeRouteKey(template, route);
      if (real !== null) return real;
      // codec 有但 route 缺占位字段 → 伪 key 降级（flow 数据问题，查重仍可用）
    }
    // codec 缺失 / route 不可解析 → 伪 key（missingCodecServers 由 buildRefsGraph 收集）
  }
  return pseudoRouteKey(route);
}

export function buildRefsGraph(flow: TaskFlow): RefsGraph {
  const nodeToRefs = new Map<string, ListenRef[]>();
  const listenToRefs = new Map<string, RefRecord[]>();
  const refCount = new Map<string, number>();
  const danglingRefs: RefRecord[] = [];
  // (server, routeKey) → [(nodeId, listen)]
  const grouping = new Map<string, Array<{ nodeId: string; cb: string | null }>>();
  const missingCodecServers = new Set<string>();

  for (const [nodeId, node] of Object.entries(flow.nodes)) {
    if (node.type !== 'action') continue;
    const refs = node.listenRefs ?? [];
    if (refs.length > 0) nodeToRefs.set(nodeId, refs);
    refs.forEach((ref, i) => {
      const rec: RefRecord = { nodeId, refIndex: i, ref };
      if (ref.listen) {
        const list = listenToRefs.get(ref.listen) ?? [];
        list.push(rec);
        listenToRefs.set(ref.listen, list);
        refCount.set(ref.listen, (refCount.get(ref.listen) ?? 0) + 1);
        if (!(ref.listen in flow.listens)) {
          danglingRefs.push(rec);
        }
      }
      // codec 缺失检测：server 非空但 IDB 无对应 codec template
      const server = ref.server?.trim();
      if (server && !getRouteKeyTemplatesSync().has(server)) {
        missingCodecServers.add(server);
      }
      // 加入分组用于查重
      const key = `${ref.server}|${resolveRouteKey(ref.server, ref.route)}`;
      const arr = grouping.get(key) ?? [];
      arr.push({ nodeId, cb: ref.listen });
      grouping.set(key, arr);
    });
  }

  // 重复注册：同一 server+route 被多个 action 注册了不同 listen
  const duplicateRegisters: DuplicateGroup[] = [];
  for (const [key, group] of grouping) {
    if (group.length <= 1) continue;
    const distinct = new Set(group.map((g) => g.cb ?? '__null__'));
    if (distinct.size <= 1 && group.length === 1) continue;
    if (distinct.size > 1) {
      const [server, rkey] = key.split('|', 2);
      duplicateRegisters.push({ server, routeKey: rkey, refs: group });
    }
  }

  return { nodeToRefs, listenToRefs, duplicateRegisters, refCount, danglingRefs, missingCodecServers };
}

/**
 * 旧伪 routeKey（按 key 排序的稳定 JSON）。
 *
 * 保留导出供 listenTemplateDefaults.ts 的展示摘要（稳定、人眼可读）。
 * 查重/校验走 buildRefsGraph 的 server 感知解析（见上），不再调用本函数。
 */
export function routeKey(route: unknown): string {
  return pseudoRouteKey(route);
}
