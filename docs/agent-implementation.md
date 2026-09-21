# Agent 节点技术文档

> **角色定位**：Agent 是分布式压测系统的**执行节点**，主动建立并维持与 Admin 控制面（gRPC）的长连接，接收命令、下载资源包、驱动机器人压测、采集并上报压测/系统指标。
> **本文档目标读者**：负责 `agent/` 包及 `cmd/agent/` 入口的开发者。
> **前置阅读**：`docs/admin-implementation.md`（Admin 端协议契约可双向交叉验证）；wire 契约 `controlplane/proto/control.proto`。
> **文档性质**：基于当前代码编写（2026-09 修订），标注 `文件:行号` 便于对照。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- Agent 二进制：`agent.exe`（Linux：`agent`），来自 `cmd/agent`，与单机 `cmd/stressbot` 完全独立
- 配置文件：`conf/agent.toml`（TOML）
- Agent **不开放本地 HTTP 控制面**，所有管理操作经 Admin；可选 pprof 端口（默认 6060）供本机诊断（`conf/agent.toml:68-73`）
- Admin 主文档：`docs/admin-implementation.md`

---

## 1. 模块职责

- 加载 `conf/agent.toml`，初始化日志、监控 collector、系统监控器
- 主动外连 Admin 控制面 gRPC 地址，断线按指数退避 + 抖动重连（默认永不放弃）
- 每条连接建立一条 `AgentControlService.Session` 双向流：Hello → Welcome 握手，随后并行驱动心跳生产、事件发送、事件接收、指标流发送四路循环
- 收到 `StartTask` 命令：预留任务槽位 → 内容寻址下载校验资源包 → 异步执行任务（加载 codec/proto/flow/scripts → 起 Manager）
- 收到 `StopTask` 命令或租约过期：取消任务 ctx → 停机器人 → 上报最终报告（待确认队列，至少一次）
- 收到 `Shutdown` 命令：ACK 落地并收到 `CommandReceipt` 后才退出进程
- 常驻系统指标采集上报；任务期切窗上报压测指标

---

## 2. 包结构与文件清单

```
agent/
  app.go           — Agent 运行时：生命周期、任务槽位（预留/启动/释放）、executeTask、shutdown
  config.go        — agent.toml 解析校验、ResolvedConfig、CollectStaticInfo
  session.go       — 单条控制会话编排：握手、四路循环、租约循环
  grpc_client.go   — 连接监督入口（RunSupervisor 接线）
  grpc_convert.go  — 域模型 ⇄ controlpb 转换（含 finalReportToProto）
  types.go         — Status 枚举（idle/busy）
  session/
    supervisor.go  — RunSupervisor：gRPC 重连监督（指数退避 + 抖动 + 健康会话重置）
    transport.go   — SendLoop/ReceiveLoop/HeartbeatProducer、ProtocolVersion
    report_outbox.go — 最终报告待确认队列（容量 128，重放直至 ReportAck）
  command/
    executor.go    — Executor：命令串行队列、StartTask 三段式、结果 outbox
    outbox.go      — OutcomeOutbox：命令 ACK 待确认留存（容量 4096）
  bundle/
    cache.go       — 本地内容寻址资源包缓存（.ready 复用 / .part 续传 / SHA-256 校验）
  task/
    types.go       — Assignment/CompletionReport/Result/RampUpConfig
    convert.go     — controlpb.TaskAssignment → 域 Assignment
    runner.go      — Runner：加载资源 → 起 Manager → 等待完成 → 清理
  metrics/
    system.go      — SystemMonitor：gopsutil 主机/进程采集（差分速率、序列号）
    reporter.go    — StressReporter（任务期切窗）/ SystemReporter（常驻）
    stream.go      — LatestMetrics：latest-wins 单槽 + gRPC 流发送
    types.go       — SystemSnapshot/StaticInfo 与 proto 转换

cmd/agent/
  main.go          — 入口：-config/-d、顶层 recover、日志、pprof、agent.Run
```

**依赖的内部包**：`monitor`（指标采集）、`controlplane/pb`（gRPC 生成代码）、`runner`（资源加载与网络引擎启动，与单机共用）、`robot`（Manager）、`internal/workpool`（协程池）、`internal/timerpool`、`internal/retry`、`internal/stresslog`、`config`（TOML/时长解析）、`state/shared`（Redis 共享状态）。

**外部依赖**：`google.golang.org/grpc`、`github.com/cenkalti/backoff/v4`（退避策略）、`github.com/shirou/gopsutil/v4`（系统指标）。

---

## 3. 主流程

### 3.1 进程启动（`cmd/agent/main.go:49-118`）

```
main()
  ├── 解析 -config（默认 conf/agent.toml）与 -d 参数
  ├── 顶层 defer recover（stderr + 日志，exit 2）
  ├── -d / daemon=true：daemon.Daemon fork 子进程（仅 Linux）
  ├── agent.LoadConfig(path, Version)：TOML 严格模式 + 环境变量展开 + 注入版本
  ├── stresslog.InitLog("log/agent.log", tag "agent")
  ├── agent.NewFromConfig(cfg)：Resolve 校验 → monitor.Init → agent.New
  ├── 可选 pprof（默认 6060，含 /metrics 路由）
  └── node.Run(ctx)：阻塞至 SIGINT/SIGTERM / 关闭命令 / 控制面不可恢复错误
```

**编译时版本注入**：`go build -ldflags "-X main.Version=v1.2.0" -o agent.exe ./cmd/agent`；`main.Version` 默认 `"dev"`，经 `LoadConfig` 注入 `cfg.Agent.AppVersion`（`agent/config.go:31-38`），随 Hello/心跳上报。

### 3.2 Agent 生命周期（`agent/app.go:84-117`）

```
New() → Run(ctx)
  1. sysmon.Start(stopCh)：立即采样一次建基线，随后固定周期循环
  2. SystemReporter.Start（常驻系统指标，metricsInterval）
  3. executor.Start：命令消费循环
  4. leaseLoop：每秒检查租约
  5. connectionSupervisor（协程池）：外连 + 重连监督
  6. 阻塞等待：ctx（信号）/ stopCh（Shutdown 命令确认后）/ 控制面返回不可恢复错误
  7. shutdown()
```

每条连接内（`agent/session.go:28-87`）：

```
runConnection(parent, conn)
  ├── client.Session(parent) 建流
  ├── 发送 Hello（协议版本、id/name/appVersion/maxBots/metricsInterval、当前 status/taskID/bots、StaticInfo）
  ├── 等首帧，必须是协议版本一致的 Welcome（否则 FailedPrecondition 重试）
  ├── 初始化租约截止（now + leaseDuration）
  ├── 并行四路循环（各自提交协程池）：
  │     sessionSendLoop（事件发送）、sessionRecvLoop（命令接收分发）、
  │     metrics.SendLoop（指标流）、heartbeatProducer（心跳生产）
  └── 任一路返回错误 → 会话结束 → RunSupervisor 退避重连
```

### 3.3 退出触发方式

| 触发 | 路径 |
|------|------|
| SIGINT / SIGTERM | `Run` 的 ctx 分支 → shutdown |
| Shutdown 命令 | Executor ACK（applied）→ `PrepareShutdown` 记录命令 → 收到 `CommandReceipt` → `confirmShutdown` 匹配 → `triggerStop` → shutdown（`agent/app.go:121-137`） |
| 控制面不可恢复错误 | Unauthenticated / PermissionDenied / FailedPrecondition → RunSupervisor 直接返回 → Run 退出（exit 1）（`agent/session/supervisor.go:59-62`） |
| 重连耗尽 | `reconnectMaxRetries >= 0` 且超过次数 → 同上 |

`triggerStop` 以 `sync.Once` 保证 `stopCh` 只关闭一次（`agent/app.go:119`）。

---

## 4. 核心组件设计

### 4.1 Agent 主结构（`agent/app.go:29-59`）

```go
type Agent struct {
    id      string              // 配置指定的节点 ID（进程全生命周期不变）
    cfg     *ResolvedConfig
    ctx     context.Context     // 全局 context（shutdown 时 cancel）
    cancel  context.CancelFunc

    sysmon        *metrics.SystemMonitor
    collector     *monitor.MetricsCollector  // 全局单例
    metrics       *metrics.LatestMetrics     // latest-wins 帧缓冲
    executor      *command.Executor
    bundleCache   *bundle.Cache
    reportOutbox  *session.ReportOutbox      // 最终报告待确认队列
    leaseDeadline atomic.Int64               // 租约截止（UnixNano）

    mu              sync.Mutex
    status          Status                   // idle | busy
    currentTask     *agenttask.Assignment
    taskCancel      context.CancelFunc
    taskCancelCause context.CancelCauseFunc  // 携带取消原因（停止命令/租约过期/shutdown）
    shuttingDown    bool
    taskWG          sync.WaitGroup

    sysReporter    *metrics.SystemReporter   // 常驻
    stressReporter *metrics.StressReporter   // 仅任务期
    stopCh   chan struct{}
    stopOnce sync.Once
    shutdownMu sync.Mutex
    shutdownID string                         // 待 Admin 确认的 Shutdown 命令
    shutdownSeq uint64
}
```

**任务槽位三段式**（`agent/app.go:150-268`）：

- `reserveTask`（`:220-236`）：持锁检查 `shuttingDown`/`currentTask` → 冲突返回 `errAgentShuttingDown` 或 `taskBusyError{currentTaskID}`；成功则创建 `WithCancelCause` 的任务 ctx 并置 busy。**预留发生在资源包下载之前**——下载失败即释放，不占用槽位产生幻影任务
- `launchReservedTask`（`:238-259`）：校验预留仍有效 → `taskWG.Add(1)`（在锁内、协程提交前，避免与 shutdown 的 Wait 竞态）→ 提交 `executeTask`
- `releaseReservedTask`（`:261-268`）：启动失败/会话中断时取消 ctx 并回 idle

### 4.2 命令执行器（`agent/command/executor.go`）

```go
type Executor struct {
    host     Host        // 对 Agent 生命周期的最小依赖接口（:30-39）
    cache    *bundle.Cache
    outcomes *OutcomeOutbox  // 命令 ACK 待确认（容量 4096）
    queue    chan commandRequest  // 串行执行队列（容量 64）
    inflight map[string]struct{}  // 执行中的 StartTask
    deferred map[string]commandRequest
}
```

- 消费循环（`:62-74`）：逐条取出命令执行，结果进 outbox
- `execute`（`:88-123`）：
  - 目标节点不匹配 → REJECTED
  - 同 `commandId` 重放 → 直接回放既有结果（幂等，`:96-98`）
  - **StartTask 走 `beginStart` 三段式**（不在此处终结）：当前已是同任务 → DUPLICATE；已有其他任务 → REJECTED；否则 `FromProto` 解析 → `ReserveTask` → 协程池内下载资源包 + 启动
  - StopTask → `CancelTask(command.TaskId, reason)`（带期望任务 ID，防误伤新任务）；当前任务不匹配 → REJECTED（含当前任务 ID）
  - Shutdown → 只记 ACK，**进程退出延迟到 CommandReceipt**（`:223-226`）
  - 瞬时错误（canceled/deadline/unavailable）不落 REJECTED，交由命令重放重试（`isTransientCommandError`，`:286-296`）
- `beginStart`（`:125-193`）：`context.AfterFunc(request.ctx, ...)` 把「会话在下载期间中断」转成任务取消原因 `errTaskSessionInterrupted`；`cache.Ensure` 下载校验发布 → 会话仍健康且无取消 → `LaunchReservedTask`；任何失败释放预留，瞬时类错误静默（等重放），其余 REJECTED

### 4.3 SystemMonitor（`agent/metrics/system.go:85-231`）

- 采集目标：当前进程 + 主机。`StaticInfo` 在创建时补全主机总内存（`NewSystemMonitor`，`:106-121`）
- `collect()`（`:154-210`）：主机 CPU%（首个采样建基线）、主机内存（total/available → used/percent）、进程 CPU%（按逻辑核数归一）、进程 RSS/线程/FD、Go 堆与 goroutine、主机网络计数器差分速率（回绕/倒退置空）
- 快照携带递增 `sequence`，Admin 侧按序去重
- 采集失败的字段保持 nil（`boundedPercent`/`counterRate` 对 NaN/Inf/负数防御）

`SystemSnapshot` 字段（`agent/metrics/types.go:10-26`）：`timestamp`、`sequence`、`hostCpuPercent`、`hostMemTotalBytes/hostMemUsedBytes/hostMemPercent`、`hostNetSendBytesPerSec/hostNetRecvBytesPerSec`、`processCpuPercent`、`processRssBytes`、`processHeapBytes`、`processGoroutines`、`processThreads`、`processFds`。`StaticInfo`（`:29-38`）：`hostname/os/arch/numCpu/memTotalBytes/goVersion/kernelVer（未采集赋值）/startedAt`。

### 4.4 StressReporter / SystemReporter（`agent/metrics/reporter.go`）

- **StressReporter**（任务期，`:22-79`）：按 `metricsInterval` 从 collector 调 `TakeReportSnapshot` 切**顺序区间窗口**（窗口序列号从 1 递增，携带窗口起止与期望周期），交给 Sink；`Snapshot()` 取累计快照不切窗（供 reset 段落报告）；`Stop()` 幂等并同步冲刷最后一帧
- **SystemReporter**（常驻，`:82-114`）：按同周期递交系统快照；Stop 不冲刷
- 两者写入 `LatestMetrics`（Sink 实现）

### 4.5 LatestMetrics（`agent/metrics/stream.go:26-139`）

- 压测/系统各一个槽位，latest-wins：新帧覆盖未发送旧帧并累计 `dropped`
- 压测槽在任务 ID 变化时清空旧任务帧并清零丢弃计数（`:41-54`）
- `SendLoop`：被唤醒后取出两槽，经 `AgentMetricsService.Report` 客户端流发送；发送失败把帧放回槽位（与新帧冲突时计数让位），会话重建后由新流继续（`:108-139`）
- `droppedIntervals` 随下一帧带给 Admin 补账（窗口缺失计数）

### 4.6 资源包缓存（`agent/bundle/cache.go`）

```go
type Cache struct {  // 根目录 <taskWorkDir>/stressbot-bundles，操作互斥串行
    root string
    mu   sync.Mutex
}
```

`Ensure(ctx, client, agentID, digest, size)`（`:40-65`）：

1. 已发布目录存在 `.ready` 标记 → 直接复用（零下载）
2. 本地 zip 校验（SHA-256 + 大小）通过 → 跳过下载
3. `downloadAttempt`（`:71-148`）：打开 `{digest}.zip.part` 追加写；以当前长度为 `BundleRequest.offset` **断点续传**；逐块校验 offset/totalSize/sha256；超长/不完整报错；完成后整体校验，损坏则删 `.part` 重试一次；原子 rename 发布
4. `extractBundle`（`:173-233`）：解压到临时目录再 rename 发布；**拒绝目录、非常规文件与符号链接条目**（`:184-187`）；路径清洗防穿越（`:188-193`）；文件以 `O_CREATE|O_EXCL` 落盘；写入 `.ready` 标记后原子替换发布目录

### 4.7 TaskRunner（`agent/task/runner.go:69-280`）

```
Run(ctx)
  0. LogLevel 非空 → 临时切换进程日志等级（defer 恢复，含异常路径）
  1. 使用命令执行器已校验发布的资源包目录（BundleDir；为空即 Failed）
  2. runner.LoadResources：codec resolver（adapter/*_codec.json + errors.json，
     缺 errors.json 仅 Warn）→ proto 编译 → flow.json → Lua 脚本
  3. collector.Reset() + SetApdexT（绝不 monitor.Init——会替换全局 collector 使 Reporter 引用失效）
  4. 严格解析任务时长（heartbeatInterval/tcpTimeout/httpTimeout，写错即 Failed）
  5. runner.StartDialer（gnet 网络引擎）
  6. runner.NewRuntimePool（Lua 预编译失败非致命）
  7. 共享状态：命令携带 Shared 时连接 Redis（runId 命名空间），Agent 只负责 Close
  8. robot.ManagerConfig（含 StartIndex 全局序号起点、CodecResolver、缩放后的 RampUp、SharedStore）
  9. mgr := robot.NewManager(ctx, ...)（任务 ctx 派生传播取消）；挂 OnStageReset/OnStageChange
 10. RampUp ? StartWithRampUp : StartAll；启动失败 → StopAll 后按取消/失败分类返回
 11. select { <-ctx.Done() | <-mgr.Done() }
 12. cleanup := mgr.StopAll()；ctx.Canceled → Stopped，否则 Completed
```

`OnStageReset` 回调（`agent/app.go:296-304`）：reset 边界触发 → 取当前快照 → `collector.Reset()` → 阶段报告（StageIndex=即将进入的阶段下标）进 ReportOutbox。

`Cleanup()`（`agent/task/runner.go:283-292`）：删除临时工作目录（当前实现 workDir 为空时为 no-op——资源包位于共享缓存目录，按内容寻址复用，不随单次任务删除）。

### 4.8 ReportOutbox（`agent/session/report_outbox.go`）

- 容量 128（`:31-36`）；`Offer` 克隆入队并返回确认结果通道，同 ReportId 重复提交返回既有通道
- `Snapshot` 按提交顺序克隆全部未确认报告——会话（重）建立时批量重放（`agent/session/transport.go:95-100`）
- `Acknowledge`（`:72-94`）：ReportAck accepted → 通知等待方 nil；rejected → 传递拒绝原因；未知 ReportId 静默忽略

### 4.9 会话监督与传输（`agent/session/`）

- **RunSupervisor**（`supervisor.go:31-85`）：
  - 指数退避（factor 2）+ 抖动，`initialInterval`（默认 5s）起、`maxInterval`（默认 30s）封顶；实际等待在 `[0, 2×maxInterval]` 内抖动
  - **健康会话重置**：存活 ≥ maxInterval 的会话断线视为新一次偶发故障的首次失败，退避与重试计数重置回初始间隔（`:46-54`）
  - 不可恢复错误（Unauthenticated/PermissionDenied/FailedPrecondition）直接返回终止进程（`:59-62`）
  - `reconnectMaxRetries >= 0` 时超次数放弃；默认 -1 持续重连
- **SendLoop**（`transport.go:69-103`）：优先排空缓冲事件，随后在 ctx 结束 / 新事件 / 命令结果通知（重放全部待确认 ACK）/ 心跳 / 报告通知（重放全部未确认报告）之间选择发送
- **ReceiveLoop**（`transport.go:118-157`）：命令校验 `delivery_generation` 与当前会话一致后入执行队列；HeartbeatAck 校验 generation 并把「剩余租约」交给回调；ReportAck/CommandReceipt 分发；ServerClosing → 以 Unavailable 结束会话（触发重连）；ProtocolError / 重复 Welcome / 未知事件 → 会话终止
- **HeartbeatProducer**（`transport.go:159-175`）：间隔优先取 Welcome 下发值，非法回退本地配置；心跳只保留最新一条在途

### 4.10 租约循环（`agent/session.go:128-143`）

每秒检查：租约截止已过且当前 busy → `cancelCurrentTask("Admin 控制面租约已过期")` 并清零截止。租约由 Welcome 初始化、每条 HeartbeatAck 续期（`agent/session.go:58`、`:103`）。

---

## 5. 协议契约（gRPC）

以下交互均定义于 `controlplane/proto/control.proto`。

### 5.1 会话建立（AgentControlService.Session 双向流，`:7-9`）

```
Agent → Admin: AgentEvent{ hello }        // protocol_version=1, agent_id, name, app_version,
                                          // max_bots, metrics_interval_nanos, status, current_task_id,
                                          // current_bots, static_info
Admin → Agent: AdminEvent{ welcome }      // protocol_version, generation, heartbeat_interval_nanos,
                                          // lease_duration_nanos, server_time_unix_nano
```

协议版本不一致（`ProtocolVersion = 1`，`agent/session/transport.go:17`）→ 会话无法建立，Admin 重连。

### 5.2 心跳与租约（`:82-96`）

```
Agent → Admin: AgentEvent{ heartbeat }    // agent_id, generation, sent_at, status(idle/busy),
                                          // current_task_id, current_bots, app_version
Admin → Agent: AdminEvent{ heartbeat_ack } // generation, server_time, lease_deadline
```

### 5.3 命令（`:98-141`）

```
Admin → Agent: AdminEvent{ command }      // command_id, sequence, agent_id, task_id,
                                          // created_at, delivery_generation,
                                          // body: start_task{assignment, bundle{sha256,size}}
                                          //     | stop_task{reason} | shutdown{reason}
Agent → Admin: AgentEvent{ command_ack }  // command_id, sequence, agent_id, task_id,
                                          // status(applied/duplicate/rejected), reason
Admin → Agent: AdminEvent{ command_receipt }  // command_id, sequence（ACK 已持久化）
```

### 5.4 最终报告（`:143-161`）

```
Agent → Admin: AgentEvent{ final_report } // report_id, agent_id, task_id, result(completed/stopped/failed),
                                          // error_message, finished_at, stage_index,
                                          // snapshot(CollectorSnapshot), cleanup_status
Admin → Agent: AdminEvent{ report_ack }   // report_id, task_id, stage_index, accepted, reason
```

至少一次语义：未收到 accepted 的报告留在 outbox，会话重建时重放。

### 5.5 指标上报（AgentMetricsService.Report 客户端流，`:15-17`、`:191-214`）

```
Agent → Admin: MetricsEnvelope{ agent_id, generation, dropped_intervals,
                                stress{task_id, reported_at, snapshot} | system{snapshot} }（多次）
Admin → Agent: MetricsClose{ accepted, rejected }（半关闭时）
```

### 5.6 资源包下载（AgentBundleService.DownloadBundle 服务端流，`:11-13`、`:178-189`）

```
Agent → Admin: BundleRequest{ agent_id, sha256, offset }   // offset 断点续传
Admin → Agent: BundleChunk{ sha256, total_size, offset, data }（256KB 分块，多次）
```

---

## 6. 心跳机制

- 间隔：Welcome 下发的 `heartbeat_interval_nanos` 优先，非法（<=0）回退本地 `agent.heartbeatInterval`（默认 10s）（`agent/session/transport.go:160-163`）
- 心跳内容反映实时状态（`runtimeState`，`agent/session.go:114-121`）：busy 携带当前任务 ID 与 bot 数
- 通道内只保留最新一条在途心跳（`Sender.OfferHeartbeat` 覆盖旧值，`agent/session/transport.go:43-56`）——心跳是状态快照而非事件，旧值无意义
- 每条 HeartbeatAck 携带剩余租约，Agent 据此续期（`agent/session/transport.go:132-140`）
- **Admin 挂掉的判活不靠心跳失败计数**：会话断开由 gRPC 流错误即刻感知，任务生死由租约倒计时决定（`leaseDuration` 默认 45s，覆盖重连最坏耗时）

---

## 7. 任务执行细节

### 7.1 executeTask（`agent/app.go:270-328`）

1. **保护性 recover**：任务内 panic 记日志不影响主循环
2. defer 清理：`currentTask/taskCancel/taskCancelCause/stressReporter` 置空、状态回 idle、`taskCancel()`
3. 创建 StressReporter 并启动切窗循环
4. 创建 Runner、注入 OnStageReset
5. `runner.Run(taskCtx)` 阻塞执行
6. `StressReporter.Stop()`（同步冲刷最后一帧窗口）
7. `finalSnapshot := collector.Snapshot(nil, 0)`（累计快照）
8. 组装 CompletionReport（含 CleanupStatus）→ `reportOutbox.Offer` → 在 `TaskReportTimeout`（默认 30s）内等待确认；超时仅 Warn（报告仍在 outbox，会话重建重放）
9. `runner.Cleanup()`
10. `debug.FreeOSMemory()`（大任务后主动归还内存）

### 7.2 任务结果判断（`agent/task/runner.go:275-279`）

| 条件 | 结果 |
|------|------|
| `ctx.Err() == context.Canceled`（停止命令/租约过期/shutdown） | `Stopped` |
| 启动阶段被取消（StartWithRampUp 返回 Canceled） | `Stopped`（避免历史归档误判失败，`runner.go:248-253`） |
| 流水线步骤失败 | `Failed` + 错误消息 |
| `mgr.Done()` 正常退出 | `Completed` |

### 7.3 RampUp 支持

`Assignment.RampUp` 已由 Admin 按本节点分得机器人数缩放（`admin/task/distribution.go:128-177`）；`Reset: true` 阶段完成触发 `OnStageReset` 上报该段落累计指标并重置采集器。

### 7.4 共享状态

命令携带 `Shared{runId, redis}` 时，Runner 连接 Redis 并以 runId 为命名空间（`agent/task/runner.go:173-199`）；连接失败 → Failed；统一清理由 Admin 在任务终态执行，Agent 只断开连接。

---

## 8. 优雅退出（`agent/app.go:335-366`）

```
shutdown()
  1. triggerStop + shuttingDown = true（拒绝新任务）
  2. cancelCurrentTask("agent shutdown")（如有）
  3. 协程池内 taskWG.Wait()，主流程最多等 TaskReportTimeout + 5s
     （Wait 放协程池外等待，避免池满死锁；timerpool 计时）
  4. stressReporter.Stop()（保护性）/ sysReporter.Stop()
  5. cancel 全局 ctx（四路会话循环退出）
  → 连接监督与指标循环随 ctx 退出；进程由 main 返回
```

注：会话断开即注销，Admin 侧靠心跳超时与 Hello 重注册收敛节点状态，Agent 无显式注销请求。

---

## 9. 配置文件完整参考（`conf/agent.toml` / `agent/config.go`）

### 9.1 顶层结构（`agent/config.go:18-24`）

```go
type AppConfig struct {
    Log     *stresslog.Config        `toml:"log"`
    Monitor *monitor.CollectorConfig `toml:"monitor"`
    Pprof   *config.PprofConfig      `toml:"pprof"`
    Agent   Config                   `toml:"agent"`
    Daemon  bool                     `toml:"daemon"`
}
```

### 9.2 [agent] 段字段（`agent/config.go:59-70`）

| 字段 | TOML 键 | 默认值 | 说明 |
|------|---------|--------|------|
| `ID` | `id` | 必填 | 节点全局唯一 ID（Resolve 校验非空，`:109-111`） |
| `AdminAddress` | `adminAddress` | 必填 | Admin 控制面 gRPC 地址，必须 host:port（`:112-117`） |
| `Name` | `name` | 留空=hostname，再空=id | 显示名（`:118-125`） |
| `MaxBots` | `maxBots` | 5000 | 单节点最大机器人数（<=0 回退 5000） |
| `HeartbeatInterval` | `heartbeatInterval` | 10s | 心跳间隔（Welcome 下发值优先） |
| `ReconnectMaxRetries` | `reconnectMaxRetries` | -1 | 0 视为 -1（`resolveReconnectRetries`，`:159-164`） |
| `MetricsInterval` | `metricsInterval` | 5s | 压测/系统指标上报周期 |
| `TaskWorkDir` | `taskWorkDir` | 留空=os.TempDir() | 任务工作目录（资源包缓存根） |
| `Reconnect` | `[agent.reconnect]` | 5s/30s/30s | initialInterval / maxInterval / taskReportTimeout |
| `AppVersion` | —（编译注入） | "dev" | 不来自配置 |

### 9.3 时长解析纪律

全部时长走 `config.ParsePositiveDuration`（`agent/config.go:134-142`）：**留空回退默认，写错直接让配置校验失败**（收集全部错误后一次性返回，不带退化默认值继续跑）。

### 9.4 联动约束（部署须知）

- `heartbeatInterval` 必须明显小于 Admin `controlPlane.unhealthyAfter`（默认 10s < 30s）
- `reconnect.maxInterval` 的 2 倍抖动上界 + 拨号握手耗时应明显小于 Admin `controlPlane.leaseDuration`，否则断连重连期间任务会因租约过期被取消（`conf/agent.toml:115-119`、`admin/config.go:38-47`）。默认 30s/45s 组合下该余量并不宽裕（退避顶满时单次等待可达 60s），调大 `maxInterval` 时必须同步调大 `leaseDuration` 并保持其小于 `offlineAfter`
- `monitor.sketch`（DDSketch 精度/桶数）必须与 Admin 侧一致，否则 Admin 合并 sketch 结果错误（`conf/agent.toml:59-66`）

### 9.5 配置示例

```toml
[log]
path = "log/agent.log"
level = "info"

[monitor]
apdexThresholdMs = 100
timingDetail = "rtt"

[pprof]
port = 6060

[agent]
id = "agent-001"
adminAddress = "127.0.0.1:7720"   # Admin 控制面 gRPC 端口
name = "agent-001"
maxBots = 5000
heartbeatInterval = "10s"
metricsInterval = "5s"
reconnectMaxRetries = -1
taskWorkDir = ""

  [agent.reconnect]
  initialInterval = "5s"
  maxInterval = "30s"
  taskReportTimeout = "30s"
```

---

## 10. 类型定义

### 10.1 状态与结果（`agent/types.go`、`agent/task/types.go:10-16`）

```go
type Status string          // StatusIdle = "idle" | StatusBusy = "busy"
type Result string          // Completed = "completed" | Stopped = "stopped" | Failed = "failed"
```

### 10.2 Assignment（`agent/task/types.go:31-52`）

```go
type Assignment struct {
    TaskID, TaskName      string
    StartNumber           int      // 账号编号基数
    StartIndex            *int     // 全局机器人序号 0-based 起点（Validate 强制非负，:55-63）
    TotalBots             int
    AccountPrefix         string
    ConcurrentNum         int
    MainService           string
    StateExtra            map[string]string
    HeartbeatInterval     string   // duration 字符串
    TCPTimeout, HTTPTimeout string
    ApdexT                int
    LogLevel              string
    BundleDigest          []byte   // 资源包摘要（命令携带，不序列化）
    BundleSize            int64
    BundleDir             string   // 命令执行器发布后的资源包目录
    RampUp                *RampUpConfig
    Shared                *SharedRuntimeAssignment // {RunID, Redis 配置}
}
```

### 10.3 CompletionReport（`agent/task/types.go:18-29`）

AgentID、TaskID、Result、ErrorMsg、FinishedAt、FinalSnapshot、StageIndex、CleanupStatus（`robot.CleanupStatus`：清理状态/原因/各阶段计数与问题明细）。

---

## 11. 错误处理与重试策略

| 操作 | 失败处理 |
|------|----------|
| gRPC 连接失败 | RunSupervisor 指数退避 + 抖动（5s→30s），默认无限重试；不可恢复错误终止进程（`agent/session/supervisor.go:31-85`） |
| 会话循环任意一路出错 | 会话整体结束 → 重连；未确认命令由 Admin 重放，未确认报告由 outbox 重放 |
| 资源包下载失败/损坏 | `.part` 续传重试一次；仍失败释放任务预留，命令按瞬时/永久分类 REJECTED 或静默重试（`agent/bundle/cache.go:136-141`、`agent/command/executor.go:172-186`） |
| StartTask 时已有任务 | REJECTED（reason 含当前任务 ID） |
| StopTask 任务不匹配 | REJECTED（防误伤新任务） |
| 指标发送失败 | 帧放回 latest-wins 槽位，新流建立后继续；被覆盖帧计数补账（`agent/metrics/stream.go:121-137`） |
| 最终报告未确认 | outbox 保留并重放；executeTask 等待 TaskReportTimeout 后放弃等待（报告仍在队列） |
| 任务流水线步骤失败 | 返回 `Failed` 报告（含错误消息） |
| Lua 脚本预编译失败 | 非致命（Warn 继续） |
| 任务时长配置写错 | 任务 Failed（不退化默认值） |

**资源失败即 Failed 的步骤**：资源包目录为空、codec/proto/flow 加载失败、共享 Redis 连接失败、网络引擎启动失败、机器人数启动失败（后者先 StopAll 已创建机器人并采集清理状态）。

### 11.1 并发安全

- `Agent.mu` 保护 `status/currentTask/taskCancel/taskCancelCause/stressReporter/shuttingDown`；`taskWG.Add(1)` 在锁内、协程提交前完成
- `SystemMonitor.mu`（RWMutex）保护 latest 快照
- `LatestMetrics.mu` 保护双槽；`stopOnce`/`shutdownMu` 各司其职
- 所有业务 goroutine 经 `internal/workpool` 提交（自带 recover）

---

## 12. 日志系统

- 日志文件 `log/agent.log`（zap + lumberjack 轮转，配置见 `[log]`），tag `agent`
- 日志为文件落盘，排查用日志文件或 pprof 端口的 `/metrics` 路由
- 任务级 `LogLevel`（`assignment.LogLevel`）在任务期间临时切换进程日志等级，defer 恢复（`agent/task/runner.go:76-93`）

---

## 13. 能力边界（现状）

- **单任务约束**：任一时刻只运行一个任务，冲突的 StartTask 直接 REJECTED（`agent/app.go:220-236`）；无任务排队。
- **无自动升级**：控制面命令只有 start_task / stop_task / shutdown 三类（`controlplane/proto/control.proto:105-109`）；升级为运维手动重启 Agent 进程，远程关机由 Shutdown 命令 + CommandReceipt 确认语义承担。
- **任务状态为内存态**：任务与最终报告（ReportOutbox）均不落盘，Agent 崩溃时未确认报告丢失；Admin 侧以停止超时安全网（60s）与全节点失效检测兜底合成报告（`admin/app.go:616-683`、`:689-717`）。
- **日志仅文件落盘**：排查用 `log/agent.log` 与 pprof 端口（`/debug/pprof`、`/metrics`），不经网络查询接口。

---

## 14. 与 Admin 的协议契约

### 14.1 Agent 必须遵守

1. Hello 必须携带协议版本 1 与完整 StaticInfo；ID 与 Admin 命令目标一致（不一致命令直接 REJECTED）
2. 心跳间隔遵循 Welcome 下发值（本地配置仅兜底）；busy 心跳必须携带 currentTaskId
3. 任务最终报告至少一次送达（outbox 重放），收到 accepted 前不得丢弃
4. 同一时刻只运行一个任务；Shutdown 命令的进程退出必须等 CommandReceipt
5. 上报的 `CollectorSnapshot` 经 `controlplane.ToProtoCollectorSnapshot` 转换，字段与 monitor 包当前结构完全一致；DDSketch 配置与 Admin 一致

### 14.2 Agent 假定 Admin 提供

1. `AgentControlService`/`AgentBundleService`/`AgentMetricsService` 三服务可用
2. StartTask 携带的 BundleDescriptor 与 DownloadBundle 归档一致（SHA-256 + 大小）
3. HeartbeatAck 携带真实剩余租约；ServerClosing 后可按重连流程恢复
