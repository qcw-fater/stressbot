# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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
- **`admin/` — Admin 服务器（16 文件）。任务调度（TaskStore 状态机 + 单例约束 + 持久化）、Agent 管理（注册/心跳/健康检查/unhealthy→offline/离线清理）、指标聚合（MergeSnapshots）、时序采样（Sampler）、历史归档（MySQL 6 表）、任务分配（proportional/debug-single）、Agent RPC 调度、前端静态托管。60 个 HTTP API 端点（前缀 `/sbot/`）。
- **`agent/` — Agent 节点（8 文件）。注册到 Admin（指数退避）→ 心跳循环 → 任务轮询 → TaskRunner 执行（下载配置 → 加载适配器 → 编译 proto → 构建流程 → Manager → 启动机器人）→ 指标上报 + 系统资源上报。本地 HTTP API（前缀 `/agent/v1/`：task/stop/shutdown/version/status/logs + `/healthz`）。
- **`monitor/` — 指标采集。原子计数器（热路径零锁：成功/失败/超时/取消/执行中/字节数）、延迟直方图（16 桶 1ms~60s+，P50/P90/P95/P99，**仅纯网络往返**，不含客户端构建/解析）、Apdex 评分（阈值 T 可配，分母为 netSampleCount）、客户端开销独立列（`ClientAvgMs`）、分布式聚合。`RecordAction(name, result, timing, wallClock, sendBytes, recvBytes, err)`：`result` ∈ Success/Failure/Timeout/Canceled，`timing` 携带 RTT 与各编解码阶段耗时；纯客户端动作（无 RTT 样本）不进直方图但 successCount 仍计数。错误按 `code` 单维聚合（展示按 `code < 100` 推导框架/业务标签），保留最近 3 条详情。导出：Console / HTTP JSON / CSV / pprof。
- **`logview/` — 日志环形缓冲区。O(1) 写入 + cursor 分页查询，供前端实时日志面板使用。
- **`errcode/` — 统一错误码。`ErrorCode`（uint64）单一维度 + 码段契约（< 100 框架保留段，工具自产、由 `codeRegistry` 分配 / ≥ 100 业务段，服务器返回）+ 29 个框架错误码常量（Network 6 / Protocol 2 / Build 4 / Listen 2 / Config 9 / Lua 4 / Callback 2）。`ActionError` 携带 `{Code, Detail}`（无 Kind）；monitor 按 code 单维聚合，展示按 `code < 100` 推导框架/业务标签。`errors.json` 加载期对 < 100 撞码硬报错。
- **`utils/` — `work_pool.go`（协程池 + recover 防止 panic 扩散）、`duration.go`、`utils/log/`（结构化日志 zap + lumberjack 轮转 + 企业微信 webhook 告警）。

### 单次动作数据流

1. `Executor` 遍历流程图 → 命中 `action` 节点 → 调用 `ActionHandler.ExecuteAction(actionDef)`
2. 声明式动作：`ActionExecutor` 构建 protobuf 消息（从 state/随机源解析字段绑定）→ 序列化 → adapter 编码消息头 → gnet 发送 → 接收响应 → adapter 解码 → 解析 S2C proto → 存储字段到 state
3. Lua 动作（`pattern: "lua"`）：当前 Robot 主流程通过 `RuntimePool` 同步执行脚本 → `execute(r)` 返回 nil 表示成功，err table `{code, detail}` 表示失败（`robot.error(code, detail)` 构造）；等待网络/休眠时只阻塞该主流程，连接收包与连接级心跳由 connectionPump 独立推进

### 前端技术栈

React 18 / Vite 8 / TypeScript 5.6 / Ant Design 5 / React Flow 12 / Monaco Editor / Zustand + Zundo（撤销重做）/ ECharts 6 / idb-keyval（IndexedDB）/ protobufjs / Vitest。

主要功能模块：可视化流程编辑器（FlowEditor）、资源管理（Proto/Lua/Adapter 的上传/编辑/同步/下发）、任务管理（创建/启动/停止）、实时监控面板（MonitorDock：动作表/趋势图/系统资源）、日志查看（Monaco）、历史归档面板、Agent 管理面板。

## 配置文件

- `conf/config.json` — 运行配置：`log`/`monitor`/`pprof`（共享）+ `standalone`（单机模式：`bot`/`stateExtra`）+ `agent`（Agent 模式）+ 可选 `redis`/`daemon`；资源路径（`flow`/`proto`/`scripts`/`adapter`）为 CLI flag（默认回退 `<conf>` 下子目录）
- `conf/agent-config.json` — Agent 模式精简配置：仅 `log`/`monitor`/`agent`（无 standalone 段，运行时由 Admin 下发）
- `conf/admin-config.json` — Admin 服务器配置：`port`、`publicUrl`、`staticDir`、`agentRegistry`、`mysql`（顶层，全局共享 `*sql.DB`）、`redis`、`history`（`retentionDays`）、`log`、`daemon`
- `conf/flow/flow.json` — 流程图（`defaultDelayMs` + `nodes` + `actions` + `listens`）— 主要配置产物
- `conf/adapter/<proto>_<service>_codec.json` — 每连接一份的声明式 codec 配置；共享 `errors.json` 提供错误码描述。`codec.lua`/`error.lua` 仅保留为 T1 一致性测试的 oracle，非生产路径。
- `conf/proto/` — 启动时动态加载的 `.proto` 文件
- `conf/scripts/` — 复杂行为的 Lua 脚本

## flow.json 数据模型

### 节点类型（9 种）

- **控制流**：`sequence`（顺序执行子节点）、`loop`（循环，支持无限/前置条件/后置条件/break/continue）、`boolean`（条件分支 trueNext/falseNext）、`switch`（按 cases 顺序首匹配、单跳无 fall-through，defaultNext 兜底）、`weighted`（加权随机 options:[{node,weight}]）、`wait`（显式等待）
- **执行**：`action`（引用 actions 表执行动作，或通过 listenRefs 注册持久化推送监听）
- **循环控制**：`break`（跳出循环）、`continue`（跳过本次）

### 动作 pattern（14 种）

- **请求-响应**：`tcpRequest` / `udpRequest` — channel 一发一收 + 超时
- **监听**：`tcpListen` / `udpListen` — 消费 ListenRefs 预缓存的推送消息（轮询 + 超时 + pollMs）
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

`state:` 前缀为声明式表达式（`cond_parser.go` 词法+递归下降解析）：字面量只有数字与**带引号字符串**，裸标识符恒为 state 路径；支持比较 `== != > >= < <=`、算术 `+ - * / %`（`%` 仅整数）、逻辑 `&& || !` 与括号。**严格类型、零关键字（无 `true`/`false`/`nil`）、无隐式转换**——裸操作数须 bool、`>`等须数值、`==`/`!=` 须同类型；类型不匹配或 key 缺失 → warn + 条件 false。`lua:script.lua` 为脚本求值（须 `return true/false`）。

## 关键约定

- `flow.json` 的 nodes 按 ID 反序列化为 `map[string]*Node`。
- Adapter 接口 9 方法（含 `DescribeError`）。`DecodeTCP` 和 `DecodeUDP` 独立方法。
- UDP 加密使用偏移量部分加密：前 N 字节保持明文供服务端查密钥表，剩余部分加密。偏移量由 `<proto>_<service>_codec.json` 的 `encrypt.offset.{encode,decode}` 单向配置（如 `udp:battle` 发送偏移 11、接收偏移 0）。
- 默认节点延迟由 `TaskFlow.DefaultDelayMs` 控制。`delayMs: -1` 禁用，`delayMs: 0` 使用 defaultDelayMs。
- `onError` 控制 action 失败后的错误链路：`ignoreCodes` 命中后 warn 并继续流程但 monitor 保留失败样本；`handler` 是普通节点调用边（不写入 next）；`retry.maxRetries` 是当前 action 的额外重试次数；`strategy` 支持空/`resume` 继续、`skip` 结束当前分支/层级（由 sequence/loop/boolean/weighted 捕获）、`abort` 中断流程。
- 任务状态机：`pending → starting → running → stopping → stopped / failed`。单例约束：同一时刻只能有一个活跃任务。
- Agent 心跳连续失败 `heartbeatFailThreshold` 次（默认 3）后放弃当前任务。Admin 重启后活跃任务自动重置为 `failed`。
- 日志和错误信息使用中文。
- Go 字段名与 JSON tag 一致：`Listens`/`listens`、`ListenRefs`/`listenRefs`、`Listen`/`listen`。`ListenDef` 是监听定义类型，`ListenCallBack`（network 包）是回调函数类型。`listen` 是外层概念，`callback` 是 listen 内部的处理机制。
- 后端 goroutine 统一走 `utils/work_pool.go` 协程池（自带 recover）。
- 前端请求收拢到 `services/api.ts` + `services/baselineApi.ts`，组件禁止直接 fetch。
- 前端 UI 文本禁止暴露技术术语（Agent→节点、Admin→服务器、IDB→本地存储）。
- 数据库只用逻辑外键，不用 FOREIGN KEY，级联删除由应用层处理。

## 验证流程

每次对代码进行修改后，按以下步骤验证：

1. **编译检查**：`go build ./...` 确保无编译错误
2. **前端编译**：`cd cmd/web && npx tsc -b` 确保无类型错误
3. **单元测试**：`cd cmd/web && npm run test`（Vitest）
4. **配置校验**：在前端编辑器中打开 flow.json，查看校验报告，确保无错误
5. **运行验证**（涉及后端改动时）：`rm -f log/stressbot.log`，启动 `go run ./cmd/agent -config conf/config.json`，运行 2~5 分钟
6. **日志审查**：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无异常输出
