# 可视化流程编辑器

## 概述

基于 React Flow 的 UE 蓝图风格可视化流程编辑器，作为核心 UI 组件 `<FlowEditor />` 嵌入主页面。支持 9 种节点类型的拖拽创建、可视化连线、声明式动作配置、Lua 脚本编辑、Proto 字段选择、实时指标叠加、验证和自动布局。

本文档对照实际代码，记录编辑器架构、组件体系、Store 设计、数据模型和集成方式。

---

## 1. 技术栈

| 关注点 | 选型 | 版本 |
|---|---|---|
| 框架 | React + TypeScript | React 18 / TS 5.6 |
| 构建 | Vite | 8 |
| 流程画布 | `@xyflow/react`（React Flow v12） | ^12 |
| UI 组件库 | Ant Design v5 | ^5 |
| 状态管理 | Zustand + Zundo | ^5 |
| Lua 编辑器 | Monaco Editor | ^4 |
| Proto 解析 | protobufjs | ^7 |
| 持久化 | idb-keyval（IndexedDB） | ^6 |
| 自动布局 | dagre | ^0.8 |
| ID 生成 | nanoid | ^5 |
| 图表 | ECharts | 6 |

---

## 2. 目录结构

```
cmd/web/src/
├── types/
│   ├── flow.ts                         # TaskFlow / FlowNode / ListenRef
│   ├── action.ts                       # ActionDef / FieldBind / FilterDef / StoreMapping
│   ├── listen.ts                       # ListenDef + classifyListen（三态判别）
│   ├── editor.ts                       # NodeLayoutMeta / FlowLayout
│   └── api.ts                          # 后端 API 类型（见 frontend-api.md）
│
├── components/
│   ├── FlowEditor/                     # ★ 核心组件
│   │   ├── index.tsx                   # FlowEditor 组件入口
│   │   ├── FlowCanvas.tsx              # React Flow 画布 + 右键菜单 + F1~F10 快捷键
│   │   ├── CanvasContextMenu.tsx       # 右键菜单 + QUICK_NODE_TYPES
│   │   ├── flowReadOnlyContext.ts      # 只读上下文
│   │   │
│   │   ├── store/
│   │   │   ├── flowStore.ts            # 业务数据 + RF 镜像 + 派生数据（zundo temporal limit 50）
│   │   │   ├── editorStore.ts          # UI 状态（选中/悬停/主题/剪贴板/开关/debugMode）
│   │   │   ├── flowHistory.ts          # undo/redo（基于 zundo temporal）
│   │   │   ├── floatingWindowStore.ts  # 浮动窗口位置
│   │   │   ├── persistDraft.ts         # LocalStorage 编辑稿
│   │   │   └── flowManagerStore.ts     # 流程库门面（服务器 MySQL，经 flowsApi）
│   │   │
│   │   ├── nodes/                      # 自定义节点视觉
│   │   │   ├── ActionNode.tsx
│   │   │   ├── SequenceNode.tsx
│   │   │   ├── LoopNode.tsx
│   │   │   ├── BooleanNode.tsx
│   │   │   ├── SwitchNode.tsx
│   │   │   ├── WeightedNode.tsx
│   │   │   ├── WaitNode.tsx
│   │   │   ├── BreakNode.tsx
│   │   │   ├── ContinueNode.tsx
│   │   │   ├── onErrorBadges.tsx       # onError 摘要徽章
│   │   │   ├── shared/
│   │   │   │   ├── NodeShell.tsx       # 通用外框
│   │   │   │   └── MetricsBadge.tsx    # 监控指标槽
│   │   │   └── registry.ts             # nodeTypes 注册
│   │   │
│   │   ├── edges/                      # 7 种边类型
│   │   │   ├── SeqEdge.tsx
│   │   │   ├── WaitThenEdge.tsx
│   │   │   ├── BranchEdge.tsx
│   │   │   ├── WeightEdge.tsx
│   │   │   ├── LoopBodyEdge.tsx
│   │   │   ├── ListenEdge.tsx
│   │   │   ├── ErrorEdge.tsx           # onError.handler 调用边
│   │   │   ├── edgeStyle.ts            # 颜色/粗细 token
│   │   │   └── registry.ts             # edgeTypes 注册
│   │   │
│   │   ├── connections/
│   │   │   └── waitConnection.ts        # wait 节点 then 连线的替换语义
│   │   │
│   │   ├── editors/                    # 节点属性编辑器
│   │   │   ├── NodeEditorDrawer.tsx    # 抽屉式调度器
│   │   │   ├── SequenceEditor.tsx
│   │   │   ├── LoopEditor.tsx
│   │   │   ├── BooleanEditor.tsx
│   │   │   ├── SwitchEditor.tsx
│   │   │   ├── WeightedEditor.tsx
│   │   │   ├── WaitEditor.tsx
│   │   │   ├── ActionEditor/           # 总控 + PatternSelector / DeclarativeForm /
│   │   │   │                           # LuaForm / BindingsTable / BindingTypeForm /
│   │   │   │                           # StoreTable / ProtoFieldPicker / OnErrorEditor /
│   │   │   │                           # SetStateEditor / ClearStateEditor 等
│   │   │   └── shared/
│   │   │       ├── ConditionInput.tsx
│   │   │       ├── StateExprInput.tsx  # 条件输入即时语法检查
│   │   │       ├── DelayInput.tsx
│   │   │       ├── NodeIdSelect.tsx
│   │   │       └── SetterPathInput.tsx / JsonDraftInput.tsx / LuaScriptField.tsx
│   │   │
│   │   ├── panels/
│   │   │   ├── NodePalette.tsx         # 左侧节点/模板面板
│   │   │   ├── Toolbar.tsx             # 顶部工具栏
│   │   │   ├── FlowManagerModal.tsx    # 流程库（服务器）
│   │   │   ├── FloatingWindow.tsx      # 浮动窗口容器
│   │   │   └── useFlowFileIO.ts        # 导入/导出 flow.json
│   │   │
│   │   ├── listens/                    # Listen 子系统
│   │   │   ├── ListenPanel.tsx
│   │   │   ├── ListenEditor.tsx
│   │   │   ├── ListenCard.tsx          # 画布卡片（虚拟节点 key `__cb__<name>`）
│   │   │   ├── ListenRefsTable.tsx
│   │   │   ├── RouteEditor.tsx / RouteFieldTrack.tsx / RouteFloatingEditor.tsx
│   │   │   ├── BackrefList.tsx
│   │   │   ├── refsGraph.ts
│   │   │   └── listenKindStyle.ts
│   │   │
│   │   ├── library/                    # 模板库门面（服务器 MySQL，经 templatesApi）
│   │   │   ├── templateStore.ts
│   │   │   ├── SaveTemplateButton.tsx
│   │   │   ├── TemplateEditorDrawer.tsx
│   │   │   ├── listenTemplateDefaults.ts
│   │   │   └── useTemplateLibraryCapability.ts
│   │   │
│   │   ├── lua/                        # Lua 编辑支持
│   │   │   ├── luaApiSpec.ts           # 7 模块 87 函数的补全规格
│   │   │   ├── luaSyntaxWorker.ts      # Web Worker 语法检查
│   │   │   └── LuaApiPopover.tsx
│   │   │
│   │   ├── proto/
│   │   │   ├── ProtoLoader.ts          # 三级加载链
│   │   │   ├── ProtoRegistry.ts        # message/field/enum 索引
│   │   │   ├── ProtoBrowser.tsx        # 扁平列表浏览
│   │   │   └── protoStore.ts           # 加载状态 zustand store
│   │   │
│   │   ├── preview/
│   │   │   └── JsonPreviewModal.tsx    # flow.json 预览
│   │   │
│   │   ├── validation/                 # 验证系统（见 §10）
│   │   │   ├── refsCheck.ts / actionRules.ts / bindingRules.ts / issues.ts
│   │   │   ├── FlowValidationCoordinator.tsx（防抖 150ms）
│   │   │   ├── validationStore.ts
│   │   │   └── ValidationReport.tsx
│   │   │
│   │   ├── codec/
│   │   │   ├── flowToJson.ts           # store → TaskFlow
│   │   │   ├── jsonToFlow.ts           # TaskFlow → RF nodes/edges
│   │   │   ├── dagreLayout.ts          # 自动布局
│   │   │   └── useCodecConnections.ts / connectionRouteModel.ts
│   │   │
│   │   └── utils/
│   │       ├── nodeIdGen.ts            # 节点 ID 生成
│   │       └── onError.ts
│   │
│   ├── modules/                        # 主页面模块抽屉
│   │   ├── ResourcesDrawer.tsx / ProtocolConfigEditor.tsx / ErrorMapEditor.tsx
│   │   ├── BaselineSyncModal.tsx       # 基线冲突对比面板
│   │   ├── codecEditor/                # 声明式 codec 编辑器（每连接一份 + 共享 errors.json）
│   │   ├── history/                    # 历史归档面板
│   │   ├── AgentsPanel.tsx / notepad/
│   │   └── configTransfer/             # 配置备份/迁移
│   │
│   ├── monitoring/
│   │   ├── MonitorDock.tsx             # 底部监控面板（见 §13）
│   │   ├── tabs/SystemTab.tsx
│   │   └── shared/                     # ActionMetricsTable / ApdexCell / liveMetrics 等
│   │
│   └── runtime/
│       ├── RuntimeBar.tsx / TaskStartModal.tsx / ActiveTaskGuardModal.tsx
│       └── TaskStartCommonFields.tsx / AuthExtraEditor.tsx
│
├── pages/
│   └── EditorPage.tsx                  # HomeShell 单页入口
│
└── services/                           # API / runtime / 资源管理（见 frontend-api.md）
    ├── api.ts / generatedClient.ts / generated/admin-api.ts
    ├── env.ts / queryClient.ts / queryOptions.ts / useRuntimeQueries.ts
    ├── tasksApi.ts / agentsApi.ts / metricsApi.ts / historyApi.ts
    ├── flowsApi.ts / templatesApi.ts / capabilitiesApi.ts / codecApi.ts
    ├── baselineApi.ts / resourcesStore.ts / protoSync.ts / scriptSync.ts
    ├── runtimeStore.ts / taskActions.ts / taskCapacity.ts / taskResourceDiff.ts
    ├── metricsBinding.ts / conditionSyntax.ts / schemaValidator.ts
    ├── errorHandler.ts / flowScriptRefs.ts / codecConnections.ts
    └── ...
```

---

## 3. 数据模型

### 3.1 FlowNode（9 种节点类型）

源文件：`cmd/web/src/types/flow.ts`

```typescript
type NodeType =
  | 'sequence' | 'action' | 'loop' | 'boolean' | 'switch'
  | 'weighted' | 'wait' | 'break' | 'continue';

interface FlowNode {
  type: NodeType;
  description?: string;       // 人类可读注释，参与 flow.json 序列化

  // sequence 专用
  next?: string[];

  // loop 专用
  body?: string;
  loopCount?: number;
  condition?: string;          // loop / boolean 共享
  breakCondition?: string;

  // boolean 专用
  trueNext?: string;
  falseNext?: string;

  // switch 专用
  cases?: SwitchCase[];        // 按顺序匹配的条件分支（condition / next）
  defaultNext?: string;        // 所有 case 未命中时跳转的节点；空 = 正常结束

  // action 专用
  action?: string;             // actions 表的 key；空 = 纯监听节点（需配 listenRefs）
  onError?: OnErrorDef;        // ignoreCodes / handler / retry / strategy
  listenRefs?: ListenRef[];

  // weighted 专用
  options?: WeightedOption[];

  // wait 专用
  waitMs?: number;
  waitMin?: number;
  waitMax?: number;
  then?: string;

  // 通用：action / boolean
  delayMs?: number;
}
```

### 3.2 ListenRef（监听引用）

```typescript
interface ListenRef {
  route: unknown;             // 不透明 JSON，如 {"cmd":4,"act":10}
  server: string;             // "tcp:logic" / "udp:battle"
  listen: string | null;      // listens 表的 key；null = 静默丢弃
  queueSize?: number;         // 监听缓存队列容量；未写默认 1，显式 <=0 后端报错
}
```

### 3.3 WeightedOption

```typescript
interface WeightedOption {
  node: string;
  weight: number;
}
```

### 3.4 TaskFlow（编辑器主数据模型）

```typescript
interface TaskFlow {
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}
```

### 3.5 ActionDef（14 种 pattern）

源文件：`cmd/web/src/types/action.ts`

```typescript
type ActionPattern =
  | 'tcpSend' | 'tcpRequest' | 'tcpConnect' | 'tcpClose' | 'tcpListen'
  | 'udpSend' | 'udpRequest' | 'udpConnect' | 'udpClose' | 'udpListen'
  | 'httpRequest' | 'setState' | 'clearState' | 'lua';
```

```typescript
interface ActionDef {
  pattern: ActionPattern;
  service?: string;
  route?: unknown;
  script?: string;
  address?: string;
  c2sProto?: string;
  s2cProto?: string;
  bindings?: FieldBind[];
  store?: StoreMapping[];
  timeout?: number;
  url?: string;              // httpRequest
  method?: 'POST' | 'GET';   // httpRequest
  contentType?: 'json' | 'form'; // httpRequest
  keys?: string[];           // clearState
}
```

### 3.6 FieldBind（17 种绑定类型）

```typescript
type BindingType =
  | 'fixed' | 'state' | 'stateFirst'
  | 'stateRandom' | 'stateRandomN'
  | 'stateMapKey' | 'stateMapValue'
  | 'randomPick' | 'randomPickN' | 'randomPickMap'
  | 'randomInt' | 'randomFloat' | 'randomBool' | 'randomString'
  | 'randomExclude'
  | 'listSize' | 'map';
```

```typescript
interface FieldBind {
  field?: string;
  type: BindingType;
  value?: unknown;
  source?: string;
  path?: string;
  values?: unknown[];
  entries?: MapEntry[];      // map 绑定：key→子绑定
  required?: boolean;
  filters?: FilterDef[];
  min?: number;
  max?: number;
  precision?: number;
  length?: number;
  count?: number;
  charset?: string;
  excludeSource?: string;
  optional?: boolean;
  wrap?: boolean;
  storeAs?: string;
  keySource?: string;
  condition?: string;
}
```

### 3.7 FilterDef

```typescript
interface FilterDef {
  path?: string;
  op: string;    // eq/neq/gt/gte/lt/lte/contains/notContains/in/notIn/notNil/isNil
  value?: unknown;
  source?: string;
  mode?: FilterMode; // 'any' | 'all' | 'none'
}
```

### 3.8 StoreMapping

```typescript
interface StoreMapping {
  field?: string;
  setter: string;
}
```

### 3.9 ListenDef（三态判别）

源文件：`cmd/web/src/types/listen.ts`

```typescript
interface ListenDef {
  s2cProto?: string;     // declarative: proto 全名
  store?: StoreMapping[]; // declarative: 字段映射
  script?: string;       // lua: 脚本文件名
  description?: string;  // 人类可读注释
}

type ListenKind = 'silent' | 'declarative' | 'lua';

function classifyListen(cb: ListenDef | null | undefined): ListenKind;
```

三态判别规则（用"字段是否存在"而非 truthy）：
- `script !== undefined` → lua
- `s2cProto !== undefined || store !== undefined` → declarative
- 否则 → silent

### 3.10 视图扩展元数据

源文件：`cmd/web/src/types/editor.ts`

```typescript
interface NodeLayoutMeta {
  x: number;
  y: number;
}

interface FlowLayout {
  nodePositions: Record<string, NodeLayoutMeta>;
  showListenEdges?: boolean;
}
```

ListenCard 节点位置也存在 `nodePositions` 中，key 为 `__cb__<name>`。

---

## 4. 9 种节点类型

> 8 种控制流节点走高对比鲜艳配色（分散节点需强对比才好区分）；action 为中性灰黑（动作辅助）。主色取 Tailwind -600 级，定义见 `cmd/web/src/styles/tokens.css`。

### 4.1 sequence（顺序容器）

- **颜色**：蓝色 `#2563eb`
- **Handle**：左入（target）+ 右出（source，每个 next 一个 handle，垂直排列）
- **中部摘要**：子节点列表
- **字段**：`next: string[]`
- **连线语义**：每个 next handle → 目标节点 target

### 4.2 action（动作节点）

- **颜色**：中性灰黑 `#595959`（动作辅助，无独立色相）
- **Handle**：左入 + 右侧 listen handle + 底部 error handle
- **中部摘要**：action 名 + pattern 标签 + onError 摘要 + listenRefs 数量徽章
- **字段**：`action: string`（引用 actions 表；空 = 纯监听节点）, `onError`, `listenRefs`, `delayMs`
- **监听连线**：右侧 listen handle → ListenEdge（橙色虚线）→ ListenCard
- **错误连线**：底部 error handle → ErrorEdge → 普通节点，保存为 `onError.handler`，不写入 `next`

### 4.3 loop（循环节点）

- **颜色**：绿色 `#16a34a`
- **Handle**：左入 + 下出（body 唯一）；无 exit handle
- **中部摘要**：`loopCount`、`condition`、`breakCondition`
- **字段**：`body`, `loopCount`, `condition`, `breakCondition`
- **设计说明**：循环节点只有一个出口（body）。执行结束后由外层 sequence 的下一个 handle 接管

### 4.4 boolean（条件分支）

- **颜色**：金黄 `#c9920a`，菱形
- **Handle**：左入 + 右出 true（绿色）+ 右出 false（红色）
- **中部摘要**：`condition` 文本
- **字段**：`condition`, `trueNext`, `falseNext`, `delayMs`

### 4.5 switch（多条件分支）

- **颜色**：品红 `#c026d3`
- **Handle**：左入 + 右出每 case 一个 handle + 右侧 case-add（虚线，拖线追加 case）+ 底部 default handle
- **中部摘要**：case 列表（条件摘要 + 目标节点）+ case-add 续接行 + 底部 default 页脚（显示默认目标）
- **字段**：`cases: SwitchCase[]`（`condition` / `next`）, `defaultNext`
- **连线语义**：case handle → 目标节点（branch 边）；case-add 拖线 → 追加 case（condition 默认 state:，进面板补）；底部 default handle → 默认目标（branch 边）
- **执行语义**：按顺序求值 cases，命中第一条执行其 next；全部未命中走 defaultNext；无 default 则正常结束（后端 warn 可见）

### 4.6 weighted（加权随机）

- **颜色**：紫色 `#7c3aed`
- **Handle**：左入 + 右出每个 option 一个 handle，旁标 weight
- **中部摘要**：option 列表 + 内嵌横向条形图（权重比例可视化）
- **字段**：`options: WeightedOption[]`

### 4.7 wait（等待）

- **颜色**：红色 `#dc2626`
- **Handle**：左入 + 右出；右侧连接写入 `then`，再次连接会替换原后继（`connections/waitConnection.ts`）
- **中部摘要**：`waitMs` 数值 + 秒数换算
- **字段**：`waitMs`, `waitMin`, `waitMax`, `then`

### 4.8 break（跳出循环）

- **颜色**：橙色 `#ea580c`
- **Handle**：左入（无出）
- **中部摘要**："break" 字样

### 4.9 continue（跳过本次）

- **颜色**：青色 `#0d9488`
- **Handle**：左入（无出）
- **中部摘要**："continue" 字样

---

## 5. 7 种边类型

注册表：`cmd/web/src/components/FlowEditor/edges/registry.ts`。

### 5.1 SeqEdge（顺序边）

灰色实线 + 箭头。用于 `sequence.next` 连线。

### 5.2 WaitThenEdge（等待后继边）

用于 `wait.then` 连线。

### 5.3 BranchEdge（分支边）

- true 边：绿色，标签 "true"
- false 边：红色，标签 "false"
- switch case 边：标签 "case 1/2/…"（带序号），颜色跟随源节点色（switch 品红）
- switch default 边：标签 "default"，颜色跟随源节点色

用于 `boolean.trueNext` / `falseNext` 与 `switch.cases[].next` / `switch.defaultNext`。boolean 的 true/false 固定绿红；switch 等其余分支跟随源节点配色。

### 5.4 WeightEdge（权重边）

紫色，标签显示权重值。用于 `weighted.options` 连线。

### 5.5 LoopBodyEdge（循环体边）

绿色，特殊弯曲样式。用于 `loop.body` 连线。

### 5.6 ListenEdge（监听边）

橙色虚线 + 箭头。连接 action 节点右侧监听 handle 到 ListenCard。可通过 Toolbar 开关切换显隐。

### 5.7 ErrorEdge（错误处理边）

用于 `onError.handler` 调用边（action 底部 error handle → handler 节点），与普通 next 边视觉区分。

---

## 6. Store 架构

### 6.1 flowStore — 业务数据

源文件：`cmd/web/src/components/FlowEditor/store/flowStore.ts`

**核心状态**：

```typescript
interface FlowState {
  // 业务数据（与 flow.json 1:1）
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;

  // React Flow 镜像
  rfNodes: RFNode[];
  rfEdges: Edge[];
  layout: FlowLayout;

  // UI-only：模板带入的默认监听注册信息，不导出到 flow.json
  listenDefaultRefs: Record<string, ListenTemplateDefaultRef | undefined>;

  // 派生数据
  listenRefCount: Record<string, number>;
  nodesByListen: Record<string, string[]>;
}
```

**关键 mutation**：
- `addNode / updateNode / replaceNode / removeNode / renameNode`
- `addAction / updateAction / removeAction`
- `addListen / updateListen / removeListen`
- 所有 mutation 末尾必须调用 `syncDerived()` 重算 RF 镜像和派生数据

**导出**：`toTaskFlow()` 序列化为 `FlowJson`

**导入**：`loadFromTaskFlow(flow, layout?)` 替换整个 store

### 6.2 editorStore — UI 状态

源文件：`cmd/web/src/components/FlowEditor/store/editorStore.ts`

```typescript
interface EditorState {
  selectedNodeId: string | null;
  focusRequestNodeId: string | null;   // 校验报告跳转请求（nonce 自增触发）
  focusRequestNonce: number;
  selectedListenName: string | null;
  hoveredListen: string | null;
  edgeHighlightNodeIds: string[];      // 选中边的高亮节点
  activePanel: ActivePanels;
  showListenEdges: boolean;
  showMinimap: boolean;
  showGrid: boolean;
  theme: 'light' | 'dark';
  clipboard: Clipboard;
  monitorDockOpen: boolean;
  debugMode: boolean;
  pendingSyncResult: BaselineSyncResult | null; // 待处理基线冲突
  // ...
}
```

- `theme` / `debugMode` / 高级设置折叠状态持久化到 LocalStorage
- 其他状态一次会话内不持久化

### 6.3 flowHistory — 撤销重做

源文件：`cmd/web/src/components/FlowEditor/store/flowHistory.ts`

flowStore 经 zundo temporal 中间件挂历史（`flowStore.ts:647` `limit: 50`）；`undo()` / `redo()` 执行后调用 `syncDerived()` 重算派生数据。

### 6.4 persistDraft — 编辑稿自动保存

源文件：`cmd/web/src/components/FlowEditor/store/persistDraft.ts`

- 监听 flowStore 变更 → debounce 300ms 写入 LocalStorage
- `beforeunload` 事件同步 flush
- 双份存储：`stressbot:flow:current`（TaskFlow）+ `stressbot:flow:layout`（FlowLayout）

### 6.5 flowManagerStore — 流程库

源文件：`cmd/web/src/components/FlowEditor/store/flowManagerStore.ts`

**服务器 MySQL 存储**（经 `services/flowsApi.ts`），保留 `saveFlow` / `getFlow` / `listFlows` / `renameFlow` / `deleteFlow` 函数形态。流程模板只存 flow + layout，不内嵌 Lua/proto/adapter。服务器未配置 MySQL 时接口返回 `FLOW_LIBRARY_DISABLED`。

```typescript
interface ManagedFlow {
  id: string;
  name: string;         // ≤ 80 字符
  nodeCount: number;
  actionCount: number;
  createdAt: string;
  updatedAt: string;
}
```

---

## 7. 编辑器系统

### 7.1 顶层布局

```
┌──────────────────────────────────────────────────────────────┐
│  Toolbar  [撤销][重做][保存][校验][预览] 文件▾(新建/导入/导出) │
├──────────┬───────────────────────────────────────────────────┤
│  Node    │                                                 │
│  Palette │             FlowCanvas (React Flow)             │
│ (左侧)   │                                                 │
├──────────┴───────────────────────────────────────────────────┤
│  MonitorDock (底部，可折叠)                                  │
└──────────────────────────────────────────────────────────────┘
```

### 7.2 NodeEditorDrawer — 抽屉式属性编辑

双击节点打开 `NodeEditorDrawer`，按 `node.type` 路由到具体编辑器：
- SequenceEditor / LoopEditor / BooleanEditor / SwitchEditor / WeightedEditor / WaitEditor / ActionEditor
- break / continue 仅显示 ID

**「还原本次修改」按钮**：打开抽屉时 `useRef` 记录深拷贝快照，编辑过程中激活，点击整体回退。

### 7.3 ActionEditor — 最复杂的编辑器

**顶层结构**：
- 动作名输入
- PatternSelector（14 种 pattern 下拉）
- 依据 Pattern 动态渲染对应表单（含 SetStateEditor / ClearStateEditor 专用表单）
- OnErrorEditor（ignoreCodes / handler / retry / strategy）
- ListenRefsTable（可折叠面板）

**各 Pattern 的字段矩阵**：

| Pattern | service | route | c2sProto | s2cProto | bindings | store | 其他 |
|---|---|---|---|---|---|---|---|
| tcpSend | 可选 | 可选 | 可选 | - | 可选 | - | - |
| tcpRequest | 可选 | 可选 | 可选 | 可选 | 可选 | 可选 | timeout |
| tcpConnect | - | - | - | - | - | - | address |
| tcpClose | - | - | - | - | - | - | - |
| tcpListen | 可选 | 可选 | - | 可选 | - | 可选 | timeout |
| udpSend | 可选 | 可选 | 可选 | - | 可选 | - | - |
| udpRequest | 可选 | 可选 | 可选 | 可选 | 可选 | 可选 | timeout |
| udpConnect | - | - | - | - | - | - | address |
| udpClose | - | - | - | - | - | - | - |
| udpListen | 可选 | 可选 | - | 可选 | - | 可选 | timeout |
| httpRequest | - | - | - | - | - | - | url, method, contentType |
| setState | - | - | - | - | 可选 | - | - |
| clearState | - | - | - | - | - | - | keys |
| lua | - | - | - | - | - | - | script |

### 7.4 BindingsTable — 字段绑定列表

表格列：`field` / `type` / 详情 / `storeAs` / `wrap` / `optional` / 操作

- `field` 列带自动补全（来源 c2sProto 解析后的字段名）
- `type` 列下拉 17 种 BindingType
- 切换 type 后右侧条件渲染对应子表单

### 7.5 BindingTypeForm — 按 type 动态渲染

| BindingType | 必填字段 | 控件 |
|---|---|---|
| fixed | value | 多类型输入 |
| state | source | state key 输入 |
| stateRandom | source, path?, filters? | source + 过滤器 |
| stateRandomN | source, count, path?, filters? | 同上 + count |
| randomInt | min, max | 数字范围 |
| randomFloat | min, max, precision | 数字范围 + 精度 |
| randomBool | - | 无字段 |
| randomString | length, charset | 长度 + 字符集选择 |
| randomPick | values | 数组编辑器 |
| randomPickN | values, count | 同上 + count |
| randomPickMap | values, keySource | key-values 表格 |
| randomExclude | values/source, excludeSource | 来源 + 排除源 |
| stateMapKey | source | 仅 source |
| stateMapValue | source, path?, filters? | 同 stateRandom |
| stateFirst | source, path?, filters? | 同 state |
| listSize | source | 仅 source |
| map | entries | key→value 映射条目（key 固定，value 为子 binding） |

`randomString.charset` 支持 `lower`（a-z）、`upper`（A-Z）、`alpha`（a-zA-Z）、`numeric`（0-9）、`alphanum`（a-zA-Z0-9，默认）别名，也可以输入自定义字符集字面量，如 `ABC-123_`。

### 7.6 LuaForm — 脚本编辑器

基于 Monaco Editor，支持三种 mode：

| mode | 入口函数 | 返回值 | Lint 检查 |
|---|---|---|---|
| `action` | `function execute(r)` | `return nil / err table` | 检查 execute 存在 |
| `boolean` | `function execute(r)` | `return true / false` | 检查 execute 存在 |
| `listen` | `function on_message(r, msg)` | 无返回值 | 检查 on_message 存在 |

功能：
- 导入/导出 .lua 文件
- Lua API 自动补全（`lua/luaApiSpec.ts`：7 个模块 87 个函数——network 21 / robot 15 / utils 15 / proto 10 / json 2 / log 4 / share 20）
- Web Worker 语法检查（`lua/luaSyntaxWorker.ts`）

---

## 8. Listen 子系统

### 8.1 三态模型

每个 ListenDef 有三种形态：

| 形态 | 数据特征 | 行为 |
|---|---|---|
| silent | `{}`（空对象） | 收到推送即丢弃 |
| declarative | `{s2cProto, store}` | 按 proto 反序列化，挑字段写 state |
| lua | `{script}` （可选 s2cProto） | 执行 Lua function on_message(r, msg)；配置 s2cProto 时 msg 为字段表，未配置时为 nil |

### 8.2 视觉布局

**画布主区 + 事件区分区**：

- 主区：控制流 DAG
- 事件区（右侧 25%）：ListenCard 自由排布
- ListenEdge：橙色虚线连接 action → ListenCard

**三层视图**：

| 层 | 形态 | 状态 |
|---|---|---|
| ListenPanel（右侧抽屉） | 列表 + 编辑 | 已实现 |
| ListenCard + ListenEdge | 画布浮动卡片 + 连线 | 已实现 |
| 双向悬停高亮 | 画布交互 | 已实现 |

### 8.3 ListenPanel — 主编辑面板

右侧抽屉式，从 Toolbar 唤出：
- 列表：名称 + 形态徽章 + 引用计数
- 引用计数为 0 标橙色警告（孤儿）
- 悬停某项 → 主画布上注册它的 action 节点亮边
- 反查展开 BackrefList

### 8.4 ListenEditor — 详情编辑

Tab 切换形态：silent / declarative / lua。

**silent 形态**：清空 s2cProto / store / script。

**declarative 形态**：
- s2cProto 选择（ProtoBrowser）
- StoreTable（与 ActionEditor 共用组件）

**lua 形态**：
- script 选择（新建/已存在）
- Monaco 编辑器（mode='listen'）

### 8.5 ListenRefsTable — 监听引用编辑

在 ActionEditor 底部以可折叠面板形式嵌入：

| 列 | 控件 |
|---|---|
| route | RouteEditor / RouteFloatingEditor（不透明 JSON，支持字段轨迹 RouteFieldTrack） |
| server | 纯文本 Input |
| listen | 下拉（全局 listens）+ 新建 + 跳转 + null（静默丢弃） |
| 形态 | 只读徽章（实时反映 listen 当前形态） |

### 8.6 RouteEditor — 不透明 JSON 输入

单 Input 接受任意 JSON。行为：
- 合法 JSON → 解析为对象写回 store
- 非法 JSON → 原样以字符串保留（避免抖动）
- 空字符串 → 写回 undefined

同一组件被 DeclarativeForm / ListenRefsTable / BackrefList 多处共用。

### 8.7 引用图与校验

`refsGraph.ts` 提供引用图计算：

```typescript
interface RefsGraph {
  nodeToRefs: Map<string, ListenRef[]>;
  listenToRefs: Map<string, Array<{ nodeId; refIndex; ref }>>;
  duplicateRegisters: Array<{ server; routeKey; refs }>;
}
```

---

## 9. Proto 系统

### 9.1 ProtoLoader — 三级加载链

源文件：`cmd/web/src/components/FlowEditor/proto/ProtoLoader.ts`

按优先级加载：

| 顺序 | 来源 | 实现方式 |
|---|---|---|
| A. 本地存储（IndexedDB，用户上传） | 优先 | `resourcesStore.listProto` |
| B. 服务器基线 `/sbot/baseline/proto/*` | 本地为空时 | fetch `/sbot/baseline/proto/index.json` 拿清单，并发 fetch 每个文件 |
| C. `import.meta.glob` 兜底 | 基线也失败时 | Vite `?raw` import 把仓库根 `conf/proto/*.proto` 编译期内联 |

三级全空时报错并提示检查资源上传与 `vite server.fs.allow` 配置。

### 9.2 ProtoRegistry — 消息索引

```typescript
class ProtoRegistry {
  load(root: protobuf.Root): void;
  lookupMessage(fullName: string): ProtoMessage | undefined;
  listMessages(prefix?: string): ProtoMessage[];
  resolveFieldType(messageName: string, fieldName: string): FieldType;
}
```

### 9.3 ProtoBrowser — 消息浏览器

扁平列表（按 fullName 字典序），不再树形展开：
- 搜索框即时过滤
- 选中消息显示字段详情
- `[选择此消息]` 按钮回填到编辑器

### 9.4 protoStore — 加载状态管理

```typescript
type ProtoStatus = 'idle' | 'loading' | 'ok' | 'error';

interface ProtoStoreState {
  status: ProtoStatus;
  hash?: string;
  errorMessage?: string;
  setStatus(s: ProtoStatus, hash?: string, err?: string): void;
}
```

---

## 10. 验证系统

统一入口：`validation/FlowValidationCoordinator.tsx`——连续编辑只在停止输入 **150ms** 后校验一次（`createValidationScheduler(validateAndPublish, 150)`）。

规则分四层，合计 **91 种规则 code**：

| 文件 | 规则数 | 覆盖面 |
|---|---|---|
| `validation/refsCheck.ts` | 43 | 图结构引用 + 必填 + Ajv Schema（`FLOW_SCHEMA_INVALID`）+ 条件语法（`CONDITION_SYNTAX`） |
| `validation/actionRules.ts` | 28 | pattern 语义（必填字段、互斥、取值域） |
| `validation/bindingRules.ts` | 19 | 绑定类型语义（source/count/区间等） |

**Error 级别（refsCheck 代表规则）**：

| 规则 | 说明 |
|---|---|
| next/body/trueNext/falseNext/options[].node/cases[].next/defaultNext 引用的节点不存在 | 引用合法性 |
| action 节点的 action 字段不存在于 actions 表 | 引用合法性 |
| listenRefs[].listen 可省略；引用不存在的 listen 不作为校验错误，运行时按队列缓存/无回调处理 | queue-only 行为 |
| loop 的 body 为空 | 必填 |
| weighted 至少 1 个 option | 必填 |
| switch 至少 1 个 case（SWITCH_NO_CASES） | 必填 |
| switch 的 case 缺少 condition（SWITCH_CASE_NO_CONDITION）或 next（SWITCH_CASE_NO_NEXT） | 必填 |
| boolean 的 condition 为空（BOOLEAN_NO_CONDITION） | 必填 |
| 条件表达式语法错误（CONDITION_SYNTAX） | 语法合法性 |
| declarative listen 的 s2cProto 不在 ProtoRegistry | proto 存在性 |
| lua listen 的 script 不存在 | 脚本存在性 |
| pattern=lua 的 action 的 script 必填 | 必填 |
| Ajv JSON Schema 校验失败（FLOW_SCHEMA_INVALID） | 结构契约 |

**Ajv Schema 校验**：`services/schemaValidator.ts` 用 Ajv 2020 校验 `config/validation/flow.schema.json`（前端）与 codec schema；结构契约类错误落 `FLOW_SCHEMA_INVALID`。

CONDITION_SYNTAX 的检查面与 Go 端 `flow/prepare.go` 的编译点一一对应：节点 `condition`、`breakCondition`、`cases[].condition` 与各 `FieldBind.condition`（含 map entry 内的子绑定）。判定由 `services/conditionSyntax.ts` 做，它镜像 `flow/condition_tokenizer.go` + `flow/condition_compile.go` 的文法——后端加载期只做语法检查（类型在运行期按实际 state 值判定），因此前端能给出与后端一致的判定而非近似子集。缺 `state:` / `lua:` 前缀同样算语法错误：后端不按 state 兜底，而是直接拒绝整份流程。`StateExprInput` 里同一套检查会在输入时当场标红。

**Warning 级别（代表规则）**：

| 规则 | 说明 |
|---|---|
| listen 引用计数为 0（孤儿） | 未使用 |
| 不可达节点（从 main BFS 不到） | 死代码 |
| loop 至少需要 loopCount > 0 或 condition 或 breakCondition | 必填项至少一项 |
| switch 未配置 defaultNext（SWITCH_NO_DEFAULT） | 所有条件未命中时将直接结束 |

### 10.2 ValidationReport — 校验报告 UI

抽屉式面板，按 Error / Warning / Info 分级展示，点击每条跳转到对应节点（经 editorStore 的 focusRequest）。

---

## 11. 持久化

### 11.1 编辑稿（LocalStorage）

由 `persistDraft.ts` 管理，debounce 300ms + beforeunload flush：

| Key | 内容 |
|---|---|
| `stressbot:flow:current` | TaskFlow 业务数据 |
| `stressbot:flow:layout` | FlowLayout 画布布局 |
| `stressbot:flow:stash` | viewActive 前的草稿 stash |

### 11.2 流程库（服务器 MySQL）

FlowManagerModal 经 `flowManagerStore` → `flowsApi` 读写服务器流程模板库（`/sbot/flows` 端点组，含乐观锁快照替换）。服务器未配置 MySQL 时返回 `FLOW_LIBRARY_DISABLED`。

### 11.3 模板库（服务器 MySQL）

Action / Listen 模板经 `library/templateStore.ts` → `templatesApi` 读写服务器模板库（`/sbot/action-templates`、`/sbot/listen-templates` 端点组）。`useTemplateLibraryCapability` 按 `capabilities.templateLibrary` 控制入口可见性。

### 11.4 资源文件（IndexedDB）

| DB | Store | 内容 |
|---|---|---|
| `stressbot-resources-proto` | `data` | 用户上传的 .proto |
| `stressbot-resources-scripts` | `data` | 用户上传的 .lua |
| `stressbot-resources-adapter` | `data` | 各 `*_codec.json` + 共享 `errors.json` |

---

## 12. 集成方式

### 12.1 FlowEditorProps — 组件 API

```typescript
interface FlowEditorProps {
  initialFlow?: FlowJson;
  initialLayout?: FlowLayout;
  autoLoadDefault?: boolean;        // 自动加载 conf/flow/flow.json
  metrics?: ReadonlyMap<string, ActionMetric>; // 按节点 ID 索引的运行指标
  readOnly?: boolean;               // viewActive / running / finalReport 时为 true
  topbarExtra?: ReactNode;          // 渲染在 Toolbar 最右侧（RuntimeBar）
}
```

### 12.2 HomeShellInner — 单页编排

源文件：`cmd/web/src/pages/EditorPage.tsx`

```
HomeShellInner
├── RuntimeBar (注入到 FlowEditor topbarExtra)
├── FlowEditor (readOnly = mode != 'edit')
│   └── NodeShell + MetricsBadge ← metrics
├── MonitorDock (可折叠)
└── Drawers
    ├── ResourcesDrawer (proto/lua/协议配置/错误码资源)
    ├── HistoryModal (列表/详情/对比)
    ├── AgentsPanel (节点状态)
    ├── SystemTab (集群系统资源)
    └── NotepadTab (笔记)
```

### 12.3 RuntimeMode 状态机

由 `runtimeStore` 管理（详见 `frontend-api.md` §13），四态：`edit` / `viewActive` / `running` / `finalReport`（`starting` / `stopping` 是后端 TaskState，不是前端 mode）：

```
edit ──[startTask]──> running ──> finalReport ──[新建]──> edit
  └──[attachToActive]──> viewActive ──> finalReport
```

运行中可临时返回编辑（detachToEdit），终态退出（detachFromActive）或彻底重置（reset）。

### 12.4 监控指标注入

`metricsBinding` 将 StressSnapshot.actions[] 映射到节点 ID：
- 名字以 `callback:` 开头 → 虚拟节点 ID `__cb__<name>`（ListenCard）
- 其它 → 匹配 `flow.nodes[i].action == metric.name`

注入后 `metrics` prop（`(nodeId) => ActionMetric | undefined`）被 NodeShell 消费：
- 左下角 executing 角标（脉动）
- 节点边框按 Apdex 染色（5 级）
- 底部 metrics 槽

### 12.5 TaskStartModal — 启动任务弹窗

源文件：`cmd/web/src/components/runtime/TaskStartModal.tsx`

两阶段 UX：复核 → 提交。

调试模式（`editorStore.debugMode`）：

| 维度 | 测试模式 | 调试模式 |
|---|---|---|
| totalBots / concurrency | 用户填写 | 自动装填 1 / 1 |
| logLevel | 用户选择 | 自动装填 debug |
| rampUp | 用户配置 | 禁用并自动关闭 |
| 节点分配 | 按容量比例分配 | 优先单节点承载 |

容量预检两种模式一致：申请数（渐进加压时为各阶段之和）不得超过空闲节点 `maxBots` 之和，口径与服务端 `admin/task/assignment.go` 对齐，实现见 `cmd/web/src/services/taskCapacity.ts`。

### 12.6 ActiveTaskGuardModal — 活动任务守卫

页面加载时检测 active 任务，引导用户选择"查看运行中"或"继续编辑"。

---

## 13. MonitorDock — 监控面板

源文件：`cmd/web/src/components/monitoring/MonitorDock.tsx`（+ `shared/liveMetrics.ts`、`shared/ActionMetricsTable.tsx`、`tabs/SystemTab.tsx`）

底部停靠面板，由 `buildLivePanelModel(stress, system, agents)` 构建单一实时面板模型（`LivePanelModel`）：

| 区块 | 内容 | 数据源 |
|---|---|---|
| 负载进度 | 运行/启动/停止/出错机器人 + 进度条；渐进加压时显示分段阶段条（阶段号来自后端 `rampUp` 快照，非前端估算） | latestStress.robots / rampUp |
| 吞吐 | 当前 QPS（区间）、全程均值、收发速率 | window.summary + bandwidth |
| 质量 | 样本/成功/失败/超时/取消/执行中、成功率、RTT Apdex | summary |
| 延迟 | 总 avg/p95/p99、RTT avg/p95、非 RTT 均值 | summary 直方图 |
| 连接 | 活跃/失败/已断开 | connections |
| 节点资源 | CPU/内存均值与最高、网卡收发、RSS/FD、热点节点 | PerAgentSystem 聚合 |
| 动作表 | 每个 ActionMetric 一行（ActionMetricsTable，虚拟滚动） | latestStress.actions |
| 系统 | 集群 CPU/MEM/NET/Goroutine 详情（SystemTab） | latestSystem + agents |

可拖拽顶部把手调整高度，高度持久化到 LocalStorage。

---

## 14. 编解码系统

### 14.1 flowToJson — store → TaskFlow

源文件：`cmd/web/src/components/FlowEditor/codec/flowToJson.ts`

序列化 store 数据为 flow.json 格式：
- 清理空字段（`undefined` / `null` / `''` / `[]` 不导出）
- 保留 `description` 字段
- ListenDef 空字段清理

### 14.2 jsonToFlow — TaskFlow → RF 节点边

源文件：`cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts`

反序列化 flow.json 为 React Flow 节点和边：
- 按 node type 生成对应视觉节点
- 按 next/body/trueNext/falseNext/options/then 生成对应类型的边
- switch：`cases[i].next` → branch 边（sourceHandle=`case-${i}`，data.branch=`case`）；`defaultNext` → branch 边（sourceHandle=`default`，data.branch=`default`）
- onError：`onError.handler` → error 边
- 计算 listenRefCount / nodesByListen 派生数据
- ListenCard 节点 key 为 `__cb__<name>`

### 14.3 dagreLayout — 自动布局

源文件：`cmd/web/src/components/FlowEditor/codec/dagreLayout.ts`

导入无 layout 文件的 flow.json 时，用 dagre 自动布局兜底。

---

## 15. 快捷键

| 快捷键 | 行为 |
|---|---|
| `Ctrl+S` | 保存（弹预览） |
| `Ctrl+Z / Ctrl+Shift+Z` | Undo / Redo |
| `Delete` | 删除选中节点/边 |
| `Ctrl+D` | 复制选中节点 |
| `Ctrl+F` | 节点搜索 |
| `F1`–`F9` | 在鼠标位置新建对应节点（按 QUICK_NODE_TYPES 顺序：sequence/loop/boolean/switch/weighted/wait/break/continue/action，`CanvasContextMenu.tsx:39`） |
| `F10` | 在鼠标位置新建 Listen 卡片（节点 F1..F{length}，listen F{length+1}，按数组长度推导） |

---

## 16. 连接规则

| 源 | 合法目标 | 备注 |
|---|---|---|
| sequence next handle | 任意节点 target | 在 N 处插入（seq-add 末尾追加） |
| loop body handle | 任意节点 target | 一对一 |
| boolean true/false handle | 任意节点 target | 一对一 |
| switch case-${i} handle | 任意节点 target | 在 i 处插入新 case（condition 默认 state:），原 case 下移 |
| switch case-add handle（虚线） | 任意节点 target | cases 末尾追加（condition 默认 state:） |
| switch default handle（底部） | 任意节点 target | defaultNext 赋值 |
| weighted option handle | 任意节点 target | 在 N 处插入（opt-add 末尾追加） |
| wait 右出 handle | 任意节点 target | 写入 `then`，重复连接替换原后继 |
| action 监听 handle | ListenCard | ListenEdge |
| action 底部 error handle | 任意节点 | ErrorEdge，写入 `onError.handler` |

**禁止**：自环、重复连接、break/continue 连出。

---

## 17. 导入导出流程

### 17.1 导出

1. `flowStore.toTaskFlow()` 序列化
2. `validateFlow()` 校验
3. 弹 `JsonPreviewModal`（Monaco 只读）
4. 用户选择：复制 / 下载 flow.json / 下载 layout.json

### 17.2 导入

1. File picker 选 .json
2. 解析 → 校验
3. 有 layout.json 一并导入，否则 dagre 自动布局
4. 替换 store
5. `syncFlowScriptsToIdb` 对缺失脚本做基线 gap-fill

---

## 18. 基线同步机制

详细机制见 `docs/resource-sync.md`。

### 18.1 resourcesStore.syncResourcesFromBaseline()

对比 IDB 与基线（Admin 或 Vite 中间件）：
- 新增 → 自动写入 IDB
- 内容相同 → unchanged
- 内容不同 → 返回冲突列表
- 基线已删除 → 返回 removed 列表

用户通过 `applyConflictResolution(decisions)` 选择保留本地或采用基线。未处理冲突保存在 `editorStore.pendingSyncResult`，工具栏「资源」按钮角标提示待处理数。

### 18.2 scriptSync.syncFlowScriptsToIdb()

扫描 flow 引用的脚本名，对 IDB 中缺失的从基线拉取写入。已有脚本永不覆盖。

### 18.3 protoSync.syncProtosToIdb()

proto 的同类 gap-fill：IDB 缺失的 proto 从基线拉回，不覆盖本地；拉回新文件后重载 proto 注册表。

---

## 19. 后续扩展方向

1. 图分组与折叠 — sequence/weighted 折叠为子流程
2. Diff 视图 — 两个 flow.json 互相对比
3. 协作模式 — 多人在线编辑
4. LSP 集成 — Lua 完整工具链
5. AI 辅助 — 自然语言生成流程图
6. ProtoIndex IDB 缓存 — 第二次进入加速到 < 200ms
