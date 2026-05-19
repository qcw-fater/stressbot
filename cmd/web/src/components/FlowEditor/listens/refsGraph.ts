/**
 * 监听 ↔ 回调引用图。
 *
 * 设计文档 §8.8：
 *   - 反查：listen name → 哪些 (nodeId, refIndex) 注册了它
 *   - 重复注册检测：同一 (server, routeKey) 在不同 action 中绑了不同 listen
 *   - 孤儿 listen：引用计数为 0
 */

import type { ListenRef, TaskFlow } from '@/types/flow';

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
}

export function buildRefsGraph(flow: TaskFlow): RefsGraph {
  const nodeToRefs = new Map<string, ListenRef[]>();
  const listenToRefs = new Map<string, RefRecord[]>();
  const refCount = new Map<string, number>();
  const danglingRefs: RefRecord[] = [];
  // (server, routeKey) → [(nodeId, listen)]
  const grouping = new Map<string, Array<{ nodeId: string; cb: string | null }>>();

  for (const [nodeId, node] of Object.entries(flow.nodes)) {
    if (node.type !== 'action') continue;
    const refs = node.listenCallbacks ?? [];
    if (refs.length > 0) nodeToRefs.set(nodeId, refs);
    refs.forEach((ref, i) => {
      const rec: RefRecord = { nodeId, refIndex: i, ref };
      if (ref.callback != null) {
        const list = listenToRefs.get(ref.callback) ?? [];
        list.push(rec);
        listenToRefs.set(ref.callback, list);
        refCount.set(ref.callback, (refCount.get(ref.callback) ?? 0) + 1);
        if (!(ref.callback in flow.listens)) {
          danglingRefs.push(rec);
        }
      }
      // 加入分组用于查重
      const key = `${ref.server}|${routeKey(ref.route)}`;
      const arr = grouping.get(key) ?? [];
      arr.push({ nodeId, cb: ref.callback });
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

  return { nodeToRefs, listenToRefs, duplicateRegisters, refCount, danglingRefs };
}

/**
 * 把 route 序列化为稳定排序 JSON（伪 routeKey）。
 *
 * 引擎实际通过 adapter.expected_route_key(route) 计算，前端无法运行 Lua adapter，
 * 这里用 key 排序后的 JSON 字符串足够覆盖 99% `{cmd, act}` 形态的查重需求。
 */
export function routeKey(route: unknown): string {
  if (route == null) return 'null';
  if (typeof route !== 'object') return JSON.stringify(route);
  const keys = Object.keys(route as Record<string, unknown>).sort();
  const sorted: Record<string, unknown> = {};
  for (const k of keys) sorted[k] = (route as Record<string, unknown>)[k];
  return JSON.stringify(sorted);
}
