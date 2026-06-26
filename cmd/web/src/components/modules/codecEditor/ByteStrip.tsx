/**
 * ByteStrip — 字节尺（byte ruler）。
 *
 * 主视觉与主校验工具。字段/刻度用**百分比**定位（相对条带宽度），布局不依赖 JS 测宽——
 * 字段增减、容器宽度变化（如祖先出现滚动条）都自动等比适配，结构上不会挤出横向滚动条。
 * scope：仅展示 + 点击选中；offset/size 修改走 HeaderFieldTable。
 */

import { useLayoutEffect, useRef, useState } from 'react';
import { Tooltip, Typography } from 'antd';
import type { CodecSchema, Field } from '@/types/codec';
import { computeByteRanges, fieldColor } from './byteLayout';

export interface ByteStripProps {
  schema: CodecSchema;
  /** 当前选中字段的 index（用于高亮）。 */
  selectedIndex: number | null;
  onSelect: (index: number) => void;
}

const DEFAULT_CONTAINER_PX = 640;
const OFFSET_LABEL_MIN_PX = 42;
const FIELD_NAME_MIN_PX = 44;
const DENSE_HEADER_THRESHOLD = 32;

function tickStepFor(totalBytes: number): number {
  if (totalBytes <= 8) return 1;
  if (totalBytes <= 16) return 2;
  if (totalBytes <= 32) return 4;
  return 8;
}

function rulerTicks(totalBytes: number): number[] {
  if (totalBytes <= 0) return [];
  const step = tickStepFor(totalBytes);
  const ticks: number[] = [];
  for (let i = 0; i <= totalBytes; i += step) ticks.push(i);
  if (ticks[ticks.length - 1] !== totalBytes) ticks.push(totalBytes);
  return ticks;
}

function dividerBytes(totalBytes: number): number[] {
  if (totalBytes <= 0) return [];
  if (totalBytes <= DENSE_HEADER_THRESHOLD) {
    return Array.from({ length: totalBytes + 1 }, (_, i) => i);
  }
  const step = tickStepFor(totalBytes);
  const ticks: number[] = [];
  for (let i = 0; i <= totalBytes; i += step) ticks.push(i);
  if (ticks[ticks.length - 1] !== totalBytes) ticks.push(totalBytes);
  return ticks;
}

/** 按最大 tick 文本长度预留左右安全区，避免首尾刻度贴边被裁。 */
function rulerPadFor(totalBytes: number): number {
  return Math.max(18, String(Math.max(0, totalBytes)).length * 7 + 12);
}

/** 字节位置 → 相对条带宽度的百分比。 */
function pct(n: number, total: number): string {
  return total > 0 ? `${(n / total) * 100}%` : '0%';
}

function tickClass(tick: number, totalBytes: number): string {
  if (tick === 0) return 'bs-tick bs-tick-start';
  if (tick === totalBytes) return 'bs-tick bs-tick-end';
  return 'bs-tick';
}

export function ByteStrip({ schema, selectedIndex, onSelect }: ByteStripProps) {
  const fields: Field[] = schema.header ?? [];
  const headerSize: number = schema.frame?.headerSize ?? 0;
  const trailerSize: number = schema.frame?.trailerSize ?? 0;
  const ranges = computeByteRanges(fields, headerSize);

  const totalBytes = Math.max(0, headerSize) + Math.max(0, trailerSize);
  const stripRef = useRef<HTMLDivElement | null>(null);
  // stripPx 仅用于判断 offset/字段名标签是否显示（非布局）；布局走百分比，不依赖它的精度。
  const [stripPx, setStripPx] = useState(DEFAULT_CONTAINER_PX);

  useLayoutEffect(() => {
    const el = stripRef.current;
    if (!el) return;
    const measure = () => setStripPx(el.clientWidth || DEFAULT_CONTAINER_PX);
    measure();
    const ro = new ResizeObserver(() => measure());
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const rulerPadPx = rulerPadFor(totalBytes);
  const pxPerByte = totalBytes > 0 ? stripPx / totalBytes : 0;

  const ticks = rulerTicks(totalBytes);
  const dividers = dividerBytes(totalBytes);
  const selectedRange = selectedIndex !== null ? ranges[selectedIndex] : undefined;
  const selectedCenter = selectedRange ? selectedRange.start + (selectedRange.end - selectedRange.start) / 2 : null;

  return (
    <div className="byte-bench-ruler">
      <div className="byte-bench-caption">
        <Typography.Text className="pce-bench-title">字节尺</Typography.Text>
        <Typography.Text type="secondary" className="pce-bench-meta">
          点击色块选中字段 · 红色斜纹 = 越界或重叠
        </Typography.Text>
      </div>

      <div className="bs-scroll">
        {/* paddingInline 留出首尾刻度安全区；内部三层用百分比撑满 content，随容器自适应 */}
        <div className="bs-inner" style={{ padding: `0 ${rulerPadPx}px` }}>
          <div className="bs-label-layer">
            {ranges.map((r, i) => {
              const sizeBytes = r.end - r.start;
              const renderPx = sizeBytes * pxPerByte;
              const isSel = selectedIndex === i;
              if (renderPx < FIELD_NAME_MIN_PX && !isSel) return null;
              const center = r.start + sizeBytes / 2;
              return (
                <span
                  key={`lbl-${i}`}
                  className={`bs-field-label${isSel ? ' bs-field-label-sel' : ''}`}
                  style={{ left: pct(center, totalBytes) }}
                >
                  {r.field.name || '(未命名)'}
                </span>
              );
            })}
            {trailerSize > 0 && (
              <span className="bs-field-label" style={{ left: pct(headerSize + trailerSize / 2, totalBytes) }}>
                trailer
              </span>
            )}
          </div>

          <div ref={stripRef} className="bs-strip">
            {dividers.map((b) => (
              <div key={`div-${b}`} className="bs-divider" style={{ left: pct(b, totalBytes) }} />
            ))}

            {headerSize > 0 && headerSize < totalBytes && (
              <div className="bs-header-border" style={{ left: pct(headerSize, totalBytes) }} />
            )}

            {selectedCenter !== null && (
              <div className="bs-probe" style={{ left: pct(selectedCenter, totalBytes) }} />
            )}

            {ranges.map((r, i) => {
              const sizeBytes = r.end - r.start;
              const renderPx = sizeBytes * pxPerByte;
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
                    className={`bs-field${isSel ? ' bs-field-sel' : ''}${r.bad ? ' bs-field-bad' : ''}`}
                    onClick={() => onSelect(i)}
                    style={{
                      left: pct(r.start, totalBytes),
                      width: pct(sizeBytes, totalBytes),
                      background: r.bad ? 'var(--pce-fault)' : fieldColor(i),
                      opacity: isSel ? 1 : 0.84,
                    }}
                  >
                    {renderPx >= OFFSET_LABEL_MIN_PX && sizeBytes > 0 && (
                      <span className="bs-field-offset">{r.start}..{r.end - 1}</span>
                    )}
                  </div>
                </Tooltip>
              );
            })}

            {trailerSize > 0 && (
              <Tooltip title={`trailer · ${headerSize}–${headerSize + trailerSize}（灰色：不计入 header 字段）`}>
                <div
                  className="bs-trailer"
                  style={{ left: pct(headerSize, totalBytes), width: pct(trailerSize, totalBytes) }}
                >
                  {trailerSize * pxPerByte >= OFFSET_LABEL_MIN_PX && <span className="bs-trailer-label">trailer</span>}
                </div>
              </Tooltip>
            )}
          </div>

          <div className="bs-ruler-layer">
            {ticks.map((tick) => (
              <span key={`tick-${tick}`} className={tickClass(tick, totalBytes)} style={{ left: pct(tick, totalBytes) }}>
                {tick}
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
