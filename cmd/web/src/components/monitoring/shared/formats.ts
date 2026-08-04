/**
 * 监控相关的数值格式化工具，供 MonitorDock、HistoryDetailView、SystemTab 等多处复用。
 *
 * 格式约定：
 *   - 字节数（fmtBytes）：自适应单位 B / KB / MB，**自身带后缀**，用于"单位由数值大小决定"
 *     的场景（如监控大盘里突出的吞吐 Statistic）；
 *   - 字节数（fmtBytesPlain）：始终按 B 计，**不带后缀**，单位由表头 `(B)` 标注。
 *     用于表格列：单位统一便于排序对比，避免 16B / 1.2KB / 2MB 混排导致视觉错位；
 *   - 毫秒（fmtMs）：< 1 用 2 位小数，< 10 用 1 位小数，否则整数；零或非有限值显示为 "—"，
 *     避免在表格里写 "0.00" 这种"看似有数据"的占位；
 *   - tabular-nums：数字等宽，列对齐时数字看起来不会跳动。
 */

/** 字节数紧凑格式：< 1K 按 B，< 1M 按 KB，否则 MB（**自身带单位后缀**）。 */
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0B';
  if (n < 1024) return `${n.toFixed(0)}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${(n / 1024 / 1024).toFixed(2)}MB`;
}

/**
 * 字节数表格列格式（单位由表头标注，单元格只显示数字）：
 *   - 0 / 非有限值 → "—"（与 fmtMs 一致，区分"真零"与"无数据"占位）
 *   - 其它 → 千分位整数（如 16 → "16"、10240 → "10,240"）
 */
export function fmtBytesPlain(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '—';
  return Math.round(n).toLocaleString('en-US');
}

/** 毫秒紧凑格式（单位由表头标注，单元格只显示数字）。 */
export function fmtMs(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—';
  if (n === 0) return '0';
  if (n < 1) return n.toFixed(2);
  if (n < 10) return n.toFixed(1);
  return n.toFixed(0);
}

/** 用于在表格 / Statistic 单元格里强制等宽数字，避免数字跳动。 */
export const NUMERIC_STYLE = { fontVariantNumeric: 'tabular-nums' as const };

export function fmtCompactNumber(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return '—';
  const abs = Math.abs(n);
  if (abs >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (abs >= 10_000) return `${(n / 1_000).toFixed(1)}K`;
  return Math.round(n).toLocaleString('en-US');
}

export function fmtPercent(n: number | null | undefined, digits = 1): string {
  if (n == null || !Number.isFinite(n)) return '—';
  return `${(n * 100).toFixed(digits)}%`;
}

export function fmtPercentValue(n: number | null | undefined, digits = 1): string {
  if (n == null || !Number.isFinite(n)) return '—';
  return `${n.toFixed(digits)}%`;
}

export function fmtScore(n: number | null | undefined, digits = 2): string {
  if (n == null || !Number.isFinite(n)) return '—';
  return n.toFixed(digits);
}

export function fmtRate(n: number | null | undefined, digits = 1): string {
  if (n == null || !Number.isFinite(n)) return '—';
  return n.toFixed(digits);
}

export function fmtDuration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds <= 0) return '—';
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}时${m.toString().padStart(2, '0')}分`;
  if (m > 0) return `${m}分${s.toString().padStart(2, '0')}秒`;
  return `${s}秒`;
}

export function fmtBandwidthKBps(kbps: number | null | undefined): string {
  if (kbps == null || !Number.isFinite(kbps) || kbps <= 0) return '—';
  if (kbps >= 1024) return `${(kbps / 1024).toFixed(2)} MB/s`;
  return `${kbps.toFixed(1)} KB/s`;
}

export function fmtBandwidthBytesPerSec(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes.toFixed(0)} B/s`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KB/s`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MB/s`;
}

export function fmtByteSize(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes.toFixed(0)} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
