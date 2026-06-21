/**
 * routeKey 真实计算（T3 Batch-4 任务 B，§3.7）。
 *
 * 背景：旧的 `routeKey(route)` 是「按 key 排序的 JSON 字符串」伪实现——前端无法运行
 * Lua adapter，用排序 JSON 凑合覆盖 99% `{cmd, act}` 形态的查重。声明式 codec 后，
 * routeKey 是**确定的**：`codec.json` 的 `routeKeyTemplate`（如 `{cmd}:{act}`）
 * + route 字段值。本模块提供真实计算 + codec 模板加载。
 *
 * 约定：
 *   - computeRouteKey 解析失败（占位字段缺失 / route 非对象）→ null（flow 数据问题，
 *     调用方降级到伪 routeKey，仍能查重）。
 *   - codec 缺失/有误（server 不在 templates Map）→ 调用方降级到伪 routeKey
 *     **并显式产 ROUTEKEY_CODEC_MISSING warning**（不静默伪 key）。
 */

import { listCodecFiles, type ResourceFile } from '@/services/resourcesStore';
import { codecFileNameToConnName } from '@/services/taskResourceDiff';

/** 占位正则，与 resourcesStore.validateCodecSchema 的 ROUTE_KEY_PLACEHOLDER_RE 一致。 */
const ROUTE_KEY_PLACEHOLDER_RE = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;

/**
 * 按 routeKeyTemplate 把 route 字段值代入占位，返回真实 routeKey。
 *
 * @param template codec.json 的 routeKeyTemplate（如 `{cmd}:{act}`）
 * @param route    listenRefs[].route / action.route（字段值 map）
 * @returns 代入后的字符串；占位字段缺失 / route 非对象 → null
 */
export function computeRouteKey(template: string, route: unknown): string | null {
  if (route == null || typeof route !== 'object' || Array.isArray(route)) {
    return null;
  }
  const record = route as Record<string, unknown>;
  let result = '';
  let lastIndex = 0;
  let missing = false;
  ROUTE_KEY_PLACEHOLDER_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = ROUTE_KEY_PLACEHOLDER_RE.exec(template)) !== null) {
    result += template.slice(lastIndex, m.index);
    lastIndex = m.index + m[0].length;
    const name = m[1];
    const v = record[name];
    if (v === undefined || v === null) {
      missing = true;
      break;
    }
    result += String(v);
  }
  if (missing) return null;
  result += template.slice(lastIndex);
  return result;
}

/**
 * 旧伪 routeKey（按 key 排序的稳定 JSON）。保留供：
 *   - computeRouteKey 返回 null（route 缺占位字段，flow 数据问题）时的降级；
 *   - codec 缺失时的降级（配合 ROUTEKEY_CODEC_MISSING warning）；
 *   - listenTemplateDefaults.ts 的展示摘要（与人眼可读的稳定字符串）。
 */
export function pseudoRouteKey(route: unknown): string {
  if (route == null) return 'null';
  if (typeof route !== 'object') return JSON.stringify(route);
  const keys = Object.keys(route as Record<string, unknown>).sort();
  const sorted: Record<string, unknown> = {};
  for (const k of keys) sorted[k] = (route as Record<string, unknown>)[k];
  return JSON.stringify(sorted);
}

/**
 * 加载所有 codec 的 routeKeyTemplate：server（如 `tcp:logic`）→ template。
 *
 * 遍历 IDB 中所有 `*_codec.json`：JSON.parse 取 `routeKeyTemplate`（字符串），
 * 文件名经 `codecFileNameToConnName` 得 server。解析失败 / 无 template / 非字符串
 * 的条目跳过（不抛）。
 *
 * @returns server → routeKeyTemplate 的 Map；空 Map = 无可用 codec
 */
export async function loadRouteKeyTemplates(): Promise<Map<string, string>> {
  const files: ResourceFile[] = await listCodecFiles();
  const map = new Map<string, string>();
  for (const f of files) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(f.content);
    } catch {
      continue; // 坏 JSON，跳过
    }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) continue;
    const template = (parsed as { routeKeyTemplate?: unknown }).routeKeyTemplate;
    if (typeof template !== 'string') continue; // 缺/非字符串，跳过
    const server = codecFileNameToConnName(f.name);
    map.set(server, template);
  }
  return map;
}

/**
 * server 感知的 routeKey 解析（refsGraph 与 refsCheck 共用，保证查重 key 一致）。
 *
 * 与 refsGraph.resolveRouteKey 同语义，导出供 refsCheck 的 listen 预注册校验复用：
 *   - server 有 template → computeRouteKey；不可解析（null）→ 伪 key 降级；
 *   - server 无 codec / server 空 → 伪 key 降级。
 *
 * 注：codec 缺失 warning 由 refsCheck 据 graph.missingCodecServers 统一产，
 * 本函数只负责返回 routeKey 字符串。
 */
export function resolveRouteKeyForServer(server: string | undefined, route: unknown): string {
  const s = server?.trim();
  if (s) {
    const template = getRouteKeyTemplatesSync().get(s);
    if (template !== undefined) {
      const real = computeRouteKey(template, route);
      if (real !== null) return real;
    }
  }
  return pseudoRouteKey(route);
}

// ── 模块级缓存（调用方 sync，无法 await IDB） ─────────────────────────
//
// 接入方案选择：**cache**（非透传）。
// 理由：validateFlow/buildRefsGraph 的调用方（ValidationReport.tsx / Toolbar.tsx /
// flowStore.ts）全部在 React useMemo / zustand store action 中**同步**调用，
// 渲染期无法 `await loadRouteKeyTemplates()`。逐个改成 async（Suspense / effect
// 重算）会扩散到 3 个组件 + store，超出本任务改动范围且有 UI 回归风险。
// 故采用模块级 cache：由 initRouteKeyTemplateCache() 在 FlowEditor 启动时加载，
// codec 文件变更（resourcesStore.subscribe 触发）时刷新。
//
// **失效条件**：subscribe 监听 resourcesStore.notify（任何 codec 写/删触发）→
// 异步重载 cache。未初始化时 getRouteKeyTemplatesSync() 返回空 Map（全降级 +
// ROUTEKEY_CODEC_MISSING warning，安全）。

let cachedTemplates: Map<string, string> = new Map();
let cacheLoaded = false;
let cacheLoadPromise: Promise<void> | null = null;

/** 同步取当前已加载的 templates（未加载返回空 Map，调用方据降级 + warning）。 */
export function getRouteKeyTemplatesSync(): Map<string, string> {
  return cachedTemplates;
}

/** 是否已完成首次加载（供测试与启动判断）。 */
export function isRouteKeyTemplateCacheLoaded(): boolean {
  return cacheLoaded;
}

/** 异步（重新）加载 templates 到 cache。多次调用复用同一个 in-flight promise。 */
export async function refreshRouteKeyTemplates(): Promise<void> {
  if (cacheLoadPromise) return cacheLoadPromise;
  cacheLoadPromise = (async () => {
    try {
      cachedTemplates = await loadRouteKeyTemplates();
      cacheLoaded = true;
    } finally {
      cacheLoadPromise = null;
    }
  })();
  return cacheLoadPromise;
}

/** 测试用：重置 cache（不触发 subscribe）。 */
export function __resetRouteKeyTemplateCacheForTest(): void {
  cachedTemplates = new Map();
  cacheLoaded = false;
  cacheLoadPromise = null;
}
