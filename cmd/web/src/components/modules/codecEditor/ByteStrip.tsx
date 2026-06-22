/**
 * ByteStrip — 字节条带（byte map）。
 *
 * scope 决策：**仅展示 + 点击选中**，不做拖拽改跨度（改跨度走 HeaderFieldTable 输入）。
 *   // DnD-resize（拖拽改 offset/size）留后续。
 *
 * 渲染：headerSize 个字节宽度的条带，每个 header 字段占 [offset, offset+size) 一段彩色区块，
 * 标注 name/offset+size/type；trailerSize>0 时在 header 段后再画一段灰色 trailer。
 * 越界 / 重叠的区段标红（错误明细复用 liveErrors，本组件只做视觉提示）。
 */

import { Tooltip, Typography } from 'antd';
import type { CodecSchema, Field } from '@/types/codec';
import { computeByteRanges, fieldColor } from './byteLayout';

export interface ByteStripProps {
  schema: CodecSchema;
  /** 当前选中字段的 index（用于高亮）。 */
  selectedIndex: number | null;
  onSelect: (index: number) => void;
}

// 每字节的渲染宽度（px）。trailer 段同此宽度。
const BYTE_PX = 18;

/**
 * 自适应刻度间隔：按 headerSize 取「每 N 字节一个 tick」，保证刻度密但不拥挤。
 *   - headerSize ≤ 8：每 1 字节一个 tick（全程密集，便于精确读偏移）。
 *   - headerSize ≤ 16：每 2 字节一个 tick。
 *   - headerSize ≤ 32：每 4 字节一个 tick。
 *   - 更大：每 8 字节一个 tick（避免长条带时刻度挤成一片）。
 * 起始 tick = 0，末尾 tick = headerSize-1（若不是间隔倍数，单独补一个，便于看末边界）。
 */
function tickStepFor(headerSize: number): number {
  if (headerSize <= 8) return 1;
  if (headerSize <= 16) return 2;
  if (headerSize <= 32) return 4;
  return 8;
}

/** 计算偏移标尺上应标注的字节序号集合（去重 + 保序）。 */
function rulerTicks(headerSize: number): number[] {
  if (headerSize <= 0) return [];
  const step = tickStepFor(headerSize);
  const ticks: number[] = [];
  for (let i = 0; i < headerSize; i += step) ticks.push(i);
  // 末字节单独补（仅当未自然落在 step 倍数上）。
  const last = headerSize - 1;
  if (ticks[ticks.length - 1] !== last) ticks.push(last);
  return ticks;
}

export function ByteStrip({ schema, selectedIndex, onSelect }: ByteStripProps) {
  const fields: Field[] = schema.header ?? [];
  const headerSize: number = schema.frame?.headerSize ?? 0;
  const trailerSize: number = schema.frame?.trailerSize ?? 0;
  const ranges = computeByteRanges(fields, headerSize);

  // 整条带宽度 = header + trailer。
  const totalBytes = Math.max(0, headerSize) + Math.max(0, trailerSize);
  const stripWidth = totalBytes * BYTE_PX;

  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        字节条带（点击区块选中对应字段；红色表示越界或重叠）
      </Typography.Text>
      <div
        style={{
          position: 'relative',
          width: stripWidth > 0 ? stripWidth : '100%',
          minWidth: 120,
          height: 44,
          marginTop: 6,
          marginBottom: 4,
          background: 'var(--hover-bg)',
          borderRadius: 4,
          border: '1px solid var(--border-color)',
        }}
      >
        {headerSize > 0 &&
          Array.from({ length: headerSize }, (_, i) => (
            <div
              key={`tick-${i}`}
              style={{
                position: 'absolute',
                left: i * BYTE_PX,
                top: 0,
                bottom: 0,
                width: 1,
                background: 'var(--divider-bg)',
              }}
            />
          ))}
        {ranges.map((r, i) => {
          const left = r.start * BYTE_PX;
          const width = Math.max(0, (r.end - r.start)) * BYTE_PX;
          const isSel = selectedIndex === i;
          return (
            <Tooltip
              key={`field-${i}`}
              title={
                <span>
                  {r.field.name || '(未命名)'} · offset {r.start}–{r.end} · {r.field.type}
                  {r.bad ? ' · 越界或重叠' : ''}
                </span>
              }
            >
              <div
                onClick={() => onSelect(i)}
                style={{
                  position: 'absolute',
                  left,
                  top: 4,
                  width: Math.max(width, 4),
                  bottom: 4,
                  background: r.bad ? 'var(--color-error)' : fieldColor(i),
                  opacity: isSel ? 1 : 0.78,
                  outline: isSel ? '2px solid var(--color-blue)' : 'none',
                  outlineOffset: 1,
                  borderRadius: 3,
                  cursor: 'pointer',
                  color: '#fff',
                  fontSize: 11,
                  padding: '0 4px',
                  overflow: 'hidden',
                  whiteSpace: 'nowrap',
                  textOverflow: 'ellipsis',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  userSelect: 'none',
                }}
              >
                {r.field.name || '?'}
              </div>
            </Tooltip>
          );
        })}
        {/* trailer 段（header 之后） */}
        {trailerSize > 0 && (
          <Tooltip title={`trailer · ${headerSize}–${headerSize + trailerSize}（灰色：不计入 header 字段）`}>
            <div
              style={{
                position: 'absolute',
                left: headerSize * BYTE_PX,
                top: 4,
                width: trailerSize * BYTE_PX,
                bottom: 4,
                background: 'var(--badge-bg)',
                borderRadius: 3,
                color: 'var(--text-secondary)',
                fontSize: 11,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                cursor: 'default',
                userSelect: 'none',
              }}
            >
              trailer
            </div>
          </Tooltip>
        )}
        {/* 偏移标尺：自适应间隔加密 tick（headerSize 越大步长越大，保持刻度密但不拥挤） */}
        <div style={{ position: 'relative', height: 14, marginTop: 2, fontSize: 10, color: 'var(--text-tertiary)' }}>
          {rulerTicks(headerSize).map((tick) => (
            <span key={`lab-${tick}`} style={{ position: 'absolute', left: tick * BYTE_PX }}>
              {tick}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}
