# 分布式压测管理系统 — 技术文档

> 本文档基于实际代码实现编写，反映 `admin/`（16 文件）、`agent/`（8 文件）、`monitor/` 的真实架构。
> 与原始设计计划 `plans/design-distributed-master.md` 存在的差异均已标注。

---

## 1. 系统概述

### 1.1 设计目标

- **分布式执行**：多个 Agent 节点并行执行压测，模拟大规模用户负载
- **集中管理**：Admin 统一调度任务、聚合指标、服务前端
- **零侵入**：现有单机模式完全兼容，Agent 模式通过 `agent.enabled` 开关切换，**不新增二进制**
- **完整可观测性**：除压测指标（成功率/延迟/Apdex）外，还采集 Agent 所在物理机的系统指标（CPU/内存/网络/线程/协程）
- **简单可靠**：全 HTTP 通信，无外部依赖（消息队列、服务发现等），历史归档使用 MySQL

### 1.2 与现有代码的关系

```
现有代码（不修改）                    新增/修改
──────────────────                   ──────────────────
engine/                              admin/           — Admin 服务端（16 文件）
robot/Manager（已支持 startNumber）  agent/           — Agent 客户端（8 文件）
network/、adapter/、protox/、script/ monitor/（扩展） — 直方图合并支持
                                     cmd/admin/       — 编译产物 admin.exe
                                     cmd/agent/       — 编译产物 agent.exe（单机/Agent 共用）
```

### 1.3 与计划的差异

| 计划项 | 实际实现 | 说明 |
|---|---|---|
| API 前缀 `/api/` | 使用 `/sbot/` | 避免与其他系统冲突 |
| 默认端口 Admin 8080 | Admin 默认 7718 | 通过 `Config.Port` 配置 |
| 默认端口 Agent 7070 | Agent 默认 7719 | 通过 `Config.Port` 配置 |
| `cmd/launcher/` Launcher 进程 | **已废弃** | 升级改为运维手动重启 |
| 热更新与滚动升级（Phase 6） | **已废弃** | `agent/upgrader.go` 未实现 |
| `PUT /api/tasks/{id}` 更新任务 | **未实现** | 任务创建后不可修改 |
| `GET /api/metrics/export` CSV 导出 | **未实现** | 前端自行处理 |
| MySQL 数据库层单例约束 | 仅内存层单例 | 无 `active_flag` 派生列 |
| `StaticInfo.KernelVer` | **未采集** | 字段存在但值为空 |
| 资源基线同步 API | **已实现** | 新增 `/sbot/resources/baseline` 和 `/sbot/baseline/*` 系列 |
| 历史归档使用 SQLite | 使用 **MySQL** | 配置 `history.mysql` |
| `task_agent_events` 表 | **已实现**（计划中无） | 记录任务期间的 Agent 状态变化事件 |

---

## 2. 系统拓扑

```
+───────────────────+
|   Web Frontend    |   React + Ant Design + Vite
|   :5173 (dev)     |   vite proxy -> admin :7718
+────────┬──────────+
         | /sbot/* (HTTP 轮询)
         v
+───────────────────+
|   Admin Server    |   cmd/admin/main.go
|   :7718           |   任务管理 + Agent 注册 + 压测/系统指标聚合 + 历史归档
+──┬──────────┬─────+
   |HTTP Push |HTTP Push（命令下发）
   v          v
+────────+ +────────+       +────────+
|Agent 1 | |Agent 2 |  ...  |Agent N |   每个 Agent 独立采集
+────────+ +────────+       +────────+   所在主机的系统指标
    |           |                |
    +────────── TCP/UDP ─────────+
               -> 游戏服务器
```

### 三个角色

| 角色 | 进程 | 职责 |
|---|---|---|
| **Admin** | `cmd/admin/main.go` | 接受前端请求、管理任务生命周期、Agent 注册表、聚合压测+系统指标、MySQL 历史归档 |
| **Agent** | `cmd/agent/main.go`（agent 模式） | 向 Admin 注册、接收任务、执行压测、采集系统指标、上报数据 |
| **Frontend** | `cmd/web/`（扩展现有） | Dashboard 监控、任务管理、Agent 管理、历史归档、资源管理 |

---

## 3. 关键设计决策

### 3.1 通信方式：HTTP Push（Agent -> Admin）

Agent 主动向 Admin 推送数据（注册、心跳、指标），Admin 不需要主动拉取 Agent 状态。

### 3.2 前端获取指标：HTTP 轮询（Frontend -> Admin）

前端轮询 `/sbot/metrics`，Admin 返回已缓存的最新聚合快照。历史趋势数据由**前端自己维护**（每次轮询结果 push 到本地数组）。

### 3.3 Agent 发现：配置文件指定

Agent 配置中指定 Admin 地址（`agent.adminAddr`），启动时 POST 注册，注册失败时指数退避重试。无需 etcd/consul。

### 3.4 任务分配：Admin 切分账号范围

Admin 将总账号范围按比例分配给各 Agent。支持两种分配模式：

- **比例分配**：按 `MaxBots` 比例切分，保证 `sum(bots) == totalBots`
- **调试模式**（`debugMode=true`）：优先分配到单个 Agent

### 3.5 配置下发：Agent 从 Admin 拉取（Pull 模式）

Admin 将 flow.json、proto 文件、Lua 脚本、适配器脚本打包存储为 `TaskConfig`。启动任务时 Admin 只向 Agent 下发轻量的 `TaskAssignment`（含 `configUrl`），Agent 按需 `GET /sbot/tasks/{id}/config/{path...}` 拉取完整配置包。

### 3.6 系统监控独立采集与上报

Agent 启动时初始化 `agent/sysmon.go` 系统监控器，定期采集 CPU、内存、网络、线程、协程等指标。**压测指标和系统指标走两个独立端点上报**：

- `POST /sbot/agent/stress` -- 仅任务运行时推送
- `POST /sbot/agent/system` -- **始终推送**（含空闲时）

### 3.7 独立模式兼容

`agent.enabled` 默认为 `false`。未配置 agent 段时，stressbot 行为与当前完全一致 -- 直接创建 Manager、启动机器人、等待信号退出。

---

## 4. 目录结构

### Admin 包（`admin/`） -- 16 文件

```
admin/
  admin.go               -- AdminServer 核心结构体、启动/关闭、任务终态归档、Agent 状态变更回调
  agent.go               -- AgentRegistry：注册表、心跳、健康检测、状态变更通知
  agent_dispatcher.go     -- AgentDispatcher：Admin -> Agent HTTP 通信（任务下发/停止/关闭）
  aggregator.go          -- MetricsAggregator：压测指标合并 + 系统指标聚合
  assignment.go          -- Assigner：任务分配算法（比例分配 + 调试模式）
  config.go              -- Config/RegistryConfig/HistoryConfig/MySQLConfig/LogConfig
  errors.go              -- 统一 API 错误类型 + 13 个预定义错误
  handlers.go            -- 所有 HTTP handler（Agent 上行 + 前端-任务 + 前端-Agent + 指标 + 日志 + 基线）
  handlers_history.go    -- 历史归档相关 HTTP handler
  helpers.go             -- stringOr/intOr/secsOr/parseLogQueryParams 等辅助函数
  history.go             -- HistoryStore：MySQL CRUD + 过期清理
  history_schema.go      -- MySQL DDL：7 张表的 CREATE TABLE 语句
  persist.go             -- JSON 文件持久化：saveTaskFile/loadTaskFiles/removeTaskFile
  sampler.go             -- Sampler：运行期定时采样写入时序表
  task.go                -- TaskStore：任务状态机 + 内存存储 + 单例约束
  types.go               -- 所有共享数据类型定义
```

### Agent 包（`agent/`） -- 8 文件

```
agent/
  agent.go               -- Agent 核心结构体、启动/关闭、心跳循环、任务轮询、任务执行
  config.go              -- Config + ResolvedConfig + CollectStaticInfo
  http_client.go         -- AdminClient：Agent -> Admin HTTP 通信（注册/心跳/上报/注销）
  http_server.go         -- Agent 本地 HTTP 服务器（接收 Admin Push 命令）
  reporter.go            -- SystemReporter + StressReporter：双指标上报循环
  sysmon.go              -- SystemMonitor：CPU/内存/网络/线程/协程采集
  task_runner.go         -- TaskRunner：单次任务执行（下载配置 -> 加载 -> 运行 -> 清理）
  types.go               -- 所有数据类型定义
```

---

## 5. Admin API 设计

> **实际 API 前缀为 `/sbot/`**，非计划中的 `/api/`。

### 5.1 Agent 上行 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/sbot/agent/register` | Agent 注册（name、address、maxBots、staticInfo） |
| `POST` | `/sbot/agent/{id}/heartbeat` | 心跳 + 当前 status |
| `POST` | `/sbot/agent/{id}/deregister` | Agent 注销（best-effort） |
| `POST` | `/sbot/agent/stress` | 推送压测指标（StressReport），仅任务运行时 |
| `POST` | `/sbot/agent/system` | 推送系统指标（SystemReport），始终上报 |
| `POST` | `/sbot/agent/{id}/task/{tid}/done` | 报告任务完成（含 finalSnapshot） |
| `GET` | `/sbot/agent/{id}/pending-task` | 轮询拉取当前任务分配（回退通道） |

### 5.2 前端 -- 任务管理 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/sbot/tasks` | 创建任务（multipart/form-data：flow.json + proto + scripts + adapter + robotConfig） |
| `GET` | `/sbot/tasks` | 列出所有任务（支持 state 过滤、分页） |
| `GET` | `/sbot/tasks/{id}` | 任务详情 |
| `GET` | `/sbot/tasks/{id}/config/{path...}` | 拉取配置文件（Agent 用） |
| `POST` | `/sbot/tasks/{id}/start` | 启动任务（单例约束） |
| `POST` | `/sbot/tasks/{id}/stop` | 停止任务 |
| `DELETE` | `/sbot/tasks/{id}` | 删除任务（仅终态可删） |

### 5.3 前端 -- Agent 管理 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/sbot/agents` | 列出所有 Agent 及简要状态（含 CPU/Mem 摘要） |
| `GET` | `/sbot/agents/{id}` | Agent 详情 |
| `DELETE` | `/sbot/agents/{id}` | 删除离线 Agent |
| `POST` | `/sbot/agents/{id}/shutdown` | 关闭单个 Agent 进程 |
| `POST` | `/sbot/agents/shutdown-all` | 批量关闭所有在线 Agent |

### 5.4 前端 -- 指标 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/sbot/metrics` | 全局聚合压测指标（StressAggregate） |
| `GET` | `/sbot/metrics/summary` | 文本摘要（适合终端查看） |
| `GET` | `/sbot/metrics/agents` | per-agent 压测指标明细 |
| `GET` | `/sbot/metrics/agents/{id}` | 单个 Agent 压测指标 |
| `GET` | `/sbot/system` | 集群系统资源聚合（ClusterSystemSnapshot） |
| `GET` | `/sbot/system/agents` | 所有 Agent 系统指标 per-agent 明细 |
| `GET` | `/sbot/system/agents/{id}` | 单个 Agent 系统指标 |

### 5.5 前端 -- 历史归档 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/sbot/history` | 历史任务列表（分页 + 过滤） |
| `GET` | `/sbot/history/tags` | 列出全部去重 tags |
| `GET` | `/sbot/history/{id}` | 历史任务详情 |
| `PUT` | `/sbot/history/{id}` | 更新 starred/tags/note |
| `DELETE` | `/sbot/history/{id}` | 删除历史记录（starred 需 force=true） |
| `GET` | `/sbot/history/{id}/agents` | 各 Agent 的历史完成报告 |
| `GET` | `/sbot/history/{id}/config` | 任务配置归档 |
| `GET` | `/sbot/history/{id}/timeseries` | 时序数据（趋势图） |
| `POST` | `/sbot/history/{id}/clone` | 用历史任务配置创建新任务 |
| `GET` | `/sbot/history/compare` | 多任务对比（`?ids=a,b,c`，最多 5 个） |

### 5.6 前端 -- 日志 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/sbot/logs/admin` | Admin 日志（ring buffer 查询） |
| `GET` | `/sbot/logs/agents/{id}` | Agent 日志（代理到 Agent 本地 HTTP） |
| `GET` | `/sbot/logs/admin/files` | 列出 Admin 日志文件 |
| `GET` | `/sbot/logs/admin/files/{name}` | 下载 Admin 日志文件 |
| `GET` | `/sbot/logs/agents/{id}/files` | 列出 Agent 日志文件 |
| `GET` | `/sbot/logs/agents/{id}/files/{name}` | 下载 Agent 日志文件 |

### 5.7 前端 -- 资源基线 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/sbot/resources/baseline` | 前端推送 IDB 资源到磁盘基线 |
| `GET` | `/sbot/baseline/proto/index.json` | 列出基线 proto 文件 |
| `GET` | `/sbot/baseline/proto/{name}` | 下载基线 proto 文件 |
| `GET` | `/sbot/baseline/scripts/index.json` | 列出基线脚本文件 |
| `GET` | `/sbot/baseline/scripts/{name}` | 下载基线脚本文件 |
| `GET` | `/sbot/baseline/adapter/index.json` | 列出基线 adapter 文件 |
| `GET` | `/sbot/baseline/adapter/{name}` | 下载指定 codec/errors 文件 |
| `GET` | `/sbot/baseline/flow/flow.json` | 下载基线 flow 配置 |
| `GET` | `/sbot/baseline/config.json` | 下载基线运行配置 |
| `GET` | `/sbot/api/error-codes` | 列出所有错误码 |

### 5.8 Agent 本地 HTTP 端点（Admin Push）

Agent 在 `port`（默认 7719）启动轻量 HTTP 服务器接受 Admin 命令：

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/agent/v1/task` | 接收任务分配 |
| `POST` | `/agent/v1/stop` | 停止当前任务 |
| `POST` | `/agent/v1/shutdown` | 关闭 Agent 进程 |
| `GET` | `/agent/v1/version` | 查询 Agent 版本 |
| `GET` | `/agent/v1/status` | 查询 Agent 状态 |
| `GET` | `/agent/v1/logs` | 查询 Agent 日志（ring buffer） |
| `GET` | `/agent/v1/logs/files` | 列出日志文件 |
| `GET` | `/agent/v1/logs/files/{name}` | 下载日志文件 |
| `GET` | `/healthz` | 健康检查 |

**API 总数**：Admin 端约 51 个端点 + Agent 本地 9 个端点。

---

## 6. 数据模型

### 6.1 Task

```go
type Task struct {
    ID           string       `json:"id"`
    Name         string       `json:"name"`
    State        TaskState    `json:"state"`
    TotalBots    int          `json:"totalBots"`
    Config       TaskConfig   `json:"config"`
    Assignments  []Assignment `json:"assignments,omitempty"`
    Reports      map[string]TaskCompletionReport `json:"reports,omitempty"`
    StageReports []TaskCompletionReport          `json:"stageReports,omitempty"`
    AgentEvents  []AgentEvent                    `json:"agentEvents,omitempty"`
    CreatedAt    time.Time    `json:"createdAt"`
    StartedAt    *time.Time   `json:"startedAt,omitempty"`
    StoppedAt    *time.Time   `json:"stoppedAt,omitempty"`
    ErrorMsg     string       `json:"errorMsg,omitempty"`
}
```

**TaskState** 枚举与状态转换：

```
TaskPending  -> TaskStarting -> TaskRunning -> TaskStopping -> TaskStopped
                  |               |               |
              TaskFailed      TaskFailed      TaskFailed
```

合法转换矩阵（`task.go: validTransition`）：

| from \ to | starting | running | stopping | stopped | failed |
|---|---|---|---|---|---|
| **pending** | YES | | | YES | YES |
| **starting** | | YES | | | YES |
| **running** | | | YES | YES | YES |
| **stopping** | | | | YES | YES |

**Active 状态定义**（占据单例位）：

```go
func IsActiveState(s TaskState) bool {
    return s == TaskStarting || s == TaskRunning || s == TaskStopping
}
```

### 6.2 TaskConfig（配置包）

```go
type TaskConfig struct {
    FlowJSON    json.RawMessage   `json:"flowJson"`
    ProtoFiles  map[string][]byte `json:"protoFiles,omitempty"`
    LuaScripts  map[string][]byte `json:"luaScripts,omitempty"`
    Codecs      map[string][]byte `json:"codecs,omitempty"`   // *_codec.json
    ErrorMap    []byte            `json:"errorMap,omitempty"` // errors.json
    RobotConfig RobotConfig       `json:"robotConfig"`
    Deadline    *time.Time        `json:"deadline,omitempty"`
}
```

### 6.3 RobotConfig

```go
type RobotConfig struct {
    Concurrency    int               `json:"concurrency"`
    TimeoutSec     int               `json:"timeoutSec"`
    AccountPrefix  string            `json:"accountPrefix,omitempty"`
    StartNumber    int               `json:"startNumber,omitempty"`
    MainService    string            `json:"mainService,omitempty"`
    StateExtra     map[string]string `json:"stateExtra,omitempty"`
    HeartbeatSec   int               `json:"heartbeatSec,omitempty"`
    HTTPTimeoutSec int               `json:"httpTimeoutSec,omitempty"`
    ApdexT         int               `json:"apdexT,omitempty"`
    DebugMode      bool              `json:"debugMode,omitempty"`
    LogLevel       string            `json:"logLevel,omitempty"`
    RampUp         *RampUpConfig     `json:"rampUp,omitempty"`
}
```

### 6.4 RampUpConfig（渐进式加压）

```go
type RampUpConfig struct {
    Stages []RampUpStage `json:"stages"`
}

type RampUpStage struct {
    Count       int  `json:"count"`       // 本阶段新增 bot 数（增量值）
    Concurrency int  `json:"concurrency,omitempty"` // 覆盖全局并发数
    HoldSec     int  `json:"holdSec,omitempty"`     // 阶段间等待秒数
    Reset       bool `json:"reset,omitempty"`        // 开始前清空所有已有机器人
}
```

分布式模式下各 Agent 分配的 `RampUp` 按比例缩放（`handlers.go: scaleRampUp`）。

### 6.5 AgentNode

```go
type AgentNode struct {
    ID              string        `json:"agentId"`
    Name            string        `json:"name"`
    Address         string        `json:"address"`
    AppVersion      string        `json:"appVersion"`
    MaxBots         int           `json:"maxBots"`
    StressInterval  string        `json:"stressInterval"`
    SystemInterval  string        `json:"systemInterval"`
    StaticInfo      StaticInfo    `json:"staticInfo"`

    Status          AgentStatus   `json:"status"`
    LastHeartbeatAt time.Time     `json:"lastHeartbeatAt"`
    CurrentTaskID   string        `json:"currentTaskId,omitempty"`
    CurrentBots     int           `json:"currentBots"`

    LatestStress    *monitor.CollectorSnapshot `json:"-"`
    LatestSystem    *SystemSnapshot            `json:"-"`
    StressUpdatedAt time.Time                  `json:"stressUpdatedAt,omitempty"`
    SystemUpdatedAt time.Time                  `json:"systemUpdatedAt,omitempty"`
}
```

**AgentStatus** 枚举：

| 值 | 含义 |
|---|---|
| `idle` | 空闲，可接受任务 |
| `busy` | 执行任务中 |
| `unhealthy` | 心跳超时但仍在线 |
| `offline` | 已离线（自动清理删除） |

**StaticInfo** 结构：

```go
type StaticInfo struct {
    Hostname   string    `json:"hostname"`
    OS         string    `json:"os"`
    Arch       string    `json:"arch"`
    NumCPU     int       `json:"numCpu"`
    MemTotalMB uint64    `json:"memTotalMB"`
    GoVersion  string    `json:"goVersion"`
    KernelVer  string    `json:"kernelVer"`     // 字段存在但实际未采集
    StartedAt  time.Time `json:"startedAt"`
}
```

### 6.6 Assignment

```go
type Assignment struct {
    TaskID      string `json:"taskId"`
    AgentID     string `json:"agentId"`
    AgentName   string `json:"agentName"`
    StartNumber int    `json:"startNumber"`
    TotalBots   int    `json:"totalBots"`
}
```

### 6.7 TaskAssignment（Admin -> Agent）

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

### 6.8 TaskCompletionReport

```go
type TaskCompletionReport struct {
    AgentID       string                     `json:"agentId"`
    TaskID        string                     `json:"taskId"`
    Result        TaskResult                 `json:"result"`     // completed | stopped | failed
    ErrorMsg      string                     `json:"errorMsg,omitempty"`
    FinishedAt    time.Time                  `json:"finishedAt"`
    FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
    StageIndex    int                        `json:"stageIndex,omitempty"` // 渐进式加压阶段索引
}
```

### 6.9 AgentEvent

```go
type AgentEvent struct {
    AgentID   string    `json:"agentId"`
    AgentName string    `json:"agentName"`
    Type      string    `json:"type"`      // "offline" | "reconnected" | "restarted"
    Timestamp time.Time `json:"timestamp"`
    Detail    string    `json:"detail,omitempty"`
}
```

### 6.10 上报报文

```go
type StressReport struct {
    AgentID    string                     `json:"agentId"`
    TaskID     string                     `json:"taskId"`
    ReportedAt time.Time                  `json:"reportedAt"`
    Snapshot   *monitor.CollectorSnapshot `json:"snapshot"`
}

type SystemReport struct {
    AgentID    string         `json:"agentId"`
    ReportedAt time.Time      `json:"reportedAt"`
    Snapshot   SystemSnapshot `json:"snapshot"`
}
```

### 6.11 RegisterRequest / HeartbeatRequest

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

type HeartbeatRequest struct {
    AgentID       string `json:"agentId"`
    Timestamp     string `json:"timestamp"`
    Status        string `json:"status"`              // "idle" | "busy"
    CurrentTaskID string `json:"currentTaskId,omitempty"`
    CurrentBots   int    `json:"currentBots"`
    AppVersion    string `json:"appVersion"`
}
```

---

## 7. 压测指标聚合

### 7.1 MetricsAggregator 结构

```go
type MetricsAggregator struct {
    registry *AgentRegistry    // 聚合器直接引用 Registry，从中读取各 Agent 最新快照
}
```

Agent 上报的指标存储在 `AgentNode.LatestStress` 和 `AgentNode.LatestSystem` 中，`MetricsAggregator` 聚合时从 Registry 遍历所有分配了指定任务的 Agent。

### 7.2 StressAggregate（压测聚合结果）

```go
type StressAggregate struct {
    Snapshot        *monitor.CollectorSnapshot `json:"snapshot"`
    ReportingAgents int                        `json:"reportingAgents"`
    TotalAgents     int                        `json:"totalAgents"`
    OfflineAgents   int                        `json:"offlineAgents"`
    AssignedAgents  int                        `json:"assignedAgents"`
}
```

### 7.3 压测指标聚合规则

| 指标类型 | 聚合方式 |
|---|---|
| 计数器（success/failure/timeout/canceled） | **求和** |
| 执行中数（executing） | **求和** |
| 机器人状态（started/running/stopped/errored） | **求和** |
| 连接指标（established/failed/dropped） | **求和** |
| 带宽（sendBytes/recvBytes） | **求和** |
| 延迟直方图 | **合并桶计数** -> 重算百分位 |
| Apdex | 用合并后的 satisfied/tolerating **重新计算** |
| 错误分布 | 相同 `code` 的计数**相加**，Messages 取并集去重（上限 5 条） |

### 7.4 延迟直方图

`LatencyHistogram` 使用 **16 个固定桶**，桶边界（毫秒）：

```
[0,1) [1,2) [2,5) [5,10) [10,20) [20,50) [50,100) [100,200)
[200,500) [500,1000) [1000,2000) [2000,5000) [5000,10000)
[10000,30000) [30000,60000) [60000,+inf)
```

**HistogramSnapshot** 新增两个 `omitempty` 字段支持分布式合并：

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

    SumNs        int64   `json:"sumNs,omitempty"`        // 延迟总和（纳秒）
    BucketCounts []int64 `json:"bucketCounts,omitempty"` // 16 个桶的计数
}
```

**MergeHistograms** 算法：

1. 合并各快照的 `Count`、`SumNs`
2. 取全局 Min/Max
3. 对应桶计数相加
4. 用合并后的桶计数通过 `percentileFromBuckets`（前缀和 + 线性插值）重算 P50/P90/P95/P99
5. 用合并后的 `SumNs / Count / 1e6` 计算 AvgMs

**精度保证**：所有 Agent 使用相同的 16 个固定桶，合并桶计数后百分位精度与单节点一致。

### 7.5 CollectorSnapshot 合并

`MergeSnapshots` 合并多个 `CollectorSnapshot`：

- `TotalActions`、`Robots`、`Connections`、`Bandwidth` 直接求和
- `Uptime` 取最大值（代表最长的运行时长）
- 按 action name 分组，合并延迟直方图、Apdex、错误分布
- 带宽速率用合并后的总字节数 / 最大 uptime 重新计算

### 7.6 压测聚合响应格式

`GET /sbot/metrics` 响应：

```json
{
  "snapshot": {
    "timestamp": "2026-05-23T10:00:00+08:00",
    "uptimeSeconds": 125.4,
    "totalActions": 584920,
    "apdexT": 100,
    "robots": { "started": 1000, "running": 987, "stopped": 13, "errored": 0 },
    "connections": { "established": 1000, "failed": 2, "dropped": 0 },
    "bandwidth": { "totalSendBytes": 5242880, "totalRecvBytes": 10485760, "sendMBps": 2.5, "recvMBps": 5.0 },
    "actions": [
      {
        "name": "Login",
        "sampleCount": 1000,
        "successCount": 998,
        "successRate": 0.998,
        "apdex": 0.95,
        "latency": { "count": 998, "minMs": 1.2, "maxMs": 150.3, "avgMs": 5.4, "p50Ms": 3.2, "p99Ms": 45.2 },
        "errors": []
      }
    ]
  },
  "reportingAgents": 2,
  "totalAgents": 3,
  "offlineAgents": 1,
  "assignedAgents": 3
}
```

---

## 8. Agent 系统监控

### 8.1 采集指标清单

| 类别 | 指标 | 来源 | 单位 |
|---|---|---|---|
| **CPU** | 总体使用率 | `gopsutil/cpu.Percent(0, false)` | % |
| | 每核使用率 | `gopsutil/cpu.Percent(0, true)` | %[] |
| | 核数 | `runtime.NumCPU()` | 个 |
| | 系统负载（Linux/macOS） | `gopsutil/load.Avg` | 1m/5m/15m |
| **内存** | 系统总内存 | `gopsutil/mem.VirtualMemory.Total` | MB |
| | 系统已用内存 | `gopsutil/mem.VirtualMemory.Used` | MB |
| | 系统使用率 | `.UsedPercent` | % |
| | Swap 已用 | `gopsutil/mem.SwapMemory.Used` | MB |
| | 进程 RSS | `gopsutil/process.MemoryInfo.RSS` | MB |
| | Go 堆分配 | `runtime.MemStats.HeapAlloc` | MB |
| | Go Sys 总占用 | `runtime.MemStats.Sys` | MB |
| **网络** | 系统发送速率 | 差分计算 | KB/s |
| | 系统接收速率 | 差分计算 | KB/s |
| **进程线程** | OS 线程数 | `gopsutil/process.NumThreads` | 个 |
| **Go 协程** | goroutine 数 | `runtime.NumGoroutine()` | 个 |
| **GC** | GC 总次数 | `runtime.MemStats.NumGC` | 次 |
| | GC 平均暂停 | 最近 256 次 PauseNs 均值 | ms |
| **文件描述符** | FD 数 | `gopsutil/process.NumFDs` | 个 |

### 8.2 SystemSnapshot 数据结构（Agent 侧）

```go
type SystemSnapshot struct {
    Timestamp    time.Time `json:"timestamp"`

    CPUPercent   float64   `json:"cpuPercent"`
    CPUPerCore   []float64 `json:"cpuPerCore"`
    LoadAvg1     float64   `json:"loadAvg1"`
    LoadAvg5     float64   `json:"loadAvg5"`
    LoadAvg15    float64   `json:"loadAvg15"`

    MemTotalMB   uint64  `json:"memTotalMB"`
    MemUsedMB    uint64  `json:"memUsedMB"`
    MemPercent   float64 `json:"memPercent"`
    SwapUsedMB   uint64  `json:"swapUsedMB"`
    ProcessRssMB uint64  `json:"processRssMB"`
    ProcessHeapMB uint64 `json:"processHeapMB"`
    ProcessSysMB uint64  `json:"processSysMB"`

    NumGoroutine int    `json:"numGoroutine"`
    NumThread    int32  `json:"numThread"`
    NumFD        int32  `json:"numFd"`
    GCCount      uint32 `json:"gcCount"`
    GCPauseAvgMs float64 `json:"gcPauseAvgMs"`

    NetSendKBps  float64 `json:"netSendKBps"`
    NetRecvKBps  float64 `json:"netRecvKBps"`
}
```

### 8.3 SystemMonitor 实现

```go
type SystemMonitor struct {
    interval time.Duration
    static   StaticInfo

    mu     sync.RWMutex
    latest SystemSnapshot

    // 网络速率差分基线
    prevNetSent uint64
    prevNetRecv uint64
    prevAt      time.Time
    initialized bool

    pid  int32
    self *process.Process
}
```

**采集流程**（`collect()`）：
1. CPU 百分比（非阻塞，`interval=0`）
2. 每核 CPU
3. 系统负载（1m/5m/15m）
4. 虚拟内存 + Swap
5. 进程 RSS、线程数、FD 数
6. Go 运行时：HeapAlloc、Sys、NumGoroutine、NumGC、PauseNs
7. 网络速率：当前累计 - 上次累计 / 时间差

### 8.4 集群系统聚合

```go
type ClusterSystemSnapshot struct {
    Timestamp        time.Time          `json:"timestamp"`
    AgentCount       int                `json:"agentCount"`
    OnlineCount      int                `json:"onlineCount"`
    OfflineCount     int                `json:"offlineCount"`
    AvgCPUPercent    float64            `json:"avgCpuPercent"`     // 按核数加权平均
    MaxCPUPercent    float64            `json:"maxCpuPercent"`     // 最高的那台
    HotAgentID       string             `json:"hotAgentId,omitempty"`
    HotAgentName     string             `json:"hotAgentName,omitempty"`
    TotalMemMB       uint64             `json:"totalMemMB"`
    UsedMemMB        uint64             `json:"usedMemMB"`
    TotalNetSendKBps float64            `json:"totalNetSendKBps"`
    TotalNetRecvKBps float64            `json:"totalNetRecvKBps"`
    TotalGoroutines  int                `json:"totalGoroutines"`
    TotalThreads     int32              `json:"totalThreads"`
    TotalFDs         int32              `json:"totalFds"`
    Agents           []AgentSystemBrief `json:"agents"`
}
```

| 指标 | 聚合方式 | 备注 |
|---|---|---|
| CPU% | 按核数加权平均 + 最大值（`hot` 标记） | 突出热点节点 |
| 内存（总/已用） | 求和 | 集群总容量视角 |
| 网络速率 | 求和 | 集群总流量 |
| Goroutine / Thread / FD | 求和 | 集群总览 |
| Load Avg | **不聚合** | per-agent 单独显示 |

---

## 9. 任务生命周期

```
1. 创建任务
   前端 POST /sbot/tasks (multipart: flow.json + proto + scripts + adapter + robotConfig)
   -> Admin 创建 Task (state=pending)
   -> 写入 data/tasks/{id}.json
   -> 资源写入磁盘基线 (conf/flow, conf/proto, conf/scripts, conf/adapter)

2. 启动任务
   前端 POST /sbot/tasks/{id}/start
   -> Admin:
     a. TaskStore.StartTask() -- 单例约束检查 (startMu.Lock)
     b. Transition: pending -> starting
     c. 过滤 status=idle 的 Agent
     d. Assigner.Assign() -- 按比例分配（或调试模式单节点）
     e. 保存 assignments
     f. 启动 Sampler（如果 history 已启用）
     g. 异步推送 TaskAssignment 到各 Agent
     h. 全部成功 -> Transition: starting -> running
        任一失败 -> 回收已推送的 Agent -> Transition: starting -> failed

3. Agent 执行
   a. 收到 TaskAssignment
   b. 从 Admin 拉取配置 (GET /sbot/tasks/{id}/config/{path})
   c. 写入临时目录
   d. 创建 TaskRunner -> 加载 adapter + proto + flow + scripts
   e. 创建 StressReporter + 启动压测上报循环
   f. robot.Manager.StartAll (或 RampUp 渐进加压)
   g. 运行直到 stop / context cancel

4. 前端监控
   前端每 5s GET /sbot/metrics  -> 压测聚合数据
   前端每 5s GET /sbot/system   -> 系统聚合数据

5. 停止任务
   前端 POST /sbot/tasks/{id}/stop
   -> Admin: Transition running -> stopping
   -> 向各 Agent 推送停止命令 (POST /agent/v1/stop)
   -> 为已离线 Agent 合成 stopped report
   -> 启动 30s 停止超时安全网
   -> Agent: mgr.StopAll() -> 上报 finalSnapshot -> POST /done
   -> Admin: 所有 Agent 上报 done -> Transition stopping -> stopped

6. 终态归档
   Transition 到 stopped/failed -> 触发 onTerminal 回调
   -> Sampler.Stop()
   -> 异步 HistoryStore.Archive()：写入 MySQL 7 张表
```

---

## 10. Agent 设计

### 10.1 Agent 进程结构

```go
type Agent struct {
    id      string
    cfg     *ResolvedConfig
    started time.Time
    ctx     context.Context
    cancel  context.CancelFunc

    sysmon    *SystemMonitor
    collector *monitor.MetricsCollector
    httpSrv   *http.Server
    httpCli   *AdminClient

    mu          sync.Mutex
    status      AgentStatus           // idle | busy
    currentTask *TaskAssignment
    taskCancel  context.CancelFunc

    taskWG sync.WaitGroup

    sysReporter    *SystemReporter
    stressReporter *StressReporter

    stopCh   chan struct{}
    stopOnce sync.Once

    regGeneration atomic.Int64  // 注册重置版本号，防止旧任务回调污染新生命周期
}
```

### 10.2 Agent 运行的循环

| 循环 | 间隔 | 功能 |
|---|---|---|
| **系统采集循环** | 5s（默认） | SystemMonitor 后台 goroutine，更新本地 `latest` 快照 |
| **心跳循环** | 10s（成功）/ 同值（失败） | POST `/sbot/agent/{id}/heartbeat`，确认存活 + 当前 status |
| **系统上报循环** | 5s（默认） | POST `/sbot/agent/system`（SystemSnapshot），始终运行 |
| **压测上报循环** | 5s（默认） | POST `/sbot/agent/stress`（StressReport），仅任务运行时 |
| **任务轮询循环** | 30s | GET `/sbot/agent/{id}/pending-task`，回退通道感知新任务 |
| **HTTP 服务器** | -- | 监听 `/agent/v1/task`、`/agent/v1/stop` 等，接受 Admin Push |

### 10.3 Agent 配置

```go
type Config struct {
    Enabled             bool   `json:"enabled"`             // 默认 false
    AdminAddr           string `json:"adminAddr"`           // Admin 地址
    PublicURL           string `json:"publicUrl"`           // Agent 对外地址（不填自动获取本机 IP）
    Port                int    `json:"port"`                // 本地 HTTP 端口（默认 7719）
    MaxBots             int    `json:"maxBots"`             // 最大机器人数量（默认 5000）
    HBInterval          string `json:"hbInterval"`          // 心跳间隔（默认 10s）
    HBRequestTimeout    string `json:"hbRequestTimeout"`    // 单次心跳请求超时（默认 5s）
    HBFailThreshold     int    `json:"hbFailThreshold"`     // 任务运行中连续心跳失败容忍次数（默认 3）
    RequestTimeout      string `json:"requestTimeout"`      // 单次 HTTP 超时（默认 30s）
    ReconnectMaxRetries int    `json:"reconnectMaxRetries"` // 最大重连次数（默认 -1 = 持续重连）
    StressInterval      string `json:"stressInterval"`      // 压测指标上报间隔（默认 5s）
    AppVersion          string `json:"-"`                   // 编译时注入
}

type ResolvedConfig struct {
    AdminAddr            string
    Name                 string        // 节点名称（主机名）
    Address              string        // Agent 对外可达地址
    Port                 int           // HTTP 监听端口
    MaxBots              int           // 最大机器人数量
    AppVersion           string
    TaskWorkDir          string        // 系统临时目录
    StressInterval       time.Duration // 压测上报间隔
    SystemInterval       time.Duration // 系统上报间隔（与 StressInterval 同步）
    HBInterval           time.Duration // 心跳间隔
    HBFailInterval       time.Duration // 心跳失败重试间隔
    HBRequestTimeout     time.Duration // 心跳单次请求超时
    HBFailThreshold      int           // 任务运行中容忍的连续心跳失败次数
    RequestTimeout       time.Duration // HTTP 超时
    ReconnectInterval    time.Duration // 5s
    ReconnectMaxInterval time.Duration // 60s
    ReconnectMaxRetries  int           // -1 = 持续重连
    TaskReportTimeout    time.Duration // 30s
}
```

### 10.4 心跳循环行为规则

- 心跳成功用 `HBInterval`；失败用 `HBFailInterval`
- 心跳单次请求超时受 `HBRequestTimeout`（默认 5s）控制，独立于通用 `RequestTimeout`（30s），保证快失败快重试
- 任意请求收到 404（`errNotRegistered`）-> 视为 Admin 重启，立即取消任务并重新注册
- 任务运行中（Busy）连续心跳失败累计 `HBFailThreshold` 次（默认 3）-> 取消当前任务；单次抖动不会误伤
- 持续失败不退进程（除非重新注册超出 `ReconnectMaxRetries`）

> **与 Admin 端阈值的联动约束**：`HBFailThreshold × HBInterval` 必须 ≤ `admin.unhealthyAfter`（默认 3×10s = 30s = unhealthyAfter），并且 `admin.offlineAfter > admin.unhealthyAfter`。
> 调大 `HBFailThreshold` 时务必同步调大 `admin.unhealthyAfter` / `admin.offlineAfter`，否则会出现"节点已被 Admin 标 unhealthy/删除，但 Agent 任务还在跑"的状态错乱。

### 10.5 任务执行流程

```go
func (a *Agent) executeTask(parentCtx context.Context, task *TaskAssignment) {
    // 1. 状态迁移：idle -> busy
    // 2. 创建 StressReporter 并启动
    // 3. 创建 TaskRunner
    // 4. 注入 OnStageReset 回调（渐进式加压阶段重置时上报中间指标）
    // 5. runner.Run(taskCtx) -- 阻塞直到任务完成
    // 6. 停止 StressReporter
    // 7. 采集 finalSnapshot
    // 8. 上报任务完成（POST /sbot/agent/{id}/task/{tid}/done）
    //    使用 context.Background()（脱离已 cancel 的 taskCtx）
    // 9. 清理临时目录
}
```

### 10.6 优雅关闭

```
1. cancelCurrentTask() -> 等待 executeTask 自然结束（含 finalSnapshot 上报）
2. taskWG.Wait()（最长 TaskReportTimeout + 5s）
3. 停止 StressReporter / SystemReporter
4. cancel 全局 ctx -> 心跳/轮询/上报循环退出
5. 注销（best-effort，5s 超时）
6. 关闭 HTTP 服务器
7. 关闭协程池
```

### 10.7 注册重置版本号（regGeneration）

`regGeneration` 是一个 `atomic.Int64`，每次重新注册成功后递增。用于防止旧任务的回调（如 `OnStageReset`）污染新生命周期。stressReporter / taskCancel 等按生命周期分配的资源都和它绑定。

---

## 11. Admin Server 设计

### 11.1 AdminServer 结构

```go
type AdminServer struct {
    cfg Config

    tasks      *TaskStore          // 任务存储（内存 + JSON 持久化）
    agents     *AgentRegistry      // Agent 注册表
    aggregator *MetricsAggregator  // 指标聚合器
    dispatcher *AgentDispatcher    // Admin -> Agent HTTP 通信
    assigner   *Assigner           // 任务分配器

    logsProxyClient *http.Client   // Agent 日志代理（5s 超时）

    history *HistoryStore          // 历史归档（可选）
    sampler *Sampler               // 时序采样（可选）

    httpSrv *http.Server
    stopCh  chan struct{}
    wg      sync.WaitGroup
}
```

### 11.2 初始化流程（`NewAdminServer`）

```
1. TaskStore（dataDir="data"）
2. AgentRegistry（含 onChange 回调）
3. MetricsAggregator
4. AgentDispatcher
5. logsProxyClient（5s 超时）
6. Assigner
7. HistoryStore（可选，history.enabled=true 时初始化）
   -> Sampler（10s 间隔）
8. 终态回调 SetOnTerminal -> onTaskTerminal
```

### 11.3 路由注册

通过 `http.NewServeMux()` 注册，使用 Go 1.22+ 的 `"METHOD /path"` 模式匹配。顶层包裹 `recoverMiddleware`，捕获 handler panic 并返回标准 500 JSON。

### 11.4 心跳超时处理

后台 goroutine（5s 间隔）定期检查：

- `LastHeartbeatAt + unhealthyThreshold` -> `unhealthy`
- `LastHeartbeatAt + offlineThreshold` -> `offline`（并自动删除）

默认值：
- `unhealthyAfter` = 30s
- `offlineAfter` = 60s

`AgentRegistry.fireOnChange()` 在锁外触发回调，避免死锁。

### 11.5 Agent 状态变更回调

`AdminServer.onAgentStatusChange` 处理以下场景：

| 场景 | 行为 |
|---|---|
| 已分配节点重新注册（busy->idle） | 合成 failed report，检查是否全部节点失效 |
| 分配节点恢复（offline->idle/busy） | 记录 reconnected 事件 |
| 分配节点离线（->offline） | 记录 offline 事件；stopping 中合成 report；running 中检查是否全部失效 |
| 全部分配节点失效 | 调用 `autoStopTask` 自动停止任务 |

### 11.6 Deadline 看门狗

`startDeadlineWatchdog` 每 5s 检查活跃任务是否超过 `config.deadline`，超时则 `autoStopTask`。

---

## 12. 任务分配算法

### 12.1 Assigner 实现

```go
type Assigner struct{}

func (a *Assigner) Assign(task *Task, agents []*AgentNode, startNumber int) ([]Assignment, error)
```

**分配流程**：

1. 过滤 `status=idle` 且 `maxBots > 0` 的 Agent
2. 检查总容量 >= task.TotalBots
3. 调试模式（`debugMode=true`）：优先找单个容量够的 Agent
4. 比例分配：`proportionalAssign`

### 12.2 比例分配算法

```go
func (a *Assigner) proportionalAssign(task *Task, agents []*AgentNode, startNumber int) []Assignment
```

1. 计算每个 Agent 的基础分配数：`totalBots * agent.MaxBots / totalCapacity`
2. 计算余数：`totalBots - sum(basic)`
3. 余数按最大剩余小数分配给前 N 个 Agent
4. 按 `startNumber` 累加生成各 Agent 的 StartNumber

---

## 13. 任务单例约束

### 13.1 TaskStore 的单例保证

```go
type TaskStore struct {
    mu       sync.RWMutex
    tasks    map[string]*Task
    startMu  sync.Mutex    // 单例约束锁
    activeID string        // 当前活跃任务 ID
    // ...
}
```

### 13.2 启动流程（`StartTask`）

```
POST /sbot/tasks/{id}/start
  + startMu.Lock()
  + if activeID != "" -> 返回 409 TASK_CONFLICT（含 activeTaskId/activeName/activeState/startedAt）
  + Transition(id, pending -> starting)
  + activeID = id
  + startMu.Unlock()
  + 异步分配并下发
```

### 13.3 状态分类

| 类别 | 包含状态 | 含义 | 数量限制 |
|---|---|---|---|
| **Active** | `starting`、`running`、`stopping` | 正在占用 Agent 执行 | **全集群最多 1 个** |
| **Inactive** | `pending`、`stopped`、`failed` | 未占用 Agent | 不限数量 |

### 13.4 拒绝并发的错误返回

```json
{
  "code": "TASK_CONFLICT",
  "message": "TASK_CONFLICT",
  "details": {
    "activeTaskId": "abc123",
    "activeName": "200v200 压测",
    "activeState": "running",
    "startedAt": "2026-05-23T10:00:00+08:00"
  }
}
```

### 13.5 Admin 重启处理

Admin 重启时 `NewTaskStore` 从 `data/tasks/*.json` 恢复：
- 所有 Active 状态的任务重置为 `failed`（`errorMsg: "admin restart, task lost"`）
- 设置 `stoppedAt` 为当前时间
- 记录到 `recoveredIDs`
- `SetOnTerminal` 后触发归档

---

## 14. 配置文件

### 14.1 Admin 配置（`conf/admin-config.json`）

```json
{
  "port": 7718,
  "publicUrl": "http://192.168.1.100:7718",
  "staticDir": "cmd/web/dist",
  "agentRegistry": {
    "unhealthyAfter": "30s",
    "offlineAfter": "60s"
  },
  "history": {
    "enabled": true,
    "mysql": {
      "host": "127.0.0.1",
      "port": 3306,
      "user": "stressbot",
      "password": "xxx",
      "database": "stressbot_history",
      "maxOpenConns": 10,
      "maxIdleConns": 5,
      "connMaxLifetime": "1h"
    },
    "retentionDays": 90
  },
  "log": {
    "level": "info",
    "path": "log/admin.log",
    "maxSizeMB": 100,
    "maxBackups": 10
  }
}
```

**Config 结构体**：

```go
type Config struct {
    Port          int            `json:"port"`          // 默认 7718
    PublicURL     string         `json:"publicUrl"`     // 必填，Agent 连接用
    StaticDir     string         `json:"staticDir"`     // 默认 cmd/web/dist
    AgentRegistry RegistryConfig `json:"agentRegistry"`
    History       HistoryConfig  `json:"history"`
    Log           LogConfig      `json:"log"`
    Daemon        bool           `json:"daemon"`        // 仅 Linux
}
```

### 14.2 Agent 配置（`conf/config.json` 的 `agent` 段）

```json
{
  "agent": {
    "enabled": false,
    "adminAddr": "http://192.168.1.100:7718",
    "publicUrl": "",
    "port": 7719,
    "maxBots": 5000,
    "hbInterval": "10s",
    "stressInterval": "5s",
    "requestTimeout": "30s",
    "reconnectMaxRetries": -1
  }
}
```

---

## 15. 历史压测记录

### 15.1 数据库表（7 张）

```
task_history          -- 主表：id、name、state、起止时间、tags、note、starred、duration_sec、stage_count
task_assignment       -- 集群分配快照（每个 Agent 一条）
task_report           -- 各 Agent 的完成报告 + finalSnapshot（JSON 列）+ stage_index
task_aggregated       -- 集群聚合的终态 stress/system 快照（复合主键 task_id + stage_index）
task_timeseries       -- 运行期采样点（每 10s 一条，stress/system 双类型）
task_config_archive   -- 任务配置归档（flow.json + protos + scripts + robotConfig）
task_agent_events     -- Agent 状态变化事件（离线、重连、重启）
```

### 15.2 HistoryStore

```go
type HistoryStore struct {
    cfg    HistoryConfig
    db     *sql.DB
    prune  time.Duration
    cancel context.CancelFunc
}
```

**核心方法**：

| 方法 | 说明 |
|---|---|
| `Archive(ctx, task, finalStress, finalSys)` | 终态归档（事务写入 7 张表） |
| `List(ctx, filter)` | 分页查询（支持 state/时间范围/tags/starred/search/排序） |
| `Get(ctx, id)` | 单条详情（含 assignments + reports + aggregated + events） |
| `GetConfig(ctx, id)` | 获取配置归档 |
| `GetTimeseries(ctx, id)` | 获取时序数据 |
| `AllTags(ctx)` | 返回去重 tags 列表（JSON_TABLE + fallback） |
| `UpdateMeta(ctx, id, req)` | 更新 starred/tags/note |
| `Delete(ctx, id, force)` | 删除（starred 需 force=true） |
| `PruneExpired(ctx, now)` | 清理过期记录 |
| `AppendTimeseries(ctx, taskID, point)` | 追加时序采样点 |

### 15.3 Sampler

```go
type Sampler struct {
    interval   time.Duration           // 默认 10s
    aggregator *MetricsAggregator
    history    *HistoryStore
    registry   *AgentRegistry
    mu         sync.Mutex
    current    *samplerJob
}
```

任务运行期间每 `interval` 采样一次：
- 聚合压测指标 -> `INSERT task_timeseries (data_type='stress')`
- 聚合系统指标 -> `INSERT task_timeseries (data_type='system')`

### 15.4 归档数据流

```
[运行期]
任务 starting -> Sampler.Start()
每 10s：AggregateStress + AggregateSystem -> AppendTimeseries

[终态]
Transition -> stopped/failed -> onTerminal 回调：
  1. Sampler.Stop()
  2. buildFinalStressFromReports() -- 优先用 Agent 终止报告聚合，兜底用心跳聚合
  3. AggregateSystem()
  4. HistoryStore.Archive() -- 事务写入：
     a. INSERT task_history（ON DUPLICATE KEY UPDATE）
     b. INSERT task_assignment
     c. INSERT task_report（含阶段完成报告）
     d. INSERT task_aggregated（ON DUPLICATE KEY UPDATE）
     e. INSERT task_config_archive（ON DUPLICATE KEY UPDATE）
     f. INSERT task_agent_events
```

### 15.5 标记功能

| 字段 | 类型 | 用途 |
|---|---|---|
| `starred` | TINYINT(1) | "收藏"，列表页置顶，不被自动清理 |
| `tags` | JSON | 自由标签，支持 `tags`（任一匹配）和 `tagsAll`（全部匹配）过滤 |
| `note` | TEXT | 备注 |

### 15.6 历史查询过滤

```go
type HistoryFilter struct {
    State         string     // 按状态
    StartedAfter  time.Time  // 开始时间下界
    StartedBefore time.Time  // 开始时间上界
    Tags          []string   // 包含任一标签
    TagsAll       []string   // 包含全部标签
    Starred       *bool      // 仅收藏
    Search        string     // 搜索 name/ID
    Limit         int        // 上限（1-100，默认 20）
    Offset        int        // 分页偏移
    OrderBy       string     // 白名单防注入
}
```

### 15.7 数据保留策略

- 默认保留 90 天（`retentionDays` 可配）
- 超过保留期且 `starred=false` 的任务**事务删除**（子表 + 主表原子）
- `starred=true` 的任务**永不自动删除**
- MySQL 不可用 / `history.enabled=false` 时：`HistoryStore` 为 nil，所有历史接口返回 `HISTORY_DISABLED`

### 15.8 克隆与对比

- **克隆**：`POST /sbot/history/{id}/clone` -- 从历史配置创建一个 `pending` 任务（可覆盖 name）
- **对比**：`GET /sbot/history/compare?ids=a,b,c` -- 最多 5 个任务的 P99 延迟对比

---

## 16. 故障处理

### 16.1 Agent 宕机

1. 30s 超时 -> `unhealthy`
2. 60s 超时 -> `offline`（自动从注册表删除）
3. `onAgentStatusChange` 回调触发：
   - 任务 `stopping` 中：合成 failed report，检查是否全部完成
   - 任务 `running` 中：记录 offline 事件，检查是否全部节点失效
   - 全部失效 -> `autoStopTask`

### 16.2 Agent 进程重启（重新注册）

当已分配任务的 Agent 重新注册（busy/unhealthy -> idle）：
- 合成 failed report（`"Agent 重新注册，任务已丢失"`）
- 记录 `restarted` 事件
- 检查是否全部节点失效

### 16.3 Admin 宕机

1. Agent 继续自治，`robot.Manager` 不依赖 Admin 连接
2. Agent 心跳失败（收到 404）-> 取消当前任务并重新注册
3. Admin 恢复后 Agent 心跳触发自动重注册
4. Admin 重启时所有 Active 任务重置为 `failed` 并归档

### 16.4 停止超时安全网

停止任务后启动 30s 超时安全网：

```go
func (s *AdminServer) startStopTimeout(taskID string) {
    // 30s 后如果仍在 stopping：
    // 为所有未上报节点合成 "停止超时，节点未响应" report
    // Transition: stopping -> stopped
}
```

### 16.5 StressReport 过期丢弃

`handleAgentStressReport` 检查上报的 `taskID` 是否与 Agent 当前任务匹配：
- 不匹配 -> 丢弃（避免跨任务串数据）
- 返回 `{"status": "stale"}`

### 16.6 网络分区

同 Agent 宕机，由心跳超时机制检测。Agent 侧不依赖实时连接维持任务。

---

## 17. 错误码体系

### 17.1 Admin 预定义错误

```go
var (
    ErrTaskNotFound     = NewError("TASK_NOT_FOUND",     404)
    ErrTaskConflict     = NewError("TASK_CONFLICT",      409)
    ErrTaskInvalidState = NewError("TASK_INVALID_STATE",  409)
    ErrAgentNotFound    = NewError("AGENT_NOT_FOUND",     404)
    ErrAgentBusy        = NewError("AGENT_BUSY",          409)
    ErrAgentOffline     = NewError("AGENT_OFFLINE",       409)
    ErrCapacityExceeded = NewError("CAPACITY_EXCEEDED",   400)
    ErrInvalidArgument  = NewError("INVALID_ARGUMENT",    400)
    ErrHistoryDisabled  = NewError("HISTORY_DISABLED",    503)
    ErrHistoryNotFound  = NewError("HISTORY_NOT_FOUND",   404)
    ErrInternal         = NewError("INTERNAL_ERROR",      500)
    ErrStarredProtected = NewError("HISTORY_STARRED",     409)
)
```

### 17.2 Error 结构

```go
type Error struct {
    Code       string         `json:"code"`
    HTTPStatus int            `json:"-"`
    Message    string         `json:"message"`
    Details    map[string]any `json:"details,omitempty"`
}
```

---

## 18. AgentDispatcher（Admin -> Agent 通信）

```go
type AgentDispatcher struct {
    httpClient *http.Client    // 30s 超时
}
```

| 方法 | 说明 |
|---|---|
| `AssignTask(addr, assignment)` | POST `/agent/v1/task`（重试 2 次） |
| `Stop(addr, taskID)` | POST `/agent/v1/stop`（重试 2 次） |
| `Shutdown(addr)` | POST `/agent/v1/shutdown`（重试 1 次） |
| `Version(addr)` | GET `/agent/v1/version` |

重试策略：指数退避（1s -> 2s -> 4s -> 上限 10s）。

---

## 19. 任务持久化

### 19.1 文件格式

任务状态持久化到 `data/tasks/{id}.json`，使用 JSON 格式。

```go
func saveTaskFile(dataDir string, task *Task) error {
    // 1. JSON MarshalIndent
    // 2. 写入临时文件 {id}.json.tmp
    // 3. Rename 覆盖（原子操作）
}
```

### 19.2 恢复流程

```go
func loadTaskFiles(dataDir string) ([]*Task, error) {
    // 1. 清理旧版 .tmp.json 残留
    // 2. 遍历 data/tasks/*.json
    // 3. Unmarshal 为 Task
    // 4. Active 状态重置为 failed
}
```

---

## 20. 已废弃功能

### 20.1 热更新与滚动升级

原计划的 Phase 6（Launcher + Agent 进程双二进制模型 + 文件 IPC 协议 + 自动回滚）**已全部废弃**。原因：

- 升级改为运维手动重启 Agent 进程
- 避免引入 Windows `ERROR_SHARING_VIOLATION` 等平台复杂性
- Launcher 二进制（`cmd/launcher/`）未实现

以下 API **未实现**：
- `POST /api/binaries` -- 二进制上传
- `GET /api/binaries` -- 二进制列表
- `POST /api/agents/{id}/upgrade` -- 单 Agent 升级
- `POST /api/agents/upgrade-all` -- 滚动升级
- `POST /agent/v1/upgrade` -- Agent 端升级处理
- `POST /agent/v1/restart` -- Agent 端重启

---

## 21. 部署防火墙要求

| 方向 | 端口 | 说明 |
|---|---|---|
| 前端 -> Admin | 7718/tcp | REST API + 静态文件托管 |
| Agent -> Admin | 7718/tcp | 注册、心跳、双指标上报、配置拉取 |
| Admin -> Agent | 7719/tcp | 命令 Push（assign/stop/shutdown） |
| Admin -> MySQL | 3306/tcp | 历史归档（可选） |

---

## 22. 验证方案

### 本机模拟分布式

```bash
go build -o admin.exe ./cmd/admin
go build -o stressbot.exe ./cmd/agent

# 终端 1：Admin
./admin.exe -config conf/admin-config.json

# 终端 2/3：两个 Agent 节点
./stressbot.exe -config conf/agent1-config.json   # agent.port: 7719
./stressbot.exe -config conf/agent2-config.json   # agent.port: 7720

# 终端 4：操作
curl -X POST http://localhost:7718/sbot/tasks ...
curl -X POST http://localhost:7718/sbot/tasks/{id}/start
curl http://localhost:7718/sbot/metrics
curl http://localhost:7718/sbot/system
curl http://localhost:7718/sbot/agents
curl -X POST http://localhost:7718/sbot/tasks/{id}/stop
```
