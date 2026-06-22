# B1-1 抽离 AdapterTab 为独立 ProtocolConfigEditor 组件

## 状态

DONE — tsc 0 报错 / 287 测试全绿 / 改动仅限 2 个目标文件。

## 实现要点

1. **新建** `cmd/web/src/components/modules/ProtocolConfigEditor.tsx`（629 行）：把原 `ResourcesDrawer.tsx` 的 `AdapterTab` 函数体（原 236-738）+ 私有 helper（原 180-234：`CODEC_FILE_SUFFIX`/`ERRORS_JSON_KEY`/`CODEC_PROTOS`/`CODEC_JSON_TEMPLATE`/`EMPTY_ERROR_MAP_TEMPLATE`/`connNameToFileName`/`fileNameToConnName`/`validateConnName`）整体平移，组件重命名为 `export function ProtocolConfigEditor()`（非 default，无 props）。所有 state、handler、JSX、import 原样保留——纯功能平移，无样式/逻辑重构。
2. **zIndex 合规（B1-3 准备）**：新建/复制连接 Modal 照搬 ResourcesDrawer 现有 pattern（实读见下），`const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;` + `styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}`。为后续 B1-2 挂浮窗做准备（浮窗基线 1000+，内嵌 Modal 需更高）。
3. **瘦身 ResourcesDrawer**：删 AdapterTab 函数 + 私有 helper；Tabs items 删 adapter 项（只留 proto/lua）；清理仅 AdapterTab 使用的 import（逐个 grep 确认无其它消费者）。

## 迁了什么

`ProtocolConfigEditor.tsx` 包含：
- 模块 doc 注释（说明抽离来源 + zIndex 合规设计）
- 完整 import 块（@ant-design icons / antd / react / @monaco-editor/react / editorStore / **floatingWindowStore** / resourcesStore 13 个 codec+errors 函数 + ResourceFile / baselineApi 2 个 / codecEditor 6 个）
- 8 个私有常量/类型（CODEC_FILE_SUFFIX / ERRORS_JSON_KEY / CODEC_PROTOS / CODEC_JSON_TEMPLATE / EMPTY_ERROR_MAP_TEMPLATE）
- 3 个私有 helper（connNameToFileName / fileNameToConnName / validateConnName）
- `export function ProtocolConfigEditor()`：全部 state（files/loading/activeConn/content/source/loadError/createOpen/createMode/createValue/viewMode/pullingBaseline）+ 全部 handler（reloadFiles/loadConn/handleSwitch/refreshBadge/openCreate/submitCreate/handleDelete/onSave/onUpload/onClear/onPullBaseline）+ 全部 JSX（Alert/连接选择器/工具栏/实时校验/视图切换/结构化视图/源码视图/新建复制 Modal）

`ResourcesDrawer.tsx` 剩余：抽屉外壳（title/拉取按钮/冲突 Alert/BaselineSyncModal）+ Tabs（proto/lua 两项）+ ResourceTable（proto/lua 表格 + 编辑 Modal）+ formatBytes helper。

## 删了哪些 import（逐个 grep 证据）

对每个被删 import，grep 全文件确认剩余代码（抽屉外壳 + ResourceTable + 冲突 Alert）无其它消费者：

### icons（@ant-design/icons）
- `CopyOutlined` — grep 仅命中原 AdapterTab :583（复制按钮），已随 AdapterTab 删除；ResourceTable 用的是 EditOutlined/DeleteOutlined/InboxOutlined。确认无其它消费者。
- `PlusOutlined` — grep 仅命中原 AdapterTab :578（新建按钮）；ResourceTable 不用 PlusOutlined。确认无其它消费者。
- 保留：`DeleteOutlined`（ResourceTable :1014 删除按钮）、`InboxOutlined`（ResourceTable :1042 上传按钮）、`EditOutlined`（ResourceTable :1002 编辑按钮）、`CloudDownloadOutlined`（外壳 :119 handlePull 按钮）。

### antd
- `Collapse` — grep 仅命中原 AdapterTab :669（预览折叠面板）；ResourceTable 不用 Collapse。确认无其它消费者。
- `Input` — grep 仅命中原 AdapterTab :728（新建连接输入框）；ResourceTable 用原生 `<input type="file">`。确认无其它消费者。
- `Segmented` — grep 仅命中原 AdapterTab :641（视图切换）。确认无其它消费者。
- `Select` — grep 仅命中原 AdapterTab :568（连接选择器）。确认无其它消费者。
- `Upload` — grep 仅命中原 AdapterTab :614（导入 .json）；ResourceTable 用原生 `<input type="file">`。确认无其它消费者。
- 保留：`Alert`（外壳 :125 冲突提示）、`App as AntApp`（外壳 :78 + ResourceTable :762）、`Button`（多处）、`Drawer`（外壳 :113）、`Empty`（ResourceTable :1058）、`Flex`（多处）、`Modal`（ResourceTable :1077 编辑 Modal）、`Space`（多处）、`Table`（ResourceTable :1066）、`Tabs`（外壳 :140）、`Tooltip`（多处）、`Typography`（多处）。

### antd type
- `type { UploadProps }` — grep 仅命中原 AdapterTab :433（onUpload 类型注解）；ResourceTable 用 `FileList` 不用 UploadProps。确认无其它消费者。

### react
- `useMemo` — grep 仅命中原 AdapterTab :257（parseCodecForEdit 缓存）；ResourceTable 用 useEffect/useRef/useState，不用 useMemo。确认无其它消费者。
- 保留：`useEffect`（外壳 + ResourceTable）、`useRef`（ResourceTable :789 fileInputRef）、`useState`（多处）。

### resourcesStore（@/services/resourcesStore）
- `getCodecSchema` — grep 仅命中原 AdapterTab（:292 :350 :292 loadConn/submitCreate）；ResourceTable 用 listProto/listScript/addProtos/addScripts 等，不用 getCodecSchema。确认无其它消费者。
- `setCodecSchema` — grep 仅命中原 AdapterTab（:361 :426 :458 submitCreate/onSave/onUpload）。确认无其它消费者。
- `setCodecSchemaFromBaseline` — grep 仅命中原 AdapterTab :519（onPullBaseline）。确认无其它消费者。
- `clearCodecSchema` — grep 仅命中原 AdapterTab（:379 :483 handleDelete/onClear）。确认无其它消费者。
- `listCodecFiles` — grep 仅命中原 AdapterTab :264（reloadFiles）。确认无其它消费者。
- `getErrorMap` — grep 仅命中原 AdapterTab :281（loadConn）。确认无其它消费者。
- `setErrorMap` — grep 仅命中原 AdapterTab（:414 :446 onSave/onUpload）。确认无其它消费者。
- `setErrorMapFromBaseline` — grep 仅命中原 AdapterTab :516（onPullBaseline）。确认无其它消费者。
- `clearErrorMap` — grep 仅命中原 AdapterTab :471（onClear）。确认无其它消费者。
- `validateCodecSchema` — grep 仅命中原 AdapterTab（:356 :420 :452 :544 submitCreate/onSave/onUpload/liveErrors）。确认无其它消费者。
- `collectCodecSchemaErrors` — grep 仅命中原 AdapterTab :325（refreshBadge）。确认无其它消费者。
- 保留：`addProtos`（ResourceTable :872 :957）、`addScripts`（ResourceTable :875 :960）、`clearProto`（ResourceTable :942）、`clearScript`（ResourceTable :945）、`listProto`（ResourceTable :776 :819）、`listScript`（ResourceTable :776 :819）、`removeProto`（ResourceTable :925）、`removeScript`（ResourceTable :928）、`subscribe`（ResourceTable :785）、`type ResourceFile`（ResourceTable :763 :769 :971 :1066）、`subtractSyncResult`（外壳 :158）、`syncResourcesFromBaseline`（外壳 :94）。

### baselineApi（@/services/baselineApi）
- `fetchBaselineCodecIndex` — grep 仅命中原 AdapterTab :505（onPullBaseline）。确认无其它消费者。
- `fetchBaselineCodec` — grep 仅命中原 AdapterTab :513（onPullBaseline）。确认无其它消费者。

### codecEditor 子组件（原 :65-70）
- `parseCodecForEdit`（codecEdit）、`FrameLayoutEditor`、`PipelineEditor`、`RouteKeyEditor`、`PreviewPanel`、`deriveTransport`（previewHelpers）— grep 全部仅命中原 AdapterTab；ResourceTable 用 `<Editor>` 但不引用任何 codecEditor 子组件。确认无其它消费者。（codecEditor/ 下文件本任务严禁改动，仅删 ResourcesDrawer 对它们的 import。）

## zIndex 怎么加的（实读 :767 pattern）

实读 ResourcesDrawer 原 :767 附近，确认现有 pattern（ResourceTable 内编辑 Modal 在用）：

```tsx
// 原 ResourcesDrawer.tsx :766-767（ResourceTable 内）
const theme = useEditorStore((s) => s.theme);
const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
// ...
// 原 :1084（ResourceTable 编辑 Modal）
<Modal
  ...
  styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
>
```

ProtocolConfigEditor 照搬此 pattern（从 `@/components/FlowEditor/store/floatingWindowStore` import `useFloatingWindowStore`），新建/复制连接 Modal 加 `styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}`。原 AdapterTab 的 Modal 无 zIndex 声明（依赖 antd 默认 1000），抽离后补齐，为 B1-2 挂浮窗（浮窗基线 1000+）准备。

## 验证结果

### tsc
```
cd cmd/web && npx tsc -b
=== tsc exit: 0 ===
```
零报错（无未使用 import、无未定义符号）。

### vitest
```
cd cmd/web && npm run test
Test Files  22 passed (22)
     Tests  287 passed (287)
=== test exit: 0 ===
```
287 测试全绿，无回归。

## git diff --stat

```
 cmd/web/src/components/modules/ResourcesDrawer.tsx | 608 +--------------------
 1 file changed, 5 insertions(+), 603 deletions(-)

新建（untracked）:
 cmd/web/src/components/modules/ProtocolConfigEditor.tsx   (629 行)
```

> 注：`git status` 中还有 `conf/scripts/ranked_*.lua`（D/M）与 `plans/2026-06-18-*.md`（D）以及 `plans/2026-06-22-config-refactor-design.md`（??）——这些是**工作区用户 WIP**（排位迁移 stash@{0} 待重做 + 用户文档草稿），本任务**未 add / 未 commit / 未触碰**，符合「不要碰 conf/」红线。本任务改动严格限于上述 2 个目标文件。

## 自审

- [x] 只平移不重构：antd 废弃 API（destroyOnHidden/destroyOnClose）原样保留，未改样式、未改逻辑。
- [x] `export function` 非 default，无 props。
- [x] UI 文本「协议配置」/「错误码映射」不暴露 codec 字样（保持原文案）。
- [x] 颜色/间距走 tokens.css 变量（本任务沿用原 inline style + `var(--text-tertiary)`/`var(--border-color)`，未引入硬编码）。
- [x] 未碰 codecEditor/ 子组件、services/、store/、pages/、runtime/、conf/。
- [x] 未 git add / git commit。
- [x] 内嵌 Modal zIndex 合规（floatingWindowStore._nextZ + 100），为 B1-2 挂浮窗准备。
- [x] AdapterTab 外部耦合点（resourcesStore 13 函数 + baselineApi 2 函数 + editorStore theme/setCodecSchemaErrors + 内嵌 Modal state）全部原样带走，无遗漏。

## Concerns

无。B1-2（EditorPage 挂浮窗 + RuntimeBar 入口）待后续任务执行；本任务产物 `ProtocolConfigEditor` 已就绪可直接被 B1-2 import 挂载。
