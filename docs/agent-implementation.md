# Agent 节点技术文档

> **历史说明（2026-08-13）**：本文记录重构前 Agent 实现，仅供追溯，不再作为当前实现契约。现行架构与控制面请以 `README.md`、`AGENTS.md`、`controlplane/proto/control.proto` 和 `docs/superpowers/specs/2026-08-13-package-architecture-design.md` 为准。

> **角色定位**：Agent 是分布式压测系统的**执行节点**，负责注册到 Admin、接收任务、驱动机器人压测、采集压测/系统指标并上报 Admin。
> **本文档目标读者**：负责 `agent/` 包及 `cmd/agent/` 入口的开发者。
> **前置阅读**：`docs/admin-implementation.md`（Admin 端协议契约可双向交叉验证）。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- 业务进程二进制：`stressbot.exe`（Linux：`stressbot`），来自 `cmd/agent`
- 单机模式：`agent.enabled=false`，行为与改造前完全一致
- Agent 模式：`agent.enabled=true`，注册到 Admin 等待任务下发
- Admin 主文档：`docs/admin-implementation.md`

## 1. 模块职责

Agent 进程是 stressbot 的业务主体，分为单机模式和 Agent 模式：

### 1.1 单机模式（agent.enabled=false）

完全保持现有行为：直接加载本地 `flow.json` -> 创建 `robot.Manager` -> 启动机器人 -> 等待 Ctrl+C 或运行时长到期 -> 导出 CSV -> 退出。

### 1.2 Agent 模式（agent.enabled=true）

- 启动时仅初始化基础设施（监控、HTTP 服务器、SystemMonitor），**不**创建 `robot.Manager`
- 向 Admin 发起注册（携带本机静态信息：Hostname、CPU 核数、内存总量、Go 版本、AppVersion、OS/Arch）
- 启动心跳循环（默认 10s 一次），失败时快速重试（同间隔）
- 启动**系统指标推送循环**（与 StressInterval 同步，默认 5s，独立于任务）
- 启动**任务轮询**（30s 间隔，回退通道）
- 收到任务下发后：从 Admin 拉取配置 -> 写入临时目录 -> 启动 TaskRunner 13 步流水线 -> 启动**压测指标推送循环**
- 收到停止指令或机器人全部退出后：上报 `done` -> 清理临时目录 -> 回到 idle
- 优雅退出：取消当前任务 -> 等待上报完成 -> 注销（best-effort）-> 关闭 HTTP -> 关闭协程池

## 2. 包结构与文件清单

### 2.1 包文件

```
agent/
  agent.go         — Agent 主结构、生命周期、注册/心跳/任务轮询/优雅退出 (~595 行)
  config.go        — Config 解析 + 校验 + 静态信息采集 (~122 行)
  sysmon.go        — SystemMonitor：基于 gopsutil/v4 采集 (~168 行)
  reporter.go      — StressReporter / SystemReporter 推送循环 (~196 行)
  task_runner.go   — 任务执行：拉配置、写临时目录、13 步流水线 (~291 行)
  http_server.go   — Agent HTTP 服务（接收 Admin 命令 + 日志查询） (~298 行)
  http_client.go   — 与 Admin 通信的 client（带重试） (~288 行)
  types.go         — 与 Admin 共享的 DTO（请求/响应/状态/枚举） (~193 行)
```

### 2.2 cmd 入口

```
cmd/agent/
  main.go          — 入口：解析 config -> 单机/Agent 分支（含 StandaloneConfig 定义）
```

### 2.3 依赖关系

Agent 包依赖以下内部包：

| 依赖包 | 用途 |
|--------|------|
| `monitor` | `MetricsCollector` 指标采集与快照 |
| `utils` | 协程池 `GetWorkPool()`、`ParseDurationDefault()`、`Daemon()` |
| `utils/log` | 结构化日志（zap + lumberjack） |
| `logview` | 环形缓冲区日志查询 |
| `adapter` | Lua 协议适配器 |
| `protox` | 动态 protobuf 加载 |
| `engine` | 流程配置解析 |
| `network` | gnet 网络引擎 |
| `robot` | Robot Manager |
| `script` | Lua 运行时池 |

外部依赖：

- `github.com/shirou/gopsutil/v4` — 系统指标采集（CPU/内存/网络/进程/负载）

## 3. 主流程

### 3.1 进程启动（cmd/agent/main.go）

```
main()
  ├── 解析 -config 参数
  ├── loadConfig() 反序列化 JSON
  ├── 初始化日志（Agent 模式用 agent.log，单机用 stressbot.log）
  ├── AttachRingBuffer（50000 条容量）
  │
  ├── if agent.enabled:
  │     runAgentMode(cfg)
  │       ├── cfg.Agent.Resolve() 校验配置
  │       ├── monitor.Init() 创建全局 collector
  │       ├── agent.New(resolved, collector)
  │       └── agent.Run() 阻塞
  │
  └── else:
        runStandalone(cfg, confDir)
          ├── 加载适配器 / proto / flow / Lua 脚本
          ├── 初始化监控
          ├── 创建 Manager + StartAll()
          ├── 等待信号或运行时长到期
          ├── StopAll() + 导出 CSV
          └── 关闭适配器 + 协程池
```

**编译时版本注入**：

```bash
go build -ldflags "-X main.Version=v1.2.0" -o stressbot.exe ./cmd/agent
```

`main.Version` 默认值为 `"dev"`，通过 `cfg.Agent.AppVersion` 传递给 Agent。

**守护进程模式**（仅 Linux）：

- 命令行 `-d` 标志或配置 `"daemon": true`
- 调用 `utils.Daemon()` fork 子进程后父进程退出

### 3.2 Agent 生命周期主循环

```
                    New()
                      │
                      ▼
              ┌───────────────────────────────────────┐
              │  Run() 阻塞主循环                       │
              │                                        │
              │  1. SystemMonitor.Start()               │
              │  2. startHTTPServer()  (port 7719)      │
              │  3. registerWithRetry()  (指数退避)      │
              │     └── regGeneration++                 │
              │  4. SystemReporter.Start()  (常驻推送)   │
              │  5. heartbeatLoop  (协程池)             │
              │  6. taskPollLoop  (30s 回退通道)         │
              │  7. 阻塞等待退出信号                     │
              │     ├── SIGINT / SIGTERM                │
              │     └── stopCh (远程关闭)                │
              │  8. shutdown()                          │
              └────────────────────────────────────────┘
                      │
                      ▼
              ┌───────────────────────────────────────┐
              │  idle (等待任务)                        │
              │                                        │
              │  两种触发方式：                          │
              │  ├── POST /agent/v1/task  (Admin Push) │
              │  └── GET pending-task   (30s Poll)     │
              └───────┬───────────────────────────────┘
                      │ 任务到达
                      ▼
              ┌───────────────────────────────────────┐
              │  busy (执行任务)                        │
              │                                        │
              │  executeTask():                        │
              │  ├── 创建 StressReporter + Start       │
              │  ├── NewTaskRunner + Run (13 步)       │
              │  ├── StressReporter.Stop() (flush)     │
              │  ├── 采集 finalSnapshot                │
              │  ├── ReportTaskDone()  (一次性上报)     │
              │  ├── runner.Cleanup()                  │
              │  └── 恢复 idle                         │
              └───────────────────────────────────────┘
                      │
                      │ 任务完成
                      ▼
                   返回 idle
```

### 3.3 任务下发：Push + Poll 双通道

- **主通道（Push）**：Admin 通过 `POST http://agent:7719/agent/v1/task` 推送任务，Agent 立即返回 `202 Accepted` 后异步处理
- **回退通道（Poll）**：Agent 每 30s 调用 `GET /sbot/agent/{id}/pending-task`，处理可能因 Push 失败遗漏的任务

任何一方先收到都先标记 `currentTask`，重复下发时 Agent 返回 `409 Conflict`（含 `currentTaskId`）。

### 3.4 退出触发方式

| 触发 | 行为 |
|------|------|
| 收到 `POST /agent/v1/stop` | 取消当前任务 ctx，等待 drain 完成，保持进程不退出 |
| 收到 `POST /agent/v1/shutdown` | 触发 `triggerStop()`，走完整 shutdown 流程后进程退出 |
| 收到 SIGINT / SIGTERM | 走完整 shutdown 流程后进程退出 |

## 4. 核心组件设计

### 4.1 Agent 主结构（agent/agent.go）

```go
type Agent struct {
    id      string                    // UUID v4（启动时生成，进程全生命周期不变）
    cfg     *ResolvedConfig           // 已解析的运行期配置
    started time.Time                 // 启动时间
    ctx     context.Context           // 全局 context（shutdown 时 cancel）
    cancel  context.CancelFunc

    sysmon    *SystemMonitor          // 系统指标采集器
    collector *monitor.MetricsCollector // 压测指标采集器（全局单例）
    httpSrv   *http.Server            // 接收 Admin 命令的 HTTP 服务
    httpCli   *AdminClient            // 与 Admin 通信的客户端

    // 任务状态（mu 保护）
    mu          sync.Mutex
    status      AgentStatus           // idle | busy
    currentTask *TaskAssignment       // 当前任务（nil = 空闲）
    taskCancel  context.CancelFunc    // 取消当前任务

    // 任务执行追踪
    taskWG sync.WaitGroup            // executeTask Add(1)/Done()，shutdown 时 Wait

    // 上报循环
    sysReporter    *SystemReporter    // 常驻（注册后启动）
    stressReporter *StressReporter    // 仅 task running 时存在

    // 优雅退出
    stopCh   chan struct{}            // 关闭即触发主循环退出
    stopOnce sync.Once               // 保证 stopCh 仅关闭一次

    // 注册版本号
    regGeneration atomic.Int64        // 每次重新注册成功后递增
}
```

**关键方法**：

| 方法 | 签名 | 说明 |
|------|------|------|
| `New` | `(cfg *ResolvedConfig, collector *MetricsCollector) (*Agent, error)` | 创建实例：生成 UUID、创建 SystemMonitor、创建 AdminClient |
| `Run` | `() error` | 阻塞主循环（含顶层 panic recover） |
| `triggerStop` | `()` | 线程安全地关闭 stopCh（sync.Once 保护） |
| `ID` | `() string` | 返回 Agent UUID |
| `Status` | `() AgentStatus` | 返回当前状态（持锁） |
| `cancelCurrentTask` | `(reason string) (taskID string, canceled bool)` | 取消当前任务（持锁安全） |
| `registerWithRetry` | `(ctx context.Context) error` | 指数退避注册（ReconnectMaxRetries 控制重试策略） |
| `heartbeatLoop` | `(ctx context.Context)` | 心跳循环 goroutine |
| `taskPollLoop` | `(ctx context.Context)` | 30s 任务轮询 goroutine |
| `executeTask` | `(parentCtx context.Context, task *TaskAssignment)` | 异步执行任务（taskWG 追踪） |
| `shutdown` | `() error` | 优雅关闭（6 步） |

### 4.2 SystemMonitor（agent/sysmon.go）

```go
type SystemMonitor struct {
    interval time.Duration            // 采集间隔
    static   StaticInfo               // 启动时一次性采集

    mu     sync.RWMutex
    latest SystemSnapshot             // 最新一次采集结果（Snapshot 只做读锁）

    // 网络速率差分基线
    prevNetSent uint64
    prevNetRecv uint64
    prevAt      time.Time
    initialized bool                  // 第一次采集只建基线，第二次起才有速率值

    // 进程句柄
    pid  int32
    self *process.Process             // gopsutil 进程句柄
}
```

**SystemSnapshot 完整字段定义**：

```go
type SystemSnapshot struct {
    Timestamp time.Time `json:"timestamp"`

    // CPU（4 字段）
    CPUPercent float64   `json:"cpuPercent"`    // 总 CPU 使用率（%）
    CPUPerCore []float64 `json:"cpuPerCore"`    // 每核心使用率
    LoadAvg1   float64   `json:"loadAvg1"`      // 1 分钟负载均值（Linux only，Windows=0）
    LoadAvg5   float64   `json:"loadAvg5"`
    LoadAvg15  float64   `json:"loadAvg15"`

    // 内存（4 字段）
    MemTotalMB uint64  `json:"memTotalMB"`      // 物理内存总量
    MemUsedMB  uint64  `json:"memUsedMB"`       // 已用物理内存
    MemPercent float64 `json:"memPercent"`      // 内存使用率（%）
    SwapUsedMB uint64  `json:"swapUsedMB"`      // Swap 已用量

    // 进程（6 字段）
    ProcessRssMB  uint64 `json:"processRssMB"`   // 进程 RSS（物理常驻内存）
    ProcessHeapMB uint64 `json:"processHeapMB"`  // Go 堆分配
    ProcessSysMB  uint64 `json:"processSysMB"`   // Go 进程总占用
    NumGoroutine  int    `json:"numGoroutine"`   // Goroutine 数量
    NumThread     int32  `json:"numThread"`      // OS 线程数
    NumFD         int32  `json:"numFd"`          // 文件描述符数（Windows 上可能为 0）

    // 网络速率（差分计算，2 字段）
    NetSendKBps float64 `json:"netSendKBps"`     // 上行速率（KB/s）
    NetRecvKBps float64 `json:"netRecvKBps"`     // 下行速率（KB/s）

    // GC（2 字段）
    GCCount      uint32  `json:"gcCount"`         // GC 总次数
    GCPauseAvgMs float64 `json:"gcPauseAvgMs"`    // 最近 N 次 GC 平均暂停时间（ms）
}
```

**StaticInfo 完整字段定义**：

```go
type StaticInfo struct {
    Hostname   string    `json:"hostname"`       // 主机名
    OS         string    `json:"os"`             // "linux" / "windows"
    Arch       string    `json:"arch"`           // "amd64" / "arm64"
    NumCPU     int       `json:"numCpu"`         // 逻辑 CPU 核数
    MemTotalMB uint64    `json:"memTotalMB"`     // 物理内存总量（gopsutil 补充）
    GoVersion  string    `json:"goVersion"`      // Go 运行时版本
    KernelVer  string    `json:"kernelVer"`      // 内核版本（best-effort）
    StartedAt  time.Time `json:"startedAt"`      // Agent 启动时间
}
```

**采集实现要点**：

| 字段 | 实现方式 | 注意事项 |
|------|----------|----------|
| `CPUPercent` | `cpu.Percent(0, false)` | 第一次返回 0，第二次起准确（gopsutil 内部维护基线） |
| `CPUPerCore` | `cpu.Percent(0, true)` | 同上 |
| `LoadAvg*` | `load.Avg()` | Windows 下返回 0，前端可隐藏 |
| `MemTotalMB` / `MemUsedMB` | `mem.VirtualMemory()` | 单位转换：bytes / 1024 / 1024 |
| `SwapUsedMB` | `mem.SwapMemory()` | |
| `ProcessRssMB` | `process.Process.MemoryInfo().RSS` | 物理常驻内存 |
| `ProcessHeapMB` | `runtime.ReadMemStats().HeapAlloc` | Go 运行时堆 |
| `ProcessSysMB` | `runtime.ReadMemStats().Sys` | Go 进程总占用（含栈、堆、运行时数据） |
| `NumGoroutine` | `runtime.NumGoroutine()` | |
| `NumThread` | `process.NumThreads()` | Windows 上是用户态线程数 |
| `NumFD` | `process.NumFDs()` | Windows 上 gopsutil 可能返回错误，写 0 |
| `GCCount` | `runtime.ReadMemStats().NumGC` | |
| `GCPauseAvgMs` | `runtime.ReadMemStats().PauseNs` | 最近 N 次 GC 暂停时间平均值（最多取最近 256 次） |
| `NetSendKBps` / `NetRecvKBps` | `net.IOCounters(false)[0]` 差分 / 时间差 | 第一次只记录基线（`initialized=false`），第二次起才有值 |

**采集频率**：每 `SystemInterval`（默认 5s）采集一次，缓存 `latest`，`Snapshot()` 仅做读锁返回。

**启动流程**：`Start(stopCh)` 先做一次同步采集（建基线），然后启动后台 ticker 循环。

### 4.3 StressReporter / SystemReporter（agent/reporter.go）

#### StressReporter

```go
type StressReporter struct {
    cli      *AdminClient              // Admin HTTP 客户端
    agentID  string                    // Agent UUID
    taskID   string                    // 当前任务 ID
    interval time.Duration             // 推送间隔（默认 5s）
    src      *monitor.MetricsCollector // 指标源

    stopOnce sync.Once                 // 幂等停止
    stopCh   chan struct{}             // 关闭即停止推送循环
}
```

**生命周期**：由 `executeTask` 控制 -- 任务开始时创建并 `Start()`，任务结束时 `Stop()`。

**行为规则**：

- 以 `StressInterval` 为间隔定时推送 `StressReport`（含 `CollectorSnapshot`）
- `Snapshot()` 方法：同步采集当前指标快照，供阶段重置回调使用
- `Stop()` 幂等：`sync.Once` 保护，先做一次同步 flush（5s 超时），确保最后一帧指标已推送
- 推送失败时指数退避（1s -> 2s -> 4s -> ... -> 上限 30s），但 ticker 不停

#### SystemReporter

```go
type SystemReporter struct {
    cli      *AdminClient
    agentID  string
    interval time.Duration
    src      *SystemMonitor

    stopOnce sync.Once
    stopCh   chan struct{}
}
```

**生命周期**：常驻运行 -- 注册成功后启动，shutdown 时停止。空闲期间也持续上报。

**行为规则**：

- 以 `SystemInterval`（= `StressInterval`）为间隔定时推送 `SystemReport`
- 推送失败时指数退避（同 StressReporter，上限 30s）
- `Stop()` 仅关闭 `stopCh`，不做额外 flush

#### 退避算法

```go
func nextBackoff(current, max time.Duration) time.Duration {
    if current == 0 {
        return time.Second  // 首次退避 1s
    }
    next := current * 2     // 指数倍增
    if next > max {
        return max           // 不超过上限（30s）
    }
    return next
}
```

### 4.4 TaskRunner（agent/task_runner.go）

```go
type TaskRunner struct {
    assignment *TaskAssignment           // 任务配置
    cfg        *ResolvedConfig           // Agent 运行期配置
    cli        *AdminClient              // Admin 客户端
    collector  *monitor.MetricsCollector // 指标采集器
    httpCli    *http.Client              // 文件下载专用（5min 超时）
    workDir    string                    // 临时工作目录

    // OnStageReset 渐进式加压阶段重置回调，由 executeTask 注入
    OnStageReset func(completedStageIdx int)
}
```

#### 13 步流水线详解

**步骤 0：临时切换日志等级**

如果 `TaskAssignment.LogLevel` 非空（支持 `debug`/`info`/`warn`/`error`），临时切换 Agent 进程的 zap 日志等级。任务结束时（包括异常路径）通过 `defer` 自动恢复原等级。

**步骤 1：创建临时目录**

```
workDir = {TaskWorkDir}/stressbot-task-{taskID}/
  conf/
    proto/       (proto 文件)
    scripts/     (Lua 脚本)
    adapter/     (适配器脚本，由配置文件列表中携带)
    flow/        (flow.json)
```

`TaskWorkDir` 默认为 `os.TempDir()`。

**步骤 2：从 Admin 下载配置文件**

遍历 `TaskAssignment.ConfigFiles` 列表，拼接 `ConfigURL + "/" + relPath`，逐个 HTTP GET 下载到 `workDir/conf/` 下对应的子目录。任何文件下载失败（HTTP 非 200）即返回 `TaskFailed`。

**步骤 3：加载声明式 codec resolver**

使用任务下发的 `workDir/conf/adapter/*_codec.json` 与共享 `errors.json` 构建 `CodecResolver`，按 `<proto>:<service>` 显式映射到对应的 `SchemaAdapter`。缺少映射或配置非法时 fail loud。

**步骤 4：加载 .proto 文件**

通过 `protox.NewLoader` 扫描 `workDir/conf/proto/` 目录，编译所有 .proto 文件。

**步骤 5：加载流程配置**

读取 `workDir/conf/flow/flow.json`，反序列化为 `engine.TaskFlow`。回填 `ActionDef.Name`（从 map key 反写）。

**步骤 6：重置 MetricsCollector**

调用 `collector.Reset()` 归零所有计数器，**不**调用 `monitor.Init()`（避免替换全局 collector 导致 Reporter 引用失效）。设置 `ApdexT` 从 `TaskAssignment.ApdexT` 获取。

**步骤 7：解析超时参数**

解析 `HeartbeatInterval`（默认 5s）、`TCPTimeout`（默认 60s）、`HTTPTimeout`（默认 10s）。

**步骤 8：启动 gnet 网络引擎**

创建 `network.NewDialer` 并启动事件循环。

**步骤 9：初始化 Lua 运行时池**

创建 `script.NewRuntimePool`，预编译脚本。预编译失败为非致命错误。

**步骤 10：创建 Robot Manager**

构建 `robot.ManagerConfig`：

```go
mgrCfg := robot.ManagerConfig{
    AccountPrefix:  assignment.AccountPrefix,  // 默认 "bot_"
    StartNumber:    assignment.StartNumber,
    Count:          assignment.TotalBots,
    ConcurrentNum:  assignment.ConcurrentNum,
    StateExtra:     assignment.StateExtra,      // 额外状态注入
    Adapter:        adp,
    RequestTimeout: tcpTimeout,
    MainService:    assignment.MainService,
    HTTPTimeout:    httpTimeout,
}
```

如果有 `RampUp` 配置，转换阶段参数并注入 `OnStageReset` 回调。

**步骤 11：启动机器人**

- 有 RampUp 配置：调用 `mgr.StartWithRampUp()`
- 无 RampUp 配置：调用 `mgr.StartAll()`

**步骤 12：等待完成**

```go
select {
case <-ctx.Done():        // 任务被取消（Admin stop / Agent shutdown / 心跳失败）
case <-mgr.Done():        // 所有机器人退出（含运行时长到期）
}
```

**步骤 13：停止所有机器人**

调用 `mgr.StopAll()`，根据 ctx 错误判断结果：
- `context.Canceled` -> `TaskStopped`
- 其他 -> `TaskCompleted`

#### OnStageReset 回调

当 Manager 完成一个 `Reset: true` 的 RampUp 阶段时触发，由 `Agent.executeTask` 注入：

1. 获取当前指标快照：`stressReporter.Snapshot()`
2. 发送阶段完成报告：`ReportTaskDone(TaskCompletionReport{StageIndex, FinalSnapshot})`
3. 重置采集器：`collector.Reset()`

#### 任务清理

`Cleanup()` 删除整个临时目录（`os.RemoveAll`），失败仅记录 WARN。

### 4.5 HTTPServer（agent/http_server.go）

监听 `cfg.Port`（默认 7719），所有 handler 被 `recoverMiddleware` 包裹（捕获 panic 返回 500 JSON）。

**完整端点列表**：

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|------|------|--------|------|------|
| `POST` | `/agent/v1/task` | `TaskAssignment` JSON | `202 Accepted` 或 `409 Conflict` | Admin 推送任务。忙碌时返回 409（含 `currentTaskId`），否则立即 202 后异步执行 |
| `POST` | `/agent/v1/stop` | 无 | `200 OK` 或 `409 Conflict` | 取消当前任务。无任务时返回 409 |
| `POST` | `/agent/v1/shutdown` | 无 | `202 Accepted` | 远程关闭 Agent 进程 |
| `GET` | `/agent/v1/version` | 无 | `{"version":"v1.2.0"}` | 查询版本号 |
| `GET` | `/agent/v1/status` | 无 | `AgentStatusResponse` JSON | 状态查询（id/status/taskId/uptime） |
| `GET` | `/agent/v1/logs` | `?afterSeq=N&limit=M` | `QueryResult` JSON | 环形缓冲区日志查询（limit 默认 200，上限 500）。缓冲区未启用返回 503 |
| `GET` | `/agent/v1/logs/files` | 无 | `[]LogFileInfo` JSON | 列出本地日志文件（name/size/modTime） |
| `GET` | `/agent/v1/logs/files/{name}` | 无 | `text/plain` 附件 | 下载指定日志文件（防路径遍历：禁止 `/`、`\`、`..`） |
| `GET` | `/healthz` | 无 | `200 OK` | 健康检查探活 |

**recoverMiddleware**：

```go
func recoverMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rec := recover(); rec != nil {
                // 记录 panic + stack trace 到应用日志
                // 返回标准 500 JSON: {"code":"STATUS_500","message":"internal server error"}
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

**任务分配流程**：

收到 `POST /agent/v1/task` -> 持锁检查 `currentTask` -> 忙碌返回 409 -> 否则立即 202 -> `taskWG.Add(1)` -> 协程池异步执行 `executeTask`。

注意 `taskWG.Add(1)` 在协程外调用，避免与 shutdown 的 `taskWG.Wait()` 形成竞态。

### 4.6 AdminClient（agent/http_client.go）

```go
type AdminClient struct {
    base    string        // "http://admin:7718"
    agentID string        // Agent UUID（注册时设置）
    client  *http.Client  // 共享 HTTP 客户端（Timeout 由 ResolvedConfig.RequestTimeout 控制）
}
```

#### RPC 方法完整列表

| 方法 | HTTP 方法 | 路径 | 请求体 | 成功响应 | 说明 |
|------|-----------|------|--------|----------|------|
| `Register` | POST | `/sbot/agent/register` | `RegisterRequest` | `200 + RegisterResponse` | 注册到 Admin |
| `Heartbeat` | POST | `/sbot/agent/{id}/heartbeat` | `HeartbeatRequest` | `200` | 心跳。404 返回 `errNotRegistered` |
| `PostStress` | POST | `/sbot/agent/stress` | `StressReport` | `200/202` | 上报压测指标 |
| `PostSystem` | POST | `/sbot/agent/system` | `SystemReport` | `200/202` | 上报系统指标 |
| `FetchPendingTask` | GET | `/sbot/agent/{id}/pending-task` | 无 | `200 + TaskAssignment` 或 `204 No Content` | 拉取待执行任务 |
| `ReportTaskDone` | POST | `/sbot/agent/{id}/task/{taskId}/done` | `TaskCompletionReport` | `200/202` | 上报任务完成 |
| `Deregister` | POST | `/sbot/agent/{id}/deregister` | `DeregisterRequest` | `200` | 注销（best-effort） |
| `DownloadFile` | GET | (任意 URL) | 无 | 文件流 | 通用文件下载到 io.Writer |

#### 设计要点

- 单一共享 `http.Client`，`RequestTimeout`（默认 30s）作为单次请求超时上限
- 调用方 `ctx` 取消（如 Agent shutdown）能立刻打断阻塞中的请求
- `errNotRegistered` 哨兵错误：Admin 返回 404 时识别（Admin 重启场景），触发重新注册
- 所有 POST 请求共用 `doPost()` 辅助方法，统一设置 `Content-Type: application/json`

#### 重试策略

**注册重试**：使用 `RetryWithRetriesAndBackoff`，参数：
- `initial` = `ReconnectInterval`（默认 5s）
- `max` = `ReconnectMaxInterval`（默认 60s）
- `maxRetries` = `ReconnectMaxRetries`（默认 -1 = 永不放弃）

```go
func RetryWithRetriesAndBackoff(ctx context.Context, op func() error,
    initial, max time.Duration, maxRetries int, desc string) error
```

- `maxRetries < 0`：无限重试（直到 ctx 取消）
- `maxRetries == 0`：当成 1 次（仅尝试一次，不重试）
- `maxRetries > 0`：最多重试 maxRetries 次

**指标上报重试**：Reporter 内部通过 `nextBackoff()` 实现，失败时指数退避（上限 30s），成功后重置。

**其他操作**：`FetchPendingTask` 失败直接返回，下次轮询再试；`Deregister` 不重试（best-effort）。

## 5. 协议契约

### 5.1 Agent -> Admin

#### 5.1.1 注册

```http
POST /sbot/agent/register
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "name": "agent-gz-01",
  "address": "http://192.168.1.200:7719",
  "appVersion": "v1.2.0",
  "maxBots": 5000,
  "stressInterval": "5s",
  "systemInterval": "5s",
  "staticInfo": {
    "hostname": "gz-stress-01",
    "os": "linux",
    "arch": "amd64",
    "numCpu": 16,
    "memTotalMB": 32768,
    "goVersion": "go1.23.4",
    "kernelVer": "",
    "startedAt": "2026-04-29T10:00:00+08:00"
  }
}
```

响应 `200 OK`：

```json
{
  "agentId": "uuid-xxx",
  "heartbeatTtl": "30s",
  "stressEndpoint": "/sbot/agent/stress",
  "systemEndpoint": "/sbot/agent/system"
}
```

#### 5.1.2 心跳

```http
POST /sbot/agent/{agentId}/heartbeat
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "timestamp": "2026-04-29T10:30:00+08:00",
  "status": "idle",
  "currentTaskId": "",
  "currentBots": 0,
  "appVersion": "v1.2.0"
}
```

响应 `200 OK`。`404` 表示 Agent 在 Admin 侧不存在，触发重新注册。

#### 5.1.3 上报压测指标

```http
POST /sbot/agent/stress
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "taskId": "task-01",
  "reportedAt": "2026-04-29T10:30:05+08:00",
  "snapshot": { /* monitor.CollectorSnapshot */ }
}
```

响应 `200` 或 `202`。

#### 5.1.4 上报系统指标

```http
POST /sbot/agent/system
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "reportedAt": "2026-04-29T10:30:05+08:00",
  "snapshot": { /* SystemSnapshot（17 字段 + Timestamp） */ }
}
```

响应 `200` 或 `202`。

#### 5.1.5 任务完成

```http
POST /sbot/agent/{agentId}/task/{taskId}/done
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "taskId": "task-01",
  "result": "completed",
  "errorMsg": "",
  "finishedAt": "2026-04-29T10:35:00+08:00",
  "finalSnapshot": { /* 最后一次完整压测快照 */ },
  "stageIndex": 0
}
```

- `result`：`completed` / `stopped` / `failed`
- `errorMsg`：仅 `result=failed` 时填写
- `stageIndex`：仅阶段完成报告时填写（OnStageReset 回调使用）
- `finalSnapshot`：任务结束时的完整指标快照

**finalSnapshot 采集流程**：

1. `StressReporter.Stop()` 同步 flush 最后一帧指标（5s 超时）
2. `collector.Snapshot(nil, 0)` 采集最终快照
3. 使用 `context.Background()` + `TaskReportTimeout`（30s）上报，脱离已取消的 taskCtx
4. 上报失败仅记录 WARN（Admin 会通过心跳超时合成 offline report）

#### 5.1.6 拉取待执行任务

```http
GET /sbot/agent/{agentId}/pending-task
```

响应：
- `200 OK` + `TaskAssignment` JSON：有任务
- `204 No Content`：当前无任务

#### 5.1.7 注销

```http
POST /sbot/agent/{agentId}/deregister
Content-Type: application/json

{"agentId": "uuid-xxx"}
```

best-effort，失败不重试。

### 5.2 Admin -> Agent

#### 5.2.1 任务下发

```http
POST http://agent:7719/agent/v1/task
Content-Type: application/json

{
  "taskId": "task-01",
  "taskName": "200v200 压测",
  "totalBots": 3000,
  "startNumber": 10000,
  "accountPrefix": "bot_",
  "concurrentNum": 50,
  "mainService": "logic",
  "stateExtra": {"gameId": "1"},
  "heartbeatInterval": "5s",
  "tcpTimeout": "60s",
  "httpTimeout": "10s",
  "apdexT": 100,
  "logLevel": "",
  "configUrl": "http://admin:7718/api/tasks/task-01/config",
  "configFiles": [
    "flow.json",
    "proto/c2s.proto",
    "scripts/battle.lua",
    "adapter/tcp_logic_codec.json",
    "adapter/errors.json"
  ],
  "rampUp": {
    "stages": [
      {"count": 1000, "concurrency": 20, "holdSec": 60, "reset": false},
      {"count": 3000, "concurrency": 50, "holdSec": 0, "reset": true}
    ]
  }
}
```

响应：
- `202 Accepted`：已接受（异步执行）
- `409 Conflict`：当前已有任务（Body 含 `currentTaskId`）

#### 5.2.2 停止任务

```http
POST http://agent:7719/agent/v1/stop
```

响应 `200 OK`，Agent 异步取消任务 ctx。无任务运行时返回 `409 Conflict`。

#### 5.2.3 远程关闭

```http
POST http://agent:7719/agent/v1/shutdown
```

响应 `202 Accepted`，Agent 触发完整 shutdown 流程后进程退出。

## 6. 心跳机制

### 6.1 心跳循环（heartbeatLoop）

独立 goroutine，通过协程池调度。

```
heartbeatLoop:
  timer(HBInterval)
  loop:
    select:
    case ctx.Done():   return
    case stopCh:       return
    case timer.C:
      构造 HeartbeatRequest（含当前 status/taskID/bots）
      err := cli.Heartbeat(ctx, req)
      if err == nil:
        重置 consecutiveFailures = 0
        interval = HBInterval（正常间隔）
        timer.Reset(interval)
        continue

      if err == errNotRegistered (404):
        if busy: cancelCurrentTask("Admin 报告未注册")
        registerWithRetry()  // 重新注册
        if 注册失败: triggerStop() + return
        regGeneration++
        interval = HBInterval
        timer.Reset(interval)
        continue

      consecutiveFailures++
      if busy && consecutiveFailures >= HBFailThreshold:
        cancelCurrentTask("心跳失败 / Admin 断联")
      记录日志（前 3 次 WARN，之后 ERROR）

      interval = HBFailInterval（失败重试间隔）
      timer.Reset(interval)
```

### 6.2 行为规则

1. **成功时用 HBInterval**（默认 10s），**失败时用 HBFailInterval**（与 HBInterval 一致，即快速重试）
2. **404 立即重注册**：Admin 返回 404 表示 Agent 在 Admin 侧不存在（Admin 重启或主动注销），立即取消任务并重新注册
3. **任务运行中连续 `HBFailThreshold` 次心跳失败才取消任务**（默认 3 次 × HBInterval 10s = 30s 容忍窗口）：单次 dial 抖动（如测试环境本地 ephemeral port 瞬时阻塞）不应误伤压测任务；持续断联到达阈值才视为 Admin 真正不可达，此时取消任务（Admin 是唯一的指标聚合点，断联后压测流量没有观测价值）
4. **持续失败不退进程**：除非重新注册超出 `ReconnectMaxRetries`
5. **重新注册成功后递增 `regGeneration`**：避免旧任务的回调到新生命周期里污染状态

### 6.3 心跳请求字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `agentId` | string | Agent UUID |
| `timestamp` | string | RFC3339 格式时间戳 |
| `status` | string | `"idle"` 或 `"busy"` |
| `currentTaskId` | string | status=busy 时为当前任务 ID，否则为空 |
| `currentBots` | int | 当前实际启动的机器人数 |
| `appVersion` | string | 应用版本号 |

## 7. 任务执行细节

### 7.1 executeTask 流程

```go
func (a *Agent) executeTask(parentCtx context.Context, task *TaskAssignment)
```

1. **保护性 recover**：任务内 panic 不影响 Agent 主循环
2. **状态机迁移**：idle -> busy（持锁），结束时 busy -> idle（defer）
3. **创建任务 context**：`context.WithCancel(parentCtx)`，存入 `a.taskCancel`
4. **创建 StressReporter**：注入 agentID/taskID/interval/collector，启动推送循环
5. **创建 TaskRunner**：注入任务配置 + OnStageReset 回调
6. **Runner.Run(ctx)**：阻塞执行 13 步流水线
7. **StressReporter.Stop()**：同步 flush 最后一帧指标
8. **采集 finalSnapshot**：`collector.Snapshot(nil, 0)`
9. **上报任务完成**：使用 `context.Background()` + 30s 超时（脱离已取消的 taskCtx）
10. **runner.Cleanup()**：删除临时目录

**上报失败处理**：仅记录 WARN，不阻塞。Admin 会通过心跳超时自动合成 offline report。

**任务互斥**：持锁检查 `currentTask != nil`，已存在任务时忽略新任务。

### 7.2 临时目录布局

```
{os.TempDir()}/stressbot-task-{taskID}/
  conf/
    flow/
      flow.json
    proto/
      c2s.proto
      s2c.proto
    scripts/
      battle.lua
      heartbeat.lua
    adapter/
      tcp_logic_codec.json
      errors.json        (可选)
```

任务结束（completed / stopped / failed）后立即删除整个目录。

### 7.3 任务结果判断

| 条件 | 结果 |
|------|------|
| `ctx.Err() == context.Canceled` | `TaskStopped` |
| 其他（包括 `mgr.Done()` 正常退出） | `TaskCompleted` |
| 流水线中任何步骤返回错误 | `TaskFailed` + 错误消息 |

### 7.4 渐进式加压（RampUp）支持

`TaskAssignment` 可选包含 `RampUp` 配置：

```go
type RampUpConfig struct {
    Stages []RampUpStage `json:"stages"`
}

type RampUpStage struct {
    Count       int  `json:"count"`                // 本阶段机器人总数
    Concurrency int  `json:"concurrency,omitempty"` // 并发启动数
    HoldSec     int  `json:"holdSec,omitempty"`     // 保持时长（秒）
    Reset       bool `json:"reset,omitempty"`       // 是否重置采集器
}
```

当 `Reset: true` 的阶段完成时，通过 `OnStageReset` 回调上报阶段指标并重置采集器。

## 8. 优雅退出

### 8.1 shutdown() 完整步骤

```go
func (a *Agent) shutdown() error
```

**6 步关闭流程**（设计原则：先停任务 -> 等上报 -> 停常驻 -> 注销 -> 关 HTTP -> 关池）：

**步骤 1：停止当前任务 + 等待上报**

```
cancelCurrentTask("agent shutdown")
taskWG.Wait()  // 最长等待 TaskReportTimeout + 5s
```

- `cancelCurrentTask` 取消任务的 ctx，触发 TaskRunner 的 `mgr.StopAll()`
- `taskWG.Wait()` 确保上报完成（executeTask 中上报使用 `context.Background()`，不会被 ctx cancel 中断）
- 超时保护：`TaskReportTimeout + 5s`（默认 35s）后放弃等待

**步骤 2：停止 Reporter**

```
stressReporter.Stop()    // 通常已被 executeTask 关掉，此处保护性调用
sysReporter.Stop()
```

**步骤 3：取消全局 ctx**

```
a.cancel()   // 心跳/轮询/上报循环退出
```

**步骤 4：注销（best-effort）**

```
Deregister(context.Background(), 5s timeout)
```

失败仅记录 WARN。

**步骤 5：关闭 HTTP 服务器**

```
httpSrv.Shutdown(5s timeout)
```

**步骤 6：关闭协程池**

```
utils.GetWorkPool().Shutdown()
```

等待池中所有 goroutine 完成或超时。

### 8.2 触发方式

| 方式 | 触发路径 |
|------|----------|
| SIGINT / SIGTERM | `Run()` 的 `sigCh` 分支 -> `shutdown()` |
| `POST /agent/v1/shutdown` | `handleShutdown` -> `triggerStop()` -> `Run()` 的 `stopCh` 分支 -> `shutdown()` |
| 注册重试耗尽 | `registerWithRetry` 返回错误 -> `triggerStop()` |

`triggerStop()` 通过 `sync.Once` 保证 `stopCh` 仅关闭一次，可安全并发调用。

## 9. 配置文件完整参考

### 9.1 全局 Config（cmd/agent/main.go）

```go
type Config struct {
    Log        *stresslog.Config       `json:"log"`
    Monitor    monitor.CollectorConfig `json:"monitor"`
    Standalone *StandaloneConfig       `json:"standalone"`
    Agent      agent.Config            `json:"agent"`
    Daemon     bool                    `json:"daemon"` // 守护进程模式（仅 Linux）
}
```

### 9.2 agent.Config 完整字段（agent/config.go）

| 字段 | JSON 键 | 类型 | 默认值 | 说明 |
|------|---------|------|--------|------|
| `Enabled` | `enabled` | bool | `false` | Agent 模式总开关 |
| `AdminAddr` | `adminAddr` | string | 无（必填） | Admin HTTP 地址，含 schema（如 `http://192.168.1.100:7718`） |
| `PublicURL` | `publicUrl` | string | 自动获取 | Agent 对外可达地址（如 `http://192.168.1.200:7719`）。为空时自动获取本机出口 IP |
| `Port` | `port` | int | `7719` | 本地 HTTP 监听端口 |
| `MaxBots` | `maxBots` | int | `5000` | 单节点最大机器人数 |
| `HBInterval` | `hbInterval` | string | `"10s"` | 心跳发送间隔 |
| `HBRequestTimeout` | `hbRequestTimeout` | string | `"5s"` | 单次心跳请求超时，独立于 RequestTimeout，超过 RequestTimeout 时会被截断 |
| `HBFailThreshold` | `hbFailThreshold` | int | `3` | 任务运行中连续心跳失败多少次才取消任务（容忍 ephemeral port 等瞬时抖动） |
| `RequestTimeout` | `requestTimeout` | string | `"30s"` | 单次 HTTP 请求超时（注册/上报/拉任务/下载等） |
| `ReconnectMaxRetries` | `reconnectMaxRetries` | int | `-1`（无限） | 注册重试次数。-1=持续重连，0=视为未配置回退-1 |
| `StressInterval` | `stressInterval` | string | `"5s"` | 压测指标上报间隔 |
| `AppVersion` | (不序列化) | string | `"dev"` | 应用版本号，编译时 `-ldflags` 注入 |

### 9.3 ResolvedConfig 派生参数（agent/config.go）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `AdminAddr` | string | 从 Config 透传 | |
| `Name` | string | `os.Hostname()` | 节点名称 |
| `Address` | string | `http://{本机IP}:{port}` | Agent 对外地址 |
| `Port` | int | `7719` | |
| `MaxBots` | int | `5000` | |
| `AppVersion` | string | `"dev"` | |
| `TaskWorkDir` | string | `os.TempDir()` | 任务临时目录根 |
| `StressInterval` | Duration | `5s` | |
| `SystemInterval` | Duration | = StressInterval | 与压测指标同步 |
| `HBInterval` | Duration | `10s` | |
| `HBFailInterval` | Duration | = HBInterval | 失败重试间隔 |
| `HBRequestTimeout` | Duration | `5s` | 单次心跳请求超时，min(配置值, RequestTimeout) |
| `HBFailThreshold` | int | `3` | 任务运行中容忍的连续心跳失败次数 |
| `RequestTimeout` | Duration | `30s` | |
| `ReconnectInterval` | Duration | `5s` | 注册重连初始间隔 |
| `ReconnectMaxInterval` | Duration | `60s` | 重连退避上限 |
| `ReconnectMaxRetries` | int | `-1` | |
| `TaskReportTimeout` | Duration | `30s` | 任务完成上报总超时 |

### 9.4 配置文件示例

```json
{
  "log": {
    "path": "log/agent.log",
    "level": "info",
    "printConsole": false,
    "maxSize": 100,
    "maxBackups": 5,
    "maxAge": 30,
    "compress": false
  },
  "monitor": {
    "enabled": true,
    "apdexT": 100
  },
  "agent": {
    "enabled": true,
    "adminAddr": "http://192.168.1.100:7718",
    "port": 7719,
    "maxBots": 5000,
    "hbInterval": "10s",
    "stressInterval": "5s"
  }
}
```

### 9.5 单机模式配置示例

```json
{
  "log": {
    "path": "log/stressbot.log",
    "level": "info"
  },
  "monitor": {
    "enabled": true,
    "reportInterval": "5s",
    "httpEnabled": false,
    "httpPort": 6060,
    "csvPath": "log/metrics.csv",
    "apdexT": 100
  },
  "standalone": {
    "bot": {
      "accountPrefix": "bot_",
      "startNumber": 1,
      "count": 100,
      "concurrentNum": 10,
      "mainService": "logic"
    },
    "duration": "10m"
  },
  "agent": {
    "enabled": false
  }
}
```

## 10. 类型定义（agent/types.go）

### 10.1 状态枚举

```go
type AgentStatus string

const (
    StatusIdle AgentStatus = "idle"
    StatusBusy AgentStatus = "busy"
)
```

### 10.2 任务结果枚举

```go
type TaskResult string

const (
    TaskCompleted TaskResult = "completed"
    TaskStopped   TaskResult = "stopped"
    TaskFailed    TaskResult = "failed"
)
```

### 10.3 Agent -> Admin 请求类型

#### RegisterRequest

```go
type RegisterRequest struct {
    AgentID        string     `json:"agentId"`
    Name           string     `json:"name"`
    Address        string     `json:"address"`
    AppVersion     string     `json:"appVersion"`
    MaxBots        int        `json:"maxBots"`
    StressInterval string     `json:"stressInterval"`
    SystemInterval string     `json:"systemInterval"`
    StaticInfo     StaticInfo `json:"staticInfo"`
}
```

#### RegisterResponse

```go
type RegisterResponse struct {
    AgentID        string `json:"agentId"`
    HeartbeatTTL   string `json:"heartbeatTtl"`
    StressEndpoint string `json:"stressEndpoint"`
    SystemEndpoint string `json:"systemEndpoint"`
}
```

#### HeartbeatRequest

```go
type HeartbeatRequest struct {
    AgentID       string `json:"agentId"`
    Timestamp     string `json:"timestamp"`
    Status        string `json:"status"`        // idle | busy
    CurrentTaskID string `json:"currentTaskId"` // status=busy 时有值
    CurrentBots   int    `json:"currentBots"`
    AppVersion    string `json:"appVersion"`
}
```

#### StressReport

```go
type StressReport struct {
    AgentID    string                     `json:"agentId"`
    TaskID     string                     `json:"taskId"`
    ReportedAt time.Time                  `json:"reportedAt"`
    Snapshot   *monitor.CollectorSnapshot `json:"snapshot"`
}
```

#### SystemReport

```go
type SystemReport struct {
    AgentID    string         `json:"agentId"`
    ReportedAt time.Time      `json:"reportedAt"`
    Snapshot   SystemSnapshot `json:"snapshot"`
}
```

#### TaskCompletionReport

```go
type TaskCompletionReport struct {
    AgentID       string                     `json:"agentId"`
    TaskID        string                     `json:"taskId"`
    Result        TaskResult                 `json:"result"`
    ErrorMsg      string                     `json:"errorMsg,omitempty"`
    FinishedAt    time.Time                  `json:"finishedAt"`
    FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
    StageIndex    int                        `json:"stageIndex,omitempty"`
}
```

#### DeregisterRequest

```go
type DeregisterRequest struct {
    AgentID string `json:"agentId"`
}
```

### 10.4 Admin -> Agent 请求类型

#### TaskAssignment

```go
type TaskAssignment struct {
    TaskID            string            `json:"taskId"`
    TaskName          string            `json:"taskName"`
    StartNumber       int               `json:"startNumber"`
    TotalBots         int               `json:"totalBots"`
    AccountPrefix     string            `json:"accountPrefix"`
    ConcurrentNum     int               `json:"concurrentNum"`
    MainService       string            `json:"mainService"`
    StateExtra        map[string]string `json:"stateExtra"`
    HeartbeatInterval string            `json:"heartbeatInterval"`
    TCPTimeout        string            `json:"tcpTimeout"`
    HTTPTimeout       string            `json:"httpTimeout"`
    ApdexT            int               `json:"apdexT"`
    LogLevel          string            `json:"logLevel,omitempty"`
    ConfigURL         string            `json:"configUrl"`
    ConfigFiles       []string          `json:"configFiles"`
    RampUp            *RampUpConfig     `json:"rampUp,omitempty"`
}
```

#### RampUpConfig / RampUpStage

```go
type RampUpConfig struct {
    Stages []RampUpStage `json:"stages"`
}

type RampUpStage struct {
    Count       int  `json:"count"`
    Concurrency int  `json:"concurrency,omitempty"`
    HoldSec     int  `json:"holdSec,omitempty"`
    Reset       bool `json:"reset,omitempty"`
}
```

### 10.5 响应类型

#### AgentStatusResponse

```go
type AgentStatusResponse struct {
    AgentID       string `json:"agentId"`
    Status        string `json:"status"`
    CurrentTaskID string `json:"currentTaskId,omitempty"`
    AppVersion    string `json:"appVersion"`
    Uptime        string `json:"uptime"`
}
```

#### ErrorResponse

```go
type ErrorResponse struct {
    Code    string          `json:"code"`
    Message string          `json:"message"`
    Details json.RawMessage `json:"details,omitempty"`
}
```

### 10.6 系统监控类型

详见第 4.2 节 `SystemSnapshot` 和 `StaticInfo`。

## 11. 错误处理与重试策略

### 11.1 网络故障

| 操作 | 失败处理 |
|------|----------|
| 注册失败 | 指数退避（5s -> 10s -> 20s -> ... -> 60s 上限），默认永不放弃（`ReconnectMaxRetries=-1`） |
| 心跳失败 | 使用 `HBFailInterval` 快速重试；404 立即重注册；任务运行中累计达到 `HBFailThreshold`（默认 3）次才取消任务 |
| 上报压测指标失败 | 指数退避（1s -> 2s -> 4s -> ... -> 30s 上限），ticker 不停 |
| 上报系统指标失败 | 同上 |
| 拉取配置失败 | 立即返回 `TaskFailed`，错误信息含 URL |
| 上报 task done 失败 | 一次性上报（30s 超时），失败仅记录 WARN，由 Admin 心跳超时自动收尾 |
| 注销失败 | best-effort，不重试 |

### 11.2 资源失败

| 场景 | 处理 |
|------|------|
| 临时目录创建失败 | 返回 `TaskFailed` |
| 配置文件下载失败 | 返回 `TaskFailed`（错误信息含文件路径和 HTTP 状态码） |
| proto 加载失败 | 返回 `TaskFailed` |
| flow.json 解析失败 | 返回 `TaskFailed` |
| 适配器加载失败 | 返回 `TaskFailed` |
| 网络引擎启动失败 | 返回 `TaskFailed` |
| 机器人数启动失败 | 返回 `TaskFailed` |
| SystemMonitor 创建失败 | Agent 拒绝启动（`agent.New` 返回 error） |
| Lua 脚本预编译失败 | 非致命错误（仅 WARN），继续执行 |

### 11.3 并发安全

- `Agent.mu`（`sync.Mutex`）保护 `status`、`currentTask`、`taskCancel`、`stressReporter` 的读写
- `SystemMonitor.mu`（`sync.RWMutex`）保护 `latest` 快照，`Snapshot()` 只做读锁
- `stopOnce`（`sync.Once`）保证 `stopCh` 仅关闭一次
- `taskWG`（`sync.WaitGroup`）追踪 `executeTask` 生命周期，`shutdown` 时 Wait
- 所有业务 goroutine 通过 `utils.GetWorkPool()` 调度，自带 panic recover

## 12. 日志系统

### 12.1 日志初始化

- Agent 模式：`log/agent.log`，tag `"agent"`
- 单机模式：`log/stressbot.log`，tag `"stressbot"`

### 12.2 环形缓冲区

通过 `logview.AttachRingBuffer` 挂接到全局 zap logger，容量 50000 条。所有经过 zap 输出的日志都会被 O(1) 追加到环形缓冲区。

### 12.3 日志查询接口

`GET /agent/v1/logs` 支持游标分页：

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `afterSeq` | uint64 | 0 | 游标：从此序列号之后开始查询 |
| `limit` | int | 200 | 返回条数（上限 500） |

返回 `logview.QueryResult`（含 `entries`、`hasMore`、`nextSeq`）。

### 12.4 日志文件管理

- `GET /agent/v1/logs/files`：扫描日志目录下同前缀的所有文件
- `GET /agent/v1/logs/files/{name}`：下载指定日志文件（防路径遍历攻击）

## 13. 辅助工具函数

### 13.1 UUID 生成

```go
func generateUUID() string
```

使用 `crypto/rand` 生成 v4 UUID（不依赖外部库）。格式：`xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`。失败时 fallback 为 `{timestamp}-{pid}`。

### 13.2 本机 IP 获取

```go
func getLocalIP() string
func buildAddress(port int) string
```

通过 UDP 连接 `8.8.8.8:53`（不实际发包）获取本机首选出口 IP。失败时回退 `127.0.0.1`。`buildAddress` 拼接为 `http://{ip}:{port}`。

### 13.3 静态信息采集

```go
func CollectStaticInfo() StaticInfo
```

采集 hostname、OS、arch、NumCPU、GoVersion、启动时间。`MemTotalMB` 在 `agent.New` 中通过 `gopsutil` 补充。

### 13.4 Duration 解析

使用 `utils.ParseDurationDefault(s, default, name)` 解析 Go duration 格式字符串，解析失败时使用默认值。

## 14. 与计划方案的差异

以下功能在实施计划（`plans/agent-implementation.md`）中设计但**未实现**：

### 14.1 已废弃：热更新/升级

- 计划中的 `agent/upgrader.go` 未创建
- HTTP 端点 `/agent/v1/upgrade` 未注册
- `Launcher` 守护进程、`.upgrade.pending` / `.upgrade.success` / `.bak` 文件 IPC 协议均未实现
- 升级改为运维手动重启 Agent 进程

### 14.2 已废弃：任务完成上报长时间重试

- 计划要求 finalSnapshot 上报失败时以指数退避重试最多 30 分钟，重试期间维持 `state=draining` 不接受新任务
- 实际实现为**一次性上报**（30s 超时），失败仅记录 WARN。Admin 通过心跳超时合成 offline report 作为兜底

### 14.3 已简化：SHA256 校验

- 计划要求下载配置文件后校验 SHA256
- `TaskAssignment` 中无 `sha256` 字段，下载后不做校验

### 14.4 配置字段差异

| 计划字段 | 实际实现 | 差异说明 |
|----------|----------|----------|
| `agent.name` | 自动取 `os.Hostname()` | 未暴露为配置项，通过 `ResolvedConfig.Name` 自动填充 |
| `agent.listenAddr` | `agent.port` | 改为仅指定端口，地址自动构建 |
| `agent.systemInterval` | = `stressInterval` | 独立参数合并，与压测指标同步上报 |
| `agent.heartbeatFailInterval` | = `hbInterval` | 独立参数合并，失败重试用相同间隔 |
| `agent.taskWorkDir` | 硬编码 `os.TempDir()` | 未暴露为配置项 |
| codec resolver | 任务下发的 `adapter/*_codec.json` + `errors.json` | 不再暴露单脚本配置项 |

### 14.5 未实现：多任务并发

- 严格执行单任务约束（`currentTask != nil` 时拒绝新任务）
- 无任务队列

### 14.6 未实现：持久任务队列

- 任务仅在内存中，Agent 崩溃时丢失最终报告
- Admin 通过心跳超时合成 offline report 作为兜底

### 14.7 新增功能（计划中未设计）

| 功能 | 说明 |
|------|------|
| `POST /agent/v1/shutdown` | 远程关闭 Agent 进程（计划中未列出） |
| `regGeneration` | 注册版本号，防止旧任务回调污染新生命周期 |
| `LogLevel` 任务级切换 | 任务执行期间临时切换 Agent 日志等级 |
| `OnStageReset` 回调 | 渐进式加压阶段重置时上报阶段指标 |
| `Daemon` 模式 | `-d` 标志或配置 `"daemon": true` 启动守护进程 |
| `StateExtra` 注入 | 任务下发时注入额外状态键值对到每个 Robot |
| `errors.json` 可选加载 | 适配器支持共享错误码映射 JSON |
| `Duration` 运行时长 | 单机模式支持配置运行时长（如 `"10m"`） |

## 15. 与 Admin 的协议契约

### 15.1 Agent 必须遵守

1. 注册请求必须包含完整的 `StaticInfo`
2. 心跳间隔必须 < Admin 的 `unhealthy` 阈值（通常 30s）
3. 任务完成后必须上报 `done`（含 `finalSnapshot`）
4. 同一时刻只能有一个活跃任务
5. `finalSnapshot` 中的 `CollectorSnapshot` 必须与 `monitor` 包当前结构完全一致

### 15.2 Agent 假定 Admin 提供

1. `/sbot/agent/*` 系列端点可用
2. 任务配置文件的 URL（`configUrl + "/" + relPath`）公开可下载
3. Admin 注册响应中给出的 `heartbeatTtl` 是 unhealthy 阈值参考

### 15.3 跨模块字段对齐

Agent 上报的 `CollectorSnapshot` 必须与 `monitor` 包当前结构完全一致（含 `LatencyBuckets`、`LatencySumNs`、`ApdexSatisfied`、`ApdexTolerating` 等聚合字段）。任何字段变动需同时修改 Admin Aggregator。
