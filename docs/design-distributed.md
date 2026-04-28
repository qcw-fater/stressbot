# 分布式压测系统架构设计

## 1. 概述

stressbot 当前是单机压测工具，所有机器人运行在单个进程内。本文档描述将其升级为**分布式压测系统**的架构设计：一个 Admin 管理后台协调多个 Worker 压测节点，通过 Web 前端进行任务管理和监控。

### 设计目标

- **分布式执行**：多个 Worker 节点并行执行压测任务，模拟大规模用户负载
- **集中管理**：Admin 统一调度任务、聚合指标、服务前端
- **零侵入**：现有单机模式完全兼容，Worker 模式通过配置切换
- **简单可靠**：全 HTTP 通信，无外部依赖（消息队列、服务发现等）

---

## 2. 系统拓扑

```
  +-------------------+
  |  Web Frontend     |       React + Ant Design + Vite
  |  :5173 (dev)      |       vite proxy → admin :8080
  +--------+----------+
           | /api/*
           v
  +-------------------+
  |  Admin Server     |       cmd/admin/main.go
  |  :8080            |       任务管理 + Worker 注册 + 指标聚合
  +--+----------+-----+
     |HTTP      |HTTP
     v          v
  +------+  +------+          +------+
  |Worker1|  |Worker2|  ...   |WorkerN|
  +------+  +------+          +------+
     |           |                  |
     +-------- TCP/UDP -----------+
              → 游戏服务器
```

### 三个角色

| 角色 | 进程 | 职责 |
|---|---|---|
| **Admin** | `cmd/admin/main.go`（新） | 接受前端请求、管理任务生命周期、Worker 注册表、聚合监控指标 |
| **Worker** | `cmd/stressbot/main.go`（worker 模式） | 向 Admin 注册、接收任务、执行压测、推送指标 |
| **Frontend** | `web/`（扩展现有） | Dashboard 监控、任务管理、Worker 管理 |

---

## 3. 关键设计决策

### 3.1 通信方式：HTTP Push（Worker→Admin）

Worker 主动向 Admin 推送数据（注册、心跳、指标），Admin 不需要主动拉取。

**理由**：
- Worker 已有 HTTP 基础设施（monitor 包的 HTTP server）
- Admin 无需维护 Worker 列表的轮询调度
- Worker 控制推送节奏，天然限流
- 简单——不需要 WebSocket、gRPC、消息队列

### 3.2 Worker 发现：配置文件指定

Worker 配置文件中指定 Admin 地址（`worker.adminAddr`），启动时 POST 注册。

**理由**：
- 内部工具，部署团队已知各节点 IP
- 无需服务发现（etcd/consul）增加运维复杂度
- 注册失败时指数退避重试，容忍 Admin 暂时不可用

### 3.3 任务分配：Admin 切分账号范围

Admin 将总账号范围均匀切分给各 Worker。例如 10000 机器人在 5 个 Worker 上：

| Worker | startNumber | count |
|---|---|---|
| Worker1 | 1 | 2000 |
| Worker2 | 2001 | 2000 |
| Worker3 | 4001 | 2000 |
| Worker4 | 6001 | 2000 |
| Worker5 | 8001 | 2000 |

`robot.Manager` 的 `ManagerConfig` 已支持外部指定 `StartNumber` 和 `Count`，**无需修改 robot 包**。

### 3.4 配置下发：Worker 从 Admin 拉取

Admin 将 flow.json、proto 文件、Lua 脚本内容打包存储为 `TaskConfig`。Worker 收到任务后通过 `GET /api/worker/config/{taskId}` 拉取，写入临时目录后用现有加载函数初始化。

**理由**：
- Worker 无需共享文件系统（NFS/Samba）
- 配置随任务创建时上传，Admin 是单一配置来源
- Worker 本地无需预置 flow.json

### 3.5 独立模式兼容

`worker.enabled` 默认为 `false`。未配置 Worker 段时，stressbot 行为与当前完全一致——直接创建 Manager、启动机器人、等待信号退出。

---

## 4. 目录结构

### 新增文件

```
admin/
  admin.go          — AdminServer 结构体、HTTP 服务器启动、路由注册
  types.go          — 共享数据类型（Task、WorkerNode、Assignment 等）
  task.go           — Task 状态机 + 内存存储（CRUD）
  worker.go         — WorkerNode 注册表、健康检测、心跳超时
  assignment.go     — 任务分配算法（均匀切分账号范围）
  aggregator.go     — MetricsAggregator：合并 N 个 CollectorSnapshot
  handlers.go       — 所有 /api/ HTTP handler

worker/
  agent.go          — WorkerAgent：注册、心跳、接收任务、推送指标、管理本地 Manager

cmd/admin/
  main.go           — Admin 进程入口：加载配置、启动 AdminServer
```

### 修改文件

| 文件 | 改动范围 |
|---|---|
| `cmd/stressbot/main.go` | Config 新增 `Worker` 配置段；`worker.enabled=true` 时启动 Agent 代替直接创建 Manager |
| `monitor/histogram.go` | `HistogramSnapshot` 新增 `SumNs` 和 `BucketCounts` 字段（`omitempty`，向后兼容） |
| `web/vite.config.ts` | proxy target 从 `localhost:6060` 改为 `localhost:8080` |

### 不修改的包

- `engine/` — TaskFlow / Executor / ActionDef 原样复用
- `robot/` — ManagerConfig 已支持外部 startNumber/count
- `network/` — gnet 引擎 per-process，无需改动
- `adapter/`、`protox/`、`script/`、`state/` — 原样复用
- `monitor/` — 仅 HistogramSnapshot 增加两个 omitempty 字段

---

## 5. Admin API 设计

### 5.1 前端 API（`/api/`）

#### 任务管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/tasks` | 创建任务（含配置包上传） |
| `GET` | `/api/tasks` | 列出所有任务 |
| `GET` | `/api/tasks/{id}` | 任务详情 + 当前聚合指标 |
| `POST` | `/api/tasks/{id}/start` | 启动任务（分配 Worker、下发任务） |
| `POST` | `/api/tasks/{id}/stop` | 停止任务 |
| `DELETE` | `/api/tasks/{id}` | 删除任务（仅 stopped 状态可删） |

#### Worker 管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/workers` | 列出所有 Worker 及状态 |
| `GET` | `/api/workers/{id}` | Worker 详情 + per-worker 指标 |

#### 监控指标

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/metrics` | 全局聚合指标（合并所有活跃 Worker） |
| `GET` | `/api/metrics/workers` | per-worker 指标明细（不聚合） |

### 5.2 Worker API（`/api/worker/`）

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/worker/register` | Worker 注册（name、address、maxBots） |
| `POST` | `/api/worker/heartbeat` | 心跳 + 系统信息 |
| `POST` | `/api/worker/metrics` | 推送 `CollectorSnapshot` |
| `GET` | `/api/worker/task` | 拉取当前任务分配 |
| `POST` | `/api/worker/task/{id}/done` | 报告任务正常完成 |
| `POST` | `/api/worker/task/{id}/failed` | 报告任务失败 |
| `GET` | `/api/worker/config/{taskId}` | 拉取配置包（flow.json + proto + scripts） |

---

## 6. 数据模型

### 6.1 Task

```go
type Task struct {
    ID          string       `json:"id"`
    Name        string       `json:"name"`
    Status      TaskStatus   `json:"status"`
    TotalBots   int          `json:"totalBots"`
    Config      *TaskConfig  `json:"config,omitempty"`
    Assignments []Assignment `json:"assignments,omitempty"`
    CreatedAt   time.Time    `json:"createdAt"`
    StartedAt   *time.Time   `json:"startedAt,omitempty"`
    StoppedAt   *time.Time   `json:"stoppedAt,omitempty"`
}

type TaskStatus string

const (
    TaskPending  TaskStatus = "pending"
    TaskRunning  TaskStatus = "running"
    TaskStopping TaskStatus = "stopping"
    TaskStopped  TaskStatus = "stopped"
    TaskFailed   TaskStatus = "failed"
)
```

**状态转换**：

```
pending → running → stopping → stopped
                   ↘ failed
running → failed（Worker 报告失败）
```

### 6.2 TaskConfig

配置包——包含压测所需的全部配置参数和文件内容，从 Admin 下发给 Worker。

```go
type TaskConfig struct {
    // 压测参数
    AccountPrefix     string            `json:"accountPrefix"`
    StartNumber       int               `json:"startNumber"`
    MainService       string            `json:"mainService"`
    ConcurrentNum     int               `json:"concurrentNum"`
    AuthAddress       string            `json:"authAddress"`
    AuthExtra         map[string]string `json:"authExtra"`

    // 网络参数
    HeartbeatInterval string            `json:"heartbeatInterval"`
    TCPTimeout        string            `json:"tcpTimeout"`
    HTTPTimeout       string            `json:"httpTimeout"`

    // 监控参数
    ApdexT            int               `json:"apdexT"`

    // 文件内容（非文件路径），下发给 Worker
    FlowJSON          json.RawMessage   `json:"flowJson"`       // flow.json 内容
    AdapterLua        string            `json:"adapterLua"`     // codec.lua 内容
    ProtoFiles        map[string]string `json:"protoFiles"`     // filename → content
    LuaScripts        map[string]string `json:"luaScripts"`     // filename → content
}
```

### 6.3 WorkerNode

```go
type WorkerNode struct {
    ID            string        `json:"id"`
    Name          string        `json:"name"`
    Address       string        `json:"address"`        // Worker 的 HTTP 监听地址
    Status        WorkerStatus  `json:"status"`         // idle/busy/unhealthy/offline
    MaxBots       int           `json:"maxBots"`        // 该 Worker 可承载的最大机器人数
    CurrentTaskID string        `json:"currentTaskId,omitempty"`
    LastHeartbeat time.Time     `json:"lastHeartbeat"`
    SystemInfo    *SystemInfo   `json:"systemInfo,omitempty"`
    RegisteredAt  time.Time     `json:"registeredAt"`
}

type WorkerStatus string

const (
    WorkerIdle      WorkerStatus = "idle"
    WorkerBusy      WorkerStatus = "busy"
    WorkerUnhealthy WorkerStatus = "unhealthy"
    WorkerOffline   WorkerStatus = "offline"
)

type SystemInfo struct {
    OS         string `json:"os"`
    Arch       string `json:"arch"`
    NumCPU     int    `json:"numCPU"`
    TotalMemMB int64  `json:"totalMemMB"`
}
```

### 6.4 Assignment

```go
type Assignment struct {
    WorkerID    string `json:"workerId"`
    StartNumber int    `json:"startNumber"`  // 该 Worker 的起始账号号
    Count       int    `json:"count"`        // 该 Worker 的机器人数
}
```

### 6.5 Worker 注册请求

```go
type RegisterRequest struct {
    Name    string `json:"name"`
    Address string `json:"address"`   // Worker 的 HTTP 监听地址，如 "192.168.1.10:7070"
    MaxBots int    `json:"maxBots"`
}
```

### 6.6 任务分配（下发给 Worker）

```go
type TaskAssignment struct {
    TaskID           string            `json:"taskId"`
    TaskName         string            `json:"taskName"`
    StartNumber      int               `json:"startNumber"`
    Count            int               `json:"count"`
    AccountPrefix    string            `json:"accountPrefix"`
    ConcurrentNum    int               `json:"concurrentNum"`
    MainService      string            `json:"mainService"`
    AuthAddress      string            `json:"authAddress"`
    AuthExtra        map[string]string `json:"authExtra"`
    HeartbeatInterval string           `json:"heartbeatInterval"`
    TCPTimeout       string            `json:"tcpTimeout"`
    HTTPTimeout      string            `json:"httpTimeout"`
    ApdexT           int               `json:"apdexT"`
    ConfigURL        string            `json:"configUrl"`    // GET 拉取配置包的 URL
}
```

---

## 7. 指标聚合

Worker 每 5 秒向 Admin 推送一次 `monitor.CollectorSnapshot`（现有的 JSON 结构）。Admin 维护 `map[workerID]*CollectorSnapshot`，前端请求时实时聚合。

### 7.1 聚合规则

| 指标类型 | 聚合方式 | 说明 |
|---|---|---|
| 计数器（success/failure/timeout/skipped） | **求和** | 各 Worker 计数器直接相加 |
| 正在执行数（executing） | **求和** | 集群总并发数 |
| 机器人状态（started/running/stopped/errored） | **求和** | 集群总览 |
| 连接指标（established/failed/dropped） | **求和** | 集群总览 |
| 带宽（totalSendBytes/totalRecvBytes） | **求和** | 重新计算 MBps |
| 延迟直方图 | **合并桶计数** | 各 Worker 桶计数相加后重新计算百分位 |
| Apdex | **重新计算** | 用聚合后的 satisfied/tolerating/total 重算 |
| 错误分布 | **按消息合并** | 相同 errorMsg 的计数相加 |
| 系统指标（goroutines/mem/GC） | **求和** | 集群资源总览；per-worker 明细单独保留 |

### 7.2 直方图合并

`HistogramSnapshot` 新增两个字段以支持精确合并：

```go
type HistogramSnapshot struct {
    // 现有字段（不变）
    Count int64   `json:"count"`
    MinMs float64 `json:"minMs"`
    MaxMs float64 `json:"maxMs"`
    AvgMs float64 `json:"avgMs"`
    P50Ms float64 `json:"p50Ms"`
    P90Ms float64 `json:"p90Ms"`
    P95Ms float64 `json:"p95Ms"`
    P99Ms float64 `json:"p99Ms"`

    // 新增字段（omitempty，向后兼容）
    SumNs        int64   `json:"sumNs,omitempty"`
    BucketCounts []int64 `json:"bucketCounts,omitempty"`
}
```

合并算法：

```go
func MergeHistograms(snaps []HistogramSnapshot) HistogramSnapshot {
    var merged HistogramSnapshot
    merged.MinMs = math.MaxFloat64
    for _, s := range snaps {
        if s.Count == 0 { continue }
        merged.Count += s.Count
        merged.SumNs += s.SumNs
        if s.MinMs < merged.MinMs { merged.MinMs = s.MinMs }
        if s.MaxMs > merged.MaxMs { merged.MaxMs = s.MaxMs }
        for i, c := range s.BucketCounts {
            merged.BucketCounts[i] += c
        }
    }
    if merged.Count > 0 {
        merged.AvgMs = float64(merged.SumNs) / float64(merged.Count) / 1e6
    }
    // 复用 monitor/histogram.go 的 percentileFromBuckets 重算百分位
    merged.P50Ms = percentileFromBuckets(merged.BucketCounts, merged.Count, 0.50)
    merged.P90Ms = percentileFromBuckets(merged.BucketCounts, merged.Count, 0.90)
    merged.P95Ms = percentileFromBuckets(merged.BucketCounts, merged.Count, 0.95)
    merged.P99Ms = percentileFromBuckets(merged.BucketCounts, merged.Count, 0.99)
    return merged
}
```

**精度保证**：所有 Worker 使用相同的 17 个固定桶，合并桶计数后百分位计算精度与单节点一致。

### 7.3 错误分布合并

```go
errorMap := make(map[string]int64)
for _, wAction := range workerActions {
    for _, e := range wAction.Errors {
        errorMap[e.Message] += e.Count
    }
}
```

### 7.4 聚合 JSON 格式

`GET /api/metrics` 返回的 JSON 与现有 `GET /metrics` 格式一致（`CollectorSnapshot`），前端无需区分单机/分布式。新增可选字段 `workers` 提供 per-worker 明细。

---

## 8. 任务生命周期

```
1. 创建任务
   前端 POST /api/tasks { name, totalBots, config: { flowJson, protoFiles, ... } }
   → Admin 创建 Task，status=pending

2. 启动任务
   前端 POST /api/tasks/{id}/start
   → Admin:
     a. 选择 status=idle 的 Worker（数量足够承载 totalBots）
     b. 均匀切分账号范围 [startNumber, startNumber+totalBots)
     c. 创建 Assignment，每个 Worker 一份
     d. 通知各 Worker（Worker 拉取或 Admin 推送）
     e. status=running，记录 startedAt

3. Worker 执行
   a. GET /api/worker/config/{taskId} 拉取配置包
   b. 写入临时目录 worker-task-{taskId}/conf/
   c. 加载 adapter、proto、flow、scripts（复用现有函数）
   d. 创建 robot.Manager（assigned startNumber/count）→ StartAll
   e. 启动 monitor.Reporter
   f. 每 5s POST /api/worker/metrics 推送 CollectorSnapshot

4. 前端监控
   前端每 3~5s GET /api/metrics → Admin 聚合所有 Worker 快照返回
   前端每 3~5s GET /api/tasks/{id} → 查看任务状态

5. 停止任务
   前端 POST /api/tasks/{id}/stop
   → Admin:
     a. status=stopping
     b. 通知各 Worker 停止
   → Worker:
     a. mgr.StopAll()
     b. 导出本地 CSV
     c. 推送最终指标
     d. POST /api/worker/task/{id}/done
   → Admin:
     a. 所有 Worker 报告 done → status=stopped
     b. 记录 stoppedAt

6. 清理
   Worker 回到 idle 状态，可接受新任务
```

### 时序图

```
Frontend          Admin             Worker1           Worker2
   |                |                  |                  |
   |--POST /tasks-->|                  |                  |
   |<--{id:123}-----|                  |                  |
   |                |                  |                  |
   |--POST /start-->|                  |                  |
   |                |--assign(task)---→|--assign(task)---→|
   |                |                  |                  |
   |                |                  |--GET /config/123→|
   |                |                  |<--{flow,proto}---|
   |                |                  |                  |--GET /config/123→
   |                |                  |                  |<--{flow,proto}---
   |                |                  |                  |
   |                |                  |[Manager.StartAll]|[Manager.StartAll]
   |                |                  |                  |
   |                |<--POST /metrics--|<--POST /metrics--|
   |                |<--POST /metrics--|<--POST /metrics--|
   |                |  (every 5s)      |  (every 5s)      |
   |                |                  |                  |
   |--GET /metrics->|                  |                  |
   |<--aggregated---|                  |                  |
   |                |                  |                  |
   |--POST /stop--->|                  |                  |
   |                |--stop signal----→|--stop signal----→|
   |                |                  |[mgr.StopAll]     |[mgr.StopAll]
   |                |                  |                  |
   |                |<-POST /done------|<-POST /done------|
   |<--{stopped}----|                  |                  |
```

---

## 9. Worker Agent 设计

`worker/agent.go` 是 Worker 模式的核心组件，负责与 Admin 通信和管理本地压测生命周期。

```go
type Agent struct {
    cfg       AgentConfig
    workerID  string                     // Admin 注册时分配
    collector *monitor.MetricsCollector
    mgr       *robot.Manager              // 当前 Manager（idle 时为 nil）

    // 任务状态
    currentTask *TaskAssignment
    taskCancel  context.CancelFunc

    httpClient *http.Client
    stopCh     chan struct{}
}

type AgentConfig struct {
    AdminAddr      string        // Admin 服务器地址
    Name           string        // Worker 显示名
    ListenAddr     string        // Worker HTTP 监听地址
    MaxBots        int           // 容量上限
    ReportInterval time.Duration // 指标推送间隔
}
```

### Agent 运行的三个循环

| 循环 | 间隔 | 功能 |
|---|---|---|
| **心跳循环** | 10s | POST `/api/worker/heartbeat`（系统信息），检测 Admin 连通性 |
| **指标推送循环** | 5s | POST `/api/worker/metrics`（CollectorSnapshot），仅任务运行中 |
| **任务轮询循环** | 5s | GET `/api/worker/task`，等待任务分配 |

### Agent 暴露的 HTTP 端点

Worker 在 `ListenAddr` 上启动一个轻量 HTTP 服务器，供 Admin 主动推送命令：

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/task/assign` | 接收任务分配 |
| `POST` | `/task/stop` | 停止当前任务 |
| `GET` | `/health` | 健康检查 |

### 任务执行流程

```go
func (a *Agent) executeTask(assignment *TaskAssignment) error {
    // 1. 拉取配置包
    config := fetchConfig(assignment.ConfigURL)

    // 2. 写入临时目录
    dir := filepath.Join(os.TempDir(), "stressbot-task-"+assignment.TaskID)
    writeConfigFiles(dir, config)

    // 3. 初始化组件（复用 cmd/stressbot/main.go 的逻辑）
    adapter := loadAdapter(dir + "/adapter/codec.lua")
    protoLoader := protox.NewLoader(dir+"/proto", nil)
    flow := loadFlow(dir + "/flow.json")
    // ...

    // 4. 创建 Manager（使用分配的 startNumber/count）
    mgrCfg := robot.ManagerConfig{
        AccountPrefix: assignment.AccountPrefix,
        StartNumber:   assignment.StartNumber,
        Count:         assignment.Count,
        // ... 其余参数
    }
    mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)

    // 5. 启动
    mgr.StartAll()

    // 6. 等待停止信号或任务完成
    <-a.taskCancel
    mgr.StopAll()
    return nil
}
```

---

## 10. Admin Server 设计

```go
type AdminServer struct {
    cfg     AdminConfig
    tasks   *TaskStore          // 任务存储（内存 map）
    workers *WorkerRegistry     // Worker 注册表
    metrics *MetricsAggregator  // 指标聚合器
    mux     *http.ServeMux
}

type AdminConfig struct {
    ListenAddr   string        `json:"listenAddr"`    // ":8080"
    StaticDir    string        `json:"staticDir"`     // 前端构建产物目录
    HeartbeatTTL time.Duration `json:"heartbeatTtl"`  // Worker 心跳超时，默认 30s
    DataDir      string        `json:"dataDir"`       // 数据存储目录（配置包、CSV）
}
```

### 路由注册

```go
func (s *AdminServer) registerRoutes() {
    // 前端 API
    s.mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
    s.mux.HandleFunc("GET /api/tasks", s.handleListTasks)
    s.mux.HandleFunc("GET /api/tasks/{id}", s.handleGetTask)
    s.mux.HandleFunc("POST /api/tasks/{id}/start", s.handleStartTask)
    s.mux.HandleFunc("POST /api/tasks/{id}/stop", s.handleStopTask)
    s.mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)
    s.mux.HandleFunc("GET /api/workers", s.handleListWorkers)
    s.mux.HandleFunc("GET /api/workers/{id}", s.handleGetWorker)
    s.mux.HandleFunc("GET /api/metrics", s.handleGetMetrics)
    s.mux.HandleFunc("GET /api/metrics/workers", s.handleGetWorkerMetrics)

    // Worker API
    s.mux.HandleFunc("POST /api/worker/register", s.handleWorkerRegister)
    s.mux.HandleFunc("POST /api/worker/heartbeat", s.handleWorkerHeartbeat)
    s.mux.HandleFunc("POST /api/worker/metrics", s.handleWorkerMetrics)
    s.mux.HandleFunc("GET /api/worker/task", s.handleWorkerGetTask)
    s.mux.HandleFunc("POST /api/worker/task/{id}/done", s.handleWorkerTaskDone)
    s.mux.HandleFunc("POST /api/worker/task/{id}/failed", s.handleWorkerTaskFailed)
    s.mux.HandleFunc("GET /api/worker/config/{taskId}", s.handleWorkerGetConfig)

    // 静态文件（前端构建产物）
    if s.cfg.StaticDir != "" {
        s.mux.Handle("/", http.FileServer(http.Dir(s.cfg.StaticDir)))
    }
}
```

### 任务存储

内存存储，`map[string]*Task`，按 ID 索引。后续可扩展为 SQLite 或文件持久化。

```go
type TaskStore struct {
    mu    sync.RWMutex
    tasks map[string]*Task
    order []string  // 按创建时间排序的 ID 列表
}
```

### Worker 注册表

```go
type WorkerRegistry struct {
    mu      sync.RWMutex
    workers map[string]*WorkerNode  // ID → WorkerNode
}
```

后台 goroutine 定期检查心跳超时（默认 30s），将超时 Worker 标记为 `unhealthy`。

---

## 11. 任务分配算法

`admin/assignment.go` 实现均匀分配：

```go
func Assign(task *Task, workers []*WorkerNode) []Assignment {
    totalBots := task.TotalBots
    startNum := task.Config.StartNumber
    n := len(workers)

    var assignments []Assignment
    base := totalBots / n
    remainder := totalBots % n

    current := startNum
    for i, w := range workers {
        count := base
        if i < remainder {
            count++  // 前 remainder 个 Worker 多分一个
        }
        assignments = append(assignments, Assignment{
            WorkerID:    w.ID,
            StartNumber: current,
            Count:       count,
        })
        current += count
    }
    return assignments
}
```

**Worker 容量检查**：分配前过滤掉 `status != idle` 的 Worker，确保 `sum(available.MaxBots) >= task.TotalBots`。

---

## 12. Worker 配置

在现有 `conf/config.json` 中新增 `worker` 段：

```json
{
  "worker": {
    "enabled": false,
    "adminAddr": "http://192.168.61.100:8080",
    "name": "worker-1",
    "listenAddr": ":7070",
    "maxBots": 5000
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | Worker 模式开关。`false` 时为单机模式 |
| `adminAddr` | string | `""` | Admin 服务器地址 |
| `name` | string | 主机名 | Worker 显示名称 |
| `listenAddr` | string | `":7070"` | Worker HTTP 监听地址（供 Admin 回调） |
| `maxBots` | int | `0` | 最大可承载机器人数，`0` 表示无限制 |

### cmd/stressbot/main.go 改动

```go
func main() {
    // ... 现有配置加载 ...

    if cfg.Worker.Enabled {
        // Worker 模式：启动 Agent
        agent := worker.NewAgent(worker.AgentConfig{
            AdminAddr:      cfg.Worker.AdminAddr,
            Name:           cfg.Worker.Name,
            ListenAddr:     cfg.Worker.ListenAddr,
            MaxBots:        cfg.Worker.MaxBots,
            ReportInterval: 5 * time.Second,
        })
        agent.Run()  // 阻塞，处理注册、任务、心跳、指标推送
    } else {
        // 单机模式：现有逻辑，完全不变
        // ... 现有代码 ...
    }
}
```

### Admin 配置

新建 `conf/admin-config.json`：

```json
{
  "listenAddr": ":8080",
  "staticDir": "web/dist",
  "heartbeatTtl": "30s",
  "dataDir": "data"
}
```

---

## 13. 故障处理

### Worker 宕机

1. Admin 心跳检测：3 次心跳超时（~30s）标记 Worker 为 `unhealthy`
2. 聚合指标继续反映该 Worker 最后一次推送的快照（标记为 stale）
3. 前端 UI 显示该 Worker 为红色/不健康状态
4. 用户可选操作：
   - **继续运行**：剩余 Worker 继续压测，指标反映实际活跃 Worker
   - **重新分配**：将不健康 Worker 的账号范围分配给其他空闲 Worker
   - **停止任务**：停止所有 Worker

### Admin 宕机

1. Worker 继续自治运行（Manager 不依赖 Admin）
2. Worker 指标推送失败时指数退避重试（1s → 2s → 4s → 8s → 30s 上限）
3. Admin 恢复后 Worker 重新注册，恢复指标推送
4. 任务状态从 Admin 数据目录恢复（如已实现持久化）

### 网络分区

- 同 Worker 宕机，由心跳超时机制检测
- Worker 侧不依赖 Admin 的实时连接来维持压测任务

---

## 14. 前端变更

### 新增页面

| 路由 | 页面 | 功能 |
|---|---|---|
| `/dashboard` | 监控仪表盘 | 聚合指标展示（复用现有 MetricsBadge 组件） |
| `/tasks` | 任务管理 | 创建/启动/停止任务，查看任务列表和状态 |
| `/tasks/:id` | 任务详情 | 任务配置、Worker 分配、实时指标 |
| `/workers` | Worker 管理 | Worker 列表、状态、容量、在线状态 |

### Vite 代理变更

```typescript
// web/vite.config.ts
proxy: {
  '/api': {
    target: 'http://localhost:8080',  // Admin 服务器（原来指向 :6060）
    changeOrigin: true,
  },
},
```

### 前端数据获取

```typescript
// 聚合指标（每 5s 轮询）
const { data: metrics } = useSWR('/api/metrics', fetcher, { refreshInterval: 5000 });

// 任务列表
const { data: tasks } = useSWR('/api/tasks', fetcher);

// Worker 列表
const { data: workers } = useSWR('/api/workers', fetcher, { refreshInterval: 10000 });
```

---

## 15. 实施顺序

### Phase 1：Monitor 增强（前置依赖）

1. `monitor/histogram.go` — `HistogramSnapshot` 新增 `SumNs` 和 `BucketCounts`，`Snapshot()` 填充这两个字段

### Phase 2：Worker Agent

2. `worker/agent.go` — 完整的 Agent 实现
3. `cmd/stressbot/main.go` — Worker 配置段 + 条件启动分支

### Phase 3：Admin 核心

4. `admin/types.go` — 数据模型定义
5. `admin/task.go` — TaskStore、Task 状态机
6. `admin/worker.go` — WorkerRegistry、心跳超时检测
7. `admin/assignment.go` — 均匀分配算法
8. `admin/aggregator.go` — CollectorSnapshot 合并、直方图合并、错误分布合并
9. `admin/handlers.go` — 所有 HTTP handler
10. `admin/admin.go` — AdminServer、路由注册、HTTP 服务器
11. `cmd/admin/main.go` — Admin 进程入口

### Phase 4：前端集成

12. `web/vite.config.ts` — proxy 改向 Admin
13. 前端新增 Dashboard / Tasks / Workers 页面
14. Admin 配置 `staticDir` 托管前端构建产物

### Phase 5：健壮性

15. Worker 健康监控 + unhealthy 自动检测
16. Admin 任务状态持久化（JSON 文件）
17. Worker 指标推送退避重试

---

## 16. 验证方案

### 单机验证（localhost 模拟分布式）

```bash
# 编译
go build ./cmd/admin
go build ./cmd/stressbot

# 终端 1：启动 Admin
go run ./cmd/admin -config conf/admin-config.json

# 终端 2：启动 Worker1
go run ./cmd/stressbot -config conf/worker1-config.json

# 终端 3：启动 Worker2
go run ./cmd/stressbot -config conf/worker2-config.json

# 终端 4：操作
# 创建任务
curl -X POST http://localhost:8080/api/tasks \
  -H "Content-Type: application/json" \
  -d '{"name":"test","totalBots":10,"config":{...}}'

# 启动任务
curl -X POST http://localhost:8080/api/tasks/{id}/start

# 查看聚合指标
curl http://localhost:8080/api/metrics

# 查看 Worker 列表
curl http://localhost:8080/api/workers

# 停止任务
curl -X POST http://localhost:8080/api/tasks/{id}/stop
```

### 验证检查项

- [ ] Worker 注册成功，Admin `/api/workers` 返回 2 个 Worker
- [ ] 任务启动后，两个 Worker 各收到分配（5 + 5 机器人）
- [ ] `/api/metrics` 返回聚合指标（successCount = Worker1.successCount + Worker2.successCount）
- [ ] 延迟百分位 P50/P95/P99 合理（从合并桶计数计算）
- [ ] 停止任务后，两个 Worker 均报告 done，任务状态变为 stopped
- [ ] Worker 宕机时，Admin 标记 unhealthy，前端可见
- [ ] 单机模式（`worker.enabled=false`）行为完全不变
