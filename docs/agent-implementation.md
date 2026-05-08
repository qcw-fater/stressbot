# Agent 实施方案

> **角色定位**：Agent 是分布式压测系统中的**业务执行节点**，负责注册到 Admin、接收任务、驱动机器人压测、采集压测/系统指标、上报 Admin。Agent 进程被 Launcher 守护，通过文件 IPC 协作完成升级。
> **本文档目标读者**：负责 `agent/` 包及 `cmd/agent/` 入口的开发者。
> **前置阅读**：`docs/design-distributed-master.md` §10 「Agent 设计」、§7 「压测指标聚合」、§8 「Agent 系统监控」、§15 「热更新」。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- 业务进程二进制：`agent.exe`（Linux：`agent`），来自 `cmd/agent`
- 单机模式：`agent.enabled=false`，行为与改造前完全一致
- Agent 模式：`agent.enabled=true`，注册到 Admin 等待任务下发
- **Admin 主文档** = `docs/admin-implementation.md`，所有协议契约可双向交叉验证

## 1. 模块职责

Agent 进程是 stressbot 的业务主体，分为单机模式和 Agent 模式：

### 单机模式（agent.enabled=false）

完全保持现有行为：直接加载本地 `flow.json` → 创建 `robot.Manager` → 启动机器人 → 等待 Ctrl+C。

### Agent 模式（agent.enabled=true）

- 启动时仅初始化基础设施（监控、HTTP 服务器、SystemMonitor），**不**创建 `robot.Manager`
- 向 Admin 发起注册（携带本机静态信息：Hostname、CPU 核数、内存总量、Go 版本、AppVersion、OS/Arch）
- 启动心跳循环（10s 一次），心跳失败时指数退避重连
- 启动**系统指标推送循环**（5s 一次，独立于任务）
- 启动**任务轮询**或被动接受 Admin Push（推 + 拉双通道，详 §3.3）
- 收到任务下发后：从 Admin 拉取配置 → 写入临时目录 → `robot.Manager.Run` → 启动**压测指标推送循环**
- 收到停止指令或机器人全部退出后：上报 `done` → 清理临时目录 → 回到 idle
- 收到升级指令：下载 → 校验 SHA256 → drain → 写 `.upgrade.pending` → `os.Exit(99)`，由 Launcher 完成替换

## 2. 包结构与文件清单

### 2.1 新增包

```
agent/
  agent.go         — Agent 主结构、生命周期、注册/心跳
  config.go        — AgentConfig 解析 + 校验
  sysmon.go        — SystemMonitor：基于 gopsutil 采集
  reporter.go      — StressReporter / SystemReporter 推送循环
  task_runner.go   — 任务执行：拉配置、写临时目录、起 Manager
  upgrader.go      — 升级处理：下载、校验、drain、Exit(99)
  http_server.go   — Agent HTTP 服务（接收 Admin 命令）
  http_client.go   — 与 Admin 通信的 client（带重试）
  types.go         — 与 Admin 共享的 DTO（与 admin/types.go 镜像）
  agent_test.go
  sysmon_test.go
  reporter_test.go
```

### 2.2 cmd 入口

```
cmd/agent/
  main.go          — 入口：解析 config → 单机/Agent 分支
                     （重命名自 cmd/stressbot/main.go）
```

### 2.3 修改文件

| 文件 | 改动 |
|---|---|
| `monitor/snapshot.go` | `ActionSnapshot` 增加 `LatencyBuckets`、`LatencySumNs`、`ApdexSatisfied`、`ApdexTolerating`（用于跨节点聚合） |
| `monitor/histogram.go` | `LatencyHistogram` 暴露桶计数获取方法 |
| `monitor/collector.go` | `Snapshot()` 输出新字段 |
| `robot/manager.go` | `RunWithContext(ctx) error`：阻塞直到所有机器人退出或 ctx 取消 |
| `conf/config.json` | 新增 `agent` 配置段 |

### 2.4 不修改

`engine/`、`network/`、`adapter/`、`protox/`、`script/`、`state/`、`robot/robot.go`（机器人本体不变）。

### 2.5 新增依赖

```bash
go get github.com/shirou/gopsutil/v4
```

## 3. 主流程

### 3.1 进程启动

```go
// cmd/agent/main.go
func main() {
    cfgPath := flag.String("config", "conf/config.json", "")
    flag.Parse()

    cfg := loadConfig(*cfgPath)

    if cfg.Agent.Enabled {
        runAgentMode(cfg)
    } else {
        runStandalone(cfg) // 现有逻辑
    }
}

func runAgentMode(cfg *Config) {
    a, err := agent.New(agent.AgentConfig{
        AdminAddr:         cfg.Agent.AdminAddr,
        Name:              cfg.Agent.Name,
        ListenAddr:        cfg.Agent.ListenAddr,
        MaxBots:           cfg.Agent.MaxBots,
        StressInterval:    utils.ParseDurationDefault(cfg.Agent.StressInterval, 5*time.Second),
        SystemInterval:    utils.ParseDurationDefault(cfg.Agent.SystemInterval, 5*time.Second),
        HeartbeatInterval: utils.ParseDurationDefault(cfg.Agent.HeartbeatInterval, 10*time.Second),
        AppVersion:        Version, // 编译时注入：-ldflags "-X main.Version=..."
    })
    if err != nil { log.Fatal(err) }
    if err := a.Run(); err != nil { log.Fatal(err) }
}
```

### 3.2 Agent 生命周期主循环

```
                Start()
                  │
                  ▼
       ┌──────────────────────┐
       │  initialize          │
       │  - SystemMonitor.Start()
       │  - http server.Listen
       │  - register loop start
       │  - system reporter start
       │  - task command listener
       └──────────┬───────────┘
                  ▼
       ┌──────────────────────┐
       │  idle                │
       │  等待任务或升级命令  │
       └─┬───────────────┬────┘
         │ 任务下达       │ 升级命令
         ▼               ▼
       ┌─────────┐   ┌──────────┐
       │ running │   │ upgrading│
       └────┬────┘   └────┬─────┘
            │             │
            │ 任务完成    │ os.Exit(99)
            ▼             ▼
         返回 idle      Launcher 接管
```

### 3.3 任务下发：Push + Poll 双通道

> 主文档已确定使用双通道。实现：

- **主通道（Push）**：Admin 通过 `POST http://agent:7070/agent/v1/task` 推送任务，Agent 立即返回 202 Accepted 后异步处理。
- **回退通道（Poll）**：Agent 每 30s 调用 `GET /api/agent/{id}/pending-task`，处理可能因 Push 失败遗漏的任务。

任何一方先收到都先标记 `currentTaskID`，重复下发时 Agent 返回 409 Conflict 即可。

### 3.4 优雅停止 / 升级退出

| 触发 | 行为 |
|---|---|
| 收到 `POST /agent/v1/stop` | drain 当前任务（停 manager → 等所有机器人退出 → 上报 `done`），保持进程不退出 |
| 收到 `POST /agent/v1/upgrade` | 下载 + 校验 → drain → 写 `.upgrade.pending` → `os.Exit(99)` |
| 收到 SIGINT / SIGTERM | drain 当前任务 → 注销（`POST /api/agent/{id}/deregister` best-effort） → 退出 |

## 4. 核心组件设计

### 4.1 Agent 主结构

```go
// agent/agent.go
type Agent struct {
    cfg    AgentConfig
    id     string         // 启动时生成 UUID
    started time.Time

    sysmon    *SystemMonitor
    collector *monitor.MetricsCollector // 复用单例
    httpSrv   *http.Server
    httpCli   *AdminClient

    // 任务状态
    mu          sync.Mutex
    currentTask *TaskAssignment
    taskCancel  context.CancelFunc
    runner      *TaskRunner

    // 上报循环句柄
    sysReporter    *SystemReporter // 常驻
    stressReporter *StressReporter // 仅 task running 时存在

    // 优雅退出
    stopCh chan struct{}
    wg     sync.WaitGroup
}

func New(cfg AgentConfig) (*Agent, error) { /* 校验配置 + 初始化 */ }
func (a *Agent) Run() error                { /* 阻塞主循环 */ }
func (a *Agent) Stop(ctx context.Context) error
```

### 4.2 SystemMonitor

```go
// agent/sysmon.go
type SystemMonitor struct {
    interval time.Duration

    mu       sync.RWMutex
    latest   SystemSnapshot     // 最新一次采集结果
    static   StaticInfo         // 启动时一次性采集

    // 网络速率累计基线
    prevNetSent uint64
    prevNetRecv uint64
    prevAt      time.Time

    self *process.Process // gopsutil 进程句柄
}

type SystemSnapshot struct {
    Timestamp     time.Time `json:"timestamp"`

    // CPU
    CPUPercent    float64   `json:"cpuPercent"`
    CPUPerCore    []float64 `json:"cpuPerCore"`
    LoadAvg1      float64   `json:"loadAvg1"`     // Linux only
    LoadAvg5      float64   `json:"loadAvg5"`
    LoadAvg15     float64   `json:"loadAvg15"`

    // 内存
    MemTotalMB    uint64    `json:"memTotalMB"`
    MemUsedMB     uint64    `json:"memUsedMB"`
    MemPercent    float64   `json:"memPercent"`
    SwapUsedMB    uint64    `json:"swapUsedMB"`

    // 进程
    ProcessRssMB  uint64    `json:"processRssMB"`
    ProcessHeapMB uint64    `json:"processHeapMB"`
    ProcessSysMB  uint64    `json:"processSysMB"`
    NumGoroutine  int       `json:"numGoroutine"`
    NumThread     int32     `json:"numThread"`
    NumFD         int32     `json:"numFd"`

    // 网络速率（差分计算）
    NetSendKBps   float64   `json:"netSendKBps"`
    NetRecvKBps   float64   `json:"netRecvKBps"`

    // GC
    GCCount       uint32    `json:"gcCount"`
    GCPauseAvgMs  float64   `json:"gcPauseAvgMs"`
}

type StaticInfo struct {
    Hostname    string `json:"hostname"`
    OS          string `json:"os"`           // "linux" / "windows"
    Arch        string `json:"arch"`         // "amd64"
    NumCPU      int    `json:"numCpu"`
    MemTotalMB  uint64 `json:"memTotalMB"`
    GoVersion   string `json:"goVersion"`
    KernelVer   string `json:"kernelVer"`    // best-effort
    StartedAt   time.Time `json:"startedAt"`
}
```

**采集实现要点**：

| 字段 | 实现 | 注意 |
|---|---|---|
| `CPUPercent` | `cpu.Percent(0, false)` | 第一次返回 0，第二次起准确（gopsutil 内部维护基线） |
| `CPUPerCore` | `cpu.Percent(0, true)` | 同上 |
| `LoadAvg*` | `load.Avg()` | Windows 下返回 0，前端可隐藏 |
| `MemTotalMB` / `MemUsedMB` | `mem.VirtualMemory()` | 单位转 MB |
| `ProcessRssMB` | `process.Process.MemoryInfo().RSS` | 物理常驻内存 |
| `ProcessHeapMB` | `runtime.ReadMemStats().HeapAlloc` | Go 运行时堆 |
| `ProcessSysMB` | `runtime.ReadMemStats().Sys` | Go 进程总占用（含栈、堆、运行时数据） |
| `NumGoroutine` | `runtime.NumGoroutine()` | |
| `NumThread` | `process.NumThreads()` | Windows 上是用户态线程数 |
| `NumFD` | `process.NumFDs()` | Windows 上 gopsutil 可能返回错误，写 0 即可 |
| `NetSendKBps` / `NetRecvKBps` | `net.IOCounters(false)[0]` 差分 / 时间差 | 第一次只记录基线，第二次起才有值 |

**调用频率**：每 5s 采集一次，缓存 `latest`，`Snapshot()` 仅做读锁返回。

### 4.3 StressReporter / SystemReporter

```go
// agent/reporter.go
type StressReporter struct {
    cli      *AdminClient
    agentID  string
    taskID   string
    interval time.Duration
    src      *monitor.MetricsCollector
    done     chan struct{}
}

func (r *StressReporter) Run(ctx context.Context) {
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            snap := r.src.Snapshot()
            r.cli.PostStress(ctx, StressReport{
                AgentID:   r.agentID,
                TaskID:    r.taskID,
                ReportedAt: time.Now(),
                Snapshot:  snap,
            })
        }
    }
}

type SystemReporter struct {
    cli      *AdminClient
    agentID  string
    interval time.Duration
    src      *SystemMonitor
}
// Run() 类似，POST /api/agent/system，无 TaskID
```

**关键策略**：
- 非阻塞：上报失败仅记录日志，不阻塞下一次
- 指数退避：连续失败时退避（1s → 2s → 4s → 上限 30s），但 ticker 不停
- 任务结束时 StressReporter 立即停止；SystemReporter 始终运行（idle 期间也要上报）

### 4.4 TaskRunner

```go
// agent/task_runner.go
type TaskRunner struct {
    assignment TaskAssignment
    workDir    string
    mgr        *robot.Manager

    cli        *AdminClient
    collector  *monitor.MetricsCollector
}

func (r *TaskRunner) Prepare(ctx context.Context) error {
    // 1. 创建临时目录 /tmp/stressbot-task-{taskID}/conf/
    // 2. 拉取 flow.json / proto/ / scripts/（HTTP GET 各 URL）
    // 3. 校验文件 SHA256（可选）
    // 4. 加载 protox / 流程 / 监控
    return nil
}

func (r *TaskRunner) Run(ctx context.Context) error {
    cfg := robot.Config{
        // 转换 assignment.Config → robot.Config
        StartNumber: r.assignment.StartNumber,
        TotalBots:   r.assignment.TotalBots,
        // ...
    }
    return r.mgr.RunWithContext(ctx, cfg)
}

func (r *TaskRunner) Cleanup() error {
    return os.RemoveAll(r.workDir)
}
```

### 4.5 Upgrader

> ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程。设计文档保留供参考。

```go
// agent/upgrader.go
type Upgrader struct {
    cli      *AdminClient
    selfPath string             // os.Executable() 缓存
    drainer  func(time.Duration) error // 由 Agent 注入：取消 task + 等机器人退出
}

func (u *Upgrader) Handle(req UpgradeRequest) error {
    newPath := u.selfPath + ".new"

    // 1. 下载到 .new
    if err := u.download(req.URL, newPath); err != nil {
        return fmt.Errorf("download: %w", err)
    }
    // 2. SHA256 校验
    if err := u.verify(newPath, req.SHA256); err != nil {
        os.Remove(newPath)
        return fmt.Errorf("verify: %w", err)
    }
    // 3. 异步 drain + exit
    go func() {
        if err := u.drainer(5 * time.Minute); err != nil {
            log.Printf("drain timeout: %v", err)
        }
        flag := filepath.Join(filepath.Dir(u.selfPath), ".upgrade.pending")
        os.WriteFile(flag, []byte(req.Version), 0o644)
        os.Exit(99)
    }()
    return nil
}

// 新版本启动后：注册成功时调用
func (u *Upgrader) MarkSuccess() {
    bak := u.selfPath + ".bak"
    if _, err := os.Stat(bak); err == nil {
        success := filepath.Join(filepath.Dir(u.selfPath), ".upgrade.success")
        os.WriteFile(success, nil, 0o644)
    }
}
```

**下载策略**：

```go
func (u *Upgrader) download(url, dst string) error {
    req, _ := http.NewRequest("GET", url, nil)
    resp, err := u.cli.Do(req)
    if err != nil { return err }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
        return fmt.Errorf("status %d", resp.StatusCode)
    }

    f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
    if err != nil { return err }
    defer f.Close()

    _, err = io.Copy(f, resp.Body)
    return err
}
```

### 4.6 HTTPServer（接收 Admin 命令）

监听 `cfg.Agent.ListenAddr`（默认 `:7070`），路由：

| 方法 | 路径 | Body | 响应 | 说明 |
|---|---|---|---|---|
| `POST` | `/agent/v1/task` | `TaskAssignment` JSON | `202 Accepted` 或 `409 Conflict` | Admin 推送任务 |
| `POST` | `/agent/v1/stop` | 空 | `200` | Admin 停止当前任务 |
| `POST` | `/agent/v1/upgrade` | `UpgradeRequest` JSON | `202 Accepted` | 触发升级 |
| `GET`  | `/agent/v1/version` | — | `{"version":"v1.2.0"}` | 查询版本 |
| `GET`  | `/agent/v1/status` | — | `AgentStatus` JSON | 调试用 |
| `GET`  | `/healthz` | — | `200 OK` | 探活 |
| `GET`  | `/debug/pprof/...` | — | pprof | 仅 debug 模式 |

**实现示例**：

```go
func (a *Agent) startHTTPServer() error {
    mux := http.NewServeMux()
    mux.HandleFunc("/agent/v1/task", a.handleTaskAssign)
    mux.HandleFunc("/agent/v1/stop", a.handleStop)
    mux.HandleFunc("/agent/v1/upgrade", a.handleUpgrade)
    mux.HandleFunc("/agent/v1/version", a.handleVersion)
    mux.HandleFunc("/agent/v1/status", a.handleStatus)
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200)
    })

    a.httpSrv = &http.Server{
        Addr:    a.cfg.ListenAddr,
        Handler: mux,
    }
    a.wg.Add(1)
    go func() {
        defer a.wg.Done()
        if err := a.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Printf("http server: %v", err)
        }
    }()
    return nil
}
```

### 4.7 AdminClient（与 Admin 通信）

```go
// agent/http_client.go
type AdminClient struct {
    base       string // "http://admin:8080"
    httpClient *http.Client
    agentID    string
}

func (c *AdminClient) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error)
func (c *AdminClient) Heartbeat(ctx context.Context, req HeartbeatRequest) error
func (c *AdminClient) PostStress(ctx context.Context, r StressReport) error
func (c *AdminClient) PostSystem(ctx context.Context, r SystemReport) error
func (c *AdminClient) FetchPendingTask(ctx context.Context) (*TaskAssignment, error)
func (c *AdminClient) ReportTaskDone(ctx context.Context, r TaskCompletionReport) error
func (c *AdminClient) Deregister(ctx context.Context) error
```

**重试策略**：所有写入接口（Register、PostStress、PostSystem、ReportTaskDone）使用指数退避（1s/2s/4s/8s/30s）；读取接口（FetchPendingTask）失败直接返回，下次轮询再试。

## 5. 协议契约

### 5.1 Agent → Admin

#### 5.1.1 注册

```http
POST /api/agent/register
Content-Type: application/json

{
  "agentId": "uuid-xxx",          // Agent 启动时生成
  "name": "agent-gz-01",
  "address": "http://10.0.0.1:7070",
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
    "kernelVer": "5.15.0",
    "startedAt": "2026-04-29T10:00:00+08:00"
  }
}
```

响应 `200 OK`：

```json
{
  "agentId": "uuid-xxx",
  "heartbeatTtl": "30s",
  "stressEndpoint": "/api/agent/stress",
  "systemEndpoint": "/api/agent/system"
}
```

#### 5.1.2 心跳

```http
POST /api/agent/{agentId}/heartbeat
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "timestamp": "2026-04-29T10:30:00+08:00",
  "status": "idle",            // idle | busy
  "currentTaskId": "task-01",  // status=busy 时存在
  "currentBots": 3000,         // 实际启动的机器人数
  "appVersion": "v1.2.0"
}
```

#### 5.1.3 上报压测指标

```http
POST /api/agent/stress
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "taskId": "task-01",
  "reportedAt": "2026-04-29T10:30:05+08:00",
  "snapshot": { /* monitor.CollectorSnapshot */ }
}
```

`snapshot` 字段对齐 `docs/api-monitor.md` GET /metrics 响应（带 `LatencyBuckets` / `LatencySumNs` 等聚合所需字段）。

#### 5.1.4 上报系统指标

```http
POST /api/agent/system
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "reportedAt": "2026-04-29T10:30:05+08:00",
  "snapshot": { /* SystemSnapshot */ }
}
```

#### 5.1.5 任务完成

```http
POST /api/agent/{agentId}/task/{taskId}/done
Content-Type: application/json

{
  "agentId": "uuid-xxx",
  "taskId": "task-01",
  "result": "completed",      // completed | stopped | failed
  "errorMsg": "",             // result=failed 时填
  "finishedAt": "2026-04-29T10:35:00+08:00",
  "finalSnapshot": { /* 最后一次完整压测快照 */ }
}
```

**`finalSnapshot` 完整性要求（强约束）**：

> Admin 会把这份 finalSnapshot **完整持久化到 MySQL `task_report` 表**，用于历史报告与版本对比。Agent 必须保证：
>
> 1. 调用 `monitor.Global().Snapshot()` 获取**最新**快照（不要使用缓存）
> 2. 上报时机：**所有 robot 已完全停止**之后，确保所有 OnComplete 回调都已经写入指标
> 3. 必须包含完整的 `actions[]`，每个动作的 `latencyBuckets` 数组必须为完整的 17 个桶（即使为零）
> 4. 必须包含 `connections`、`bandwidth`、`robots` 等所有顶层字段
> 5. 时间戳 `timestamp` 必须填写实际采样时刻
> 6. 即使 `result=failed`，也必须尽力提供 finalSnapshot（哪怕是部分指标），用于事后分析
>
> 实现示例：
>
> ```go
> // robot 全部停止后再调用
> wg.Wait()  // 等所有 robot goroutine 退出
> snap := monitor.Global().Snapshot()
> client.ReportTaskDone(ctx, TaskCompletionReport{
>     AgentID: agentID, TaskID: taskID,
>     Result: result, ErrorMsg: errMsg,
>     FinishedAt: time.Now(),
>     FinalSnapshot: snap,
> })
> ```
>
> **重试策略**：上报失败必须以指数退避重试（最多 30 分钟），重试期间不接受新任务。仅在重试彻底失败后才放弃，并 log ERROR + 维持 idle 状态。这是因为 Agent 的 finalSnapshot **是 Admin 历史记录的唯一数据源**，丢失后无法恢复。

#### 5.1.6 拉取待执行任务

```http
GET /api/agent/{agentId}/pending-task
```

响应：
- `200 OK` + `TaskAssignment` JSON：有任务
- `204 No Content`：当前 idle，无任务

#### 5.1.7 注销

```http
POST /api/agent/{agentId}/deregister
```

best-effort，失败不重试。

### 5.2 Admin → Agent

#### 5.2.1 任务下发

```http
POST http://agent:7070/agent/v1/task
Content-Type: application/json

{
  "taskId": "task-01",
  "name": "200v200 压测",
  "totalBots": 3000,
  "startNumber": 10000,
  "configBase": "http://admin:8080/api/tasks/task-01/config",
  "configFiles": [
    { "path": "flow.json",            "url": "...", "sha256": "..." },
    { "path": "proto/c2s.proto",      "url": "...", "sha256": "..." },
    { "path": "scripts/battle.lua",   "url": "...", "sha256": "..." }
  ],
  "robotConfig": {
    "authAddr": "auth.example.com:8001",
    "concurrency": 50,
    "timeoutSec": 30
  },
  "deadline": "2026-04-29T11:00:00+08:00"
}
```

响应：
- `202 Accepted`：已接受（异步执行）
- `409 Conflict`：当前已有任务（Body 含 `currentTaskId`）

#### 5.2.2 停止任务

```http
POST http://agent:7070/agent/v1/stop
```

响应 `200 OK`，Agent 异步 drain（保证 < 1 分钟）。

#### 5.2.3 升级

```http
POST http://agent:7070/agent/v1/upgrade
Content-Type: application/json

{
  "url": "http://admin:8080/api/binaries/agent-v1.2.0.exe",
  "sha256": "abc123...",
  "version": "v1.2.0"
}
```

响应 `202 Accepted`，Agent 异步处理（下载 → drain → 写标记 → exit 99）。

## 6. 任务执行细节

### 6.1 配置拉取流程

```
1. 收到 TaskAssignment
2. 创建 workDir = filepath.Join(os.TempDir(), "stressbot-task-"+taskID)
3. 创建子目录 workDir/conf/、workDir/conf/proto/、workDir/conf/scripts/
4. 遍历 ConfigFiles：
   - HTTP GET 每个 URL
   - 写入 workDir/conf/{path}
   - 校验 SHA256（不匹配则整体失败，上报 result=failed）
5. 加载 protox：protox.NewLoader(workDir/conf/proto)
6. 解析 flow.json：engine.LoadFlow(workDir/conf/flow.json)
7. 创建 robot.Manager，注入：
   - StartNumber = TaskAssignment.StartNumber
   - TotalBots   = TaskAssignment.TotalBots
   - 其他参数从 RobotConfig 中取
```

### 6.2 临时目录布局

```
/tmp/stressbot-task-{taskID}/
  conf/
    flow.json
    proto/
      c2s.proto
      s2c.proto
    scripts/
      battle.lua
      heartbeat.lua
```

任务结束（done / stopped / failed）后立即删除整个目录。

### 6.3 与 Manager 的交互

**Manager 改造（robot/manager.go）**：

```go
// 新增方法（不破坏现有 RunWithSignal）
func (m *Manager) RunWithContext(ctx context.Context, cfg RunConfig) error {
    if err := m.StartAll(cfg); err != nil { return err }
    select {
    case <-ctx.Done():
        m.StopAll()
    case <-m.allDone(): // 所有机器人退出
    }
    return nil
}

type RunConfig struct {
    StartNumber int
    TotalBots   int
    Concurrency int
}
```

> **注意**：现有 `Manager` 已支持 `startNumber` 和 `totalBots` 切分，需要确认其参数能从外部注入（如有硬编码读取 `config.json`，要重构为函数参数）。

## 7. 系统监控字段（SystemSnapshot）

完整字段表见 §4.2。前端展示时优先显示：CPU%、Mem%、NetSendKBps / NetRecvKBps、NumGoroutine。详见 `docs/api-monitor.md` 第 4 章。

## 8. 升级流程详细步骤

> ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程。设计文档保留供参考。

```
[Admin] POST /agent/v1/upgrade {url, sha256, version}
   │
   ▼
[Agent] handleUpgrade:
   1. 立即返回 202 Accepted
   2. goroutine:
      a. http GET url → ./agent.exe.new
      b. SHA256(./agent.exe.new) == sha256 ?
         - 不匹配 → os.Remove(.new)，记录失败，return
      c. 调用 a.drainer(5min):
         - 取消当前任务的 ctx
         - 等待 robot.Manager 全部退出
         - 上报 task done (result=stopped) 给 Admin
      d. 写入 .upgrade.pending（内容 = version 字符串，仅用于日志）
      e. os.Exit(99)

[Launcher] cmd.Wait() 返回 exit=99
   ▼
[Launcher] 检测 .upgrade.pending：
   1. 备份 agent.exe → agent.exe.bak
   2. os.Rename(agent.exe.new → agent.exe)
   3. 删除 .upgrade.pending
   4. spawn 新 agent.exe
   ▼
[新 Agent] 启动，注册 Admin（携带新 AppVersion）
   ▼
[新 Agent] 注册成功 → MarkSuccess()：
   - 检测到 agent.exe.bak 存在 → 写空文件 .upgrade.success
   ▼
[Launcher] 检测到 .upgrade.success:
   - 删除 .upgrade.success
   - 删除 .bak
   - 升级完成
```

**升级失败（新版本注册不上 Admin）**：
- 60s 后 Launcher 看不到 `.upgrade.success`，自动回滚 `.bak`
- 老版本重新注册 Admin（旧 AppVersion）
- Admin 看到版本号没变，标记升级失败，停止滚动升级

## 9. 配置文件 config.json（agent 段）

```json
{
  "agent": {
    "enabled": false,
    "adminAddr": "http://192.168.1.100:8080",
    "name": "agent-gz-01",
    "listenAddr": ":7070",
    "maxBots": 5000,
    "stressInterval": "5s",
    "systemInterval": "5s",
    "heartbeatInterval": "10s",
    "registerRetryMaxInterval": "30s",
    "taskWorkDir": "",          // 空 = os.TempDir()
    "appVersion": ""            // 空 = 编译时注入的 Version
  }
}
```

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | Agent 模式总开关 |
| `adminAddr` | string | — | Admin HTTP 地址（含 schema） |
| `name` | string | hostname | Agent 显示名（前端列表用） |
| `listenAddr` | string | `:7070` | 接收 Admin 推送的监听地址 |
| `maxBots` | int | `5000` | 本节点支持的最大机器人数（用于 Admin 分配） |
| `stressInterval` | string | `5s` | 压测指标上报间隔 |
| `systemInterval` | string | `5s` | 系统指标上报间隔 |
| `heartbeatInterval` | string | `10s` | 心跳间隔 |

## 10. 错误处理与重试策略

### 10.1 网络故障

| 操作 | 失败处理 |
|---|---|
| 注册失败 | 指数退避（1s→2s→...→60s 上限），永不放弃 |
| 心跳失败 | 同上，并打印 WARN，连续失败 3 次后输出 ERROR |
| 上报指标失败 | 指数退避（1s→2s→4s→上限 30s），ticker 不停 |
| 拉取配置失败 | 立即上报 task done (result=failed)，错误信息含 URL |
| 上报 task done 失败 | 指数退避（1s→2s→...→60s 上限），**最多重试 30 分钟**，因为 finalSnapshot 是 Admin 历史归档的唯一数据源；重试期间 Agent 维持本地 `state=draining`，不接受新任务 |

### 10.2 资源失败

| 场景 | 处理 |
|---|---|
| 临时目录创建失败 | 上报 task done (result=failed) |
| protox 加载失败 | 同上 |
| flow.json 解析失败 | 同上 |
| Manager.RunWithContext 返回错误 | 同上 |
| SystemMonitor 启动失败 | Agent 拒绝启动（致命错误，退出码 1 ） |

### 10.3 升级失败

> ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程。设计文档保留供参考。

| 场景 | 处理 |
|---|---|
| 下载失败 / SHA256 不匹配 | 不退出，记录失败到日志，下次升级请求重试 |
| `.upgrade.pending` 写入失败 | 取消升级 |
| `.upgrade.success` 写入失败 | 不致命，Launcher 60s 后回滚（保险机制）|

## 11. 实施分阶段计划

| 阶段 | 内容 | 工时 |
|---|---|---|
| Phase 1 | `agent/types.go` + `config.go`：DTO 与配置结构 | 0.25 天 |
| Phase 2 | `agent/sysmon.go`：SystemMonitor 实现 + gopsutil 引入 + 跨平台测试 | 1 天 |
| Phase 3 | `agent/http_client.go`：AdminClient + 重试 | 0.5 天 |
| Phase 4 | `agent/agent.go` 主结构 + 注册 + 心跳循环 | 0.75 天 |
| Phase 5 | `agent/reporter.go`：StressReporter + SystemReporter | 0.5 天 |
| Phase 6 | `agent/http_server.go`：Agent 端 HTTP 命令处理 | 0.5 天 |
| Phase 7 | `agent/task_runner.go`：配置拉取 + Manager 执行 | 1 天 |
| Phase 8 | `agent/upgrader.go`：升级流程 | 0.5 天 |
| Phase 9 | `cmd/agent/main.go`：单机/Agent 模式分支，重命名自 `cmd/stressbot/main.go` | 0.25 天 |
| Phase 10 | `monitor/snapshot.go` 扩展：聚合所需字段 | 0.5 天 |
| Phase 11 | `robot/manager.go`：`RunWithContext` 改造 | 0.5 天 |
| Phase 12 | 单元测试（含 mock Admin server） | 1 天 |
| Phase 13 | 与真实 Admin 联调 | 0.5 天 |

**总计：约 7.75 天**。

## 12. 单元测试清单

| 包 | 测试文件 | 关键测试 |
|---|---|---|
| `agent` | `agent_test.go` | TestRegisterRetry、TestHeartbeatStops、TestStateMachine（idle→running→idle）、TestConcurrentTaskRejected |
| `agent` | `sysmon_test.go` | TestCollectAllFields、TestNetRateBaseline、TestPlatformGoroutineLeak |
| `agent` | `reporter_test.go` | TestExponentialBackoff、TestSystemReporterAlwaysOn、TestStressReporterStopsOnTaskEnd、**TestFinalSnapshotIntegrity**（确保 17 桶 + 完整字段）、**TestTaskDoneLongRetry**（30 分钟内不放弃） |
| `agent` | `task_runner_test.go` | TestPullConfig、TestSHA256Mismatch、TestCleanupOnFailure、TestStartNumberHonored |
| `agent` | `upgrader_test.go` | TestDownloadVerify、TestSHA256Mismatch、TestPendingFlagWritten |

**Mock Admin server**：用 `httptest.NewServer` 模拟 Admin 的 8 个上行接口，记录请求体并断言。

## 13. 验收标准

- [ ] `go build ./cmd/agent` 在 Windows / Linux 编译通过
- [ ] 单机模式（`agent.enabled=false`）行为与改造前完全一致：能直接跑 `flow.json`，所有现有验证项通过
- [ ] Agent 模式启动后能注册到 Admin，心跳 / 系统指标推送稳定
- [ ] Admin 推送任务后，Agent 能拉取配置并执行，账号范围严格遵守 `startNumber` / `totalBots`
- [ ] 任务结束后能上报 `done`（result=completed/stopped/failed 三种均能正确判断）
- [ ] **finalSnapshot 完整性**：上报的 finalSnapshot 包含完整 actions/connections/bandwidth/robots，每个动作的 latencyBuckets 数组长度为 17
- [ ] **finalSnapshot 时序正确性**：所有 robot goroutine 退出后才采样，确保最后一批 OnComplete 已写入
- [ ] **finalSnapshot 上报重试**：模拟 Admin 短暂不可达，Agent 持续重试至上报成功；连续失败 30 分钟内不接受新任务
- [ ] 收到 stop 命令后 1 分钟内 drain 完成
- [ ] 收到 upgrade 命令后能完整跑通：下载 → 校验 → drain → 退出 99 → Launcher 替换 → 新版本注册 — ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程
- [ ] 单元测试通过率 100%
- [ ] 跨平台采集（Windows / Linux）所有 SystemSnapshot 字段无 nil pointer / panic
- [ ] 与 Admin 联调：能完整完成 1 → N → 1 的任务生命周期

## 14. 与其他模块的协议契约（强约束）

### 14.1 Agent 必须遵守

1. 升级时**先**写 `.new` 后写 `.pending`，最后才 `os.Exit(99)`，顺序不可乱
2. 新版本注册成功后必须写 `.upgrade.success`（一次性）
3. 不允许使用退出码 `99` 作其他用途
4. 不能直接读写 `.bak` 文件（Launcher 私有）

### 14.2 Agent 假定 Admin 提供

1. `/api/binaries/{filename}` 是公开可下载的（无需 token）
2. `/api/tasks/{id}/config/*` 是公开可下载的
3. Admin 注册响应中给出的 `heartbeatTtl` 是 unhealthy 阈值，本 Agent 心跳间隔需小于此值的 1/3

### 14.3 跨模块字段对齐

Agent 上报的 `CollectorSnapshot` 必须与 `monitor` 包当前结构完全一致（含新增的 `LatencyBuckets`、`LatencySumNs` 等聚合字段）。任何字段变动需同时修改 Admin Aggregator。
