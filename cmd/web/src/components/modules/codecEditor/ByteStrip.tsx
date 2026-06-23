/**
 * ByteStrip — 字节尺（byte ruler）。
 *
 * 设计目标：这是协议配置里的主视觉与主校验工具，不追求“填满好看”，而追求
 * offset/size 可读、首尾刻度完整、长 header 只在条带内部横向滚动。
 *
 * scope 决策：仅展示 + 点击选中；offset/size 修改仍走 HeaderFieldTable。
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

const MIN_BYTE_PX = 12;
const MAX_BYTE_PX = 32;
const DEFAULT_CONTAINER_PX = 640;
const OFFSET_LABEL_MIN_PX = 42;
const FIELD_NAME_MIN_PX = 44;
const DENSE_HEADER_THRESHOLD = 32;

function clamp(n: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, n));
}

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

/** 按最大 tick 文本长度预留左右安全区，避免 0 和末尾刻度贴边被裁。 */
function rulerPadFor(totalBytes: number): number {
  return Math.max(18, String(Math.max(0, totalBytes)).length * 7 + 12);
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
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState(DEFAULT_CONTAINER_PX);

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    setContainerWidth(el.clientWidth || DEFAULT_CONTAINER_PX);
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        if (entry.contentRect.width > 0) setContainerWidth(entry.contentRect.width);
      }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const rulerPadPx = rulerPadFor(totalBytes);
  const availableTrackPx = Math.max(0, containerWidth - rulerPadPx * 2);
  const pxPerByte = totalBytes > 0
    ? clamp(availableTrackPx / totalBytes, MIN_BYTE_PX, MAX_BYTE_PX)
    : MAX_BYTE_PX;
  const trackWidthPx = totalBytes * pxPerByte;
  const innerWidthPx = trackWidthPx + rulerPadPx * 2;
  const byteLeft = (byte: number) => rulerPadPx + byte * pxPerByte;

  const ticks = rulerTicks(totalBytes);
  const dividers = dividerBytes(totalBytes);
  const selectedRange = selectedIndex !== null ? ranges[selectedIndex] : undefined;
  const selectedCenter = selectedRange ? selectedRange.start + (selectedRange.end - selectedRange.start) / 2 : null;

  return (
    <div className="byte-bench-ruler">
      <div className="byte-bench-caption">
        <Typography.Text className="pce-bench-title">BYTE RULER</Typography.Text>
        <Typography.Text type="secondary" className="pce-bench-meta">
          click span to select · red hatch = overlap / out of bounds
        </Typography.Text>
      </div>

      <div ref={scrollRef} className="bs-scroll">
        <div className="bs-inner" style={{ width: innerWidthPx }}>
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
                  style={{ left: byteLeft(center) }}
                >
                  {r.field.name || '(未命名)'}
                </span>
              );
            })}
            {trailerSize > 0 && (
              <span className="bs-field-label" style={{ left: byteLeft(headerSize + trailerSize / 2) }}>
                trailer
              </span>
            )}
          </div>

          <div className="bs-strip" style={{ left: rulerPadPx, width: trackWidthPx }}>
            {dividers.map((b) => (
              <div key={`div-${b}`} className="bs-divider" style={{ left: b * pxPerByte }} />
            ))}

            {headerSize > 0 && headerSize < totalBytes && (
              <div className="bs-header-border" style={{ left: headerSize * pxPerByte }} />
            )}

            {selectedCenter !== null && (
              <div className="bs-probe" style={{ left: selectedCenter * pxPerByte }} />
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
                      left: r.start * pxPerByte,
                      width: Math.max(sizeBytes * pxPerByte, 2),
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
                  style={{ left: headerSize * pxPerByte, width: trailerSize * pxPerByte }}
                >
                  {trailerSize * pxPerByte >= OFFSET_LABEL_MIN_PX && <span className="bs-trailer-label">trailer</span>}
                </div>
              </Tooltip>
            )}
          </div>

          <div className="bs-ruler-layer">
            {ticks.map((tick) => (
              <span key={`tick-${tick}`} className={tickClass(tick, totalBytes)} style={{ left: byteLeft(tick) }}>
                {tick}
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
