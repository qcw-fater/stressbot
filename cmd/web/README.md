# stressbot · Web 前端

stressbot 的浏览器端管理界面：可视化流程编辑器 + 资源管理 + 任务启停 + 实时监控 + 历史归档 + 节点管理的单页应用。

生产形态由 Admin 服务器静态托管（`server.staticDir`，默认 `cmd/web/dist`，挂在 `/stressbot/` 子路径），所有 API 走 `/sbot/` 前缀；开发形态用 Vite dev server 本地起页面、把 `/sbot/` 代理到 Admin。

后端引擎、flow.json 数据模型、Lua API 等全栈文档见[根 README](../../README.md)，本文件只覆盖前端。

---

## 功能一览

| 模块 | 入口 | 说明 |
| --- | --- | --- |
| 流程编辑器 | 主区 `FlowEditor` | 节点式 DAG 画布（9 种业务节点 + listen 卡片、7 种边），拖拽建节点、拖 handle 连线、双击开编辑器 |
| 节点编辑 | 双击节点 → `NodeEditorDrawer` | 6 种控制流编辑器 + ActionEditor（14 pattern × 17 binding type） |
| 监听子系统 | 工具栏 →「监听」 | ListenDef 三态（silent / declarative / lua）、ListenCard 画布卡、反向引用、重复注册检测、route 三形态编辑 |
| 协议配置 | RuntimeBar →「协议配置」 | `*_codec.json` 结构化编辑（帧布局 / pipeline / 心跳 / routeKey 模板）+ `errors.json` 错误码映射，预览走后端 codec 引擎 |
| 资源管理 | RuntimeBar →「资源」 | Proto / Lua 文件上传、Monaco 编辑、与服务器基线同步（DiffEditor 逐项解决三方冲突） |
| 任务管理 | RuntimeBar「启动」→ `TaskStartModal` | 参数复核 + 资源清单 + 容量预检 + 提交；测试 / 调试双模式（调试自动 1 机器人 1 并发 debug 日志） |
| 实时监控 | 底部 `MonitorDock` | 动作指标表（成功率 / 延迟分位 / Apdex / 错误下钻 / CSV 下载）+ 集群系统资源仪表盘，指标可回绑画布节点徽章 |
| 历史归档 | RuntimeBar →「历史」 | 列表 / 详情 / 对比三视图（2~5 条对比）、备注标签收藏、克隆重跑、归档下载、离线 HTML 报告导出 |
| 节点管理 | RuntimeBar →「节点」 | 压测节点（Agent）卡片：状态 / CPU / 内存 / 容量、强制注销、全部关闭 |
| 流程库 / 模板库 | 工具栏「流程库」/ 左侧栏 | 组件模板（action / listen）与整份流程（flow + layout）均存服务器 MySQL，跨流程复用 |
| 校验 | 工具栏 →「校验」 | 86 种规则 code（引用完整性 / action / binding / 条件语法）+ JSON Schema 校验，报告抽屉三 Tab + 节点角标 + 跳转定位 |
| 撤销重做 | `Ctrl+Z` / `Ctrl+Shift+Z` | zundo 时间旅行，limit 50，只记业务字段不记视图 |
| 编辑稿持久化 | 自动 | LocalStorage 300ms 防抖写盘 + 关页前 flush，刷新恢复 |
| 配置备份恢复 | 文件菜单 | 全量配置导出 / 导入、逐节冲突解决、崩溃后 `RecoveryGuard` 自动恢复 |
| 笔记 | RuntimeBar →「笔记」 | Monaco 笔记浮窗，IndexedDB 持久化 |

---

## 快速开始

```bash
cd cmd/web
npm install
npm run dev           # http://localhost:5173/stressbot/（/sbot/ 代理到 Admin）
npm run build         # tsc -b + vite build → dist/
npm run type-check    # tsc -b --noEmit
npm run lint          # ESLint + prettier --check
npm run format        # prettier --write
npm run test          # Vitest 单测（104 文件 / 770 用例）
npm run test:ui       # Vitest UI
npm run generate:api  # 从 ../../api/admin/openapi.yaml 重新生成 src/generated/admin-api.ts
```

开发依赖一个运行中的 Admin（默认 `http://localhost:7718`，可在 `.env.development.local` 用 `STRESSBOT_ADMIN` 覆盖代理目标）：

```bash
# 仓库根目录另开终端
go run ./cmd/admin -config conf/admin.toml
```

页面是单页应用无路由库：`App.tsx → EditorPage` 一个入口，其余全是浮层（Drawer / FloatingWindow）。

### 环境变量

| 变量 | 默认 | 用途 |
| --- | --- | --- |
| `VITE_API_PREFIX` | `/sbot` | 管理 API 前缀 |
| `VITE_BASELINE_PREFIX` | `/sbot/baseline` | 基线资源读取前缀 |
| `VITE_ROBOT_*`（9 个） | — | 创建任务表单 `RobotConfig` 初值覆盖（`ACCOUNT_PREFIX` / `MAIN_SERVICE` / `CONCURRENCY` / `TIMEOUT_SEC` / `HEARTBEAT_SEC` / `HTTP_TIMEOUT_SEC` / `APDEX_T` / `START_NUMBER` / `LOG_LEVEL`） |
| `STRESSBOT_ADMIN` | `http://localhost:7718` | 仅 dev：Vite 代理目标（写在 `.env.development.local`，无 `VITE_` 前缀、不进前端包） |

模板见 `.env.example`，生产默认值见 `.env.production`；前端唯一读取处是 `src/services/env.ts`。

---

## 技术栈

| 库 | 用途 |
| --- | --- |
| React 18 + TypeScript 5.6 | UI 框架 |
| Vite 8 | 构建 / dev server / Vitest |
| Ant Design 5 | 组件库（中文 locale，暗色 algorithm 切换） |
| @xyflow/react 12 | 流程画布（React Flow） |
| Monaco Editor | JSON / Lua / Proto / 笔记 / Diff 编辑器（本地打包，不走 CDN） |
| Zustand 5 + zundo | 状态管理 + 撤销重做 |
| @tanstack/react-query 5 | 服务端状态与轮询 |
| ECharts 6 | 监控图表 / 系统仪表盘 |
| protobufjs | 前端 proto 解析（字段补全 / 校验） |
| idb-keyval | IndexedDB 持久化（资源 / 笔记 / 恢复日志） |
| dagre | 流程图自动布局 |
| luaparse | Lua 语法检查（Web Worker 内） |
| react-rnd | 浮动窗口拖拽缩放 |
| Ajv | flow / codec JSON Schema 校验 |
| openapi-typescript + openapi-fetch | API 契约类型生成与类型安全调用 |

---

## 目录结构

```
src/
├── App.tsx / main.tsx            # 入口：QueryClientProvider + ConfigProvider（中文/暗色）
├── pages/
│   └── EditorPage.tsx            # 唯一页面（HomeShell）：编排 FlowEditor + RuntimeBar +
│                                 #   各功能浮层 + 按模式轮询；boot 时探测历史服务与活跃任务
├── components/
│   ├── FlowEditor/               # 流程编辑器核心（详见下节）
│   ├── modules/                  # 编辑器之外的独立模块
│   │   ├── ResourcesDrawer.tsx   #   资源抽屉（Proto / Lua）
│   │   ├── BaselineSyncModal.tsx #   基线同步冲突（Monaco DiffEditor 三方合并）
│   │   ├── ProtocolConfigEditor.tsx # 协议配置面板（codec.json / errors.json 分区）
│   │   ├── codecEditor/          #   声明式 codec 结构化编辑（帧布局/pipeline/心跳/routeKey/预览）
│   │   ├── ErrorMapEditor.tsx    #   errors.json KV 编辑器（业务码 ≥ 100）
│   │   ├── history/              #   历史归档（list / detail / compare + 离线 HTML 报告）
│   │   ├── configTransfer/       #   配置备份 / 恢复 / 冲突解决 / 崩溃恢复
│   │   ├── AgentsPanel.tsx       #   压测节点面板
│   │   └── notepad/              #   笔记浮窗
│   ├── monitoring/               # MonitorDock 监控坞：动作指标表 / 系统仪表盘 / 共享口径
│   ├── runtime/                  # RuntimeBar 运行控制条 / TaskStartModal / 活跃任务引导
│   └── shared/monaco/            # Monaco 懒加载门面（本地 chunk）
├── services/                     # 服务层（组件禁止直接 fetch，见下节）
├── generated/                    # openapi-typescript 产物（admin-api.ts，勿手改）
├── types/                        # 镜像后端 Go 结构的 TS 类型（flow.ts ↔ engine/flow.go 等）
├── styles/                       # tokens.css（设计 token + 暗色覆盖）+ global.css
├── shims/                        # protobufjs inquire → 本地 Long（免运行时动态 require）
└── test/                         # Vitest setup（matchMedia / ResizeObserver stub）+ 冒烟
```

---

## 页面架构与运行模式

`EditorPage`（HomeShell）持有一个单向模式状态机（`services/runtimeStore.ts`）：

```
edit ──启动任务──▶ starting/running ──停止──▶ stopping ──▶ finalReport
  ▲                                                   │
  └──────────── 查看活跃任务 viewActive ◀─────────────┘
```

- **edit**：编辑态，后台 10s 轮询节点列表 / 系统资源；检测到活跃任务弹 `ActiveTaskGuardModal` 引导进入查看。
- **viewActive / running**：5s 轮询任务详情 / 压测指标 / 系统资源 / 节点列表；FlowEditor 进入只读模式。
- **finalReport**：终态报告，仍只读，可跳转历史详情。

RuntimeBar 是顶部运行控制条（状态徽章、启停、资源/历史/节点/系统/笔记/协议配置六个入口、设置），通过 `FlowEditor` 的 `topbarExtra` 插槽挂在工具栏右侧。除资源抽屉外的功能面板全部 lazy + `LazyMount`（关闭即卸载）。

---

## FlowEditor

```
FlowEditor/
├── index.tsx                # 对外出口：Provider 装配 + 全局快捷键 + 初始加载链
├── FlowCanvas.tsx           # ReactFlow 主画布（拖入/连线/右键菜单/快捷键）
├── CanvasContextMenu.tsx    # 右键菜单（复制/剪切/粘贴/存模板/删除/新建）
├── flowReadOnlyContext.ts   # 只读 Context
├── nodes/                   # 9 种节点组件 + registry + onError 角标 + NodeShell/MetricsBadge
├── edges/                   # 7 种边 + registry + edgeStyle
├── editors/                 # NodeEditorDrawer 按类型路由 → 6 种控制流编辑器 + ActionEditor/
│   └── shared/              # ConditionInput / DelayInput / LuaScriptField / StateExprInput 等
├── listens/                 # 监听子系统（面板/卡片/编辑器/引用图/route 编辑）
├── codec/                   # jsonToFlow / flowToJson / dagreLayout / 连接路由模型
├── validation/              # 校验规则 + 协调器（防抖 150ms）+ 报告抽屉
├── library/                 # 组件模板库门面（服务器 MySQL）
├── proto/                   # ProtoLoader / ProtoRegistry / ProtoBrowser
├── lua/                     # Lua 补全 / hover / 语法 Worker / API 参考面板
├── panels/                  # Toolbar / NodePalette / FlowManagerModal / FloatingWindow
├── store/                   # flowStore / editorStore / floatingWindowStore / persistDraft / flowHistory
├── preview/                 # JSON 预览（Monaco 只读）
└── utils/                   # nodeIdGen（nanoid）/ normalizeOnError
```

### 画布与节点

节点 10 种（`nodes/registry.ts`）：`sequence` / `action` / `loop` / `boolean` / `switch` / `weighted` / `wait` / `break` / `continue` + `listenCard`（监听卡片，id 为 `__cb__<name>`）。边 7 种（`edges/registry.ts`）：`seq` / `branch` / `weight` / `loopBody` / `listen` / `error` / `waitThen`。

连线即业务：从 source handle 拖到 target handle 会直接改写业务字段（如 sequence 的 `next` 插入、boolean 的 `trueNext`、action 的 `onError.handler`），删除边则反向清除引用——业务图与视觉通过 `codec/jsonToFlow` / `flowToJson` 派生同步，`flowToJson(jsonToFlow(flow))` 圆周等价（有测试守护，用真实 `conf/flow/flow.json` round-trip）。

常用交互：

| 操作 | 行为 |
| --- | --- |
| 左侧栏拖入 / `F1`~`F10` | 在鼠标位置新建节点（F10 新建 listen 卡片） |
| 双击节点 | 打开对应编辑器（break / continue 无编辑器） |
| `Ctrl+C` / `Ctrl+X` / `Ctrl+V` | 复制 / 剪切 / 粘贴节点（含 action / listen 引用数据，快捷键仅画布聚焦时生效） |
| `Delete` / `Backspace` | 删除选中节点 / 边（批量删除按 source 聚合防索引漂移） |
| 右键 | 节点 / 边 / 画布空白三套菜单（含粘贴落点、存为模板） |
| 选中边 | 两端节点联动高亮（按源节点类型着色） |
| 工具栏「自动布局」 | dagre 重排（LR，跳过 listen 边） |

### ActionEditor

14 种 pattern 分组下拉（请求-响应 / 发送 / 连接管理 / 监听 / 状态操作 / Lua），按 pattern 动态渲染表单：

- **声明式表单**：service / route / c2s / s2c proto、字段绑定表（17 种 binding type，含 `condition` 条件绑定与 `filters` 过滤器）、StoreMapping（S2C → state 路径）、`setState` / `clearState` 摘要卡片、`onError` 错误链路（ignoreCodes / handler / retry / strategy）。
- **Lua 表单**：脚本选择 + Monaco 编辑（补全 / hover / 语法检查）。
- **辅助**：proto 字段选择器、state key 候选（flow 图 + 启动配置 + Lua 脚本三方收集）、消息级预览、监听注册表（`listenRefs`：route + server + listen 三列，支持批量粘贴 JSON）。

### 监听子系统（listens/）

- `ListenDef` 三态：`silent`（`{}`，仅注册队列）/ `declarative`（s2cProto + store 自动存储）/ `lua`（script 回调），三态切换带确认与 30s 内切回恢复。
- `ListenPanel` 列表：形态徽章 + 引用计数（0 引用橙色「孤儿」标识）；`ListenCard` 画布浮卡可拖拽布局。
- `BackrefList` 反向引用：从 listen 视角查哪些 action 的哪条 listenRef 引用了我，可就地改连接 / route / queueSize。
- route 编辑按 codec 的 `routeKeyTemplate` 占位字段生成表单，三种形态（完整表单 / 行内轨道条 / 浮动窗口）共用一个 `routeFormModel`。

### 校验（validation/）

86 种规则 code：`refsCheck`（流程级引用 / 环 / 孤儿 / listenRefs，42）+ `actionRules`（action 语义，24）+ `bindingRules`（绑定与过滤器，19）+ 条件语法（1）；另有 Ajv 对后端 `config/validation/flow.schema.json` / `codec.schema.json` 的 Schema 校验。编辑停 150ms 自动跑，结果两处呈现：校验报告抽屉（errors / warnings / infos 三 Tab，「跳转」按钮 pan 到节点并打开编辑器）与节点外壳角标（按节点分组）。

### 持久化与历史

| 数据 | 位置 | 机制 |
| --- | --- | --- |
| 编辑稿（flow + layout） | LocalStorage | 业务字段变更 300ms 防抖写盘 + `beforeunload` flush；初始加载优先级 initialFlow > 草稿 > 服务器基线 |
| 撤销重做 | zundo temporal | limit 50，partialize 只存 4 个业务字段，加载 / 重置时清空历史 |
| 组件模板（action / listen） | 服务器 MySQL | `templatesApi`，snapshot + revision 乐观并发；入口按 capabilities 显隐 |
| 流程模板（flow + layout） | 服务器 MySQL | `flowsApi`，未配置 MySQL 时返回 `FLOW_LIBRARY_DISABLED` |
| 主题 / 调试模式 | LocalStorage | `stressbot:theme` 等 |

### 只读模式

`viewActive` / `running` / `finalReport` 三态下 `FlowEditor` 收到 `readOnly`：画布禁拖拽 / 禁连线 / 删除键失效、快捷键与右键菜单写操作全部旁路、工具栏编辑类按钮禁用、左侧节点栏隐藏。

### 对外接口

```ts
interface FlowEditorProps {
  initialFlow?: FlowJson;                     // 初始 flow.json
  initialLayout?: FlowLayout;                 // 视觉位置元数据
  autoLoadDefault?: boolean;                  // 自动拉取基线 flow（默认 true）
  metrics?: ReadonlyMap<string, ActionMetric>;// 节点指标徽章（不传则不显示）
  readOnly?: boolean;                         // 只读模式
  topbarExtra?: ReactNode;                    // 工具栏右侧插槽（RuntimeBar）
}
```

---

## 服务层与后端交互

**请求收拢约定**：所有 HTTP 走 `src/services/`（`api.ts` / `baselineApi.ts` 及各 `*Api.ts`），组件禁止直接 `fetch`。

- `api.ts`：统一 fetch 封装——非 2xx 一律抛 `ApiError`（保留 `code` / `status` / `details` 供 `TASK_CONFLICT`、`CAPACITY_EXCEEDED` 等分支），204 返回 `undefined`，`adaptList` 抹平后端裸数组与 `{items,total}` 两种列表形态。
- `generatedClient.ts` + `src/generated/admin-api.ts`：openapi-fetch 类型安全调用，类型由 `npm run generate:api` 从 [`api/admin/openapi.yaml`](../../api/admin/openapi.yaml)（浏览器管理 API 的唯一 OpenAPI 契约）生成。
- API 模块按域拆分：`tasksApi` / `agentsApi` / `metricsApi` / `historyApi` / `flowsApi` / `templatesApi` / `baselineApi` / `capabilitiesApi` / `codecApi`。
- 基线资源（磁盘 → 浏览器方向）全部经 `/sbot/baseline/*`：flow.json、proto / scripts / adapter 索引与单文件；返回三态（null = 确认不存在 ≠ 网络失败），删除对账只在 `missing` 上执行。
- React Query：`queryClient`（retry 2 且 AbortError 不重试、gcTime 10min、关窗不重取）+ 集中 `queryKeys` / `queryOptions`；`useRuntimeQueries` 按模式轮询（运行态 5s / 编辑态 10s），`useQueryHealthTracker` 上报连接健康并在终态回调。
- 任务启停是事务编排（`taskActions.ts`，失败回滚）；容量预检纯函数（`taskCapacity.ts`）须与后端分配逻辑保持一致。

### 资源管理与同步

Proto / Lua / 协议配置以浏览器 IndexedDB 为工作副本（`resourcesStore.ts`，三个独立 DB：`stressbot-resources-proto` / `-scripts` / `-adapter`），每份记录 `baseHash`（上次同步到服务器的哈希）做三方冲突判断：

- 打开资源抽屉显式「拉取」基线；本地有未同步修改且远端也变了 → `BaselineSyncModal` 用 Monaco DiffEditor 逐项确认保留本地 / 采用远端。
- 启动任务时收集全部本地资源 multipart 上传 Admin 持久化后下发节点；`scriptSync` / `protoSync` 做 gap-fill 同步（只补缺，不覆盖本地稿）。
- Proto 加载链三级：IndexedDB 本地 → 服务器基线 → 编译期内联仓库 `conf/proto/*.proto` 兜底（`import.meta.glob`）。

### 协议配置编辑

`ProtocolConfigEditor` 分两区：`*_codec.json`（每连接一份，帧布局 / 加解密 pipeline / routeKey 模板 / 声明式心跳结构化编辑 + 预览，预览请求走后端 `POST /sbot/codec/preview`）与 `errors.json`（共享错误码描述，业务码 ≥ 100 的 KV 编辑器）。

---

## 状态与持久化总览

| Store / 存储 | 内容 |
| --- | --- |
| `flowStore`（zustand + zundo） | flow.json 业务数据 + React Flow 视觉态 + 派生（listen 引用计数等），CRUD 带级联清理与引用同步 |
| `editorStore` | 编辑器 UI：选中 / 面板多开 / 剪贴板 / 主题 / 开关项 |
| `floatingWindowStore` | 浮动窗口多开：位置 / 尺寸 / zIndex / 视口夹紧 |
| `runtimeStore` | 集成层状态机（mode / activeTask / agents 等），仅启动表单字段持久化 localStorage |
| `validationStore` / `protoStore` / metrics 徽章 store | 校验报告、proto 加载状态、节点指标 |
| IndexedDB | 资源三库 + `stressbot-notepad` + `stressbot-config-recovery`（恢复日志） |
| LocalStorage | 编辑稿草稿、主题、监控坞高度等 UI 位置量 |

---

## 主题与样式

- `styles/tokens.css`：设计 token 单一事实源——节点配色（9 种节点各有主色）、间距、阴影；`:root[data-theme='dark']` 覆盖实现暗色。
- 主题切换：`editorStore.theme` 持久化 localStorage，写 `document.documentElement` 的 `data-theme`，antd 侧由 `main.tsx` 的 `theme.algorithm`（dark / default）同步；Monaco / 滚动条 / MiniMap 均有暗色适配。
- antd 固定中文 locale。

---

## 构建与部署

```bash
npm run build   # → dist/（tsc 类型检查 + vite build）
```

- `base: '/stressbot/'`：应用挂在子路径，dev 地址是 `http://localhost:5173/stressbot/`。
- sourcemap 默认关闭（体积翻倍），排查线上问题时 `npx vite build --sourcemap` 单独出带映射的包。
- 手动分包（`manualChunks`）按依赖族拆：monaco / react-flow / protobuf / react / echarts 按子模块 / antd + rc-* 多组，`__vitePreload` 助手钉在独立极小 chunk 防止首屏静态拉取 Monaco 本体。

部署两条路径：

1. **Admin 托管（默认）**：Admin 配置 `server.staticDir`（默认 `cmd/web/dist`）直接静态托管，前端与 API 同源。
2. **Nginx 独立部署**：参考 [`nginx.conf`](./nginx.conf)（80 端口多项目共用示例——`/stressbot/` 静态文件 + `/sbot/` 反代 Admin，静态资源一年强缓存）。

---

## 测试

Vitest + jsdom + @testing-library/react，setup 见 `src/test/setup.ts`（antd 所需的 `matchMedia` / `ResizeObserver` stub）。当前 **104 个测试文件 / 770 个用例**，与源码同目录共置（`*.test.ts(x)`）。

- 超时放宽到 20s：满负载并行时 jsdom 环境准备 + antd / Monaco 模块导入会把交互链拖过默认 5s，断言本身没问题（见 `vite.config.ts` 注释）。
- 代表性守护：codec round-trip 圆周等价（真实 flow.json）、refsGraph 引用一致性、configTransfer 恢复日志、容量预检与后端对齐。

```bash
npm run test              # 全量
npm run test -- codec.test  # 单文件
```

---

## 项目约定

- **请求收拢**：组件禁止直接 `fetch`，一律经 `services/`。
- **UI 文案不暴露技术术语**：Agent → 「节点」、Admin → 「服务器」、IndexedDB → 「本地存储」。
- **类型镜像后端**：`src/types/` 与 Go 结构（`engine/flow.go`、`protocol/codec/schema.go` 等）的 JSON 字段一一对应，改后端必须同步前端。
- **API 契约唯一**：浏览器管理 API 以 `api/admin/openapi.yaml` 为准，改动后跑 `npm run generate:api` 同步类型。
- **废弃能力物理删除**：不留 disabled 控件、兼容解析或注释掉的旧代码（历史 IndexedDB 模板库只保留数据不保留代码路径）。
- 前端验证流程：`npx tsc -b` + `npm run test` + 编辑器内打开 flow.json 查看校验报告。

## 相关文档

- [根 README](../../README.md) —— 全栈功能总览（flow.json 数据模型 / 声明式动作 / Lua API / 分布式架构 / 监控）
- [`api/admin/openapi.yaml`](../../api/admin/openapi.yaml) —— 浏览器管理 API 唯一契约
- [`docs/visual-flow-editor.md`](../../docs/visual-flow-editor.md) —— 流程编辑器视觉设计（节点配色事实源）
- [AGENTS.md](../../AGENTS.md) —— 仓库级开发约定
