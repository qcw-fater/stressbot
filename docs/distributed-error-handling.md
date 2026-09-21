# 分布式异常处理机制

> 本文档描述 stressbot 在分布式（1 个 Admin + N 个 Agent）部署下的完整异常处理实现。
> 全部以代码为准，不写"应该怎样"，只写"实际怎样"。每处行为描述均标注源码文件与行号，便于对照阅读。

**设计前提**：Admin 是单点的任务调度 / 指标聚合中心，运维上保证其足够稳定；Agent 是水平扩展的执行节点，允许随时上下线。整体策略：

- **Admin 不可达 → Agent 侧收缩**：gRPC 会话断开即刻感知，任务生死由**租约倒计时**决定（断连超过 `leaseDuration` 才取消任务，期间持续重连，恢复后任务无感继续）
- **Agent 异常 → Admin 侧兜底容错**：心跳超时分级（unhealthy/offline）、任务完成报告合成、停止超时安全网、全节点失效自动停止
- **控制面消息不丢**：命令走持久化命令日志 + 至少一次投递 + ACK/回执；最终报告走 Agent 侧待确认队列 + 重放

---

## 目录

- [1. 全局约定](#1-全局约定)
  - [1.1 协程池与 panic 恢复](#11-协程池与-panic-恢复)
  - [1.2 顶层 recover 层级](#12-顶层-recover-层级)
  - [1.3 配置化的超时与重连参数](#13-配置化的超时与重连参数)
- [2. Admin 异常退出 → Agent 端处理](#2-admin-异常退出--agent-端处理)
  - [2.1 会话断开与重连](#21-会话断开与重连)
  - [2.2 租约判活](#22-租约判活)
  - [2.3 Admin 重启](#23-admin-重启)
  - [2.4 Admin 主动关闭](#24-admin-主动关闭)
  - [2.5 任务完成报告的至少一次语义](#25-任务完成报告的至少一次语义)
  - [2.6 Agent 优雅关闭](#26-agent-优雅关闭)
- [3. Agent 异常退出 → Admin 端处理](#3-agent-异常退出--admin-端处理)
  - [3.1 Admin 感知 Agent 状态的通道](#31-admin-感知-agent-状态的通道)
  - [3.2 心跳超时阈值与状态扫描](#32-心跳超时阈值与状态扫描)
  - [3.3 单节点离线 → 任务级处理](#33-单节点离线--任务级处理)
  - [3.4 Agent 进程重启（实例变化）](#34-agent-进程重启实例变化)
  - [3.5 自然完成路径](#35-自然完成路径)
  - [3.6 用户手动停止任务](#36-用户手动停止任务)
  - [3.7 自动停止（autoStopTask）](#37-自动停止autostoptask)
- [4. 任务生命周期与持久化](#4-任务生命周期与持久化)
  - [4.1 Admin 重启 → 活跃任务恢复](#41-admin-重启--活跃任务恢复)
  - [4.2 持久化原子性](#42-持久化原子性)
  - [4.3 终态归档异步化](#43-终态归档异步化)
- [5. 实现细节与陷阱](#5-实现细节与陷阱)
- [6. 任务启动/停止失败回滚](#6-任务启动停止失败回滚)
- [7. 故障行为速查表](#7-故障行为速查表)
- [8. 前端可观察项](#8-前端可观察项)

---

## 1. 全局约定

### 1.1 协程池与 panic 恢复

所有业务 goroutine 走 `internal/workpool`（`workpool.Default()`，方法 `Go` / `GoWithStop` / `Submit` / `SubmitWithStop`；`internal/workpool/workpool.go:184-202`）。工程规范约束**禁止裸 `go func`**。

**双层 panic 恢复**（`internal/workpool/workpool.go:61-70`、`:145-152`）：

1. **ants 池级 PanicHandler**：`Init()` 时设置，捕获 panic 写 DPanic 日志（含 stack）
2. **任务级 defer recover**：`submit` 中每个提交到 ants 的闭包自带 `recover()`，再次捕获并记录

实现要点：

- 底层 `github.com/panjf2000/ants/v2`，`Pool` 维护 `sync.WaitGroup` 与停止通道
- `submit` 先登记 wg 再二次检查 stopped（`:111-113`），保证 Shutdown 的 `wg.Wait()` 不因竞态遗留计数而永久阻塞
- `Shutdown()` 等待最长 5s（`poolShutdownTimeout`，`:23`），超时后放弃；`GoWithStop` 型任务可通过 `StopChan()` 提前退出
- 池容量默认无限制（ants 语义），Admin / Agent 进程退出时统一调用 `Shutdown`

### 1.2 顶层 recover 层级

| 层级 | 文件 / 函数 | 行为 |
|------|------------|------|
| 进程级 | `cmd/agent/main.go:51-64`、`cmd/admin/main.go:51-64` | 顶层 `defer recover()`，stderr + 日志，exit 2 |
| HTTP 层 | `admin/httpapi/routes.go:114-126`（Wrap 中间件） | 捕获 handler panic → 日志（path/method/panic/stack）→ 标准 500 JSON；请求体先经 OpenAPI Schema 校验，非法返回 `REQUEST_SCHEMA_INVALID` |
| 协程池层 | `internal/workpool/workpool.go`（PanicHandler + 任务级 defer） | goroutine panic 写 DPanic 日志，不外泄 |
| 业务任务级 | `agent/app.go:271-275`（executeTask）、`agent/task/runner.go` 各错误分支 | 单任务崩溃不影响主循环与后续任务 |

### 1.3 配置化的超时与重连参数

三进程配置均为 TOML：`conf/stressbot.toml`、`conf/agent.toml`、`conf/admin.toml`。加载走 `config.LoadTOML`（严格模式：未知字段启动失败，`config/load.go:30-46`）+ 环境变量展开（`${VAR}` 未定义报错 / `${VAR:-default}` / `$$`，`config/load.go:68` 起）。

**时长纪律**：时长类配置统一 `config.ParseDuration` / `config.ParsePositiveDuration`（`config/duration.go:21-50`）——**留空回退默认，写错一律报错**，不允许"解析失败打 Warn 退默认"。

**Agent 侧参数**（`agent/config.go:59-93`、`agent/config.go:108-157`）：

| 配置项 | 默认值 | 含义 |
|--------|--------|------|
| `agent.heartbeatInterval` | 10s | 心跳生产兜底间隔（Welcome 下发值优先） |
| `agent.metricsInterval` | 5s | 压测/系统指标上报周期 |
| `agent.reconnect.initialInterval` | 5s | 重连退避初始间隔 |
| `agent.reconnect.maxInterval` | 30s | 重连退避上限（实际等待在此值基础上 [0, 2×] 抖动） |
| `agent.reconnect.taskReportTimeout` | 30s | 最终报告确认等待超时 |
| `agent.reconnectMaxRetries` | -1 | 0 视为 -1（JSON/TOML 零值歧义修正，`resolveReconnectRetries`，`agent/config.go:159-164`） |

**Admin 侧 controlPlane 参数**（`admin/config.go:48-55`）：

| 配置项 | 默认值 | 含义 |
|--------|--------|------|
| `heartbeatInterval` | 10s | 期望心跳间隔，随 Welcome 下发 |
| `unhealthyAfter` | 30s | 心跳超时标记 unhealthy 阈值 |
| `leaseDuration` | **45s** | 任务租约：Agent 断连超过该时长才取消任务 |
| `offlineAfter` | 60s | 标记 offline 并从注册表删除的阈值 |

**联动强校验**（`admin/config.go:189-219`，写错启动失败）：

```
heartbeatInterval < unhealthyAfter < offlineAfter
heartbeatInterval < leaseDuration  < offlineAfter
```

设计意图：`leaseDuration` 必须明显大于 Agent 重连最坏耗时（`reconnect.maxInterval` 的 2 倍抖动上界 + 拨号握手），且小于 `offlineAfter`——让 **Agent 侧租约先于 Admin 离线清理接管任务生死**，两套判定窗口不重叠，避免"Admin 已把节点删了，Agent 任务还在跑"的状态错乱。

**调参联动**：把 `reconnect.maxInterval` 从 30s 调大后，必须同步调大 `leaseDuration` 并保持其小于 `offlineAfter`；调大 `offlineAfter` 需同步评估 `unhealthyAfter`。

---

## 2. Admin 异常退出 → Agent 端处理

> 总原则：Admin 挂掉时 Agent 行为统一为"断连感知 → 重连；租约到期才取消任务，取消后不补档"。

### 2.1 会话断开与重连

**代码位置**：`agent/session/supervisor.go:31-85`、`agent/session.go:28-87`

每条 gRPC 连接上并行四路循环（发送 / 接收 / 指标流 / 心跳生产，`agent/session.go:64-76`），任一路返回错误即会话结束，进入 `RunSupervisor` 重连：

1. `grpc.NewClient` 建连 → `runConnection`（Hello → Welcome 握手 → 四路循环）
2. 会话结束：**存活 ≥ `maxInterval` 的会话视为健康连接**，断线是新一次偶发故障的首次失败——退避策略与重试计数重置回初始间隔（`supervisor.go:46-54`）；更短存活的断线按既有退避继续升级
3. 退避：指数倍增（factor 2）+ 抖动，5s 起、30s 封顶，实际等待在 `[0, 2×maxInterval]` 内
4. 不可恢复错误（Unauthenticated / PermissionDenied / FailedPrecondition，如协议版本不匹配）直接返回 → Agent 进程退出（`supervisor.go:59-62`）
5. `reconnectMaxRetries >= 0` 且超次数 → 放弃退出；默认 -1 持续重连

**指标不丢账**：会话断开期间 `LatestMetrics` 单槽继续收新帧，未发送的被覆盖帧累计 `droppedIntervals`，随重连后第一帧带给 Admin 补账（`agent/metrics/stream.go:41-54`）。

**命令不丢**：重连后 `Commands.Replay` 唤醒会话发送循环，按投递游标重放未 ACK 命令（`admin/grpcapi/control_service.go:143-145`、`admin/command/bus.go:88-94`）；命令 `delivery_generation` 与新会话不匹配时 Admin 不投递旧代命令。

### 2.2 租约判活

**代码位置**：`agent/session.go:128-143`

```
leaseLoop（每秒）:
  deadline = leaseDeadline.Load()          // Welcome 初始化，每条 HeartbeatAck 续期
  if deadline 已过 && 当前 busy:
      cancelCurrentTask("Admin 控制面租约已过期")
      leaseDeadline.Store(0)               // 只触发一次
```

- 断连后租约倒计时**继续**：重连成功即续期，任务无感继续；持续失联超过 `leaseDuration`（默认 45s）才取消任务
- 任务取消经 `taskCancelCause` 携带原因，`StopAll` 后按 `context.Canceled` 归类为 `Stopped` 结果（`agent/task/runner.go:275-277`）
- 空闲（idle）状态下租约过期无动作——没有任务可取消

**任务取消后的清理链**（`agent/app.go:277-284` defer）：

1. `taskCancelCause(err)` → Runner 的 `select <-ctx.Done()` → `mgr.StopAll()`（采集 `CleanupStatus`）
2. `stressReporter.Stop()`（幂等，冲刷最后一帧窗口）
3. 最终报告（result=stopped + finalSnapshot + cleanupStatus）进入 outbox 尝试上报与重放
4. `currentTask = nil`、`status = idle`，Agent 回到空闲态等待新命令

### 2.3 Admin 重启

Admin 重启后命令日志（内存）丢失、任务从磁盘恢复为 failed（见 §4.1）。Agent 侧重连后：

1. Hello 携带 `current_task_id`（若任务仍在跑）
2. Admin `OwnsActiveTask` 判定该任务已不是活跃任务 → 立即 `ScheduleStop("Admin 不再持有该任务，停止孤儿压测")` 并标记 orphanStop 防重复（`admin/grpcapi/control_service.go:106-123`）
3. Agent 收到 StopTask → 取消任务 → 上报 stopped 报告（Admin 侧任务已终态，报告被静默丢弃并终态 NACK）

无任务运行时，Hello 即普通重注册，节点回到 idle 等待分配。

### 2.4 Admin 主动关闭

**代码位置**：`admin/grpcapi/session.go:229-241`、`admin/app.go:293-320`、`admin/server.go:101-131`

Admin 收到退出信号后：

1. `sessions.Close("Admin 正在关闭")`：向全部会话广播 `ServerClosing{reason}` 并取消其 context
2. Agent `ReceiveLoop` 收到 ServerClosing → 以 `Unavailable` 结束会话（`agent/session/transport.go:147-148`）→ 进入正常重连流程（Unavailable 是可恢复错误）
3. gRPC `GracefulStop`（10s 上限）→ HTTP 管理面 `Shutdown`（10s 超时）→ 协程池 → 存储

关闭顺序的防死锁设计：先 `runtimeCancel` + `close(stopCh)` 再等待各服务（若把 cancel 留在 Run 的 defer 中，Shutdown 会先等指标 worker，而 worker 只能在 Shutdown 返回后收到 cancel，形成关闭死锁；`admin/app.go:294-299` 注释）。

### 2.5 任务完成报告的至少一次语义

**代码位置**：`agent/session/report_outbox.go`、`agent/app.go:305-324`

`executeTask` 在 Runner 返回后组装最终报告并 `reportOutbox.Offer`：

- 报告以 `report_id`（agentID-taskID-stageIndex-时间戳）为键，容量 128
- 在 `taskReportTimeout`（默认 30s）内等待确认通道；**超时只是放弃等待**，报告保留在 outbox
- 会话发送循环在报告入队与会话重建时重放全部未确认报告（`agent/session/transport.go:95-100`）
- `ReportAck.accepted=true` → 移除并通知等待方；`accepted=false` → 传递拒绝原因（永久性拒绝如"节点不属于该任务"，Agent 不再重发）
- Admin 侧对迟到报告的兜底：`CompletionService.Accept` 校验汇报者在预期名单内，不在则返回 `PermanentReportError`，gRPC 层终态 NACK（`admin/task/completion.go:26-40`）

### 2.6 Agent 优雅关闭

**代码位置**：`agent/app.go:335-366`

```
shutdown():
  1. triggerStop（sync.Once）+ shuttingDown = true（拒绝新任务预留）
  2. cancelCurrentTask("agent shutdown")
  3. 协程池内 taskWG.Wait()，主流程最多等 taskReportTimeout + 5s
     （Wait 放在协程池里执行、主流程用 timerpool 计时等待——避免全部协程互相等待且池满的死锁）
  4. stressReporter.Stop()（保护性）/ sysReporter.Stop()
  5. cancel 全局 ctx → 四路会话循环、监督循环、系统采集循环退出
```

**远程关机的两段确认**：收到 Shutdown 命令时 Executor 只发 ACK（applied）并 `PrepareShutdown` 记录命令 ID/序号；进程退出延迟到 Admin 持久化该 ACK 并回发 `CommandReceipt`（`agent/command/executor.go:223-226`、`agent/app.go:121-137`）。若 ACK 丢失，Admin 会重放命令，此时进程必须还活着才能重放结果——这是把退出推迟到回执的原因（`controlplane/proto/control.proto:122-128` 注释）。

---

## 3. Agent 异常退出 → Admin 端处理

> 总原则：单节点离线不停整个任务（除非全离线）；异常要在任务事件与最终报告里体现。

### 3.1 Admin 感知 Agent 状态的通道

| 通道 | 触发点 | 代码位置 | 作用 |
|------|--------|---------|------|
| 心跳超时扫描 | `Registry.scanAndMarkStatus`（5s/次） | `admin/agent/registry.go:378-393` | 主动检测，标记 unhealthy / offline |
| gRPC 会话生命周期 | `Session` 退出 / `Attach` 顶替 / `Detach` | `admin/grpcapi/control_service.go:96-103`、`admin/grpcapi/session.go:154-203` | 会话级 generation 管理、旧代指标丢弃 |
| 任意请求 Touch | `Registry.Touch` | `admin/metrics/accept.go:38`、`admin/task/completion.go:60` 等 | 把指标帧/报告等请求当 keepalive |

**Touch 与 Heartbeat 的差别**：

- `Touch`（`admin/agent/registry.go:237-250`）：只刷新 `LastHeartbeatAt` 与 appVersion；触发 unhealthy/offline → 在线恢复
- `Heartbeat`（`:128-160`）：额外更新语义字段 `CurrentTaskID`/`CurrentBots`；任务切换时清空 `LatestStress` 防旧任务指标串入
- `StartTask`（`:195-211`）：Start ACK 提交后同步节点为 busy——首个指标窗口无需等心跳
- `CompleteTask`（`:215-234`）：仅在 taskID 仍是节点当前任务时清理回 idle；返回 false 表示报告迟到

### 3.2 心跳超时阈值与状态扫描

**代码位置**：`admin/agent/registry.go:395-442`

`scanAndMarkStatus` 每 5 秒遍历全部节点：

```
lag = now - LastHeartbeatAt
lag >= offlineAfter(60s)   → 标记 Offline，**立即从注册表删除**（无延迟清理队列）
lag >= unhealthyAfter(30s) → 标记 Unhealthy（仍在线，告警）
其余                        → 保持业务状态
```

- 状态变更收集后在**锁外** `fireOnChange`（防 `agents.mu → tasks.mu` 的 AB-BA 死锁）
- offline 状态是瞬态的：标记、触发回调、删除一气呵成；该节点 ID 可被后续 Hello 重新注册
- unhealthy/offline 节点收到任何请求（心跳/指标/报告）时经 `touchLocked` 自动恢复：有任务 → busy，无任务 → idle（`:254-277`）

### 3.3 单节点离线 → 任务级处理

**代码位置**：`admin/app.go:421-520`（onAgentStatusChange）

回调先过滤：无活跃任务、或离线节点不在活跃任务分配名单中 → 不动作。分配节点离线时记录 `AgentEvent{Type:"offline"}`，然后按任务状态分支：

- **任务 Stopping 时节点离线**：立刻合成 `ResultFailed + "节点离线"` report（CleanupStatus 标记"清理状态未知"）；若全部预期节点已有 report → 转 Stopped（`admin/app.go:485-516`）
- **任务 Running/Starting 时节点离线**：`checkAndStopIfAllLost` 检查剩余节点（`:518-519`）

**`checkAndStopIfAllLost` 判定**（`admin/app.go:689-717`）：

```
任务的某个预期节点（SucceededAgents 优先，空则 Assignments）视为"已失效"当：
  - 该 agentID 已有 Reports[agentID]（已合成或自然上报）
  - 或节点已从注册表删除
  - 或节点 status == Offline
  - 或节点 CurrentTaskID != 当前 taskID
全部预期节点失效 → autoStopTask(taskID, "所有分配节点已失效")
```

### 3.4 Agent 进程重启（实例变化）

**代码位置**：`admin/agent/registry.go:79-125`、`admin/app.go:522-546`

Registry 为每个节点维护 `instances[agentID]{startedAt, taskID}`。Hello 注册时若旧实例 `StartedAt` 与新 Hello 不同且旧实例仍持有任务 → 锁外触发 `onRestart(agentID, lostTaskID)`：

1. 记录 `AgentEvent{Type:"restarted", Detail:"节点进程实例已变化，原任务已丢失"}`
2. 立即为该节点合成 `ResultFailed + "节点进程重启，任务已丢失"` report（避免任务卡等一份永远不会到来的报告）
3. `checkAndStopIfAllLost`：剩余节点够则任务继续，全失效则转 failed

**不补档**：新实例是空白节点，不会被塞回旧任务；旧任务在该节点上的机器人量直接放弃。运行任务期间的新节点注册不受限制（同 ID 覆盖）。

### 3.5 自然完成路径

**代码位置**：`admin/task/completion.go:59-100`（CompletionService.Accept）

```
gRPC FinalReport
  → agents.Touch（刷新心跳）
  → 校验汇报者在预期名单（ExpectedAgents：SucceededAgents 优先，空则 Assignments）
     不在 → PermanentReportError（终态 NACK，Agent 停止重发）
  → StageIndex > 0：阶段报告，同节点同阶段幂等覆盖 StageReports，不触发状态转换
  → 最终报告：Reports[agentID] = report
  → 全部预期节点汇报完毕：聚合 CleanupSummary → running/stopping → stopped
```

### 3.6 用户手动停止任务

**代码位置**：`admin/httpapi/task_routes.go:497-548`

1. 任务必须 running（否则 `TASK_INVALID_STATE`）
2. `running → stopping`
3. 为目标节点（SucceededAgents 优先）创建 StopTask 命令；**命令创建失败 → 回滚 `stopping → running`** 并返回 500（`task_routes.go:530-533`；该回滚边在 `validTransition` 中显式合法，`admin/task/store.go:276`）
4. `synthesizeOfflineReports`（`admin/app.go:559-614`）：为已离线且未上报的节点合成 `ResultStopped + "节点离线，未上报"` report
5. 全部已有 report → 立即 `stopping → stopped`；否则启动 60s 安全网
6. Agent：StopTask → `CancelTask(taskID, reason)` → 停机器人 → stopped 报告

**停止超时安全网**（`admin/app.go:616-683`）：

```
stopWaitTimeout = 60s
  必须 ≥ Agent 端 Manager.closeRobotsTimeout(15s) + Robot.Close 兜底(10s) + 网络上报延迟，
  否则 Agent 真实上报到达前 Admin 已合成 fake report 并归档，真实指标被丢弃（:616-620 注释）
超时后：为未上报节点合成 ResultStopped + "停止超时，节点未响应" → stopping → stopped
```

安全网用 `GoWithStop` + `waitStopGuard`（select stopCh / timer）：进程关闭时立即放弃等待，不占协程导致 workpool 关闭超时（`:622-633`）。

### 3.7 自动停止（autoStopTask）

**代码位置**：`admin/app.go:720-788`

触发场景：

- 所有分配节点失效 — 由 `checkAndStopIfAllLost` 调用
- Deadline 超时 — `startDeadlineWatchdog`（5s/次）检查活跃任务 `Config.Deadline`（`admin/app.go:802-828`）

行为：

1. 任务在 Starting：直接 `starting → failed`（命令尚未被节点接受，无需 stop）
2. Running：`running → stopping` → 为目标节点创建 StopTask 命令（10s 超时）
3. 为所有未上报节点合成 `ResultFailed + reason` report
4. `stopping → failed`

---

## 4. 任务生命周期与持久化

### 4.1 Admin 重启 → 活跃任务恢复

**代码位置**：`admin/task/store.go:51-67`、`admin/task/admin_store.go:24-28`

`NewStore` 加载 `data/tasks/*.json` 时，状态为 starting/running/stopping 的任务：

1. 重置为 `Failed`，`ErrorMsg = "admin restart, task lost"`，`StoppedAt = now`
2. 记入 `recoveredIDs`
3. `SetOnTerminal` 注册回调后，对每个恢复任务**补放一次** onTerminal → 归档到 history（`admin/task/store.go:71-79`）

Agent 端对应路径见 §2.3：重连后 Hello 触发孤儿任务停止。

### 4.2 持久化原子性

**代码位置**：`admin/task/persist.go`

- 任务 JSON 写入：临时文件 + `os.Rename` 原子替换；`Create/Update/Transition` 后同步落盘
- 加载时跳过半写文件；解析失败的文件跳过并打 Warn
- 命令日志、指标窗口、AgentRegistry 均为内存态（重启丢失/重建）；资源包内容寻址可按任务配置重建

### 4.3 终态归档异步化

**代码位置**：`admin/task/store.go:188-211`、`admin/app.go:322-370`

`Transition` 进入终态时：

1. 同一临界区内清除 `activeID`（与状态写入同锁，杜绝"activeID 指向已终结任务"的永久卡死，见该处注释）
2. 以 `TerminalClone`（JSON 深拷贝）副本提交协程池异步执行 `onTerminal`

`onTaskTerminal`：停 Sampler → 共享状态命名空间清理登记（Redis）→ 优先用 Agent 终止报告聚合最终快照（`buildFinalStressFromReports`，过滤空快照），空则兜底窗口聚合 → `history.Archive`。history 未启用时 `metricsWindows.DropTask` 释放窗口状态。

---

## 5. 实现细节与陷阱

### 5.1 会话 generation

- `SessionRegistry.Attach` 为每次会话递增 generation 并顶替旧会话（取消旧 context，`admin/grpcapi/session.go:154-177`）；per-Agent gate 串行化 Attach 与 WithCurrent，防旧流事件落库前被顶替（`:181-194`）
- 命令投递携带 `delivery_generation`，Agent 校验与当前会话一致才入队（`agent/session/transport.go:126-128`）；指标流每帧校验 generation（`admin/grpcapi/metrics_service.go:38-43`）
- 会话结束统一 `DropGeneration` 清理该代指标槽位（`admin/grpcapi/control_service.go:98`）

### 5.2 锁顺序约定

**全局顺序**：`agents.mu` → `tasks.mu`。

- `Registry.fireOnChange` 在锁外触发回调（`admin/agent/registry.go:65-73`），`onAgentStatusChange` 随后才进入 tasks 操作
- `CompletionService.Accept` 先 `agents.Touch`（获取/释放 agents.mu），再 `tasks.Update`（tasks.mu）
- `Repository.Transition` 的终态回调经深拷贝 + 协程池异步执行，锁内不触发跨模块调用
- 新增涉及两者的逻辑必须保持该顺序，或确保两者不在同一调用链同时持有

### 5.3 所有 Transition 错误都已检查

全部 `Transition` 调用点的错误均落日志（"状态转换失败"），代表"状态机被另一条路径抢先转换"（如手动停止与自然完成竞争），属良性竞态，记录即可不回退。调用点分布：`onAgentStatusChange`（stopping→stopped）、`startStopTimeout`、`autoStopTask`、`handleStartTask`/`startTaskBackground`（starting→failed/running）、`handleStopTask`（running→stopping、stopping→stopped/running 回滚）。

### 5.4 命令幂等与重放

- Admin 命令日志按全局递增 Sequence 排序；会话投递游标（非 ACK 游标）保证健康会话每条命令只流一次、重连后从头补发（`admin/command/bus.go:96-114`）
- 在途窗口 `CommandDispatchWindow = 16`：未 ACK 命令占满配额时暂停拉取，ACK 后自动补投（`admin/grpcapi/session.go:43-64`）
- Agent 对同 `commandId` 重放直接回放既有 ACK 结果（`agent/command/executor.go:96-98`）；重复 StartTask 且任务相同 → DUPLICATE
- 瞬时错误（canceled/deadline/unavailable）不落 REJECTED，交由命令重放重试（`agent/command/executor.go:286-296`）
- StartTask 期间会话中断：`context.AfterFunc` 把会话取消转成任务取消原因 `errTaskSessionInterrupted`，释放预留并静默（等命令重放），不产生永久 REJECTED（`agent/command/executor.go:159-186`）

### 5.5 协程池容量与 Shutdown

- `Shutdown(5s)` 超时后放弃等待；`GoWithStop` 任务经 `StopChan()` 感知关闭提前退出
- `submit` 的「先登记 wg 再二次检查 stopped」消除与 Shutdown 的计数竞态（`internal/workpool/workpool.go:111-113`）
- Admin 关闭顺序先 cancel runtimeCtx 再等待各服务（§2.4）；Agent 的 `taskWG.Wait()` 在协程池内执行、主流程限时等待（§2.6）——两处都是"不要在等待方占住池"的同型设计

---

## 6. 任务启动/停止失败回滚

### 6.1 启动失败（`admin/httpapi/task_routes.go:465-495`）

`startTaskBackground`（协程池异步）：

1. 任务不存在（并发删除）→ 停 Sampler 返回
2. `ScheduleStart`：打包资源 + 组装 StartTask 命令 + 批量写入命令日志；失败 → `starting → failed` + 停 Sampler
3. 成功 → 成功节点记入 `SucceededAgents` → `starting → running` → `finishTaskIfFullyReported` 兜底（节点瞬间完成时立即终态）

命令已写入但节点未接受的情况由命令重放与孤儿停止机制收敛，无需逐节点回收。

### 6.2 停止失败

停止命令创建失败 → 回滚 `stopping → running`，前端收 500（`admin/httpapi/task_routes.go:530-533`）；`validTransition` 显式允许该回滚边（`admin/task/store.go:276`）。

### 6.3 分布式参数拆分失败前置

`ValidateDistributedConcurrency` 在启动入口拒绝 `0 < concurrency < 分配节点数` 的组合（`admin/task/distribution.go:30-47`）；`SplitGlobalValues` 运行期兜底保底 1 + 总和守恒（`:69-124`）。

---

## 7. 故障行为速查表

| 故障场景 | Agent 行为 | Admin 行为 | 任务最终状态 |
|---------|-----------|-----------|------------|
| Admin 挂掉，Agent Idle | 重连退避；系统指标本地继续采集 | — | 无任务 |
| Admin 挂掉，Agent Busy | 重连退避；租约到期（默认 45s）取消任务 → idle；未确认报告留在 outbox | — | 任务在 Agent 端被丢弃，Admin 恢复后由孤儿停止收尾 |
| Admin 重启 | 重连后 Hello 携带 currentTaskId → 收到孤儿 StopTask → 取消并上报 | 活跃任务恢复为 failed 并补归档 | failed（旧） |
| Admin 主动关闭 | 收到 ServerClosing → 以 Unavailable 重连 | 广播 ServerClosing → GracefulStop | 不变（重连后续跑） |
| Admin 持续不可达 + 重连超次数（默认 -1 不触发） | 退出进程 | — | 任务被丢弃 |
| Agent 进程退出，无任务 | — | 60s 后 offline 并从注册表删除 | 无影响 |
| Agent 进程退出，有任务 | — | offline 事件；stopping 中合成 report，running 中检查全失效 | 剩余节点继续 / 全失效则 failed |
| Agent 进程重启，无任务 | Hello 重注册 → idle | 覆盖旧 entry | 无影响 |
| Agent 进程重启，有任务 | 新实例 Hello | restarted 事件 + 合成 failed report + 检查全失效 | 剩余节点继续 / 全失效则 failed |
| 所有分配节点离线或重启 | 各自走重连 | checkAndStopIfAllLost → autoStopTask | failed |
| 任务运行中新节点注册 | 注册成功 → idle 等待分配 | 接受注册（同 ID 覆盖） | 不影响当前任务 |
| 用户手动停止 | 收 StopTask → 停机器人 → stopped 报告 | 离线节点合成 report；60s 安全网 | stopped |
| 停止命令创建失败 | — | 回滚 stopping → running | running |
| 任务 deadline 超时 | 收 StopTask | autoStopTask("任务超时") | failed |
| 会话在 StartTask 下载期间断开 | 下载中止、预留释放、命令重放重试 | 命令未 ACK → 重连后重放 | starting/running 不变 |
| HTTP handler panic | — | recover 中间件捕获，500 JSON + 日志 | 不影响其他请求 |
| 业务 goroutine panic | 协程池双层恢复捕获 | 同左 | 该 goroutine 退出 |
| 进程顶层 panic | `cmd/agent` recover + exit 2 | `cmd/admin` recover + exit 2 | 由进程管理器拉起 |
| 指标帧迟到/重复 | — | 窗口序列号判 duplicate 丢弃；系统帧按 sequence 去重 | 不影响 |
| 压测帧任务不匹配 | — | AcceptanceService 静默丢弃（返回 nil） | 不影响 |

---

## 8. 前端可观察项

| 数据来源 | 字段 | 渲染位置 |
|---------|------|---------|
| `GET /sbot/agents` | `status`（idle/busy/unhealthy/offline） | 节点列表、在线计数 |
| `GET /sbot/tasks/{id}` | `agentEvents[]`（offline/reconnected/restarted） | 任务详情时间线、异常徽标 |
| `GET /sbot/tasks/{id}` | `cleanupSummary`（全节点清理状态汇总） | 最终报告清理面板 |
| `GET /sbot/metrics` | `reportingAgents`/`freshAgents`/`staleAgents`/`coverageRatio`/`asOf` | 数据覆盖率提示 |
| Task `state == failed` + `errorMsg` | `errorMsg` | 最终报告 banner |
| `GET /sbot/history/{id}` | `agentReports[].cleanupStatus` | 历史详情节点报告 |

---

## 附录：任务状态机

```
             ┌──────────────────────────────→ stopped ←─────────────────┐
             │                                  ↑                       │
pending ──→ starting ──→ running ──→ stopping ──┘（回滚边：stopping → running，§6.2）
   │           │            │            │
   ↓           ↓            ↓            ↓
 failed      failed      failed       failed
```

状态转换合法性校验（`admin/task/store.go:267-280`）：

| from | to |
|------|----|
| `pending` | `starting` / `stopped` / `failed` |
| `starting` | `running` / `failed` |
| `running` | `stopping` / `stopped` / `failed` |
| `stopping` | `running`（停止命令创建失败回滚）/ `stopped` / `failed` |

终态：`stopped`、`failed`。活跃态（`IsActiveState`，`admin/task/types.go:33-35`）：`starting`、`running`、`stopping`——全集群同时最多一个活跃任务（`Begin` 单例入口，`admin/task/store.go:249-265`）。
