/**
 * ByteStrip — 字节条带（byte map），三明治布局。
 *
 * 三层（自上而下，共享同一个宽度基准 = 色带总宽，水平对齐）：
 *   1. 字段名标签层（色块上方）
 *   2. 色带层（色块 + offset 范围内显）
 *   3. 字节刻度尺层（色带下方）
 *
 * 自适应宽度：百分比定位（left/width 基于 totalBytes 的百分比），不再固定 BYTE_PX。
 *   - 容器 width:100% 填满父容器。
 *   - MIN_BYTE_PX=8：当 totalBytes×8 > 容器宽时，内层 min-width=totalBytes×8 + 外层横向滚动；
 *     否则内层 width:100%，不滚动。
 *   - 三层包同一个内层容器（共用 min-width/width），保证标签/色块/刻度尺对齐。
 *
 * scope 决策：**仅展示 + 点击选中**，不做拖拽改跨度（改跨度走 HeaderFieldTable 输入）。
 * 越界 / 重叠的区段标红（错误明细复用 liveErrors，本组件只做视觉提示）。
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

/** 每字节最小渲染宽度（px）。totalBytes×8 超过容器宽时触发横向滚动。 */
const MIN_BYTE_PX = 8;

/** 色块渲染宽度阈值（px）：≥ 此值才在色块内显示 offset 范围文字。 */
const OFFSET_LABEL_MIN_PX = 40;

/** 字段名标签渲染宽度阈值（px）：色块 ≥ 此值才显示上方字段名标签（选中字段强制显示）。 */
const FIELD_NAME_MIN_PX = 36;

/** headerSize 超过此值时刻度尺/字节分隔线采用更大 step，避免太密。 */
const DENSE_HEADER_THRESHOLD = 32;

/**
 * 自适应刻度间隔：按 totalBytes 取「每 N 字节一个 tick」，保证刻度密但不拥挤。
 *   - totalBytes ≤ 8：每 1 字节一个 tick。
 *   - totalBytes ≤ 16：每 2 字节一个 tick。
 *   - totalBytes ≤ 32：每 4 字节一个 tick。
 *   - 更大：每 8 字节一个 tick。
 * 起始 tick = 0，末尾 tick = totalBytes（边界，若不是 step 倍数单独补一个）。
 */
function tickStepFor(totalBytes: number): number {
  if (totalBytes <= 8) return 1;
  if (totalBytes <= 16) return 2;
  if (totalBytes <= 32) return 4;
  return 8;
}

/** 计算刻度尺上应标注的字节序号集合（含末字节边界，去重 + 保序）。 */
function rulerTicks(totalBytes: number): number[] {
  if (totalBytes <= 0) return [];
  const step = tickStepFor(totalBytes);
  const ticks: number[] = [];
  for (let i = 0; i <= totalBytes; i += step) ticks.push(i);
  // 末字节边界单独补（仅当未自然落在 step 倍数上）。
  if (ticks[ticks.length - 1] !== totalBytes) ticks.push(totalBytes);
  return ticks;
}

/** 列出需要画字节分隔线的字节序号（0..totalBytes），大 header 时按 step 抽稀。 */
function dividerBytes(totalBytes: number): number[] {
  if (totalBytes <= 0) return [];
  if (totalBytes <= DENSE_HEADER_THRESHOLD) {
    return Array.from({ length: totalBytes + 1 }, (_, i) => i);
  }
  // 大 header：只画 step 倍数处，避免 divider 糊成实色。
  const step = tickStepFor(totalBytes);
  const ticks: number[] = [];
  for (let i = 0; i <= totalBytes; i += step) ticks.push(i);
  if (ticks[ticks.length - 1] !== totalBytes) ticks.push(totalBytes);
  return ticks;
}

/** 字节序号 → 相对于 totalBytes 的百分比 left 定值。totalBytes=0 时返回 0（guard）。 */
function pct(byte: number, totalBytes: number): number {
  if (totalBytes <= 0) return 0;
  return (byte / totalBytes) * 100;
}

/** containerWidth 初始兜底值（避免首帧 realPxPerByte=0 导致标签全隐藏）。 */
const DEFAULT_CONTAINER_PX = 480;

export function ByteStrip({ schema, selectedIndex, onSelect }: ByteStripProps) {
  const fields: Field[] = schema.header ?? [];
  const headerSize: number = schema.frame?.headerSize ?? 0;
  const trailerSize: number = schema.frame?.trailerSize ?? 0;
  const ranges = computeByteRanges(fields, headerSize);

  // 整条带宽度 = header + trailer（字节）。
  const totalBytes = Math.max(0, headerSize) + Math.max(0, trailerSize);

  // 内层容器基准宽度：totalBytes×MIN_BYTE_PX 触发横向滚动；否则 width:100% 填满。
  const minWidthPx = totalBytes * MIN_BYTE_PX;
  const innerWidthStyle = totalBytes > 0 ? { minWidth: minWidthPx, width: '100%' } : { width: '100%' };

  // 实测内层容器宽度（三层共享 .bs-inner）：ResizeObserver 自动兼容两种模式。
  //   - 非滚动：clientWidth = 容器宽，realPxPerByte = containerWidth/totalBytes（远大于 8）。
  //   - 滚动：min-width 撑开 clientWidth = totalBytes×MIN_BYTE_PX，realPxPerByte = 8（与原估算一致）。
  const innerRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState<number>(DEFAULT_CONTAINER_PX);

  useLayoutEffect(() => {
    const el = innerRef.current;
    if (!el) return;
    // 初次同步测量（ResizeObserver 首帧回调前避免闪烁）。
    setContainerWidth(el.clientWidth);
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        // contentRect.width 已减去 padding/border，与 clientWidth 一致语义。
        const w = entry.contentRect.width;
        if (w > 0) setContainerWidth(w);
      }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // 真实每字节像素：用实测容器宽 / totalBytes；totalBytes=0 时无字段无标签，置 0 安全。
  const realPxPerByte = totalBytes > 0 ? containerWidth / totalBytes : 0;
  const pxPerByte = realPxPerByte;

  const ticks = rulerTicks(totalBytes);
  const dividers = dividerBytes(totalBytes);

  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        字节条带（点击区块选中对应字段；红色表示越界或重叠）
      </Typography.Text>
      {/* 外层：横向滚动容器（仅在 totalBytes×8 超出容器宽时触发） */}
      <div className="bs-scroll" style={{ marginTop: 6, marginBottom: 4 }}>
        {/* 内层：三层共用基准宽度，保证标签/色带/刻度尺水平对齐 */}
        <div ref={innerRef} className="bs-inner" style={innerWidthStyle}>
          {/* === 层 1：字段名标签 === */}
          <div className="bs-label-layer">
            {ranges.map((r, i) => {
              const sizeBytes = r.end - r.start;
              const renderPx = sizeBytes * pxPerByte;
              const isSel = selectedIndex === i;
              // 色块够宽才显示；选中字段强制显示。
              if (renderPx < FIELD_NAME_MIN_PX && !isSel) return null;
              const center = r.start + sizeBytes / 2;
              return (
                <span
                  key={`lbl-${i}`}
                  className={`bs-field-label${isSel ? ' bs-field-label-sel' : ''}`}
                  style={{ left: `${pct(center, totalBytes)}%` }}
                >
                  {r.field.name || '(未命名)'}
                </span>
              );
            })}
            {trailerSize > 0 && (
              <span
                className="bs-field-label"
                style={{ left: `${pct(headerSize + trailerSize / 2, totalBytes)}%` }}
              >
                trailer
              </span>
            )}
          </div>

          {/* === 层 2：色带 === */}
          <div className="bs-strip">
            {/* 字节分隔线 */}
            {dividers.map((b) => (
              <div
                key={`div-${b}`}
                className="bs-divider"
                style={{ left: `${pct(b, totalBytes)}%` }}
              />
            ))}

            {/* headerSize 边界线（区分 header 与 trailer） */}
            {headerSize > 0 && headerSize < totalBytes && (
              <div
                className="bs-header-border"
                style={{ left: `${pct(headerSize, totalBytes)}%` }}
              />
            )}

            {/* 字段色块 */}
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
                      left: `${pct(r.start, totalBytes)}%`,
                      width: `max(${pct(sizeBytes, totalBytes)}%, 2px)`,
                      background: r.bad ? 'var(--color-error)' : fieldColor(i),
                      opacity: isSel ? 1 : 0.82,
                    }}
                  >
                    {/* 色块内 offset 范围（仅够宽时显示） */}
                    {renderPx >= OFFSET_LABEL_MIN_PX && sizeBytes > 0 && (
                      <span className="bs-field-offset">
                        {r.start}..{r.end - 1}
                      </span>
                    )}
                  </div>
                </Tooltip>
              );
            })}

            {/* trailer 段（header 之后） */}
            {trailerSize > 0 && (
              <Tooltip title={`trailer · ${headerSize}–${headerSize + trailerSize}（灰色：不计入 header 字段）`}>
                <div
                  className="bs-trailer"
                  style={{
                    left: `${pct(headerSize, totalBytes)}%`,
                    width: `${pct(trailerSize, totalBytes)}%`,
                  }}
                >
                  {trailerSize * pxPerByte >= OFFSET_LABEL_MIN_PX && (
                    <span className="bs-trailer-label">trailer</span>
                  )}
                </div>
              </Tooltip>
            )}
          </div>

          {/* === 层 3：刻度尺 === */}
          <div className="bs-ruler-layer">
            {ticks.map((tick) => (
              <span
                key={`tick-${tick}`}
                className="bs-tick"
                style={{ left: `${pct(tick, totalBytes)}%` }}
              >
                {tick}
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
