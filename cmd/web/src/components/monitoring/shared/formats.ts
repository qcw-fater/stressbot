/**
 * 监控相关的数值格式化工具，供 ActionsTab、HistoryDetailView、SystemTab 等多处复用。
 *
 * 格式约定：
 *   - 字节数（fmtBytes）：< 1KB 用 B，< 1MB 用 KB，否则 MB；
 *   - 毫秒（fmtMs）：< 1 用 2 位小数，< 10 用 1 位小数，否则整数；零或非有限值显示为 "—"，
 *     避免在表格里写 "0.00" 这种"看似有数据"的占位；
 *   - tabular-nums：数字等宽，列对齐时数字看起来不会跳动。
 */

/** 字节数紧凑格式：< 1K 按 B，< 1M 按 KB，否则 MB。 */
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0B';
  if (n < 1024) return `${n.toFixed(0)}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${(n / 1024 / 1024).toFixed(2)}MB`;
}

/** 毫秒紧凑格式（单位由表头标注，单元格只显示数字）。 */
export function fmtMs(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '—';
  if (n < 1) return n.toFixed(2);
  if (n < 10) return n.toFixed(1);
  return n.toFixed(0);
}

/** 用于在表格 / Statistic 单元格里强制等宽数字，避免数字跳动。 */
export const NUMERIC_STYLE = { fontVariantNumeric: 'tabular-nums' as const };
