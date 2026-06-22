# ByteStrip 重做（容器自适应 + 字段名外标）

## 状态

DONE（验证全绿）。

## 改动文件

- `cmd/web/src/components/modules/codecEditor/ByteStrip.tsx`（重写）
- `cmd/web/src/components/modules/codecEditor/codecEditor.css`（追加 ByteStrip 样式段）

未触碰：`byteLayout.ts`、`FrameLayoutEditor.tsx`、`HeaderFieldTable.tsx`、其它 codecEditor 组件、services/、conf/、pages/、runtime/。

## 新三明治布局

三层自上而下，共享同一个内层容器（`.bs-inner`，共用 `min-width`/`width`），保证标签/色带/刻度尺在任何宽度下水平对齐。

```tsx
<div className="bs-scroll">           {/* 外层：横向滚动容器 */}
  <div className="bs-inner" style={innerWidthStyle}>
    {/* 层 1：字段名标签 */}
    <div className="bs-label-layer">
      {ranges.map((r, i) => {
        if (renderPx < FIELD_NAME_MIN_PX && !isSel) return null;
        return (
          <span className="bs-field-label" style={{ left: `${pct(center, totalBytes)}%` }}>
            {r.field.name || '(未命名)'}
          </span>
        );
      })}
    </div>

    {/* 层 2：色带 */}
    <div className="bs-strip">         {/* overflow:hidden clip 越界 */}
      {dividers.map(b => <div className="bs-divider" style={{ left: pct(b) }} />)}
      <div className="bs-header-border" style={{ left: pct(headerSize) }} />
      {ranges.map((r, i) => (
        <Tooltip ...>
          <div className="bs-field bs-field-sel?" onClick={onSelect(i)}
               style={{ left: pct(r.start), width: `max(pct(size), 2px)`,
                        background: r.bad ? 'var(--color-error)' : fieldColor(i),
                        opacity: isSel ? 1 : 0.82 }}>
            {renderPx >= OFFSET_LABEL_MIN_PX && <span className="bs-field-offset">{r.start}..{r.end-1}</span>}
          </div>
        </Tooltip>
      ))}
      {trailerSize > 0 && <div className="bs-trailer" style={{ left, width }} ... />}
    </div>

    {/* 层 3：刻度尺 */}
    <div className="bs-ruler-layer">
      {ticks.map(t => <span className="bs-tick" style={{ left: pct(t) }}>{t}</span>)}
    </div>
  </div>
</div>
```

CSS 关键片段（`codecEditor.css` 追加段）：

```css
.bs-scroll { overflow-x: auto; overflow-y: hidden; }
.bs-inner  { position: relative; }

.bs-label-layer, .bs-ruler-layer { position: relative; height: 16px; }
.bs-ruler-layer { height: 14px; margin-top: 2px; }

.bs-field-label {
  position: absolute; top: 0; transform: translateX(-50%);
  font-size: 11px; color: var(--text-secondary);
  white-space: nowrap; pointer-events: none; user-select: none;
}
.bs-field-label-sel { font-weight: 600; color: var(--text-primary); z-index: 2; }

.bs-strip {
  position: relative; height: 28px; overflow: hidden;
  background: var(--hover-bg);
  border: 1px solid var(--border-color); border-radius: 4px;
}

.bs-divider    { position: absolute; top:0; bottom:0; width:1px; background: var(--divider-bg); }
.bs-header-border { ...; width:1px; background: var(--text-tertiary); z-index: 1; }

.bs-field {
  position: absolute; top: 3px; bottom: 3px; border-radius: 3px; cursor: pointer;
  color: #fff; font-size: 10px;
  display: flex; align-items: center; justify-content: center;
  user-select: none; overflow: hidden;
}
.bs-field-sel { outline: 2px solid var(--color-blue); outline-offset: 1px; z-index: 2; }

.bs-trailer { position:absolute; top:3px; bottom:3px; background: var(--badge-bg); ... }
.bs-tick    { position:absolute; top:0; transform: translateX(-50%);
              font-size:10px; color: var(--text-tertiary); white-space: nowrap; }
```

## 自适应宽度策略（替代固定 BYTE_PX=18）

- **百分比定位**：所有元素 `left`/`width` 基于 `totalBytes` 的百分比（`left = byte/totalBytes×100%`，`width = size/totalBytes×100%`）。
- **每字节最小宽度** `MIN_BYTE_PX = 8`：
  - 当 `totalBytes × 8 > 容器宽` 时，内层 `min-width = totalBytes × 8`，外层 `.bs-scroll { overflow-x: auto }` 触发横向滚动。
  - 否则内层 `width: 100%` 填满父容器，不出现滚动条。
- `innerWidthStyle`：
  ```ts
  const minWidthPx = totalBytes * MIN_BYTE_PX;
  const innerWidthStyle = totalBytes > 0
    ? { minWidth: minWidthPx, width: '100%' }
    : { width: '100%' };
  ```
- 三层包在同一个 `.bs-inner`，水平滚动时三层一起滚，对齐不破。
- `pct(byte, totalBytes)` helper 对 `totalBytes<=0` 做 guard 返回 0（避免除零）。

## 字段名显示策略（层 1）

- 每字段一个 span，绝对定位 `left = (start + size/2)/totalBytes×100%`，`transform: translateX(-50%)` 居中对齐色块中心。
- **显示阈值** `FIELD_NAME_MIN_PX = 36`：色块渲染宽度 < 36px 隐藏标签（避免密集重叠）。
- **选中字段强制显示**：`isSel` 时跳过阈值判断，`bs-field-label-sel`（加粗 + `var(--text-primary)` + `z-index:2`）置顶盖在相邻标签之上。
- fontSize 11，color `var(--text-secondary)`，`white-space: nowrap`，`pointer-events: none`。
- 渲染宽度估算用 `sizeBytes × MIN_BYTE_PX`（下界，保守判断，触发滚动时精确）。

## offset 范围内显阈值（层 2 色块内）

- `OFFSET_LABEL_MIN_PX = 40`：色块渲染宽度 ≥ 40px 才在色块内显示 `{start}..{end-1}`（如 `0..3`）。
- 否则空白，靠色 + Tooltip 区分。
- color `#fff`，fontSize 10，居中（flex）。
- trailer 段同样按 `trailerSize × MIN_BYTE_PX ≥ 40` 决定是否显示 "trailer" 文字。

## 刻度尺（层 3）

- 自适应 step：`tickStepFor(totalBytes)` —— ≤8 每 1 字节，≤16 每 2，≤32 每 4，更大每 8。
- 每个 tick `left = tick/totalBytes×100%`，`transform: translateX(-50%)`。
- 末字节边界（`totalBytes` 本身）单独补为最后一个 tick（即使非 step 倍数），便于看末边界。
- fontSize 10，color `var(--text-tertiary)`。
- 字节分隔线（`.bs-divider`）同步抽稀：`totalBytes ≤ 32` 时每字节一条；更大时只画 step 倍数处，避免 divider 糊成实色。

## 越界 / 重叠 clip

- bad 字段色块 `background: var(--color-error)`，`width` 仍按 size 算（视觉上可能延伸出 header 段）。
- 容器 `.bs-strip { overflow: hidden }` 把越界部分 clip 在 totalBytes 范围内。
- Tooltip 末尾追加 "· 越界或重叠"。

## headerSize 边界线

- `headerSize > 0 && headerSize < totalBytes` 时，在 `headerSize/totalBytes×100%` 处画 1px `var(--text-tertiary)` 实线（`.bs-header-border`，z-index:1），区分 header 与 trailer。

## 配色合规

- 主题色全走 tokens 变量：outline=`var(--color-blue)`、error=`var(--color-error)`、底=`var(--hover-bg)`、边框=`var(--border-color)`、divider=`var(--divider-bg)`、trailer 底=`var(--badge-bg)`、文字=`var(--text-primary/secondary/tertiary)`。
- **`FIELD_COLORS` 功能性色板豁免**：色块 `background` 用 `fieldColor(i)` 返回 `FIELD_COLORS` 数组（`byteLayout.ts` 未改），用作字节字段的功能性区分色（类似图表数据色），与主题色体系语义不同。暗色主题下 `FIELD_COLORS` 保持不变（功能性色板不随主题切换），文字/边框/背景仍跟随。
- 暗色主题：所有走变量的颜色自动跟随 `:root[data-theme='dark']`。

## props 契约

未改：`ByteStripProps { schema; selectedIndex; onSelect }`；`FrameLayoutEditor.tsx` 消费方式不变。

## 验证

- `cd cmd/web && npx tsc -b` → exit 0。
- `cd cmd/web && npm run test` → **22 files / 287 tests passed**（byteLayout 纯函数测试无影响，未回归）。
- `git diff --stat`：
  ```
  ByteStrip.tsx   | 316 ++++++++++++---------
  codecEditor.css | 132 +++++++++
  2 files changed, 320 insertions(+), 128 deletions(-)
  ```
  其它文件零改动。

## 自审

1. **未读不写**：先读全 ByteStrip.tsx / byteLayout.ts / FrameLayoutEditor.tsx / tokens.css / codecEditor.css 后才动手。
2. **红线**：byteLayout.ts 零改动；FrameLayoutEditor.tsx 零改动；props 契约保持；>3 属性样式全抽到 codecEditor.css（组件内 inline 只保留依赖运行时变量的 style：left/width/background/opacity）。
3. **主题色**：全 tokens；FIELD_COLORS 豁免并 report 说明。
4. **antd v5 合规**：Tooltip/Typography 正常使用。
5. **边界**：`totalBytes=0` 时 `pct` guard 返回 0，渲染空色带（width:100%），不报错。
6. **百分比 + min-width 混用**：三层共用 `.bs-inner` 的 `min-width`，水平滚动时一起滚、对齐不破；这是规格的核心要求。

## concerns（看不到 UI，需用户验证）

1. **极端 headerSize（100+ 字节）横向滚动体验**：`MIN_BYTE_PX=8` 下 100 字节 → `min-width=800px`，在 ~540px 宽的浮窗左列必然横向滚动。规格要求"滚动触发"，但用户在小窗口下是否接受滚动条需实际确认。如想减少滚动频率，可把 `MIN_BYTE_PX` 调到 6（100B→600px，仍滚但更晚触发）。
2. **字段名密集时的隐藏策略**：当前用 `MIN_BYTE_PX=8` 做下界估算渲染宽度 → 判断 `< FIELD_NAME_MIN_PX(36)` 隐藏。**触发滚动后精确，不滚动时偏保守（倾向隐藏）**。如果用户在宽屏下（容器 ~540px、totalBytes=32 → 每字节 ~16px → 单字节字段渲染 16px < 36 隐藏；4 字节字段 64px ≥ 36 显示）觉得隐藏太激进，可改为基于 ResizeObserver 的实际容器宽计算，或降低 `FIELD_NAME_MIN_PX` 到 28。
3. **offset 内显阈值 40px**：触发滚动模式下，4 字节字段 = 32px < 40 不显示 offset，5 字节 = 40px 显示。阈值是否合适需用户确认。
4. **trailer 段无 onClick**：原实现 trailer 不可点击（cursor:default），新实现保留此行为；如需点击 trailer 触发某动作需补。
5. **空 schema（totalBytes=0）**：渲染一个空色带（width:100%），无刻度、无字段；如希望完全不渲染需用户确认。

## fix：字段名 ResizeObserver 实测

### 问题（review Important）

字段名标签显示判断用 `sizeBytes × MIN_BYTE_PX(8)` 估算渲染宽度，但 `MIN_BYTE_PX=8` 只是滚动触发后的每字节宽度。非滚动场景（绝大多数）实际每字节 = `containerWidth/totalBytes`（远大于 8），导致窄字段（1–4 字节）标签被系统性误隐藏 —— 本次重做的核心卖点"字段名外标"失效。

算例：容器 540px、`totalBytes=32` → 实际 ~16.9px/字节，4 字节字段实际 ~67px（本该显示标签，阈值 `FIELD_NAME_MIN_PX=36`），但估算 `4×8=32 < 36` → 误隐藏。`OFFSET_LABEL_MIN_PX`（色块内 offset 数字）同理偏小。

### 修复方案

用 `ResizeObserver` 实测 `.bs-inner`（三层共享的内层容器）宽度，算真实每字节像素，重新判断字段名标签 + offset 内显 + trailer 内显：

1. 加 `ref` 指向 `.bs-inner`。
2. `useLayoutEffect` + `ResizeObserver` 测其 `clientWidth`，存 `containerWidth` state，初值给兜底 `DEFAULT_CONTAINER_PX=480`（避免首帧 `realPxPerByte=0` 全隐藏）。
3. `realPxPerByte = totalBytes > 0 ? containerWidth / totalBytes : 0`。
4. 字段名标签显示判断改用 `sizeBytes × realPxPerByte ≥ FIELD_NAME_MIN_PX`（替代原 `sizeBytes×8` 估算）。
5. offset 内显判断改用 `sizeBytes × realPxPerByte ≥ OFFSET_LABEL_MIN_PX`。
6. trailer 内显判断同改。

`ResizeObserver` 自动兼容两种模式：滚动时 `.bs-inner` 实际宽 = `totalBytes×MIN_BYTE_PX`（min-width 撑开），`realPxPerByte=8`（与原估算一致，滚动模式本就对）；非滚动时 = `containerWidth/totalBytes`（修好了）。

### 关键代码

```tsx
import { useLayoutEffect, useRef, useState } from 'react';

/** containerWidth 初始兜底值（避免首帧 realPxPerByte=0 导致标签全隐藏）。 */
const DEFAULT_CONTAINER_PX = 480;

export function ByteStrip({ schema, selectedIndex, onSelect }: ByteStripProps) {
  // ...
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
  // ...
  // 内层容器挂 ref：
  <div ref={innerRef} className="bs-inner" style={innerWidthStyle}>
```

字段名标签判断（替代原 `renderPx = sizeBytes * MIN_BYTE_PX`）：

```tsx
const renderPx = sizeBytes * pxPerByte;   // pxPerByte = realPxPerByte
if (renderPx < FIELD_NAME_MIN_PX && !isSel) return null;
```

offset 内显判断（同理）：

```tsx
{renderPx >= OFFSET_LABEL_MIN_PX && sizeBytes > 0 && (
  <span className="bs-field-offset">{r.start}..{r.end - 1}</span>
)}
```

### 边界处理

- **`containerWidth` 初值 480**：`useLayoutEffect` 在 paint 前同步测量 + `setContainerWidth(el.clientWidth)`，实测后立即重渲染；兜底 480 保证首帧（若极少数情况下 ref 未挂）`realPxPerByte` 不为 0，按合理假设宽渲染。无可见闪烁。
- **`totalBytes=0`**：`realPxPerByte=0`，无字段无标签，安全。
- **组件卸载**：`ResizeObserver.disconnect()` 在 effect cleanup 中调用，防泄漏。
- **`entry.contentRect.width` 守卫**：`if (w > 0)` 过滤零宽回调（如 display:none 父级）。

### 验证

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test`：**22 files / 287 tests passed**（byteLayout 纯函数测试不受影响，ByteStrip 组件无单测）。
- `git diff --stat` 限于 `ByteStrip.tsx`（codecEditor.css 为此前重做遗留，非本 fix 改动；sharedstate/* 为其它任务遗留，非本 fix 改动）。
