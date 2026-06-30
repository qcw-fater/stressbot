# 协议配置编辑器重新设计 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把协议配置编辑器从「单列堆叠 4 个全宽工作台」重组为「分区标签页 + 帧布局字段表全宽 + 详情行内展开」，消除莫名留白、让重要内容获得充足空间，并把文案改为「标题中文 / 协议术语英文」。

**Architecture:** 顶层用 antd `Tabs` 承载 4 个分区（帧布局 / 管线 / 路由键 / 预览），每个分区独占主舞台。帧布局 tab 内：帧参数顶栏 + 字节尺 hero + 字段表全宽（详情用 antd `Table` 受控 `expandedRowKeys` 行内展开）。路由键 / 预览用左右双栏填满。数据流不变（content 字符串单源 → `parseCodecForEdit` → raw+schema → `codecEdit` helper → `onEdit`）。

**Tech Stack:** React 18 / TypeScript 5.6 / antd 5.21 / Vitest。CSS 走 `tokens.css` 变量，不硬编码颜色（功能性色板除外）。

## Global Constraints

- 浮窗默认 900×640、最小 600×400；布局须在该范围可用，窄窗（< 720px）双栏退化为单列。
- 文案：分区/区块标题全中文；`offset/size/type/endian/role/le/be` 等与 `codec.json` 字段一一对应的协议术语保留英文；路由键占位 `{cmd}:{act}`、字段名本身保留英文。
- **不改后端、不改 `services/` API、不改编辑语义**（`codecEdit.ts` helper、`validateCodecSchema`、`parseCodecForEdit` 的逻辑与签名保持不变）。
- 数据流不变：单一数据源 = `content` 字符串；结构化编辑经 `codecEdit` helper 生成新 content → `onEdit(setContent)` → 重算 `parsed`。
- 前端请求收拢 `services/`（本任务不新增任何网络请求）。
- 颜色走 `tokens.css` 变量，不硬编码 `#xxx`（`byteLayout.ts` 的 `FIELD_COLORS` 功能色板除外）。
- 验收三件套：`cd cmd/web && npx tsc -b`（类型）+ `cd cmd/web && npm run test`（Vitest 不破坏现有）+ `cd cmd/web && npm run dev`（手动视觉核对）。

## 文件结构

| 文件 | 职责 | 本计划改动 |
|---|---|---|
| `cmd/web/src/components/modules/codecEditor/codecEditor.css` | 布局 + 字节尺/表格轻样式 | Task 1：重写布局类、新增双栏/hero/tab 类、字节尺 hero 加高；移除废弃类 |
| `cmd/web/src/components/modules/ProtocolConfigEditor.tsx` | 顶层骨架 | Task 2：命令栏精简 + 校验条 + antd `Tabs`；Task 7：预览从 `Collapse` 移出 |
| `cmd/web/src/components/modules/codecEditor/ByteStrip.tsx` | 字节尺 | Task 3：文案中文 + hero 适配 |
| `cmd/web/src/components/modules/codecEditor/HeaderFieldTable.tsx` | 字段表 | Task 4：去 endian 列、列名「字段名」、高度自适应、行内展开 |
| `cmd/web/src/components/modules/codecEditor/RoleLinkedForm.tsx` | 字段详情 | Task 4：吸收 endian 编辑、作为行内展开内容 |
| `cmd/web/src/components/modules/codecEditor/FrameLayoutEditor.tsx` | 帧布局组装 | Task 4：移除上下堆叠、字段表全宽、字节尺 hero |
| `cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx` | 管线 | Task 5：步骤卡片分区小标题 |
| `cmd/web/src/components/modules/codecEditor/RouteKeyEditor.tsx` | 路由键 | Task 6：双栏（编辑 ‖ 实时样例） |
| `cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx` | 预览 | Task 7：双栏（输入 ‖ 结果） |

className 体系（Task 1 定义，后续任务引用，保持一致）：
`.pce-shell`（根）/ `.pce-cmdbar` + `.pce-cmdbar-target` + `.pce-cmdbar-actions`（命令栏）/ `.pce-status`（校验条）/ `.pce-tabs`（Tabs 包装）/ `.pce-stage`（主舞台）/ `.frame-tab`（帧布局 tab 内）/ `.frame-scalars`（帧参数）/ `.byte-hero`（字节尺 hero）/ `.split-2` + `.split-2-left` + `.split-2-right`（通用左右双栏）。

---

### Task 1: CSS 布局骨架重写

**Files:**
- Modify: `cmd/web/src/components/modules/codecEditor/codecEditor.css`（布局相关类；`.bs-*` 字节尺细节样式保留，仅调 `.bs-strip` 高度）

**Interfaces:**
- Produces: 本任务定义的所有布局 className（见上「className 体系」），供 Task 2–7 的组件引用。

- [ ] **Step 1: 重写 `ProtocolConfigEditor` 布局类**

把 `codecEditor.css` 顶部的 `/* === ProtocolConfigEditor 布局 === */` 区块（从 `.pce-root` 到 `.pce-source-editor`）整体替换为下面内容。**保留**其后 `.bs-*`（字节尺）、`.flet-*`（表格内联输入）、`.frame-*` 表格细节里不冲突的部分，但删除 `.pce-workspace`、`.frame-edit-stack`、`.frame-table-pane`、`.frame-inspector-pane`、`.frame-empty-inspector`、`.field-inspector`、`.field-inspector-header`、`.field-inspector-text`、`.pce-preview-bench` 这些废弃类（Task 4 会移除对应的 JSX）。

```css
/* === ProtocolConfigEditor 布局 === */

.pce-shell {
  flex: 1;
  min-height: 0;
  height: 100%;
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
}

/* 命令栏：左 target（连接选择+来源） | 右 actions（分组按钮+视图切换） */
.pce-cmdbar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-sm);
  align-items: center;
  padding: var(--space-sm);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  background: var(--bg-panel);
}
.pce-cmdbar-target {
  flex: 1 1 320px;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  flex-wrap: wrap;
}
.pce-cmdbar-actions {
  flex: 0 1 auto;
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  flex-wrap: wrap;
}
.pce-cmdbar-group {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}
.pce-cmdbar-divider {
  width: 1px;
  align-self: stretch;
  background: var(--divider-bg);
  margin-inline: 2px;
}
.pce-target-kicker {
  font-family: "JetBrains Mono", "IBM Plex Mono", SFMono-Regular, Consolas, monospace;
  font-size: 10px;
  letter-spacing: 0.08em;
  color: var(--text-tertiary);
}
.pce-target-name { font-size: 12px; }
.pce-source-label { font-size: 11px; color: var(--text-tertiary); white-space: nowrap; }

/* 校验条：常驻紧凑单行 */
.pce-status {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  flex-wrap: wrap;
  padding: 6px 10px;
  border: 1px solid var(--border-color);
  border-left: 3px solid var(--color-blue);
  border-radius: 6px;
  background: var(--bg-elevated);
}
.pce-status-warn { border-left-color: var(--color-error); }
.pce-status-title { font-size: 12px; font-weight: 600; }
.pce-status-note { font-size: 12px; color: var(--text-tertiary); }
.pce-status-details { width: 100%; }
.pce-status-list { margin: 0; padding-left: 18px; max-height: 120px; overflow: auto; font-size: 12px; }

/* 分区 Tabs：紧凑 */
.pce-tabs { flex: 0 0 auto; }
.pce-tabs .ant-tabs-nav { margin-bottom: 0; }

/* 主舞台：随 tab 切换，flex 撑满剩余 */
.pce-stage {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--border-color);
  border-radius: 8px;
  background: var(--bg-panel);
}

/* 源码模式 Monaco */
.pce-source-editor { flex: 1; min-height: 240px; }

/* === 帧布局 tab === */
.frame-tab {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
  padding: var(--space-sm);
}
.frame-scalars {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-sm) var(--space-md);
  align-items: flex-end;
  padding: 10px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-elevated);
}
.frame-scalars .frame-control-item { min-width: 0; }
.frame-scalars .frame-control-label {
  display: block; margin-bottom: 2px; font-size: 11px; color: var(--text-tertiary);
}
.frame-scalars .frame-length-scope { flex: 1 1 260px; }

/* 字节尺 hero：醒目 */
.byte-hero {
  padding: 10px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-elevated);
}
.byte-hero .byte-bench-caption { margin-bottom: 8px; }

/* 字段表全宽容器 */
.frame-table-wrap { min-width: 0; }

/* bench 标题（帧布局/管线/路由键/预览复用） */
.pce-bench {
  min-width: 0;
  border: 1px solid var(--border-color);
  border-radius: 8px;
  background: var(--bg-elevated);
}
.pce-bench-header {
  display: flex; justify-content: space-between; align-items: center;
  gap: var(--space-sm); flex-wrap: wrap;
  padding: 8px 10px; border-bottom: 1px solid var(--border-color);
}
.pce-bench-title {
  font-family: "JetBrains Mono", "IBM Plex Mono", SFMono-Regular, Consolas, monospace;
  font-size: 12px; font-weight: 700; letter-spacing: 0.08em; color: var(--text-primary);
}
.pce-bench-meta { margin-left: 8px; font-size: 12px; color: var(--text-tertiary); font-weight: normal; }

/* === 通用左右双栏（路由键 / 预览） === */
.split-2 {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: var(--space-md);
  min-width: 0;
}
.split-2-left, .split-2-right { min-width: 0; }

/* 行内展开详情：全宽内嵌卡片 */
.field-detail-inline {
  padding: 10px 12px;
  background: var(--bg-panel);
}

/* 选中行高亮（保留现有交互） */
.flet-row-selected > td {
  background: var(--hover-bg) !important;
  box-shadow: inset 3px 0 0 var(--color-blue);
}
.flet-row-bad > td { box-shadow: inset 3px 0 0 var(--color-error); }

/* === 响应式：窄窗双栏退化 === */
@media (max-width: 720px) {
  .split-2 { grid-template-columns: minmax(0, 1fr); }
}

/* === 字节尺 hero 加高 === */
.bs-strip { height: 44px; }  /* 原 30px → 44px，作为帧布局视觉中心 */
```

- [ ] **Step 2: 验证 CSS 无语法错误**

Run: `cd cmd/web && npm run dev`（启动 Vite，观察控制台无 CSS 解析错误，浏览器能加载页面）
Expected: Vite 正常启动，无 `PostCSS`/`CSS` 报错。此时 UI 因组件还没用新 className 会暂时错位——属正常，后续任务修复。验证后可停掉 dev server。

- [ ] **Step 3: 提交**

```bash
git add cmd/web/src/components/modules/codecEditor/codecEditor.css
git commit -m "refactor(codec-ui): 重写协议配置编辑器布局 CSS（分区/双栏/hero 骨架）"
```

---

### Task 2: 顶层骨架重组（命令栏 + 校验条 + Tabs）

**Files:**
- Modify: `cmd/web/src/components/modules/ProtocolConfigEditor.tsx`

**Interfaces:**
- Consumes: `FrameLayoutEditor` / `PipelineEditor` / `RouteKeyEditor` / `PreviewPanel`（props 不变，各组件内部改造在 Task 3–7）
- Produces: 顶层用 antd `Tabs` 把 4 个分区组件挂到对应 tab；源码模式隐藏 Tabs 显示 Monaco。

- [ ] **Step 1: 引入 `Tabs`，替换 import**

在 `ProtocolConfigEditor.tsx` 的 antd import 块加入 `Tabs`：

```tsx
import {
  Alert, App as AntApp, Button, Collapse, Divider, Flex, Input, Modal,
  Segmented, Select, Space, Tabs, Tooltip, Typography, Upload,
} from 'antd';
```
（同时移除不再使用的 `Space`?——保留，命令栏仍用。`Divider` 新增用于按钮分组。）

- [ ] **Step 2: 重写 `return` 的 JSX 骨架**

把现有 `return ( <Flex vertical gap={0} className="pce-root"> ... </Flex> )` 整体替换为下面的结构（命令栏分组用 `.pce-cmdbar-divider`，主区按视图切换；保留 Modal 不动）：

```tsx
return (
  <Flex vertical gap={0} className="pce-shell">
    {/* 命令栏 */}
    <div className="pce-cmdbar">
      <div className="pce-cmdbar-target">
        <Typography.Text className="pce-target-kicker">当前对象</Typography.Text>
        <Select
          size="small"
          style={{ minWidth: 200 }}
          value={activeConn ?? undefined}
          placeholder={files.length === 0 ? '暂无连接' : '选择连接'}
          loading={loading}
          onChange={handleSwitch}
          options={selectOptions}
        />
        <Typography.Text code className="pce-target-name">{activeLabel}</Typography.Text>
        <span className="pce-source-label">{source ?? '尚未加载'}</span>
      </div>

      <div className="pce-cmdbar-actions">
        <div className="pce-cmdbar-group">
          <Tooltip title="新建连接（输入 <协议>:<服务名>，如 tcp:logic）">
            <Button size="small" icon={<PlusOutlined />} onClick={() => openCreate('new')}>新建</Button>
          </Tooltip>
          <Tooltip title="复制当前连接为新连接">
            <Button size="small" icon={<CopyOutlined />} disabled={activeConn === null || activeConn === '__errors__'} onClick={() => openCreate('copy')}>复制</Button>
          </Tooltip>
          <Tooltip title="删除当前连接">
            <Button size="small" danger icon={<DeleteOutlined />} disabled={activeConn === null || activeConn === '__errors__'} onClick={() => activeConn && handleDelete(activeConn)}>删除</Button>
          </Tooltip>
        </div>
        <span className="pce-cmdbar-divider" />
        <div className="pce-cmdbar-group">
          <Tooltip title="从服务器拉取全部协议配置到本地">
            <Button size="small" icon={<CloudDownloadOutlined />} loading={pullingBaseline} onClick={onPullBaseline}>从基线载入</Button>
          </Tooltip>
          <Upload accept=".json,application/json" beforeUpload={onUpload} showUploadList={false}>
            <Button icon={<InboxOutlined />} size="small" disabled={activeConn === null}>导入</Button>
          </Upload>
        </div>
        <span className="pce-cmdbar-divider" />
        <div className="pce-cmdbar-group">
          <Button onClick={onSave} type="primary" size="small" disabled={activeConn === null}>保存</Button>
          <Button onClick={onClear} danger size="small" disabled={activeConn === null}>清空</Button>
        </div>
        {!isErrorsView && (
          <Segmented
            size="small"
            value={viewMode}
            onChange={(v) => setViewMode(v as 'struct' | 'source')}
            options={[{ label: '结构化', value: 'struct' }, { label: '源码', value: 'source' }]}
          />
        )}
      </div>
    </div>

    {/* 校验条 */}
    <div className={`pce-status${liveErrors.length > 0 || loadError ? ' pce-status-warn' : ''}`}>
      <Typography.Text className="pce-status-title">
        {loadError ? '配置文件不存在' : validationSummary}
      </Typography.Text>
      <Typography.Text className="pce-status-note">
        {loadError ? '请新建连接或从基线载入。' : '协议配置启动任务时随连接配置一起下发。'}
      </Typography.Text>
      {activeConn !== null && activeConn !== '__errors__' && liveErrors.length > 1 && (
        <Collapse ghost size="small" className="pce-status-details" items={[{
          key: 'errors', label: `查看全部 ${liveErrors.length} 处问题`,
          children: <ul className="pce-status-list">{liveErrors.map((e, i) => <li key={i}>{e}</li>)}</ul>,
        }]} />
      )}
    </div>

    {/* 主舞台 */}
    <div className="pce-stage">
      {showStructView && parsed.raw && parsed.schema ? (
        <Tabs
          size="small"
          className="pce-tabs"
          defaultActiveKey="frame"
          items={[
            { key: 'frame', label: '帧布局', children: <FrameLayoutEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} /> },
            { key: 'pipeline', label: '管线', children: <PipelineEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} /> },
            { key: 'route', label: '路由键', children: <RouteKeyEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} /> },
            { key: 'preview', label: '预览', children: <PreviewPanel raw={parsed.raw} schema={parsed.schema} transport={deriveTransport(activeConn)} /> },
          ]}
        />
      ) : (
        <>
          {!isErrorsView && viewMode === 'struct' && parsed.error && (
            <Alert type="warning" showIcon message="源码不是合法 JSON，请切到源码视图修正" description={parsed.error} style={{ margin: 8 }} />
          )}
          <div className="pce-source-editor">
            <Editor
              language="json" theme={monacoTheme} value={content}
              onChange={(v) => setContent(v ?? '')}
              options={{ fontSize: 12, minimap: { enabled: false }, scrollBeyondLastLine: false, fixedOverflowWidgets: true, automaticLayout: true }}
            />
          </div>
        </>
      )}
    </div>

    {/* 新建/复制 Modal（保持不变） */}
    <Modal /* ...原 Modal 内容不动... */ />
  </Flex>
);
```
注意：保留原 `<Modal>...</Modal>` 整段不变（新建/复制连接）。移除原来的 `pce-command-rail` / `pce-validation-strip` / `pce-main` / `pce-workspace` / `pce-preview-bench` 的 JSX（PreviewPanel 不再被 `Collapse` 包裹，直接作为 `preview` tab 的 children——见上）。

- [ ] **Step 3: 类型检查**

Run: `cd cmd/web && npx tsc -b`
Expected: 无错误。（`Divider` 已 import；若 tsc 报未使用 import，移除之。）

- [ ] **Step 4: 视觉核对**

Run: `cd cmd/web && npm run dev`，打开协议配置浮窗。
Expected: 命令栏单行（窄时换行）、按钮三组用竖线隔开；4 个 tab 可切换（内容暂未优化，但能显示）；源码模式隐藏 tab 显示 Monaco；`errors.json` 强制源码、无 tab。

- [ ] **Step 5: 提交**

```bash
git add cmd/web/src/components/modules/ProtocolConfigEditor.tsx
git commit -m "refactor(codec-ui): 顶层骨架改为分区 Tabs + 精简命令栏"
```

---

### Task 3: 字节尺 hero + 文案中文化

**Files:**
- Modify: `cmd/web/src/components/modules/codecEditor/ByteStrip.tsx`

**Interfaces:**
- Props 不变：`{ schema, selectedIndex, onSelect }`。

- [ ] **Step 1: 文案中文化（caption）**

把 `ByteStrip.tsx` 里 `byte-bench-caption` 的英文 meta 改为中文。定位：

```tsx
<Typography.Text type="secondary" className="pce-bench-meta">
  click span to select · red hatch = overlap / out of bounds
</Typography.Text>
```
替换为：

```tsx
<Typography.Text type="secondary" className="pce-bench-meta">
  点击色块选中字段 · 红色斜纹 = 越界或重叠
</Typography.Text>
```
并把 caption 的 `BYTE RULER` 标题改为中文：

```tsx
<Typography.Text className="pce-bench-title">字节尺</Typography.Text>
```

- [ ] **Step 2: hero 适配（strip 已由 Task 1 的 `.bs-strip { height: 44px }` 加高）**

无需改 ByteStrip.tsx 的尺寸逻辑——`.bs-field` 用 `top:4px; bottom:4px` 百分比式定位（实际是绝对定位上下边距），随 `.bs-strip` 加高自动变高。仅确认 `Tooltip` 文案里的 `offset`/`type` 等术语保留英文（符合文案规范）。定位这段：

```tsx
title={<span>{r.field.name || '(未命名)'} · offset {r.start}–{r.end} · {r.field.type}{r.bad ? ' · 越界或重叠' : ''}</span>}
```
确认无需改（术语英文 + 中文提示混排，符合规范）。

- [ ] **Step 3: 类型检查 + 视觉核对**

Run: `cd cmd/web && npx tsc -b`
Expected: 无错误。

Run: `cd cmd/web && npm run dev`，帧布局 tab。
Expected: 字节尺标题「字节尺」、标注中文；条带比之前更高更醒目。

- [ ] **Step 4: 提交**

```bash
git add cmd/web/src/components/modules/codecEditor/ByteStrip.tsx
git commit -m "refactor(codec-ui): 字节尺文案中文化 + hero 加高"
```

---

### Task 4: 字段表全宽 + 详情行内展开（含 endian 移入详情）

本任务改 3 个文件，是帧布局 tab 的核心。

**Files:**
- Modify: `cmd/web/src/components/modules/codecEditor/HeaderFieldTable.tsx`
- Modify: `cmd/web/src/components/modules/codecEditor/RoleLinkedForm.tsx`
- Modify: `cmd/web/src/components/modules/codecEditor/FrameLayoutEditor.tsx`

**Interfaces:**
- `HeaderFieldTable`：新增 `expandable` 行内展开渲染 `RoleLinkedForm`；用受控 `expandedRowKeys` 跟随 `selectedIndex`。
- `RoleLinkedForm`：顶部新增通用的「字节序」编辑（所有 role 显示），吸收原表格 endian 列。
- `FrameLayoutEditor`：移除 `frame-edit-stack` 上下堆叠；字段表全宽；移除独立的空 inspector 占位。

- [ ] **Step 1: `RoleLinkedForm` 吸收 endian 编辑**

在 `RoleLinkedForm.tsx` 的 `<div className="field-inspector">` 改为 `<div className="field-detail-inline">`，并在 `field-inspector-header` 区块之后、role 特定内容之前，插入通用的「字节序」编辑行。把：

```tsx
return (
  <div className="field-inspector">
    <div className="field-inspector-header">
      <div>
        <Typography.Text className="pce-bench-title">FIELD PROBE</Typography.Text>
        <Typography.Text className="pce-bench-meta">{fieldName}</Typography.Text>
      </div>
      <Tag>{field.role}</Tag>
    </div>

    {field.role === 'route' && ( ... )}
```
替换为：

```tsx
const endianOptions = [
  { value: '__default__', label: `默认（${schema.endianDefault ?? 'le'}）` },
  { value: 'le', label: 'le' },
  { value: 'be', label: 'be' },
];

return (
  <div className="field-detail-inline">
    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
      <div>
        <Typography.Text className="pce-bench-title">字段详情</Typography.Text>
        <Typography.Text className="pce-bench-meta">{fieldName}</Typography.Text>
      </div>
      <Tag>{field.role}</Tag>
    </div>

    {/* 字节序（原表格 endian 列，所有 role 通用） */}
    <div className="frame-control-item" style={{ marginBottom: 8 }}>
      <Typography.Text type="secondary" className="frame-control-label">字节序（endian）</Typography.Text>
      <Select
        size="small"
        style={{ width: 160 }}
        value={field.endian ?? '__default__'}
        options={endianOptions}
        onChange={(v) => patch({ endian: v === '__default__' ? undefined : (v as string) })}
      />
    </div>

    {field.role === 'route' && ( ... )}  {/* 其余 role 分支保持不变 */}
```
（`schema` 已在 props 中；`patch`、`Select` 已 import。其余 `flags`/`checksumOut`/`value`/`errorCode`/`length`/`reserved` 分支不动。）

- [ ] **Step 2: `HeaderFieldTable` 去 endian 列 + 列名「字段名」+ 高度自适应 + 行内展开**

在 `HeaderFieldTable.tsx`：

(a) 删除 `columns` 数组里的 `endian` 列定义（整个 `{ title: 'endian', dataIndex: 'endian', ... }` 对象）。

(b) `name` 列标题改中文：

```tsx
{
  title: '字段名',
  dataIndex: 'name',
  width: 140,
  render: (_, record, idx) => ( ... 不变 ... ),
},
```
其余列标题 `offset/size/type/role` 保留英文。

(c) 给 `Table` 加 `expandable`（受控，跟随 `selectedIndex`），并把 `scroll` 的 `y: 280` 去掉（高度由容器 flex 撑满）。定位 `<Table<Field> ...>`：

```tsx
<Table<Field>
  rowKey={(_record, idx) => String(idx)}
  size="small"
  dataSource={fields}
  columns={columns}
  pagination={false}
  scroll={{ x: 'max-content' }}
  expandable={{
    showExpandColumn: false,
    expandedRowKeys: selectedIndex !== null ? [String(selectedIndex)] : [],
    expandedRowRender: (_record, idx) => {
      const i = typeof idx === 'number' ? idx : 0;
      const f = fields[i];
      if (!f) return null;
      return <RoleLinkedForm raw={raw} schema={schema} fieldIndex={i} field={f} onEdit={onEdit} />;
    },
  }}
  onRow={(_record, idx) => ({
    onClick: () => {
      const i = typeof idx === 'number' ? idx : null;
      // toggle：再次点击已选中行 → 取消选中并收起
      onSelect(i !== null && selectedIndex === i ? null : i);
    },
    style: { cursor: 'pointer' },
  })}
  rowClassName={(_record, idx) => {
    const parts: string[] = [];
    if (ranges[idx]?.bad) parts.push('flet-row-bad');
    if (typeof idx === 'number' && selectedIndex === idx) parts.push('flet-row-selected');
    return parts.join(' ');
  }}
/>
```
注意：`expandedRowKeys` 用 `String(idx)` 与 `rowKey` 一致（rowKey 返回 `String(idx)`）。

(d) import `RoleLinkedForm`：

```tsx
import { RoleLinkedForm } from './RoleLinkedForm';
```

(e) `HeaderFieldTable` 的 `onSelect` 类型已是 `(index: number | null) => void`，支持 null（toggle 收起）。确认 props 签名含 `selectedIndex: number | null`（已是）。

- [ ] **Step 3: `FrameLayoutEditor` 字段表全宽、移除上下堆叠**

把 `FrameLayoutEditor.tsx` 的 `<section>` 内部重组：去掉 `frame-edit-stack` / `frame-table-pane` / `frame-inspector-pane` / 空 inspector 占位，字段表全宽。替换 `<section className="pce-bench frame-bench"> ... </section>` 内部为：

```tsx
return (
  <section className="frame-tab">
    {/* 帧参数 */}
    <FrameScalars raw={raw} schema={schema} onEdit={onEdit} />

    {/* 字节尺 hero */}
    <div className="byte-hero">
      <ByteStrip schema={schema} selectedIndex={selectedIndex} onSelect={setSelectedIndex} />
    </div>

    {/* 字段表（全宽，详情行内展开） */}
    <div className="frame-table-wrap">
      <HeaderFieldTable
        raw={raw}
        schema={schema}
        selectedIndex={selectedIndex}
        onSelect={setSelectedIndex}
        onEdit={onEdit}
      />
    </div>
  </section>
);
```
移除原 `pce-bench-header`（FRAME 标题块）——帧布局 tab 不再需要外层 FRAME 标题（tab 标签已是「帧布局」）。移除 `selectedField` Tag（选中信息已在行内展开详情里显示）。`selectedIndex` state、`selectedField`/`fields` 计算保留（`selectedField` 若不再用可删，但 `HeaderFieldTable` 内部已处理选中，`FrameLayoutEditor` 的 `selectedField` 仅原 Tag 用，删 Tag 后可移除 `selectedField` 变量）。

- [ ] **Step 4: 类型检查**

Run: `cd cmd/web && npx tsc -b`
Expected: 无错误。若 `FrameLayoutEditor` 的 `selectedField`/`Tag` import 未使用，移除。

- [ ] **Step 5: 单元测试不破坏**

Run: `cd cmd/web && npm run test`
Expected: 全部通过（`codecEdit.test.ts` 等纯逻辑测试不受影响——本任务只动组件布局与 endian 的 UI 位置，`updateHeaderField` 逻辑未改）。

- [ ] **Step 6: 视觉核对**

Run: `cd cmd/web && npm run dev`，帧布局 tab。
Expected:
- 字段表 6 列（字段名/offset/size/type/role/操作）全宽舒展，无横向滚动。
- 点击某行 → 该行下方展开详情（含「字节序」下拉 + role 特定内容）；再点同一行 → 收起。
- 未选中任何字段时，表格独占全高。
- endian 修改在行内详情生效（改 le/be/默认，切源码可见 `endian` 字段相应变化）。

- [ ] **Step 7: 提交**

```bash
git add cmd/web/src/components/modules/codecEditor/HeaderFieldTable.tsx cmd/web/src/components/modules/codecEditor/RoleLinkedForm.tsx cmd/web/src/components/modules/codecEditor/FrameLayoutEditor.tsx
git commit -m "refactor(codec-ui): 字段表全宽 + 详情行内展开（endian 移入详情）"
```

---

### Task 5: 管线步骤卡片分区小标题

**Files:**
- Modify: `cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx`

**Interfaces:**
- Props 不变：`{ raw, schema, onEdit }`。

- [ ] **Step 1: 卡片标题改为中文 + 「encode 顺序」说明已是中文（确认）**

定位 `PipelineEditor` 的 `Card title`，`PIPELINE` 标题改中文：

```tsx
<span className="pce-bench-title">管线</span>
```
meta 文案 `{steps.length} steps · encode 顺序，decode 自动反序` 保留（术语英文 + 中文说明）。

- [ ] **Step 2: 步骤卡片内加分区小标题**

在 `PipelineStepCard` 的 `Space direction="vertical"` 内，把现有的三条注释分组（`组1 基本属性` / `组2 输入处理` / `组3 输出与条件`）从隐式 Divider 改为带中文小标题的分区。把：

```tsx
{/* ── 组2：输入处理（encrypt 偏移 / checksum·hash over / 算法 params） ── */}
{(isEncrypt || isStandaloneDigest || selectedAlgo) && (
  <Divider style={{ margin: '8px 0' }} dashed />
)}
```
替换为：

```tsx
{(isEncrypt || isStandaloneDigest || selectedAlgo) && (
  <Typography.Text type="secondary" style={{ fontSize: 11, letterSpacing: '0.04em', marginTop: 4 }}>
    输入处理
  </Typography.Text>
)}
```
把：

```tsx
{/* ── 组3：输出与条件（produces / when） ── */}
<Divider style={{ margin: '8px 0' }} dashed />
```
替换为：

```tsx
<Typography.Text type="secondary" style={{ fontSize: 11, letterSpacing: '0.04em', marginTop: 4 }}>
  输出与条件
</Typography.Text>
```
（`基本属性` 组在卡片顶部，无需额外标题——`name/op/算法/...` 那一行即是。`Divider` import 若不再使用可移除；但 `ParamsDynamic` 等子组件不用动。）

- [ ] **Step 3: 类型检查 + 测试 + 视觉**

Run: `cd cmd/web && npx tsc -b` → 无错误。
Run: `cd cmd/web && npm run test` → 全过。
Run: `cd cmd/web && npm run dev`，管线 tab → 步骤卡片内「输入处理」「输出与条件」小标题清晰分隔。

- [ ] **Step 4: 提交**

```bash
git add cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx
git commit -m "refactor(codec-ui): 管线步骤卡片分区小标题（中文）"
```

---

### Task 6: 路由键双栏（编辑 ‖ 实时样例）

**Files:**
- Modify: `cmd/web/src/components/modules/codecEditor/RouteKeyEditor.tsx`

**Interfaces:**
- Props 不变：`{ raw, schema, onEdit }`。

- [ ] **Step 1: 标题中文化 + 双栏布局**

把 `RouteKeyEditor.tsx` 的 `Card` title `ROUTE KEY` 改中文：

```tsx
<span className="pce-bench-title">路由键</span>
```

- [ ] **Step 2: 把 `Space direction="vertical"` 内容重组为左右双栏**

把 `<Card>...</Card>` 的 children（原纵向堆叠的：说明 / Input / route 字段清单 / 未知占位 Alert / 样例）改为左编辑 + 右样例的双栏。替换 `<Space direction="vertical" size={8} style={{ width: '100%' }}> ... </Space>` 为：

```tsx
<div className="split-2">
  {/* 左：模板编辑 + 校验 */}
  <div className="split-2-left">
    <Space direction="vertical" size={8} style={{ width: '100%' }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        模板中的 {`{name}`} 占位需对应 role:&quot;route&quot; 字段，如 {`{cmd}:{act}`}
      </Typography.Text>
      <Input
        size="small"
        value={template}
        placeholder="{cmd}:{act}"
        onChange={(e) => onEdit(setRouteKeyTemplate(raw, e.target.value))}
      />
      <div>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>可用 route 字段：</Typography.Text>
        {routeFields.length === 0 ? (
          <Typography.Text type="secondary" style={{ fontSize: 12, marginLeft: 6 }}>
            （当前 header 无 role:&quot;route&quot; 字段）
          </Typography.Text>
        ) : (
          <Space size={4} wrap style={{ marginTop: 4 }}>
            {routeFields.map((n) => <Tag key={n} style={{ fontSize: 12 }}>{`{${n}}`}</Tag>)}
          </Space>
        )}
      </div>
      {unknown.length > 0 && (
        <Alert type="error" showIcon style={{ padding: '6px 12px' }}
          message={<span style={{ fontSize: 12 }}>未知占位：{unknown.map((u) => `{${u}}`).join(' ')}（必须指向某个 route 字段）</span>} />
      )}
    </Space>
  </div>

  {/* 右：实时样例 */}
  <div className="split-2-right">
    <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
      样例 routeKey
    </Typography.Text>
    <div style={{
      fontFamily: 'monospace', fontSize: 16, fontWeight: 600,
      padding: '10px 12px', background: 'var(--hover-bg)', borderRadius: 6,
      wordBreak: 'break-all', minHeight: 44, display: 'flex', alignItems: 'center',
    }}>
      {sample || <Typography.Text type="secondary" style={{ fontSize: 12 }}>（空）</Typography.Text>}
    </div>
  </div>
</div>
```
（`sample`、`routeFields`、`unknown` 变量已存在，无需改 helper。）

- [ ] **Step 3: 类型检查 + 测试 + 视觉**

Run: `cd cmd/web && npx tsc -b` → 无错误。
Run: `cd cmd/web && npm run test` → 全过。
Run: `cd cmd/web && npm run dev`，路由键 tab → 左编辑右样例双栏；输入 `{cmd}:{act}` 右侧实时显示样例。

- [ ] **Step 4: 提交**

```bash
git add cmd/web/src/components/modules/codecEditor/RouteKeyEditor.tsx
git commit -m "refactor(codec-ui): 路由键改为双栏（模板编辑 ‖ 实时样例）"
```

---

### Task 7: 预览双栏（输入 ‖ 结果）+ 提升为 tab 内容

**Files:**
- Modify: `cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx`
- 注：`ProtocolConfigEditor.tsx` 在 Task 2 已把 `PreviewPanel` 作为 `preview` tab 的 children（不再 `Collapse` 包裹）。

**Interfaces:**
- Props 不变：`{ raw, schema, transport }`。

- [ ] **Step 1: 标题中文化**

`PreviewPanel.tsx` 的 `Card title="预览"` 已是中文，保留。`Segmented` 选项「编码/解码」已是中文，保留。

- [ ] **Step 2: 输入区与结果区改为左右双栏**

把 `PreviewPanel` 的 `<Space direction="vertical" size={10} style={{ width: '100%' }}> ... </Space>`（模式切换 + 输入 + 按钮 + 结果）重组为：顶部模式切换行，下方左右双栏（左输入+按钮，右结果）。在模式切换 `Space` 之后，把「输入区 + 触发按钮」包进左栏，「结果区」包进右栏。结构：

```tsx
<Space size={8} wrap align="center">
  {/* 模式切换 + TCP tag + 说明（保持不变） */}
</Space>

<div className="split-2">
  {/* 左：输入 + 触发按钮 */}
  <div className="split-2-left">
    <Space direction="vertical" size={10} style={{ width: '100%' }}>
      {mode === 'encode' ? (
        <>
          <div>{/* 路由字段输入（保持不变） */}</div>
          <div>{/* body hex（保持不变） */}</div>
        </>
      ) : (
        <div>{/* 帧 hex（保持不变） */}</div>
      )}
      <div>{/* key hex（保持不变） */}</div>
      <Button type="primary" size="small" loading={loading} onClick={runPreview}>预览</Button>
    </Space>
  </div>

  {/* 右：结果 */}
  <div className="split-2-right">
    {empty && <Empty description={<span style={{ fontSize: 12 }}>点「预览」查看编解码结果</span>} image={Empty.PRESENTED_IMAGE_SIMPLE} />}
    {reqError && <Alert type="error" showIcon message={reqError} />}
    {result && result.error && <Alert type="error" showIcon message={result.error} />}
    {result && !result.error && !reqError && (
      <Space direction="vertical" size={8} style={{ width: '100%' }}>
        {/* 原 encode/decode 结果渲染（HexOutput + FieldsTable + routeKey 等）保持不变 */}
      </Space>
    )}
    {!hasError && !empty && (
      <Typography.Text type="secondary" style={{ fontSize: 11 }}>结果仅用于核对，不会保存或影响任务下发。</Typography.Text>
    )}
  </div>
</div>
```
（各输入/结果子块的内容代码原样搬进对应栏，不改正文逻辑。）

- [ ] **Step 3: 类型检查 + 测试 + 视觉**

Run: `cd cmd/web && npx tsc -b` → 无错误。
Run: `cd cmd/web && npm run test` → 全过。
Run: `cd cmd/web && npm run dev`，预览 tab → 左输入右结果双栏；点「预览」右侧显示结果。

- [ ] **Step 4: 提交**

```bash
git add cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx
git commit -m "refactor(codec-ui): 预览改为双栏（输入 ‖ 结果），提升为独立 tab"
```

---

### Task 8: 响应式 + 文案 + 全局核对

**Files:**
- 全部 codecEditor 文件（核对，不改逻辑）。

- [ ] **Step 1: 窄窗响应式核对**

Run: `cd cmd/web && npm run dev`，把协议配置浮窗拖到最小（600×400）。
Expected: 路由键 / 预览的 `.split-2` 双栏退化为上下单列（Task 1 的 `@media (max-width: 720px)` 生效）；字段表全宽无横向溢出（极窄时允许 `scroll x`）；命令栏按钮换行；字节尺横向滚动。

- [ ] **Step 2: 文案全核对**

逐项核对（参考 spec §9）：
- 分区标签：帧布局 / 管线 / 路由键 / 预览 ✓
- 帧布局内：帧参数 / 字节尺 / 字段表 / 字段详情 ✓
- 字节尺标注中文 ✓（Task 3）
- 字段表列：字段名（中）/ offset / size / type / role（英）✓（Task 4）
- 协议术语 offset/size/type/endian/role/le/be 保留英文 ✓
- 校验条「校验通过 · 启动时随连接下发」✓（Task 2）

- [ ] **Step 3: 全量类型 + 测试**

Run: `cd cmd/web && npx tsc -b`
Expected: 无错误。

Run: `cd cmd/web && npm run test`
Expected: 全部通过（`codecEdit.test.ts` / `algosForStepOp.test.ts` / `previewHelpers.test.ts` 等）。

- [ ] **Step 4: 数据流闭环验证**

`npm run dev`，选一个连接 → 帧布局改字段 offset/size/type/role/endian → 管线加步骤 → 路由键改模板 → 切源码视图确认 JSON 正确 → 保存 → 重开浮窗确认内容持久。
Expected: 所有结构化编辑经 `codecEdit` 回灌 content，源码视图 JSON 正确，保存后重开内容一致（数据流无损）。

- [ ] **Step 5: 提交收尾**

```bash
git add -u cmd/web/src/components/modules/ProtocolConfigEditor.tsx cmd/web/src/components/modules/codecEditor/
git commit -m "refactor(codec-ui): 协议配置编辑器排版重设计收尾（响应式 + 文案核对）"
```

---

## Self-Review

**1. Spec coverage（逐节对照 spec）：**
- §3.1 分区标签页 → Task 2 ✓
- §3.3 字段表全宽 + 详情行内展开 → Task 4 ✓
- §4 信息架构 4 tab → Task 2 ✓；校验徽标（增强项）→ spec §7 已标注降级为全局校验条（Task 2 的 `.pce-status`），本计划不实现按分区徽标（YAGNI，依赖 `validateCodecSchema` 改动，spec 已授权降级）✓
- §5 全局骨架（命令栏精简 + 校验条 + 源码模式）→ Task 2 ✓
- §6.1 帧布局（帧参数 + 字节尺 hero + 字段表全宽行内展开 + endian 移入）→ Task 3 + 4 ✓
- §6.2 管线卡片分组 → Task 5 ✓
- §6.3 路由键双栏 → Task 6 ✓
- §6.4 预览双栏 + 提升 → Task 7 ✓
- §8 响应式 → Task 1（media query）+ Task 8（核对）✓
- §9 文案 → Task 2/3/4/5/6/7 + Task 8 核对 ✓
- §10 实现拆分 → 与各 Task 文件一一对应 ✓
- §11 数据流不变 → 各 Task 均不改 `codecEdit`/`validateCodecSchema`/`parseCodecForEdit` ✓
- §12 不改动范围 → 各 Task 未触及后端/services/编辑语义 ✓
- §13 验证 → Task 8 ✓

**2. Placeholder scan：** 无 TBD/TODO；每个代码 step 都给了具体代码片段或精确改动指令。

**3. Type consistency：**
- className 统一：`pce-shell`/`pce-cmdbar*`/`pce-status`/`pce-tabs`/`pce-stage`/`frame-tab`/`frame-scalars`/`byte-hero`/`frame-table-wrap`/`split-2*`/`field-detail-inline`/`pce-bench*`（Task 1 定义，Task 2–7 引用，一致）。
- `HeaderFieldTable.expandable.expandedRowKeys` 用 `String(selectedIndex)` 与 `rowKey: String(idx)` 一致。
- `RoleLinkedForm` 吸收 endian 用 `patch({ endian })` → `updateHeaderField(raw, fieldIndex, { endian })`，与原表格 endian 列的写法一致（`v === '__default__' ? undefined : v`）。
- `FrameLayoutEditor.onSelect` 透传给 `HeaderFieldTable.onSelect`，类型 `(index: number | null) => void` 支持 toggle。
