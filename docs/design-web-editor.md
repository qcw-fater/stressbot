# stressbot Web 可视化流程编辑器 — 详细设计方案

> **目标定位**：为 stressbot 压测工具构建一个类 UE 蓝图（Blueprint）的可视化流程编辑器，所见即所得地编排 `flow.json`。
>
> **本期范围**：仅完成前端可视化编辑能力，与后端的通信接口（生效、下发、运行控制、监控数据回传）作为占位接口预留，后续单独实施。

---

## 1. 设计目标与核心约束

### 1.1 核心目标

1. **类 UE 蓝图体验**：在画布上以"节点 + 连线"的方式编排流程，双击节点打开属性编辑器，拖拽针脚（pin）建立连接。
2. **完整覆盖现有 `flow.json`**：所有节点类型（`sequence / action / loop / boolean / weighted / wait / break / continue`）和所有 ActionDef pattern（11 种）必须能完全可视化编辑。
3. **声明式动作的字段编辑器自动生成**：基于 `conf/proto/*.proto` 解析结果，根据 `c2sProto` 字段自动渲染绑定（FieldBind）表单，不让用户手动记字段名。
4. **脚本式动作内嵌编辑器**：使用 Monaco（VS Code 同款引擎）编辑 Lua，提供基础语法高亮和导入/导出文件能力。
5. **动作节点可复用**：建立"动作模板库"，已编辑过的 Action 可保存到库中，跨流程拖拽复用。
6. **保存前可预览完整 JSON**：导出/保存前提供 JSON 预览面板，对生成结果一目了然，可直接拷贝。
7. **整体封装为可嵌入组件**：核心编辑器作为独立 React 组件 `<FlowEditor />` 暴露，便于后续接入到包含监控、机器人控制、运行日志等更大的 IDE 形态界面。
8. **预留监控数据展示位**：节点视图保留指标显示槽（counter / latency / 当前进入数等），后端监控接入后只需填数据，无需重做布局。

### 1.2 设计原则

| 原则 | 说明 |
|---|---|
| **以 `Node` 为画布基本单位** | 画布上每个图形节点 ↔ `flow.json` 中的一个节点（map[string]\*Node 的一个 entry）。 |
| **以 `Action` 为编辑基本单位** | `action` 节点上的 `action` 字段引用一个 ActionDef，ActionDef 才是真正的"业务原语"，可独立保存/复用。 |
| **节点单一职责** | 视觉表现严格对应 `redesign-flow-nodes.md` 中的语义：`sequence` 是顺序容器，`loop` 仅持单 body，`weighted` 持 options 列表。 |
| **callback 与 action 平级** | callback 是事件入口，独立子系统（详见 §8），不在主画布上以节点形式存在；通过双向悬停高亮 / 跳转 / 反查与画布联动。 |
| **数据 ↔ 视图严格双向同步** | 任何一次保存的 `flow.json` 都能被无损反序列化、还原为视觉布局。 |
| **零后端假设** | 本期所有持久化（动作模板库、布局、当前编辑稿）都走 LocalStorage / IndexedDB / 本地文件 import-export，不依赖后端接口。 |
| **类型安全** | 全 TypeScript，节点 / 动作 / 绑定的 TS 类型与 Go 结构体一一对应（手工维护一份 `types.ts` 对照表，并在 schema 校验中复用）。 |

### 1.3 非目标（明确不做的事）

- 不做后端通信、不做服务下发、不做 Lua/proto 服务端校验（这些都属于"后端联调"阶段，本期只留接口）。
- 不做团队协作、版本控制、多人在线编辑（如有需要，后续基于本组件再叠加）。
- 不做完整 IDE 级 Lua 工具链（不集成 LSP，仅做语法高亮 + 简单 lint 占位）。
- 本期不展示真实压测指标（仅预留 UI 槽位）。

---

## 2. 技术选型

### 2.1 核心栈

| 关注点 | 选型 | 理由 |
|---|---|---|
| 框架 | **React 18 + TypeScript** | React Flow 生态最丰富；TS 强类型对节点/字段映射友好。 |
| 构建 | **Vite** | 启动快、HMR 友好；产物可作为静态资源或库分发。 |
| 流程画布 | **`@xyflow/react`（React Flow v12）** | 业界主流流程编辑库；支持自定义节点 / 自定义边 / 子流程 / mini-map / undo-redo；MIT 协议；可二次封装。 |
| UI 组件库 | **Ant Design v5** | 中文项目首选；表单 / Modal / Tree / Tabs / Drawer 齐全；与 React Flow 兼容良好。 |
| 状态管理 | **Zustand** | React Flow 官方文档推荐搭档；体积极小（<3KB）；非常适合存放图状态。 |
| Lua 编辑器 | **Monaco Editor**（`@monaco-editor/react`） | 与 VS Code 同款；自带 Lua 高亮；亦可作为 JSON/Proto 预览只读视图。 |
| Proto 解析 | **`protobufjs`** | 纯 JS 解析 `.proto` 文件，能拿到 message / field / type / enum 完整 AST，用于驱动声明式动作的字段编辑器。 |
| 表单引擎 | **Ant Design Form + 自定义 schema** | FieldBind 是高度结构化的，自定义 schema renderer 比 react-jsonschema-form 更可控。 |
| ID 生成 | **`nanoid`** | 用于新建节点时生成默认节点 ID（用户可改）。 |
| JSON 校验 | （计划中）`ajv` | 原计划保存前用 JSON Schema 兜底校验，目前已通过 `validation/refsCheck.ts` 实现等价的引用合法性 + 业务规则校验，覆盖 schema 校验需求；ajv 依赖暂不引入，待后续真正需要 schema 兜底再补。 |
| 持久化 | **`idb-keyval`**（IndexedDB 简易封装）+ **LocalStorage** | LocalStorage 存当前编辑稿；IndexedDB 存动作模板库（可能较大）。 |

### 2.2 备选方案与放弃理由

| 方案 | 放弃原因 |
|---|---|
| Vue 3 + Vue Flow | 团队/生态权衡：React Flow 更成熟，文档更完善，自定义节点/handle 控制力更强。 |
| Rete.js | 偏向纯计算图（信号节点风格），改造为"流程节点"成本反而高。 |
| Drawflow / jsPlumb | 自定义节点 / 子流程支持弱，不适合 UE 蓝图风格的复杂节点。 |
| Redux Toolkit | 对图状态来说过重，Zustand 更贴合。 |
| 自研画布 | 性能/交互调优成本极高（缩放、对齐、连线动画、minimap），收益有限。 |

### 2.3 React Flow v12 关键能力对齐

| 能力 | 用途 |
|---|---|
| 自定义节点（`nodeTypes`） | 为 7 类节点各定义一个 React 组件（带不同视觉/handle 布局）。 |
| 自定义边（`edgeTypes`） | 为"顺序边 / 真假分支边 / 权重边"配色、加箭头、贴标签。 |
| Handles（连接针脚） | UE 蓝图风格：左入右出，loop body 下出，boolean 双出（true/false）。 |
| `useReactFlow` hook | 编程式增删节点/边、聚焦节点。 |
| `MiniMap`、`Controls`、`Background` | 直接用官方组件；点阵网格背景。 |
| 子流程 / 分组（Group node） | 用于实现 sequence / weighted "容器"视觉。 |
| Undo/Redo | 自行接入 zundo 或在 store 层做命令栈。 |

---

## 3. 目录结构

`web/` 目录与 `cmd / engine / robot / network / monitor` 同级；下文结构反映**当前实际实现**：

```
e:/jump/Server-Jump/stressbot/
├── web/
│   ├── package.json
│   ├── vite.config.ts                 # 含 /conf/proto/ /conf/scripts/ 中间件，从仓库 conf/ 直读 proto 与脚本
│   ├── tsconfig.json
│   ├── index.html
│   ├── src/
│   │   ├── main.tsx                   # 应用入口（demo 用）
│   │   ├── App.tsx                    # demo 容器（顶栏 + EditorPage）
│   │   ├── vite-env.d.ts
│   │   │
│   │   ├── components/
│   │   │   └── FlowEditor/            # ★ 核心组件（对外封装单元）
│   │   │       ├── index.tsx                # FlowEditor 组件入口（FlowEditorProps）
│   │   │       ├── FlowCanvas.tsx           # React Flow 画布 + 右键菜单 + 拖放接收
│   │   │       │
│   │   │       ├── store/
│   │   │       │   ├── flowStore.ts         # 业务数据 + RF 镜像 + 派生数据（callbackRefCount / nodesByCallback / issuesByNodeId）
│   │   │       │   ├── editorStore.ts       # UI 状态（选中 / 悬停 / 主题 / 剪贴板 / 各开关）
│   │   │       │   ├── undoRedo.ts          # 自定义快照式 undo/redo
│   │   │       │   └── persistDraft.ts      # LocalStorage 编辑稿（debounce 300ms + beforeunload flush）
│   │   │       │
│   │   │       ├── nodes/                   # 自定义节点视觉
│   │   │       │   ├── ActionNode.tsx / SequenceNode.tsx / LoopNode.tsx / BooleanNode.tsx
│   │   │       │   ├── WeightedNode.tsx / WaitNode.tsx / BreakNode.tsx / ContinueNode.tsx
│   │   │       │   ├── shared/
│   │   │       │   │   ├── NodeShell.tsx    # 通用外框（标题/描述/状态徽章/指标槽）
│   │   │       │   │   └── MetricsBadge.tsx
│   │   │       │   └── registry.ts          # nodeTypes 注册中心
│   │   │       │
│   │   │       ├── edges/
│   │   │       │   ├── SeqEdge.tsx / BranchEdge.tsx / WeightEdge.tsx
│   │   │       │   ├── LoopBodyEdge.tsx / ListenEdge.tsx
│   │   │       │   ├── edgeStyle.ts         # 颜色 / 粗细 / 样式 token 的统一来源
│   │   │       │   └── registry.ts
│   │   │       │
│   │   │       ├── editors/                 # 节点属性编辑器
│   │   │       │   ├── NodeEditorDrawer.tsx # 抽屉式调度器（按 node.type 路由）+「还原本次修改」
│   │   │       │   ├── SequenceEditor.tsx / LoopEditor.tsx / BooleanEditor.tsx
│   │   │       │   ├── WeightedEditor.tsx / WaitEditor.tsx
│   │   │       │   ├── ActionEditor/
│   │   │       │   │   ├── index.tsx        # 总控
│   │   │       │   │   ├── PatternSelector.tsx
│   │   │       │   │   ├── DeclarativeForm.tsx       # 声明式（含 RouteEditor 复用）
│   │   │       │   │   ├── LuaForm.tsx               # 脚本式（Monaco，主题随 editorStore.theme）
│   │   │       │   │   ├── BindingsTable.tsx
│   │   │       │   │   ├── BindingTypeForm.tsx
│   │   │       │   │   ├── StoreTable.tsx            # 与 CallbackEditor 共用
│   │   │       │   │   └── ProtoFieldPicker.tsx
│   │   │       │   └── shared/
│   │   │       │       ├── ConditionInput.tsx        # 含 lua: / state: / plain 三态说明
│   │   │       │       ├── DelayInput.tsx
│   │   │       │       └── NodeIdSelect.tsx
│   │   │       │
│   │   │       ├── panels/
│   │   │       │   ├── NodePalette.tsx     # 左侧：节点类型 + Action 模板 + Callback 模板（独立滚动区）
│   │   │       │   └── Toolbar.tsx         # 顶部工具栏（导入/导出/预览/校验/Codec/主题/Undo-Redo）
│   │   │       │
│   │   │       ├── callbacks/              # ★ Callback 子系统（详见 §8）
│   │   │       │   ├── CallbackPanel.tsx       # 右侧抽屉：列表 + 三态徽章 + 引用计数（孤儿警告）
│   │   │       │   ├── CallbackEditor.tsx      # 单 callback 详情（silent/declarative/lua Tab 切换）
│   │   │       │   ├── CallbackCard.tsx        # 画布事件区浮动卡片（接收 ListenEdge）
│   │   │       │   ├── ListenRefsTable.tsx     # action.listenCallbacks 表格（嵌入 ActionEditor）
│   │   │       │   ├── RouteEditor.tsx         # 不透明 JSON 路由单 Input（详见 §8.7）
│   │   │       │   ├── BackrefList.tsx         # 反查：可直接编辑 server / route / 删除引用
│   │   │       │   ├── refsGraph.ts            # 引用图计算（仅 callbackToRefs / duplicateRegisters 等用途）
│   │   │       │   └── callbackKindStyle.ts    # 三态颜色 / 短文案 单一来源
│   │   │       │
│   │   │       ├── library/                # 模板库（IndexedDB）
│   │   │       │   ├── templateStore.ts        # idb-keyval 封装
│   │   │       │   ├── SaveTemplateButton.tsx
│   │   │       │   └── TemplateEditorDrawer.tsx
│   │   │       │
│   │   │       ├── proto/
│   │   │       │   ├── ProtoLoader.ts          # 优先 fetch /conf/proto/index.json，失败回退 import.meta.glob
│   │   │       │   ├── ProtoRegistry.ts        # message / field / enum 索引
│   │   │       │   ├── ProtoBrowser.tsx        # 平铺列表（按 fullName 字典序），不再树形展开
│   │   │       │   └── protoStore.ts           # 加载状态 zustand store（idle/loading/ok/error + hash）
│   │   │       │
│   │   │       ├── adapter/
│   │   │       │   └── CodecAdapterDrawer.tsx  # codec.lua 编辑器（LocalStorage 持久化，Monaco 主题感知）
│   │   │       │
│   │   │       ├── preview/
│   │   │       │   └── JsonPreviewModal.tsx    # 完整 flow.json 预览（Monaco 只读，主题感知）
│   │   │       │
│   │   │       ├── validation/
│   │   │       │   ├── refsCheck.ts            # 节点 / 动作 / callback 引用 + 必填字段校验
│   │   │       │   ├── refsCheck.test.ts
│   │   │       │   └── ValidationReport.tsx    # 报告 UI（按节点跳转）
│   │   │       │
│   │   │       ├── codec/
│   │   │       │   ├── flowToJson.ts           # store → TaskFlow（清理空字段、保留 description）
│   │   │       │   ├── jsonToFlow.ts           # TaskFlow → rfNodes/rfEdges + 派生 callbackRefCount/nodesByCallback
│   │   │       │   ├── dagreLayout.ts          # 自动布局
│   │   │       │   └── codec.test.ts
│   │   │       │
│   │   │       ├── styles/
│   │   │       │   └── inlineStyles.ts         # monoCellStyle 等共享 inline style 常量
│   │   │       │
│   │   │       └── utils/
│   │   │           └── nodeIdGen.ts            # 节点 ID 生成 / 唯一性
│   │   │
│   │   ├── pages/
│   │   │   └── EditorPage.tsx                  # 全屏编辑器 demo
│   │   │
│   │   ├── styles/
│   │   │   ├── global.css
│   │   │   └── tokens.css                      # 设计 token（颜色 / 间距 / 阴影 / 主题切换变量）
│   │   │
│   │   └── types/
│   │       ├── flow.ts                         # TaskFlow / FlowNode（含 description）/ ListenRef
│   │       ├── action.ts                       # ActionDef / FieldBind / FilterDef / StoreMapping
│   │       ├── callback.ts                     # CallbackDef + classifyCallback（按字段存在性判别）
│   │       ├── editor.ts                       # NodeLayoutMeta（仅 x/y）/ FlowLayout / emptyFlowLayout
│   │       └── proto.ts                        # ProtoMessage / ProtoField / ProtoEnum
│   │
│   └── public/
└── docs/
    └── design-web-editor.md  ← 本文档
```

**与早期草案的差异**（保留以方便未来读者）：
- 原计划 `store.ts / selectors.ts / commands.ts` 单文件 → 现拆为 `store/flowStore.ts + editorStore.ts + undoRedo.ts + persistDraft.ts`，业务数据与 UI 状态分离（详见 §10.1）。
- 原计划 `panels/ActionLibrary.tsx + callbacks/CallbackLibrary.tsx` 双独立抽屉 → 现合并到 `panels/NodePalette.tsx`，模板区采用上下两个独立滚动列表（Action / Callback）。
- 原计划 `lua/ + persistence/ + api/ + schema/ + monitor/` 多文件夹 → 实际实现把 Lua 编辑就近放在 `editors/ActionEditor/LuaForm.tsx`，持久化合并到 `store/persistDraft.ts`，schema 校验由 `validation/refsCheck.ts` 覆盖（无 ajv），api 与 monitor 暂未落盘文件。

> 严禁污染 Go 代码区：所有前端文件只在 `web/` 下；当前阶段不在 Go 端引入 web 静态资源 embed。

---

## 4. 数据模型（TypeScript ↔ Go 对照）

### 4.1 与 `engine/flow.go` 一一对应

为防止前后端漂移，TS 类型严格对齐 `engine/flow.go` 的结构体。建议做法：

- 在 `web/src/components/FlowEditor/types/flow.ts` 中**手工维护**一份 TS 接口（不引入 codegen，避免增加构建复杂度）。
- 单测中加入"采样若干现网 flow.json，使用 ajv 校验通过"的回归用例。

```ts
// types/flow.ts
export type NodeType =
  | 'sequence' | 'action' | 'loop' | 'boolean'
  | 'weighted' | 'wait' | 'break' | 'continue';

export interface WeightedOption {
  node: string;
  weight: number;
}

export interface ListenRef {
  route: unknown;             // 透明传给 adapter
  server: string;             // "tcp:logic" | "udp:battle"
  callback: string | null;    // 引用 callbacks 名；null = 静默消费
}

export interface FlowNode {
  type: NodeType;

  description?: string;        // 通用：节点注释（写回 flow.json，编辑器节点头部展示一行）

  next?: string[];             // sequence
  body?: string;               // loop
  loopCount?: number;          // loop
  condition?: string;          // loop / boolean
  breakCondition?: string;     // loop
  trueNext?: string;           // boolean
  falseNext?: string;          // boolean
  action?: string;             // action
  breakOff?: boolean;          // action
  listenCallbacks?: ListenRef[]; // action
  options?: WeightedOption[];  // weighted
  waitMs?: number;             // wait
  delayMs?: number;            // action / boolean
}

export interface TaskFlow {
  defaultDelayMs?: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  callbacks: Record<string, CallbackDef>;
}
```

```ts
// types/action.ts
export type ActionPattern =
  | 'tcpSend' | 'tcpRequest' | 'lua'
  | 'connect' | 'connectUDP' | 'exchangeKey'
  | 'close' | 'clearState' | 'udpSendProto'
  | 'waitListen' | 'setState';

export type BindingType =
  | 'fixed' | 'state' | 'stateFirst'
  | 'stateRandom' | 'stateRandomN'
  | 'stateMapKey' | 'stateMapValue'
  | 'randomPick' | 'randomPickN' | 'randomPickMap'
  | 'randomExclude' | 'randomInt' | 'randomBool'
  | 'randomString' | 'listSize'
  | 'nested' | 'nestedList';

export interface FilterDef { path: string; op: string; value?: unknown; source?: string; }
export interface StoreMapping { field?: string; path?: string; setter: string; }

export interface FieldBind {
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
  length?: number;
  count?: number;
  charset?: string;
  excludeSource?: string;
  optional?: boolean;
  wrap?: boolean;
  message?: string;
  bindings?: FieldBind[];
  storeAs?: string;
  keySource?: string;
  items?: FieldBind[];
}

export interface ActionDef {
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
  target?: 'tcp' | 'udp';
  keys?: string[];
  optional?: boolean;
  secretArg?: string;
}
```

### 4.2 视图扩展元数据

`flow.json` 本身不含位置信息。我们采取 **"姊妹文件 + 兜底自动布局"** 策略：

- 主产物：`flow.json`（业务数据，引擎直接消费）
- 副产物：`flow.layout.json`（仅前端使用，可选）

实际实现（已收敛）：

```ts
// types/editor.ts
export interface NodeLayoutMeta {
  x: number;
  y: number;
}

// flow.json 之外的视觉副产物，独立存档为 flow.layout.json
export interface FlowLayout {
  // CallbackCard 节点也存在这里，key 为 `__cb__<callbackName>`，不再单独维护 callbackPositions 字段
  nodePositions: Record<string, NodeLayoutMeta>;
  // Toolbar 中各开关状态（目前仅 showListenEdges 持久化）
  showListenEdges?: boolean;
}
```

- 早期草案中的 `collapsed / comment / groups / callbackPositions` 字段均**无人读写已删除**：节点折叠未实现，节点备注改由业务字段 `FlowNode.description` 承担（参与 flow.json 序列化），CallbackCard 的位置统一走 `nodePositions`，分组功能未落地（保留在 §17 路线图）。
- 仅在导出时一并落盘 `flow.layout.json`；`flow.json` 保持纯净。
- 导入只有 `flow.json` 时，用 `dagre` 自动布局算法兜底。

业务数据上，`FlowNode` 新增了 `description?: string` 字段（参与 flow.json 编解码），用于在节点头部展示一行注释；模板入库时自动取该字段作为说明，无需额外填写。

---

## 5. 节点视觉设计（蓝图风格）

### 5.1 通用规则

每个节点都是一张"卡片"：

```
┌──────────────────────────────────────┐
│  [type icon] NodeId         [⋯]     │  ← 头部（标题栏）
│                                       │
│  ── 类型专属内容（核心）             │  ← 中部
│                                       │
│  〔指标徽章占位〕                    │  ← 底部（监控槽）
└──────────────────────────────────────┘
```

- 头部：左侧为类型图标 + 节点 ID；右侧为操作菜单（删除 / 重命名 / 转模板）。
- 中部：每种类型显示其核心字段摘要（详细编辑走 Modal）。
- 底部：固定保留 32px 高度的"监控槽"（本期为空，后续监控上线时填入"当前进入数 / 平均耗时 / 失败率"）。

### 5.2 各类型节点视觉

| 类型 | 外形 / 颜色（边框/标题） | Handle 布局 | 中部摘要 |
|---|---|---|---|
| `sequence` | 矩形 / 蓝色 #1890ff | 左入（target）+ 右出（source，每个 next 一个 handle，垂直排列） | 子节点列表（拖拽排序） |
| `action` | 矩形 / 蓝紫 #597ef7 | 左入 + 右出（单 handle）+ **右侧"📡 监听"专用 handle 区** | `action 名` + `pattern 标签`，下方显示 listenCallbacks 数量徽章 |
| `loop` | 矩形带回环装饰 / 绿色 #52c41a | **左入 + 下出（body 唯一）**；body 跑完后由外层 sequence 的下一个 handle 接管，loop 自身不出 exit | `count`、`condition`、`breakCondition` 摘要 |
| `boolean` | **菱形** / 黄色 #fadb14 | 左入 + 右出 true（绿）+ 右出 false（红） | `condition` 摘要 |
| `weighted` | 矩形 / 紫色 #722ed1 | 左入 + 右出每个 option 一个 handle，旁标 weight | option 列表 + **内嵌横向条形图**（每行 `████░░ 40%` 直观显示权重比例） |
| `wait` | 矩形（窄）/ 红色 #f5222d | 左入 + 右出 | `waitMs` 数值 + 秒数换算提示 |
| `break` | 矩形（小）/ 橙色 #fa8c16 | 左入（无出） | "break" 字样 |
| `continue` | 矩形（小）/ 青色 #13c2c2 | 左入（无出） | "continue" 字样 |

> 配色统一采用 **Ant Design 5 调色板**（`@ant-design/colors`），便于与 antd 组件 / Tag / 主题 token 自动对齐；浅色背景 = 同色 `-1`/`-2` 阶（如 `sequence` 浅底用 `#bae0ff`），选中态用 `-7`/`-8` 阶加深边框。所有色值统一收口在 `web/src/styles/tokens.css`，禁止在节点组件内硬编码十六进制。

**Loop 设计澄清**：循环节点只有一个出口 handle（指向 body 子图入口）。当 body 子图执行结束、`loopCount` 用尽或 `breakCondition` 命中时，引擎自动回到外层 sequence 的"下一个 next"。视觉上不画"exit"是为了和引擎语义对齐——loop 没有"退出后"的接续节点概念，接续是 sequence 的责任。

**Action 节点的右侧监听区**：除了顶部/底部的常规控制流 handle，action 节点右侧增加一组监听专用 handle（仅当 `listenCallbacks.length > 0` 时渲染），从这里引出**橙色虚线 `ListenEdge`** 指向画布事件区的 `CallbackCard`（详见 §8.3）。

**Weighted 节点的条形图示例**：

```
┌───────────────────────────────┐
│ 🎲 businessWeight             │
├───────────────────────────────┤
│ ████████░░  normalModel  40%  │ ──▶ option handle
│ ████░░░░░░  lobbyOp      20%  │ ──▶
│ ███░░░░░░░  shop         15%  │ ──▶
│ ██░░░░░░░░  lottery      10%  │ ──▶
│ ███░░░░░░░  others       15%  │ ──▶
└───────────────────────────────┘
```

**特别说明：sequence 和 weighted 是"显式连线编排"还是"内嵌容器"？**

我们采用 **显式连线** + **可选分组框** 双轨制（受 UE 蓝图启发）：

- **主表达**：`sequence.next` 通过节点上的多个 source handle 显式连出，连到子节点的 target handle，画布上能看到清晰的拓扑。
- **辅助分组**：用户可框选一组节点，放入"Group"虚拟容器（仅视觉），便于折叠和导航。
- 这样既保留了 UE 蓝图的视觉表达力，又不会出现"动作节点必须放进框里"的强制层级（避免 `sequence` 退化为不能复用的子图）。

> 用户在文档中提到的"sequence 是个框，可以把动作拖进去"思路也可行，但带来嵌套坐标系问题（子节点位置相对父节点）。本期先采用显式连线方案，后期可作为视图模式（"扁平 / 容器"切换）扩展。

### 5.3 连接规则（合法性约束）

| 源 | 合法目标 | 备注 |
|---|---|---|
| `sequence` 的某个 next handle | 任意节点 target | 连接后，`next: []` 中追加目标 ID |
| `loop` 的 body handle | 任意节点 target | 一对一，重连即替换；loop 没有 exit handle |
| `boolean` 的 true / false handle | 任意节点 target | 一对一 |
| `weighted` 的 option handle | 任意节点 target | 一对一（每个 option 独占一个 handle） |
| `wait` / `action` 的 right handle | 任意节点 target | 一对一（顺序流出） |
| `action` 的"📡 监听" handle | 画布事件区中的 `CallbackCard` | 多对多，连接后在 action.listenCallbacks 中追加一条 ListenRef，并把 callback 绑定到目标 card |

**禁止**：

- 自环（节点连到自己）— 由 store 校验拒绝
- 重复连接（同一对 source-target）— React Flow 自动去重 + 提示
- `break / continue` 不允许连出 — 视觉上不渲染 source handle

校验失败时，连线被拒绝并 `message.warning`。

### 5.4 监控数据槽（预留）

实际实现：未注入 `metricsProvider` 或对应 nodeId 无指标时，`MetricsBadge` 直接 `return null`（不占用版面，避免空槽抖动）。原"预留 32px 空槽"的草案策略放弃，改为**有指标才渲染**的紧凑形态，更贴合"信息密度高的画布"。

```tsx
// nodes/shared/MetricsBadge.tsx
function MetricsBadge({ nodeId }: { nodeId: string }) {
  const metrics = useNodeMetrics(nodeId);
  if (!metrics) return null;
  return (
    <div className="metrics-slot">
      <Tag>进入: {metrics.entered}</Tag>
      <Tag>平均: {metrics.avgMs}ms</Tag>
      <Tag color={metrics.failRate > 0.05 ? 'red' : 'green'}>
        失败: {(metrics.failRate * 100).toFixed(1)}%
      </Tag>
    </div>
  );
}
```

`metricsProvider` 通过 `<FlowEditor metricsProvider={...} />` 注入；`docs/design-monitor.md` 落地后只需在外层连接 SSE/WebSocket 拉取 `monitor.NodeMetrics`，UI 自动点亮。

---

## 6. 编辑器交互设计

### 6.1 顶层布局

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  Toolbar  [新建][打开][保存][导入][导出][预览][校验][Undo][Redo]  ☑监听边  │
├──────────┬─────────────────────────────────────────────────┬─────────────────┤
│          │                                                 │                 │
│  Node    │                                                 │   Action        │
│  Palette │             FlowCanvas (React Flow)             │   Library       │
│ (左侧)   │                                                 │  (右侧抽屉)     │
│          │                                                 │                 │
│  - seq   │  +  ───  +  ─┬─  +                             │  搜索框         │
│  - act   │             │                                   │  ────           │
│  - loop  │             ▼                                   │  PlayerLogin    │
│  - bool  │             +                                   │  StartMatch     │
│  - wgt   │                                                 │  ConnectBattle  │
│  - wait  │                                                 │  ...            │
│          │                                                 │                 │
│          │     [MiniMap]    [Controls]   [Background]      │                 │
├──────────┴─────────────────────────────────────────────────┴─────────────────┤
│  Bottom  startNode: main    defaultDelayMs: 1000   Callbacks (12)            │
└──────────────────────────────────────────────────────────────────────────────┘
```

- **NodePalette**（左侧）：节点类型 + Action 模板 + Callback 模板**三段合一**，每段独立滚动 + 独立搜索；早期草案的"右侧独立 ActionLibrary 抽屉"已并入此面板，避免来回切换抽屉。
- **CallbackPanel / NodeEditorDrawer / ProtoBrowser / ValidationReport / JsonPreview / CodecAdapter** 等都通过 `editorStore.activePanel` 单一通道唤起，互斥显示。
- **MetadataPanel**（顶层 `startNode` / `defaultDelayMs` 等）尚未独立成面板；当前 `defaultDelayMs` 由 `flowStore` 直接管理并参与导出，UI 暂未提供编辑入口（如需修改，编辑 flow.json 后重新导入）。

### 6.2 关键交互流

**A. 新建节点**
1. 用户从 Node Palette 拖拽节点类型 → drop 到画布。
2. 系统：生成唯一 ID（`node_${nanoid(6)}`，可改）；插入 `nodes` map；React Flow 添加可视节点。

**B. 双击节点 → 打开编辑器**
1. `onNodeDoubleClick` → `NodeEditorDrawer`（抽屉式，非 Modal）按 `node.type` 路由到具体编辑器（SequenceEditor / LoopEditor / BooleanEditor / WeightedEditor / WaitEditor / ActionEditor / break · continue 仅 ID）。
2. Drawer 顶部固定 `[保存到模板]`（按节点类型自适应：action 节点保存 ActionDef，listen 关联了 callback 时联动入库）。
3. **「还原本次修改」按钮**：打开抽屉时通过 `useRef` 记录当前 node + 关联 ActionDef 的深拷贝快照；编辑过程中按钮激活，点击后整体回退到快照。这条比"逐字段撤销"更直观，覆盖"试着改了一通发现不对要全部丢弃"的常见场景，且与全局 Undo/Redo 相互独立。

**C. 拖拽连线**
1. `onConnect`（React Flow 回调）→ store 收到事件。
2. 校验 §5.3 中的合法性。
3. 写回对应字段（`next` push / `body` set / `trueNext` set / `options` push）。

**D. 删除连线**
1. 选中边 → Delete 键。
2. Store 反向写回（`next` 移除该 ID / 字段清空）。

**E. 保存**
1. 收集 store 的 nodes/edges/actions/callbacks → 反序列化为 `TaskFlow`。
2. ajv 校验 + 引用合法性检查（§10.4）。
3. **打开 JsonPreviewModal**，展示完整 JSON。
4. 用户确认 → 触发 `onSave(flowJson)` 回调（本期写 LocalStorage + 触发 download 选项）。

**F. 导入**
1. 点击工具栏 [导入] → File picker。
2. 解析 JSON → ajv 校验。
3. 若同时存在 `flow.layout.json`，用其位置；否则 dagre 自动布局。
4. 替换 store。

### 6.3 快捷键

| 快捷键 | 行为 |
|---|---|
| `Ctrl+S` | 保存（弹预览） |
| `Ctrl+Z / Ctrl+Y` | Undo / Redo |
| `Delete` | 删除选中节点/边 |
| `Ctrl+D` | 复制选中节点 |
| `双击空白` | 新建 sequence 节点 |
| `Ctrl+F` | 节点搜索（按 ID/action 名） |

---

## 7. ActionEditor — 最复杂的一块

ActionEditor 是整个编辑器中信息密度最高、动态性最强的部分。本节单独详细设计。

### 7.1 顶层结构

```
┌─ ActionEditor (Modal) ──────────────────────────────────────────┐
│                                                                  │
│  动作名（actions map 的 key）：[__________________]  [♀ 保存到库] │
│                                                                  │
│  Pattern: ( tcpSend | tcpRequest | lua | connect | connectUDP    │
│             | exchangeKey | close | clearState | udpSendProto    │
│             | waitListen | setState )                            │
│  ────────────────────────────────────────────────────────────── │
│                                                                  │
│  〔依据 Pattern 渲染对应表单〕                                  │
│                                                                  │
│  ────────────────────────────────────────────────────────────── │
│  [取消]                                       [应用] [应用并关闭] │
└──────────────────────────────────────────────────────────────────┘
```

### 7.2 各 Pattern 的字段矩阵

| Pattern | service | route | c2sProto | s2cProto | bindings | store | 其他 |
|---|---|---|---|---|---|---|---|
| `tcpSend` | ✅ | ✅ | ✅ | – | ✅ | – | – |
| `tcpRequest` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | – |
| `udpSendProto` | ✅ | ✅ | ✅ | – | ✅ | – | – |
| `connect` | ✅ | – | – | – | – | – | `address` |
| `connectUDP` | ✅ | – | – | – | – | – | `address` |
| `exchangeKey` | ✅ | (可选) | – | – | – | – | `secretArg` |
| `close` | ✅ | – | – | – | – | – | `target: tcp\|udp` |
| `clearState` | – | – | – | – | – | – | `keys: []string` |
| `waitListen` | ✅ | ✅ | – | ✅ | – | ✅ | `timeout`, `pollMs`, `optional` |
| `setState` | – | – | – | – | ✅ | – | – |
| `lua` | – | – | – | – | – | – | `script` |

`DeclarativeForm` 是一个 schema-driven 组件：根据 `pattern → 字段集合` 动态渲染表单。

### 7.3 BindingsTable（FieldBind 列表）

最关键的子组件。表格列：

| 列 | 控件 | 说明 |
|---|---|---|
| `field` | 输入 + 自动补全（来源：c2sProto 解析后的字段名） | 嵌套层级正确（`nested` 内的 field 来自父字段的 message 类型） |
| `type` | 下拉（17 种 BindingType） | 切换后右侧条件渲染（见下） |
| 详情 | 动态控件区 | 见 §7.4 |
| `storeAs` | 文本框 | 中间变量名 |
| `wrap` | 开关 | repeated 字段单值包装 |
| `optional` | 开关 | 跳过空字段 |
| 操作 | [↑] [↓] [×] | 排序 / 删除 |

底部：`[+ 添加绑定]` 按钮 + 模板下拉（"从库选择常用绑定模板"）。

### 7.4 BindingTypeForm — 每种 type 的子表单

按 17 种 BindingType 分别动态渲染：

| BindingType | 必填字段 | 控件设计 |
|---|---|---|
| `fixed` | `value` | 多类型输入：自动检测类型（number / string / boolean / array） |
| `state` | `source` | state key 输入框（自动补全：来自当前 flow 中所有出现过的 state key） |
| `stateFirst` / `stateRandom` | `source`、`path` (可选)、`filters` (可选) | source + path + 过滤器列表编辑器 |
| `stateRandomN` | `source`, `count`, `path`, `filters` | 同上 + count |
| `stateMapKey` | `source` | 仅 source |
| `stateMapValue` | `source`, `path`, `filters` | 同 stateRandom |
| `randomPick` | `values` | 数组编辑器（每行一个值，类型混合） |
| `randomPickN` | `values`, `count` | 同上 + count |
| `randomPickMap` | `values: [{key, values}]`, `keySource` | key-values 表格 + keySource |
| `randomExclude` | `values` 或 `source`, `excludeSource` | 来源二选一 + excludeSource |
| `randomInt` | `min`, `max` | 数字范围输入 |
| `randomBool` | – | 无字段 |
| `randomString` | `length`, `charset` | 长度 + 字符集下拉（alpha/numeric/alphanum/自定义） |
| `listSize` | `source` | 仅 source |
| `nested` | `message`, `bindings` | message 选择器（proto 浏览器）+ 嵌套 BindingsTable |
| `nestedList` | `items: [{message, bindings}]` | 多个 item，每个 item 是一个嵌套 BindingsTable |

**Filter 编辑器（filters）**：

```
┌─ Filter ────────────────────┐
│ path:    [_______________]  │
│ op:      [== / != / ... ▼]  │
│ value:   [_______________]  │  ← 二选一
│ source:  [_______________]  │
│ [删除]                       │
└──────────────────────────────┘
```

`op` 下拉枚举：`==/eq, !=/neq, >/gt, >=/gte, </lt, <=/lte, contains, in, timeWindow, dailyTimeWindow, notNil, isNil`。

### 7.5 ProtoFieldPicker — 字段自动补全

这是声明式编辑体验的核心。用户输入 `c2sProto = "Game.LoginPlayerC2S"` 后：

1. `ProtoRegistry.lookup("Game.LoginPlayerC2S")` 返回 `ProtoMessage`：
   ```ts
   interface ProtoMessage {
     fullName: string;
     fields: Array<{
       name: string;
       type: string;          // "int32" | "string" | "Game.SubMsg" | ...
       repeated: boolean;
       enumName?: string;     // 若是 enum 类型
       messageName?: string;  // 若是嵌套 message
     }>;
   }
   ```
2. `BindingsTable` 中 `field` 列变成下拉，候选项 = `fields.map(f => f.name)`。
3. 选中字段后，`type` 列默认值按字段类型推断（`int32` → `randomInt` 或 `fixed`，`string` → `randomString` 或 `state`）。
4. 当 binding type 是 `nested` 时，`field` 选择后自动联动设置 `message` 为该字段的 `messageName`，并递归展开子 BindingsTable。

### 7.6 LuaForm — 脚本式动作

```
┌─ ActionEditor (pattern=lua) ────────────────────────────────────┐
│  script: [post_login.lua    ▼ 选择已存在 / + 新建]              │
│  ┌─ Monaco Editor (language=lua) ──────────────────────────┐   │
│  │  function execute(r)                                     │   │
│  │      local val = robot.get("foo")                        │   │
│  │      ...                                                 │   │
│  │  end                                                     │   │
│  └──────────────────────────────────────────────────────────┘   │
│  [导入 .lua 文件]  [导出当前内容到文件]   [Lint] (占位)        │
└──────────────────────────────────────────────────────────────────┘
```

- **存储位置**：脚本内容存于"脚本库"（IndexedDB），`script` 字段只引用文件名。
- **导入**：用户选 `.lua` 本地文件 → 读取内容 → 存入脚本库 → 设置 `script` 字段。
- **导出**：当前编辑内容下载为 `.lua` 文件，便于落地到 `conf/scripts/`。
- **API 提示**（Monaco snippets）：内置 `robot.* / network.* / proto.* / utils.* / json.* / log.*` 的 completion 数据（来自 `flow-config` SKILL 中的 API 速查表）。
- **本期 Lint**：仅做匹配 `function execute(r)` / `function onMessage(r, msg)` 的存在性检查。
- **Lua 脚本返回值约定（v2 起）**：
  - **action 脚本**（`pattern: "lua"`）：`return code [, send_bytes, recv_bytes]`。`code` 仍是 0=成功；
    `send/recv` 由 lua API 多返回值给出（如 `network.tcp_send` 第 2 个返回值、`network.request`
    第 3、4 个返回值）。引擎层 `RunActionScript` 透传给 `monitor.RecordAction`，使 ActionsTab
    的 ↑avg / ↓avg 字节列对 lua 动作也能反映真实流量（旧的"全部 0"已修复）。
  - **boolean 脚本**（`condition: "lua:xxx.lua"` / loop `breakCondition`）：必须 `return true / false`。
    返回 number / nil / 其它类型直接报错（v2 起不再兼容 v1 的 0/1 约定）。
  - **callback 脚本**（`script: "listen_xxx.lua"`）：`onMessage(r, msg)` 仍无返回值约定。

### 7.7 ListenRefsTable（action 节点的 listenCallbacks 编辑器）

> 此表格在 ActionEditor 中作为 "注册监听" 折叠面板出现；其完整设计与跨引用导航见 **§8.6**。

为 `action` 节点编辑 `listenCallbacks: ListenRef[]` 字段。当前表格列：

| 列 | 控件 |
|---|---|
| `route` | `RouteEditor`：单 Input，接受不透明 JSON（如 `{"cmd":4,"act":10}`），与 ActionDef.route 完全一致 |
| `server` | 纯文本 Input（candidate 候选已不可靠，统一手输；占位提示 `如 tcp:logic`） |
| `callback` | 下拉（来自全局 callbacks）+ `[+ 新建]` + `[→ 跳转]` + `[null = 静默消费]` 选项 |
| 形态 | 只读徽章：silent / declarative / lua（实时反映被引用 callback 的当前形态，颜色见 §8） |

底部 `[+ 添加监听]` 与 `[+ 批量粘贴 JSON]` 入口（见 §8.6）。

callback 是一个独立子系统（不只是个字段值），完整设计见下一章。

---

## 8. Callback 子系统

### 8.1 概念回顾与三态判别

`callback` 与 `action` 在数据上平级（都是 `TaskFlow` 顶层 map），在语义上是**事件入口**：

```
action 节点执行 ConnectLogicTCP（含 listenCallbacks）
  ↓
引擎在该 service 的连接上注册一组持久监听
  ↓
服务端推送到达 → adapter 解码出 route key → 命中已注册条目 → 触发 CallbackDef
  ↓
按 callback 形态分派：
  - silent {}                             仅消费、不处理（占位放行 register）
  - declarative s2cProto + store          按 proto 反序列化，挑字段写 state
  - lua script                            调用 Lua function onMessage(r, msg)
```

**三态判别规则**（写在 `types/callback.ts` 中作为单一事实源）：

```ts
export type CallbackKind = 'silent' | 'declarative' | 'lua';

export function classifyCallback(cb: CallbackDef): CallbackKind {
  // 用"字段存在性"而非"truthy"做判别：用户从 silent 切换到 lua 时
  // script 会被初始化为 ''（空字符串），如果用 truthy 判别就会立即弹回 silent，
  // 在 CallbackEditor 中表现为"切到 lua 后立刻被改回静默"的 bug。
  if (cb.script !== undefined) return 'lua';
  if (cb.s2cProto !== undefined || cb.store !== undefined) return 'declarative';
  return 'silent';
}
```

判别在 UI 多处使用：列表徽章、ListenRefsTable 的形态列、校验报告分类等。`CallbackEditor` 的 `Tabs` 用 **本地 `selectedKind` state**（仅初始值由 `classifyCallback` 给出），而非每次 render 都从 `cb` 重算 —— 否则在用户已切到 lua、但 store 中字段仍未填写的中间态，会被反复 reclassify 回 silent。

三态颜色与短文案的单一来源：`callbacks/callbackKindStyle.ts`。所有 Tag 配色都从这里取，禁止散落到各组件内部。

### 8.2 数据模型补全

```ts
// types/callback.ts
export interface CallbackDef {
  s2cProto?: string;     // declarative: 解析推送的 proto 全名
  store?: StoreMapping[];// declarative: 字段 → state 映射（与 ActionDef 共用 StoreMapping）
  script?: string;       // lua: 引用脚本库内的 .lua 文件名
}
```

> ListenRef.callback 在 `flow.json` 中是 `string | null`：
> - 字符串：引用 `callbacks` map 中的某项
> - `null`：静默消费（既不分派 callback，也不报错）。这与 callback 自身是 `{}` 的"silent 形态"完全不同——前者是**根本不查找**，后者是**查找到一个空 callback**。两条路径在引擎里都允许，UI 上要明确区分。

UI 区分：
- ListenRef 的 callback 列下拉中显式列出 `(null) — 静默丢弃`
- 选中 `null` 时不显示形态徽章
- 选中具体 callback 时显示其当前形态徽章

### 8.3 视觉布局策略

callbacks 是 **全局事件入口**，没有"前置/后置"控制流语义，因此不参与主 DAG 拓扑。但要做到 UE 蓝图风格的"事件驱动可视化"，我们采用**画布主区 + 事件区分区** + **三层视图叠加**：

```
┌──────────────────────────────────────────────────────────────────┐
│  [Toolbar] 保存 | 预览 | 校验 | 切换"显式监听边"☑ | …            │
├────────────────────────────────────┬─────────────────────────────┤
│                                    │                             │
│   画布主区（控制流 DAG）             │   画布事件区（CallbackCards）│
│                                    │                             │
│    [start] → [seq] → [Connect]·····│····▶ ┌─📋 stateUpdate─┐    │
│                       │            │      └────────────────┘    │
│                       │            │     ┌─📜 frameData──┐       │
│                       └─📡 17────··│····▶│listen_frame.. │       │
│                                    │     └───────────────┘       │
│                                    │     ┌─🔇 matchPoll─┐        │
│                                    │     └──────────────┘        │
└────────────────────────────────────┴─────────────────────────────┘
```

| 视图层 | 形态 | 主要用途 | 必做 |
|---|---|---|---|
| **A. CallbackPanel（右侧抽屉）** | 列表 + 编辑 | 主要 CRUD 入口、批量操作、反查 | ★ 必做 |
| **B. CallbackCard + ListenEdge（画布事件区）** | 浮动卡片 + 橙色虚线连线 | UE 蓝图风格的"事件订阅"显式可视化；从 action 右侧📡 handle 拖到 card 即建立监听 | ★ 必做 |
| **C. 双向悬停高亮** | 画布交互 | 悬停 callback → 高亮所有 register 它的 action；反之亦然；与 ListenEdge 互补（前者瞬时定位，后者持久展示） | ★ 必做 |
| **D. CallbackGraphView（独立 Tab）** | 仅含 callbacks + actions 的引用关系图 | 大型流程的事件订阅拓扑审计 | 留作 §17 扩展 |

**关键设计点**：

1. **画布事件区**位于主区右侧（默认占 25% 宽度，可拖拽调整或折叠）。CallbackCard 在事件区内自由排布，可拖拽分组，但**不参与主 DAG 自动布局**。
2. **ListenEdge** 是橙色虚线（与控制流灰实线明确区分），`CallbackCard` 也使用橙色描边，让"事件订阅"视觉与"控制流"完全解耦。
3. **可选关闭显式监听边**：Toolbar 提供"☑ 显式监听边"开关。大型流程（如示例 `ConnectLogicTCP` 一次注册 17 条监听）打开后会有视觉过载，关闭后只在 action 节点显示 `📡 17` 徽章 + 双向悬停高亮。默认大流程（监听数 > 8）自动关闭，小流程默认打开。
4. **新建 callback 的两种入口**：从 ListenRefsTable 的 `[+ 新建]` 按钮（详见 §8.6），或者**直接在事件区右键 → 新建 CallbackCard**。两种入口产物相同，区别只在是否立即建立 ListenEdge。

### 8.4 CallbackPanel — 主编辑面板

右侧抽屉式，从 Toolbar 的 `[Callbacks]` 按钮唤出（也可从 ListenRefsTable 的 `[→ 跳转]` 直接定位）：

```
┌─ Callbacks (12) ─────────────────────────────┐
│  [+ 新建]   [▣ 从库导入]    [搜索 ____]      │
├──────────────────────────────────────────────┤
│  ▸ matchPoll              [silent]    (1)    │  ← (n) = 被 n 个 action 引用
│  ▸ teamStartMatch         [silent]    (1)    │
│  ▾ stateUpdate            [decl]      (1)    │
│      s2cProto: Game.MainStateUpdateS2C       │
│      store: 1 项 (status → playerStatus)     │
│      [编辑] [删除] [转模板] [反查 ▾]         │
│  ▸ frameData              [lua]       (1)    │
│      script: listen_frame_data.lua           │
│  ▸ heroUpdate             [silent]    (1)    │
│  ▸ unusedCallback         [silent]    (0) ⚠ │  ← 0 引用 = 警告（孤儿）
└──────────────────────────────────────────────┘
```

要点：
- 形态徽章 + 引用计数一目了然
- 引用计数为 0 的 callback 标橙色警告（导出仍允许，校验报告中提示）
- 鼠标悬停某项 → 主画布上 register 它的 action 节点亮边
- 点击 `[反查 ▾]` 展开 `BackrefList`：列出所有引用该 callback 的 `action 节点 ID + route + server`，可点击跳转到节点

### 8.5 CallbackEditor — 详情编辑

双击 CallbackPanel 列表项打开 Modal。顶部用 Tab 切换形态：

```
┌─ Edit Callback: stateUpdate ─────────────────────────────────┐
│  名称（map key）：[stateUpdate__________]                    │
│                                                               │
│  形态：[ 静默 silent  |  ★ 声明式 declarative  |  Lua 脚本 ] │
│  ──────────────────────────────────────────────────────────  │
│                                                               │
│  〔形态对应的表单〕                                          │
│                                                               │
│  ──────────────────────────────────────────────────────────  │
│  反向引用 (1)：                                              │
│   • node "ConnectLogicTCP"  route {cmd:2, act:18}  tcp:logic │
│  ──────────────────────────────────────────────────────────  │
│  [取消]  [保存到库]                  [应用] [应用并关闭]    │
└───────────────────────────────────────────────────────────────┘
```

#### 8.5.1 silent 形态表单

只展示一行说明：
```
此 callback 为静默消费占位（{}），收到推送即丢弃，不分派任何处理。
若要清除字段或写入 state，请切换为"声明式"。
```

切换到此形态时清空 `s2cProto / store / script` 字段。

#### 8.5.2 declarative 形态表单

```
s2cProto: [Game.MainStateUpdateS2C   ▾ 选择 (ProtoBrowser)]
store:
  ┌──────────────────────────────────────────────────────────┐
  │ field   │ path  │ setter         │ 操作                  │
  ├─────────┼───────┼────────────────┼──────────────────────┤
  │ status  │       │ playerStatus   │ [↑][↓][×]            │
  │ (空)    │       │ pushSnapshot   │ [↑][↓][×]            │
  └──────────────────────────────────────────────────────────┘
  [+ 添加映射]
```

特别说明：
- `field` 为空 = 写入整个 fieldMap（与 §3.5 StoreMapping 一致），UI 上显示为 `(整体)`
- StoreTable 直接复用 ActionEditor 的 `StoreTable.tsx`，零重复实现
- `s2cProto` 选择后，`field` 列变下拉（候选 = ProtoBrowser 解析的字段名）；`path` 列支持点号/索引/管道语法（与 ActionEditor 一致），并提供路径示例的快速插入按钮

#### 8.5.3 lua 形态表单

```
script: [listen_frame_data.lua  ▾ 已存在 / + 新建]
[导入 .lua]  [导出当前内容]   入口签名：onMessage(r, msg)
┌─ Monaco (lua) ──────────────────────────────────────────────┐
│ function onMessage(r, msg)                                  │
│     -- msg 类型：                                           │
│     --   有 s2cProto 时为 proto userdata；                  │
│     --   无 s2cProto 时为原始二进制字符串（如 frameData）   │
│     ...                                                      │
│ end                                                          │
└──────────────────────────────────────────────────────────────┘
```

关键差异（与 ActionEditor 的 LuaForm 区分）：

| 维度 | action 的 lua | boolean 的 lua（条件 / loop breakCondition） | callback 的 lua |
|---|---|---|---|
| 入口函数 | `function execute(r)` | `function execute(r)` | `function onMessage(r, msg)` |
| 返回值语义 | `return code [, send, recv]`（0=成功；后两个由 lua API 多返回值累加） | `return true / false`（其它类型直接报错） | 无返回值（约定不读） |
| 入参 | 仅 robot | 仅 robot | robot + msg（proto userdata 或 binary string） |
| 模板（新建时） | `execute_template.lua`（含 `_send/_recv` 累加示例） | `boolean_template.lua`（`return false`） | `on_message_template.lua` |
| Lint 检查 | 必须存在 `function execute(r)` | 必须存在 `function execute(r)` | 必须存在 `function onMessage(r, msg)` |
| 引擎入口 | `script.RuntimePool.RunActionScript` | `script.RuntimePool.RunBooleanScript` | `script.RuntimePool.RunCallbackScript` |

`LuaForm` 组件支持 `mode: 'action' | 'boolean' | 'callback'` 入参，按 mode 切换：模板内容、Monaco snippet、Lint 规则、签名提示文本。`ConditionInput`（boolean / loop 节点编辑器）传 `mode='boolean'`，编辑 action 节点时传 `'action'`。

> 注意：callback 的 `script` 在**没有** `s2cProto` 时，引擎会把原始二进制以 string 形式注入 `msg`（见 `listen_frame_data.lua` 的 `string.byte(msg, 13)` 用法）。因此 lua 形态可以**不**配对 `s2cProto`；CallbackEditor 在 lua 形态下不强制 proto 选择。

### 8.6 ListenRefsTable 增强（修订 §7.7）

action 节点上编辑 `listenCallbacks` 的表格，是连接 action 与 callback 子系统的关键纽带。当前列设计：

| 列 | 控件 | 说明 |
|---|---|---|
| `route` | `RouteEditor` | **单 Input + 不透明 JSON**（详见 §8.7）；不再做 `{cmd, act}` 表单与 JSON 双模式切换 |
| `server` | 纯文本 Input | 早期"按 service 候选下拉"被验证不够可靠（候选源不稳定 + 用户对 server 名感知更可靠），统一改为手输；占位提示 `如 tcp:logic` |
| `callback` | 下拉 | 选项含 `(null) — 静默丢弃`；`+ 新建` 直接打开 CallbackEditor 创建并回填；`→ 跳转` 在 CallbackPanel 中定位 |
| 形态 | 只读徽章 | 实时反映 callback 当前形态（`callbackKindStyle.ts` 单一来源：silent/灰、declarative/蓝、lua/紫）；callback 形态切换会即时更新这里 |
| 操作 | `[×]` | 删除 |

底部入口：
- `[+ 添加监听]`
- `[+ 批量粘贴 JSON]` —— 弹窗粘贴一段 `ListenRef[]` JSON，校验通过后一次性追加多条；适合从其他 flow.json 直接搬运监听集
- `[+ 批量从模板]` 与 `[导入] / [导出]` —— 路线图，尚未落地

**ListenRefsTable 嵌入位置**：在 ActionEditor Drawer 底部以**可折叠面板**（默认折叠，标题显示"注册监听（N）"）形式嵌入。

**双侧编辑**：除 ActionEditor 中的 ListenRefsTable 外，`CallbackEditor` 内的 `BackrefList`（callback 的反向引用列表）也支持**直接编辑每条引用的 server / route 与删除**，省去"从 callback 跳到对应 action 再修改"的来回切换；node ID 仍是可点击跳转链接。

### 8.7 RouteEditor —— 不透明 JSON 单 Input

`route` 在引擎中是 `unknown/any`（透明传给 adapter）。原计划为 `{cmd, act}` 结构化表单 + JSON 双模式，落地后发现**双模式带来更多边界 case**（异形 route、空对象、用户输入未完成的中间态），且与 `ActionDef.route` 已有的 JSON Input 表现冲突；现已收敛为**单一的不透明 JSON Input**：

```
┌──────────────────────────────────────────┐
│ {"cmd":4,"act":10}                       │
└──────────────────────────────────────────┘
占位提示：如 {"cmd":4,"act":10}
```

行为约定（实现见 `callbacks/RouteEditor.tsx`）：
- 输入合法 JSON → 解析为对象后写回 store
- 输入非法 JSON（用户尚未输完）→ **原样以字符串保留**，避免抖动；下次合法时再解析为对象
- 输入空字符串 → 写回 `undefined`（语义上等价于"未配置"）
- 同一 `RouteEditor` 组件被 `DeclarativeForm`（ActionDef.route）/ `ListenRefsTable` / `BackrefList` 三处共用

### 8.8 引用图 & 校验

新建 `callbacks/refsGraph.ts`，提供以下能力：

```ts
export interface RefsGraph {
  // node id -> 该 action 注册的所有 ListenRef
  nodeToRefs: Map<string, ListenRef[]>;
  // callback name -> 反向：哪些 (nodeId, refIndex) 引用了它
  callbackToRefs: Map<string, Array<{ nodeId: string; refIndex: number; ref: ListenRef }>>;
  // (server, routeKey) -> 多重注册检测
  duplicateRegisters: Array<{ server: string; routeKey: string; refs: Array<{ nodeId: string; cb: string | null }> }>;
}

export function buildRefsGraph(flow: TaskFlow): RefsGraph;
```

校验项（保存导出前 / ValidationReport 中）：

| 等级 | 检查 | 说明 |
|---|---|---|
| Error | `listenCallbacks[].callback` 引用了不存在的 callback | 强阻止导出 |
| Error | `declarative` callback 的 `s2cProto` 不在 ProtoRegistry 内 | 强阻止 |
| Error | `lua` callback 的 `script` 在脚本库中不存在 | 强阻止 |
| Warning | callback 形态为 `silent` 但被多个 action 引用同一 server+route | 提示是否真的需要静默 |
| Warning | callback 引用计数为 0（孤儿）| 列于 CallbackPanel 与报告中 |
| Warning | 同一 `server + routeKey` 在不同 action 的 listenCallbacks 中被注册了不同 callback | 后注册可能覆盖前者，提示用户确认 |
| Info | `lua` callback 没有 `s2cProto` 时提示"将以原始二进制传入" | 仅提示，不阻断 |

`routeKey` 由 `adapter.expected_response_key(route)` 计算，但前端无法运行 Lua adapter，因此简化为：把 route 序列化为稳定 JSON（key 排序）作为伪 key。这对绝大多数 `{cmd, act}` 形态足够区分；异形 route 走"按完整 JSON 比对"。

### 8.9 Callback 模板库（已并入 NodePalette）

实际实现：模板库与节点面板**共用一个左侧 `NodePalette`**，不再做独立抽屉。`NodePalette` 自上而下分三个独立滚动区：

1. 节点类型（sequence / action / loop / boolean / weighted / wait / break / continue / **callback**）
2. Action 模板（标题固定 + 独立搜索框 + 独立滚动）
3. Callback 模板（同上）

每个模板项可拖入画布；ActionEditor / CallbackEditor 内的 `[保存到库]` 按钮把当前对象写入对应 IndexedDB store。

存储模型（idb-keyval，封装在 `library/templateStore.ts`）：

```ts
ActionTemplate   = { id, name, action: ActionDef,    tags?, description?, updatedAt }
CallbackTemplate = { id, name, callback: CallbackDef, tags?, description?, updatedAt }
```

新建 callback 的两种入口：
- 从 `NodePalette` 节点类型区拖入"Callback"块（落到画布事件区生成 silent 占位）
- 在画布右键菜单 → 新建节点 → Callback

整体导出 / 导入到 `actions-library.json` / `callbacks-library.json` 留作下一阶段；当前仅做单模板新增 / 编辑 / 删除。

### 8.10 与画布的双向高亮联动

**当前实现：单向（callback → 节点）已落地，反向（节点 → callback 列表）暂未启用**。

实现细节：

```ts
// store/editorStore.ts
interface EditorState {
  hoveredCallback: string | null;   // CallbackPanel 上正在悬停的 callback name
  // hoveredNodeId 字段已删除：原计划用于反向高亮但 CallbackPanel 一直未订阅，
  // 与之配套的 setHoveredNode/onMouseEnter 一起清理掉，避免无效 store 订阅。
}

// store/flowStore.ts —— 派生数据
interface FlowState {
  callbackRefCount: Record<string, number>;
  nodesByCallback: Record<string, string[]>; // 由 jsonToFlow / syncDerived 维护，反向索引
  // ...
}

// nodes/shared/NodeShell.tsx —— 精确高亮（不再"hoveredCallback 非空就全高亮"）
const isRegisteringHovered = useFlowStore(s =>
  hoveredCallback ? (s.nodesByCallback[hoveredCallback] ?? []).includes(nodeId) : false
);
```

效果：用户在 CallbackPanel 鼠标移到 `stateUpdate` → 仅在 `listenCallbacks` 中真正注册了它的 action 节点（如 `ConnectLogicTCP`）亮边；其它节点保持常态。

**反向高亮（节点 → callback 列表）路线图**：未来在 `editorStore` 重新加 `hoveredNodeId`，配合 `flowStore.callbacksByNode` 反查表（与 `nodesByCallback` 对偶）实现 CallbackPanel 列表项高亮；本期先落地的精确单向已覆盖最高频场景（看一个 callback 是从哪些 action 注册的）。

---

## 9. Proto 解析与浏览

### 9.1 加载策略

本期前端不直接读文件系统（浏览器限制）。**核心决策：启动时一次性全量加载所有 proto，加载完成前编辑器显示 loading 遮罩**。`conf/proto/` 当前 ~110 个文件，protobufjs 全量解析约 2~3 秒，后续 ActionEditor / ProtoBrowser / 字段补全均为内存查找，零网络延迟。

实际加载源（按优先级，由 `proto/ProtoLoader.ts` 实现）：

| 来源 | 适用阶段 | 实现方式 |
|---|---|---|
| **A. Vite 中间件 `/conf/proto/`** | 当前默认 | `web/vite.config.ts` 注册中间件直接 serve 仓库根 `conf/proto/` 目录；`ProtoLoader` 先 `GET /conf/proto/index.json`（中间件实时枚举生成）拿文件清单，再并发 fetch 每个 `.proto` 文件内容 |
| **B. `import.meta.glob` 兜底** | 中间件不可用 / 静态打包 | 通过 Vite 的 `?raw` import 在打包期把 proto 文本嵌入产物，作为 A 失败时的降级路径；不需要后端配合 |
| **C. Go monitor HTTP API** | 路线图 | `GET /api/proto/files` + `GET /api/proto/file/:name`；后端联调阶段切换，对 ProtoLoader 透明 |
| **D. 用户手动上传** | 路线图 | "项目设置" → File API 选目录/多文件；A/B/C 都不可用时降级 |

加载状态由 `proto/protoStore.ts` 的 zustand store 统一管理：

```ts
type ProtoStatus = 'idle' | 'loading' | 'ok' | 'error';
interface ProtoStoreState {
  status: ProtoStatus;
  hash?: string;          // 内容 hash，用于驱动 ProtoBrowser useMemo 重算
  errorMessage?: string;
  setStatus(s: ProtoStatus, hash?: string, err?: string): void;
}
```

下游组件（`ProtoBrowser` / `ProtoFieldPicker` 等）订阅 `status + hash`，一旦 ProtoLoader 完成解析、`status` 切到 `ok`，UI 立即重算下拉/列表 —— 早期版本因 `useMemo` 漏依赖导致"日志显示已加载、UI 仍空"，现已修。

**AST 遍历坑**：`protobuf.NamespaceBase` 仅是类型名，运行期没有同名构造器，不能用 `instanceof`。`ProtoLoader` 与 `ProtoRegistry` 的 `walk` 函数统一用 duck-typing：

```ts
function isNamespaceLike(n: unknown): boolean {
  return (n as { nestedArray?: unknown }).nestedArray !== undefined;
}
```

**索引产物缓存到 IndexedDB**（key = `protoCache.hash`）路线图保留：第二次进入页面时若 hash 一致直接复用，启动耗时可降到 < 200ms。当前每次进页面都会重新解析（~2-3s @ 110 个文件），等性能成为瓶颈再做。

### 9.2 ProtoRegistry

```ts
// proto/ProtoRegistry.ts
class ProtoRegistry {
  private root: protobuf.Root;
  private messageIndex = new Map<string, ProtoMessage>();
  private enumIndex = new Map<string, ProtoEnum>();

  load(root: protobuf.Root) { /* 遍历所有 type 建索引 */ }
  lookupMessage(fullName: string): ProtoMessage | undefined;
  listMessages(prefix?: string): ProtoMessage[];
  resolveFieldType(messageName: string, fieldName: string): FieldType;
}
```

### 9.3 ProtoBrowser

右侧抽屉式 message 浏览器（被 ActionEditor 唤起选 `c2sProto` 时使用）。

**当前实现：扁平列表 + 搜索**（不再做命名空间树）。早期"按 Game / Battle / enum.proto 分组展开"的草案在实操中收益小：所有 message 已带 `Game.` / `Battle.` 前缀，搜索就能即时定位；树形折叠反而多一步交互。改为：

```
┌─ Proto Browser ─────────────────┐
│  [搜索框] 状态：已加载 N 条     │
├─────────────────────────────────┤
│  LoginPlayerC2S                 │  ← 主标题：shortName
│    Game.LoginPlayerC2S          │  ← 描述：fullName（小字、灰）
│  LoginPlayerS2C                 │
│    Game.LoginPlayerS2C          │
│  HeroData                       │
│    Game.Hero.HeroData           │
│  ...                            │
│  ──── 详情 ──────               │
│  Game.LoginPlayerC2S            │
│  ┌─ 字段 ────────────────┐     │
│  │ playerId: int32       │     │
│  │ session: string       │     │
│  └────────────────────────┘    │
│  [选择此消息]                  │
└─────────────────────────────────┘
```

加载状态通过 `proto/protoStore.ts` 暴露（`idle | loading | ok | error` + 内容 hash），抽屉空态显式提示 "Proto 正在加载…/已加载 N 条/加载失败" 三态，避免出现"看上去是空"的体验问题。

### 9.4 缓存

解析结果以 `{rootJson: protobuf.INamespace}` 形式存 IndexedDB，避免每次都重新加载。版本变更通过 hash 失效。

---

## 10. 持久化与导入导出

### 10.1 LocalStorage（编辑稿）

实际落地由 `store/persistDraft.ts` 负责：

- 监听 `useFlowStore` 任意变更 → **debounce 300ms** 写入 LocalStorage（原方案 500ms 实测下用户连续编辑后立即刷新会丢最近一两次操作；缩短到 300ms 显著降低概率）
- 同时挂 `window.addEventListener('beforeunload', flushNow)`：页面关闭/刷新前**同步落盘**最后一次未触发的 debounce，从根本上杜绝丢失
- LocalStorage 双份：`stressbot:flow:current` 存 TaskFlow，`stressbot:flow:layout` 存 FlowLayout（含 nodePositions / showListenEdges），互相独立便于增量更新

打开页面时通过 `useEffect` 一次性恢复编辑稿（StrictMode 下用 `useRef` 哨兵避免触发两次）；恢复成功后右上角 toast `已恢复编辑稿 / 上次保存于 ...`。

**双 Store 分工**（详见 `store/flowStore.ts` + `store/editorStore.ts`）：

| Store | 内容 | 持久化 |
|---|---|---|
| `flowStore` | 业务数据（nodes/actions/callbacks/defaultDelayMs）、RF 镜像（rfNodes/rfEdges/layout）、派生数据（callbackRefCount/nodesByCallback/issuesByNodeId） | 通过 `persistDraft` 整体落盘 |
| `editorStore` | UI 临时状态（selectedNodeId / hoveredCallback / activePanel / clipboard / 各开关 / theme） | 仅 theme 单独存 LocalStorage（`stressbot:theme`），其它一次会话内的临时状态不持久化 |

**派生数据更新约束**：所有 `addNode/updateNode/replaceNode/removeNode/renameNode` 与对应的 action / callback CRUD 在末尾**必须**调用 `get().syncDerived()`，否则 `rfNodes` 不会随业务数据变化而重算，会出现"编辑器已修改、画布仍显示旧值、刷新才生效"的怪相。这条约束在 store 实现里通过 grep 强制覆盖；新增 mutation 时务必保持。

### 10.2 IndexedDB（动作 / 回调模板库）

```
db: stressbot-editor
stores:
  - actionLibrary:   { id, name, action: ActionDef,    tags?: string[], updatedAt }
  - callbackLibrary: { id, name, callback: CallbackDef, tags?: string[], updatedAt }
  - luaLibrary:      { name, content, mode: 'action' | 'callback' }   # mode 决定签名/Lint 规则
  - protoCache:      { hash, rootJson }
```

### 10.3 流程库 — FlowManagerModal（IndexedDB 流程持久化）

> **新增（2026-05-09）**：Toolbar 中「流程管理」按钮唤起 `FlowManagerModal`，提供用户级的"本地流程库"，支持保存/打开/覆盖/删除多份完整流程草稿。

**存储架构**：

```
db:   stressbot-flows-manager       (idb-keyval createStore)
store: data                          (单 object store，key = ManagedFlow.id)
```

**ManagedFlow 数据模型**（定义于 `store/flowManagerStore.ts`）：

```ts
export interface ManagedFlow {
  id: string;          // nanoid() 自动生成；覆盖时传入已有 id
  name: string;        // 用户可读名称（如 "200v200 v1.2"）
  flow: TaskFlow;      // 完整业务数据（nodes/actions/callbacks）
  layout: FlowLayout;  // 画布布局（nodePositions/showListenEdges）
  updatedAt: number;   // Date.now()，列表按此倒序
}
```

**操作**：

| 操作 | 实现 | 说明 |
|---|---|---|
| 另存为新流程 | `saveFlow(name, flow, layout)` | 不传 `existingId` 时自动 `nanoid()` 生成新 ID |
| 覆盖已有流程 | `saveFlow(name, flow, layout, existingId)` | 传入已有记录 ID，更新 flow/layout/updatedAt |
| 打开流程 | `getFlow(id)` → `loadFromTaskFlow(flow, layout)` | 替换当前画布，关闭 modal |
| 删除流程 | `deleteFlow(id)` | 不可恢复，Popconfirm 二次确认 |
| 列出全部流程 | `listFlows()` | 返回按 `updatedAt` 倒序的数组 |

**UI 布局**（`FlowManagerModal`）：

```
┌─ 本地流程管理 (IndexedDB) ──────────────────────────────────────┐
│  [输入流程名称保存当前草稿...]  [另存为新流程]                     │
├──────────────────────────────────────────────────────────────────┤
│  流程名称            │ 更新时间              │ 操作              │
│  200v200 v1.2        │ 2026-05-09 14:30:00  │ [打开] [覆盖] [×] │
│  未命名流程 0508      │ 2026-05-08 10:00:00  │ [打开] [覆盖] [×] │
└──────────────────────────────────────────────────────────────────┘
```

设计要点：
- **与 LocalStorage 编辑稿互补**：`persistDraft` 存的是"当前工作草稿"（自动保存），FlowManagerModal 存的是"命名快照"（手动保存），两者互不干扰。
- **打开流程会替换当前草稿**：覆盖前不做二次确认（草稿已由 `persistDraft` 自动保存到 LocalStorage，可通过刷新恢复）。
- **覆盖操作有 Popconfirm**：提示"用当前草稿覆盖 xxx？"，防止误触。

操作面板：

- **保存动作到库**：从 ActionEditor 触发，输入名称/标签 → 写入 `actionLibrary`。
- **保存回调到库**：从 CallbackEditor 触发 → 写入 `callbackLibrary`。
- **从库选择**：右侧 `ActionLibrary` / `CallbackLibrary` 面板支持搜索 / 标签筛选 / 拖入。
- **导入/导出**：分别导出为 `actions-library.json` 和 `callbacks-library.json`；导入合并去重。

### 10.4 导出 / 校验

导出流程：

```
[导出按钮]
  ↓
1. 序列化 store.nodes -> Record<string, FlowNode>
2. 序列化 store.actions -> Record<string, ActionDef>
3. 序列化 store.callbacks -> Record<string, CallbackDef>
4. 组装 TaskFlow
5. ajv 校验 schema
6. 引用合法性 + 业务校验（refsCheck.ts + refsGraph.ts）
7. 弹 JsonPreviewModal 展示
8. 用户点击：[复制] / [下载 flow.json] / [下载 layout.json] / [应用]
```

**校验规则总表**（结合 §8.8 callback 子系统的检查项）：

| 等级 | 规则 | 校验时机 |
|---|---|---|
| Error | 所有 `sequence.next` / `loop.body` / `boolean.trueNext`·`falseNext` / `weighted.options[].node` 引用的节点 ID 必须存在于 `nodes` | 实时 + 导出 |
| Error | 每个 `action 节点` 的 `action` 字段必须存在于 `actions` 表 | 实时 + 导出 |
| Error | 每个 `listenCallbacks[].callback` 必须存在于 `callbacks` 表（`null` 例外，表示静默丢弃） | 实时 + 导出 |
| Error | `Loop` 节点的 `body` 不能为空 | 实时 + 导出 |
| Error | `Weighted` 节点至少有 1 个 option | 实时 + 导出 |
| Error | `declarative` callback 的 `s2cProto` 必填且必须在 ProtoRegistry 内 | 实时 + 导出 |
| Error | `lua` callback 的 `script` 必填且必须存在于脚本库 | 实时 + 导出 |
| Error | `Pattern = lua` 的 ActionDef 的 `script` 必填 | 实时 + 导出 |
| Warning | `Boolean` 节点 `condition` 为空 | 实时（黄色边框） |
| Warning | `tcpRequest` 的 `s2cProto` 为空（合法但失去 store 能力） | 实时 |
| Warning | callback 引用计数为 0（孤儿） | 导出 + ValidationReport |
| Warning | 同一 `server + routeKey` 在多个 action 注册了不同 callback（后注册可能覆盖） | 导出（详见 §8.8） |
| Warning | 不可达节点（从 `start` 节点 BFS 到不了） | 导出 |
| Warning | callback 形态为 `silent` 但同一路由被多个 action 绑定 | 导出 |
| Info | `lua` callback 没有 `s2cProto` 时提示"将以原始二进制传入" | CallbackEditor 编辑时 |

**实时校验**通过 `validation/refsCheck.ts` 的 `validateFlow(flow)` 在 `flowStore.syncDerived()` 中触发，按节点 ID 分组写入 `issuesByNodeId`，由 `NodeShell` 渲染右上角 `!` / `?` 徽章（红色 = error，橙色 = warning），并把 message 拼接到 `title` 内，鼠标悬停看清单。
**导出校验**通过 Toolbar `[校验]` 按钮唤起 `validation/ValidationReport.tsx` 抽屉（注意：实际位于 `validation/`，不在原计划的 `preview/`），按 Error/Warning/Info 分级，点击每条可跳到对应节点。

补充已落地的常见**必填字段**校验：
- `loop`：`loopCount > 0` 或 `condition.trim()` 或 `breakCondition.trim()` 至少一项非空（否则报 error）
- `boolean`：`condition.trim()` 必填，且 `trueNext / falseNext` 至少一个有值
- `wait`：`waitMs > 0`
- `action.listenCallbacks[].server`：必填非空字符串（手输容易漏）

### 10.5 导入

```
[导入按钮]
  ↓
1. File picker 选 .json
2. 解析 → ajv 校验
3. 若文件中含未识别字段（如旧版 trueBranch），提示并询问是否兼容转换
4. 同时弹出"是否同时导入 flow.layout.json"
5. 加载 layout（若无，dagre 自动布局）
6. 替换 store（询问是否覆盖当前编辑稿）
```

### 10.6 兼容性

第一版仅支持 `redesign-flow-nodes.md` 描述的新格式。导入时若检测到旧格式（如 `nodes` 是数组、`next: [{node, weight}]`），给出明确提示并指引用户先用 Go 端工具迁移（后续可在前端加 codemod）。

---

## 11. 与后端通信的接口预留

本期不实现，但提前定义清晰的 API 边界，避免后期重构。

### 11.1 占位接口列表

```ts
// api/endpoints.ts
export const endpoints = {
  // 流程配置
  loadFlow:    () => GET  '/api/flow',
  saveFlow:    () => POST '/api/flow',
  validateFlow:() => POST '/api/flow/validate',

  // proto / 脚本资源
  listProto:   () => GET  '/api/proto',
  loadProto:   (name: string) => GET '/api/proto/:name',
  listScripts: () => GET  '/api/scripts',
  loadScript:  (name: string) => GET '/api/scripts/:name',
  saveScript:  (name: string) => PUT '/api/scripts/:name',

  // 运行控制（后期）
  startBots:   () => POST '/api/run/start',
  stopBots:    () => POST '/api/run/stop',
  status:      () => GET  '/api/run/status',

  // 监控（后期）
  metricsSnapshot: () => GET  '/api/metrics/snapshot',
  metricsStream:   () => SSE  '/api/metrics/stream',
};
```

### 11.2 客户端封装

```ts
// api/client.ts
const baseUrl = import.meta.env.VITE_API_BASE ?? '';
export const apiClient = {
  get<T>(path: string): Promise<T> { ... },
  post<T>(path: string, body?: unknown): Promise<T> { ... },
  // 内置 mock 模式：BASE_URL 为空时使用 mock.ts
};
```

### 11.3 Mock 数据

`api/mock.ts` 提供 `mockClient`，内置：

- 一个 `flow.json` 样本（即 `conf/flow.json` 的简化版）
- 一份 proto 列表（解析自打包内的几个示例 .proto）
- 一组动作模板（PlayerLogin, StartMatch, ConnectBattleTCP）

让前端在零后端环境下也能完整跑起来。

---

## 12. 监控数据展示位的预留方案

虽然本期不实现监控，但在节点视图中已预留 `MetricsBadge` 槽位（§5.4）。最终监控接入步骤（参考 `docs/design-monitor.md`）：

1. **数据源接入**：`MetricsProvider` 通过 SSE 订阅 `/api/metrics/stream`，按 nodeId 索引最新指标。
2. **指标维度**：`{ entered: number; failRate: number; avgMs: number; p95Ms: number; }`。
3. **可视化扩展**（未来）：
   - 节点边框颜色随 failRate 变化（绿→黄→红）
   - 节点上添加"放大查看"按钮，点击弹出完整直方图
   - 边上叠加"流量计"动画（每个动作执行时短暂闪烁）

**当前期落地点**：

```ts
// monitor/MetricsProvider.tsx
const MetricsContext = React.createContext<NodeMetrics | undefined>(undefined);

export function MetricsProvider({ children }) {
  // 本期：return <MetricsContext.Provider value={undefined}>...</...>;
  // 后期：useEffect 内开 SSE 订阅，setState 更新数据
  return <MetricsContext.Provider value={undefined}>{children}</MetricsContext.Provider>;
}

export function useNodeMetrics(nodeId: string): NodeMetrics | undefined {
  const all = React.useContext(MetricsContext);
  return all?.[nodeId];
}
```

---

## 13. `<FlowEditor />` 组件 API（封装契约）

**当前最小 props 集合**（已收敛，待真正接入后端时再扩展受控 / 回调类参数）：

```tsx
// components/FlowEditor/index.tsx
export interface FlowEditorProps {
  /** 初始 flow.json，未传时按 autoLoadDefault 决定是否从 /conf/flow.json fetch */
  initialFlow?: TaskFlow;
  /** 初始 layout.json */
  initialLayout?: FlowLayout;
  /** 自动加载 conf/flow.json（开发模式默认 true） */
  autoLoadDefault?: boolean;
  /** 监控数据提供方：实时返回某节点的运行指标，未提供时不显示监控徽章 */
  metricsProvider?: MetricsProvider;
}
```

### 13.1 使用示例

```tsx
// 最简使用：开发期由 Vite 中间件吐出 conf/flow.json 与 conf/proto/
<FlowEditor />

// 嵌入更大的 IDE 布局并接外部监控
<Layout>
  <Layout.Sider>RobotControlPanel</Layout.Sider>
  <Layout.Content>
    <FlowEditor metricsProvider={(id) => sseMetrics[id]} />
  </Layout.Content>
  <Layout.Sider>LogViewer</Layout.Sider>
</Layout>
```

### 13.2 暂不暴露的能力（路线图）

下列 prop 草案在落地阶段验证后认为属于过度设计或后端联调阶段才有意义，**当前未实现，避免产生死代码与误导调用方**：

| Prop | 现状 | 替代方案 |
|---|---|---|
| `onSave` | 未接线 | 当前由 Toolbar `导出 flow.json` 触发 download；后端联调阶段再补 |
| `onNodeSelect` | 未实现 | 外部如需联动可直接订阅 `useEditorStore(s => s.selectedNodeId)` |
| `protoRoot` / `presetActions` | 未实现 | proto 由内部 `ProtoLoader` 自加载（详见 §9）；动作模板由 IndexedDB 持久化 |
| `controlled / readOnly` | 未实现 | 后端联调上线后再设计受控模式与"运行中只读快照" |
| `theme` | 未实现 | 由 `editorStore.theme` 内部管理 + `tokens.css` 切换；外部 `ConfigProvider` 主题与之独立 |
| `toolbarExtra` | 未实现 | Toolbar 当前内嵌固定按钮组；如有插槽需求再扩展 |

---

## 14. 实施阶段（Phase Plan）

> 每个 Phase 结束需可独立运行 demo 并完成 §15 验证清单。

### Phase 1：脚手架与最小可见画布（~1 天）

- 初始化 `web/` 目录：`npm create vite@latest web -- --template react-ts`
- 装包：`@xyflow/react antd zustand nanoid @monaco-editor/react protobufjs ajv idb-keyval dagre`
- 配置 `vite.config.ts`、`tsconfig.json`
- 编写 `App.tsx` demo 容器
- 实现最小 `FlowCanvas`：能拖入一个 sequence、一个 action，连一条线
- ✅ 产出：`npm run dev` 起服，画布可加节点连线。

### Phase 2：完整节点系统（~2 天）

- 7 类节点的 React 组件 + handle 布局
- Node Palette（左侧拖拽源）
- 连线合法性校验（§5.3）
- store.ts 完整：nodes / edges / actions / callbacks
- 序列化 ↔ TaskFlow 的 codec 测试用例（5 套）
- ✅ 产出：能搭出 §3.5 的简单 loop demo，导出为正确 JSON。

### Phase 3：节点编辑器（~3 天）

- NodeEditorModal 调度
- SequenceEditor、LoopEditor、BooleanEditor、WeightedEditor、WaitEditor
- ConditionInput / DelayInput 等共享控件
- 与画布双向同步（编辑器改 → 画布刷新；画布连线改 → 编辑器实时反映）
- ✅ 产出：可用拖+双击+表单完整编辑 §3.4 的"带 continue 的循环"。

### Phase 4：Proto 解析与浏览（~2 天）

- ProtoLoader / ProtoRegistry
- ProtoBrowser 抽屉
- IndexedDB 缓存 + 失效
- ✅ 产出：上传 `conf/proto/` 全部文件，能按 `Game.*` 浏览所有 message + field。

### Phase 5：ActionEditor —— 声明式（~4 天，最复杂）

- PatternSelector + DeclarativeForm
- BindingsTable + BindingTypeForm（17 种 BindingType）
- ProtoFieldPicker（联动）
- StoreTable（与 callback 子系统共用）
- 嵌套 binding（nested / nestedList）
- ✅ 产出：能完整复刻 `conf/flow.json` 中的 `PlayerLogin`、`SelectHero`、`ReportBattleDelay`（含 nestedList）。

### Phase 6：ActionEditor —— 脚本式（~1 天）

- LuaForm + Monaco（支持 mode='action' / 'callback'）
- 脚本库 IndexedDB
- 导入 / 导出 .lua
- 内置 API 提示
- ✅ 产出：能完整复刻 `match_succeed.lua`（导入并保存）。

### Phase 7：Callback 子系统（~3 天，关键路径）

- `types/callback.ts` + `classifyCallback` 三态判别
- CallbackPanel（右侧抽屉 + 三态徽章 + 引用计数）
- CallbackEditor（CallbackTabs：silent / declarative / lua）
- declarative 形态：复用 ProtoBrowser + StoreTable
- lua 形态：复用 LuaForm（mode='callback'，签名 `onMessage(r, msg)`）
- RouteEditor（双模式：`{cmd, act}` / JSON）
- ListenRefsTable（`+ 新建` / `→ 跳转` / 形态徽章）
- BackrefList（callback → action 反向引用）
- refsGraph + 双向悬停高亮（CallbackPanel ↔ FlowCanvas）
- ✅ 产出：能完整复刻现网 `conf/flow.json` 中的全部 12 条 callbacks（含 silent 占位 + `stateUpdate` declarative + `frameData` lua）。

### Phase 8：模板库（动作 + 回调）（~2 天）

- IndexedDB `actionLibrary` + `callbackLibrary`
- 右侧 `ActionLibrary` / `CallbackLibrary` 抽屉
- 搜索 / 标签 / 拖入
- 整体导出 / 导入（`actions-library.json`、`callbacks-library.json`）
- ✅ 产出：能把 `PlayerLogin`、`stateUpdate`、`frameData` 保存到库后，新建 flow 时直接拖入复用。

### Phase 9：预览与校验（~1.5 天）

- JsonPreviewModal（Monaco 只读）
- ajv schema + refsCheck（节点 / 动作 / 回调引用、s2cProto 存在性、route 重复注册）
- ValidationReport 报告 UI（按 Error/Warning/Info 分级，可点击跳转）
- ✅ 产出：导出前能定位"callback 缺失"、"重复 route 注册"、"declarative s2cProto 不在 ProtoRegistry 中"等所有合法性问题。

### Phase 10：导入/导出/持久化/Undo-Redo（~1.5 天）

- File API 导入导出
- LocalStorage 编辑稿
- 命令栈（commands.ts）+ Ctrl+Z/Y
- ✅ 产出：刷新页面恢复编辑稿；能撤销 20 步操作。

### Phase 11：监控槽位与组件 API 收口（~1 天）

- MetricsProvider 占位
- nodes/shared/MetricsBadge 渲染
- FlowEditor props 接口最终化
- 抽离 `index.tsx` 作为对外入口
- ✅ 产出：`<FlowEditor />` 可被外部容器直接使用。

### Phase 12：完整业务流复刻（~2 天）

- 用编辑器从零搭一遍 `conf/flow.json`（登录 → 业务循环 → 战斗 + 全部 callbacks）
- 与现有 `conf/flow.json` 做 diff，对齐到字段级别（含 callbacks map）
- 跑 `go run ./cmd/validate <导出文件>` 必须通过
- ✅ 产出：编辑器导出文件可被引擎直接消费跑通现网压测流程，含 stateUpdate / frameData 等全部 callback 行为。

### Phase 13：测试与文档（~1.5 天）

- 单测：codec / refsCheck / refsGraph / proto registry / 三态判别
- 操作录像（README 嵌入 GIF）
- 嵌入示例（含 demo + 后端联调示例）
- ✅ 产出：`web/README.md` 完整、`web/tests` 通过。

> **总工作量预估：约 25 工作日**（不含后端联调）。Phase 5 / 7 / 12 是关键路径，其余可并行加快。

---

## 15. 验证清单（每 Phase 通用）

> 类比 `CLAUDE.md` 的"验证流程"，前端也建立明确的可重复验证步骤。

1. **构建检查**：`cd web && npm run build` 无 TS 报错、无打包警告。
2. **Lint**：`npm run lint` 通过（eslint + prettier）。
3. **单测**：`npm run test` 全绿。
4. **冒烟测试**：
   - 启动 `npm run dev`
   - 新建 → 编辑 → 保存 → 导出 → 关闭 → 重启 → 导入 → 验证图同构
5. **回归用例**：导入 `conf/flow.json` → 不报错 → 导出后 `go run ./cmd/validate <out>` 通过。
6. **可访问性**：键盘导航全部交互可达。

---

## 16. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| React Flow v12 学习曲线 | Phase 1-2 进度 | 优先消化官方 example，从 simplest 开始迭代 |
| protobufjs 解析项目内全部 .proto 失败（依赖、语法兼容） | Phase 4 阻塞 | 提供"逐文件加载 + 错误隔离"模式；缺失 message 仅在使用处提示 |
| 大流程图（>200 节点）画布卡顿 | 体验下降 | React Flow v12 已支持虚拟化；必要时关 minimap 和动画 |
| nestedList 等复杂 binding 编辑深度大 | UX 复杂 | 用 Tab 化嵌套 + 面包屑导航；最大允许 5 层（旧 flow 实测最多 3 层） |
| LocalStorage 容量限制（5MB） | 大型流程编辑稿丢失 | 转存 IndexedDB；每次保存生成 timestamp 历史，最多保留 10 份 |
| Lua API 提示数据维护成本 | 信息陈旧 | 从 `script/` 包内的 register 表自动生成 snippets（后端配合输出 JSON） |
| 与未实现的后端联调字段不匹配 | 后期重构 | §11 提前定义 endpoints；本期所有 mock 实现都通过同一接口 |
| 旧/新 flow.json 混用 | 导入异常 | 第一版直拒旧格式 + 明确报错 |
| `route` 类型为 `unknown`，前端无法等价计算 routeKey | 重复注册检测漏报 | 用稳定排序 JSON 序列化作为伪 key（覆盖 99% `{cmd, act}` 场景），异形 route 走完整 JSON 比对 |
| `ListenRef.callback = null` 与 callback `{}` 语义混淆 | 用户误用 | UI 显式区分：null 选项标"静默丢弃"；callback `{}` 标"silent 占位"；CallbackPanel / ListenRefsTable 形态徽章联动 |
| `lua` 形态 callback 无 s2cProto 时收到原始 binary | 用户写 `proto.get_field_map(msg)` 报错 | LuaForm（mode='callback'）在无 s2cProto 时给出明确提示与 binary 解析示例 snippet |
| callback 形态切换误清字段 | 数据丢失 | 切换形态时弹确认；切回原形态自动恢复内存中的旧字段（30 秒内） |
| 大流程显式 ListenEdge 视觉过载（如 `ConnectLogicTCP` 一次 17 条监听全画虚线） | 画布乱成一团 | Toolbar 提供"☑ 显式监听边"开关；监听数 > 8 时该 action 默认折叠为 `📡 17` 徽章，不渲染单条 ListenEdge；用户可单独点击徽章展开该节点的监听边 |
| Loop 节点意外多画 exit handle | 用户误连导致 flow.json 出现非法字段 | NodeShell 在 type='loop' 时不渲染 exit handle；LoopEditor 编辑 `loopCount` / `breakCondition` 也不暴露任何 exit 出口字段 |

---

## 17. 后续扩展方向（路线图）

1. **运行联调** — 接入 §11 的 start/stop/status，编辑器即"压测控制台"。
2. **监控数据接入** — `MetricsProvider` 实接，节点上实时显示 `entered / avgMs / p95 / failRate`。
3. **图分组与折叠** — sequence / weighted 支持"折叠为子流程"，缓解大型图视觉压力。
4. **Diff 视图** — 两个 flow.json 互相对比，高亮节点 / 字段差异。
5. **协作模式** — 多人在线编辑（CRDT 或基于 Yjs），用于团队压测脚本演进。
6. **LSP / Lua 完整工具链** — Monaco 接入 lua-language-server，提供跳转 / 重命名。
7. **AI 辅助** — 自然语言 → 流程图原型生成（"创建一个登录后随机选 5 个英雄并循环战斗的压测脚本"）。
8. **图嵌入到日志** — 压测运行日志中点击节点 ID 跳转到画布并定位（与监控联动）。

---

## 18. 字段速查与映射表

### 18.1 节点字段 → React Flow 数据

| flow.json 字段 | React Flow 表达 | 编辑入口 |
|---|---|---|
| `nodes[id].type` | `node.type`（自定义节点类型） | Palette 拖入 / 不可改 |
| `sequence.next[i]` | edges：source = id, source-handle = `seq-${i}`, target = `next[i]` | 拖线 / SequenceEditor 排序 |
| `loop.body` | edges：source-handle = `body`, target = body | 拖线 / LoopEditor |
| `loop.loopCount/condition/breakCondition` | node.data.loopMeta | LoopEditor |
| `boolean.trueNext/falseNext` | edges：source-handle = `true`/`false` | 拖线 / BooleanEditor |
| `boolean.condition` | node.data.condition | BooleanEditor |
| `weighted.options[i]` | edges：source-handle = `opt-${i}`, edge.label = weight | 拖线 / WeightedEditor |
| `wait.waitMs` | node.data.waitMs | WaitEditor |
| `action.action` | node.data.action（actions map 的 key 字符串） | ActionEditor |
| `action.breakOff/listenCallbacks/delayMs` | node.data.* | ActionEditor |

### 18.2 ActionDef 字段 → ActionEditor 控件

| ActionDef 字段 | 控件 | 适用 pattern |
|---|---|---|
| `pattern` | PatternSelector（下拉） | 全部 |
| `service` | 文本（自动补全） | 多数 |
| `route` | JSON 输入 / `{cmd, act}` 表单 | tcp/udp 系 |
| `c2sProto` | ProtoBrowser 选择 | tcpSend/tcpRequest/udpSendProto |
| `s2cProto` | ProtoBrowser 选择 | tcpRequest/waitListen |
| `bindings` | BindingsTable | tcpSend/tcpRequest/udpSendProto/setState |
| `store` | StoreTable | tcpRequest/waitListen |
| `address` | 文本（支持 `state:key`） | connect/connectUDP |
| `target` | 单选（tcp/udp） | close |
| `keys` | TagInput | clearState |
| `timeout` / `pollMs` | 数字 | waitListen |
| `optional` | 开关 | waitListen |
| `secretArg` | 文本 | exchangeKey |
| `script` | LuaForm（mode='action'） | lua |
| `listenCallbacks` | ListenRefsTable（§8.6） | 任意（最常见 connect / connectUDP） |

### 18.3 CallbackDef 字段 → CallbackEditor 控件

| CallbackDef 字段 | 控件 | 适用形态 |
|---|---|---|
| (形态切换) | CallbackTabs（silent / declarative / lua） | 全部 |
| `s2cProto` | ProtoBrowser 选择（可空 = 原始 binary） | declarative / lua |
| `store` | StoreTable（与 ActionDef 共用） | declarative |
| `script` | LuaForm（mode='callback'，签名 `onMessage(r, msg)`） | lua |
| (反向引用) | BackrefList | 全部（只读） |

### 18.4 ListenRef 字段 → ListenRefsTable 控件

| ListenRef 字段 | 控件 | 备注 |
|---|---|---|
| `route` | RouteEditor（§8.7） | `{cmd, act}` 双字段 / JSON 双模式 |
| `server` | 下拉（`tcp:` / `udp:` 分组） | 服务名候选来自 flow 内出现的 service |
| `callback` | 下拉 + `[+ 新建]` + `[→ 跳转]` | 含 `(null) — 静默丢弃` 选项 |
| (派生) 形态徽章 | 只读，跟随 callback 当前形态实时变化 | silent / declarative / lua |

---

## 19. 开发约定与编码风格

- **组件命名**：PascalCase；文件名与默认导出一致。
- **目录组织**：按"功能纵切"组织（不按"组件 / 容器 / hook"横切）。
- **样式**：CSS Modules + 设计 token；避免内联 style，除非强动态。
- **类型导入**：`import type` 区分类型 / 运行时。
- **测试**：`*.spec.ts` 同目录；Vitest。
- **注释**：业务/约束/为什么用注释，"做了什么"靠代码自解释（与 Go 端 `CLAUDE.md` 风格一致）。
- **国际化**：本期仅简体中文；UI 字符串集中放 `i18n/zh.ts`，便于后续接入 i18next。

---

## 附录 A — 项目骨架初始化命令清单

```bash
cd e:/jump/Server-Jump/stressbot
mkdir web && cd web

npm create vite@latest . -- --template react-ts
npm install

# 核心依赖（锚定主版本号，子版本随 npm 解析最新）
npm install @xyflow/react@^12 antd@^5 zustand@^5 nanoid@^5 \
            @monaco-editor/react@^4 protobufjs@^7 \
            idb-keyval@^6 dagre@^0.8 zundo@^2

# 开发依赖
npm install -D @types/dagre vitest @vitest/ui \
               eslint prettier eslint-config-prettier \
               eslint-plugin-react eslint-plugin-react-hooks
```

> **变更说明**：
> - `ajv` 已下线（详见 §2.1）；schema 校验由 `validation/refsCheck.ts` 等价覆盖，未再引入新依赖。
> - **Zustand 选择器须配合 `useShallow`**：当 selector 返回新对象（如 `s => ({ nodes: s.nodes, actions: s.actions })`）时，必须用 `import { useShallow } from 'zustand/react/shallow'` 包裹，否则会因引用每次都变触发 React `Maximum update depth exceeded`。这条在多个 panel 与 callbacks 模块中均已落地。
> - **`zundo`** 是 zustand 的 temporal 中间件实现；当前 `store/undoRedo.ts` 在其上做了简单封装，仅按需打快照，避免每次 hover 也写 history。

---

## 附录 B — 与现有项目文档的衔接

| 现有文档 | 本设计的关系 |
|---|---|
| `docs/redesign-flow-nodes.md` | 节点类型与 JSON 格式的**唯一权威来源**；本编辑器的 TS 类型必须与之同步 |
| `docs/design-monitor.md` | 监控数据 schema 的来源；§12 中的 `NodeMetrics` 与之对齐 |
| `docs/refactor-adapter-layer.md` | adapter 层（codec.lua / route）；编辑器只需把 `route` 当作不透明结构透传 |
| `.claude/skills/flow-config/SKILL.md` | 编辑器内置文档与提示语的来源；保存编辑器内"帮助"按钮跳转该文档 |
| `CLAUDE.md`（项目根） | 验证流程对齐；前端 README 中重申"导出后必须 `go run ./cmd/validate` 通过" |

---

> 本设计文档随实现迭代演进。**最近一次对齐：2026-05-09**，主要更新：补充 §10.3 FlowManagerModal 流程库、§20.3a TaskStartModal 调试模式、§20.3b ActiveTaskGuardModal、§20.8 scriptSync 脚本同步服务、§20.9 resourcesStore 双 DB 资源管理、HistoryDrawer → HistoryModal 重命名；§20 分布式集成从单页编辑模式扩展为完整压测控制台。
>
> 任何与实际代码偏离之处以代码为准；新增重大设计偏离时在对应章节顶部追加"已变更：日期 + 摘要"补丁说明，避免推翻全章。

---

## 20. 分布式集成（HomeShell — 单页压测控制台）

> **变更说明（2026-04-30）**：依据用户反馈"将所有功能集成到单页"，FlowEditor 不再是裸编辑器，而是被嵌入到 `HomeShellInner`（`pages/EditorPage.tsx`）这个统一页面中，画布上方接 `RuntimeBar`，下方接 `MonitorDock`，左右各种 Drawer 通过按钮触发。原 §11/§12 中的"占位接口"在此章节落地。
>
> 与 stressbot Admin（默认 `:8080`）的交互见 `docs/api-monitor.md`，本章节只讲前端如何编排。

### 20.1 架构层次

```
HomeShellInner (pages/EditorPage.tsx)
├── RuntimeBar (components/runtime/RuntimeBar.tsx)        ─── topbarExtra 注入到 FlowEditor 顶栏
├── FlowEditor (readOnly = mode != 'edit')
│   └── NodeShell + MetricsBadge ← metricsProvider (派生自 latestStress)
├── MonitorDock (6 Tabs · 可拖拽折叠)
└── Drawers
    ├── ResourcesDrawer  proto/lua 资源
    ├── HistoryModal     list / detail / compare（Modal 形态，原 HistoryDrawer 已重构为 Modal）
    └── AgentsDrawer     节点状态 + 全部停止（无升级；版本部署改为运维手动重启 Agent）
```

### 20.2 RuntimeMode 状态机（`services/runtimeStore.ts`）

| Mode | 触发 | 行为 |
|---|---|---|
| `edit` | 默认；`stopTask` 完成 N 秒后 | FlowEditor 完全可编辑；仅轮询 agents/system（10s）显示集群容量 |
| `viewActive` | 进入页面发现已有 active 任务且用户选"查看运行中" | FlowEditor readOnly；高频轮询 task/metrics（5s） |
| `running` | 当前会话刚 `startTask` 成功 | 同上；`ownedTaskId` 标记为本会话所有 |
| `finalReport` | 任务终态（stopped/failed） | 停止轮询；保留最后一份 snapshot；提示用户进 history 或新建任务 |

`pollingPolicy(mode)` 集中决定是否启用 task / stress / system / agents 四组轮询及间隔。

### 20.3 任务生命周期（`services/taskActions.ts`）

```
edit ──[startTask]──> running ─轮询task─> task.state=stopped/failed
                                        └──> finalReport ──[新建任务]──> edit

         viewActive (其他客户端启动) ──同上
```

- **startTask**：`flowStore.exportFlow()` → 校验通过 → 收集 `resourcesStore` 的 proto/lua → `tasksApi.createTask` (multipart) → `tasksApi.startTask` → 切到 `running`。其中容量校验在前端先估算一次（`agents.maxBots` 求和），后端最终决定。
- **stopTask**：`tasksApi.stopTask`；轮询持续直到 stopped。
- **attachToActive**：`tasksApi.getTask + getHistoryConfig`（如果是 owner）→ `flowStore.loadFromTaskFlow`。本地未保存的草稿先 stash 到 LocalStorage，离开 viewActive 时可一键还原。

### 20.3a 启动任务弹窗 — TaskStartModal（两阶段参数复核 + 调试模式）

> **新增（2026-05-09）**：`components/runtime/TaskStartModal.tsx` — 启动压测任务的确认弹窗，采用"参数复核 → 提交"两阶段 UX，确保用户在提交前看清所有关键参数。

**两阶段 UX**：

1. **复核阶段**（弹窗打开）：展示任务名、机器人数、Auth 地址、并发数、资源清单（proto/lua 文件数量）、容量预检结果。
2. **提交阶段**（点击启动）：调用 `services.startTask`；成功后回调关闭 modal，失败由 `showApiError` 接住并展示。

**调试模式**（`editorStore.debugMode`）：

弹窗顶部提供 **测试 ↔ 调试** 二选一 `Segmented` 控制，持久化到 `localStorage`（`stressbot:debugMode`）：

| 维度 | 测试模式（默认，蓝色） | 调试模式（紫色） |
|---|---|---|
| totalBots / concurrency | 用户填写 | 自动装填 `1` / `1` |
| logLevel | 用户选择（默认 info） | 自动装填 `debug` |
| skipCapacityCheck | `false`（容量不足阻塞启动） | `true`（跳过容量预检，让服务端兜底） |
| 任务名 | 用户填写 | 如果当前为占位名则自动填 `debug · MMDD-HHmm` |
| 适用场景 | 正式压测 | 本地快速验证流程是否跑通 |

- 模式切换**不回滚**已填数值（保留用户偏好），仅在首次切入调试时主动装填一次（`useRef` 哨兵防重复装填）。
- 0 个在线 Agent 时无论哪种模式都**禁用启动按钮**（前端最低门槛）。

**Auth 扩展字段编辑器**（`AuthExtraEditor`）：

高级设置折叠面板中的 `robotConfig.authExtra`（`Record<string, string>`）提供可视化键值对编辑，而非原始 JSON 文本框。lua 脚本通过 `robot.get(key)` 读取；常用字段如 `version` / `channel` / `platform`。

**资源同步**：

弹窗打开时自动执行 `syncFlowScriptsToIdb`（详见 §20.8），把 flow 引用但 IDB 缺失的脚本从默认基线拉回。同步完成后展示 proto/lua 文件数量，以及缺失脚本数（缺失时禁止启动）。

### 20.3b 活动任务守卫 — ActiveTaskGuardModal

> **新增（2026-05-09）**：`components/runtime/ActiveTaskGuardModal.tsx` — 页面加载时检测到已有 active 任务的引导弹窗。

**触发时机**：`HomeShellInner` 启动时（boot 阶段）调用 `tasksApi.listTasks()`，若发现 `state ∈ {starting, running, stopping}` 的任务，设置 `guardTask` state 触发弹窗。

**用户选择**：

| 选项 | 行为 |
|---|---|
| 「查看运行中」 | 调用 `attachToActive(taskId)`；runtimeStore mode → `viewActive`；本地草稿 stash 到 LocalStorage；画布替换为该任务的 flow 并锁定为只读 |
| 「继续编辑」 | 关闭弹窗，留在 `edit` 模式；**启动按钮禁用**（RuntimeBar 显示 tooltip 提示"集群已有任务在执行"） |

关闭弹窗（点 X / mask）等价于"继续编辑"。

弹窗中展示任务详情：任务名、任务 ID、状态（彩色 Tag）、机器人数 + Agent 分布数、启动时间。

### 20.4 节点级监控（T4）

`metricsBinding.buildNodeMetricsMap(snapshot, flow)` 把 `StressSnapshot.actions[]` 按以下规则映射到节点 ID：

- 名字以 `callback:` 开头 → 虚拟节点 ID `__cb__<name>`（CallbackCard 监听）
- 其它 → 凡是 `flow.nodes[i].action == metric.name` 的节点都映射；多个节点共享同一 action 时全部命中

`makeMetricsProvider(map)` 包装为 `(nodeId) => ActionMetric | undefined` 注入 `useMetricsStore.setProvider`，所有 NodeShell 自动消费：

- 左下角 `executing` 角标（脉动）
- 节点边框按 Apdex 染色（excellent 绿 / good 浅绿 / fair 黄 / poor 橙 / danger 红，5 级）
- 底部 metrics 槽显示 `exec N · p99 12ms · A 0.92 · err 3`（带 Tooltip 详情）

校验红线优先级 > Apdex 染色，避免覆盖。

### 20.5 监控面板 MonitorDock（T5）

`components/monitoring/MonitorDock.tsx` — 底部停靠面板，6 个 Tab：

| Tab | 内容 | 数据源 |
|---|---|---|
| 大盘 | 4 张状态卡（机器人/连接/带宽/集群） | latestStress + latestSystem |
| 动作 | 每个 ActionMetric 一行（名/exec/sample/成功率/Apdex/QPS/延迟分布/p99/错误） | latestStress.actions |
| 错误 | 跨动作错误聚合，按 count 倒排 | latestStress.actions[].errors |
| 趋势 | 4 张 echarts 折线（机器人/QPS/CPU/带宽） | runtimeStore.{stressHistory, systemHistory} 滑窗 60 点 |
| per-Agent | 每 Agent 加权 apdex/成功率/CPU/MEM/Goroutine | metricsApi.getPerAgentMetrics + getPerAgentSystem（按需拉） |
| 系统 | 集群拓扑 + CPU/MEM/NET/Goroutine 详情 | latestSystem + agents |

可拖拽顶部把手调整高度（160~80vh），落点持久化到 LocalStorage；编辑态自动折叠成 32px 条，运行态自动展开。

### 20.6 跨模块 Drawer（T6）

| Drawer | 关键能力 |
|---|---|
| ResourcesDrawer | 上传 / 删除 / 清空 / 默认基线导入；写入 IndexedDB；`protoStore.reload()` 触发提示 |
| HistoryModal | list（搜索/收藏/分页） / detail（备注/标签/趋势/动作汇总/克隆/下载归档） / compare（2~5 个并排比较）；原 HistoryDrawer 已重构为 Modal 形态（`components/modules/history/HistoryModal.tsx`） |
| AgentsDrawer | 表格（状态/任务/CPU%/MEM%/心跳） · 「全部停止」按钮（等价于停止当前 active 任务） · 离线删除 |

错误处理统一过 `services/errorHandler.ts`：通用错误 message.error；`TASK_CONFLICT` 走 Modal.confirm 让用户选择"查看运行中"或"留在编辑态"。

### 20.7 Lua IntelliSense（T8）

- **luaApiSpec.ts**：7 个模块（robot/network/proto/utils/json/log/adapter）× 53 个函数的结构化 API；维护原则是"stressbot/script/api_*.go 改了 → 这里同步改"
- **luaProviders.ts**：在 LuaForm onMount 时给 Monaco 注册 completion / hover / signature 三个 provider；防重入（registered 全局标志）
- **luaSyntaxWorker.ts**：Web Worker 跑 luaparse；`mode='action'` 校验 `function execute(r)`，`mode='callback'` 校验 `function onMessage(r, msg)`；返回 SyntaxIssue[] 写回 Monaco markers（红/黄波浪线）
- **luaSyntaxClient.ts**：单例 Worker 客户端，去抖（LuaForm 中 400ms）+ 请求覆盖

LuaForm 顶部 Alert 同时显示错误数 + 警告数 + 前 5 条详情；Worker 失败时退化为旧字符串包含校验，不让面板完全没提示。

### 20.8 脚本同步服务 — scriptSync（`services/scriptSync.ts`）

> **新增（2026-05-09）**：自动把 flow 引用的 Lua 脚本与 IndexedDB 同步，保证启动任务时所有被引用脚本都存在。

**设计目标**：

- **单一事实源**：`flow → 引用的脚本`是唯一来源，IDB 只是用户编辑稿/本地副本。
- **保护用户编辑稿**：IDB 中已存在的脚本**永不覆盖**（即使内容与基线不同）。
- **兜底拉取**：IDB 中没有的脚本从 `/conf/scripts/<name>` 拉取默认基线（开发期由 Vite `confMountPlugin` 提供）。

**调用时机**：

| 调用点 | 说明 |
|---|---|
| Toolbar 导入 JSON / 加载 conf/flow.json 后 | 自动把"引用了但 IDB 没有"的脚本从基线拉回 |
| EditorPage 初始化默认 flow 后 | 同上 |
| TaskStartModal 弹窗打开时 | 最后一道兜底；若仍有缺失则禁止启动 |

**脚本名扫描范围**（`collectFlowScriptNames`）：

| 来源字段 | 说明 |
|---|---|
| `actions[].script` | 动作节点 lua 模式 |
| `callbacks[].script` | listen 回调 lua 模式 |
| `nodes[].condition`（`lua:` 前缀） | boolean / loop 前置条件 |
| `nodes[].breakCondition`（`lua:` 前缀） | loop 后置条件 |

仅扫静态字段；脚本内部的 `require('xxx')` / `dofile()` 是动态的，无法静态分析，由用户在资源管理中手动上传。

**返回结果**（`ScriptSyncResult`）：

```ts
interface ScriptSyncResult {
  added: string[];    // 本次从基线拉回并写入 IDB 的脚本名
  skipped: string[];  // IDB 已有，未做任何操作（保护用户编辑稿）
  missing: string[];  // 基线也拉不到的脚本名（启动会失败）
}
```

### 20.9 IndexedDB 资源管理 — resourcesStore（`services/resourcesStore.ts`）

> **新增（2026-05-09）**：管理用户上传的 proto / lua 资源文件，采用双 DB 架构。

**双 DB 架构**：

由于 `idb-keyval` 的限制（每个 DB 只能挂一个 object store，不会触发 version upgrade 加 store），proto 和 lua 各使用一个独立数据库：

| 数据库 | Object Store | 内容 |
|---|---|---|
| `stressbot-resources-proto` | `data` | 用户上传的 .proto 文件 |
| `stressbot-resources-scripts` | `data` | 用户上传/编辑的 .lua 脚本 |

**ResourceFile 数据模型**：

```ts
export interface ResourceFile {
  name: string;        // 文件名（作为 IDB key）
  content: string;     // utf-8 文本内容（非 ArrayBuffer，方便 Monaco 直接使用）
  size: number;        // 字节长度
  uploadedAt: string;  // ISO 时间戳
}
```

**Lua 脚本基线版本自动清除机制**：

当引擎对 Lua 脚本返回值的契约发生不向后兼容的破坏性变更时（例如 v1 → v2 切换为三元组返回），需 bump `SCRIPT_BASELINE_VERSION` 常量。浏览器加载时比对 LocalStorage 中保存的版本号：

- 不匹配 → 一次性清空 `stressbot-resources-scripts` 中所有数据（用户编辑稿与基线副本一并丢弃）
- 写入新版本号到 LocalStorage，避免重复清空
- 下次进入 LuaForm 或启动任务时，由 `scriptSync` 的"IDB miss → fetch /conf/scripts/<name>"路径自动拉新版基线

设计取舍：清空策略会丢失用户编辑过的本地稿，但 IDB 本身不是云端存储，换取"基线升级零手工操作"的体验。

**变更订阅**（`subscribe` 模式）：

暴露 `subscribe(fn)` 给 React 组件订阅"资源变更"事件（配合 `useSyncExternalStore`）。所有写操作（`addProto` / `removeProto` / `addScript` / `removeScript` 等）完成后触发 `notify()`，订阅方自动重算。

**Legacy 迁移**（v0 → v1）：

v0 版本使用同一 `stressbot-resources` DB 同时挂 proto / scripts 两个 store，触发 IDB "One of the specified object stores was not found" 错误。模块加载时自动检测旧 DB，把 proto 数据搬入新 DB，然后删除旧 DB。迁移失败静默（旧 DB 不存在 / 已损坏都按"无需迁移"处理）。

### 20.10 共享设计原则

1. **Mode 驱动 UI**：所有"什么时候能编辑 / 什么时候轮询 / 什么时候默认展开"全部由 `runtimeStore.mode` 决定，避免散在组件里
2. **Provider Bridge**：`metricsProvider`、`MetricsProvider`、`registerTaskConflictHandler` 都用回调注入，让 services 层不依赖任何 UI 组件
3. **轮询与展现分离**：HomeShell 集中创建 4 个 `usePolling`，写入 runtimeStore；Drawer/Tab 仅消费 store，便于 mock / 离线测试
4. **资源优先级**：Proto/Lua 加载顺序 `IndexedDB > /conf > 编译时 fallback`；用户上传文件后 `protoStore.reload()` 立即生效
5. **草稿保护**：进 viewActive 前自动 stash localStorage；finalReport / 用户主动"恢复编辑稿"按钮一键还原

### 20.11 测试覆盖（截至本次提交）

- 22（codec / refsCheck / refsGraph）
- 17（metricsBinding：节点映射 / 多节点共享 action / callback 卡片 / Apdex 边界）
- 19（luaApiSpec 元数据完整性 11 + luaEntryCheck 8）
- **合计 58 个用例，全部通过**；`npm run build` 通过（主 bundle 3.5MB / luaSyntaxWorker chunk 27KB 独立）
