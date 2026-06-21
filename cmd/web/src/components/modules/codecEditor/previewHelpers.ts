/**
 * previewHelpers — PreviewPanel 的纯函数 helper（无 React 依赖，便于单测）。
 *
 * 设计要点：
 *   - **transport 由连接名推导**：`<proto>:<service>` 取首个 `:` 前的 proto，
 *     仅识别 tcp/udp；无法识别时回退 'tcp'（与后端 preview.go 空串/非法→tcp 语义对齐）。
 *   - **route 字段提取**：从 schema.header 取所有 `role:"route"` 且 name 非空的字段，
 *     保序返回（供 encode 表单渲染每字段一个值输入）。
 *   - **route map 组装**：encode 提交时把表单的 `{fieldName: string}` 规约为
 *     `{fieldName: number|string}`——空串剔除；纯数字串转 number（与后端 normalizeRouteMap
 *     的 string→int64 路径对齐，这里提前数值化便于 JSON 传输稳定）。
 *
 * 不做兼容兜底：非法输入按契约回退/剔除，不抛错。
 */

import type { CodecSchema, Field } from '@/types/codec';

/** PreviewPanel 支持的传输协议（codec 单 transport）。 */
export type PreviewTransport = 'tcp' | 'udp';

/**
 * 从连接名推导 transport。
 *   'tcp:logic' → 'tcp'；'udp:battle' → 'udp'；无法识别（无冒号/未知 proto）→ 'tcp'。
 */
export function deriveTransport(connName: string | null | undefined): PreviewTransport {
  if (!connName) return 'tcp';
  const idx = connName.indexOf(':');
  if (idx <= 0) return 'tcp';
  const proto = connName.slice(0, idx);
  return proto === 'udp' ? 'udp' : 'tcp';
}

/**
 * 从 schema.header 收集所有 role:"route" 字段名（保序，去重）。
 * 非法 header（非数组/缺字段）安全降级为空数组。
 */
export function collectRouteFields(schema: CodecSchema | null): Field[] {
  if (!schema) return [];
  const header = Array.isArray(schema.header) ? schema.header : [];
  const seen = new Set<string>();
  const out: Field[] = [];
  for (const f of header) {
    if (!f || f.role !== 'route') continue;
    if (typeof f.name !== 'string' || f.name === '') continue;
    if (seen.has(f.name)) continue;
    seen.add(f.name);
    out.push(f);
  }
  return out;
}

/**
 * 把表单输入的 route map（fieldName → 字符串值）规约为 preview 请求的 route map。
 *   - 空白串剔除（不发给后端，后端对缺失 route 字段会用 0）。
 *   - 纯整数串（含负号）→ number；其它保留为 string（后端 routePreviewFloorInt 处理）。
 */
export function buildRouteMap(input: Record<string, string>): Record<string, number | string> {
  const out: Record<string, number | string> = {};
  for (const [k, raw] of Object.entries(input)) {
    const v = raw.trim();
    if (v === '') continue;
    const n = Number(v);
    // Number('123') === 123、Number('-1') === -1、Number('1e3')===1000；
    // 但 Number('')===0、Number('  ')===0 已被 trim+空串剔除覆盖；Number('0x10') 用 Number 仍解析，
    // 这里要求「整数串」用正则收紧，避免 '12ab' 之类被 Number→NaN 误判。
    if (/^-?\d+$/.test(v)) {
      out[k] = n;
    } else {
      out[k] = v;
    }
  }
  return out;
}
