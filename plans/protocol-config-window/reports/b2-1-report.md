# B2-1 + B2-2 — 协议配置浮窗：布局重构 + 配色统一

## 范围回顾

- B2-1 布局重构：去 magic number、工具条分组、视图切换归位、两列布局、source 文本移位。
- B2-2 配色统一：CSS 变量命名归一、去硬编码色、视觉重量对齐。

不动逻辑（state/handler/service），只动样式与布局结构。

## 一、布局怎么改的（B2-1）

### 1. 去 magic number（`calc(100vh - 440px)` 全部移除）

ProtocolConfigEditor 顶层从 `<Flex vertical gap={8}>` 改为 `<Flex vertical gap={0} className="pce-root">`。新增 5 个布局 class（codecEditor.css）：

```css
/* 顶层 flex 列：撑满浮窗 content box（floating-window-body flex:1） */
.pce-root { flex: 1; min-height: 0; height: 100%; }

/* 主内容区：flex:1 撑满工具条以下的剩余空间，min-height:0 让子区可滚动 */
.pce-main {
  flex: 1; min-height: 0; overflow: hidden;
  display: flex; flex-direction: column;
}
```

Monaco 容器从 `height: calc(100vh - 440px)` → `flex: 1; min-height: 240px`（class `.pce-source-editor`）；结构化视图容器从 `maxHeight: calc(100vh - 440px)` → `.pce-struct-cols { flex: 1; overflow: auto }`。

floating-window-body 本身有 `flex:1; overflow:auto; padding:12px`，PCE root 设 `height:100%` 正好填满 content box，浮窗缩放时内容区跟随。

### 2. 工具条分组（Card 分隔三组）

原「Alert + 连接工具条 + 编辑器工具条」紧贴 → 改为视觉分隔的三组：

```jsx
<Flex vertical gap={0} className="pce-root">
  {/* 组1：提示信息（Alert × 1~3 个，className="pce-alert" 统一 margin-bottom）*/}

  {/* 组2：连接管理（Card size="small" className="pce-toolbar-card"）
      Select + 新建/复制/删除 + 从基线载入 + 来源状态 */}
  <Card size="small" className="pce-toolbar-card" styles={{ body: { padding: 'var(--space-sm)' } }}>
    <Flex justify="space-between" align="center" gap={8} wrap="wrap">
      <Space size={6} wrap> ... </Space>
      <span className="pce-source-label">{source ?? '尚未加载'}</span>
    </Flex>
  </Card>

  {/* 组3：编辑操作（Card size="small" className="pce-toolbar-card"）
      左：导入/保存/清空；右：Segmented 视图切换 */}
  <Card size="small" className="pce-toolbar-card" styles={{ body: { padding: 'var(--space-sm)' } }}>
    <Flex justify="space-between" align="center" gap={8} wrap="wrap"> ... </Flex>
  </Card>

  <div className="pce-main"> ... </div>
</Flex>
```

`.pce-toolbar-card { margin-bottom: var(--space-sm); border: 1px solid var(--border-color); }`

### 3. 视图切换归位（Segmented 并入工具条右端）

原 Segmented 独占一行 → 并入「编辑操作」Card 的 Flex 右端（`justify="space-between"`，左操作右切换）：

```jsx
<Card size="small" className="pce-toolbar-card">
  <Flex justify="space-between" align="center" gap={8} wrap="wrap">
    <Space size={4} wrap>
      <Upload>...</Upload>
      <Button>保存</Button>
      <Button danger>清空</Button>
    </Space>
    {!isErrorsView && (
      <Segmented size="small" value={viewMode} onChange={...}
        options={[{ label: '结构化', value: 'struct' }, { label: '源码', value: 'source' }]} />
    )}
  </Flex>
</Card>
```

### 4. 两列布局（结构化视图，纯 CSS flex-wrap，无 JS 测宽）

```jsx
<div className="pce-main">
  {showStructView ? (
    <div className="pce-struct-cols">
      <div className="pce-col-left">
        <FrameLayoutEditor /> <PipelineEditor /> <RouteKeyEditor />
      </div>
      <div className="pce-col-right">
        <PreviewPanel /> {/* 常驻，不再 Collapse 包裹 */}
      </div>
    </div>
  ) : ( ...源码视图... )}
</div>
```

```css
.pce-struct-cols {
  flex: 1; display: flex; flex-wrap: wrap;
  gap: var(--space-md); align-content: flex-start;
  overflow: auto; padding: var(--space-sm);
  border: 1px solid var(--border-color);
}
.pce-col-left  { flex: 1 1 60%; min-width: 360px; }  /* 帧布局+管线+路由键 */
.pce-col-right { flex: 1 1 36%; min-width: 280px; }  /* PreviewPanel 常驻 */
```

浮窗宽 900px（defaultSize）时左右并列；缩到 < 640px（360+280）时 flex-wrap 自动回退单列。

PreviewPanel 从原 Collapse 折叠面板里拆出来常驻右列（任务规格明确「右列放 PreviewPanel（常驻）」），同时移除了已无用的 `Collapse` import。

### 5. source 文本移位

原 `<span>{source}</span>` 与「从基线载入」按钮混在同一个 Space → 移到 Card 右端（Flex space-between），与操作按钮语义分离：

```css
.pce-source-label {
  font-size: 11px;
  color: var(--text-tertiary);   /* 浅 rgba(0,0,0,0.45) / 深 rgba(255,255,255,0.5) */
  white-space: nowrap;
}
```

## 二、配色怎么统一（B2-2）

### 1. CSS 变量命名归一

tokens.css 中**没有** `--fill-color-quaternary` / `--fill-quaternary` / `--primary-color`（都是 antd v4 遗留 fallback）。统一对齐到 tokens 已有变量：

| 原用法 | 文件 | 新用法（tokens 已有变量） |
|---|---|---|
| `var(--fill-color-quaternary, rgba(0,0,0,0.04))` | ByteStrip.tsx (字节条带背景) | `var(--hover-bg)` |
| `var(--fill-quaternary, rgba(0,0,0,0.02))` | PreviewPanel.tsx (HexOutput 背景) | `var(--hover-bg)` |
| `var(--primary-color, #e6f4ff)` | HeaderFieldTable.tsx (选中行) | `var(--hover-bg)` |
| `var(--border-color, rgba(0,0,0,0.06)/(0,0,0,0.08)/(0,0,0,0.1)/#d9d9d9)` | 多处 fallback | `var(--border-color)` 去 fallback |
| `var(--text-secondary, #999)` | PipelineEditor.tsx (算法描述) | `var(--text-secondary)` 去 fallback |
| `var(--text-tertiary, #999)` | ByteStrip.tsx (标尺) | `var(--text-tertiary)` 去 fallback |
| `var(--text-tertiary)` inline style | ProtocolConfigEditor.tsx (source 文本) | 抽到 `.pce-source-label` class |

**tokens.css 证据**（实读确认）：
- `--hover-bg`：浅 `rgba(0, 0, 0, 0.06)` / 深 `rgba(255, 255, 255, 0.08)` — 表头/区域底色/悬停色，语义对齐字节条带底色、HexOutput 底色、选中行底色。
- `--border-color`：浅 `rgba(0, 0, 0, 0.1)` / 深 `rgba(255, 255, 255, 0.1)` — 单一来源。
- `--text-tertiary`：浅 `rgba(0, 0, 0, 0.45)` / 深 `rgba(255, 255, 255, 0.5)`。

### 2. 去硬编码色

| 原硬编码 | 位置 | 新 token（tokens 已有） |
|---|---|---|
| `#1677ff`（ByteStrip 选中 outline） | ByteStrip.tsx | `var(--color-blue)`（浅 #1677ff / 深 #4096ff） |
| `#1677ff`（.flet-input:focus border） | codecEditor.css | `var(--color-blue)` |
| `rgba(5, 145, 255, 0.1)`（.flet-input:focus shadow） | codecEditor.css | `color-mix(in srgb, var(--color-blue) 12%, transparent)` |
| `#ff4d4f`（.flet-row-bad 红条） | codecEditor.css | `var(--color-error)`（浅 #f5222d / 深 #ff7875） |
| `#ff4d4f`（ByteStrip 越界块背景） | ByteStrip.tsx | `var(--color-error)` |
| `rgba(0,0,0,0.06)`（ByteStrip tick） | ByteStrip.tsx | `var(--divider-bg)`（浅 rgba(0,0,0,0.06) / 深 rgba(255,255,255,0.08)） |
| `rgba(0,0,0,0.18)`（ByteStrip trailer 背景） | ByteStrip.tsx | `var(--badge-bg)`（浅 rgba(0,0,0,0.05) / 深 rgba(255,255,255,0.08)） |
| `#fff`（ByteStrip trailer 字色） | ByteStrip.tsx | `var(--text-secondary)`（trailer 在 badge-bg 上，字色用次级文本即可读） |
| `#d9d9d9`（.flet-input border fallback） | codecEditor.css | `var(--border-color)` 去 fallback |
| `#fff`（.flet-input 背景 fallback） | codecEditor.css | `var(--bg-elevated)` |
| `rgba(0,0,0,0.88)`（.flet-input 字色 fallback） | codecEditor.css | `var(--text-primary)` |

**功能性色板保留**（红线例外）：
- `FIELD_COLORS`（byteLayout.ts）：10 色字段区分色板，保留为常量。色板本身在浅/暗主题都可读（中等饱和度的色相轮转，白色字 `#fff` 在所有色板上对比度 > 4.5:1）。
- ByteStrip 字段块上的字色 `#fff`：色板上的白字，tokens 无 `--text-on-color` 变量，保留 `#fff` 合理。

### 3. 视觉重量对齐

所有工具条用 `Card size="small"` + `styles={{ body: { padding: 'var(--space-sm)' } }}`，工具条 Button/Select 已统一 `size="small"`，与子编辑器卡片（FrameScalars/PipelineEditor/RouteKeyEditor/RoleLinkedForm 都用 `Card size="small"`）层级一致。本任务未改子卡片标题字号（规格中的 `styles={{header:{fontSize:13}}}` 建议留到 B2-3+4 表单密度任务统一处理，避免本任务越界）。

## 三、tokens.css 是否补变量

**未补**。所有需求都能对齐到 tokens.css 已有变量（`--hover-bg`/`--border-color`/`--color-blue`/`--color-error`/`--divider-bg`/`--badge-bg`/`--text-primary`/`--text-secondary`/`--text-tertiary`/`--bg-elevated`/`--space-sm`/`--space-md`）。

## 四、验证结果

### tsc
```
$ npx tsc -b
(无输出，exit 0)
```

### Vitest
```
$ npm run test
Test Files  22 passed (22)
     Tests  287 passed (287)
```
287/287 全绿，无回归。

### diff --stat（限 modules/）
```
 .../components/modules/ProtocolConfigEditor.tsx    | 261 +++++++++++----------
 .../components/modules/codecEditor/ByteStrip.tsx   |  16 +-
 .../components/modules/codecEditor/HeaderFieldTable.tsx |   2 +-
 .../components/modules/codecEditor/PipelineEditor.tsx   |   4 +-
 .../components/modules/codecEditor/PreviewPanel.tsx     |   2 +-
 .../components/modules/codecEditor/codecEditor.css      |  92 +++++-
 6 files changed, 227 insertions(+), 150 deletions(-)
```

改动严格限于 `cmd/web/src/components/modules/`（ProtocolConfigEditor.tsx + codecEditor/ 子目录 5 个文件）。conf/services/pages/runtime/FlowEditor/store 零改动。

### 硬编码色与 magic number 自查
```
$ git grep -n "#1677ff\|#ff4d4f\|#e6f4ff\|100vh - 440" cmd/web/src/components/modules/
```
残留全部在注释（"替代 calc(100vh - 440) magic number" / "浅 #1677ff / 深 #4096ff" 色名说明），非实际硬编码。history/ 下的命中是历史报告打印样式（reportPrintCss）与图表色板（reportCharts），与协议配置无关，不在本任务范围。

```
$ git grep -nE "#[0-9a-fA-F]{3,6}\b" <所改 6 文件>
```
仅剩 3 处合规项：
- `ByteStrip.tsx:96: color: '#fff'` — 字段块色板上的白字（色板功能性配色，红线例外）。
- `codecEditor.css:87 / 94` — 注释里的色名说明。

## 五、自审

- 所有颜色走 tokens 变量，无硬编码（功能性色板 FIELD_COLORS + 色板上的 `#fff` 字除外，符合红线）。
- 暗色主题通过 `[data-theme='dark']` 自动跟随（用变量即自动跟随）。
- antd v5 合规：未用废弃 `bordered` prop；Card 用 `size="small"`；用 `styles={{ body: {...} }}` 替代 `bodyStyle`（废弃 API）。
- `>3 属性`的样式（PCE 布局、工具条卡片、源文本标签）抽到 codecEditor.css，用 class + tokens 变量。
- 未动逻辑（state/handler/service/校验），只动样式与布局结构。
- UI 文本「协议配置」未暴露 codec。
- PreviewPanel 从 Collapse 折叠态改为常驻右列（规格明确「右列放 PreviewPanel（常驻）」），移除了 Collapse 的 unused import。

## 六、需要用户验证的设计决策（看不到 UI）

1. **两列宽比 60% / 36%**：基于 PreviewPanel 内容（Segmented + Tag + Input + Table + HexOutput）估算 280px 可读，左列编辑器主体（Card 内 Space + 字段表）360px 可用。实际窄窗回退阈值 = 360+280 = 640px，与浮窗 minSize 600px 接近，可能在 600~640px 区间是单列。请验证浮窗缩到最窄时左右列排布符合预期。
2. **PreviewPanel 常驻右列**（移除 Collapse 折叠）：任务规格明确要求，但意味着 PreviewPanel 总是展开占右列空间。若 PreviewPanel 调用后端 codec 引擎频繁，用户可能更想默认折叠——请确认这是期望行为。
3. **PCE root 撑满 floating-window-body**：floating-window-body 有 `overflow:auto`，PCE root 设 `height:100%` 理论上 body 自身不会滚动（PCE main 内部滚动）。若发现浮窗内出现双重滚动条，可能需要把 floating-window-body 的 overflow 改成 visible 或调整 PCE 高度策略——但这属于 FloatingWindow 通用容器改动，不在本任务范围。
4. **工具条 Card body padding 用 `var(--space-sm)`**（8px）：比 antd Card 默认（16px）紧凑，比 size="small" 默认（12px）略小，为的是让工具条区视觉重量轻于主内容区。若觉得太紧可调到 `--space-md`（12px）。
5. **字节条带 trailer 字色改用 `--text-secondary`**（原 `#fff`）：trailer 背景从 `rgba(0,0,0,0.18)` 改为 `--badge-bg`（浅 rgba(0,0,0,0.05)），`#fff` 字在浅色 badge-bg 上不可读，因此字色降级到次级文本色。trailer 不再像原版那样深色块状，视觉更轻——请确认这是期望效果。
6. **HeaderFieldTable 选中行从 `#e6f4ff`（v4 浅蓝）改为 `--hover-bg`（中性灰 6% 黑）**：选中态视觉重量比原版轻（不再那么"蓝"）。若希望选中行更突出，可考虑加 `box-shadow: inset 3px 0 0 var(--color-blue)`（左条标记），但那是 B2-3+4 表单密度任务的范畴。
