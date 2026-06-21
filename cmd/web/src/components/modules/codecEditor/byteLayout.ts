/**
 * byteLayout — 字节条带的纯计算 helper。
 *
 * 仅展示 + 选中（scope 决策：不做拖拽改跨度，DnD-resize 留后续）。
 * 越界 / 重叠检测在这里做，供 ByteStrip 标红 + HeaderFieldTable 行提示复用。
 */

import type { Field } from '@/types/codec';

/** 某字段在字节条带上的渲染区间 [offset, offset+size)。 */
export interface ByteRange {
  field: Field;
  start: number;
  end: number;
  /** 该区间是否有问题（越界或与其它字段重叠）。 */
  bad: boolean;
}

/**
 * 计算 header 字段的字节区间 + 越界/重叠标记。
 * 越界：end > headerSize 或 start < 0 或 size <= 0。
 * 重叠：与其它任一区间相交（端点相接不算重叠）。
 */
export function computeByteRanges(fields: Field[], headerSize: number): ByteRange[] {
  const ranges: ByteRange[] = fields.map((f) => ({
    field: f,
    start: typeof f.offset === 'number' ? f.offset : 0,
    end: typeof f.offset === 'number' && typeof f.size === 'number' ? f.offset + f.size : 0,
    bad: false,
  }));

  for (const r of ranges) {
    const outOfBounds = r.start < 0 || r.end > headerSize || r.end <= r.start;
    const overlaps = ranges.some((other) => {
      if (other === r) return false;
      // 相交：max(start) < min(end)
      return Math.max(r.start, other.start) < Math.min(r.end, other.end);
    });
    r.bad = outOfBounds || overlaps;
  }
  return ranges;
}

/** 一组稳定区分的色板（按字段在 header 中的索引循环取色）。 */
export const FIELD_COLORS = [
  '#5b8ff9',
  '#5ad8a6',
  '#f6bd16',
  '#e86452',
  '#6dc8ec',
  '#945fb9',
  '#ff9845',
  '#1e9493',
  '#ff99c3',
  '#9d2933',
];

export function fieldColor(index: number): string {
  return FIELD_COLORS[index % FIELD_COLORS.length];
}
