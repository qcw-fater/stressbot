# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

stressbot 是一个可配置化通用游戏服务器压测工具，用 Go 编写。它是旧版 "Robot" 压测工具的重构版本，核心思路是解耦业务逻辑与框架：所有消息收发、字段填充、随机化、心跳、回调、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，少量难以通用的行为通过 **Lua 脚本** 实现。

一套 `conf/flow.json + conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测。

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
go build -o stressbot.exe ./cmd/stressbot

# 校验 flow.json（每次修改 flow.json 后务必执行）
go run ./cmd/validate conf/flow.json

# 启动压测
go run ./cmd/stressbot -config conf/config.json

# 指定配置文件路径
go run ./cmd/stressbot -config path/to/config.json
```

## 架构

**启动流程：** `cmd/stressbot/main.go` 加载配置 → 消息头协议 → .proto 文件 → 流程配置 → 启动 gnet 网络引擎 → 创建 Lua 运行时池 → 创建 Robot Manager → 批量启动机器人。

**核心分层（依赖顺序）：**

- **`engine/`** — 流程执行引擎。定义 `TaskFlow`（节点 DAG）、`ActionDef`（声明式动作模式）、`Executor`（节点图遍历）。`Executor` 通过 `ActionHandler` 接口委托实际工作，与 network/robot 层解耦。
- **`robot/`** — `Robot` 是单个压测客户端实例，持有独立的 state、网络连接、Lua 运行时和执行器。`Manager` 负责批量创建和限速启动。`robotActionHandler` 实现 `engine.ActionHandler`，桥接 engine 层与 network 层。`netSenderAdapter` 将 Robot 适配到 `NetSender` 接口。
- **`network/`** — 基于 gnet 的 TCP/UDP 连接层。`Client` 管理多服务 TCP 连接和单个 UDP 连接。`Connection` 处理收发、请求-响应匹配、持久化监听、per-connection 心跳。`Protocol` 处理消息头编解码（XOR/GZIP/加密，由 `header.json` 配置）。`Dialer` 封装 gnet 事件循环。
- **`protox/`** — 动态 protobuf 加载与反射。`Loader` 发现 .proto 文件，`Registry` 编译，`Factory` 按全名在运行时创建/序列化/解析消息。
- **`script/`** — Lua 运行时池（`gopher-lua`）。每个 Robot 获取独占 `LState`。向 Lua 暴露 `network`、`robot`、`utils`、`proto`、`json` 模块。Lua 访问通过 `luaMu` 互斥锁串行化（回调/心跳可能在其他 goroutine 触发）。
- **`state/`** — 线程安全的键值状态存储。保存服务器响应字段（通过 `store` 映射），支持 list/map 操作用于随机选取。

**单次动作数据流：**
1. `Executor` 遍历流程图 → 命中 `action` 节点 → 调用 `ActionHandler.ExecuteAction(actionDef)`
2. 声明式动作：`ActionExecutor` 构建 protobuf 消息（从 state/随机源解析字段绑定）→ 序列化 → 编码消息头 → 通过 `NetSender` 发送 → 接收响应 → 解析 S2C proto → 存储字段到 state
3. Lua 动作（`pattern: "lua"`）：获取 `luaMu` → 通过 `RuntimePool` 执行脚本 → 返回 0 表示成功

**流程图节点类型：** `start`、`sequence`、`action`、`loop`、`boolean`、`weighted`、`wait`。节点支持 `listenCallbacks`（持久化推送监听）、`delayMs`（节奏控制）、`breakOff`（错误传播中断）。

## 配置文件

- `conf/config.json` — 运行配置：机器人数、并发速率、Auth 地址、proto/脚本目录
- `conf/header.json` — 消息头协议定义：magic 字节、字段大小、XOR/GZIP/加密设置、UDP 加密偏移
- `conf/flow.json` — 流程图（节点、动作、回调）— 主要配置产物
- `conf/proto/` — 启动时动态加载的 `.proto` 文件
- `conf/scripts/` — 复杂行为的 Lua 脚本（战斗逻辑、条件判断、心跳 body 构建）

## 关键约定

- `flow.json` 的 nodes 数组按 ID 反序列化为 `map[string]*Node`。保留旧字段别名兼容（`trueBranch`→`trueNext`、`options`→`next`、`listen`→`listenCallbacks`、`value`→`waitSeconds`）。
- `stateRef` 绑定类型保留复杂结构（嵌套消息），`state` 扁平化为原始值。
- UDP 加密使用偏移量部分加密：前 N 字节（由 header.json 的 `encrypt.udpOffset` 配置，默认 11）保持明文供服务端查密钥表，剩余部分加密。
- 默认节点延迟 1 秒（`DefaultNodeDelayMs = 1000`），对齐旧 Robot 工具。`delayMs: -1` 禁用，`delayMs: 0` 使用默认值。
- 日志和错误信息使用中文（对齐旧工具命名）。

## 验证流程

每次对代码进行修改后，必须按以下步骤验证功能正确性，重复迭代直到通过：

1. **编译检查**：`go build ./...` 确保无编译错误
2. **配置校验**：`go run ./cmd/validate conf/flow.json` 确保 flow.json 结构合法
3. **运行测试**：删除旧日志 `rm -f log/stressbot.log`，然后启动 `go run ./cmd/stressbot -config conf/config.json`，运行 2~5 分钟
4. **日志审查**：
   - 检查错误：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无输出
   - 检查战斗循环：`grep -c "BattleEnd" log/stressbot.log` 应 ≥ 2（至少完成两轮完整战斗）
   - 检查帧同步：`grep "SyncFrame: frame=" log/stressbot.log` 应有持续递增的帧号
5. **清理**：确认无误后删除日志 `rm -f log/stressbot.log`，停止进程

**完整战斗循环预期路径：**
`businessWeight → normalModel → CreateNormalTeam → SelectHero → StartMatch → MatchSucceed → startBattle(ConnectBattleTCP + ConnectBattleUDP + RegisterBattle) → loadLoop → BattleLoadOK → StartGame → syncLoop(100帧) → BattleEnd → BattleReward → GameOver → 回到 businessWeight`
