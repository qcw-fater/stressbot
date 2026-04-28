# 分布式压测管理系统 — 架构设计

> **目标**：在现有单机 stressbot 基础上，构建一套分布式压测管理系统，支持多台压测服务器协同执行、集中管控、实时指标汇总，并通过 REST API 对接 Web 前端（`design-web-editor.md` 中的可视化编辑器）。

---

## 1. 系统概述

### 1.1 背景与需求

当前 stressbot 是单机运行的压测工具，存在以下局限：

- 单机并发能力有限，无法模拟大规模用户
- 无远程控制能力，启停只能在服务器上手动操作
- 指标分散在各台机器上，无法全局汇总

分布式管理系统需要满足：

| 功能 | 说明 |
|---|---|
| 多节点协同压测 | N 台 Agent 同时运行，共同模拟 N×M 个机器人 |
| 集中启停控制 | 前端/API 一键下发任务到所有 Agent |
| 实时指标汇总 | Agent 定期上报，Master 聚合后推送给前端 |
| 任务生命周期管理 | 创建/配置/启动/停止/归档压测任务 |
| Agent 健康监控 | 检测 Agent 存活状态，故障自动标记 |

### 1.2 与现有代码的关系

```
现有代码（不修改）               新增代码
─────────────────               ──────────────────
stressbot（单机模式）  ←── 被 agent 以库方式集成
monitor/              ←── collector.go 直接复用
engine/               ←── agent 直接调用
robot/Manager         ←── agent 控制其启停
```

---

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                         Web 前端                                │
│   流程编辑器 │ 任务管理 │ Worker 列表 │ 实时监控大盘            │
└──────────────────┬──────────────────────────────────────────────┘
                   │ REST API（HTTP 轮询）
┌──────────────────▼──────────────────────────────────────────────┐
│                      Master 管理服务                            │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────────────────┐ │
│  │  Task Manager│ │ Worker Registry│ │   Metrics Aggregator   │ │
│  │  任务生命周期 │ │  节点注册/心跳 │ │  多节点指标合并+缓存    │ │
│  └──────────────┘ └──────────────┘ └──────────────────────────┘ │
└──────────────────┬──────────────────────────────────────────────┘
          HTTP Push│命令下发  HTTP Push│指标上报
    ┌──────────────┼──────────────────────────────────┐
    │              │                                  │
┌───▼───────────┐ ┌▼────────────────┐ ┌──────────────▼──────┐
│   Agent #1    │ │    Agent #2      │ │     Agent #N        │
│ ┌───────────┐ │ │ ┌───────────┐   │ │ ┌───────────┐       │
│ │  runner   │ │ │ │  runner   │   │ │ │  runner   │       │
│ │  (robot   │ │ │ │  (robot   │   │ │ │  (robot   │       │
│ │  Manager) │ │ │ │  Manager) │   │ │ │  Manager) │       │
│ └───────────┘ │ │ └───────────┘   │ │ └───────────┘       │
│ reporter每5s  │ │ reporter每5s    │ │ reporter每5s        │
│ 上报snapshot  │ │ 上报snapshot    │ │ 上报snapshot        │
└───────────────┘ └─────────────────┘ └─────────────────────┘
      服务器A               服务器B                服务器C
```

### 2.1 三层职责分工

| 层 | 职责 | 技术 |
|---|---|---|
| **前端** | 流程编辑、任务配置、监控大盘 | React + HTTP 轮询 |
| **Master** | 任务管理、节点调度、指标聚合 | Go HTTP Server |
| **Agent** | 执行压测、上报指标、接受控制 | Go + stressbot 库 |

---

## 3. Master 管理服务

### 3.1 职责与边界

- 接受前端的所有管理请求（REST API）
- 维护 Worker Registry（Agent 列表、状态、元数据）
- 持久化任务配置（内存 + 可选落盘）
- 将任务分发给选中的 Agent（HTTP Push）
- 汇总所有 Agent 上报的 `CollectorSnapshot`，缓存最新聚合结果供前端轮询
- 检测 Agent 心跳超时，自动标记离线

### 3.2 核心组件

```
master/
├── server.go         — HTTP 路由、中间件（CORS、Auth）
├── handler/
│   ├── frontend.go   — 前端 API 处理器（/api/v1/...）
│   └── internal.go   — Agent 上报 API 处理器（/internal/v1/...）
├── registry/
│   └── worker.go     — Worker 注册、心跳检测、状态管理
├── task/
│   ├── manager.go    — 任务 CRUD、状态机
│   └── distributor.go — 将任务下发给目标 Agent
├── aggregate/
│   ├── merger.go     — 合并多个 CollectorSnapshot，缓存最新结果
│   └── history.go    — 近 N 个周期的快照环形缓冲（供 /metrics/history）
└── store/
    ├── memory.go     — 内存存储（任务、Worker、快照）
    └── file.go       — 任务配置文件持久化（可选）
```

### 3.3 Worker Registry

```go
// WorkerInfo Agent 节点信息
type WorkerInfo struct {
    ID           string        // 唯一 ID，Agent 启动时生成（hostname+随机后缀）
    Name         string        // 人读名称（可配置）
    Addr         string        // Agent HTTP 监听地址，Master 调用它
    Tags         []string      // 标签（如 region、机房），用于任务路由
    Status       WorkerStatus  // Idle / Running / Offline
    LastSeen     time.Time     // 最后心跳时间
    CurrentTask  string        // 当前正在执行的任务 ID（空=空闲）
    Caps         WorkerCaps    // 能力描述（最大机器人数等）
}

type WorkerStatus string
const (
    WorkerIdle    WorkerStatus = "idle"
    WorkerRunning WorkerStatus = "running"
    WorkerOffline WorkerStatus = "offline"
)

type WorkerCaps struct {
    MaxRobots   int    `json:"maxRobots"`   // 该节点能承载的最大机器人数
    Version     string `json:"version"`     // Agent 版本
}
```

心跳检测：Registry 后台每 15s 扫描一次，`LastSeen` 超过 30s 的 Worker 标记为 `Offline`，并对其上的运行中任务触发降级处理（标记 Task 为 `PartialFailed`）。

### 3.4 Task Manager

```go
// Task 压测任务
type Task struct {
    ID          string       // 任务 ID（nanoid）
    Name        string       // 任务名称
    Status      TaskStatus   // Created/Running/Stopped/Completed/Failed
    Config      TaskConfig   // 任务配置（含 flow.json 内容、机器人数等）
    Workers     []string     // 分配到的 Worker ID 列表
    CreatedAt   time.Time
    StartedAt   *time.Time
    StoppedAt   *time.Time
}

// TaskStatus 状态机
type TaskStatus string
const (
    TaskCreated  TaskStatus = "created"   // 已创建，未启动
    TaskRunning  TaskStatus = "running"   // 正在运行
    TaskStopping TaskStatus = "stopping"  // 停止中（已下发停止命令）
    TaskStopped  TaskStatus = "stopped"   // 已停止
    TaskFailed   TaskStatus = "failed"    // 异常终止
)
```

**状态机转换：**

```
created → running   : POST /tasks/{id}/start（向所有目标 Agent 下发启动命令）
running → stopping  : POST /tasks/{id}/stop（向所有 Agent 下发停止命令）
stopping → stopped  : 所有 Agent 确认停止后（通过心跳/回调）
running → failed    : 所有 Agent 均离线
```

### 3.5 Metrics Aggregator

负责将多个 Agent 上报的 `CollectorSnapshot` 合并为一个全局快照。

**合并规则：**

| 指标类型 | 合并方式 |
|---|---|
| robot.running / stopped / errored | **求和** |
| connections.established / dropped | **求和** |
| totalActions | **求和** |
| bandwidth.sendMBps / recvMBps | **求和** |
| per-action.successCount / failureCount | **求和** |
| per-action latency histogram（桶计数） | **逐桶求和** → 重新计算百分位 |
| per-action.Apdex | 从合并后的 apdexSatisfied/Tolerating 总和重算 |
| system（goroutines/mem） | **每个 Agent 各自显示**，不合并 |

**延迟直方图合并（关键算法）：**

```go
// MergeHistograms 将多个节点的桶计数数组逐桶相加
// 合并后的桶数组等价于"所有样本放在同一台机器上统计"的结果
func MergeHistograms(snapshots []HistogramSnapshot) HistogramSnapshot {
    // 所有桶计数求和，然后重新计算百分位
    // Min = min(各节点 Min)，Max = max(各节点 Max)
    // Avg = sum(各节点 sum_ns) / sum(各节点 count)
}
```

---

## 4. Agent 工作节点

### 4.1 职责与边界

- 启动时向 Master 注册自身（IP:Port、能力信息）
- 暴露 HTTP API 接受 Master 的控制命令（启动/停止任务）
- 内部调用 `robot.Manager` 控制压测执行（**库调用，不是子进程**）
- 后台 goroutine 定期将 `monitor.Global().Snapshot()` POST 给 Master
- 定期发送心跳到 Master

### 4.2 代码结构

```
agent/
├── main.go         — 解析配置、注册、启动 HTTP server
├── server.go       — HTTP 路由（/agent/v1/...）
├── handler.go      — 命令处理器（start/stop/status）
├── runner.go       — 管理 robot.Manager 生命周期
├── reporter.go     — 定期将 snapshot POST 到 Master
└── client.go       — Master HTTP 客户端封装
```

### 4.3 Runner：stressbot 库调用

Agent 以**库模式**集成 stressbot，直接调用 `robot.Manager` 的 Go API，而非启动子进程：

```go
// runner.go
type Runner struct {
    mu      sync.Mutex
    mgr     *robot.Manager       // 当前运行的 Manager，nil = 空闲
    cancel  context.CancelFunc
    taskID  string
}

// Start 用任务配置启动压测
func (r *Runner) Start(cfg RunnerConfig) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.mgr != nil {
        return errors.New("已有任务在运行")
    }

    // 1. 初始化 monitor（使用任务配置的 apdexT 等）
    monitor.Init(cfg.MonitorConfig)

    // 2. 加载 flow / proto / lua（从任务配置中解析）
    flow, factory, luaPool, adp, dialer := r.setup(cfg)

    // 3. 创建并启动 Manager
    mgr := robot.NewManager(cfg.ManagerConfig, flow, factory, dialer, luaPool)
    r.mgr = mgr
    r.taskID = cfg.TaskID
    return mgr.StartAll()
}

// Stop 停止压测
func (r *Runner) Stop() error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.mgr == nil {
        return nil
    }
    r.mgr.StopAll()
    r.mgr = nil
    r.taskID = ""
    return nil
}
```

### 4.4 Reporter：指标上报

```go
// reporter.go
type Reporter struct {
    masterURL string        // http://master:8080
    agentID   string
    taskID    string
    interval  time.Duration // 默认 5s
    stopCh    chan struct{}
}

func (r *Reporter) loop() {
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            snap := monitor.Global().Snapshot(nil, 0)
            body, _ := json.Marshal(&MetricsReport{
                AgentID:  r.agentID,
                TaskID:   r.taskID,
                Snapshot: snap,
            })
            // POST /internal/v1/metrics
            http.Post(r.masterURL+"/internal/v1/metrics", "application/json", bytes.NewReader(body))
        case <-r.stopCh:
            return
        }
    }
}
```

### 4.5 Agent 配置文件

```json
// conf/agent.json
{
  "agentID": "",           // 空=自动生成（hostname+随机）
  "name": "agent-gz-01",  // 人读名称
  "tags": ["guangzhou", "zone-1"],
  "listenAddr": ":8081",   // Agent HTTP 监听端口
  "masterURL": "http://192.168.1.100:8080",  // Master 地址
  "maxRobots": 500,
  "reportInterval": "5s",
  "heartbeatInterval": "10s"
}
```

---

## 5. 通信协议设计

### 5.1 协议选型

| 通信方向 | 协议 | 理由 |
|---|---|---|
| 前端 → Master（控制） | REST HTTP | 简单、易调试、防火墙友好 |
| 前端 → Master（指标刷新） | HTTP GET 定时轮询 | 数据固定 5s 更新，轮询与推送效果等价；无状态，天然容错 |
| Agent → Master（上报） | HTTP POST | 无状态，简单可靠；失败可重试 |
| Master → Agent（命令） | HTTP POST | Master 主动推送命令；Agent 需暴露 HTTP |
| Agent → Master（心跳） | HTTP POST | 同上 |

> **选型说明**：指标数据由 Agent 固定每 5s 上报一次，Master 聚合后缓存最新快照。前端以相同间隔轮询 `GET /metrics`，效果与 WebSocket 推送完全等价，却省去了长连接状态管理、断线重连、反向代理升级头配置等全部复杂度。控制命令（启停）用普通 REST 即可，不需要实时通道。

### 5.2 Agent 注册 + 心跳流程

```
Agent 启动
  │
  ├─→ POST /internal/v1/workers/register  → Master 登记，返回确认
  │
  └─→ 每 10s POST /internal/v1/workers/{id}/heartbeat
           Body: { "status": "idle"|"running", "currentTask": "..." }
```

Master 收到注册时：
1. 若 AgentID 已存在 → 更新 `LastSeen` 和状态（重启场景）
2. 否则 → 新增 Worker 条目

### 5.3 任务启动流程

```
前端
  │ POST /api/v1/tasks/{id}/start
  ▼
Master Task Manager
  ├─ 验证任务配置合法
  ├─ 选择目标 Worker（全部 Idle 节点 / 指定节点）
  ├─ 计算每个节点的机器人数分配
  │    总机器人数=1000，3个节点 → 各 334/333/333
  │
  ├─→ POST http://agent-1:8081/agent/v1/tasks/start
  │       Body: StartTaskRequest（含完整 flow.json、config 覆盖、机器人数）
  ├─→ POST http://agent-2:8081/agent/v1/tasks/start
  ├─→ POST http://agent-3:8081/agent/v1/tasks/start
  │
  ├─ 等待所有 Agent 返回 200（超时 30s）
  ├─ Task.Status → running
  └─ 启动 MetricsAggregator（开始合并上报数据）
```

### 5.4 任务停止流程

```
前端
  │ POST /api/v1/tasks/{id}/stop
  ▼
Master
  ├─ Task.Status → stopping
  ├─→ 广播 POST /agent/v1/tasks/stop 到所有关联 Agent
  ├─ 等待所有 Agent 返回 200（超时 30s）
  ├─ Task.Status → stopped
  └─ 导出最终 CSV（可选）
```

### 5.5 指标更新与轮询流程

```
Agent
  └─ 每 5s POST /internal/v1/metrics  → Master Aggregator

Master Aggregator
  └─ 更新 per-agent snapshot 缓存
  └─ 触发合并 → 写入 current[taskID]（最新聚合快照，直接覆盖）
  └─ 追加到 history[taskID] 环形缓冲

前端（监控大盘页面打开时）
  └─ 每 5s GET /api/v1/tasks/{id}/metrics  → 返回 current[taskID]
  └─ 检测响应中 task.status == "stopped" → 停止轮询，展示结束提示
```

> 前端侧实现极简，一个 `setInterval` 即可，无需任何 WebSocket 客户端代码：
> ```typescript
> const poll = setInterval(async () => {
>   const res = await fetch(`/api/v1/tasks/${taskId}/metrics`)
>   const data = await res.json()
>   updateDashboard(data)
>   if (data.task.status === 'stopped' || data.task.status === 'failed') {
>     clearInterval(poll)
>   }
> }, 5000)
> ```

---

## 6. 完整 API 设计

### 6.1 前端 API（Master 对外，前缀 `/api/v1`）

#### Worker 管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/workers` | 列出所有 Worker（含状态）|
| `GET` | `/api/v1/workers/{id}` | Worker 详情 |

响应示例 `GET /api/v1/workers`：
```json
{
  "workers": [
    {
      "id": "agent-gz-01-a3f9",
      "name": "agent-gz-01",
      "addr": "192.168.1.101:8081",
      "tags": ["guangzhou"],
      "status": "running",
      "currentTask": "task_abc123",
      "lastSeen": "2026-04-28T18:00:05Z",
      "caps": { "maxRobots": 500, "version": "1.2.0" }
    }
  ]
}
```

#### 任务管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks` | 创建任务（上传配置） |
| `GET` | `/api/v1/tasks` | 列出所有任务 |
| `GET` | `/api/v1/tasks/{id}` | 任务详情 |
| `PUT` | `/api/v1/tasks/{id}` | 更新任务配置（仅限 created/stopped 状态） |
| `DELETE` | `/api/v1/tasks/{id}` | 删除任务 |
| `POST` | `/api/v1/tasks/{id}/start` | 启动任务 |
| `POST` | `/api/v1/tasks/{id}/stop` | 停止任务 |

`POST /api/v1/tasks` 请求体：
```json
{
  "name": "登录压测-20260428",
  "flow": { /* flow.json 完整内容 */ },
  "config": {
    "totalRobots": 1000,
    "concurrentNum": 50,
    "accountPrefix": "bot_",
    "startNumber": 1,
    "authAddress": "http://192.168.1.200:8888",
    "mainService": "logic",
    "targetWorkerTags": ["guangzhou"]   // 可选：按标签选择 Worker，空=全部
  }
}
```

`POST /api/v1/tasks/{id}/start` 请求体（可选覆盖）：
```json
{
  "workerIDs": ["agent-gz-01-a3f9", "agent-gz-02-b2e1"],  // 空=全部 idle Worker
  "overrideTotalRobots": 200   // 覆盖任务配置中的机器人总数
}
```

#### 指标查询

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/tasks/{id}/metrics` | 当前聚合指标快照（前端每 5s 轮询此接口）|
| `GET` | `/api/v1/tasks/{id}/metrics/history` | 近 N 个周期的快照列表（用于趋势图）|
| `GET` | `/api/v1/tasks/{id}/metrics/export` | 导出 CSV |

`GET /api/v1/tasks/{id}/metrics` 响应（同时携带任务状态，前端据此决定是否停止轮询）：
```json
{
  "taskID": "task_abc123",
  "status": "running",
  "uptimeSeconds": 125.4,
  "workers": {
    "total": 3,
    "active": 3,
    "offline": 0
  },
  "robots": {
    "started": 1000,
    "running": 987,
    "stopped": 13,
    "errored": 0
  },
  "totalActions": 584920,
  "actions": [
    {
      "name": "CreateNormalTeam",
      "sampleCount": 4920,
      "successCount": 4918,
      "failureCount": 2,
      "timeoutCount": 0,
      "successRate": 0.9996,
      "latency": {
        "count": 4918,
        "minMs": 3.0,
        "maxMs": 120.0,
        "avgMs": 6.5,
        "p50Ms": 5.0,
        "p90Ms": 10.0,
        "p95Ms": 15.0,
        "p99Ms": 45.0
      },
      "apdex": 0.98,
      "avgQps": 4.2,
      "errors": [
        { "msg": "dial tcp: connection refused", "count": 2 }
      ]
    }
  ],
  "perWorker": [
    {
      "workerID": "agent-gz-01-a3f9",
      "robotsRunning": 333,
      "totalActions": 195000,
      "lastReportAt": "2026-04-28T18:02:10Z"
    }
  ]
}
```

### 6.2 Agent 上报 API（Master 内部，前缀 `/internal/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/internal/v1/workers/register` | Agent 注册 |
| `POST` | `/internal/v1/workers/{id}/heartbeat` | Agent 心跳 |
| `POST` | `/internal/v1/metrics` | Agent 上报指标快照 |

`POST /internal/v1/workers/register` 请求体：
```json
{
  "agentID": "agent-gz-01-a3f9",
  "name": "agent-gz-01",
  "listenAddr": "192.168.1.101:8081",
  "tags": ["guangzhou"],
  "caps": { "maxRobots": 500, "version": "1.2.0" }
}
```

`POST /internal/v1/metrics` 请求体：
```json
{
  "agentID": "agent-gz-01-a3f9",
  "taskID": "task_abc123",
  "snapshot": { /* monitor.CollectorSnapshot */ }
}
```

### 6.3 Agent 控制 API（Master 调 Agent，前缀 `/agent/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/agent/v1/tasks/start` | 启动任务（Master 下发） |
| `POST` | `/agent/v1/tasks/stop` | 停止任务 |
| `GET` | `/agent/v1/status` | Agent 状态 |
| `GET` | `/agent/v1/metrics` | 当前指标（调试用） |

`POST /agent/v1/tasks/start` 请求体（Master 下发）：
```json
{
  "taskID": "task_abc123",
  "flow": { /* flow.json 完整内容 */ },
  "config": {
    "robotCount": 334,          // 本节点分配的机器人数
    "concurrentNum": 20,
    "accountPrefix": "bot_",
    "startNumber": 1,           // Master 计算好偏移，保证各节点账号不重叠
    "authAddress": "http://192.168.1.200:8888",
    "mainService": "logic"
  },
  "monitorConfig": {
    "enabled": true,
    "apdexT": 100,
    "reportInterval": "5s"
  },
  "protoFiles": { /* map[filename]string，base64 或内联内容 */ },
  "luaScripts": { /* map[filename]string */ }
}
```

> **账号偏移计算**：Master 确保各 Agent 的账号段不重叠。Agent #1 从 `startNumber=1` 起，Agent #2 从 `startNumber=335` 起，以此类推，避免多节点登录同一账号。

---

## 7. 指标汇总算法

### 7.1 延迟直方图合并

现有 `monitor.HistogramSnapshot` 没有导出桶计数数组（只有百分位值），**需要扩展** `CollectorSnapshot` 以包含原始桶数据，才能正确合并：

```go
// 扩展 ActionSnapshot（agent 上报时包含原始桶数据）
type ActionSnapshot struct {
    // ... 现有字段 ...
    LatencyBuckets [numBuckets]int64 `json:"latencyBuckets"` // 新增：原始桶数组
    LatencySumNs   int64             `json:"latencySumNs"`   // 新增：用于重算 avg
    ApdexSatisfied  int64            `json:"apdexSatisfied"` // 新增：用于重算 Apdex
    ApdexTolerating int64            `json:"apdexTolerating"`
}
```

合并算法：

```go
// aggregate/merger.go
func MergeSnapshots(agentSnaps []*CollectorSnapshot, apdexT int) *MergedSnapshot {
    merged := &MergedSnapshot{ApdexT: apdexT}

    perAction := make(map[string]*actionAccumulator)

    for _, snap := range agentSnaps {
        merged.Robots.Running  += snap.Robots.Running
        merged.Robots.Stopped  += snap.Robots.Stopped
        merged.Robots.Errored  += snap.Robots.Errored
        merged.TotalActions    += snap.TotalActions
        merged.Bandwidth.TotalSendBytes += snap.Bandwidth.TotalSendBytes
        merged.Bandwidth.TotalRecvBytes += snap.Bandwidth.TotalRecvBytes

        for _, a := range snap.Actions {
            acc := getOrCreate(perAction, a.Name)
            acc.successCount   += a.SuccessCount
            acc.failureCount   += a.FailureCount
            acc.timeoutCount   += a.TimeoutCount
            acc.sendBytes      += int64(a.AvgSendBytes * float64(a.SuccessCount))
            acc.recvBytes      += int64(a.AvgRecvBytes * float64(a.SuccessCount))
            acc.sumNs          += a.LatencySumNs
            acc.apdexSatisfied += a.ApdexSatisfied
            acc.apdexTolerating += a.ApdexTolerating
            for i, v := range a.LatencyBuckets {
                acc.buckets[i] += v
            }
            if a.Latency.MinMs < acc.minMs || acc.minMs == 0 {
                acc.minMs = a.Latency.MinMs
            }
            if a.Latency.MaxMs > acc.maxMs {
                acc.maxMs = a.Latency.MaxMs
            }
        }
    }

    // 从 accumulator 重新计算百分位和 Apdex
    for name, acc := range perAction {
        total := acc.successCount + acc.failureCount + acc.timeoutCount
        snap := ActionSnapshot{
            Name:         name,
            SuccessCount: acc.successCount,
            // ...
            Apdex: (float64(acc.apdexSatisfied) + float64(acc.apdexTolerating)*0.5) / float64(total),
            Latency: recalcHistogram(acc), // 从桶重算百分位
        }
        merged.Actions = append(merged.Actions, snap)
    }
    return merged
}
```

### 7.2 历史快照缓冲

```go
// aggregate/history.go
// 环形缓冲，保存最近 N 个聚合快照（默认 N=60，即 5min @ 5s间隔）
type HistoryBuffer struct {
    mu       sync.RWMutex
    buf      []*TimedSnapshot
    capacity int
    head     int
    size     int
}

type TimedSnapshot struct {
    Timestamp time.Time
    Snapshot  *MergedSnapshot
}
```

---

## 8. 数据存储方案

### 8.1 Master 内存存储

Master 采用**纯内存存储**（不依赖数据库）：

```go
type MemStore struct {
    mu       sync.RWMutex
    workers  map[string]*WorkerInfo    // id → worker
    tasks    map[string]*Task          // id → task
    snapshots map[string][]*TimedSnapshot // taskID → history
    current  map[string]*MergedSnapshot  // taskID → latest
}
```

**持久化策略**：
- 任务配置在创建时写入本地 JSON 文件（`data/tasks/{id}.json`）
- Master 重启时从 `data/tasks/` 目录恢复任务列表（状态重置为 `stopped`）
- Worker 注册信息不持久化（Agent 重启会重新注册）

### 8.2 Agent 无状态

Agent 不持久化任何数据，所有运行状态在内存中。任务配置由 Master 在启动时下发。

---

## 9. Master 配置文件

```json
// conf/master.json
{
  "listenAddr": ":8080",
  "dataDir": "data",           // 任务配置持久化目录
  "workerTimeout": "30s",      // Worker 心跳超时时间
  "aggregateInterval": "5s",   // Agent 上报后触发聚合的最小间隔（防止高频上报时频繁重算）
  "auth": {
    "enabled": false,           // 简单 API Key 鉴权
    "apiKey": "change-me"
  },
  "cors": {
    "allowOrigins": ["http://localhost:3000", "http://192.168.1.50:3000"]
  }
}
```

---

## 10. 新增代码结构

```
stressbot/
├── cmd/
│   ├── stressbot/   (已有 — 单机模式，不修改)
│   ├── validate/    (已有)
│   ├── master/      (新增)
│   │   └── main.go
│   └── agent/       (新增)
│       └── main.go
├── master/          (新增)
│   ├── server.go
│   ├── handler/
│   │   ├── frontend.go
│   │   └── internal.go
│   ├── registry/
│   │   └── worker.go
│   ├── task/
│   │   ├── manager.go
│   │   └── distributor.go
│   ├── aggregate/
│   │   ├── merger.go     — 合并 + 缓存最新 current snapshot
│   │   └── history.go    — 环形历史缓冲（供 /metrics/history）
│   └── store/
│       ├── memory.go
│       └── file.go
├── agent/           (新增)
│   ├── server.go
│   ├── handler.go
│   ├── runner.go    — 调用 robot.Manager，monitor.Init 等
│   ├── reporter.go  — 定期 POST snapshot 到 Master
│   └── client.go    — Master HTTP 客户端
├── monitor/         (已有 — 复用 CollectorSnapshot，小扩展)
│   └── snapshot.go  ← 新增 LatencyBuckets/LatencySumNs 字段
└── conf/
    ├── master.json  (新增)
    └── agent.json   (新增)
```

**已有代码改动最小化**：
- `monitor/snapshot.go`：`ActionSnapshot` 新增 3 个字段（`LatencyBuckets`、`LatencySumNs`、`ApdexSatisfied/Tolerating`）
- `monitor/collector.go`：`Snapshot()` 方法填充上述新字段
- `engine/`、`robot/`、`network/`：**不修改**

---

## 11. 关键问题的处理方案

### 11.1 Agent 账号不重叠

Master 在下发 `StartTaskRequest` 时计算每个 Agent 的 `startNumber` 偏移：

```go
func distributeRobots(total int, workers []*WorkerInfo) []robotAlloc {
    // 按 maxRobots 加权分配
    allocs := make([]robotAlloc, len(workers))
    offset := 1
    for i, w := range workers {
        count := total * w.Caps.MaxRobots / totalCap
        allocs[i] = robotAlloc{workerID: w.ID, count: count, startNumber: offset}
        offset += count
    }
    return allocs
}
```

### 11.2 Agent 离线处理

心跳超时后：
1. Worker.Status → `Offline`
2. 若该 Worker 上有 Running 任务，任务状态不变（其他 Worker 继续跑）
3. 前端 `perWorker` 列表中该 Worker 标记为 `offline`
4. Master 不再向该 Worker 下发停止命令（已离线）

### 11.3 Master 重启恢复

1. 从 `data/tasks/` 恢复任务列表，状态统一设为 `stopped`（不自动重启）
2. 等待 Agent 重新注册（Agent 启动时发心跳注册）
3. 前端可手动重新 start 任务

### 11.4 前端网络抖动处理

轮询天然容错：某次 `fetch` 超时或失败，前端记录连续失败次数，超过阈值（如 3 次）时显示"连接中断"提示；网络恢复后下一次轮询自动成功，无需任何重连逻辑。Master 始终缓存最新快照，前端任何时刻发起请求都能拿到当前数据，不存在"错过推送"的问题。

---

## 12. 部署方案

### 12.1 最小化部署

```
[服务器 A：192.168.1.100]
  Master 进程: ./master -config conf/master.json   # 端口 8080
  Agent 进程:  ./agent  -config conf/agent.json    # 端口 8081

[服务器 B：192.168.1.101]
  Agent 进程:  ./agent  -config conf/agent.json    # 端口 8081

[服务器 C：192.168.1.102]
  Agent 进程:  ./agent  -config conf/agent.json    # 端口 8081

[前端（任意机器或 CDN）]
  Web 界面 → 连接 http://192.168.1.100:8080
```

### 12.2 构建命令

```bash
# 编译 Agent
go build -o agent.exe ./cmd/agent

# 编译 Master
go build -o master.exe ./cmd/master

# 启动 Master
./master.exe -config conf/master.json

# 启动 Agent（各压测服务器）
./agent.exe -config conf/agent.json
```

### 12.3 防火墙要求

| 方向 | 端口 | 说明 |
|---|---|---|
| 前端 → Master | 8080/tcp | REST API（控制 + 指标轮询） |
| Agent → Master | 8080/tcp | 注册、心跳、上报 |
| Master → Agent | 8081/tcp | 命令下发 |

---

## 13. 实施顺序

### Phase 1：Agent 核心（约 2 天）

1. `agent/runner.go` — 封装 `robot.Manager` 启停
2. `agent/reporter.go` — 定期上报 snapshot（Master 端用 stub 接收即可）
3. `agent/server.go` + `agent/handler.go` — 接受 start/stop 命令
4. `cmd/agent/main.go` — 完整流程打通（命令行启动）

验证：`curl POST /agent/v1/tasks/start` → 机器人开始运行，`/agent/v1/metrics` 能看到数据

### Phase 2：Master 骨架（约 2 天）

5. `master/store/memory.go` — 内存存储
6. `master/registry/worker.go` — Worker 注册 + 心跳检测
7. `master/task/manager.go` — 任务 CRUD + 状态机
8. `master/handler/internal.go` — 接受 Agent 注册/心跳/上报
9. `master/handler/frontend.go` — 任务管理 REST API（不含指标汇总）

验证：Agent 注册到 Master，`GET /api/v1/workers` 能看到节点列表，能通过 API 触发任务启动

### Phase 3：指标汇总（约 1 天）

10. `monitor/snapshot.go` — 新增桶数据字段
11. `master/aggregate/merger.go` — 多节点 snapshot 合并，缓存 current
12. `master/aggregate/history.go` — 环形历史缓冲
13. `master/handler/frontend.go` — 补全指标查询 API（`/metrics`、`/metrics/history`）

验证：`curl GET /api/v1/tasks/{id}/metrics` 能拿到聚合数据，P99/Apdex 数值正确；前端每 5s 刷新监控大盘数据符合预期

### Phase 4：持久化 + 完善（约 0.5 天）

15. `master/store/file.go` — 任务配置落盘、重启恢复
16. API 鉴权（简单 API Key）
17. 前端联调（补全 `design-web-editor.md` 中预留的接口）
18. 集成测试：3 个 Agent + 1 个 Master + 前端完整联调

---

## 14. 与 Web 编辑器的接口对齐

`design-web-editor.md` 中预留了以下占位接口，本系统实现后直接填充：

| 编辑器占位 | 对应 Master API |
|---|---|
| 保存/下发 flow.json | `PUT /api/v1/tasks/{id}` 更新配置 |
| 启动压测按钮 | `POST /api/v1/tasks/{id}/start` |
| 停止压测按钮 | `POST /api/v1/tasks/{id}/stop` |
| 节点指标显示槽 | `GET /api/v1/tasks/{id}/metrics`（5s 轮询）中 `actions[].name` 对应节点名 |
| 机器人状态面板 | 同轮询响应中 `robots` 字段 |
| Worker 列表 | `GET /api/v1/workers` |
