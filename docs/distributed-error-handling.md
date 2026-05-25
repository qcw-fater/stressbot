# 分布式异常处理机制

> 本文档描述 stressbot 在分布式（1 个 Admin + N 个 Agent）部署下的完整异常处理实现。
> 全部以代码为准，不写"应该怎样"，只写"实际怎样"。每处行为描述均标注源码文件与关键函数，便于对照阅读。

**设计前提**：Admin 是单点的指标聚合 / 任务调度中心，运维上保证其足够稳定；Agent 是水平扩展的执行节点，假设会随时上下线。因此整体策略偏向 **"Admin 不可达 → Agent 主动收缩；Agent 异常 → Admin 兜底容错"**。

---

## 目录

- [1. 全局约定](#1-全局约定)
  - [1.1 协程池与 panic 恢复](#11-协程池与-panic-恢复)
  - [1.2 顶层 recover 层级](#12-顶层-recover-层级)
  - [1.3 配置化的超时与重连参数](#13-配置化的超时与重连参数)
- [2. Admin 异常退出 → Agent 端处理](#2-admin-异常退出--agent-端处理)
  - [2.1 心跳循环核心逻辑](#21-心跳循环核心逻辑)
  - [2.2 Idle 状态 Admin 挂掉](#22-idle-状态-admin-挂掉)
  - [2.3 Busy 状态 Admin 挂掉](#23-busy-状态-admin-挂掉)
  - [2.4 Admin 重启 → 心跳收到 404](#24-admin-重启--心跳收到-404)
  - [2.5 注册重连策略](#25-注册重连策略)
  - [2.6 任务完成上报的"丢弃后上报"细节](#26-任务完成上报的丢弃后上报细节)
  - [2.7 Agent 优雅关闭](#27-agent-优雅关闭)
- [3. Agent 异常退出 → Admin 端处理](#3-agent-异常退出--admin-端处理)
  - [3.1 Admin 感知 Agent 状态的通道](#31-admin-感知-agent-状态的通道)
  - [3.2 心跳超时阈值与状态扫描](#32-心跳超时阈值与状态扫描)
  - [3.3 单节点离线 → 任务级处理](#33-单节点离线--任务级处理)
  - [3.4 Agent 进程重启（已分配节点重新注册）](#34-agent-进程重启已分配节点重新注册)
  - [3.5 自然完成路径](#35-自然完成路径)
  - [3.6 用户手动停止任务](#36-用户手动停止任务)
  - [3.7 自动停止（autoStopTask）](#37-自动停止autostoptask)
- [4. 任务生命周期与持久化](#4-任务生命周期与持久化)
  - [4.1 Admin 重启 → 活跃任务恢复](#41-admin-重启--活跃任务恢复)
  - [4.2 持久化原子性](#42-持久化原子性)
  - [4.3 终态归档异步化](#43-终态归档异步化)
- [5. 实现细节与陷阱](#5-实现细节与陷阱)
  - [5.1 注册版本号 regGeneration](#51-注册版本号-reggeneration)
  - [5.2 锁顺序约定](#52-锁顺序约定)
  - [5.3 所有 Transition 错误都已检查](#53-所有-transition-错误都已检查)
  - [5.4 协程池容量与 Shutdown](#54-协程池容量与-shutdown)
- [6. 任务分配失败回滚](#6-任务分配失败回滚)
- [7. 故障行为速查表](#7-故障行为速查表)
- [8. 前端可观察项](#8-前端可观察项)
- [9. 与计划的差异](#9-与计划的差异)

---

## 1. 全局约定

### 1.1 协程池与 panic 恢复

所有业务 goroutine 必须走 `utils.GetWorkPool()`（通过 `Go` / `GoWithStop` 方法提交）。代码位置：`utils/work_pool.go`。

**双层 panic 恢复机制**：

1. **ants 池级 PanicHandler**：在 `InitWorkPool` 中设置 `ants.Options.PanicHandler`，捕获 panic 后写 `DPanic` 日志（含 stack trace）。
2. **任务级 defer recover**：在 `submit` 方法中，每个提交到 ants 的闭包都有 `defer` 块调用 `recover()`，再次捕获并记录。

两层恢复确保即使一层遗漏，另一层也能兜底。DPanic+ 级别日志会触发企业微信 webhook 告警（如果配置了 `utils/log` 中的 webhook 地址）。

工程中通过规范约束 **禁止裸 `go func`**，所有 goroutine 必须通过协程池提交。协程池单例由 `utils.InitWorkPool` 创建（`sync.Once` 保护），Admin / Agent 进程关闭时调用 `Shutdown()` 等待最长 5s 让池中任务自然结束。

**协程池实现细节**（`utils/work_pool.go`）：

- 底层使用 `github.com/panjf2000/ants/v2` 协程池库。
- `WorkPool` 结构体包装了 ants.Pool，额外维护 `sync.WaitGroup`（wg）和 `sync.Map`（goroutines 追踪）。
- 每个 goroutine 在启动时分配唯一 ID（`goID` 原子递增），记录启动时间、caller 信息到 `sync.Map`。
- `Shutdown()` 关闭 `stopCh` 通道通知所有带停止通知的任务退出，然后 `wg.Wait()` 等待所有任务完成，超时后打印泄漏 goroutine 列表（含 caller 文件:行号 + 运行时长）。
- `Cap == 0`（默认）表示无限制，业务上极少触发 `MaxBlockingTasks`。

### 1.2 顶层 recover 层级

| 层级 | 文件 / 函数 | 行为 |
|------|------------|------|
| 进程级 | `cmd/agent/main.go`、`cmd/admin/main.go` | 顶层 `defer recover()`，捕获 panic 后写 stderr + 日志，`os.Exit(2)` |
| HTTP 层 | `admin/handlers.go::recoverMiddleware`、`agent/http_server.go::recoverMiddleware` | 包裹整个 mux，捕获 handler panic 后写日志 + 返回 500 JSON（避免连接重置） |
| 协程池层 | `utils/work_pool.go::PanicHandler` + 每个任务 defer | 捕获 goroutine panic 后写 `DPanic` 日志 |
| 业务任务级 | `agent/agent.go::executeTask`、`agent/agent.go::Run` 等关键函数 | 局部 `defer recover()`，确保单个任务崩溃不影响其他任务 |

**HTTP recover 中间件**的具体实现：

Admin 端（`admin/handlers.go::recoverMiddleware`）：
```go
func recoverMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rec := recover(); rec != nil {
                stresslog.Error("[ADMIN] HTTP handler panic",
                    zap.String("path", r.URL.Path),
                    zap.String("method", r.Method),
                    zap.Any("panic", rec),
                    zap.String("stack", string(debug.Stack())))
                writeError(w, ErrInternal.WithMessage("internal server error"))
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

Agent 端（`agent/http_server.go::recoverMiddleware`）：
```go
func recoverMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rec := recover(); rec != nil {
                stresslog.Error("[AGENT] HTTP handler panic",
                    zap.String("path", r.URL.Path),
                    zap.String("method", r.Method),
                    zap.Any("panic", rec),
                    zap.String("stack", string(debug.Stack())))
                writeJSONError(w, http.StatusInternalServerError, "internal server error")
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

两端的实现几乎相同，区别仅在于日志前缀 `[ADMIN]` / `[AGENT]` 和错误响应构造函数。不依赖 `net/http` 默认的 per-request recover（仅写 stderr 且会断开连接），而是主动捕获后返回结构化 500 JSON 错误。

### 1.3 配置化的超时与重连参数

Agent 与 Admin 通信的所有时间参数都可在 `agent-config.json` 配置；未配置或非法时使用默认值并打 Warn 日志。默认值解析由 `utils.ParseDurationDefault`（`utils/duration.go`）完成。

**Agent 配置项**（`agent/config.go::ResolvedConfig`）：

| 配置项 | 默认值 | 含义 |
|--------|--------|------|
| `HBInterval` | `10s` | 心跳成功时的下一次间隔 |
| `HBFailInterval` | 同 `HBInterval` | 心跳失败时的下一次重试间隔 |
| `RequestTimeout` | `30s` | Agent → Admin 单次 HTTP 请求超时 |
| `ReconnectInterval` | `5s` | 注册重试的初始退避间隔（硬编码） |
| `ReconnectMaxInterval` | `60s` | 注册重试的退避上限（硬编码） |
| `ReconnectMaxRetries` | `-1` | 注册重试最大次数。`-1` = 持续重连永不放弃；`0` 视为未配置 → 走默认 `-1` |
| `TaskReportTimeout` | `30s` | 任务完成上报的整体超时（硬编码） |
| `StressInterval` | `5s` | 压测指标上报周期 |
| `SystemInterval` | 同 `StressInterval` | 系统指标上报周期 |
| `Port` | `7719` | Agent 本地 HTTP 监听端口 |
| `MaxBots` | `5000` | 单节点最大机器人数量 |

**Admin 配置项**（`admin-config.json::agentRegistry`）：

| 配置项 | 默认值 | 含义 |
|--------|--------|------|
| `UnhealthyAfter` | `30s` | 超过此时间无心跳 → 标记 unhealthy |
| `OfflineAfter` | `60s` | 超过此时间无心跳 → 标记 offline 并从注册表删除 |

**resolveReconnectRetries 的特殊处理**（`agent/config.go::resolveReconnectRetries`）：
```go
func resolveReconnectRetries(v int) int {
    if v == 0 {
        return -1 // JSON 零值视为未配置，回退持续重连
    }
    return v
}
```

这个函数处理了 JSON 零值歧义问题：用户不填该字段时 JSON 反序列化结果为 0，如果不做处理会被当作"不重试"，与用户期望的"持续重连"不符。

---

## 2. Admin 异常退出 → Agent 端处理

> 用户需求：Admin 是核心、原则上不应该挂；一旦挂掉，Agent 行为统一为"丢弃当前任务 + 走重连 = 等价于新的注册"，不补档。

### 2.1 心跳循环核心逻辑

**代码位置**：`agent/agent.go::heartbeatLoop`

心跳循环是 Agent 与 Admin 保持联系的核心机制。每次心跳失败（含网络超时、连接拒绝、非 2xx 响应）的处理流程：

1. `consecutiveFailures` 递增。
2. 日志级别随次数递进：
   - Busy 状态首次失败：Error 级别（`"[AGENT] 任务运行中与 Admin 断联，立即取消当前任务"`）
   - 其他场景 `≤3` 次：Warn 级别
   - `>3` 次：Error 级别
3. 心跳间隔切换到 `HBFailInterval`（默认与 `HBInterval` 一致）。
4. 心跳恢复后 `consecutiveFailures` 归零并打 Info `"[AGENT] 心跳恢复"`。
5. **不**主动退出进程；只有"重新注册超出最大重试次数"才会退出。

心跳请求的构造包含以下字段（`agent/agent.go::heartbeatLoop`）：
```go
req := HeartbeatRequest{
    AgentID:       a.id,
    Timestamp:     time.Now().Format(time.RFC3339),
    Status:        string(status),      // "idle" 或 "busy"
    CurrentTaskID: taskID,               // 当前任务 ID（空闲时为空）
    CurrentBots:   bots,                 // 当前 bot 数量
    AppVersion:    a.cfg.AppVersion,     // 应用版本号
}
```

### 2.2 Idle 状态 Admin 挂掉

当 Agent 处于 Idle 状态（无活跃任务）且 Admin 不可达时：

- 心跳持续失败，日志按 `heartbeatLoop` 的递进规则输出。
- 没有任务可丢弃，Agent 保持存活。
- Admin 恢复后心跳自动恢复，`consecutiveFailures` 归零，Agent 回到正常工作状态。
- 不触发 `registerWithRetry`，仅在收到 404 时才触发重新注册。

### 2.3 Busy 状态 Admin 挂掉

**核心规则：第一次心跳失败立刻取消任务，丢弃，不补档。**

代码位置：`agent/agent.go::heartbeatLoop`

```go
// 用户需求 §2.2：运行任务时第一次心跳失败立刻取消任务
if status == StatusBusy && consecutiveFailures == 1 {
    stresslog.Error("[AGENT] 任务运行中与 Admin 断联，立即取消当前任务",
        zap.String("taskID", taskID),
        zap.Error(err))
    a.cancelCurrentTask("心跳失败 / Admin 断联")
}
```

为什么是第一次就取消，不是等几次：

- Admin 是唯一的指标聚合点。StressReporter 默认 5s 上报，Admin 挂了上报全部失败。
- 如果等 6 次 × 10s = 60s 再决定，这 60s 的压测流量完全无观测：前端看不到、历史不记录。
- 与其产生无意义的压测流量，不如立刻停止，等 Admin 恢复后再走"全新连接"重连流程。

**任务取消后的清理**（`agent/agent.go::executeTask` 的 defer 链）：

1. `taskCancel()` → `TaskRunner.Run` 收到 `<-ctx.Done()` → `Manager.StopAll()` 停所有机器人。
2. `stressReporter.Stop()` 停止指标上报（内部 `sync.Once` 保护，幂等调用安全）。
3. 任务结束的 `finalSnapshot` 仍会通过 `context.Background() + TaskReportTimeout` 尝试上报一次；Admin 不可达时 30s 后放弃。
4. `currentTask = nil`、`status = StatusIdle`、`runner = nil`、`taskCancel = nil`，Agent 回到 Idle 态。

`executeTask` 的 defer 链确保无论 `runner.Run` 如何退出（正常完成、context 取消、panic），清理逻辑都会执行：
```go
defer func() {
    a.mu.Lock()
    a.currentTask = nil
    a.taskCancel = nil
    a.status = StatusIdle
    a.mu.Unlock()
}()
```

心跳继续重试，直到 Admin 恢复（情况 A：网络恢复）或 Admin 重启被发现（情况 B：见 §2.4）。

### 2.4 Admin 重启 → 心跳收到 404

**代码位置**：`agent/http_client.go::Heartbeat`、`agent/agent.go::heartbeatLoop`

`AdminClient.Heartbeat` 检测到 `404` 返回特殊错误 `errNotRegistered`：

```go
// agent/http_client.go
var errNotRegistered = errors.New("agent not registered on admin")

func (c *AdminClient) Heartbeat(ctx context.Context, req HeartbeatRequest) error {
    // ...
    if resp.StatusCode == http.StatusNotFound {
        return errNotRegistered
    }
    // ...
}
```

Agent 处理流程（`agent/agent.go::heartbeatLoop`）：

1. 如果 Busy → 先 `cancelCurrentTask("Admin 报告未注册，可能重启")`（同 §2.3）。
2. 调用 `registerWithRetry(ctx)` 重新注册。
3. 注册成功 → `regGeneration.Add(1)`，继续心跳，Agent 保持 Idle，等待 Admin 重新分配任务。
4. 注册失败（超出 `reconnectMaxRetries`）→ `triggerStop()`，Agent 进程退出。

```go
if errors.Is(err, errNotRegistered) {
    if status == StatusBusy {
        a.cancelCurrentTask("Admin 报告未注册，可能重启")
    }
    stresslog.Warn("[AGENT] Admin 报告未注册，尝试重新注册")
    if regErr := a.registerWithRetry(ctx); regErr != nil {
        stresslog.Error("[AGENT] 重新注册失败，退出 Agent", zap.Error(regErr))
        a.triggerStop()
        return
    }
    stresslog.Info("[AGENT] 重新注册成功，继续心跳")
    a.regGeneration.Add(1)
    consecutiveFailures = 0
    interval = a.cfg.HBInterval
    timer.Reset(interval)
    continue
}
```

**用户需求满足证明**：
- 无任务运行：心跳失败走重试，收到 404 走重连。
- 有任务运行：取消任务（等价于丢弃）→ 转 Idle → 重连。
- 重连后 Admin 视为新 Idle 节点，不补档。

### 2.5 注册重连策略

**代码位置**：`agent/http_client.go::RetryWithRetriesAndBackoff`、`agent/agent.go::registerWithRetry`

`RetryWithRetriesAndBackoff` 的完整逻辑：

```go
func RetryWithRetriesAndBackoff(ctx context.Context, op func() error,
    initial, max time.Duration, maxRetries int, desc string) error {
    // 参数校验
    if initial <= 0 { initial = time.Second }
    if max <= 0 { max = 60 * time.Second }

    backoff := time.Duration(0)
    attempt := 0
    for {
        err := op()
        if err == nil { return nil }
        if ctx.Err() != nil { return ctx.Err() }

        // maxRetries < 0 无限重试；>= 0 检查已重试次数
        if maxRetries >= 0 && attempt >= maxRetries {
            return fmt.Errorf("%s: 已达最大重试次数 %d: %w", desc, maxRetries, err)
        }
        attempt++

        // 指数退避
        if backoff == 0 {
            backoff = initial
        } else {
            backoff *= 2
            if backoff > max { backoff = max }
        }

        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(backoff):
        }
    }
}
```

关键行为：
- 初始退避 `reconnectInterval`（5s），每次失败翻倍直到 `reconnectMaxInterval`（60s）。
- `reconnectMaxRetries < 0` 持续重连；`>= 0` 表示最多重试 N 次（总共 N+1 次尝试）。
- 任意 `ctx.Done()` 立刻退出。
- 默认配置（`-1`）下永不放弃，对应"Admin 理论不会挂"的部署前提。
- `maxRetries == 0` 被当作 1 次尝试（仅尝试一次，不重试），因为 `resolveReconnectRetries` 在 JSON 层已将 0 转为 -1。

### 2.6 任务完成上报的"丢弃后上报"细节

**代码位置**：`agent/agent.go::executeTask`

`executeTask` 在 `runner.Run(taskCtx)` 返回后会尝试上报 `TaskCompletionReport`：

```go
// 使用 context.Background() 脱离已 cancel 的 taskCtx
reportCtx, reportCancel := context.WithTimeout(
    context.Background(), a.cfg.TaskReportTimeout)
defer reportCancel()

report := TaskCompletionReport{
    AgentID:       a.id,
    TaskID:        task.TaskID,
    Result:        result,
    ErrorMsg:      errMsg,
    FinishedAt:    time.Now(),
    FinalSnapshot: finalSnap,
}

if err := a.httpCli.ReportTaskDone(reportCtx, report); err != nil {
    stresslog.Warn("[AGENT] 任务完成上报失败（任务已丢弃，由 Admin 心跳超时自动收尾）",
        zap.String("taskID", task.TaskID),
        zap.Error(err))
}
```

关键设计：
- 使用 `context.Background()` + `TaskReportTimeout`（默认 30s），脱离已被 cancel 的 taskCtx，让上报有机会在 Admin 恢复时发出去。
- **只尝试一次**，失败后打 Warn 日志即返回。
- 失败的语义：任务已丢弃，由 Admin 心跳超时安全网在 60s 后自动合成 offline report。

`runner.Cleanup()` 在上报完成后清理临时目录（任务工作目录在 `os.TempDir()` 下）。

### 2.7 Agent 优雅关闭

**代码位置**：`agent/agent.go::shutdown`

执行顺序（严格按编号，每步完成后才进入下一步）：

1. **停止当前任务**：`cancelCurrentTask("agent shutdown")` 触发任务取消。
2. **等待任务结束**：通过独立 goroutine + channel 等待 `taskWG.Wait()` 完成。最长等待 `TaskReportTimeout + 5s` 余量。
   - **不在协程池里 Wait**：`taskWG.Wait()` 在独立 goroutine 中执行，避免"全部协程都在等其他协程，但池容量已满 / 关闭"的死锁场景。
   ```go
   waitTaskDone := make(chan struct{})
   utils.GetWorkPool().Go(func() {
       a.taskWG.Wait()
       close(waitTaskDone)
   })
   select {
   case <-waitTaskDone:
   case <-time.After(a.cfg.TaskReportTimeout + 5*time.Second):
       stresslog.Warn("[AGENT] 等待任务退出超时，继续关闭流程")
   }
   ```
3. **停止上报循环**：`stressReporter.Stop()` / `sysReporter.Stop()`（幂等，`sync.Once` 保护）。
4. **取消全局 ctx**：`a.cancel()` → 心跳 / 轮询 / system 上报循环退出。
5. **注销**：`httpCli.Deregister(deregCtx)` best-effort 注销（5s 超时）。
6. **关闭 HTTP 服务器**：`shutdownHTTPServer(context.Background())`，5s 超时。
7. **关闭协程池**：`utils.GetWorkPool().Shutdown()` 等待协程池任务完成。

`triggerStop()` 使用 `sync.Once` 保护，确保 `stopCh` 只关闭一次，可安全并发调用。

---

## 3. Agent 异常退出 → Admin 端处理

> 用户需求：单节点离线不停整个任务（除非全离线）；要在测试监控 / 测试结果中体现。

### 3.1 Admin 感知 Agent 状态的通道

| 通道 | 触发点 | 代码位置 | 作用 |
|------|--------|---------|------|
| 心跳超时扫描 | `AgentRegistry.scanAndMarkStatus`（5s/次） | `admin/agent.go::StartHealthChecker` | 主动检测，标记 unhealthy / offline |
| 任意请求 Touch | `AgentRegistry.Touch` | `admin/handlers.go` 中多个 handler | 把任意请求当 keepalive |

**Touch 与 Heartbeat 的差别**：

`Touch`（`admin/agent.go::Touch`）：
- 只更新 `LastHeartbeatAt` 和 `AppVersion`。
- 不动 `CurrentTaskID` / `CurrentBots`（那些是心跳的语义性字段，必须由心跳路径更新）。
- 触发 unhealthy/offline → 在线 的恢复。

`Heartbeat`（`admin/agent.go::Heartbeat`）：
- 更新 `LastHeartbeatAt`、`AppVersion`、`CurrentTaskID`、`CurrentBots`。
- 当 `CurrentTaskID` 发生变化时，清除 `LatestStress` 和 `StressUpdatedAt`，避免旧任务的指标串到新任务。
- 同样触发 unhealthy/offline → 在线 的恢复。

**实现选择"任意请求都更新 LastHeartbeatAt"**的理由：

- Agent 默认 5s 一次 stress 上报、5s 一次 system 上报、10s 一次心跳，三类请求频次差不多。
- 任一通道存活就说明 Agent 在线，标记 offline 应该以"全部通道都失败"为准。
- 实现成本只是几个 handler 各加一行 `s.agents.Touch(agentID, "")`。

哪些 handler 调用了 Touch：
- `handleAgentStressReport`：`s.agents.Touch(report.AgentID, "")`
- `handleAgentSystemReport`：`s.agents.Touch(report.AgentID, "")`
- `handleAgentTaskDone`：`s.agents.Touch(agentID, "")`
- `handleAgentPendingTask`：`s.agents.Touch(agentID, "")`

### 3.2 心跳超时阈值与状态扫描

**代码位置**：`admin/agent.go::scanAndMarkStatus`

`scanAndMarkStatus` 由 `StartHealthChecker` 以 5s 间隔调用：

```go
func (r *AgentRegistry) scanAndMarkStatus() {
    var changes []statusChange
    r.mu.Lock()
    now := time.Now()
    for _, node := range r.agents {
        lag := now.Sub(node.LastHeartbeatAt)
        var newStatus AgentStatus
        switch {
        case lag >= r.offlineThreshold:
            newStatus = AgentOffline
        case lag >= r.unhealthyThreshold:
            newStatus = AgentUnhealthy
        default:
            continue
        }
        if node.Status != newStatus {
            from := node.Status
            node.Status = newStatus
            changes = append(changes, statusChange{...})
        }
        // offline → 从注册表删除
        if node.Status == AgentOffline {
            delete(r.agents, node.ID)
        }
    }
    r.mu.Unlock()
    r.fireOnChange(changes) // 锁外触发回调
}
```

**心跳超时阈值**（在 `admin-config.json::agentRegistry` 中配置）：

| 距上次活动 | 状态变更 | 含义 |
|-----------|---------|------|
| `< unhealthyAfter`（默认 30s） | 保持业务状态 | 健康 |
| `>= unhealthyAfter` | → `Unhealthy` | 告警，但仍视为在线 |
| `>= offlineAfter`（默认 60s） | → `Offline` | 视为离线，从注册表删除 |

**与计划的差异**：计划中提到 `purgeAfter`（24h 后清理无任务的 offline 节点），但实际代码中 offline 节点在 `scanAndMarkStatus` 中立即从注册表删除（`delete(r.agents, node.ID)`），没有 24h 延迟清理。这意味着 offline 状态是瞬态的——标记后立即删除。

**`fireOnChange` 的设计**：状态变更回调收集到 `changes` 切片中，在 `agents.mu` 锁释放后才触发。这防止了 `onChange` 回调（可能获取 `tasks.mu`）与 `agents.mu` 的死锁。

### 3.3 单节点离线 → 任务级处理

**代码位置**：`admin/admin.go::onAgentStatusChange`

`onAgentStatusChange` 是 Admin 处理 Agent 状态变更的核心回调，注册在 `AgentRegistry` 中。

处理逻辑按场景分支：

**场景 A：活跃任务中，已分配节点离线**（`to == AgentOffline`）：

1. 检查当前是否有活跃任务（`s.tasks.ActiveTask()`）。
2. 检查离线 Agent 是否是活跃任务的分配节点。
3. 记录 `AgentEvent{Type:"offline", Detail:"心跳超时"}`。

然后根据任务状态进一步处理：

- **任务 Stopping 时节点离线**：立刻合成 `ResultFailed + "节点离线"` 的 report；若所有节点都已有 report → 转 `Stopped`。
  ```go
  if task.State == TaskStopping {
      // 合成 report
      if len(task.Reports) == len(task.Assignments) {
          s.tasks.Transition(task.ID, TaskStopping, TaskStopped)
      }
      return
  }
  ```

- **任务 Running 时节点离线**：调用 `checkAndStopIfAllLost` 检查是否所有分配节点都已失效。

**场景 B：已分配节点重新注册**（`busy/unhealthy → idle`）：

```go
if (from == AgentBusy || from == AgentUnhealthy) && to == AgentIdle {
    // 记录 restarted 事件
    // 立即合成 ResultFailed report
    // checkAndStopIfAllLost
}
```

**场景 C：节点恢复**（`offline → idle/busy`）：
```go
if from == AgentOffline && (to == AgentIdle || to == AgentBusy) {
    // 记录 reconnected 事件
}
```

**`checkAndStopIfAllLost` 的判定**（`admin/admin.go::checkAndStopIfAllLost`）：

```text
任务的某个 Assignment 视为"已失效"当：
  - 该 AgentID 已存在 Reports[agentID]（已合成 report 或自然上报）
  - 或 节点已从注册表删除（Get 返回 false）
  - 或 节点 status == Offline
  - 或 节点 CurrentTaskID != 当前 taskID（Agent 已不再认为自己跑这个任务）
全部 Assignment 都失效 → autoStopTask(taskID, "所有分配节点已失效")
```

代码实现：
```go
func (s *AdminServer) checkAndStopIfAllLost(taskID string) {
    task, ok := s.tasks.Get(taskID)
    if !ok || !IsActiveState(task.State) { return }
    anyAlive := false
    for _, a := range task.Assignments {
        if _, hasReport := task.Reports[a.AgentID]; hasReport { continue }
        node, nodeOk := s.agents.Get(a.AgentID)
        if nodeOk && node.Status != AgentOffline && node.CurrentTaskID == taskID {
            anyAlive = true
            break
        }
    }
    if !anyAlive {
        s.autoStopTask(taskID, "所有分配节点已失效")
    }
}
```

### 3.4 Agent 进程重启（已分配节点重新注册）

**代码位置**：`admin/agent.go::Register`、`admin/handlers.go::handleAgentRegister`、`admin/admin.go::onAgentStatusChange`

用户需求：运行任务期间允许新 Agent 注册，因为已分配 Agent 异常重启走重连流程，等价于"新注册"，不允许会导致行为不一致。

实现细节：

1. `handleAgentRegister`（`admin/handlers.go`）不拒绝活跃任务期间的注册：
   ```go
   func (s *AdminServer) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
       // 解析请求，构造 AgentNode（Status: AgentIdle）
       if err := s.agents.Register(node); err != nil {
           writeError(w, err)
           return
       }
       writeJSON(w, http.StatusOK, RegisterResponse{...})
   }
   ```

2. `AgentRegistry.Register`（`admin/agent.go`）无条件覆盖旧 entry：
   ```go
   func (r *AgentRegistry) Register(node *AgentNode) error {
       r.mu.Lock()
       existing, exists := r.agents[node.ID]
       from := AgentStatus("")
       if exists { from = existing.Status }
       r.agents[node.ID] = node // 无条件覆盖
       needCallback := exists && r.onChange != nil && from != node.Status
       r.mu.Unlock()
       if needCallback { r.onChange(node.ID, from, node.Status) }
       return nil
   }
   ```

3. 旧状态 `busy/unhealthy → idle` 触发 `onAgentStatusChange`，被识别为"已分配节点重启"：
   - 记录 `AgentEvent{Type:"restarted", Detail:"Agent 重新注册，已分配任务在该节点丢失"}`。
   - **立即为该 Agent 合成 `ResultFailed` report**，避免任务因等待"永远不会到来的"完成上报而卡死。
   - 调用 `checkAndStopIfAllLost`：若所有分配节点都已失效 → `autoStopTask`；否则任务继续。

**不补档**：新注册后该 Agent 是空白节点，不会被自动塞回旧任务；旧任务在该节点上的 bots 量直接放弃。

### 3.5 自然完成路径

**代码位置**：`admin/handlers.go::handleAgentTaskDone`

```
1. s.agents.Touch(agentID, "")         ← 刷新心跳
2. s.agents.Heartbeat(agentID, ...)    ← 把 Agent 标记回 Idle
3. s.tasks.Update(taskID, ...)         ← 写入 Reports[agentID] = report
4. 检查 len(Reports) == len(Assignments)
   → Running: Transition(taskID, TaskRunning, TaskStopped)
   → Stopping: Transition(taskID, TaskStopping, TaskStopped)
```

**锁顺序关键**：`agents.Heartbeat` 必须在 `tasks.Update` **之外**调用，避免 `agents.mu` 与 `tasks.mu` 的 AB-BA 死锁（见 §5.2）。

```go
func (s *AdminServer) handleAgentTaskDone(w http.ResponseWriter, r *http.Request) {
    // 1. Touch 刷新心跳
    s.agents.Touch(agentID, "")
    // 2. Heartbeat 标记回 idle（在 tasks.Update 之外！）
    s.agents.Heartbeat(agentID, HeartbeatRequest{Status: "idle"})
    // 3. Update 写入 report
    var needTransition TaskState
    err := s.tasks.Update(taskID, func(t *Task) {
        // 阶段完成报告：存入 StageReports
        if report.StageIndex > 0 {
            t.StageReports = append(t.StageReports, report)
            return
        }
        // 最终完成报告
        t.Reports[agentID] = report
        if len(t.Reports) == len(t.Assignments) {
            if t.State == TaskRunning { needTransition = TaskRunning }
            else if t.State == TaskStopping { needTransition = TaskStopping }
        }
    })
    // 4. Transition 在 Update 外部调用
    if needTransition == TaskRunning {
        s.tasks.Transition(taskID, TaskRunning, TaskStopped)
    } else if needTransition == TaskStopping {
        s.tasks.Transition(taskID, TaskStopping, TaskStopped)
    }
}
```

**阶段完成报告**（`StageIndex > 0`）：渐进式加压模式下，每个阶段完成时 Agent 会发送阶段报告，存入 `StageReports` 而非 `Reports`，不触发状态转换。

**自然完成路径不主动调用 `synthesizeOfflineReports`**。如果某 Agent 在任务期间离线，该 Agent 的 report 会通过 §3.3 / §3.4 / §3.6 的某条路径补齐。

### 3.6 用户手动停止任务

**代码位置**：`admin/handlers.go::handleStopTask`

1. 校验任务状态必须为 `TaskRunning`。
2. `Running → Stopping`（`Transition`）。
3. 向在线节点 POST `/agent/v1/stop`（通过 `dispatcher.Stop`）。
4. `synthesizeOfflineReports` 立即为已离线节点合成 `ResultStopped + "节点离线，未上报"`。
5. 若所有节点都已有 report → 立即 `Stopping → Stopped`。
6. 否则启动 30s 安全网 `startStopTimeout`。

```go
func (s *AdminServer) handleStopTask(w http.ResponseWriter, r *http.Request) {
    // 校验 + Transition Running→Stopping
    // 向在线节点发送 stop 命令
    allReported := s.synthesizeOfflineReports(id)
    if allReported {
        s.tasks.Transition(id, TaskStopping, TaskStopped)
    } else {
        s.startStopTimeout(id) // 30s 安全网
    }
}
```

**`startStopTimeout` 安全网**（`admin/admin.go::startStopTimeout`）：

```go
func (s *AdminServer) startStopTimeout(taskID string) {
    utils.GetWorkPool().Go(func() {
        time.Sleep(30 * time.Second)
        task, ok := s.tasks.Get(taskID)
        if !ok || task.State != TaskStopping { return }
        // 为所有未上报的节点合成 ResultStopped report
        s.tasks.Update(taskID, func(t *Task) {
            for _, a := range t.Assignments {
                if _, exists := t.Reports[a.AgentID]; !exists {
                    t.Reports[a.AgentID] = TaskCompletionReport{
                        Result: ResultStopped,
                        ErrorMsg: "停止超时，节点未响应",
                    }
                }
            }
        })
        s.tasks.Transition(taskID, TaskStopping, TaskStopped)
    })
}
```

### 3.7 自动停止（autoStopTask）

**代码位置**：`admin/admin.go::autoStopTask`

触发场景：
- 所有分配节点失效（offline / restarted / report 已合成）— 由 `checkAndStopIfAllLost` 调用。
- Deadline 超时 — 由 `startDeadlineWatchdog`（5s/次）检测。

行为：`Running/Starting → Stopping → Failed`，为所有未上报的 Assignment 合成 `ResultFailed + reason` 的 report。

```go
func (s *AdminServer) autoStopTask(taskID string, reason string) {
    task, ok := s.tasks.Get(taskID)
    if !ok || !IsActiveState(task.State) { return }

    // Starting 阶段直接转 Failed（还没发到 Agent，不需要 stop RPC）
    if task.State == TaskStarting {
        s.tasks.Transition(taskID, TaskStarting, TaskFailed)
        return
    }

    // Running → Stopping
    if task.State == TaskRunning {
        s.tasks.Transition(taskID, TaskRunning, TaskStopping)
    }

    // 向在线节点发送 stop 命令
    for _, a := range task.Assignments {
        node, ok := s.agents.Get(a.AgentID)
        if ok && node.Status != AgentOffline {
            s.dispatcher.Stop(node.Address, taskID)
        }
    }

    // 为所有未上报的节点合成 ResultFailed report
    s.tasks.Update(taskID, func(t *Task) {
        for _, a := range t.Assignments {
            if _, ok := t.Reports[a.AgentID]; !ok {
                t.Reports[a.AgentID] = TaskCompletionReport{
                    Result: ResultFailed,
                    ErrorMsg: reason,
                }
            }
        }
    })

    // Stopping → Failed
    s.tasks.Transition(taskID, TaskStopping, TaskFailed)
}
```

**Deadline 看门狗**（`admin/admin.go::startDeadlineWatchdog`）：

每 5 秒检查活跃任务的 `Config.Deadline`，超时则调用 `autoStopTask("任务超时")`。

---

## 4. 任务生命周期与持久化

### 4.1 Admin 重启 → 活跃任务恢复

**代码位置**：`admin/task.go::NewTaskStore`

`NewTaskStore` 加载 `data/tasks/*.json` 时，发现状态为 `starting/running/stopping` 的任务：

1. 一律重置为 `TaskFailed`，`ErrorMsg = "admin restart, task lost"`。
2. `recoveredIDs` 记录这些任务 ID。
3. `SetOnTerminal` 注册回调后，对每个 recovered 任务触发 `onTaskTerminal` → 归档到 history。

```go
func NewTaskStore(dataDir string) (*TaskStore, error) {
    tasks, err := loadTaskFiles(dataDir)
    // ...
    var recoveredIDs []string
    for _, t := range tasks {
        if IsActiveState(t.State) {
            t.State = TaskFailed
            t.ErrorMsg = "admin restart, task lost"
            t.StoppedAt = &now
            recoveredIDs = append(recoveredIDs, t.ID)
        }
        ts.tasks[t.ID] = t
    }
    ts.recoveredIDs = recoveredIDs
    return ts, nil
}
```

Agent 端的对应路径见 §2.4：Admin 重启后给 Agent 的心跳返回 404 → Agent 主动取消任务并重新注册。

### 4.2 持久化原子性

**代码位置**：`admin/persist.go::saveTaskFile`

```go
func saveTaskFile(dataDir string, task *Task) error {
    dir := filepath.Join(dataDir, "tasks")
    os.MkdirAll(dir, 0o755)
    data, _ := json.MarshalIndent(task, "", "  ")

    // 原子写入：先写临时文件再 rename
    tmp := taskFilePath(dataDir, task.ID) + ".tmp"
    os.WriteFile(tmp, data, 0o644)
    dst := taskFilePath(dataDir, task.ID)
    if err := os.Rename(tmp, dst); err != nil {
        os.Remove(tmp) // rename 失败则删除临时文件
        return err
    }
    return nil
}
```

**`loadTaskFiles` 启动加载**（`admin/persist.go::loadTaskFiles`）：
- 跳过非 `.json` 后缀的文件。
- 跳过 `.tmp` 后缀（不加载半写文件）。
- 清理旧版残留的 `.tmp.json` 文件。
- 解析失败的文件跳过并打 Warn 日志。

### 4.3 终态归档异步化

**代码位置**：`admin/task.go::Transition`

`TaskStore.Transition` 检测到终态时：

1. 清理 `activeID`。
2. 深拷贝 task 数据（JSON marshal/unmarshal），提交到 `utils.GetWorkPool().Go(...)` 异步执行 `onTerminal`（归档不阻塞状态机）。

```go
if !IsActiveState(to) {
    if ts.activeID == id { ts.activeID = "" }
    if ts.onTerminal != nil {
        var taskCopy Task
        if data, err := json.Marshal(t); err == nil {
            json.Unmarshal(data, &taskCopy)
        }
        taskRef := &taskCopy
        utils.GetWorkPool().Go(func() { ts.onTerminal(taskRef) })
    }
}
```

深拷贝避免了异步归档 goroutine 与后续任务操作之间的 data race。

**`onTaskTerminal` 的行为**（`admin/admin.go::onTaskTerminal`）：
1. 停止 Sampler（如果存在）。
2. 异步归档到 HistoryStore（如果 history 模块启用）。
3. 归档时优先从 agent 终止报告聚合最终快照，兜底从心跳聚合。

---

## 5. 实现细节与陷阱

### 5.1 注册版本号 regGeneration

**代码位置**：`agent/agent.go::Agent.regGeneration`

Agent 每次重新注册成功后 `regGeneration.Add(1)`。

```go
type Agent struct {
    // ...
    regGeneration atomic.Int64
}
```

当前主要用于诊断，未来可扩展：比如把 `stressReporter` 绑定到某个 generation，重新注册后 Admin 视角 task 已不存在，Agent 的旧 reporter 即便没来得及关闭也只会失败，不会污染新任务的指标。

`regGeneration` 在以下时机递增：
- 初始注册成功（`Run()` 中）：`a.regGeneration.Add(1)`
- Admin 重启后重新注册成功（`heartbeatLoop` 中）：`a.regGeneration.Add(1)`

### 5.2 锁顺序约定

涉及 `agents.mu` 与 `tasks.mu` 的调用路径必须遵守同一顺序，避免 AB-BA 死锁。

**全局锁顺序**：`agents.mu` → `tasks.mu`。`agents.mu` 必须在 `tasks.mu` 之前获取。

具体实现：

- `onAgentStatusChange`（从 `agents.Heartbeat` / `scanAndMarkStatus` 触发）：先释放 `agents.mu`（`fireOnChange` 在锁外调用），然后获取 `tasks.mu`。
  - `fireOnChange` 的设计就是收集所有状态变更，释放锁后再逐个触发回调。

- `handleAgentTaskDone`：**必须把 `agents.Heartbeat` 放在 `tasks.Update` 之外**。
  ```go
  s.agents.Touch(agentID, "")       // agents.mu 获取/释放
  s.agents.Heartbeat(agentID, ...)  // agents.mu 获取/释放
  s.tasks.Update(taskID, ...)       // tasks.mu 获取/释放
  ```
  当前实现已遵循。

- `handleStopTask`：先 `Transition`（`tasks.mu`），再 `synthesizeOfflineReports`（内部有 `tasks.Update`），最后 `startStopTimeout`。不涉及 `agents.mu` 的同时持有。

- 其他 admin handler 中如新增涉及两者的逻辑，必须显式保证 `agents.mu` 先于 `tasks.mu`，或确保两者不在同一调用链中同时持有。

### 5.3 所有 Transition 错误都已检查

所有 `s.tasks.Transition(...)` 调用的返回错误都已处理：

```text
[ADMIN] 状态转换失败 <from>→<to>  taskId=...  error=...
```

通常这类错误代表"状态机被另一条路径抢先转换"（如手动停止 + 自然完成竞争），属于良性竞态；记录日志即可，不需要回退。

涉及的 Transition 调用点（`admin/admin.go`、`admin/handlers.go`）：
- `onAgentStatusChange` 中 Stopping → Stopped
- `startStopTimeout` 中 Stopping → Stopped
- `autoStopTask` 中 Starting → Failed、Running → Stopping、Stopping → Failed
- `handleStartTask` 中 Starting → Failed、Starting → Running
- `handleStopTask` 中 Running → Stopping、Stopping → Stopped
- `startTaskBackground` 中 Starting → Failed、Starting → Running

### 5.4 协程池容量与 Shutdown

**代码位置**：`utils/work_pool.go`

- `Cap == 0`（默认）表示无限制（ants 库语义），业务上极少触发 `MaxBlockingTasks`。
- `Shutdown(timeout=5s)`：超时后打印泄漏 goroutine 列表（含 caller 文件:行号 + 运行时长），便于排查"未正常退出的循环"。
- `Shutdown` 使用 `sync.Once` 语义（`stopped.CompareAndSwap(false, true)`），多次调用安全。
- `Shutdown` 中 `wg.Wait()` 在独立 goroutine 中执行（不在池内），避免死锁。

---

## 6. 任务分配失败回滚

**代码位置**：`admin/handlers.go::startTaskBackground`

`startTaskBackground` 是任务分配的异步执行函数，在 `handleStartTask` 中通过协程池提交：

1. 读取任务配置，构建 `TaskAssignment`（包含 ConfigURL、ConfigFiles、RampUp 等）。
2. 向各 Agent 异步推送任务（`dispatcher.AssignTask`）。
3. 推送成功的 Agent 立即通过 `agents.Heartbeat` 标记为 busy。
4. 如果任一 Agent 分配失败：
   - 向已接受任务的 Agent 发送 stop 命令回收资源。
   - `Transition(taskID, TaskStarting, TaskFailed)` 回滚任务状态。
   - 停止 Sampler。

```go
func (s *AdminServer) startTaskBackground(taskID, taskName string, assignments []Assignment) {
    var failed []string
    var succeeded []string
    // ...
    for _, a := range assignments {
        if err := s.dispatcher.AssignTask(agent.Address, cfg); err != nil {
            failed = append(failed, a.AgentID)
        } else {
            succeeded = append(succeeded, a.AgentID)
            s.agents.Heartbeat(a.AgentID, HeartbeatRequest{Status: "busy", ...})
        }
    }
    if len(failed) > 0 {
        // 回滚已成功的 Agent
        for _, agentID := range succeeded {
            s.dispatcher.Stop(agent.Address, taskID)
        }
        s.tasks.Transition(taskID, TaskStarting, TaskFailed)
        s.sampler.Stop(taskID)
        return
    }
    // 全部成功
    s.tasks.Transition(taskID, TaskStarting, TaskRunning)
}
```

**RampUp 缩放**：分布式模式下每个 Agent 分到的 bot 数不同，`scaleRampUp` 按比例缩放各 stage 的 count，最后一个 stage 取剩余数确保总数精确。

---

## 7. 故障行为速查表

| 故障场景 | Agent 行为 | Admin 行为 | 任务最终状态 |
|---------|-----------|-----------|------------|
| Admin 挂掉，Agent Idle | 心跳失败递进重试，**不退进程** | — | 无任务 |
| Admin 挂掉，Agent Busy | 第 1 次心跳失败 → 取消任务 → 回 Idle → 持续重连 | — | 任务在 Agent 端被丢弃 |
| Admin 重启 | Agent 收到 404 → 取消任务（若有）→ 重新注册 → Idle | 加载旧 active 任务为 failed 并归档 | failed（旧）/ 等待新任务 |
| Admin 持续不可达 + 注册超出 maxRetries（默认 -1 不会触发）| 退出进程 | — | 任务被丢弃 |
| Agent 进程退出，无任务 | — | 60s 后标记 offline 并从注册表删除 | 无影响 |
| Agent 进程退出，有任务 | — | 60s 后 offline，记录 AgentEvent；剩余节点继续 | 继续 running |
| Agent 进程重启，无任务 | 新注册 → Idle | 覆盖旧 entry | 无影响 |
| Agent 进程重启，有任务 | 新注册 → Idle | 记录 `restarted` 事件，合成该节点的 failed report；检查是否全失效 | 剩余节点继续 / 全失效则 failed |
| 所有 Agent 离线或重启 | 各自走重连 | `checkAndStopIfAllLost` → `autoStopTask` | failed |
| 任务运行中新 Agent 注册 | 注册成功 → Idle，等待分配 | 接受注册，未分配的节点保持 Idle | 不影响当前任务 |
| 用户手动停止任务 | 收到 `/agent/v1/stop` → 停 bots → 上报 | 离线节点合成 stopped report；30s 安全网 | stopped |
| 任务 deadline 超时 | 收到 `/agent/v1/stop` | `autoStopTask("任务超时")` | failed |
| Agent 心跳网络抖动 | 单次失败 → fail-interval 重试 → 恢复 | LastHeartbeatAt 被任意请求 Touch 刷新；通常无感 | 无影响 |
| HTTP handler panic | — | recover 中间件捕获，返回 500 JSON，写日志 + stack trace | 不影响其他请求 |
| 业务 goroutine panic | 协程池双层 PanicHandler 捕获 | 同左 | 该 goroutine 退出，其他不受影响 |
| 进程顶层 panic | `cmd/agent/main.go` 顶层 recover 写日志 + exit 2 | `cmd/admin/main.go` 顶层 recover 写日志 + exit 2 | 由进程管理器拉起 |

---

## 8. 前端可观察项

| 数据来源 | 字段 | 渲染位置 |
|---------|------|---------|
| `GET /sbot/agents` | `status` (`idle`/`busy`/`unhealthy`/`offline`) | 节点列表、MonitorDock 在线计数 |
| `GET /sbot/tasks/{id}` | `agentEvents[]` | RuntimeBar "N 节点异常" 徽标 + MonitorDock Alert + HistoryDetailView 时间线 |
| `AgentEvent.type` | `offline` / `reconnected` / `restarted` / `deregistered` | 不同标签颜色与文案：离线 (error) / 恢复 (success) / 重启丢任务 (warning) / 注销 (default) |
| `StressAggregate` | `reportingAgents` / `totalAgents` / `offlineAgents` / `assignedAgents` | RuntimeBar 数据覆盖率提示 |
| Task `state == failed` + `errorMsg` | `errorMsg` | 最终报告 banner |

---

## 9. 与计划的差异

本节列出实际实现与 `plans/error-handling.md` 之间的差异。

### 9.1 配置参数简化

**计划中提到但实际未实现的配置项**：
- `heartbeatFailInterval`：计划中描述为独立配置项且默认与 `heartbeatInterval` 不同（更短以加速重连），实际实现中 `HBFailInterval` 硬编码等于 `HBInterval`（`agent/config.go::Resolve` 中 `HBFailInterval: hbInterval`）。
- `reconnectInterval` / `reconnectMaxInterval`：计划中描述为可配置，实际硬编码为 5s / 60s（`agent/config.go::Resolve`）。
- `taskReportTimeout`：计划中描述为可配置，实际硬编码为 30s。
- `heartbeatInterval >= 25s` 的启动校验：计划中提到会拒绝启动，实际代码中未发现此校验逻辑。

**计划中提到但实际未实现的兼容性字段**：
- `maxHeartbeatFailures` / `taskRunAdminLostExit` / `reconnectEnabled` / `registerRetryMaxInterval` 等旧字段兼容：实际代码中未找到相关反序列化逻辑。

### 9.2 Agent 状态清理策略

**计划中**：`Offline` 节点超过 `purgeAfter`（24h）且 `CurrentTaskID == ""` 时从注册表删除。

**实际实现**：`scanAndMarkStatus`（`admin/agent.go`）在标记为 Offline 后立即从注册表删除（`delete(r.agents, node.ID)`），无 24h 延迟清理。`purgeAfter` 配置项不存在。

### 9.3 Deregister 回调

**计划中**：`Deregister` 触发 `onAgentStatusChange`，会为关联任务的 Agent 合成 report。

**实际实现**：`Deregister`（`admin/agent.go`）在删除注册表条目时，仅当 `node.CurrentTaskID != ""` 时才触发 `onChange` 回调（模拟 `→ AgentOffline` 的变更），让 `onAgentStatusChange` 处理任务侧的合成 report。

### 9.4 stress 报告的过期检测

**实际额外实现**（计划中未提及）：`handleAgentStressReport`（`admin/handlers.go`）检测 stress 报告是否属于当前任务，过期的报告直接丢弃（返回 `{"status":"stale"}`），避免旧任务数据串入 `LatestStress`。

### 9.5 阶段完成报告

**实际额外实现**（计划中未提及）：`handleAgentTaskDone` 支持 `StageIndex > 0` 的阶段完成报告，存入 `StageReports` 而非 `Reports`，用于渐进式加压模式的阶段汇报。计划中未描述此机制。

### 9.6 任务分配失败时的 Sampler 处理

**实际实现**：`startTaskBackground` 中分配失败时调用 `s.sampler.Stop(taskID)` 停止采样器。计划中未明确描述此步骤。

### 9.7 regGeneration 的字段类型

**计划中**：描述为 `atomic.Uint64`。

**实际实现**：`agent/agent.go` 中 `regGeneration` 类型为 `atomic.Int64`。

---

## 附录：任务状态机

```
pending ──→ starting ──→ running ──→ stopping ──→ stopped
  │            │            │                         ↑
  │            │            └──────→ failed           │
  │            │                                     │
  │            └──→ failed                           │
  │                                                  │
  └──→ stopped（计划未提及，validTransition 允许）     │
                                                     │
                              stopping ──→ failed    │
                                                     │
                              running ──→ stopped（自然完成）
```

状态转换合法性校验（`admin/task.go::validTransition`）：

| from | to |
|------|----|
| `TaskPending` | `TaskStarting` / `TaskFailed` / `TaskStopped` |
| `TaskStarting` | `TaskRunning` / `TaskFailed` |
| `TaskRunning` | `TaskStopping` / `TaskStopped` / `TaskFailed` |
| `TaskStopping` | `TaskStopped` / `TaskFailed` |

终态：`TaskStopped`、`TaskFailed`。
活跃态（`IsActiveState`）：`TaskStarting`、`TaskRunning`、`TaskStopping`。
