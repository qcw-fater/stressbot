# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## 项目概述

stressbot 是一个可配置化通用游戏服务器压测工具，用 Go 编写后端 + React 前端。核心设计：所有消息收发、字段填充、随机化、心跳、监听、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，少量难以通用的行为通过 **Lua 脚本** 实现。

一套 `conf/flow/flow.json + conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测。

## 构建与运行命令

```bash
# 编译（单机 + Agent 共用一个二进制）
go build -o stressbot.exe ./cmd/agent

# 单机压测（agent.enabled 默认 false）
go run ./cmd/agent -config conf/config.json
# 单机模式可选 flag：覆盖资源路径（空值回退到 <config 所在目录> 下默认）
#   -flow <file>     流程配置（默认 <conf>/flow/flow.json）
#   -proto <dir>     proto 目录（默认 <conf>/proto）
#   -scripts <dir>   Lua 脚本目录（默认 <conf>/scripts）
#   -adapter <dir>   适配器目录，含各 *_codec.json 与可选 errors.json（默认 <conf>/adapter）
# 示例：切换压测场景无需挪文件
go run ./cmd/agent -config conf/config.json -flow conf/flow/rank.json

# Admin 服务器
go run ./cmd/admin -config conf/admin-config.json

# 前端开发
cd cmd/web && npm install && npm run dev   # http://localhost:5173
cd cmd/web && npm run build                # → dist/，Admin 静态托管
cd cmd/web && npm run test                 # Vitest
```

## 运行模式

### 单机模式（standalone）
`agent.enabled=false`，单个进程完成全部工作：加载配置 → 声明式 codec 配置（`*_codec.json` + `errors.json`）→ .proto 文件 → 流程配置 → 启动 gnet 网络引擎 → 创建 Lua 运行时池 → 创建 Robot Manager → 批量启动机器人。

### Agent 模式（distributed）
`agent.enabled=true`，Agent 注册到 Admin → 接收任务 → 下载配置 → 执行。Admin 负责任务调度、Agent 管理、指标聚合、历史归档。

### Admin 模式
独立服务器进程，提供 Web UI + 60 个 HTTP API 端点（前缀 `/sbot/`），管理多个 Agent 节点的任务下发和指标收集。

## 架构

### 核心分层（依赖顺序）

- **`engine/`** — 流程执行引擎。`TaskFlow`（节点 DAG）、`ActionDef`（14 种 pattern 的声明式动作）、`Executor`（节点图遍历）、`ActionExecutor`（消息构建/发送/接收/存储）、条件解析器（`cond_eval.go` + `cond_parser.go`，支持 `&&`/`||`/`!`/括号嵌套）。`Executor` 通过 `ActionHandler` 接口委托实际工作，与 network/robot 层解耦。
- **`robot/`** — `Robot` 是单个压测客户端实例，持有独立的 state、网络连接、Lua 运行时和执行器。`Manager` 负责批量创建（`StartAll`）和渐进加压（`StartWithRampUp`，分阶段增加机器人数量和并发度）。`robotActionHandler` 实现 `engine.ActionHandler`，桥接 engine 层与 network 层。
- **`network/`** — 基于 gnet 的 TCP/UDP 连接层。`Client` 管理多服务命名连接池。`Connection` 处理收发、请求-响应匹配（responseMap + buffered channel + 超时 select）、持久化监听队列/Go 回调分发、per-connection 声明式心跳。`Dialer` 封装 gnet 事件循环。
- **`protox/`** — 动态 protobuf 加载与反射。`Loader` 发现 .proto 文件，`Registry` 编译，`Factory` 按全名在运行时创建/序列化/解析消息。
- **`script/`** — Lua 运行时池（`gopher-lua`）。每个 Robot 获取独占 `LState`，由主流程同步执行业务 Lua；阻塞型 Lua API 只暂停当前 Robot 主流程，connectionPump 与连接级心跳继续独立运行。7 个模块共 86 个函数：`network`（21）、`robot`（14）、`utils`（15）、`proto`（10）、`json`（2）、`log`（4）、`share`（20）。
- **`state/` — 线程安全的键值状态存储（RWMutex）。保存服务器响应字段（通过 `store` 映射），支持 list/map 操作用于随机选取。`CompareValues` 支持 10 种过滤运算符。
- **`adapter/` — 协议适配器接口（9 方法）。热路径帧解析（`HeaderSize`/`BodyLength`）纯 Go 缓存，编解码由 `CodecResolver` 按 `"<proto>:<service>"` 解析、`SchemaAdapter` 包装 `codec/` Go 引擎驱动，配置来自 `conf/adapter/<proto>_<service>_codec.json`（每连接一份）。
- **`admin/` — Admin 服务器。浏览器管理面继续使用 HTTP（前缀 `/sbot/`）；Admin-Agent 控制面使用 `grpc-go`，包含双向会话/命令、资源包流式下载和指标客户端流。内部负责 TaskStore 状态机、AgentRegistry、SessionRegistry/CommandStore/CommandBus、BundleStore、TelemetryIngestor、任务分配、指标聚合、MySQL 历史归档和前端静态托管。
- **`agent/` — Agent 节点。主动建立到 Admin 的 gRPC 长连接（指数退避）→ Hello/心跳与租约 → 接收可靠命令 → 下载内容寻址资源包 → TaskRunner 执行 → 流式上报压力/系统指标 → 最终报告确认。Agent 不开放本地 HTTP 控制面。
- **`monitor/` — 指标采集。原子计数器 + per-action 短临界区、DDSketch 延迟分布（1% 相对精度、最多 2048 bins，P50/P90/P95/P99 可严格合并）、Apdex 评分、非 RTT 客户端开销独立列、累计面 + 顺序区间窗口。`RecordAction(name, result, timing, wallClock, sendBytes, recvBytes, err)`：`result` ∈ Success/Failure/Timeout/Canceled，`timing` 携带 RTT、监听等待与各编解码阶段耗时。错误按 `code` 单维聚合（展示按 `code < 100` 推导框架/业务标签），保留最近 3 条详情。内部 Agent/Admin 报告携带 sketch，公共 API 必须剥离；空延迟分布展示值为 null。导出：Console / HTTP JSON / CSV / pprof。
  - **动作分类（`ActionSnapshot.Kind`）** 按运行时实际发生的网络行为定型，取最强语义：有 RTT 样本 → `networked`（往返）；否则有监听命中 → `listen`（监听）；否则有发送字节 → `send`（发送）；都没有 → `local`（本地）。前端按 kind 选主指标列。
  - **Apdex 只对 `networked` 打分**，样本是 RTT。监听/发送/本地类在快照里带 kind，UI 显示「不适用」——这三类的耗时主体是服务端业务时长或客户端执行时长，没有可比的统一阈值，掺进总分会让分数随动作构成漂移。分母是「发起过请求的样本数」：超时与连接中断记 frustrated（拿不到 RTT 但确实是坏体验），业务错误按真实 RTT 正常打分（服务端正确处理了请求）。
  - **监听等待**（`ListenWait` 直方图）单列，从 `NetExchange.RecvFrameAt`（帧在内核可读的时刻）算起而非轮询唤醒时刻，避开协作式调度的量化误差。已在队列里的消息记 `ListenReady` 计数而不产生 0ms 样本，超时记 `ListenTimeoutCount` 并单独出成率。
  - **总耗时（wallClock）只留直方图作诊断，不再打 Apdex**：它含 Lua 里的 sleep 和客户端调度延迟，高 CPU 下会把施压机自身的拥塞读成服务端劣化。
- **`errcode/` — 统一错误码。`ErrorCode`（uint64）单一维度 + 码段契约（< 100 框架保留段，工具自产、由 `codeRegistry` 分配 / ≥ 100 业务段，服务器返回）+ 29 个框架错误码常量（Network 6 / Protocol 2 / Build 4 / Listen 2 / Config 9 / Lua 4 / Callback 2）。`ActionError` 携带 `{Code, Detail}`（无 Kind）；monitor 按 code 单维聚合，展示按 `code < 100` 推导框架/业务标签。`errors.json` 加载期对 < 100 撞码硬报错。
- **`utils/` — `work_pool.go`（协程池 + recover 防止 panic 扩散）、`duration.go`、`utils/log/`（稳定 JSON Lines + zap + 256 KiB/1s 有界缓冲 + lumberjack 轮转 + 企业微信 webhook 告警）。Admin/Agent 只写本地文件，不提供日志查询/代理 API；生产采集与查询交给外部日志栈。

### 单次动作数据流

1. `Executor` 遍历流程图 → 命中 `action` 节点 → 调用 `ActionHandler.ExecuteAction(actionDef)`
2. 声明式动作：`ActionExecutor` 构建 protobuf 消息（从 state/随机源解析字段绑定）→ 序列化 → adapter 编码消息头 → gnet 发送 → 接收响应 → adapter 解码 → 解析 S2C proto → 存储字段到 state
3. Lua 动作（`pattern: "lua"`）：当前 Robot 主流程通过 `RuntimePool` 同步执行脚本 → `execute(r)` 返回 nil 表示成功，err table `{code, detail}` 表示失败（`robot.error(code, detail)` 构造）；等待网络/休眠时只阻塞该主流程，连接收包与连接级心跳由 connectionPump 独立推进

RTT 的两个测点都取在贴近内核的位置，避免协作式调度把施压机自身的排队算进服务端延迟：起点是 `AsyncWrite` 的写完成回调（数据真正交给内核，而非入队时刻），终点是 `RecvFrameAt`（帧在 gnet 入站缓冲里可读的时刻，而非 Robot 被唤醒读到它的时刻）。

### 前端技术栈

React 18 / Vite 8 / TypeScript 5.6 / Ant Design 5 / React Flow 12 / Monaco Editor / Zustand + Zundo（撤销重做）/ ECharts 6 / idb-keyval（IndexedDB）/ protobufjs / Vitest。

主要功能模块：可视化流程编辑器（FlowEditor）、资源管理（Proto/Lua/Adapter 的上传/编辑/同步/下发）、任务管理（创建/启动/停止）、实时监控面板（MonitorDock：动作表/趋势图/系统资源）、历史归档面板、Agent 管理面板。

## 配置文件

- `conf/config.json` — 运行配置：`log`/`monitor`/`pprof`（共享）+ `standalone`（单机模式：`bot`/`stateExtra`）+ `agent`（Agent 模式）+ 可选 `redis`/`daemon`；资源路径（`flow`/`proto`/`scripts`/`adapter`）为 CLI flag（默认回退 `<conf>` 下子目录）
- `conf/agent-config.json` — Agent 模式精简配置：仅 `log`/`monitor`/`agent`（无 standalone 段，运行时由 Admin 下发）
- `conf/admin-config.json` — Admin 服务器配置：`port`、`publicUrl`、`staticDir`、`agentRegistry`、`mysql`（顶层，全局共享 `*sql.DB`）、`redis`、`history`（`retentionDays`）、`log`、`daemon`
- `conf/flow/flow.json` — 流程图（`defaultDelayMs` + `nodes` + `actions` + `listens`）— 主要配置产物
- `conf/adapter/<proto>_<service>_codec.json` — 每连接一份的声明式 codec 配置；共享 `errors.json` 提供错误码描述。编解码统一由纯 Go `codec/` 引擎驱动，无 Lua codec 路径。
- `conf/proto/` — 启动时动态加载的 `.proto` 文件
- `conf/scripts/` — 复杂行为的 Lua 脚本

## flow.json 数据模型

### 节点类型（9 种）

- **控制流**：`sequence`（顺序执行子节点）、`loop`（循环，支持无限/前置条件/后置条件/break/continue）、`boolean`（条件分支 trueNext/falseNext）、`switch`（按 cases 顺序首匹配、单跳无 fall-through，defaultNext 兜底）、`weighted`（加权随机 options:[{node,weight}]）、`wait`（显式等待）
- **执行**：`action`（引用 actions 表执行动作，或通过 listenRefs 注册持久化推送监听）
- **循环控制**：`break`（跳出循环）、`continue`（跳过本次）

### 动作 pattern（14 种）

- **请求-响应**：`tcpRequest` / `udpRequest` — channel 一发一收 + 超时
- **监听**：`tcpListen` / `udpListen` — 事件等待 ListenRefs 预缓存的推送消息（队列边沿通知 + 超时）
- **连接管理**：`tcpConnect` / `udpConnect` / `tcpClose` / `udpClose`
- **发送**：`tcpSend` / `udpSend` / `httpRequest`（支持 JSON/form body）
- **状态与辅助**：`setState` / `clearState` / `lua`

### binding type（17 种）

- **取值**：`fixed`（固定值）/ `state` / `stateFirst`
- **随机选取**：`stateRandom` / `stateRandomN` / `stateMapKey` / `stateMapValue` / `randomPick` / `randomPickN` / `randomPickMap` / `randomExclude`
- **随机生成**：`randomInt` / `randomFloat` / `randomBool` / `randomString`（charset 支持 `lower`/`upper`/`alpha`/`numeric`/`alphanum` 与自定义字符集）
- **辅助**：`listSize` / `map`（用 entries:[{key,value}] 构造 proto map 字段）

### 通用属性

- binding 可选属性：`optional` / `required` / `wrap` / `storeAs` / `path` / `filters`
- 条件绑定：FieldBind 上的 `condition` 字符串（`state:`/`lua:` 前缀），非结构体；过滤器比较用 FilterDef（`path` / `op` / `value` / `source` / `mode`）
- store 映射（StoreMapping）：`field`（含嵌套路径）+ `setter`

### 过滤器运算符（12 种）

`eq` / `neq` / `gt` / `gte` / `lt` / `lte` / `contains` / `notContains` / `in` / `notIn` / `notNil` / `isNil`；FilterDef 含 `mode`（any/all/none）多值聚合

### 条件表达式

支持复合表达式：`state:key op value`（内置比较）+ `lua:script.lua`（脚本求值），支持 `&&` / `||` / `!` / 括号嵌套。

## 关键约定

- `flow.json` 的 nodes 按 ID 反序列化为 `map[string]*Node`。
- Adapter 接口 9 方法（含 `DescribeError`）。`DecodeTCP` 和 `DecodeUDP` 独立方法。
- UDP 加密使用偏移量部分加密：前 N 字节保持明文供服务端查密钥表，剩余部分加密。偏移量由 `<proto>_<service>_codec.json` 的 `encrypt.offset.{encode,decode}` 单向配置（如 `udp:battle` 发送偏移 11、接收偏移 0）。
- 默认节点延迟由 `TaskFlow.DefaultDelayMs` 控制。`delayMs: -1` 禁用，`delayMs: 0` 使用 defaultDelayMs。
- `onError` 控制 action 失败后的错误链路：`ignoreCodes` 命中后 warn 并继续流程但 monitor 保留失败样本；`handler` 是普通节点调用边（不写入 next）；`retry.maxRetries` 是当前 action 的额外重试次数；`strategy` 支持空/`resume` 继续、`skip` 结束当前分支/层级（由 sequence/loop/boolean/weighted 捕获）、`abort` 中断流程。
- 任务状态机：`pending → starting → running → stopping → stopped / failed`。单例约束：同一时刻只能有一个活跃任务。
- Agent 心跳连续失败 `heartbeatFailThreshold` 次（默认 3）后放弃当前任务。Admin 重启后活跃任务自动重置为 `failed`。
- 日志和错误信息使用中文。
- 废弃字段或能力必须从前后端类型、表单、序列化、Schema、配置样例和当前源码注释中物理删除；不得保留 disabled 控件、兼容解析、注释掉的旧代码或过时说明。历史设计文档可保留当时状态。
- Go 字段名与 JSON tag 一致：`Listens`/`listens`、`ListenRefs`/`listenRefs`、`Listen`/`listen`。`ListenDef` 是监听定义类型，`ListenCallBack`（network 包）是回调函数类型。`listen` 是外层概念，`callback` 是 listen 内部的处理机制。
- 后端 goroutine 统一走 `utils/work_pool.go` 协程池（自带 recover）。
- 前端请求收拢到 `services/api.ts` + `services/baselineApi.ts`，组件禁止直接 fetch。
- 前端 UI 文本禁止暴露技术术语（Agent→节点、Admin→服务器、IDB→本地存储）。
- 数据库只用逻辑外键，不用 FOREIGN KEY，级联删除由应用层处理。

## 内存优化约束与已证实结论（2026-07 压测调优）

**设计原则（用户明确要求，长期有效）**：
- **数据面改造必须"留存侧 + 消费侧"同步落地**。除非不对称本身是明确设计并写下理由，否则不允许只改一侧：wire-first 首版只做了留存侧（state 存 `WireValue`），消费侧（robot.get 转 Lua 表、listen 解码）仍走 dynamicpb 整树，结果 8000 人剖面上消费侧成为 CPU/分配主源（整读转换 14.8% + 帧解码 6.9% + 由此拉动的 GC 26%），又追加一轮改造才补齐。同类改造（缓存策略、编码形态、生命周期）一律先画出该数据的**全部读写路径**，逐一确认新形态覆盖或显式豁免。
- 优先设计层修复，而非业务脚本改写或运维参数（GOGC 等）兜底。

**硬约束**：
- 业务脚本（`conf/scripts/*.lua`）的**逻辑不可改**——包括"存哪些数据"（不允许按访问面裁剪留存字段）；**用法可以改**（等价 API 替换），但必须保持语义与留存数据集合不变。
- 不设置 `GOMEMLIMIT` 等运行时环境变量作为"架构方案"（可作为运维手段单独讨论）。

**已被线上剖面证实的结论**（勿重复尝试）：
- 把 `playerData`（`LoginPlayerDataS2C`，map/repeated 重度、每机器人独有）从"get_field_map 展开 map 存储"改为"存 `protox.Frozen` 解码消息引用"是**净退化**：同相位对比 live +217MB @5000 人（dynamicpb 每条目固定开销 ≥ `map[string]any` 装箱）。已还原。
- 每机器人独有的大消息，任何**解码态**表示（dynamicpb / Go map / Lua table）都 ~600KB/机器人；唯一小一个数量级的形态是 **wire 字节本身**（约 1/5~1/10）。要压这块只能走"持字节 + 按需 wire 扫描解码"的惰性视图。
- 广播类消息（同内容多接收方）用内容寻址去重有效：留存与消费（2026-07-29 D2 起）统一走 `protox.WireCache` 共享 wire 字节（容量按原始字节硬上界即真实钉住量）。**"脚本消费改独占瞬态解码"已被证伪**（wire-first 首版，029→031 剖面）：同场 60 人相同帧数据逐机器人解码使 churn 放大 60 倍（区间 ~1.3TB dynamicpb 分配），且帧循环脚本挂起在 await 时协程局部变量钉着自己那份解码树，5000 人陆续进战斗 → live 单调 +1.15GB。注意 wire 惰性视图**不是**独占解码——60 接收方共享同一 `WireShared` 字节、无解码树可钉，不触犯该结论。独占推送（动作响应）不得进任何去重缓存（污染+换血）。
- 消费侧 wire-first（2026-07-29 D1/D2）：`robot.get` 整读、listen 脚本 `on_message` 表、`get_field_map` 走 wire→Lua 单遍直转（`protox.WalkWire`，零 dynamicpb 中间树）；`await_listen` 大消息给脚本 wire 惰性视图 userdata（`proto.get_field/get_path/list_*` 按需 wire 扫描，语义逐字对齐 `Factory.GetField`——含"缺席 message 按默认值实例下钻"这一与 `Navigate` 不同的历史语义）。`protox.FrozenCache` 共享解码仅作 schema 降级回落路径。正确性三层防线：离线差分 fuzz（`wirediff/wirewalk/wireview_test.go`）+ 线上影子采样（`/debug/wire`，失配自动降级该 schema 回解码路径）。
- 调优观测端点（挂 pprof 端口）：`/debug/sched`（Go 调度延迟分位数，量化施压机负载对 Apdex/P99 的污染，压测中看 `sinceLast` 组）、`/debug/statekeys`（`robot.get/get_path` 按 key 计数，`tables`/`wireDecodes` 列定位整读热点，`?reset=1` 分窗口）、`/debug/dedup`、`/debug/wire`。
- **降级回落机制的退役计划**（2026-07-29 记，用户已确认方向）：wire 消费路径（直转器+惰性视图）经**数轮生产规模压测**且 `/debug/wire` 持续零失配、影子验证按计划稳态关闭后，可整体退役「schema 降级回落」——删除 `FrozenCache`、`Frozen` 消费路径与各访问器的解码分支，失配语义从 fail-safe 改为 fail-stop（直接报错停测）。这是信心问题而非技术问题；退役前 FrozenCache 常态为空、成本近零，**不要提前删**（回落必须落在共享解码上，独占解码回落已被 029→031 剖面证伪）。
- 影子验证失配日志（`[WIRE] 影子验证失配`）携带离线复现全要素：schema 全名、访问路径、两侧产物摘要、wire 字节 hex 转储（截断 4KB，`rawLen` 给全长）。直转 Walk 在已过校验字节上的意外失败同样走失配上报（`ReportWireFailure`），**任何 wire 路径的回退都必须留日志，禁止静默回退**。

**`robot.get` 与 `robot.get_view` 的使用边界（2026-07-30 P1，脚本作者契约）**：
- 一句话决策：**整份数据要拿来自由加工/修改 → `get`；大消息只读挑着看 → `get_view`**。`get` 返回独立 Lua 表（整树物化，成本 ∝ 树大小，改它不影响 state）；`get_view` 返回 wire 惰性视图 userdata（与 `await_listen` 给脚本的是同一种东西），零物化，只能经 `proto.get_field/get_path/list_size/list_get/iter_list/serialize` 窄读。范例脚本：`conf/scripts/system_shop_buy.lua`。
- 两者**不可能混用出错值**：表与 userdata 语法互斥，误用即刻报错且报错文案指路正确 API（视图上 `view.foo`/`view.foo = v`/表原语 → 教学报错；`get_view` 用在标量、脚本存的 Lua 表、被 `set_path` 改写出 `Overlay` 的 key → 报错指路 `robot.get`）。key 不存在时二者都返回 nil。
- 视图是借出时那份不可变字节的快照引用：key 被覆盖不影响已借出视图，无失效协议；跨 await 挂起持有安全（共享 `WireShared` 字节，不钉解码树，不触犯 029→031 结论）。
- 写侧不受影响、无需新 API：`robot.set(key, 视图/响应)` 原样存 wire 引用（零转换）；`robot.set_path` 走 `Overlay` 写覆盖层（不解码不重编码）；视图永远只读。
- `proto.iter_list` 语义（2026-07-30 起，改版前全仓零使用方）：两侧分支统一产出——message 元素为 userdata（wire 侧子视图 / 解码侧子消息包装，≡ `list_get`），标量元素装箱值；未知字段/非法路径 → nil，非 repeated 字段 → 空迭代。wire 侧为游标实现（`protox.WireListCursor`，一遍收集元素跨度、逐元素惰性产出子视图），顺序遍历整链 O(n)，替代逐下标 `list_get` 的 O(n²)。**降级回落时脚本可见形态不得漂移**是此处（也是所有消费 API）的硬约束。
- 观测：`/debug/statekeys` 中 `view:` 前缀计数 `get_view` 调用；迁移某热点 key 后应看到其 `get:` 行的 `tables/wireDecodes` 掉零、`view:` 行上量。
- 直转器 scratch 池化（2026-07-30 P2）：`walkWireLevel` 的字段累计切片走 `sync.Pool`（瞬态 scratch，GC 周期自动清空、常驻 ∝ 并发转换数，不属于"内存换 CPU"的缓存交易，不触内存红线）。**归还前必须逐元素 clear**——scratch 引用共享 wire 快照字节，只截断 len 会经池钉住大缓冲（029→031 钉扎形态）；隔离契约由 `TestWalkWireAccsPoolNoContamination` + 差分 fuzz 守护。产物形态（Lua 表/Go map，归调用方所有）**不可池化**，这块的解法是视图（P1）而非池。
- Lua 线程用 trampoline 长驻复用（`script/trampoline.go`）后，`newLState/newRegistry` churn 已消除；`RSS ≈ 2× live` 是 GOGC=100 的正常余量，压 RSS 先压 live。
- 全局 timer 池（2026-07-30 P3，`utils/timerpool.go`）：高频等待点（`robotScheduler.wait` 帧循环 poll、`awaitResponse`/`RequestResponse` 每请求超时窗）统一 `utils.GetTimer/PutTimer`，消除每次 `time.NewTimer` 分配。正确性依赖 Go 1.23+ timer 语义（无缓冲通道、Stop/Reset 保证无旧触发），**归还后不得再引用**；嵌套等待（listen 回调里再 wait）各取各的，天然安全。低频点（连接关闭、心跳注册）不必接。
- 收包解码路径零分配化（2026-07-30 P4，`codec/`）：① gzip 解压 `readAllSized` 按 trailer ISIZE 定长一次分配（替代 `io.ReadAll` 512B 起步倍增；提示短则截断、长则追加兜底，多成员流有回归测试）；② 流密码实现 `CipherInPlace` 原地解密（decode 的 work 是私有副本；**实现约束：报错前不得改写 data**，块密码不实现）；③ decode 头字段暂存用栈上定长数组替代每帧 routeMap/checksumOut 两个临时 map，routeKey 用 `strconv.AppendInt` 栈上拼接。全部有与复制版/旧行为的对拍测试。encode 侧 `stash` 嵌套 map 未动（摊到进程生命周期分配占比低，对拍风险不值）。
- 解压去重缓存（2026-07-30 P5，`codec/inflate_cache.go`，显式内存换 CPU）：大广播帧推给全部机器人时逐连接重复 gunzip，按压缩字节内容寻址共享解压产物。**二见登记**防污染（018→019 教训机制化）：首见只记 8 字节哈希标记，第二次见到才存条目——逐机器人唯一的响应永远停在标记层，无需知道路由类型。共享安全前提：产物只读流转（已审计）+ `inflateShareSafe` 保证 compress 是 decode 最后执行步（其后再有原地改写步则禁用共享）。双上界 LRU（1024 条 / 48MB），观测 `/debug/inflate`（hits≈0 且 misses 高涨 = 负载无重复大帧，缓存空转）。
- routeKey 驻留（2026-07-30 P5，`codec/intern.go`）：解码/编码路由键改为 COW 表驻留（读侧原子 Load + 无分配 map 查找），消除每帧一个小字符串分配。表容量上限 4096 防损坏帧撑爆，超限退化为普通分配。
- 压缩帧 work 缓冲池化（2026-07-30 P5，`codec/engine.go`）：flags 判定要解压的帧，其 body 副本是纯瞬态 scratch → `sync.Pool` 租借。**归还纪律：只有 work 被解压/共享/复制解密产物顶替后才归还**；任何可能让缓冲作为 body 外泄的路径（解压失败 keep、提前 return）一律不归还、交给 GC——宁可放弃复用也不冒池污染风险。
- 导航路径驻留表（2026-07-30 P6，`protox/wirenav.go`）：wire 读路径（`NavigateSegs`/`GetFieldCompat`/`MaterializeAllowed`）的影子采样判定与 fd 解析合并为一次查表——`maphash(schema全名+路径段) → 条目`（COW 表，容量上限 8192），条目携带首 K 计数、per-schema 采样计数指针、按 `wireNavigate` 层级推进规则预解析的 fd 链。替代旧 `shadowShouldVerify` 每次导航拼 key + 双 `sync.Map` LoadOrStore（4 次堆分配）与逐层 `ByName`。条目按描述符**身份**（指针）校验：proto 重载后自动替换并重新首 K 全查（比旧的按名计数更严格）；`Factory.Close()` 清整表解除描述符钉扎。哈希碰撞/表满 → 不驻留，跳过首 K 仅参与 per-schema 稳态采样（旧表反而无界，动态 map-key 路径会撑爆它）。fd 链槽位为 nil（非字段段/编译终止）时运行时回退 `ByName`，行为不变；对拍测试 `wirenav_test.go` + 既有差分 fuzz。
- 心跳注册时编译布局（2026-07-30 P7，`engine/heartbeat_plan.go`）：raw-binary 心跳每 tick 打的是同一份定长小端流，宽度/偏移/哪些槽恒定在注册那刻已定。`CompileHeartbeatPlan` 把布局定型为「总长 + 动态槽位表」，fixed 槽编译期预填，每 tick 只覆写动态槽（零 `heartbeatTypeWidth` 查表、零 `append` 增长、body 缓冲跨 tick 复用）。同批前移的还有私有计数器推进表（`CompileHeartbeatCounters`，不再每 tick 全字段扫）与 resolver key 字符串 + 解析结果（不再每 tick 拼串查表）。**缓冲复用的安全前提**：`codec` encode 把 body `copy` 进新分配的整包、各 cipher `Encrypt` 一律 make+copy 不原地改写入参——这条一旦被破坏，心跳会发出上一 tick 的密文残留。**编译失败不静默吞**：回落 `BuildHeartbeatBody` 保持原「每 tick Warn」可见性（坏配置不能因为多一层编译就消失），故 `BuildHeartbeatBody` 作为 oracle 保留且不复用快路径代码，对拍见 `heartbeat_plan_test.go`（多 tick 逐字节 + 同 key 读/自增交错序 + skip/错误语义 + 零分配）。plan 持有复用缓冲**非并发安全**，每连接一份、仅由该连接 pump 串行调用（与 `privateCounters` 同一约定）。
- 收包路由分派合表（2026-07-30 P7，`network/connection.go`）：`listenResp`（回调）+ `listenQueues`（队列）合成 `listenRoutes map[string]*listenBinding`，`OnReceive` 一次持锁内查一次即取齐回调与队列传给 `dispatchListen`。旧路径每条推送要 3~4 次字符串 map 查找 + 2~3 次 `c.mu`（OnReceive 查到回调却丢掉、dispatchListen 加锁重查、缓存模式再加锁查队列）。并发约定：`binding.cb` 的读写都在 `c.mu` 下（幂等重注册会回写最新回调），`queue` 在绑定发布进 map 前建好、之后只读，Push/Pop 仍在 `c.mu` 之外由 per-queue mu 串行化。顺带把 `NewMessage` 挪到「确定有消费方之后」——无人认领的广播不再白白分配。合表后「有回调却没队列」这种旧双表可能的偏斜状态在结构上不再存在。
- 列表下标早退（2026-07-30 P6，`wireCollectList(b, fd, limit)`）：`xs[i]` 下标访问（`NavigateSegs`/`ListItemCompat`/`GetFieldCompat`）只扫到第 i+1 个元素即停，不再全量收集解码（repeated 元素只追加不覆盖，前缀即定值；packed 整块解出可能略多于 limit）。层级结构在 `WireValue` 构造点已全量校验，早退跳过尾部不损失防线。注意**单数字段/map 不可早退**——标量 last-wins、单数 message 多段拼接、map 重复 key 替换都要求扫完整层，这是 protobuf 合并语义的硬约束。终端整列表读（`robot.get_path("...heroList")`）与游标遍历天然需要全量，无早退空间。

## 验证流程

- Windows 下使用 `rg` 时禁止把含 `*`/`?` 的路径作为位置参数；应传目录字面路径并用 `-g '<pattern>'` 过滤，动态模块目录先枚举出实际路径。
- PowerShell 中检索包含 `|`、括号或引号的源码片段时优先拆成多个 `rg -F` 固定字符串查询；禁止在外层双引号命令中叠加复杂 `rg` 正则转义。
- 预期可能零匹配的 `rg` 必须单独执行，不得与文件读取或其他已有有效输出捆绑；`rg` 的零匹配返回码 1 不能被误判为前序操作失败。
- 在受限 Windows 工作区运行 Go 构建、测试或 pprof 命令时，将 `GOCACHE` 指向仓库内可写且已忽略的临时目录，避免默认用户缓存清理失败使成功结果返回非零。
- Windows PowerShell 运行前端命令时使用 `npm.cmd` / `npx.cmd`，避免执行策略拦截同名 `.ps1` shim。
- 异步业务动作的超时必须覆盖服务端最大业务窗口；上游请求失败后不得继续执行依赖请求制造级联超时，必须进入显式恢复与清理分支。

每次对代码进行修改后，按以下步骤验证：

1. **编译检查**：`go build ./...` 确保无编译错误
2. **前端编译**：`cd cmd/web && npx tsc -b` 确保无类型错误
3. **单元测试**：`cd cmd/web && npm run test`（Vitest）
4. **配置校验**：在前端编辑器中打开 flow.json，查看校验报告，确保无错误
5. **运行验证**（涉及后端改动时）：`rm -f log/stressbot.log`，启动 `go run ./cmd/agent -config conf/config.json`，运行 2~5 分钟
6. **日志审查**：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无异常输出
