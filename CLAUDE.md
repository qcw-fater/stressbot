# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

stressbot 是一个可配置化通用游戏服务器压测工具，用 Go 编写。它是旧版 "Robot" 压测工具的重构版本，核心思路是解耦业务逻辑与框架：所有消息收发、字段填充、随机化、心跳、回调、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，少量难以通用的行为通过 **Lua 脚本** 实现。

一套 `conf/flow/flow.json + conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测。

## 开发主旨

**目标：** 将旧压测工具（`E:\jump\Server-Jump\Robot`）从与游戏服务器强绑定的专用工具，重构为通用游戏服务器压测工具。

**三个相关项目：**
- 旧压测工具：`E:\jump\Server-Jump\Robot` — 与游戏服务器 `E:\jump\Server-Jump\Server` 强绑定
- 新压测工具：本目录 `stressbot` — 目标是通用化
- 游戏服务器：`E:\jump\Server-Jump\Server`

**核心设计原则：**

1. **声明式配置覆盖旧硬编码：** 旧工具 `Robot/game` 包下的所有 `OnHandleXXX` 方法的过程都要能通过配置实现。每个节点需可配置：发送/接收的消息 proto 类型、发送前填充 C2S 的哪些字段、接收 S2C 后存储哪些字段。
2. **Lua 脚本兜底复杂逻辑：** 复杂或不太通用的 `OnHandleXXX` 方法通过 Lua 脚本配置实现。
3. **声明式随机化模拟用户行为：** 压测需要模拟用户随机行为，旧工具中 `Robot/utils/tools.go` 的 `RandRangeNumber`、`RandSilenceOne`、`RandSilenceFilterOne` 等方法的随机赋值效果，在新工具中都要能通过配置（bindings 的 type 字段）实现。
4. **验证标准：** 完善新工具后，必须将旧工具的流程配置复刻为新工具可用的 `flow.json`，确保新工具 + 新流程配置与旧工具 + 旧流程配置产生相同的运行效果。

## 构建与运行命令

```bash
# 编译
go build -o stressbot.exe ./cmd/agent

# 启动压测（单机模式，agent.enabled 默认 false）
go run ./cmd/agent -config conf/config.json

# Admin 服务器
go run ./cmd/admin -config conf/admin-config.json

# 前端开发
cd cmd/web && npm install && npm run dev   # http://localhost:5173
cd cmd/web && npm run build                # → dist/，Admin 静态托管
```

**flow.json 校验**已集成到前端编辑器中（实时校验，画布节点上显示错误标记）。每次修改 flow.json 后在前端编辑器中打开查看校验报告即可。

## 架构

**启动流程：** `cmd/agent/main.go` 加载配置 → Lua 协议适配器 → .proto 文件 → 流程配置 → 启动 gnet 网络引擎 → 创建 Lua 运行时池 → 创建 Robot Manager → 批量启动机器人。Agent 模式下注册到 Admin → 接收任务 → 下载配置 → 执行。

**核心分层（依赖顺序）：**

- **`engine/`** — 流程执行引擎。定义 `TaskFlow`（节点 DAG）、`ActionDef`（声明式动作模式，14 种 pattern）、`Executor`（节点图遍历）。`Executor` 通过 `ActionHandler` 接口委托实际工作，与 network/robot 层解耦。
- **`robot/`** — `Robot` 是单个压测客户端实例，持有独立的 state、网络连接、Lua 运行时和执行器。`Manager` 负责批量创建（`StartAll`）和渐进加压（`StartWithRampUp`）。`robotActionHandler` 实现 `engine.ActionHandler`，桥接 engine 层与 network 层。`netSenderAdapter` 将 Robot 适配到 `NetSender` 接口。
- **`network/`** — 基于 gnet 的 TCP/UDP 连接层。`Client` 管理多服务命名连接池（`TCPConn` + `UDPConn`）。`Connection` 处理收发、请求-响应匹配、持久化监听、per-connection 心跳。`Dialer` 封装 gnet 事件循环。
- **`protox/`** — 动态 protobuf 加载与反射。`Loader` 发现 .proto 文件，`Registry` 编译，`Factory` 按全名在运行时创建/序列化/解析消息。
- **`script/`** — Lua 运行时池（`gopher-lua`）。每个 Robot 获取独占 `LState`。向 Lua 暴露 7 个模块（`network`、`robot`、`utils`、`proto`、`json`、`log`、`adapter`），共 65 个函数。Lua 访问通过 `luaMu` 互斥锁串行化（回调/心跳可能在其他 goroutine 触发）。
- **`state/`** — 线程安全的键值状态存储。保存服务器响应字段（通过 `store` 映射），支持 list/map 操作用于随机选取。`CompareValues` 支持 12 种过滤运算符。
- **`adapter/`** — 协议适配器接口（8 方法）。热路径帧解析纯 Go 实现，编解码通过 Lua 池调用 `codec.lua`。
- **`admin/`** — Admin 服务器。任务调度（TaskStore 状态机 + 单例约束）、Agent 管理（注册/心跳/健康检查）、指标聚合（MergeSnapshots）、历史归档（MySQL 6 表）、前端静态托管。40+ HTTP API 端点。
- **`agent/`** — Agent 节点。注册到 Admin（指数退避）→ 心跳循环 → 任务轮询 → TaskRunner 执行 → 指标上报。
- **`monitor/`** — 指标采集。原子计数器（热路径零锁）、延迟直方图（16 桶 P50~P99）、Apdex 评分、分布式聚合。导出：Console / HTTP JSON / CSV / pprof。
- **`logview/`** — 日志环形缓冲区。O(1) 写入 + cursor 分页查询，供实时日志面板使用。

**单次动作数据流：**
1. `Executor` 遍历流程图 → 命中 `action` 节点 → 调用 `ActionHandler.ExecuteAction(actionDef)`
2. 声明式动作：`ActionExecutor` 构建 protobuf 消息（从 state/随机源解析字段绑定）→ 序列化 → adapter 编码消息头 → gnet 发送 → 接收响应 → adapter 解码 → 解析 S2C proto → 存储字段到 state
3. Lua 动作（`pattern: "lua"`）：获取 `luaMu` → 通过 `RuntimePool` 执行脚本 → 返回 0 表示成功

**流程图节点类型：** `sequence`、`action`、`loop`、`boolean`、`weighted`、`wait`、`break`、`continue`。节点支持 `listenCallbacks`（持久化推送监听）、`delayMs`（节奏控制）、`errorStrategy`（`"abort"` 中断 / `"ignore"` 继续）。

## 配置文件

- `conf/config.json` — 运行配置：`log`/`monitor`（共享）+ `standalone`（单机模式：bot/adapter/network/proto/flow/script）+ `agent`（Agent 模式）
- `conf/agent-config.json` — Agent 模式精简配置：仅 `log`/`monitor`/`agent`（无 standalone 段，运行时由 Admin 下发）
- `conf/admin-config.json` — Admin 服务器配置：listenAddr、agentRegistry、task、history（MySQL）、log
- `conf/flow/flow.json` — 流程图（nodes + actions + callbacks）— 主要配置产物
- `conf/adapter/codec.lua` — 协议适配器脚本（7 个必需 Lua 函数）
- `conf/proto/` — 启动时动态加载的 `.proto` 文件
- `conf/scripts/` — 复杂行为的 Lua 脚本（战斗逻辑、条件判断、心跳 body 构建）

## 关键约定

- `flow.json` 的 nodes 按 ID 反序列化为 `map[string]*Node`。
- 15 种 binding type（`fixed`/`state`/`stateFirst`/`stateRandom`/`stateRandomN`/`stateMapKey`/`stateMapValue`/`randomPick`/`randomPickN`/`randomPickMap`/`randomExclude`/`randomInt`/`randomBool`/`randomString`/`listSize`）。`state` 扁平化为原始值。
- Adapter 接口 8 方法。帧解析（`HeaderSize`/`BodyLength`）纯 Go 缓存，编解码通过 Lua 池。`DecodeTCP` 和 `DecodeUDP` 独立方法。
- UDP 加密使用偏移量部分加密：前 N 字节（由 `codec.lua` 的 `encrypt.udpOffset` 配置，默认 11）保持明文供服务端查密钥表，剩余部分加密。
- 默认节点延迟由 `TaskFlow.DefaultDelayMs` 控制。`delayMs: -1` 禁用，`delayMs: 0` 使用 defaultDelayMs。
- `errorStrategy` 控制动作失败行为：`"abort"` 中断流程，空或 `"ignore"` 继续（替代旧 `breakOff` 字段）。
- 任务状态机：`pending → starting → running → stopping → stopped / failed`。单例约束：同一时刻只能有一个活跃任务。
- Agent 心跳连续失败 `maxHeartbeatFailures` 次后自动退出（0 = 不退出）。Admin 重启后活跃任务自动重置为 `failed`。
- 日志和错误信息使用中文（对齐旧工具命名）。

## 验证流程

每次对代码进行修改后，必须按以下步骤验证功能正确性，重复迭代直到通过：

1. **编译检查**：`go build ./...` 确保无编译错误
2. **配置校验**：在前端编辑器中打开 flow.json，查看校验报告（工具栏"校验"按钮），确保无错误
3. **运行测试**：删除旧日志 `rm -f log/stressbot.log`，然后启动 `go run ./cmd/agent -config conf/config.json`，运行 2~5 分钟
4. **日志审查**：
   - 检查错误：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无输出
   - 检查战斗循环：`grep -c "BattleEnd" log/stressbot.log` 应 ≥ 2（至少完成两轮完整战斗）
   - 检查帧同步：`grep "SyncFrame: frame=" log/stressbot.log` 应有持续递增的帧号
5. **清理**：确认无误后删除日志 `rm -f log/stressbot.log`，停止进程

**完整战斗循环预期路径：**
`businessWeight → normalModel → CreateNormalTeam → SelectHero → StartMatch → MatchSucceed → startBattle(ConnectBattleTCP + ConnectBattleUDP + RegisterBattle) → loadLoop → BattleLoadOK → StartGame → syncLoop(100帧) → BattleEnd → BattleReward → GameOver → 回到 businessWeight`
