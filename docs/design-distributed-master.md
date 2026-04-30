# 分布式压测管理系统 — 架构设计

> **目标**：在现有单机 stressbot 基础上，构建一套分布式压测管理系统，支持多台压测服务器协同执行、集中管控、实时压测+系统指标汇总，并通过 REST API 对接 Web 前端。

---

## 1. 系统概述

### 1.1 设计目标

- **分布式执行**：多个 Agent 节点并行执行压测，模拟大规模用户负载
- **集中管理**：Admin 统一调度任务、聚合指标、服务前端
- **零侵入**：现有单机模式完全兼容，Agent 模式通过 `agent.enabled` 开关切换，**不新增二进制**
- **完整可观测性**：除压测指标（成功率/延迟/Apdex）外，还采集 Agent 所在物理机的系统指标（CPU/内存/网络/线程/协程）
- **简单可靠**：全 HTTP 通信，无外部依赖（消息队列、服务发现、数据库等）

### 1.2 与现有代码的关系

```
现有代码（不修改）                    新增/修改
──────────────────                   ──────────────────
engine/                              admin/           — Admin 服务端
robot/Manager（已支持 startNumber）  agent/agent.go   — Agent 客户端
network/、adapter/、protox/、script/ agent/sysmon.go  — 系统监控采集
monitor/（小幅扩展）                 agent/upgrader.go — Agent 进程升级
                                     cmd/admin/       — 编译产物 admin.exe
                                     cmd/agent/       — 编译产物 agent.exe
                                                        （重命名自 cmd/stressbot）
                                     cmd/launcher/    — 编译产物 agent-launcher.exe
```

---

## 2. 系统拓扑

```
+───────────────────+
│   Web Frontend    │   React + Ant Design + Vite
│   :5173 (dev)     │   vite proxy → admin :8080
+────────┬──────────+
         │ /api/*（HTTP 轮询）
         ▼
+───────────────────+
│   Admin Server    │   cmd/admin/main.go
│   :8080           │   任务管理 + Agent 注册 + 压测/系统指标聚合
+──┬──────────┬─────+
   │HTTP Push │HTTP Push（命令下发）
   ▼          ▼
+────────+ +────────+       +────────+
│Agent 1 │ │Agent 2 │  ...  │Agent N │   每个 Agent 独立采集
+────────+ +────────+       +────────+   所在主机的系统指标
    │           │                │
    +────────── TCP/UDP ─────────+
               → 游戏服务器
```

### 三个角色

| 角色 | 进程 | 职责 |
|---|---|---|
| **Admin** | `cmd/admin/main.go`（新） | 接受前端请求、管理任务生命周期、Agent 注册表、聚合压测+系统指标 |
| **Agent** | `cmd/agent/main.go`（agent 模式） | 向 Admin 注册、接收任务、执行压测、采集系统指标、上报数据 |
| **Frontend** | `web/`（扩展现有） | Dashboard 监控、任务管理、Agent 管理、系统资源大盘 |

---

## 3. 关键设计决策

### 3.1 通信方式：HTTP Push（Agent→Admin）

Agent 主动向 Admin 推送数据（注册、心跳、指标），Admin 不需要主动拉取 Agent 状态。

### 3.2 前端获取指标：HTTP 轮询（Frontend→Admin）

前端每 5s 轮询 `GET /api/metrics`，Admin 返回已缓存的最新聚合快照。轮询效果与 WebSocket 推送等价（数据本就 5s 一批），却无需长连接管理、断线重连、反向代理 Upgrade 头配置。历史趋势数据（折线图）由**前端自己维护**（每次轮询结果 push 到本地数组），无需后端存储。

### 3.3 Agent 发现：配置文件指定

Agent 配置中指定 Admin 地址（`agent.adminAddr`），启动时 POST 注册，注册失败时指数退避重试。无需 etcd/consul。

### 3.4 任务分配：Admin 切分账号范围

Admin 将总账号范围均匀切分给各 Agent，`robot.Manager` 的 `ManagerConfig` 已支持外部指定 `StartNumber` 和 `Count`，**无需修改 robot 包**。

| Agent | startNumber | count |
|---|---|---|
| Agent1 | 1 | 2000 |
| Agent2 | 2001 | 2000 |
| Agent3 | 4001 | 2000 |

### 3.5 配置下发：Agent 从 Admin 拉取（Pull 模式）

Admin 将 flow.json、proto 文件、Lua 脚本打包存储为 `TaskConfig`。启动任务时 Admin 只向 Agent 下发轻量的 `TaskAssignment`（含 `configUrl`），Agent 按需 `GET /api/agent/config/{taskId}` 拉取完整配置包。

**理由**：配置包可能较大（多 proto 文件 + lua 脚本），不适合内联在启动命令 body 中。

### 3.6 系统监控独立采集与上报

Agent 启动时初始化 `agent/sysmon.go` 系统监控器，定期采集 CPU、内存、网络、线程、协程等指标。**压测指标和系统指标走两个独立端点上报**：
- `POST /api/agent/stress` — 仅任务运行时推送，频率默认 5s
- `POST /api/agent/system` — **始终推送**（含空闲时），频率默认 5s，可独立调整

**理由**：
- 物理机指标对压测结果至关重要（CPU 打满会拖慢压测客户端，影响延迟统计的真实性）
- 两类数据生命周期不同：stress 强耦合于任务，system 始终需要观察 Agent 健康
- **故障隔离**：stress 计算异常或慢操作不影响 system 上报
- **频率独立**：未来可将 system 调高到 1s 一次以观察 CPU 抖动，stress 保持 5s 保证百分位样本量
- **职责单一**：每个 endpoint 一个意图，符合 REST 设计；Admin 端可分别埋点观察健康度

### 3.7 独立模式兼容

`agent.enabled` 默认为 `false`。未配置 agent 段时，stressbot 行为与当前完全一致——直接创建 Manager、启动机器人、等待信号退出，**不引入任何额外开销**。

---

## 4. 目录结构

### 新增文件

```
admin/
  admin.go          — AdminServer 结构体、HTTP 服务器启动、路由注册
  types.go          — 共享数据类型（Task、AgentNode、Assignment、SystemSnapshot 等）
  task.go           — Task 状态机 + 内存存储（CRUD）
  agent.go          — AgentNode 注册表、健康检测、心跳超时
  assignment.go     — 任务分配算法（均匀切分账号范围）
  aggregator.go     — MetricsAggregator：压测指标合并 + 系统指标聚合
  handlers.go       — 所有 /api/ HTTP handler

agent/
  agent.go          — Agent 进程：注册、心跳、接收任务、上报指标、管理本地 Manager
  sysmon.go         — SystemMonitor：CPU/内存/网络/线程/协程 采集
  upgrader.go       — Agent 进程升级处理：下载、校验、drain、写标记、退出

cmd/admin/
  main.go           — Admin 进程入口（编译产物 admin.exe）

cmd/launcher/
  main.go           — Launcher 进程入口（编译产物 agent-launcher.exe，独立小程序，~1MB）
                     职责：spawn Agent 进程、监控 exit code、升级时替换二进制
                     不联网、不依赖 admin/agent/engine/network 等业务包
```

### 修改文件

| 文件 | 改动范围 |
|---|---|
| `cmd/agent/main.go` | Config 新增 `Agent` 配置段；`agent.enabled=true` 时启动 Agent 模式，否则单机模式（无 `--role` 标志） |
| `monitor/histogram.go` | `HistogramSnapshot` 新增 `SumNs` 和 `BucketCounts` 字段（`omitempty`，向后兼容） |
| `web/vite.config.ts` | proxy target 从 `localhost:6060` 改为 `localhost:8080` |

### 新增依赖

| 依赖 | 用途 |
|---|---|
| `github.com/shirou/gopsutil/v4` | 跨平台采集 CPU/内存/网络/进程系统指标，纯 Go 实现，无 cgo |

### 不修改的包

`engine/`、`robot/`、`network/`、`adapter/`、`protox/`、`script/`、`state/` 全部原样复用。

---

## 5. Admin API 设计

### 5.1 前端 API（`/api/`）

#### 任务管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/tasks` | 创建任务（含配置包上传） |
| `GET` | `/api/tasks` | 列出所有任务 |
| `GET` | `/api/tasks/{id}` | 任务详情 + 当前聚合指标 |
| `PUT` | `/api/tasks/{id}` | 更新任务配置（仅 pending/stopped 状态） |
| `POST` | `/api/tasks/{id}/start` | 启动任务（分配 Agent、下发任务） |
| `POST` | `/api/tasks/{id}/stop` | 停止任务 |
| `DELETE` | `/api/tasks/{id}` | 删除任务（仅 stopped 状态） |

#### Agent 管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/agents` | 列出所有 Agent 及简要状态 |
| `GET` | `/api/agents/{id}` | Agent 详情（含完整系统指标、当前任务） |

#### 压测指标

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/metrics` | 全局聚合压测指标（前端每 5s 轮询，含任务状态） |
| `GET` | `/api/metrics/agents` | per-agent 压测指标明细（不聚合） |
| `GET` | `/api/metrics/export` | 导出聚合 CSV |

#### 系统指标（新增）

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/system` | 集群系统资源总览（CPU 平均、总内存、总网络速率等） |
| `GET` | `/api/system/agents` | 所有 Agent 的系统指标 per-agent 明细 |
| `GET` | `/api/system/agents/{id}` | 单个 Agent 的最新系统指标快照 |

**轮询约定**：`GET /api/metrics` 响应中包含 `task.status` 字段，前端检测到 `stopped/failed` 时停止压测指标轮询。系统指标轮询独立维持（用户始终关心 Agent 健康状态）。

```typescript
// 前端历史维护（压测指标）
const bizHistory = useRef<BizSnapshot[]>([])
const { data: metrics } = useSWR('/api/metrics', fetcher, {
  refreshInterval: 5000,
  onSuccess: snap => {
    bizHistory.current.push(snap)
    if (bizHistory.current.length > 120) bizHistory.current.shift()  // 10 分钟
  }
})

// 前端历史维护（系统指标，独立轮询）
const sysHistory = useRef<SysSnapshot[]>([])
const { data: system } = useSWR('/api/system', fetcher, {
  refreshInterval: 5000,
  onSuccess: snap => {
    sysHistory.current.push(snap)
    if (sysHistory.current.length > 120) sysHistory.current.shift()
  }
})
```

### 5.2 Agent 上报 API（`/api/agent/`）

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/agent/register` | Agent 注册（name、address、maxBots、staticInfo） |
| `POST` | `/api/agent/heartbeat` | 心跳 + 当前 status |
| `POST` | `/api/agent/stress` | 推送压测指标（CollectorSnapshot），仅任务运行时 |
| `POST` | `/api/agent/system` | 推送系统指标（SystemSnapshot），始终上报（含空闲时） |
| `GET` | `/api/agent/task` | 轮询拉取当前任务分配（Push 失败时的恢复手段） |
| `GET` | `/api/agent/config/{taskId}` | 拉取配置包（flow.json + proto + scripts） |
| `POST` | `/api/agent/task/{id}/done` | 报告任务正常完成 |
| `POST` | `/api/agent/task/{id}/failed` | 报告任务失败（含错误信息） |

### 5.3 Agent 控制端点（Admin 主动 Push）

Agent 在 `listenAddr` 启动轻量 HTTP 服务器接受 Admin 命令。与 Agent 主动轮询形成**双通道**：Push 保证低延迟，Poll 提供 Admin 重启后的恢复能力。

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/task/assign` | 接收任务分配 |
| `POST` | `/task/stop` | 停止当前任务 |
| `GET` | `/health` | 健康检查 |

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
                  ↘ failed（所有 Agent 均离线或失败）
running → failed（Agent 主动报告 /task/{id}/failed）
```

### 6.2 TaskConfig（配置包）

```go
type TaskConfig struct {
    AccountPrefix     string            `json:"accountPrefix"`
    StartNumber       int               `json:"startNumber"`
    MainService       string            `json:"mainService"`
    ConcurrentNum     int               `json:"concurrentNum"`
    AuthAddress       string            `json:"authAddress"`
    AuthExtra         map[string]string `json:"authExtra"`
    HeartbeatInterval string            `json:"heartbeatInterval"`
    TCPTimeout        string            `json:"tcpTimeout"`
    HTTPTimeout       string            `json:"httpTimeout"`
    ApdexT            int               `json:"apdexT"`

    FlowJSON          json.RawMessage   `json:"flowJson"`
    AdapterLua        string            `json:"adapterLua"`
    ProtoFiles        map[string]string `json:"protoFiles"`
    LuaScripts        map[string]string `json:"luaScripts"`
}
```

### 6.3 AgentNode

```go
type AgentNode struct {
    ID            string         `json:"id"`
    Name          string         `json:"name"`
    Address       string         `json:"address"`
    Status        AgentStatus    `json:"status"`
    MaxBots       int            `json:"maxBots"`
    CurrentTaskID string         `json:"currentTaskId,omitempty"`
    LastHeartbeat time.Time      `json:"lastHeartbeat"`
    StaticInfo    *StaticInfo    `json:"staticInfo,omitempty"`    // 启动时一次性上报
    LatestSystem  *SystemSnapshot `json:"latestSystem,omitempty"` // 最新系统指标快照
    RegisteredAt  time.Time      `json:"registeredAt"`
}

type AgentStatus string
const (
    AgentIdle      AgentStatus = "idle"
    AgentBusy      AgentStatus = "busy"
    AgentUnhealthy AgentStatus = "unhealthy"  // 心跳超时 1×TTL
    AgentOffline   AgentStatus = "offline"    // 心跳超时 2×TTL
)

// StaticInfo Agent 启动时一次性上报，不变
type StaticInfo struct {
    Hostname    string `json:"hostname"`
    OS          string `json:"os"`
    Arch        string `json:"arch"`
    NumCPU      int    `json:"numCPU"`
    TotalMemMB  int64  `json:"totalMemMB"`
    BootTime    int64  `json:"bootTime"`     // unix timestamp
    GoVersion   string `json:"goVersion"`
    AppVersion  string `json:"appVersion"`   // stressbot 版本
}
```

### 6.4 Assignment

```go
type Assignment struct {
    AgentID     string `json:"agentId"`
    StartNumber int    `json:"startNumber"`
    Count       int    `json:"count"`
}

type TaskAssignment struct {
    TaskID            string            `json:"taskId"`
    TaskName          string            `json:"taskName"`
    StartNumber       int               `json:"startNumber"`
    Count             int               `json:"count"`
    AccountPrefix     string            `json:"accountPrefix"`
    ConcurrentNum     int               `json:"concurrentNum"`
    MainService       string            `json:"mainService"`
    AuthAddress       string            `json:"authAddress"`
    AuthExtra         map[string]string `json:"authExtra"`
    HeartbeatInterval string            `json:"heartbeatInterval"`
    TCPTimeout        string            `json:"tcpTimeout"`
    HTTPTimeout       string            `json:"httpTimeout"`
    ApdexT            int               `json:"apdexT"`
    ConfigURL         string            `json:"configUrl"`
}
```

---

## 7. 压测指标聚合

Agent 通过两个独立端点上报压测指标和系统指标。Admin 维护 `map[agentID]*CollectorSnapshot`（压测）和 `map[agentID]*SystemSnapshot`（系统）两份缓存，前端请求时实时聚合。

### 7.1 压测指标聚合规则

| 指标类型 | 聚合方式 |
|---|---|
| 计数器（success/failure/timeout/skipped） | **求和** |
| 正在执行数（executing） | **求和** |
| 机器人状态（started/running/stopped/errored） | **求和** |
| 连接指标（established/failed/dropped） | **求和** |
| 带宽（压测产生的网络发送/接收字节） | **求和** |
| 延迟直方图 | **合并桶计数** → 重算百分位（见 §7.2） |
| Apdex | 用合并后的 satisfied/tolerating 总和**重新计算** |
| 错误分布 | 相同 errorMsg 的计数**相加** |

### 7.2 延迟直方图合并

`HistogramSnapshot` 新增两个字段支持精确合并，加 `omitempty` 向后兼容：

```go
type HistogramSnapshot struct {
    Count int64   `json:"count"`
    MinMs float64 `json:"minMs"`
    MaxMs float64 `json:"maxMs"`
    AvgMs float64 `json:"avgMs"`
    P50Ms float64 `json:"p50Ms"`
    P90Ms float64 `json:"p90Ms"`
    P95Ms float64 `json:"p95Ms"`
    P99Ms float64 `json:"p99Ms"`

    // 新增（omitempty，向后兼容单机模式）
    SumNs        int64   `json:"sumNs,omitempty"`
    BucketCounts []int64 `json:"bucketCounts,omitempty"`  // 长度 = numBuckets(17)
}
```

合并算法：

```go
func MergeHistograms(snaps []HistogramSnapshot) HistogramSnapshot {
    var merged HistogramSnapshot
    merged.MinMs = math.MaxFloat64
    buckets := make([]int64, numBuckets)

    for _, s := range snaps {
        if s.Count == 0 { continue }
        merged.Count += s.Count
        merged.SumNs += s.SumNs
        if s.MinMs < merged.MinMs { merged.MinMs = s.MinMs }
        if s.MaxMs > merged.MaxMs { merged.MaxMs = s.MaxMs }
        for i, c := range s.BucketCounts {
            buckets[i] += c
        }
    }
    if merged.Count > 0 {
        merged.AvgMs = float64(merged.SumNs) / float64(merged.Count) / 1e6
        merged.P50Ms = percentileFromBuckets(buckets, merged.Count, 0.50)
        merged.P90Ms = percentileFromBuckets(buckets, merged.Count, 0.90)
        merged.P95Ms = percentileFromBuckets(buckets, merged.Count, 0.95)
        merged.P99Ms = percentileFromBuckets(buckets, merged.Count, 0.99)
    }
    return merged
}
```

**精度保证**：所有 Agent 使用相同的 17 个固定桶，合并桶计数后百分位精度与单节点一致。不能将各 Agent 百分位值直接加权平均，会引入显著误差。

### 7.3 压测聚合响应格式

`GET /api/metrics` 响应：

```json
{
  "task": { "id": "abc123", "status": "running", "uptimeSeconds": 125.4 },
  "robots": { "started": 1000, "running": 987, "stopped": 13, "errored": 0 },
  "totalActions": 584920,
  "actions": [ /* 同单机 CollectorSnapshot.actions 格式 */ ],
  "agents": [
    { "id": "a1", "status": "busy", "robotsRunning": 334, "lastReportAt": "..." },
    { "id": "a2", "status": "busy", "robotsRunning": 333, "lastReportAt": "..." }
  ]
}
```

---

## 8. Agent 系统监控

每个 Agent 独立采集所在物理机的系统指标，与压测指标一起上报。这部分数据回答两个核心问题：
1. **Agent 自身是否健康**？CPU 打满 / 内存吃光 / 文件描述符耗尽都会拖慢压测客户端，导致延迟统计失真
2. **集群整体压测能力**？聚合后的总 CPU/内存/网络速率帮助判断是否需要扩容 Agent

### 8.1 采集指标清单

| 类别 | 指标 | 来源 | 单位 |
|---|---|---|---|
| **CPU** | 总体使用率 | `gopsutil/cpu.Percent(interval=0)` | % |
|  | 每核使用率 | `gopsutil/cpu.Percent(perCPU=true)` | %[] |
|  | 核数 | `runtime.NumCPU()` | 个 |
|  | 系统负载（Linux/macOS） | `gopsutil/load.Avg` | 1m/5m/15m |
| **内存** | 系统总内存 | `gopsutil/mem.VirtualMemory.Total` | MB |
|  | 系统已用内存 | `gopsutil/mem.VirtualMemory.Used` | MB |
|  | 系统使用率 | 同上 `.UsedPercent` | % |
|  | 进程 RSS | `gopsutil/process.MemoryInfo.RSS` | MB |
|  | Go 堆分配 | `runtime.MemStats.HeapAlloc` | MB |
|  | Go Sys 总占用 | `runtime.MemStats.Sys` | MB |
| **网络** | 累计发送字节（系统） | `gopsutil/net.IOCounters.BytesSent` | bytes |
|  | 累计接收字节（系统） | `gopsutil/net.IOCounters.BytesRecv` | bytes |
|  | 发送速率 | （当前累计 - 上次累计）/ 时间差 | KB/s |
|  | 接收速率 | 同上 | KB/s |
| **进程线程** | OS 线程数 | `gopsutil/process.NumThreads` | 个 |
| **Go 协程** | goroutine 数 | `runtime.NumGoroutine()` | 个 |
| **GC** | GC 总次数 | `runtime.MemStats.NumGC` | 次 |
|  | GC 平均暂停 | `runtime.MemStats.PauseTotalNs / NumGC` | ms |
| **文件描述符** | 打开的 FD 数（仅 Linux/macOS） | `gopsutil/process.NumFDs` | 个 |

### 8.2 SystemSnapshot 数据结构

```go
type SystemSnapshot struct {
    Timestamp time.Time `json:"timestamp"`

    // CPU
    CPUPercent float64   `json:"cpuPercent"`
    CPUPerCore []float64 `json:"cpuPerCore,omitempty"`
    LoadAvg1   float64   `json:"loadAvg1,omitempty"`
    LoadAvg5   float64   `json:"loadAvg5,omitempty"`
    LoadAvg15  float64   `json:"loadAvg15,omitempty"`

    // 内存（单位：MB）
    MemTotalMB    int64   `json:"memTotalMB"`
    MemUsedMB     int64   `json:"memUsedMB"`
    MemPercent    float64 `json:"memPercent"`
    ProcessRSSMB  int64   `json:"processRssMB"`
    ProcessHeapMB float64 `json:"processHeapMB"`
    ProcessSysMB  float64 `json:"processSysMB"`

    // 进程 / 运行时
    NumGoroutine int     `json:"numGoroutine"`
    NumThread    int     `json:"numThread"`
    NumFD        int     `json:"numFd,omitempty"`
    GCCount      uint32  `json:"gcCount"`
    GCPauseAvgMs float64 `json:"gcPauseAvgMs,omitempty"`

    // 网络（系统级，所有网卡聚合）
    NetSendTotalMB float64 `json:"netSendTotalMB"`
    NetRecvTotalMB float64 `json:"netRecvTotalMB"`
    NetSendKBps    float64 `json:"netSendKBps"`
    NetRecvKBps    float64 `json:"netRecvKBps"`
}
```

### 8.3 SystemMonitor 实现

```go
// agent/sysmon.go
type SystemMonitor struct {
    interval time.Duration
    proc     *process.Process

    mu       sync.RWMutex
    latest   SystemSnapshot

    prevNetSent uint64
    prevNetRecv uint64
    prevTime    time.Time
    stopCh      chan struct{}
}

func NewSystemMonitor(interval time.Duration) (*SystemMonitor, error) {
    proc, err := process.NewProcess(int32(os.Getpid()))
    if err != nil {
        return nil, err
    }
    return &SystemMonitor{
        interval: interval,
        proc:     proc,
        stopCh:   make(chan struct{}),
    }, nil
}

func (s *SystemMonitor) Start() {
    go s.loop()
}

func (s *SystemMonitor) loop() {
    s.collect()  // 立即采集一次，避免首次上报为空
    ticker := time.NewTicker(s.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            s.collect()
        case <-s.stopCh:
            return
        }
    }
}

func (s *SystemMonitor) collect() {
    snap := SystemSnapshot{Timestamp: time.Now()}

    // CPU（非阻塞）
    if cpus, err := cpu.Percent(0, false); err == nil && len(cpus) > 0 {
        snap.CPUPercent = cpus[0]
    }
    if cores, err := cpu.Percent(0, true); err == nil {
        snap.CPUPerCore = cores
    }
    if loads, err := load.Avg(); err == nil {
        snap.LoadAvg1, snap.LoadAvg5, snap.LoadAvg15 = loads.Load1, loads.Load5, loads.Load15
    }

    // 内存
    if vm, err := mem.VirtualMemory(); err == nil {
        snap.MemTotalMB = int64(vm.Total / 1024 / 1024)
        snap.MemUsedMB  = int64(vm.Used / 1024 / 1024)
        snap.MemPercent = vm.UsedPercent
    }
    if mi, err := s.proc.MemoryInfo(); err == nil {
        snap.ProcessRSSMB = int64(mi.RSS / 1024 / 1024)
    }

    // Go 运行时
    var ms runtime.MemStats
    runtime.ReadMemStats(&ms)
    snap.ProcessHeapMB = float64(ms.HeapAlloc) / 1024 / 1024
    snap.ProcessSysMB  = float64(ms.Sys) / 1024 / 1024
    snap.NumGoroutine  = runtime.NumGoroutine()
    snap.GCCount       = ms.NumGC
    if ms.NumGC > 0 {
        snap.GCPauseAvgMs = float64(ms.PauseTotalNs/uint64(ms.NumGC)) / 1e6
    }
    if n, err := s.proc.NumThreads(); err == nil {
        snap.NumThread = int(n)
    }
    if n, err := s.proc.NumFDs(); err == nil {
        snap.NumFD = int(n)
    }

    // 网络（速率 = 累计差值 / 时间差）
    if io, err := net.IOCounters(false); err == nil && len(io) > 0 {
        sent, recv := io[0].BytesSent, io[0].BytesRecv
        snap.NetSendTotalMB = float64(sent) / 1024 / 1024
        snap.NetRecvTotalMB = float64(recv) / 1024 / 1024
        if !s.prevTime.IsZero() {
            dt := time.Since(s.prevTime).Seconds()
            snap.NetSendKBps = float64(sent-s.prevNetSent) / 1024 / dt
            snap.NetRecvKBps = float64(recv-s.prevNetRecv) / 1024 / dt
        }
        s.prevNetSent, s.prevNetRecv, s.prevTime = sent, recv, time.Now()
    }

    s.mu.Lock()
    s.latest = snap
    s.mu.Unlock()
}

func (s *SystemMonitor) Snapshot() SystemSnapshot {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.latest
}
```

### 8.4 上报报文结构

Agent 用两个独立端点分别推送两类报文：

```go
// POST /api/agent/stress（仅任务运行时）
type StressReport struct {
    AgentID  string                     `json:"agentId"`
    TaskID   string                     `json:"taskId"`
    Snapshot *monitor.CollectorSnapshot `json:"snapshot"`
}

// POST /api/agent/system（始终上报）
type SystemReport struct {
    AgentID  string          `json:"agentId"`
    Snapshot *SystemSnapshot `json:"snapshot"`
}
```

> **关键点**：即使 Agent 处于 `idle` 状态（无运行任务），系统指标依然每 5s 上报到 `/api/agent/system`。这样 Admin 始终能看到所有 Agent 的健康状况，便于运维。压测指标 `/api/agent/stress` 仅在任务期间推送，任务停止后该循环退出。

### 8.5 集群系统聚合规则

`GET /api/system` 响应：

```go
type ClusterSystem struct {
    Timestamp     time.Time   `json:"timestamp"`
    AgentCount    int         `json:"agentCount"`        // 在线 Agent 数

    // CPU 聚合
    AvgCPUPercent float64     `json:"avgCpuPercent"`     // 加权平均（按核数）
    MaxCPUPercent float64     `json:"maxCpuPercent"`     // 最高的那台
    HotAgentID    string      `json:"hotAgentId,omitempty"` // CPU 最高的 Agent ID

    // 内存聚合（求和）
    TotalMemMB    int64       `json:"totalMemMB"`
    UsedMemMB     int64       `json:"usedMemMB"`
    AvgMemPercent float64     `json:"avgMemPercent"`

    // 网络聚合（求和）
    NetSendKBps   float64     `json:"netSendKBps"`
    NetRecvKBps   float64     `json:"netRecvKBps"`

    // 进程聚合（求和）
    TotalGoroutine int        `json:"totalGoroutine"`
    TotalThread    int        `json:"totalThread"`
    TotalFD        int        `json:"totalFD"`

    // per-agent 简要信息
    Agents []AgentSystemBrief `json:"agents"`
}

type AgentSystemBrief struct {
    AgentID      string  `json:"agentId"`
    Name         string  `json:"name"`
    Status       string  `json:"status"`
    CPUPercent   float64 `json:"cpuPercent"`
    MemPercent   float64 `json:"memPercent"`
    NumGoroutine int     `json:"numGoroutine"`
    NetSendKBps  float64 `json:"netSendKBps"`
    NetRecvKBps  float64 `json:"netRecvKBps"`
    LastSeen     int64   `json:"lastSeen"`  // unix
}
```

| 指标 | 聚合方式 | 备注 |
|---|---|---|
| CPU% | 按核数加权平均 + 最大值（`hot` 标记） | 突出热点节点 |
| 内存（总/已用） | 求和 | 集群总容量视角 |
| 网络速率 | 求和 | 集群总流量 |
| Goroutine / Thread / FD | 求和 | 集群总览 |
| Load Avg | **不聚合** | per-agent 单独显示，跨机器无意义 |

### 8.6 系统指标历史

同压测指标，**前端本地维护**最近 120 个快照数组用于折线图。后端不做历史存储，不做长期趋势分析（有需要时通过前端 CSV 导出）。

---

## 9. 任务生命周期

```
1. 创建任务
   前端 POST /api/tasks { name, totalBots, config: { flowJson, protoFiles, ... } }
   → Admin 创建 Task，status=pending，配置包写入 data/tasks/{id}/

2. 启动任务
   前端 POST /api/tasks/{id}/start
   → Admin:
     a. 过滤 status=idle 的 Agent，确认总容量 >= totalBots
     b. 均匀切分账号范围，创建 Assignment 列表
     c. 向各 Agent 推送 TaskAssignment（POST /task/assign）
     d. status=running，记录 startedAt

3. Agent 执行
   a. 收到分配后 GET /api/agent/config/{taskId} 拉取配置包
   b. 写入临时目录 /tmp/stressbot-task-{id}/conf/
   c. 加载 adapter、proto、flow、scripts（复用现有函数）
   d. 创建 robot.Manager（assigned startNumber/count）→ StartAll
   e. 启动两个独立的上报循环：
      - 每 5s POST /api/agent/stress（携带 CollectorSnapshot）
      - 每 5s POST /api/agent/system（携带 SystemSnapshot，从 Agent 启动时就在跑）

4. 前端监控
   前端每 5s GET /api/metrics      → 压测聚合数据
   前端每 5s GET /api/system        → 系统聚合数据
   前端本地维护两份历史数组用于折线图

5. 停止任务
   前端 POST /api/tasks/{id}/stop
   → Admin: status=stopping，向各 Agent 推送停止命令
   → Agent: mgr.StopAll() → 导出 CSV → 推送最终指标 → POST /done
   → Admin: 所有 Agent 报告 done → status=stopped

6. 清理
   Agent 状态回到 idle，继续上报系统指标（不再上报压测指标）
```

---

## 10. Agent 设计

### 10.1 部署形态

一个 Agent 节点由两个独立二进制组成（详见 §15 热更新与滚动升级）：

```
+────────────────────────+      spawn      +────────────────────────+
│  agent-launcher.exe    │ ──────────────→ │  agent.exe             │
│  Launcher 进程，~1MB   │                 │  Agent 进程，业务主体  │
│  常驻、不联网          │ ←──── exit ──── │  监听 :7070，HTTP 通信 │
+────────────────────────+                 +────────────────────────+
```

| 二进制 | 角色 | 职责 | 网络 |
|---|---|---|---|
| `agent-launcher.exe` | Launcher 进程 | 守护、监控 exit code、升级时替换 `agent.exe` | **完全不联网** |
| `agent.exe` | Agent 进程 | 注册、心跳、任务执行、指标上报、接收 Admin 命令 | 全部 HTTP 与 Admin 通信 |

**部署形式**：

| 模式 | 所需文件 | 启动方式 |
|---|---|---|
| 单机模式（开发 / 小规模压测） | 仅 `agent.exe` | 直接 `./agent.exe -config conf/config.json`，配置 `agent.enabled=false` |
| 分布式 Agent 节点 | `agent-launcher.exe` + `agent.exe`（同目录） | 启动 `agent-launcher.exe`，其内部 spawn `agent.exe`，配置 `agent.enabled=true` |

**关键约定**：
- `agent.exe` 是单机/Agent 共用的同一份二进制，**没有 `--role` 标志**，行为完全由 `config.json` 决定
- Launcher 仅在分布式部署时存在；单机模式不需要它
- Admin 视角下 Agent 节点仍是单一端点（:7070），Launcher 对 Admin 完全透明

本章节余下部分描述的均是 **Agent 进程**（即 `agent.exe` 在 Agent 模式下）的实现细节。

### 10.2 Agent 进程结构

`agent/agent.go` 是 Agent 进程的核心组件。

```go
type Agent struct {
    cfg        AgentConfig
    agentID    string
    sysmon     *SystemMonitor              // 系统监控采集器
    collector  *monitor.MetricsCollector   // 压测指标采集器
    mgr        *robot.Manager              // 当前 Manager（idle 时为 nil）
    currentTask *TaskAssignment
    taskCancel  context.CancelFunc
    httpClient  *http.Client
    stopCh      chan struct{}
}

type AgentConfig struct {
    AdminAddr           string        // 如 "http://192.168.1.100:8080"
    Name                string
    ListenAddr          string        // Agent HTTP 监听地址
    MaxBots             int
    StressInterval      time.Duration // 压测指标推送间隔，默认 5s
    SystemInterval      time.Duration // 系统指标采集 + 推送间隔，默认 5s
    HeartbeatInterval   time.Duration // 心跳间隔，默认 10s
}
```

### Agent 运行的循环

| 循环 | 间隔 | 功能 |
|---|---|---|
| **系统采集循环** | 5s | SystemMonitor 后台 goroutine，更新本地 `latest` 快照 |
| **心跳循环** | 10s | POST `/api/agent/heartbeat`，确认存活 + 当前 status |
| **系统上报循环** | 5s | POST `/api/agent/system`（SystemSnapshot），始终运行，含空闲时 |
| **压测上报循环** | 5s | POST `/api/agent/stress`（CollectorSnapshot），仅任务运行时启动，任务停止时退出 |
| **任务轮询循环** | 5s | GET `/api/agent/task`，兜底感知新任务（Push 失败时恢复） |
| **HTTP 服务器** | — | 监听 `/task/assign`、`/task/stop`、`/health`，接受 Admin Push |

### 上报循环示例

系统上报循环（Agent 启动即运行，与任务无关）：

```go
func (a *Agent) systemReportLoop() {
    ticker := time.NewTicker(a.cfg.SystemInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            a.postWithRetry("/api/agent/system", SystemReport{
                AgentID:  a.agentID,
                Snapshot: a.sysmon.Snapshot(),
            })
        case <-a.stopCh:
            return
        }
    }
}
```

压测上报循环（任务期间运行，由 `executeTask` 启动 / 停止）：

```go
func (a *Agent) stressReportLoop(ctx context.Context, taskID string) {
    ticker := time.NewTicker(a.cfg.StressInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            a.postWithRetry("/api/agent/stress", StressReport{
                AgentID:  a.agentID,
                TaskID:   taskID,
                Snapshot: monitor.Global().Snapshot(nil, 0),
            })
        case <-ctx.Done():
            return
        }
    }
}
```

### 任务执行流程

```go
func (a *Agent) executeTask(assignment *TaskAssignment) error {
    config := a.fetchConfig(assignment.ConfigURL)

    dir := filepath.Join(os.TempDir(), "stressbot-task-"+assignment.TaskID)
    writeConfigFiles(dir, config)

    adp     := loadAdapter(filepath.Join(dir, "adapter", "codec.lua"))
    proto   := protox.NewLoader(filepath.Join(dir, "proto"), nil)
    flow    := loadFlow(filepath.Join(dir, "flow.json"))
    scripts := script.NewRuntimePool(filepath.Join(dir, "scripts"))

    monitor.Init(monitor.CollectorConfig{Enabled: true, ApdexT: assignment.ApdexT})

    mgr := robot.NewManager(robot.ManagerConfig{
        AccountPrefix: assignment.AccountPrefix,
        StartNumber:   assignment.StartNumber,
        Count:         assignment.Count,
        ConcurrentNum: assignment.ConcurrentNum,
        // ...
    }, flow, factory, dialer, scripts)

    a.mgr = mgr
    mgr.StartAll()

    // 启动压测上报循环（任务期间专属）
    stressCtx, stressCancel := context.WithCancel(context.Background())
    go a.stressReportLoop(stressCtx, assignment.TaskID)

    <-a.taskStop
    mgr.StopAll()
    stressCancel()              // 关闭压测上报循环；系统上报循环不受影响
    a.flushFinalStress(assignment.TaskID)  // 最后一次压测快照，确保停止后数据完整
    a.reportDone(assignment.TaskID)
    return nil
}
```

### Admin 宕机时的 Agent 自治

Agent 继续自治运行，`robot.Manager` 不依赖 Admin 实时连接。指标推送失败时指数退避重试（1s → 2s → 4s → 8s → 上限 30s）。Admin 恢复后 Agent 重新注册（心跳触发自动重注册），立即恢复指标推送。

---

## 11. Admin Server 设计

```go
type AdminServer struct {
    cfg     AdminConfig
    tasks   *TaskStore
    agents  *AgentRegistry
    metrics *MetricsAggregator
    mux     *http.ServeMux
}

type AdminConfig struct {
    ListenAddr   string        `json:"listenAddr"`
    StaticDir    string        `json:"staticDir"`
    HeartbeatTTL time.Duration `json:"heartbeatTtl"`
    DataDir      string        `json:"dataDir"`
}

type AgentRegistry struct {
    mu     sync.RWMutex
    agents map[string]*AgentNode
}

type MetricsAggregator struct {
    mu          sync.RWMutex
    stress    map[string]*monitor.CollectorSnapshot  // agentID → 最新压测快照
    system      map[string]*SystemSnapshot             // agentID → 最新系统快照
}
```

### 路由注册

```go
func (s *AdminServer) registerRoutes() {
    // 前端 API（任务）
    s.mux.HandleFunc("POST /api/tasks",            s.handleCreateTask)
    s.mux.HandleFunc("GET /api/tasks",             s.handleListTasks)
    s.mux.HandleFunc("GET /api/tasks/{id}",        s.handleGetTask)
    s.mux.HandleFunc("PUT /api/tasks/{id}",        s.handleUpdateTask)
    s.mux.HandleFunc("POST /api/tasks/{id}/start", s.handleStartTask)
    s.mux.HandleFunc("POST /api/tasks/{id}/stop",  s.handleStopTask)
    s.mux.HandleFunc("DELETE /api/tasks/{id}",     s.handleDeleteTask)

    // 前端 API（Agent）
    s.mux.HandleFunc("GET /api/agents",            s.handleListAgents)
    s.mux.HandleFunc("GET /api/agents/{id}",       s.handleGetAgent)

    // 前端 API（压测指标）
    s.mux.HandleFunc("GET /api/metrics",           s.handleGetMetrics)
    s.mux.HandleFunc("GET /api/metrics/agents",    s.handleGetAgentMetrics)
    s.mux.HandleFunc("GET /api/metrics/export",    s.handleExportCSV)

    // 前端 API（系统指标）
    s.mux.HandleFunc("GET /api/system",            s.handleGetSystem)
    s.mux.HandleFunc("GET /api/system/agents",     s.handleGetAgentsSystem)
    s.mux.HandleFunc("GET /api/system/agents/{id}", s.handleGetAgentSystem)

    // Agent 上报 API
    s.mux.HandleFunc("POST /api/agent/register",         s.handleAgentRegister)
    s.mux.HandleFunc("POST /api/agent/heartbeat",        s.handleAgentHeartbeat)
    s.mux.HandleFunc("POST /api/agent/stress",           s.handleAgentStressReport)
    s.mux.HandleFunc("POST /api/agent/system",           s.handleAgentSystemReport)
    s.mux.HandleFunc("GET /api/agent/task",              s.handleAgentGetTask)
    s.mux.HandleFunc("GET /api/agent/config/{taskId}",   s.handleAgentGetConfig)
    s.mux.HandleFunc("POST /api/agent/task/{id}/done",   s.handleAgentTaskDone)
    s.mux.HandleFunc("POST /api/agent/task/{id}/failed", s.handleAgentTaskFailed)

    // 静态文件
    if s.cfg.StaticDir != "" {
        s.mux.Handle("/", http.FileServer(http.Dir(s.cfg.StaticDir)))
    }
}
```

### 心跳超时处理

后台 goroutine 定期检查：
- `LastHeartbeat + 1×TTL` → `unhealthy`（可能还在跑，网络抖动）
- `LastHeartbeat + 2×TTL` → `offline`（基本确认宕机）

`offline` 的 Agent 在指标聚合中**保留最后一次快照但标记为 stale**，避免数据骤变让前端误以为压测吞吐真的下降。

---

## 12. 任务分配算法

```go
// admin/assignment.go
func Assign(task *Task, agents []*AgentNode) []Assignment {
    totalBots := task.TotalBots
    startNum  := task.Config.StartNumber
    n         := len(agents)

    base      := totalBots / n
    remainder := totalBots % n

    var assignments []Assignment
    current := startNum
    for i, a := range agents {
        count := base
        if i < remainder {
            count++
        }
        assignments = append(assignments, Assignment{
            AgentID:     a.ID,
            StartNumber: current,
            Count:       count,
        })
        current += count
    }
    return assignments
}
```

**容量检查**：分配前过滤掉 `status != idle` 的 Agent，确保 `sum(available.MaxBots) >= task.TotalBots`，不满足时返回 `400 Bad Request`。

---

## 13. 配置文件

### Agent 配置（现有 `conf/config.json` 新增 `agent` 段）

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
    "heartbeatInterval": "10s"
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | Agent 模式开关 |
| `adminAddr` | string | `""` | Admin 服务器地址 |
| `name` | string | 主机名 | Agent 显示名 |
| `listenAddr` | string | `":7070"` | Agent HTTP 监听地址（供 Admin Push） |
| `maxBots` | int | `0` | 最大可承载机器人数，0 = 无限制 |
| `stressInterval` | duration | `"5s"` | 压测指标推送间隔 |
| `systemInterval` | duration | `"5s"` | 系统采集间隔 |
| `heartbeatInterval` | duration | `"10s"` | 心跳间隔 |

### cmd/agent/main.go 改动

```go
func main() {
    cfgPath := flag.String("config", "conf/config.json", "")
    flag.Parse()

    cfg := loadConfig(*cfgPath)

    if cfg.Agent.Enabled {
        // Agent 模式：注册到 Admin，等待任务下发
        a := agent.New(agent.AgentConfig{
            AdminAddr:         cfg.Agent.AdminAddr,
            Name:              cfg.Agent.Name,
            ListenAddr:        cfg.Agent.ListenAddr,
            MaxBots:           cfg.Agent.MaxBots,
            StressInterval:    parseDur(cfg.Agent.StressInterval, 5*time.Second),
            SystemInterval:    parseDur(cfg.Agent.SystemInterval, 5*time.Second),
            HeartbeatInterval: parseDur(cfg.Agent.HeartbeatInterval, 10*time.Second),
        })
        a.Run()  // 阻塞直到退出信号
    } else {
        // 单机模式：现有逻辑完全不变
        runStandalone(cfg)
    }
}
```

> Launcher 是独立二进制（`cmd/launcher`），不在本文件中。Agent 模式下由 Launcher 进程拉起本程序，本程序无需感知 Launcher 的存在。

### Admin 配置（新建 `conf/admin-config.json`）

```json
{
  "listenAddr": ":8080",
  "staticDir": "web/dist",
  "heartbeatTtl": "30s",
  "dataDir": "data"
}
```

---

## 14. 故障处理

### Agent 宕机

1. 1×TTL（30s）超时 → `unhealthy`
2. 2×TTL（60s）超时 → `offline`
3. 压测聚合保留该 Agent 最后一次快照但标记 stale
4. 前端 Agent 列表以红色显示
5. 用户可：
   - **继续运行**：剩余 Agent 继续，指标反映实际活跃节点
   - **停止任务**：Admin 只向 `status != offline` 的 Agent 下发停止

### Admin 宕机

1. Agent 继续自治，`robot.Manager` 不依赖 Admin 连接
2. 指标推送失败时指数退避重试（1s → 2s → 4s → 8s → 上限 30s）
3. Admin 恢复后 Agent 心跳触发自动重注册，立即恢复
4. 任务状态从 `dataDir` 恢复（如已实现持久化）

### 网络分区

同 Agent 宕机，由心跳超时机制检测。Agent 侧不依赖实时连接维持任务。

---

## 15. 热更新与滚动升级

### 15.1 设计目标

- **不依赖外部进程管理器**（systemd / nssm / supervisor），完全在 stressbot 自身完成
- **跨平台**：Windows 和 Linux 行为一致（重点解决 Windows 不能覆盖运行中 exe 的问题）
- **集中分发**：前端上传到 Admin 一次，所有 Agent 从 Admin 拉取，不一台台手动操作
- **失败自动回滚**：升级失败时自动恢复旧版本，不中断集群
- **接受短暂停机**：升级期间端口断开 1~3s（远低于心跳 TTL 30s），Agent 不会被误判为离线

### 15.2 Launcher + Agent 进程双二进制模型

Agent 节点由两个**完全独立的二进制**协作运行：

```
+──────────────────────────+              +──────────────────────────+
│  agent-launcher.exe      │  spawn       │  agent.exe               │
│  Launcher 进程, PID 100  │ ───────────→ │  Agent 进程, PID 101     │
│  常驻不退出              │              │  实际业务进程            │
│  纯本地操作              │ ←─── exit ── │                          │
+──────────────────────────+              +──────────────────────────+
   不联网，不监听端口                         监听 :7070，对外通信
   ~1MB（独立 cmd/launcher）                  ~30MB（含 gnet/gopsutil/proto 等）
```

| 二进制 | 进程 | 职责 | 通信 |
|---|---|---|---|
| `agent-launcher.exe` | Launcher | spawn Agent 进程 / 监控 exit code / 升级时替换 `agent.exe` | **不联网**，仅本地文件 IPC |
| `agent.exe` | Agent 进程 | 注册、心跳、任务执行、指标上报、接收 Admin 命令 | 与 Admin 全部 HTTP 通信 |

为什么独立成两个二进制：

- **职责物理隔离**：Launcher 代码极简（< 300 行），不依赖 admin/agent/engine/network 等业务包，编译后体积 ~1MB
- **Launcher 几乎不需要升级**：业务代码迭代不会牵动 Launcher 二进制，避免"自己拉起自己"的鸡生蛋问题
- **agent.exe 单机/Agent 共用**：单机模式直接运行；Agent 模式由 Launcher 拉起，本身无需感知 Launcher 的存在
- **无需 `--role` 标志**：agent.exe 行为完全由 `config.json` 决定，命令行干净

部署目录布局：

```
agent-node/
  agent-launcher.exe   ← 启动入口
  agent.exe            ← 业务进程，由 Launcher spawn
  conf/
    config.json            ← agent.enabled=true
    flow.json
    proto/
    scripts/
  log/
```

> **关键**：Launcher 不与 Admin 通信。Admin 视角下 Agent 节点只有 `agent.exe` 监听的 :7070 一个端点，所有现有 API 设计完全不变。

### 15.3 为什么需要 Launcher

只为解决一个问题：**Windows 不能覆盖运行中的 exe**。

- 单进程方案在 Linux 可行（POSIX 允许覆盖运行中的 ELF），但 Windows 上 `os.Rename` 会返回 `ERROR_SHARING_VIOLATION`
- Launcher 让 Agent 进程先完全退出，**Launcher 才执行替换**，此时 Windows 也允许覆盖
- Launcher 是独立小二进制，生命周期内基本不变，可视为本地"运行时固件"

### 15.4 完整升级链路

```
+──────────+
│ 前端     │
+────┬─────+
     │ ① 上传新二进制（multipart）
     │ POST /api/binaries
     ▼
+──────────────────────────────+
│  Admin                       │
│  data/binaries/              │
│    agent-v1.2.0.exe      │
│    agent-v1.2.0.sha256   │
+──────────────────────────────+
     │ ② 滚动升级（前端按钮）
     │ POST /api/agents/upgrade-all { version: "v1.2.0" }
     ▼
   Admin 内部：遍历 Agent 列表，逐台升级
     │ ③ 下发命令
     │ POST http://agent-1:7070/agent/v1/upgrade
     │ { url: "http://admin:8080/api/binaries/agent-v1.2.0.exe",
     │   sha256: "abc123...",
     │   version: "v1.2.0" }
     ▼
+──────────────────────────────────────+
│  Agent 服务器                         │
│                                       │
│  +──────────────────────────────+    │
│  │  agent.exe (PID 101)     │    │
│  │  Agent 进程                   │    │
│  │  ④ 立即返回 202 Accepted     │    │
│  │  ⑤ 异步执行：                │    │
│  │     - 从 Admin pull 二进制   │    │
│  │       → ./agent.exe.new  │    │
│  │     - 校验 SHA256            │    │
│  │     - drain 当前任务         │    │
│  │     - 写 .upgrade.pending    │    │
│  │     - os.Exit(99)            │    │
│  +────────────┬─────────────────+    │
│               │ exit                  │
│               ▼                       │
│  +──────────────────────────────+    │
│  │  agent-launcher.exe      │    │
│  │  Launcher（始终在运行）      │    │
│  │  ⑥ cmd.Wait() 返回 exit=99   │    │
│  │  ⑦ 检测 .upgrade.pending     │    │
│  │  ⑧ 备份 agent.exe → .bak │    │
│  │  ⑨ os.Rename(.new → .exe)    │    │
│  │  ⑩ spawn 新 agent.exe    │    │
│  +────────────┬─────────────────+    │
│               │ spawn                 │
│               ▼                       │
│  +──────────────────────────────+    │
│  │  agent.exe (PID 102)     │    │
│  │  新版 Agent 进程              │    │
│  │  ⑪ 启动 → register           │    │
│  │  ⑫ 注册成功 → 写 .success    │    │
│  +──────────────────────────────+    │
└─────┬─────────────────────────────────+
      │ ⑬ register 携带新版本号
      ▼
   Admin
   ⑭ 检测到 AppVersion 变化 → 标记升级成功
   ⑮ 继续滚动升级下一台
```

**关键设计点**：

1. **二进制流向是 Agent 进程 pull，不是 Admin push**——Admin 只负责存储和提供 URL，避免向 N 个 Agent 重复传输大文件
2. **Launcher 与 Agent 进程通过文件系统 IPC**（共享同一磁盘），不走 HTTP/管道
3. **升级触发的入口是 Agent 进程**，不是 Launcher——Launcher 只对 Agent 进程的退出做出被动响应

### 15.5 IPC 标记文件协议

Launcher 与 Agent 进程之间通过 4 个标记文件协调，全部在二进制所在目录：

| 文件 | 写入方 | 读取方 | 含义 |
|---|---|---|---|
| `agent.exe.new` | Agent 进程 | Launcher | 新版本二进制（下载完成） |
| `.upgrade.pending` | Agent 进程 | Launcher | 升级请求标记，含目标版本号 |
| `agent.exe.bak` | Launcher | Launcher | 旧版本备份，用于回滚 |
| `.upgrade.success` | 新 Agent 进程 | Launcher | 新版本注册成功，可清理 .bak |

**Agent 进程退出码协议**：

| Exit Code | 含义 | Launcher 行为 |
|---|---|---|
| `0` | 正常停止（用户主动退出） | 不重启 |
| `99` | 计划升级 | 检测 `.upgrade.pending` → 替换 → spawn 新 Agent 进程 |
| 其他 | 崩溃 | 直接重启 Agent 进程，2s 冷却防止狂奔 |

### 15.6 失败回滚机制

Launcher 在 spawn 新 Agent 进程后等待最多 60s 接收 `.upgrade.success` 标记。超时即认为升级失败：

| 失败场景 | Launcher 行为 |
|---|---|
| 下载失败 | Agent 进程不会 exit 99，没有 `.upgrade.pending`，老版本继续运行 |
| Rename 失败 | Launcher 立即 `os.Rename(.bak → exe)`，spawn 旧版 |
| 新版启动崩溃（exit ≠ 99） | Launcher 看到 .bak 存在但无 .success → 回滚 |
| 新版启动但注册失败 | 同上，60s 超时回滚 |
| 新版正常运行 | 收到 .success → 删除 .bak，升级完成 |

整个回滚不依赖任何外部工具或人工介入。

### 15.7 Launcher 实现示例

```go
// cmd/launcher/main.go
const (
    agentBinary = "agent.exe" // Linux 下为 "agent"
    flagPending = ".upgrade.pending"
    flagSuccess = ".upgrade.success"
    suffixNew   = ".new"
    suffixBak   = ".bak"
    exitUpgrade = 99
)

func main() {
    selfPath, _ := os.Executable()
    dir := filepath.Dir(selfPath)
    agentPath := filepath.Join(dir, agentBinary)

    for {
        applyPendingUpgrade(agentPath, dir)

        cmd := exec.Command(agentPath, os.Args[1:]...)
        cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
        if err := cmd.Start(); err != nil {
            time.Sleep(2 * time.Second)
            continue
        }

        err := cmd.Wait()
        exitCode := exitCodeOf(err)
        watchUpgradeOutcome(agentPath, dir)

        if exitCode != exitUpgrade && exitCode != 0 {
            time.Sleep(2 * time.Second) // 崩溃冷却
        }
    }
}

func applyPendingUpgrade(agentPath, dir string) {
    pending := filepath.Join(dir, flagPending)
    if _, err := os.Stat(pending); os.IsNotExist(err) {
        return
    }

    newPath := agentPath + suffixNew
    bakPath := agentPath + suffixBak

    if _, err := os.Stat(newPath); os.IsNotExist(err) {
        os.Remove(pending)
        return
    }

    if err := copyFile(agentPath, bakPath); err != nil {
        os.Remove(pending)
        return
    }

    if err := os.Rename(newPath, agentPath); err != nil {
        os.Rename(bakPath, agentPath) // 立即回滚
        os.Remove(pending)
        return
    }

    os.Remove(pending)
}

func watchUpgradeOutcome(agentPath, dir string) {
    bakPath := agentPath + suffixBak
    if _, err := os.Stat(bakPath); os.IsNotExist(err) {
        return
    }

    success := filepath.Join(dir, flagSuccess)
    deadline := time.Now().Add(60 * time.Second)
    for time.Now().Before(deadline) {
        if _, err := os.Stat(success); err == nil {
            os.Remove(success)
            os.Remove(bakPath)
            return
        }
        time.Sleep(1 * time.Second)
    }
    os.Rename(bakPath, agentPath) // 超时回滚
}
```

> Launcher 的二进制位置（`os.Executable()` 返回的是 `agent-launcher.exe` 自身路径）和被守护的 Agent 进程二进制是**不同的文件**。Launcher 通过同目录下的固定文件名 `agent.exe` 找到 Agent 进程，命令行参数原样透传。

### 15.8 Agent 进程端升级处理

```go
// agent/upgrader.go
func (a *Agent) handleUpgrade(req UpgradeRequest) {
    selfPath, _ := os.Executable() // agent.exe 自身
    newPath := selfPath + ".new"

    if err := downloadAndVerify(req.URL, req.SHA256, newPath); err != nil {
        return
    }

    go func() {
        a.drainAndStop(5 * time.Minute)

        flag := filepath.Join(filepath.Dir(selfPath), ".upgrade.pending")
        os.WriteFile(flag, []byte(req.Version), 0644)

        os.Exit(99)
    }()
}

// 新 Agent 进程注册成功后写 .success
func (a *Agent) onRegisterSuccess() {
    selfPath, _ := os.Executable()
    bak := selfPath + ".bak"
    if _, err := os.Stat(bak); err == nil {
        success := filepath.Join(filepath.Dir(selfPath), ".upgrade.success")
        os.WriteFile(success, nil, 0644)
    }
}
```

### 15.9 新增 API

#### Admin 对前端

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/binaries` | 上传新二进制（multipart：file + version + os/arch） |
| `GET` | `/api/binaries` | 列出所有可用版本 |
| `GET` | `/api/binaries/{filename}` | 下载二进制（Agent 进程也用此 URL pull） |
| `DELETE` | `/api/binaries/{filename}` | 删除旧版本 |
| `POST` | `/api/agents/{id}/upgrade` | 升级单个 Agent |
| `POST` | `/api/agents/upgrade-all` | 滚动升级所有 Agent |
| `GET` | `/api/agents/upgrade-status` | 查询滚动升级进度 |

#### Admin 对 Agent

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/agent/v1/upgrade` | Body: `{url, sha256, version}`，触发升级 |
| `POST` | `/agent/v1/restart` | 简单重启（不下载，仅 Agent 进程 exit → Launcher 重启） |
| `GET` | `/agent/v1/version` | 查询当前版本 |

注册请求扩展（用于 Admin 检测升级结果）：

```go
type RegisterRequest struct {
    AgentID    string
    Name       string
    Address    string
    AppVersion string  // 新增：编译时注入，Admin 据此追踪升级进度
    StaticInfo *StaticInfo
}
```

### 15.10 Admin 滚动升级流程

```go
func (s *AdminServer) upgradeAll(version string) {
    binaryURL := fmt.Sprintf("%s/api/binaries/agent-%s.exe", s.publicURL, version)
    sha := s.computeSHA256("data/binaries/agent-" + version + ".exe")

    for _, agent := range s.agents.List() {
        if agent.Status == AgentOffline {
            continue
        }

        // 1. 下发升级命令
        s.postToAgent(agent.Address, "/agent/v1/upgrade", UpgradeRequest{
            URL: binaryURL, SHA256: sha, Version: version,
        })

        // 2. 等待 Agent 重新注册且 AppVersion == version
        if err := s.waitForVersion(agent.ID, version, 5*time.Minute); err != nil {
            log.Printf("Agent %s 升级失败，停止滚动升级", agent.ID)
            return
        }
    }
}
```

### 15.11 Launcher 自身的局限

| 局限 | 应对 |
|---|---|
| Launcher 自身崩溃 | Agent 进程仍在运行，业务不中断；但下次 Agent 进程退出后无人拉起 → 节点离线 → Admin 心跳超时检测，运维介入 |
| Launcher 自身需要升级 | Agent 进程当前不会去替换 `agent-launcher.exe`（避免循环依赖），需运维 SCP/RDP 替换；好在 Launcher 代码极简、独立编译，迭代频率近乎零 |
| 端口短暂中断（1~3s） | 远低于心跳 TTL，节点不会被误判离线；Admin 命令重试机制覆盖 |

这些局限可接受——避免引入更复杂的 listener fd 传递、双父进程互监督等机制，保持方案简单可靠。

---

## 16. 任务单例约束

### 16.1 设计动机

压测工具的本质是**整个集群同时为单一目标施压**。允许并发多任务会带来：

- 账号范围冲突（不同任务可能复用同一段账号区间）
- 指标聚合歧义（前端"当前任务大盘"展示哪一个？）
- Agent 状态模糊（一个 Agent 不能同时为两个任务跑机器人）
- 网络/资源竞争干扰压测数据本身的可信度

因此明确：**全集群在任意时刻最多有一个"执行中"的任务**。

### 16.2 状态分类

按是否占用集群资源划分两类：

| 类别 | 包含状态 | 含义 | 数量限制 |
|---|---|---|---|
| **Active** | `starting`、`running`、`stopping` | 正在占用 Agent 执行或正在收尾 | **全集群最多 1 个** |
| **Inactive** | `pending`、`stopped`、`failed` | 未占用 Agent | 不限数量 |

> `pending` 不限制：用户可预先创建多个任务草稿排队（但需手动一个一个启动，不自动调度）。

### 16.3 单例保证机制

**双重保证**：

1. **内存层（强一致）**：`TaskStore` 提供 `ActiveTask()` 查询接口，启动接口加 mutex 检查
2. **数据库层（兜底）**：`task_history` 表对 `state IN ('starting','running','stopping')` 加部分唯一索引（MySQL 通过函数索引或 `active_flag` 派生列实现）

启动流程：

```
POST /api/tasks/{id}/start
  ├ TaskStore.startMu.Lock()
  ├ if active := ActiveTask(); active != nil
  │   └ 返回 409 TASK_CONFLICT { activeTaskId: active.ID, activeName: active.Name }
  ├ Transition(id, pending → starting)
  ├ TaskStore.startMu.Unlock()
  └ 异步分配并下发
```

**升级 / 重启等场景**：

| 场景 | 行为 |
|---|---|
| Admin 重启时存在 active 任务 | 持久化恢复后状态变为 `failed`（详见 §14 故障处理），active 计数清零 |
| Agent 崩溃 → unhealthy → offline | 任务状态保持 `running`（仍是 active），等待 Agent 恢复或运维强制 stop |
| 强制 stop active 任务 | 切到 `stopping`（仍占据单例位）→ 全部 Agent 上报 done 后切 `stopped`，单例位释放 |

### 16.4 拒绝并发的错误返回

```json
{
  "code": "TASK_CONFLICT",
  "message": "another task is currently active",
  "details": {
    "activeTaskId": "task-xxx",
    "activeName": "200v200 压测",
    "activeState": "running",
    "startedAt": "2026-04-29T10:00:00+08:00"
  }
}
```

前端拿到此错误时，应引导用户跳转到 active 任务详情页或先停止再启动。

### 16.5 未来扩展点（不在当前实现范围）

- **任务队列**：pending 任务自动排队，前一个 stopped 后自动启动下一个
- **优先级**：高优先级任务可抢占低优先级（需更复杂的资源调度）
- **多集群隔离**：把集群拆成 N 个 group，每 group 内独立单例（适合多团队共享场景）

> 当前版本只做硬性单例，**任何并发启动一律拒绝**。如有任务队列需求，前端可在客户端做 UI 排队（提示用户"前一个完成后自动跳转"），后端不参与。

---

## 17. 历史压测记录

### 17.1 设计目标

压测结束后用户需要：

- **回查历史**：列出过去 N 天所有跑过的任务，按时间/状态/标签筛选
- **报告生成**：单次任务的完整快照（动作明细、延迟分位数、错误分布）+ 时序趋势
- **版本对比**：同一压测脚本在 v1.1 vs v1.2 的指标差异（核心：基线对比）
- **问题归档**：标记某次任务为"基线"、"事故复现"、"修复后回归"等
- **任务复用**：从历史任务直接克隆出一份新任务（同样的配置 + 不同账号范围）

### 17.2 存储分层

数据按"温度"分层存储：

| 数据 | 存储位置 | 写入时机 | 用途 |
|---|---|---|---|
| 任务实时状态 | Admin 内存 + `data/tasks/*.json` | 状态变更 | 列表 / 详情（运行中） |
| 时序采样点 | MySQL `task_timeseries` | 任务运行期间每 10s | 历史趋势曲线 |
| 终态快照（per-Agent） | MySQL `task_report` | 任务终态 | 历史详情、Agent 对比 |
| 终态聚合（集群） | MySQL `task_aggregated` | 任务终态 | 历史报告主数据 |
| 任务配置归档 | MySQL `task_config_archive` | 任务终态 | 复用 / 回放 |
| 用户标记 | MySQL `task_history.tags/note/starred` | 任意时刻 | 筛选 / 报表 |

### 17.3 数据库表概览

```
task_history          ← 主表：id、name、state、起止时间、tags、note、starred、duration
task_assignment       ← 集群分配快照（每个 Agent 一条）
task_report           ← 各 Agent 的完成报告 + finalSnapshot（JSON 列）
task_aggregated       ← 集群聚合的终态 stress/system 快照
task_timeseries       ← 运行期采样点（每 10s 一条，记录聚合 stress 快照）
task_config_archive   ← 任务配置归档（flow.json + protos + scripts，BLOB/JSON）
```

详细 DDL 见 `docs/admin-implementation.md` §4 的 HistoryStore 章节。

### 17.4 数据流

```
[运行期]
任务 starting → Sampler 启动（每 10s 采样一次集群聚合 + 系统聚合 → INSERT task_timeseries）

[终态]
任务 stopped/failed → HistoryStore.Archive(task)：
  1. INSERT task_history（基本信息）
  2. INSERT task_assignment（每个 Agent 一行）
  3. INSERT task_report（每个 Agent 的 finalSnapshot）
  4. INSERT task_aggregated（最后一次聚合快照）
  5. INSERT task_config_archive（保留配置原文，便于复用）
  6. data/tasks/{id}.json 可异步删除（可选，保留 7 天作冷备）
  7. Sampler 停止
```

### 17.5 标记功能

每条 `task_history` 支持：

| 字段 | 类型 | 用途 |
|---|---|---|
| `starred` | bool | "收藏"，在列表页置顶 |
| `tags` | JSON 数组 | 自由标签：`["benchmark","v1.2","beforeFix"]` |
| `note` | TEXT | 备注（markdown，最大 8KB） |

支持的查询场景：

- 仅显示 starred 的任务
- 按 tag 过滤（任意一个 tag 匹配 / 所有 tag 都匹配）
- 全文搜索 name + note

### 17.6 后端组件

新增两个组件（详见 admin-implementation.md §4）：

- `admin/history.go` —— `HistoryStore`：MySQL CRUD + filter
- `admin/sampler.go` —— `Sampler`：运行期定时采样写入时序

### 17.7 API 概览

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/history` | 历史任务列表（分页 + 过滤） |
| `GET` | `/api/history/{id}` | 历史任务详情（含聚合快照） |
| `PUT` | `/api/history/{id}` | 更新 tags / note / starred |
| `DELETE` | `/api/history/{id}` | 删除历史任务 |
| `GET` | `/api/history/{id}/timeseries` | 时序数据（趋势图） |
| `GET` | `/api/history/{id}/agents` | 各 Agent 的历史快照 |
| `GET` | `/api/history/{id}/config` | 任务配置归档（用于下载/克隆） |
| `POST` | `/api/history/{id}/clone` | 用历史任务的配置创建新任务（pending 状态，不立即启动） |
| `GET` | `/api/history/compare?ids=a,b,c` | 多任务对比（最多 5 个） |
| `GET` | `/api/history/tags` | 列出全部用过的 tags（用于前端 autocomplete） |

详细字段定义见 `docs/api-monitor.md`。

### 17.8 数据保留策略

- 默认保留 90 天（可配置 `history.retentionDays`）
- 超过保留期且 `starred=false` 的任务自动删除（每天凌晨清理）
- `starred=true` 的任务**永不自动删除**，需用户手动删除
- MySQL 不可用 / `history.enabled=false` 时：完全跳过历史功能，运行时不影响压测

### 17.9 与现有 `data/tasks/*.json` 的关系

| 用途 | 文件 JSON（轻量） | MySQL 历史（重量） |
|---|---|---|
| 运行中状态恢复 | ✅ 主用 | ❌ |
| 列表/详情（活跃 + pending） | ✅ 主用 | ❌ |
| 历史归档与查询 | ❌ | ✅ 主用 |
| 时序趋势 | ❌ | ✅ |
| 任务克隆（含配置） | ❌ | ✅ |

终态任务在 MySQL 入库后，文件 JSON 仅作冷备保留 7 天即可删除（实际保留逻辑可后续扩展，前期不删）。

---

## 18. 前端变更

### 新增页面

| 路由 | 页面 | 数据源 |
|---|---|---|
| `/dashboard` | 压测监控大盘 | `/api/metrics`（5s 轮询）+ 前端历史数组 |
| `/system` | **系统资源大盘** | `/api/system`（5s 轮询）+ 前端历史数组 |
| `/tasks` | 任务管理（含 active/pending） | `/api/tasks` |
| `/tasks/:id` | 任务详情 | `/api/tasks/:id` + `/api/metrics` |
| `/agents` | Agent 管理 | `/api/agents`（含系统指标摘要） |
| `/agents/:id` | Agent 详情 | `/api/agents/:id` + `/api/system/agents/:id` |
| `/history` | **历史压测列表** | `/api/history`（分页 + 过滤 + 标签） |
| `/history/:id` | **历史报告详情** | `/api/history/:id` + `/api/history/:id/timeseries` |
| `/history/compare` | **历史对比** | `/api/history/compare?ids=a,b,c` |

### 系统大盘建议布局

```
┌─────────────────────────────────────────────────────┐
│ 集群总览                                            │
│ Agent 在线: 3/3  CPU 平均: 45%  Mem: 12.3/64 GB    │
│ 网络发送: 25.4 MB/s  接收: 18.1 MB/s                │
│ Goroutines: 1284  Threads: 96                       │
└─────────────────────────────────────────────────────┘
┌──────────────┬──────────────┬──────────────┐
│ CPU 折线图   │ 内存折线图   │ 网络折线图   │
│ (per-agent)  │ (per-agent)  │ (per-agent)  │
└──────────────┴──────────────┴──────────────┘
┌─────────────────────────────────────────────────────┐
│ Agent 健康表（按 CPU% 降序）                        │
│ agent-gz-01  ●busy  CPU 78% Mem 65% Goroutines 512 │
│ agent-gz-02  ●busy  CPU 32% Mem 41% Goroutines 388 │
│ agent-gz-03  ●idle  CPU  5% Mem 12% Goroutines  84 │
└─────────────────────────────────────────────────────┘
```

### Vite 代理变更

```typescript
// web/vite.config.ts
proxy: {
  '/api': {
    target: 'http://localhost:8080',
    changeOrigin: true,
  },
},
```

### 前端打包托管

Admin 直接托管前端构建产物，无需单独 Nginx：

```json
{ "staticDir": "web/dist" }
```

`vite build` 输出到 `web/dist`，访问 `http://admin:8080` 即可。

---

## 19. 实施顺序

### Phase 1：Monitor + 系统监控基础（约 1 天）

1. `monitor/histogram.go` — `HistogramSnapshot` 新增 `SumNs` 和 `BucketCounts`
2. `agent/sysmon.go` — SystemMonitor 实现（gopsutil 采集 + 速率计算）
3. 引入 `gopsutil/v4` 依赖，验证 Windows/Linux 跨平台采集

### Phase 2：Agent（约 2 天）

4. `agent/agent.go` — Agent 完整实现（注册、心跳、双指标推送、任务轮询、HTTP 服务器）
5. `cmd/agent/main.go` — Agent 配置段 + `agent.enabled` 条件分支

验证：单机模式行为完全不变；agent 模式下 Agent 启动后定期采集系统指标，等待 Admin

### Phase 3：Admin 核心（约 2 天）

6. `admin/types.go` — 数据模型
7. `admin/task.go` — TaskStore、状态机、**单例约束**
8. `admin/agent.go` — AgentRegistry、心跳超时检测
9. `admin/assignment.go` — 均匀分配
10. `admin/aggregator.go` — 压测指标合并 + 系统指标聚合
11. `admin/handlers.go` — 所有 HTTP handler（含 `/api/system/*`、单例 409 错误）
12. `admin/admin.go` — AdminServer、路由
13. `cmd/admin/main.go` — Admin 入口

验证：Agent 注册到 Admin → `/api/agents` 显示节点；任务启动 → `/api/metrics` 聚合压测数据，P99 正确；`/api/system` 显示集群 CPU/内存/网络

### Phase 4：前端集成（约 1.5 天）

14. `web/vite.config.ts` — proxy 改向 Admin
15. 前端新增 Dashboard / System / Tasks / Agents 页面
16. Admin 托管前端静态资源

### Phase 5：健壮性（约 0.5 天）

17. Admin 任务状态持久化（`data/tasks/{id}.json`）
18. Agent 指标推送指数退避
19. unhealthy → offline 两级超时
20. 压测指标 stale 标记

### Phase 6：热更新与滚动升级（约 1.5 天）

21. `cmd/launcher/main.go` — Launcher 独立小二进制（spawn / wait / 文件替换 / 回滚），编译产物 `agent-launcher.exe`
22. `agent/upgrader.go` — Agent 进程端升级处理（下载、校验、drain、写 `.upgrade.pending`、`os.Exit(99)`）
23. `admin/binaries.go` — 二进制存储与下载端点（multipart 上传、SHA256 校验）
24. `admin/upgrade.go` — 单个 / 滚动升级流程
25. `admin/handlers.go` — `/api/binaries`、`/api/agents/upgrade-*` 等 API
26. `agent/agent.go` — `RegisterRequest` 增加 `AppVersion` 字段
27. 前端：二进制管理页 + 滚动升级按钮 + 升级进度展示

验证：上传二进制 → 触发滚动升级 → 各 Agent 依次完成升级（Windows/Linux 均能成功）；模拟新版崩溃 → Launcher 自动回滚旧版本

### Phase 7：历史压测记录（约 2 天）

28. `admin/history.go` — `HistoryStore`：MySQL Schema + CRUD（list/get/update/delete/clone）
29. `admin/sampler.go` — `Sampler`：运行期 10s 一次定时采样写时序表
30. `admin/handlers.go` — `/api/history/*` 全部 API（列表/详情/标签更新/对比/克隆）
31. `admin/admin.go` — 任务终态时触发归档；启动定时清理协程（每天凌晨清理 90 天前未 starred 的记录）
32. `conf/admin-config.json` — 新增 `history.mysql.dsn` 等配置
33. 前端：历史列表页 + 详情页（含时序趋势图）+ 对比页 + 标签编辑

验证：跑完 1 个任务后 `/api/history` 看到记录；时序数据可用于绘制趋势图；克隆后新任务的 `pending` 状态正确；标签筛选生效；`history.enabled=false` 时跳过所有 MySQL 调用，运行不报错

---

## 20. 验证方案

### 本机模拟分布式

```bash
go build -o admin.exe ./cmd/admin
go build -o agent.exe ./cmd/agent
go build -o agent-launcher.exe ./cmd/launcher

# 终端 1：Admin
./admin.exe -config conf/admin-config.json

# 终端 2 / 3：两个 Agent 节点（验证日常路径，直接跑 agent.exe，不经过 Launcher）
./agent.exe -config conf/agent1-config.json   # listenAddr=:7070
./agent.exe -config conf/agent2-config.json   # listenAddr=:7071

# 终端 5：单独验证热更新链路时，改用 Launcher 拉起
./agent-launcher.exe -config conf/agent1-config.json

# 终端 4：操作
curl -X POST http://localhost:8080/api/tasks -d '{"name":"test","totalBots":10,"config":{...}}'
curl -X POST http://localhost:8080/api/tasks/{id}/start
curl http://localhost:8080/api/metrics       # 压测聚合
curl http://localhost:8080/api/system        # 系统聚合
curl http://localhost:8080/api/agents        # Agent 列表（含系统指标摘要）
curl -X POST http://localhost:8080/api/tasks/{id}/stop
```

### 验证检查项

**功能性**

- [ ] Agent 注册成功，`GET /api/agents` 返回 2 个 Agent（idle）
- [ ] 任务启动后两个 Agent 各收到分配，账号范围不重叠
- [ ] `GET /api/metrics` 压测指标聚合正确（successCount = a1 + a2）
- [ ] 延迟 P50/P95/P99 合理（基于合并桶计数，非平均）
- [ ] 停止后两个 Agent 上报 done，任务变为 stopped
- [ ] 单机模式（`agent.enabled=false`）行为完全不变

**系统监控**

- [ ] Agent 启动后立即开始采集系统指标，5s 内 `/api/system/agents/{id}` 返回非空快照
- [ ] CPU% 在压测期间显著上升，停止后回落
- [ ] 网络速率（`netSendKBps`）与实际流量正比
- [ ] Goroutine 数随机器人数线性增长
- [ ] 即使 Agent 处于 idle，系统指标依然每 5s 上报
- [ ] `/api/system` 集群聚合：`avgCpuPercent` 反映两节点平均，`hotAgentId` 标记最高节点
- [ ] 跨平台：Windows / Linux 均能正确采集（重点验证 NumThreads / NumFD）

**故障**

- [ ] Agent 宕机 30s 后 → unhealthy，60s 后 → offline
- [ ] Admin 重启后 Agent 自动重注册，指标推送恢复

**任务单例**

- [ ] 创建 3 个 pending 任务，全部成功（pending 不限制）
- [ ] 启动其中 1 个 → 第 2 个调用 start 返回 409，details 含 activeTaskId
- [ ] 第 1 个 stopped 后，第 2 个 start 成功
- [ ] 内存层 + 数据库层双重检查（手工注入并发 start 请求）

**历史压测记录**

- [ ] 任务 starting 时 Sampler 启动，每 10s 写入一条 `task_timeseries` 记录
- [ ] 任务 stopped/failed 时归档：`task_history`、`task_assignment`、`task_report`、`task_aggregated`、`task_config_archive` 全部插入完整
- [ ] `GET /api/history` 列表显示所有终态任务
- [ ] `PUT /api/history/{id}` 设置 starred=true 后 DELETE 操作受拒绝
- [ ] tags 多值过滤生效（`?tags=benchmark&tags=v1.2`）
- [ ] 时序数据点数 ≈ 任务时长 / 10s
- [ ] 克隆历史任务：`POST /history/{id}/clone` 创建一个 pending 任务，配置完全一致
- [ ] 对比接口：`/history/compare?ids=a,b` 返回两个任务的关键指标对比
- [ ] MySQL 不可用时 admin 启动失败（fail-fast），`history.enabled=false` 时不连 MySQL
- [ ] 90 天清理：模拟时间推进 90 天，未 starred 的自动删除，starred 保留

**热更新**

- [ ] 上传二进制 → `GET /api/binaries` 列表显示新版本
- [ ] 触发单个 Agent 升级 → Launcher 在 Agent 进程退出后替换 `agent.exe`，新 Agent 进程启动并以新版本号重新注册
- [ ] Windows 平台升级成功（验证不会触发 ERROR_SHARING_VIOLATION）
- [ ] 滚动升级 N 个 Agent，`GET /api/agents/upgrade-status` 反映进度
- [ ] 升级期间端口断开 < 5s，Admin 心跳不触发 unhealthy
- [ ] 模拟新版启动失败（如手动改坏 .new 文件）：Launcher 在 60s 后自动回滚 `.bak`，Agent 以旧版本重新注册
- [ ] 升级成功后 `.bak` / `.upgrade.pending` / `.upgrade.success` 等临时文件均被清理

---

## 21. 部署防火墙要求

| 方向 | 端口 | 说明 |
|---|---|---|
| 前端 → Admin | 8080/tcp | REST API + 静态文件托管 |
| Agent → Admin | 8080/tcp | 注册、心跳、双指标上报、配置拉取 |
| Admin → Agent | 7070/tcp | 命令 Push（assign/stop） |
