# 可视化流程编辑器

## 概述

基于 React Flow 的 UE 蓝图风格可视化流程编辑器，作为核心 UI 组件 `<FlowEditor />` 嵌入主页面。支持 8 种节点类型的拖拽创建、可视化连线、声明式动作配置、Lua 脚本编辑、Proto 字段选择、实时指标叠加、验证和自动布局。

本文档对照 `plans/design-web-editor.md`（计划文档）和实际代码，详尽记录编辑器架构、组件体系、Store 设计、数据模型和集成方式。

### 与计划的差异汇总

| 差异点 | 计划 | 实际实现 |
|---|---|---|
| FlowNode.breakOff | 计划中有 | 实际 `types/flow.ts` 中不存在 |
| FlowNode.listenCallbacks | 计划中字段名 | 实际为 `listenRefs`（`FlowNode.listenRefs`） |
| ListenRef.callback | 计划中字段名 | 实际为 `listen`（`ListenRef.listen`） |
| ActionDef 字段集 | 计划中有 `target/secretArg` 等 | 实际为 `url/method/contentType/keys` 等，pattern 名也不同（如 `tcpSend/tcpRequest` 而非 `connect/connectUDP`） |
| BindingType | 计划中有 `nested/nestedList` | 实际无 `nested/nestedList`，新增 `randomFloat` |
| TaskFlow.callbacks | 计划中字段名 | 实际为 `TaskFlow.listens` |
| CallbackKind / classifyCallback | 计划中函数名 | 实际为 `ListenKind / classifyListen` |
| CallbackCard | 计划中组件名 | 实际组件名一致但数据模型中 key 前缀 `__cb__` 保持 |
| ProtoIndex 缓存到 IDB | 计划中有 | 实际暂未实现，每次重新解析 |
| ajv JSON Schema 校验 | 计划中使用 | 实际由 `validation/refsCheck.ts` 等价覆盖 |
| FlowManagerModal | 计划中在 §10.3 | 实际已实现（`store/flowManagerStore.ts`） |
| 路由前缀 | 计划 `/api/` | 实际 `/sbot/`（见 `services/env.ts`） |
| FlowEditor.onSave prop | 计划中有 | 实际未接线，由 Toolbar 触发 download |
| FlowEditor.theme prop | 计划中有 | 实际由 `editorStore.theme` 内部管理 |

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
│   └── FlowEditor/                     # ★ 核心组件
│       ├── index.tsx                   # FlowEditor 组件入口
│       ├── FlowCanvas.tsx              # React Flow 画布 + 右键菜单
│       │
│       ├── store/
│       │   ├── flowStore.ts            # 业务数据 + RF 镜像 + 派生数据
│       │   ├── editorStore.ts          # UI 状态（选中/悬停/主题/剪贴板/开关）
│       │   ├── undoRedo.ts             # 自定义快照式 undo/redo
│       │   ├── persistDraft.ts         # LocalStorage 编辑稿
│       │   └── flowManagerStore.ts     # IndexedDB 流程库
│       │
│       ├── nodes/                      # 自定义节点视觉
│       │   ├── ActionNode.tsx
│       │   ├── SequenceNode.tsx
│       │   ├── LoopNode.tsx
│       │   ├── BooleanNode.tsx
│       │   ├── WeightedNode.tsx
│       │   ├── WaitNode.tsx
│       │   ├── BreakNode.tsx
│       │   ├── ContinueNode.tsx
│       │   ├── shared/
│       │   │   ├── NodeShell.tsx       # 通用外框
│       │   │   └── MetricsBadge.tsx    # 监控指标槽
│       │   └── registry.ts             # nodeTypes 注册
│       │
│       ├── edges/
│       │   ├── SeqEdge.tsx
│       │   ├── BranchEdge.tsx
│       │   ├── WeightEdge.tsx
│       │   ├── LoopBodyEdge.tsx
│       │   ├── ListenEdge.tsx
│       │   ├── edgeStyle.ts            # 颜色/粗细 token
│       │   └── registry.ts
│       │
│       ├── editors/                    # 节点属性编辑器
│       │   ├── NodeEditorDrawer.tsx    # 抽屉式调度器
│       │   ├── SequenceEditor.tsx
│       │   ├── LoopEditor.tsx
│       │   ├── BooleanEditor.tsx
│       │   ├── WeightedEditor.tsx
│       │   ├── WaitEditor.tsx
│       │   ├── ActionEditor/
│       │   │   ├── index.tsx           # 总控
│       │   │   ├── PatternSelector.tsx
│       │   │   ├── DeclarativeForm.tsx
│       │   │   ├── LuaForm.tsx
│       │   │   ├── BindingsTable.tsx
│       │   │   ├── BindingTypeForm.tsx
│       │   │   ├── StoreTable.tsx
│       │   │   └── ProtoFieldPicker.tsx
│       │   └── shared/
│       │       ├── ConditionInput.tsx
│       │       ├── DelayInput.tsx
│       │       └── NodeIdSelect.tsx
│       │
│       ├── panels/
│       │   ├── NodePalette.tsx         # 左侧节点/模板面板
│       │   ├── Toolbar.tsx             # 顶部工具栏
│       │   └── FloatingWindow.tsx      # 浮动窗口容器
│       │
│       ├── callbacks/                  # Listen 子系统
│       │   ├── CallbackPanel.tsx
│       │   ├── CallbackEditor.tsx
│       │   ├── CallbackCard.tsx
│       │   ├── ListenRefsTable.tsx
│       │   ├── RouteEditor.tsx
│       │   ├── BackrefList.tsx
│       │   ├── refsGraph.ts
│       │   └── callbackKindStyle.ts
│       │
│       ├── library/                    # 模板库（IndexedDB）
│       │   ├── templateStore.ts
│       │   ├── SaveTemplateButton.tsx
│       │   └── TemplateEditorDrawer.tsx
│       │
│       ├── proto/
│       │   ├── ProtoLoader.ts          # 加载策略
│       │   ├── ProtoRegistry.ts        # message/field/enum 索引
│       │   ├── ProtoBrowser.tsx        # 扁平列表浏览
│       │   └── protoStore.ts           # 加载状态 zustand store
│       │
│       ├── adapter/
│       │   └── CodecAdapterDrawer.tsx  # codec.lua 编辑器
│       │
│       ├── preview/
│       │   └── JsonPreviewModal.tsx    # flow.json 预览
│       │
│       ├── validation/
│       │   ├── refsCheck.ts            # 引用 + 必填字段校验
│       │   ├── refsCheck.test.ts
│       │   └── ValidationReport.tsx
│       │
│       ├── codec/
│       │   ├── flowToJson.ts           # store → TaskFlow
│       │   ├── jsonToFlow.ts           # TaskFlow → RF nodes/edges
│       │   ├── dagreLayout.ts          # 自动布局
│       │   └── codec.test.ts
│       │
│       └── utils/
│           └── nodeIdGen.ts            # 节点 ID 生成
│
├── pages/
│   └── EditorPage.tsx                  # HomeShell 单页入口
│
└── services/                           # API / runtime / 资源管理
    ├── api.ts
    ├── env.ts
    ├── tasksApi.ts
    ├── agentsApi.ts
    ├── metricsApi.ts
    ├── historyApi.ts
    ├── logsApi.ts
    ├── baselineApi.ts
    ├── runtimeStore.ts
    ├── taskActions.ts
    ├── resourcesStore.ts
    └── scriptSync.ts
```

---

## 3. 数据模型

### 3.1 FlowNode（8 种节点类型）

源文件：`cmd/web/src/types/flow.ts`

```typescript
type NodeType =
  | 'sequence' | 'action' | 'loop' | 'boolean'
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

  // action 专用
  action?: string;             // actions 表的 key
  errorStrategy?: 'ignore' | 'skip' | 'abort';
  listenRefs?: ListenRef[];

  // weighted 专用
  options?: WeightedOption[];

  // wait 专用
  waitMs?: number;
  waitMin?: number;
  waitMax?: number;

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
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}
```

> 计划中字段名 `callbacks`，实际为 `listens`。

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
  pollMs?: number;
  url?: string;              // httpRequest
  method?: 'POST' | 'GET';   // httpRequest
  contentType?: 'json' | 'form'; // httpRequest
  keys?: string[];           // clearState
}
```

### 3.6 FieldBind（16 种绑定类型）

```typescript
type BindingType =
  | 'fixed' | 'state' | 'stateFirst'
  | 'stateRandom' | 'stateRandomN'
  | 'stateMapKey' | 'stateMapValue'
  | 'randomPick' | 'randomPickN' | 'randomPickMap'
  | 'randomInt' | 'randomFloat' | 'randomBool' | 'randomString'
  | 'randomExclude'
  | 'listSize';
```

```typescript
interface FieldBind {
  field?: string;
  type: BindingType;
  value?: unknown;
  source?: string;
  path?: string;
  values?: unknown[];
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
  op: string;    // eq/neq/gt/gte/lt/lte/contains/in/notNil/isNil
  value?: unknown;
  source?: string;
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

## 4. 8 种节点类型

### 4.1 sequence（顺序容器）

- **颜色**：蓝色 `#1890ff`
- **Handle**：左入（target）+ 右出（source，每个 next 一个 handle，垂直排列）
- **中部摘要**：子节点列表
- **字段**：`next: string[]`
- **连线语义**：每个 next handle → 目标节点 target

### 4.2 action（动作节点）

- **颜色**：蓝紫 `#597ef7`
- **Handle**：左入 + 右出（单 handle）+ 右侧监听专用 handle 区
- **中部摘要**：action 名 + pattern 标签 + listenRefs 数量徽章
- **字段**：`action: string`（引用 actions 表）, `errorStrategy`, `listenRefs`, `delayMs`
- **监听连线**：右侧监听 handle → ListenEdge（橙色虚线）→ CallbackCard

### 4.3 loop（循环节点）

- **颜色**：绿色 `#52c41a`
- **Handle**：左入 + 下出（body 唯一）；无 exit handle
- **中部摘要**：`loopCount`、`condition`、`breakCondition`
- **字段**：`body`, `loopCount`, `condition`, `breakCondition`
- **设计说明**：循环节点只有一个出口（body）。执行结束后由外层 sequence 的下一个 handle 接管

### 4.4 boolean（条件分支）

- **颜色**：黄色 `#fadb14`，菱形
- **Handle**：左入 + 右出 true（绿色）+ 右出 false（红色）
- **中部摘要**：`condition` 文本
- **字段**：`condition`, `trueNext`, `falseNext`, `delayMs`

### 4.5 weighted（加权随机）

- **颜色**：紫色 `#722ed1`
- **Handle**：左入 + 右出每个 option 一个 handle，旁标 weight
- **中部摘要**：option 列表 + 内嵌横向条形图（权重比例可视化）
- **字段**：`options: WeightedOption[]`

### 4.6 wait（等待）

- **颜色**：红色 `#f5222d`
- **Handle**：左入 + 右出
- **中部摘要**：`waitMs` 数值 + 秒数换算
- **字段**：`waitMs`, `waitMin`, `waitMax`

### 4.7 break（跳出循环）

- **颜色**：橙色 `#fa8c16`
- **Handle**：左入（无出）
- **中部摘要**："break" 字样

### 4.8 continue（跳过本次）

- **颜色**：青色 `#13c2c2`
- **Handle**：左入（无出）
- **中部摘要**："continue" 字样

---

## 5. 5 种边类型

### 5.1 SeqEdge（顺序边）

灰色实线 + 箭头。用于 `sequence.next` 连线。

### 5.2 BranchEdge（分支边）

- true 边：绿色，标签 "T"
- false 边：红色，标签 "F"

用于 `boolean.trueNext` / `falseNext`。

### 5.3 WeightEdge（权重边）

紫色，标签显示权重值。用于 `weighted.options` 连线。

### 5.4 LoopBodyEdge（循环体边）

绿色，特殊弯曲样式。用于 `loop.body` 连线。

### 5.5 ListenEdge（监听边）

橙色虚线 + 箭头。连接 action 节点右侧监听 handle 到 CallbackCard。可通过 Toolbar 开关切换显隐。

---

## 6. Store 架构

### 6.1 flowStore — 业务数据

源文件：`cmd/web/src/components/FlowEditor/store/flowStore.ts`

**核心状态**：

```typescript
interface FlowState {
  // 业务数据
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
  defaultDelayMs: number;

  // React Flow 镜像
  rfNodes: Node[];
  rfEdges: Edge[];
  layout: FlowLayout;

  // 派生数据
  callbackRefCount: Record<string, number>;
  nodesByCallback: Record<string, string[]>;
  issuesByNodeId: Record<string, ValidationIssue[]>;
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
  hoveredCallback: string | null;
  activePanel: string | null;
  clipboard: { ... } | null;
  theme: 'light' | 'dark';
  debugMode: boolean;
  showListenEdges: boolean;
  // ...
}
```

- 仅 `theme` 持久化到 LocalStorage（`stressbot:theme`）
- `debugMode` 持久化到 LocalStorage（`stressbot:debugMode`）
- 其他状态一次会话内不持久化

### 6.3 undoRedo — 快照式撤销重做

源文件：`cmd/web/src/components/FlowEditor/store/undoRedo.ts`

基于 zundo 中间件封装，按需打快照（避免 hover 等高频操作写入 history）。

### 6.4 persistDraft — 编辑稿自动保存

源文件：`cmd/web/src/components/FlowEditor/store/persistDraft.ts`

- 监听 flowStore 变更 → debounce 300ms 写入 LocalStorage
- `beforeunload` 事件同步 flush
- 双份存储：`stressbot:flow:current`（TaskFlow）+ `stressbot:flow:layout`（FlowLayout）

### 6.5 flowManagerStore — 流程库

源文件：`cmd/web/src/components/FlowEditor/store/flowManagerStore.ts`

IndexedDB 存储（`stressbot-flows-manager`），支持多份命名流程快照：

```typescript
interface ManagedFlow {
  id: string;        // nanoid()
  name: string;
  flow: TaskFlow;
  layout: FlowLayout;
  updatedAt: number; // Date.now()
}
```

操作：`saveFlow` / `getFlow` / `deleteFlow` / `listFlows`

---

## 7. 编辑器系统

### 7.1 顶层布局

```
┌──────────────────────────────────────────────────────────────┐
│  Toolbar  [新建][打开][保存][导入][导出][预览][校验][Undo][Redo]│
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
- SequenceEditor / LoopEditor / BooleanEditor / WeightedEditor / WaitEditor / ActionEditor
- break / continue 仅显示 ID

**「还原本次修改」按钮**：打开抽屉时 `useRef` 记录深拷贝快照，编辑过程中激活，点击整体回退。

### 7.3 ActionEditor — 最复杂的编辑器

**顶层结构**：
- 动作名输入
- PatternSelector（14 种 pattern 下拉）
- 依据 Pattern 动态渲染对应表单
- ListenRefsTable（可折叠面板）

**各 Pattern 的字段矩阵**：

| Pattern | service | route | c2sProto | s2cProto | bindings | store | 其他 |
|---|---|---|---|---|---|---|---|
| tcpSend | 可选 | 可选 | 可选 | - | 可选 | - | - |
| tcpRequest | 可选 | 可选 | 可选 | 可选 | 可选 | 可选 | timeout |
| tcpConnect | - | - | - | - | - | - | address |
| tcpClose | - | - | - | - | - | - | - |
| tcpListen | 可选 | 可选 | - | 可选 | - | 可选 | timeout, pollMs |
| udpSend | 可选 | 可选 | 可选 | - | 可选 | - | - |
| udpRequest | 可选 | 可选 | 可选 | 可选 | 可选 | 可选 | timeout |
| udpConnect | - | - | - | - | - | - | address |
| udpClose | - | - | - | - | - | - | - |
| udpListen | 可选 | 可选 | - | 可选 | - | 可选 | timeout, pollMs |
| httpRequest | - | - | - | - | - | - | url, method, contentType |
| setState | - | - | - | - | 可选 | - | - |
| clearState | - | - | - | - | - | - | keys |
| lua | - | - | - | - | - | - | script |

### 7.4 BindingsTable — 字段绑定列表

表格列：`field` / `type` / 详情 / `storeAs` / `wrap` / `optional` / 操作

- `field` 列带自动补全（来源 c2sProto 解析后的字段名）
- `type` 列下拉 16 种 BindingType
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
| randomString | length, charset | 长度 + 字符集别名选择；支持自定义字符集字面量 |
| randomPick | values | 数组编辑器 |
| randomPickN | values, count | 同上 + count |

`randomString.charset` 支持 `lower`（a-z）、`upper`（A-Z）、`alpha`（a-zA-Z）、`numeric`（0-9）、`alphanum`（a-zA-Z0-9，默认）别名，也可以输入自定义字符集字面量，如 `ABC-123_`。
| randomPickMap | values, keySource | key-values 表格 |
| randomExclude | values/source, excludeSource | 来源 + 排除源 |
| stateMapKey | source | 仅 source |
| stateMapValue | source, path?, filters? | 同 stateRandom |
| stateFirst | source, path?, filters? | 同 state |
| listSize | source | 仅 source |

### 7.6 LuaForm — 脚本编辑器

基于 Monaco Editor，支持三种 mode：

| mode | 入口函数 | 返回值 | Lint 检查 |
|---|---|---|---|
| `action` | `function execute(r)` | `return code` | 检查 execute 存在 |
| `boolean` | `function execute(r)` | `return true / false` | 检查 execute 存在 |
| `callback` | `function onMessage(r, msg)` | 无返回值 | 检查 onMessage 存在 |

功能：
- 导入/导出 .lua 文件
- Lua API 自动补全（`luaApiSpec.ts`：7 个模块 68 个函数）
- Web Worker 语法检查（`luaSyntaxWorker.ts`）

---

## 8. Listen 子系统

### 8.1 三态模型

每个 ListenDef 有三种形态：

| 形态 | 数据特征 | 行为 |
|---|---|---|
| silent | `{}`（空对象） | 收到推送即丢弃 |
| declarative | `{s2cProto, store}` | 按 proto 反序列化，挑字段写 state |
| lua | `{script}` （可选 s2cProto） | 执行 Lua function onMessage(r, msg) |

### 8.2 视觉布局

**画布主区 + 事件区分区**：

- 主区：控制流 DAG
- 事件区（右侧 25%）：CallbackCard 自由排布
- ListenEdge：橙色虚线连接 action → CallbackCard

**三层视图**：

| 层 | 形态 | 状态 |
|---|---|---|
| CallbackPanel（右侧抽屉） | 列表 + 编辑 | 已实现 |
| CallbackCard + ListenEdge | 画布浮动卡片 + 连线 | 已实现 |
| 双向悬停高亮 | 画布交互 | 单向已实现（callback → 节点） |

### 8.3 CallbackPanel — 主编辑面板

右侧抽屉式，从 Toolbar 唤出：
- 列表：名称 + 形态徽章 + 引用计数
- 引用计数为 0 标橙色警告（孤儿）
- 悬停某项 → 主画布上注册它的 action 节点亮边
- 反查展开 BackrefList

### 8.4 CallbackEditor — 详情编辑

Tab 切换形态：silent / declarative / lua。

**silent 形态**：清空 s2cProto / store / script。

**declarative 形态**：
- s2cProto 选择（ProtoBrowser）
- StoreTable（与 ActionEditor 共用组件）

**lua 形态**：
- script 选择（新建/已存在）
- Monaco 编辑器（mode='callback'）

### 8.5 ListenRefsTable — 监听引用编辑

在 ActionEditor 底部以可折叠面板形式嵌入：

| 列 | 控件 |
|---|---|
| route | RouteEditor（不透明 JSON 单 Input） |
| server | 纯文本 Input |
| listen | 下拉（全局 listens）+ 新建 + 跳转 + null（静默丢弃） |
| 形态 | 只读徽章（实时反映 listen 当前形态） |

### 8.6 RouteEditor — 不透明 JSON 输入

单 Input 接受任意 JSON。行为：
- 合法 JSON → 解析为对象写回 store
- 非法 JSON → 原样以字符串保留（避免抖动）
- 空字符串 → 写回 undefined

同一组件被 DeclarativeForm / ListenRefsTable / BackrefList 三处共用。

### 8.7 引用图与校验

`refsGraph.ts` 提供引用图计算：

```typescript
interface RefsGraph {
  nodeToRefs: Map<string, ListenRef[]>;
  callbackToRefs: Map<string, Array<{ nodeId; refIndex; ref }>>;
  duplicateRegisters: Array<{ server; routeKey; refs }>;
}
```

---

## 9. Proto 系统

### 9.1 ProtoLoader — 加载策略

源文件：`cmd/web/src/components/FlowEditor/proto/ProtoLoader.ts`

按优先级加载：

| 来源 | 适用阶段 | 实现方式 |
|---|---|---|
| A. Vite 中间件 `/conf/proto/` | 开发期默认 | fetch `/sbot/baseline/proto/index.json` 拿清单，并发 fetch 每个文件 |
| B. Admin 端点 `/sbot/baseline/proto/*` | 生产期 | 同上，但基线前缀指向 Admin |
| C. `import.meta.glob` 兜底 | 静态打包 | Vite `?raw` import 编译期嵌入 |

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

### 10.1 refsCheck — 引用校验

源文件：`cmd/web/src/components/FlowEditor/validation/refsCheck.ts`

**Error 级别**：

| 规则 | 说明 |
|---|---|
| next/body/trueNext/falseNext/options[].node 引用的节点不存在 | 引用合法性 |
| action 节点的 action 字段不存在于 actions 表 | 引用合法性 |
| listenRefs[].listen 引用不存在的 listen（null 例外） | 引用合法性 |
| loop 的 body 为空 | 必填 |
| weighted 至少 1 个 option | 必填 |
| declarative listen 的 s2cProto 不在 ProtoRegistry | proto 存在性 |
| lua listen 的 script 不存在 | 脚本存在性 |
| pattern=lua 的 action 的 script 必填 | 必填 |

**Warning 级别**：

| 规则 | 说明 |
|---|---|
| boolean 的 condition 为空 | 可能的逻辑遗漏 |
| callback 引用计数为 0（孤儿） | 未使用 |
| 不可达节点（从 start BFS 不到） | 死代码 |
| loop 至少需要 loopCount > 0 或 condition 或 breakCondition | 必填项至少一项 |

### 10.2 ValidationReport — 校验报告 UI

抽屉式面板，按 Error / Warning / Info 分级展示，点击每条跳转到对应节点。

---

## 11. IndexedDB 持久化

### 11.1 编辑稿（LocalStorage）

由 `persistDraft.ts` 管理，debounce 300ms + beforeunload flush：

| Key | 内容 |
|---|---|
| `stressbot:flow:current` | TaskFlow 业务数据 |
| `stressbot:flow:layout` | FlowLayout 画布布局 |
| `stressbot:flow:stash` | viewActive 前的草稿 stash |

### 11.2 流程库（IndexedDB）

| DB | Store | 内容 |
|---|---|---|
| `stressbot-flows-manager` | `data` | ManagedFlow（命名快照） |

### 11.3 模板库（IndexedDB）

| DB | Store | 内容 |
|---|---|---|
| `stressbot-editor` (actionLibrary) | — | ActionTemplate |
| `stressbot-editor` (callbackLibrary) | — | CallbackTemplate |

### 11.4 资源文件（IndexedDB）

| DB | Store | 内容 |
|---|---|---|
| `stressbot-resources-proto` | `data` | 用户上传的 .proto |
| `stressbot-resources-scripts` | `data` | 用户上传的 .lua |
| `stressbot-resources-adapter` | `data` | codec.lua / error.lua |

---

## 12. 集成方式

### 12.1 FlowEditorProps — 组件 API

```typescript
interface FlowEditorProps {
  initialFlow?: TaskFlow;
  initialLayout?: FlowLayout;
  autoLoadDefault?: boolean;        // 自动加载 conf/flow/flow.json
  metricsProvider?: MetricsProvider; // 监控数据注入
}
```

### 12.2 HomeShellInner — 单页编排

源文件：`cmd/web/src/pages/EditorPage.tsx`

```
HomeShellInner
├── RuntimeBar (注入到 FlowEditor topbarExtra)
├── FlowEditor (readOnly = mode != 'edit')
│   └── NodeShell + MetricsBadge ← metricsProvider
├── MonitorDock (6 Tabs，可折叠)
└── Drawers
    ├── ResourcesDrawer (proto/lua 资源)
    ├── HistoryModal (列表/详情/对比)
    ├── AgentsPanel (节点状态)
    ├── SystemTab (集群系统资源)
    ├── LogsTab (日志查看)
    └── NotepadTab (笔记)
```

### 12.3 RuntimeMode 状态机

由 `runtimeStore` 管理（详见 `frontend-api.md` §11）：

```
edit ──[startTask]──> running ──> finalReport ──[新建]──> edit
  └──[attachToActive]──> viewActive ──> finalReport
```

### 12.4 监控指标注入

`metricsBinding.buildNodeMetricsMap(snapshot, flow)` 将 StressSnapshot.actions[] 映射到节点 ID：
- 名字以 `callback:` 开头 → 虚拟节点 ID `__cb__<name>`
- 其它 → 匹配 `flow.nodes[i].action == metric.name`

`makeMetricsProvider(map)` 包装为 `(nodeId) => ActionMetric | undefined`，注入 `useMetricsStore`。

NodeShell 消费：
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
| skipCapacityCheck | false | true |

### 12.6 ActiveTaskGuardModal — 活动任务守卫

页面加载时检测 active 任务，引导用户选择"查看运行中"或"继续编辑"。

---

## 13. MonitorDock — 监控面板

源文件：`cmd/web/src/components/monitoring/MonitorDock.tsx`

底部停靠面板，6 个 Tab：

| Tab | 内容 | 数据源 |
|---|---|---|
| 大盘 | 4 张状态卡（机器人/连接/带宽/集群） | latestStress + latestSystem |
| 动作 | 每个 ActionMetric 一行 | latestStress.actions |
| 错误 | 跨动作错误聚合 | actions[].errors |
| 趋势 | 4 张 ECharts 折线图 | stressHistory / systemHistory（滑窗 60 点） |
| per-Agent | 每 Agent apdex/成功率/CPU/MEM | PerAgentMetrics + PerAgentSystem |
| 系统 | 集群 CPU/MEM/NET/Goroutine | latestSystem + agents |

可拖拽顶部把手调整高度（160~80vh），高度持久化到 LocalStorage。

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
- 按 next/body/trueNext/falseNext/options 生成对应类型的边
- 计算 callbackRefCount / nodesByCallback 派生数据
- CallbackCard 节点 key 为 `__cb__<name>`

### 14.3 dagreLayout — 自动布局

源文件：`cmd/web/src/components/FlowEditor/codec/dagreLayout.ts`

导入无 layout 文件的 flow.json 时，用 dagre 自动布局兜底。

---

## 15. 快捷键

| 快捷键 | 行为 |
|---|---|
| `Ctrl+S` | 保存（弹预览） |
| `Ctrl+Z / Ctrl+Y` | Undo / Redo |
| `Delete` | 删除选中节点/边 |
| `Ctrl+D` | 复制选中节点 |
| `Ctrl+F` | 节点搜索 |

---

## 16. 连接规则

| 源 | 合法目标 | 备注 |
|---|---|---|
| sequence next handle | 任意节点 target | next[] 追加 |
| loop body handle | 任意节点 target | 一对一 |
| boolean true/false handle | 任意节点 target | 一对一 |
| weighted option handle | 任意节点 target | 一对一 |
| wait / action 右侧 handle | 任意节点 target | 一对一 |
| action 监听 handle | CallbackCard | ListenEdge |

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

---

## 18. 基线同步机制

### 18.1 resourcesStore.syncResourcesFromBaseline()

对比 IDB 与基线（Admin 或 Vite 中间件）：
- 新增 → 自动写入 IDB
- 内容相同 → unchanged
- 内容不同 → 返回冲突列表
- 基线已删除 → 返回 removed 列表

用户通过 `applyConflictResolution(decisions)` 选择保留本地或采用基线。

### 18.2 scriptSync.syncFlowScriptsToIdb()

扫描 flow 引用的脚本名，对 IDB 中缺失的从基线拉取写入。已有脚本永不覆盖。

### 18.3 Lua 脚本基线版本清除

`SCRIPT_BASELINE_VERSION` 常量。浏览器加载时比对 LocalStorage 版本号，不匹配则一次性清空 scripts DB。

---

## 19. 后续扩展方向

1. 运行联调 — 完整的压测控制台
2. 图分组与折叠 — sequence/weighted 折叠为子流程
3. Diff 视图 — 两个 flow.json 互相对比
4. 协作模式 — 多人在线编辑
5. LSP 集成 — Lua 完整工具链
6. AI 辅助 — 自然语言生成流程图
7. ProtoIndex IDB 缓存 — 第二次进入加速到 < 200ms
