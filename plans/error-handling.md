# 分布式异常处理机制

本文描述 stressbot 在分布式（1 个 Admin + N 个 Agent）部署下的异常处理实现。
全部以代码为准，不写"应该怎样"，只写"实际怎样"。

> 设计前提：Admin 是单点的指标聚合 / 任务调度中心，运维上保证其足够稳定；
> Agent 是水平扩展的执行节点，假设会随时上下线。
> 因此整体策略偏向"Admin 不可达 → Agent 主动收缩；Agent 异常 → Admin 兜底容错"。

---

## 0. 全局约定

### 0.1 并发原语

- 所有业务 goroutine 必须走 `utils.GetWorkPool()`（`Go` / `GoWithStop`）。
  - 协程池内置 `PanicHandler`，捕获 panic 后写 `DPanic` 日志（含 stack trace），不影响其他协程。
  - `grep -rn "^\s*go func\|^\s*go [a-zA-Z]" --include='*.go'` 全工程 0 命中。
- 协程池单例由 `utils.InitWorkPool` 创建，Admin / Agent 进程关闭时调用 `Shutdown()` 等待最长 5s 让池中任务自然结束。

### 0.2 顶层 recover

| 层级 | 文件 | 行为 |
|------|------|------|
| 进程级 | `cmd/admin/main.go`、`cmd/agent/main.go` | 顶层 `defer recover()`，捕获 panic 后写 stderr + 日志，`os.Exit(2)` |
| HTTP 层 | `admin/handlers.go::recoverMiddleware`、`agent/http_server.go::recoverMiddleware` | 包裹整个 mux，捕获 handler panic 后写日志 + 返回 500 JSON（避免连接重置）|
| 协程池层 | `utils/work_pool.go::PanicHandler` + 每个任务 defer | 捕获 goroutine panic 后写 `DPanic` 日志 |
| 业务任务级 | `agent/agent.go::executeTask`、`admin/admin.go::onTaskTerminal` 等关键 goroutine | 局部 `defer recover()`，确保单个任务崩溃不影响其他任务 |

### 0.3 配置化的超时与重连

Agent 与 Admin 通信的所有时间参数都可在 `agent-config.json` 配置；未配置或非法时使用默认值并打 Warn 日志（`utils.ParseDurationDefault` / `parseDuration`）。

| 配置项 | 默认值 | 含义 |
|--------|--------|------|
| `heartbeatInterval` | `10s` | 心跳成功时的下一次间隔 |
| `heartbeatFailInterval` | 同 `heartbeatInterval` | 心跳失败时的下一次重试间隔（更短，加速重连） |
| `requestTimeout` | `30s` | Agent → Admin 单次 HTTP 请求超时（心跳 / 上报 / 任务完成 / 拉取任务）|
| `reconnectInterval` | `5s` | 注册重试的初始退避间隔 |
| `reconnectMaxInterval` | `60s` | 注册重试的退避上限（指数退避封顶）|
| `reconnectMaxRetries` | `-1` | 注册重试最大次数。`-1` = 持续重连永不放弃；`0` 视为未配置 → 走默认 `-1` |
| `taskReportTimeout` | `30s` | 任务完成上报的整体超时 |
| `stressInterval` | `5s` | 压测指标上报周期 |
| `systemInterval` | `5s` | 系统指标上报周期 |

兼容性：旧字段 `maxHeartbeatFailures` / `taskRunAdminLostExit` / `reconnectEnabled` / `registerRetryMaxInterval` 仍可被反序列化，启动时打 Warn 日志说明已废弃 / 已迁移。

校验约束：`heartbeatInterval` 必须 < 25s（远小于 Admin 默认 `unhealthyAfter=30s`），否则启动失败。

---

## 1. Admin 异常退出 → Agent 端处理

> 用户需求 §2：Admin 是核心、原则上不应该挂；一旦挂掉，Agent 行为统一为"丢弃当前任务 + 走重连 = 等价于新的注册"，不补档。

### 1.1 通用心跳失败行为（`agent/agent.go::heartbeatLoop`）

每次心跳失败（含网络超时、连接拒绝、非 2xx 响应）：

- `consecutiveFailures` 递增。
- 日志级别随次数递进：第 1 次 Warn（运行中任务则 Error，见 §1.2）、≤3 次 Warn、>3 次 Error。
- 心跳间隔切换到 `heartbeatFailInterval`（默认与 `heartbeatInterval` 一致）。
- 心跳恢复后 `consecutiveFailures` 归零并打 Info `心跳恢复`。
- **不**主动退出进程；只有"重新注册超出最大重试次数"才会退出（见 §1.3）。

### 1.2 Idle 状态 Admin 挂掉

- 心跳持续失败，日志按上面递进。
- 没有任务可丢弃，Agent 保持存活。
- Admin 恢复后心跳自动恢复，回到正常工作状态。

### 1.3 Busy 状态 Admin 挂掉（任务进行中）

**核心规则（用户需求 §2.2）：第一次心跳失败立刻取消任务，丢弃，不补档。**

```go
if status == StatusBusy && consecutiveFailures == 1 {
    stresslog.Error("[AGENT] 任务运行中与 Admin 断联，立即取消当前任务", ...)
    a.cancelCurrentTask("心跳失败 / Admin 断联")
}
```

为什么是第一次就取消，不是等几次：

- Admin 是唯一的指标聚合点。StressReporter 默认 5s 上报，Admin 挂了上报全部失败。
- 如果等 6 次 × 10s = 60s 再决定，这 60s 的压测流量完全无观测：前端看不到、历史不记录。
- 与其产生无意义的压测流量，不如立刻停止，等 Admin 恢复后再走"全新连接"重连流程。

任务取消后的清理（`executeTask` 的 defer 链）：

1. `taskCancel()` → `TaskRunner.Run` 收到 `<-ctx.Done()` → `Manager.StopAll()` 停所有机器人。
2. `stressReporter.Stop()` 停止指标上报。
3. 任务结束的 `finalSnapshot` 仍会通过 `context.Background() + TaskReportTimeout` 尝试上报一次；Admin 不可达时 30s 后放弃。
4. `currentTask = nil`、`status = StatusIdle`、`runner = nil`、`taskCancel = nil`，Agent 回到 Idle 态。

心跳继续重试，直到 Admin 恢复（情况 A：网络恢复）或 Admin 重启被发现（情况 B：见 §1.4）。

### 1.4 Admin 重启 → 心跳收到 404

`AdminClient.Heartbeat` 检测到 `404` 返回特殊错误 `errNotRegistered`。Agent 处理流程：

1. 如果 Busy → 先 `cancelCurrentTask("Admin 报告未注册，可能重启")`（同 §1.3）。
2. 调用 `registerWithRetry(ctx)` 重新注册。
3. 注册成功 → `regGeneration.Add(1)`，继续心跳，Agent 保持 Idle，等待 Admin 重新分配任务。
4. 注册失败（超出 `reconnectMaxRetries`） → `triggerStop()`，Agent 进程退出（用户需求 §2.3 的极端兜底）。

**用户需求 §2.2 / §2.3 满足证明：**
- §2.1 测试任务没运行：从 1.2 直接走重连。
- §2.2 测试任务在运行：1.3 取消任务（等价于丢弃）→ 转 Idle → 1.4 重连。
- §2.3 思路统一：等价于全新注册 / 重连，重连后 Admin 视为新 Idle 节点，不补档。

### 1.5 注册重连策略（`registerWithRetry` + `RetryWithRetriesAndBackoff`）

- 初始退避 `reconnectInterval`，每次失败翻倍直到 `reconnectMaxInterval`。
- `reconnectMaxRetries < 0` 持续重连；`>= 0` 表示最多重试 N 次。
- 任意 `ctx.Done()` 立刻退出。
- 默认配置（`-1`）下永不放弃，对应"Admin 理论不会挂"的部署前提。

### 1.6 任务运行中的"丢弃后上报"细节

`executeTask` 在 `runner.Run(taskCtx)` 返回后会尝试上报 `TaskCompletionReport`：

- 使用 `context.Background()` + `TaskReportTimeout`（默认 30s）。脱离已被 cancel 的 taskCtx，让上报有机会在 Admin 恢复时发出去。
- **只尝试一次**，失败后打 Warn 日志即返回。
- 失败的语义：任务已丢弃，由 Admin 心跳超时安全网在 60s 后自动合成 offline report（见 §2.3）。

### 1.7 Agent 优雅关闭（`shutdown`）

执行顺序：

1. `cancelCurrentTask("agent shutdown")` 触发任务取消。
2. `taskWG.Wait()` 等待 `executeTask` 完整结束（包括 finalSnapshot 上报），最长等待 `TaskReportTimeout + 5s`。
3. `stressReporter.Stop()` / `sysReporter.Stop()`（幂等，`sync.Once` 保护）。
4. `a.cancel()` 取消全局 ctx → 心跳 / 轮询 / system 上报循环退出。
5. `httpCli.Deregister(deregCtx)` best-effort 注销（5s 超时）。
6. 关闭 HTTP 服务器。
7. `utils.GetWorkPool().Shutdown()` 等待协程池任务完成。

注意：等待 `taskWG` 改成独立 channel + select 形式，**不在协程池里 Wait**，规避"全部协程都在等其他协程，但池容量已满 / 关闭"的死锁场景。

---

## 2. Agent 异常退出 → Admin 端处理

> 用户需求 §3：单节点离线不停整个任务（除非全离线）；要在测试监控 / 测试结果中体现。

### 2.1 Admin 感知 Agent 状态的两种通道

| 通道 | 触发点 | 作用 |
|------|--------|------|
| 心跳超时扫描 | `AgentRegistry.scanAndMarkStatus`（5s/次） | 主动检测，标记 unhealthy / offline |
| 任意请求 Touch | `AgentRegistry.Touch`（每个 Agent → Admin 的 HTTP handler 都调用） | 把任意请求当 keepalive，避免心跳本身丢包导致误判 |

用户需求 §6.1 的权衡：实现选择"任意请求都更新 LastHeartbeatAt"。理由：

- Agent 默认 5s 一次 stress 上报、5s 一次 system 上报、10s 一次心跳，三类请求频次差不多。
- 任一通道存活就说明 Agent 在线，标记 offline 应该以"全部通道都失败"为准。
- 实现成本只是 4 个 handler 各加一行 `s.agents.Touch(agentID, "")`。

`Touch` 与 `Heartbeat` 的差别：`Touch` 只更新 `LastHeartbeatAt` 和 `AppVersion`，不动 `CurrentTaskID` / `CurrentBots`（那些是心跳的语义性字段，必须由心跳路径更新）；同时会触发 unhealthy/offline → 在线 的恢复。

### 2.2 心跳超时阈值（默认值，可在 `admin-config.json::agentRegistry` 配置）

| 距上次活动 | 状态变更 | 含义 |
|-----------|---------|------|
| `< unhealthyAfter`（30s） | 保持业务状态 | 健康 |
| `>= unhealthyAfter` | → `Unhealthy` | 告警，但仍视为在线 |
| `>= offlineAfter`（60s） | → `Offline` | 视为离线，触发任务侧处理 |
| `Offline` 且 `> purgeAfter`（24h） 且 `CurrentTaskID == ""` | 从注册表删除 | 清理 |

### 2.3 单节点离线 → 任务级处理（`onAgentStatusChange`）

| 任务状态 | 节点离线时的动作 |
|----------|------------------|
| 无活跃任务 | 不动作，节点列表显示 offline |
| 活跃任务 + 该节点未被分配 | 忽略 |
| 活跃任务 + 已分配节点 + `Running` | 记录 `AgentEvent{Type:"offline"}` → 调用 `checkAndStopIfAllLost` → 仅当所有分配节点都失效才 `autoStopTask` |
| 活跃任务 + 已分配节点 + `Stopping` | 立即合成 `ResultFailed + "节点离线"` 的 report；若所有节点都已有 report → 转 `Stopped` |
| `Offline → Idle/Busy`（恢复） | 记录 `AgentEvent{Type:"reconnected"}`，任务继续 |

`checkAndStopIfAllLost` 的判定（用户需求 §3.2）：

```text
任务的某个 Assignment 视为"已失效"当：
  - 该 AgentID 已存在 Reports[agentID]（已合成 report 或自然上报）
  - 或 节点 status == Offline
  - 或 节点 CurrentTaskID != 当前 taskID（Agent 已不再认为自己跑这个任务）
全部 Assignment 都失效 → autoStopTask(taskID, "所有分配节点已失效")
```

### 2.4 Agent 进程重启（已分配节点重新注册）

用户需求 §5：运行任务期间允许新 Agent 注册，因为已分配 Agent 异常重启走重连流程，等价于"新注册"，不允许会导致行为不一致。

实现细节：

1. `handleAgentRegister` 不再拒绝活跃任务期间的注册。
2. `AgentRegistry.Register` 无条件覆盖旧 entry（旧状态无论 idle/busy/unhealthy/offline 都覆盖为新的 Idle 节点）。
3. 旧状态 `busy/unhealthy → idle` 触发 `onAgentStatusChange`，被识别为"已分配节点重启"：
   - 记录 `AgentEvent{Type:"restarted", Detail:"Agent 重新注册，已分配任务在该节点丢失"}`。
   - **立即为该 Agent 合成 `ResultFailed` report**，避免任务因等待"永远不会到来的"完成上报而卡死。
   - 调用 `checkAndStopIfAllLost`：若所有分配节点都已失效 → `autoStopTask`；否则任务继续。

不补档（用户需求 §2.3）：新注册后该 Agent 是空白节点，不会被自动塞回旧任务；旧任务在该节点上的 bots 量直接放弃。

### 2.5 自然完成路径（`handleAgentTaskDone`）

1. `s.agents.Touch(agentID, "")` 刷新心跳。
2. `s.agents.Heartbeat(...)` 把 Agent 标记回 Idle（**在 `tasks.Update` 之外调用**，避免 `agents.mu` 与 `tasks.mu` 的 AB-BA 死锁——见 §4.2）。
3. `tasks.Update` 写入 `Reports[agentID] = report`，检查是否全部到齐。
4. `len(Reports) == len(Assignments)` → 在 `Update` 外部调用 `Transition`：
   - `Running → Stopped`（自然完成）
   - `Stopping → Stopped`（手动停止后所有节点上报齐）

注意：自然完成路径不主动调用 `synthesizeOfflineReports`。如果某 Agent 在任务期间离线，该 Agent 的 report 会通过 §2.3 / §2.4 / §2.6 的某条路径补齐。

### 2.6 用户手动停止任务（`handleStopTask`）

1. `Running → Stopping`。
2. 向在线节点 POST `/agent/v1/stop`。
3. `synthesizeOfflineReports` 立即为已离线节点合成 `ResultStopped + "节点离线，未上报"`。
4. 若所有节点都已有 report → 立即 `Stopping → Stopped`。
5. 否则启动 30s 安全网 `startStopTimeout`：到时仍在 Stopping → 为剩余节点合成 `ResultStopped + "停止超时，节点未响应"` → 强制 `Stopping → Stopped`。

### 2.7 自动停止（`autoStopTask`）

触发场景：
- 所有分配节点失效（offline / restarted / report 已合成）— 由 `checkAndStopIfAllLost` 调用。
- Deadline 超时 — 由 `startDeadlineWatchdog`（5s/次）检测。

行为：`Running → Stopping → Failed`，为所有未上报的 Assignment 合成 `ResultFailed + reason` 的 report。

---

## 3. 任务生命周期与持久化

### 3.1 Admin 重启 → 活跃任务恢复

`NewTaskStore` 加载 `data/tasks/*.json` 时，发现状态为 starting/running/stopping 的任务：

1. 一律重置为 `TaskFailed`，`ErrorMsg = "admin restart, task lost"`。
2. `recoveredIDs` 记录这些任务 ID。
3. `SetOnTerminal` 注册回调后，对每个 recovered 任务触发 `onTaskTerminal` → 归档到 history。

Agent 端的对应路径见 §1.4：Admin 重启后给 Agent 的心跳/请求返回 404 → Agent 主动取消任务并重新注册。

### 3.2 持久化原子性

`saveTaskFile`（`admin/persist.go`）：

1. 写入 `<id>.json.tmp`。
2. `os.Rename` 到 `<id>.json`（大多数文件系统上是原子操作）。
3. rename 失败则删除临时文件。

`loadTaskFiles` 启动加载：
- 跳过非 `.json` 后缀；
- 跳过 `.tmp` 后缀（不加载半写文件）；
- 清理旧版残留的 `.tmp.json` 文件；
- 解析失败的文件跳过并打 Warn 日志。

### 3.3 终态归档异步化

`TaskStore.Transition` 检测到终态时：

1. 清理 `activeID`。
2. 深拷贝 task 数据，提交到 `utils.GetWorkPool().Go(...)` 异步执行 `onTerminal`（归档不阻塞状态机）。

---

## 4. 实现细节与陷阱

### 4.1 注册版本号 `regGeneration`

Agent 每次重新注册成功后 `regGeneration.Add(1)`。当前主要用于诊断，未来可扩展：
比如把 stressReporter 绑定到某个 generation，重新注册后 Admin 视角 task 已不存在，Agent 的旧 reporter 即便没来得及关闭也只会失败，不会污染新任务的指标。

### 4.2 锁顺序约定

涉及 `agents.mu` 与 `tasks.mu` 的调用路径必须遵守同一顺序，避免 AB-BA 死锁：

- `onAgentStatusChange`（从 `agents.Heartbeat` / `scanAndMarkStatus` 触发）：`agents.mu → tasks.mu`。
- `handleAgentTaskDone`：**必须把 `agents.Heartbeat` 放在 `tasks.Update` 之外**。当前实现已遵循。
- 其他 admin handler 中如新增涉及两者的逻辑，必须显式保证 `agents.mu` 先于 `tasks.mu`。

### 4.3 心跳间隔的硬下限

`agent/config.go::Resolve` 校验：`heartbeatInterval >= 25s` 直接拒绝启动。原因：默认 `unhealthyAfter=30s`，心跳间隔过长会让 Agent 始终处于 Unhealthy → Offline 摆动，触发不必要的 reconnected/offline 事件。

### 4.4 所有 `Transition` 错误都已检查

之前版本中 `handlers.go` 的若干 `s.tasks.Transition(...)` 忽略返回错误。当前实现已统一加 Warn 日志：

```text
[ADMIN] 状态转换失败 <from>→<to>  taskId=...  error=...
```

通常这类错误代表"状态机被另一条路径抢先转换"（如手动停止 + 自然完成竞争），属于良性竞态；记录日志即可，不需要回退。

### 4.5 协程池容量与 Shutdown

`Cap == 0`（默认）表示无限制，业务上极少触发 `MaxBlockingTasks`。
`Shutdown(timeout=5s)`：超时后打印泄漏 goroutine 列表（含 caller 文件:行号），便于排查"未正常退出的循环"。

---

## 5. 故障行为速查表

| 故障场景 | Agent 行为 | Admin 行为 | 任务最终状态 |
|---------|-----------|-----------|------------|
| Admin 挂掉，Agent Idle | 心跳失败递进重试，**不退进程** | — | 无任务 |
| Admin 挂掉，Agent Busy | 第 1 次心跳失败 → 取消任务 → 回 Idle → 持续重连 | — | 任务在 Agent 端被丢弃 |
| Admin 重启 | Agent 收到 404 → 取消任务（若有）→ 重新注册 → Idle | 加载旧 active 任务为 failed 并归档 | failed（旧）/ 等待新任务 |
| Admin 持续不可达 + 注册超出 maxRetries（默认 -1 不会触发）| 退出进程 | — | 任务被丢弃 |
| Agent 进程退出，无任务 | — | 60s 后标记 offline；24h 后清理 | 无影响 |
| Agent 进程退出，有任务 | — | 60s 后 offline，记录 AgentEvent；剩余节点继续 | 继续 running |
| Agent 进程重启，无任务 | 新注册 → Idle | 覆盖旧 entry | 无影响 |
| Agent 进程重启，有任务 | 新注册 → Idle | 记录 `restarted` 事件，合成该节点的 failed report；检查是否全失效 | 剩余节点继续 / 全失效则 failed |
| 所有 Agent 离线或重启 | 各自走重连 | `checkAndStopIfAllLost` → `autoStopTask` | failed |
| 任务运行中新 Agent 注册 | 注册成功 → Idle，等待分配 | 接受注册，未分配的节点保持 Idle | 不影响当前任务 |
| 用户手动停止任务 | 收到 `/agent/v1/stop` → 停 bots → 上报 | 离线节点合成 stopped report；30s 安全网 | stopped |
| 任务 deadline 超时 | 收到 `/agent/v1/stop` | `autoStopTask("任务超时")` | failed |
| Agent 心跳网络抖动 | 单次失败 → fail-interval 加速重试 → 恢复 | LastHeartbeatAt 被任意请求 Touch 刷新；通常无感 | 无影响 |
| HTTP handler panic | — | recover 中间件捕获，返回 500 JSON，写日志 + stack trace | 不影响其他请求 |
| 业务 goroutine panic | 协程池 PanicHandler 捕获 | 同左 | 该 goroutine 退出，其他不受影响 |
| 进程顶层 panic | `cmd/agent/main.go` 顶层 recover 写日志 + exit 2 | `cmd/admin/main.go` 顶层 recover 写日志 + exit 2 | 由进程管理器（systemd 等）拉起 |

---

## 6. 前端可观察项

| 数据来源 | 字段 | 渲染位置 |
|---------|------|---------|
| `GET /sbot/agents` | `status` (`idle`/`busy`/`unhealthy`/`offline`) | 节点列表、`MonitorDock` 在线计数 |
| `GET /sbot/tasks/{id}` | `agentEvents[]` | `RuntimeBar` "N 节点异常" 徽标 + `MonitorDock` Alert + `HistoryDetailView` 时间线 |
| `AgentEvent.type` | `offline` / `reconnected` / `restarted` / `deregistered` | 不同标签颜色与文案：离线 (error) / 恢复 (success) / 重启丢任务 (warning) / 注销 (default) |
| `StressAggregate` | `reportingAgents` / `totalAgents` / `offlineAgents` / `assignedAgents` | `RuntimeBar` 数据覆盖率提示 |
| Task `state == failed` + `errorMsg` | `errorMsg` | 最终报告 banner |

---

## 7. 配置示例（`conf/agent-config.json`）

```json
{
  "agent": {
    "enabled": true,
    "adminAddr": "http://admin.example:8080",
    "name": "",
    "listenAddr": ":7070",
    "maxBots": 5000,

    "stressInterval": "5s",
    "systemInterval": "5s",
    "heartbeatInterval": "10s",
    "heartbeatFailInterval": "5s",

    "requestTimeout": "30s",
    "reconnectInterval": "5s",
    "reconnectMaxInterval": "60s",
    "reconnectMaxRetries": -1,
    "taskReportTimeout": "30s",

    "taskWorkDir": ""
  }
}
```

任何字段空缺均使用默认值并在启动日志中打 Warn。
`reconnectMaxRetries: 0` 当成"未配置"处理（避免和"完全不重试"的歧义），仍然走默认 `-1`；要"完全不重试"请显式填 `1`。
