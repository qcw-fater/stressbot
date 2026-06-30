# Admin 服务端实施文档

> **角色定位**：Admin 是分布式压测系统的**控制中枢**，对前端提供管控 API（任务/Agent/指标/系统/历史/日志/基线资源），对 Agent 提供注册/心跳/上报接口和命令下发能力。
> **本文档目标读者**：负责 `admin/` 包及 `cmd/admin/` 入口的开发者。
> **文档性质**：本文档基于实际代码生成，标注了与原始设计计划的差异。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- Admin 二进制：`admin.exe`（Linux：`admin`），来自 `cmd/admin`
- 默认监听端口：`7718`
- 数据目录：`data/`（任务持久化）
- 静态资源目录：`cmd/web/dist/`（前端构建产物）
- HTTP 路由前缀：`/sbot/`
- Agent 主文档 = `docs/agent-implementation.md`，所有协议契约可双向交叉验证

---

## 1. 模块职责

Admin 同时承担五类职责：

1. **集群管理**：维护 Agent 注册表（增删改查、健康检测、版本追踪）
2. **任务编排**：接收前端请求 -> 单例校验 -> 分配账号范围 -> 推送任务 -> 监控完成
3. **指标聚合**：合并多 Agent 上报的压测/系统指标，生成集群快照供前端轮询
4. **历史归档**：任务运行期定时采样 + 终态归档至 MySQL，支持回查、对比、克隆、标签
5. **运维支持**：Agent 关机命令下发、前端静态资源托管、基线资源读写、日志代理

不做的事：
- 不做用户认证（内网工具，假定可信网络）
- 不做指标告警（监控系统职责由外部 Prometheus 等承担，可后续接入）
- 不做任务排队调度（任务单例约束，并发启动直接拒绝）
- 不做二进制存储与分发（已废弃）
- 不做自动升级编排（已废弃，升级改为运维手动重启 Agent 进程）

---

## 2. 包结构与文件清单

```
admin/
  admin.go               -- AdminServer 主结构、HTTP 路由注册、生命周期、Agent 状态变更处理
  config.go              -- Config 解析与校验、默认值
  types.go               -- 全部 DTO 类型定义（Task/Agent/Report/Snapshot/History 等）
  task.go                -- TaskStore 实现 + 状态机 + 单例约束 + 文件恢复
  agent.go               -- AgentRegistry + 心跳超时检测 + 状态恢复
  assignment.go          -- Assigner：比例分配 + debug 单节点分配
  aggregator.go          -- MetricsAggregator：压测/系统指标聚合
  agent_dispatcher.go    -- Admin -> Agent HTTP 通信（带重试）
  history.go             -- HistoryStore：MySQL 持久化、查询、标签、清理
  history_schema.go      -- 8 张表 DDL
  sampler.go             -- Sampler：运行期定时采集时序数据
  handlers.go            -- 全部 HTTP handler（Agent 上行 + 前端任务/Agent/指标/系统/日志/基线）
  handlers_history.go    -- 历史归档相关 handler
  persist.go             -- JSON 文件持久化（Task 实时态，原子写入）
  errors.go              -- Admin 统一错误类型 + 预定义错误码
  helpers.go             -- 辅助函数（配置默认值填充、日志查询参数解析）

cmd/admin/
  main.go                -- Admin 进程入口（信号处理、日志初始化、graceful shutdown）
```

**实际依赖**：

```bash
go get github.com/go-sql-driver/mysql   # MySQL 驱动
go get go.uber.org/zap                   # 结构化日志
```

**与计划的差异**：
- 计划中的 `binary.go`（BinaryStore）和 `upgrade.go`（UpgradeOrchestrator）**未实现**，二进制存储与自动升级功能已废弃。
- 计划中的 `handlers_history.go` 已按计划实现为独立文件。
- 路由前缀从计划中的 `/api/` 变更为 `/sbot/`。
- 未引入 `github.com/google/uuid` 和 `github.com/jmoiron/sqlx`，改用 `crypto/rand` 生成 ID 和标准 `database/sql`。

---

## 3. 数据模型（完整 DTO）

### 3.1 Task

```go
type Task struct {
    ID          string     `json:"id"`              // 随机 32 字符 hex
    Name        string     `json:"name"`
    State       TaskState  `json:"state"`
    TotalBots   int        `json:"totalBots"`       // 集群总机器人数
    Config      TaskConfig `json:"config"`          // 任务配置
    Assignments []Assignment `json:"assignments,omitempty"`   // 分配快照
    Reports     map[string]TaskCompletionReport `json:"reports,omitempty"` // agentID -> 完成报告
    StageReports []TaskCompletionReport `json:"stageReports,omitempty"` // 渐进式加压阶段报告
    AgentEvents []AgentEvent `json:"agentEvents,omitempty"` // Agent 状态变化事件
    CreatedAt   time.Time  `json:"createdAt"`
    StartedAt   *time.Time `json:"startedAt,omitempty"`
    StoppedAt   *time.Time `json:"stoppedAt,omitempty"`
    ErrorMsg    string     `json:"errorMsg,omitempty"`
}
```

**与计划的差异**：新增 `StageReports`（渐进式加压阶段完成报告）和 `AgentEvents`（任务期间 Agent 状态变化事件）字段。

### 3.2 TaskState

```go
type TaskState string

const (
    TaskPending  TaskState = "pending"   // 已创建未启动
    TaskStarting TaskState = "starting"  // 正在向 Agent 推送
    TaskRunning  TaskState = "running"   // 至少一个 Agent 已开始执行
    TaskStopping TaskState = "stopping"  // 收到停止指令，等待 Agent 上报 done
    TaskStopped  TaskState = "stopped"   // 全部 Agent 已 done
    TaskFailed   TaskState = "failed"    // 失败
)
```

### 3.3 TaskResult

```go
type TaskResult string

const (
    ResultCompleted TaskResult = "completed" // 任务自然完成
    ResultStopped   TaskResult = "stopped"   // 任务被手动停止
    ResultFailed    TaskResult = "failed"    // 任务失败
)
```

### 3.4 TaskConfig

```go
type TaskConfig struct {
    FlowJSON    json.RawMessage   `json:"flowJson"`           // flow.json 原始内容
    ProtoFiles  map[string][]byte `json:"protoFiles,omitempty"` // 文件名 -> 内容
    LuaScripts  map[string][]byte `json:"luaScripts,omitempty"` // 文件名 -> 内容
    Codecs      map[string][]byte `json:"codecs,omitempty"`   // *_codec.json 文件名 -> 内容
    ErrorMap    []byte            `json:"errorMap,omitempty"` // errors.json 内容
    RobotConfig RobotConfig       `json:"robotConfig"`        // 运行时配置
    Deadline    *time.Time        `json:"deadline,omitempty"` // 任务截止时间
}
```

**与计划的差异**：协议适配资源已改为 `Codecs`（多份 `*_codec.json`）+ `ErrorMap`（共享 `errors.json`）。

### 3.5 RobotConfig

```go
type RobotConfig struct {
    Concurrency    int               `json:"concurrency"`               // 并发启动数
    TimeoutSec     int               `json:"timeoutSec"`                // TCP 请求超时秒数
    AccountPrefix  string            `json:"accountPrefix,omitempty"`   // 账号名前缀
    StartNumber    int               `json:"startNumber,omitempty"`     // 起始编号
    MainService    string            `json:"mainService,omitempty"`     // 主服务名
    StateExtra     map[string]string `json:"stateExtra,omitempty"`      // 初始状态额外键值对
    HeartbeatSec   int               `json:"heartbeatSec,omitempty"`    // 心跳间隔秒数
    HTTPTimeoutSec int               `json:"httpTimeoutSec,omitempty"`  // HTTP 请求超时秒数
    ApdexT         int               `json:"apdexT,omitempty"`          // Apdex 阈值（毫秒）
    DebugMode      bool              `json:"debugMode,omitempty"`       // 调试模式
    LogLevel       string            `json:"logLevel,omitempty"`        // 临时日志等级
    RampUp         *RampUpConfig     `json:"rampUp,omitempty"`          // 渐进式加压配置
}
```

### 3.6 RampUpConfig / RampUpStage

```go
type RampUpConfig struct {
    Stages []RampUpStage `json:"stages"`
}

type RampUpStage struct {
    Count       int `json:"count"`                 // 本阶段新增 bot 数
    Concurrency int `json:"concurrency,omitempty"` // 覆盖全局并发数
    HoldSec     int `json:"holdSec,omitempty"`     // 阶段间等待秒数
    Reset       bool `json:"reset,omitempty"`      // 开始前清空已有机器人
}
```

### 3.7 Assignment

```go
type Assignment struct {
    TaskID      string `json:"taskId"`
    AgentID     string `json:"agentId"`
    AgentName   string `json:"agentName"`    // Agent 显示名称
    StartNumber int    `json:"startNumber"`  // 本 Agent 的 bot 起始编号
    TotalBots   int    `json:"totalBots"`    // 本 Agent 分配的 bot 数量
}
```

**与计划的差异**：新增 `AgentName` 字段用于显示。

### 3.8 TaskAssignment（Admin -> Agent 下发）

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
    HeartbeatInterval string            `json:"heartbeatInterval"` // duration 字符串如 "5s"
    TCPTimeout        string            `json:"tcpTimeout"`
    HTTPTimeout       string            `json:"httpTimeout"`
    ApdexT            int               `json:"apdexT"`
    LogLevel          string            `json:"logLevel,omitempty"`
    ConfigURL         string            `json:"configUrl"`         // 配置下载基础 URL
    ConfigFiles       []string          `json:"configFiles"`       // 需要下载的文件列表
    RampUp            *RampUpConfig     `json:"rampUp,omitempty"`  // 已按比例缩放
}
```

### 3.9 AgentNode

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

    // 运行时状态
    Status          AgentStatus   `json:"status"`
    LastHeartbeatAt time.Time     `json:"lastHeartbeatAt"`
    CurrentTaskID   string        `json:"currentTaskId,omitempty"`
    CurrentBots     int           `json:"currentBots"`

    // 最新指标快照（不序列化到前端）
    LatestStress    *monitor.CollectorSnapshot `json:"-"`
    LatestSystem    *SystemSnapshot            `json:"-"`
    StressUpdatedAt time.Time     `json:"stressUpdatedAt,omitempty"`
    SystemUpdatedAt time.Time     `json:"systemUpdatedAt,omitempty"`
}
```

### 3.10 StaticInfo

```go
type StaticInfo struct {
    Hostname   string    `json:"hostname"`
    OS         string    `json:"os"`
    Arch       string    `json:"arch"`
    NumCPU     int       `json:"numCpu"`
    MemTotalMB uint64    `json:"memTotalMB"`
    GoVersion  string    `json:"goVersion"`
    KernelVer  string    `json:"kernelVer"`
    StartedAt  time.Time `json:"startedAt"`
}
```

### 3.11 AgentStatus

```go
type AgentStatus string

const (
    AgentIdle      AgentStatus = "idle"
    AgentBusy      AgentStatus = "busy"
    AgentUnhealthy AgentStatus = "unhealthy" // 心跳超 30s 未到
    AgentOffline   AgentStatus = "offline"   // 心跳超 60s 未到
)
```

### 3.12 上报报文

```go
type StressReport struct {
    AgentID    string                    `json:"agentId"`
    TaskID     string                    `json:"taskId"`
    ReportedAt time.Time                 `json:"reportedAt"`
    Snapshot   *monitor.CollectorSnapshot `json:"snapshot"`
}

type SystemReport struct {
    AgentID    string         `json:"agentId"`
    ReportedAt time.Time      `json:"reportedAt"`
    Snapshot   SystemSnapshot `json:"snapshot"`
}

type TaskCompletionReport struct {
    AgentID       string                    `json:"agentId"`
    TaskID        string                    `json:"taskId"`
    Result        TaskResult                `json:"result"`
    ErrorMsg      string                    `json:"errorMsg,omitempty"`
    FinishedAt    time.Time                 `json:"finishedAt"`
    FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
    StageIndex    int                       `json:"stageIndex,omitempty"` // -1 或零值=最终报告，>=0=阶段报告
}
```

### 3.13 SystemSnapshot

```go
type SystemSnapshot struct {
    Timestamp     time.Time `json:"timestamp"`
    CPUPercent    float64   `json:"cpuPercent"`
    CPUPerCore    []float64 `json:"cpuPerCore,omitempty"`
    LoadAvg1      float64   `json:"loadAvg1,omitempty"`
    LoadAvg5      float64   `json:"loadAvg5,omitempty"`
    LoadAvg15     float64   `json:"loadAvg15,omitempty"`
    MemTotalMB    uint64    `json:"memTotalMB"`
    MemUsedMB     uint64    `json:"memUsedMB"`
    MemPercent    float64   `json:"memPercent"`
    SwapUsedMB    uint64    `json:"swapUsedMB,omitempty"`
    ProcessRssMB  uint64    `json:"processRssMB"`
    ProcessHeapMB uint64    `json:"processHeapMB"`
    ProcessSysMB  uint64    `json:"processSysMB"`
    NumGoroutine  int       `json:"numGoroutine"`
    NumThread     int32     `json:"numThread"`
    NumFD         int32     `json:"numFd,omitempty"`
    GCCount       uint32    `json:"gcCount"`
    GCPauseAvgMs  float64   `json:"gcPauseAvgMs,omitempty"`
    NetSendKBps   float64   `json:"netSendKBps"`
    NetRecvKBps   float64   `json:"netRecvKBps"`
}
```

### 3.14 注册/心跳请求

```go
type RegisterRequest struct {
    AgentID        string    `json:"agentId"`
    Name           string    `json:"name"`
    Address        string    `json:"address"`
    AppVersion     string    `json:"appVersion"`
    MaxBots        int       `json:"maxBots"`
    StressInterval string    `json:"stressInterval"`
    SystemInterval string    `json:"systemInterval"`
    StaticInfo     StaticInfo `json:"staticInfo"`
}

type RegisterResponse struct {
    AgentID        string `json:"agentId"`
    HeartbeatTTL   string `json:"heartbeatTtl"`
    StressEndpoint string `json:"stressEndpoint"`
    SystemEndpoint string `json:"systemEndpoint"`
}

type HeartbeatRequest struct {
    AgentID       string `json:"agentId"`
    Timestamp     string `json:"timestamp"`
    Status        string `json:"status"`
    CurrentTaskID string `json:"currentTaskId,omitempty"`
    CurrentBots   int    `json:"currentBots"`
    AppVersion    string `json:"appVersion"`
}
```

### 3.15 AgentEvent

```go
type AgentEvent struct {
    AgentID   string    `json:"agentId"`
    AgentName string    `json:"agentName"`
    Type      string    `json:"type"`      // "offline" | "reconnected" | "restarted"
    Timestamp time.Time `json:"timestamp"`
    Detail    string    `json:"detail,omitempty"`
}
```

### 3.16 聚合快照

```go
type StressAggregate struct {
    Snapshot        *monitor.CollectorSnapshot `json:"snapshot"`
    ReportingAgents int                        `json:"reportingAgents"`
    TotalAgents     int                        `json:"totalAgents"`
    OfflineAgents   int                        `json:"offlineAgents"`
    AssignedAgents  int                        `json:"assignedAgents"`
}

type ClusterSystemSnapshot struct {
    Timestamp       time.Time          `json:"timestamp"`
    AgentCount      int                `json:"agentCount"`
    OnlineCount     int                `json:"onlineCount"`
    OfflineCount    int                `json:"offlineCount"`
    AvgCPUPercent   float64            `json:"avgCpuPercent"`
    MaxCPUPercent   float64            `json:"maxCpuPercent"`
    HotAgentID      string             `json:"hotAgentId,omitempty"`
    HotAgentName    string             `json:"hotAgentName,omitempty"`
    TotalMemMB      uint64             `json:"totalMemMB"`
    UsedMemMB       uint64             `json:"usedMemMB"`
    TotalNetSendKBps float64           `json:"totalNetSendKBps"`
    TotalNetRecvKBps float64           `json:"totalNetRecvKBps"`
    TotalGoroutines int                `json:"totalGoroutines"`
    TotalThreads    int32              `json:"totalThreads"`
    TotalFDs        int32              `json:"totalFds"`
    Agents          []AgentSystemBrief `json:"agents"`
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
    LastSeen     int64   `json:"lastSeen"`
}
```

### 3.17 历史归档类型

```go
type HistoryRecord struct {
    ID            string         `json:"id"`
    Name          string         `json:"name"`
    State         TaskState      `json:"state"`
    TotalBots     int            `json:"totalBots"`
    AgentCount    int            `json:"agentCount"`
    CreatedAt     time.Time      `json:"createdAt"`
    StartedAt     *time.Time     `json:"startedAt,omitempty"`
    StoppedAt     *time.Time     `json:"stoppedAt,omitempty"`
    DurationSec   int            `json:"durationSec"`
    ErrorMsg      string         `json:"errorMsg,omitempty"`
    Starred       bool           `json:"starred"`
    Tags          []string       `json:"tags"`
    Note          string         `json:"note"`
    ConfigSummary ConfigSummary  `json:"configSummary"`
}

type ConfigSummary struct {
    Concurrency int `json:"concurrency"`
    TimeoutSec  int `json:"timeoutSec"`
    FlowSizeKB  int `json:"flowSizeKB"`
    ProtoCount  int `json:"protoCount"`
    ScriptCount int `json:"scriptCount"`
}

type HistoryDetail struct {
    HistoryRecord
    Assignments  []Assignment              `json:"assignments"`
    AgentReports []HistoryAgentReport      `json:"agentReports"`
    AgentEvents  []AgentEvent              `json:"agentEvents,omitempty"`
    FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
    FinalSystem  ClusterSystemSnapshot     `json:"finalSystem"`
}

type HistoryAgentReport struct {
    AgentID       string                    `json:"agentId"`
    AgentName     string                    `json:"agentName"`
    Result        TaskResult                `json:"result"`
    ErrorMsg      string                    `json:"errorMsg,omitempty"`
    FinishedAt    time.Time                 `json:"finishedAt"`
    FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
}

type HistoryFilter struct {
    State          string
    StartedAfter   time.Time
    StartedBefore  time.Time
    Tags           []string     // 任一匹配
    TagsAll        []string     // 全部匹配
    Starred        *bool
    Search         string       // 模糊匹配 name + ID
    Limit          int          // 默认 20，上限 100
    Offset         int
    OrderBy        string       // 白名单防注入
}

type HistoryListResponse struct {
    Total int             `json:"total"`
    Items []HistoryRecord `json:"items"`
}

type UpdateHistoryRequest struct {
    Starred *bool    `json:"starred,omitempty"`
    Tags    []string `json:"tags,omitempty"`
    Note    *string  `json:"note,omitempty"`
}

type CompareResponse struct {
    Tasks []HistoryDetail `json:"tasks"`
    Diff  CompareDiff     `json:"diff"`
}

type CompareDiff struct {
    Actions map[string][]float64 `json:"actions"` // actionName -> [taskA_p99, taskB_p99, ...]
}
```

### 3.18 时序采样

```go
type TimeseriesPoint struct {
    TaskID     string          `json:"taskId"`
    SampledAt  time.Time       `json:"sampledAt"`
    ElapsedSec int             `json:"elapsedSec"`
    DataType   string          `json:"dataType"` // "stress" | "system"
    Snapshot   json.RawMessage `json:"snapshot"`
}

type TimeseriesResponse struct {
    TaskID string            `json:"taskId"`
    Stress []TimeseriesPoint `json:"stress"`
    System []TimeseriesPoint `json:"system"`
}
```

---

## 4. 核心组件设计

### 4.1 AdminServer

```go
type AdminServer struct {
    cfg Config

    tasks      *TaskStore
    agents     *AgentRegistry
    aggregator *MetricsAggregator
    dispatcher *AgentDispatcher
    assigner   *Assigner

    logsProxyClient *http.Client  // Agent 日志代理（5s 超时）

    history *HistoryStore  // 可选，cfg.History.Enabled=false 时为 nil
    sampler *Sampler       // 同上

    httpSrv *http.Server
    stopCh  chan struct{}
    wg      sync.WaitGroup
}
```

**与计划的差异**：移除了 `binaries`（BinaryStore）和 `upgrader`（UpgradeOrchestrator），这两个组件已废弃。

**初始化顺序**（`NewAdminServer`）：

```
 1. TaskStore：从 data/tasks/ 恢复任务，活跃任务重置为 failed
 2. AgentRegistry：创建空注册表，注册 onChange 回调
 3. MetricsAggregator：注入 AgentRegistry 引用
 4. AgentDispatcher：创建 HTTP client（30s 超时）
 5. Logs proxy client：5s 超时 HTTP client
 6. Assigner：创建分配器
 7. HistoryStore（可选）：连接 MySQL -> Schema 迁移 -> 创建 Sampler
 8. 终态回调：SetOnTerminal 设置归档触发器
```

**启动顺序**（`Run`）：

```
 1. 初始化协程池（utils.InitWorkPool）
 2. 启动 AgentRegistry 心跳超时检测协程
 3. 启动 HistoryStore 定时清理协程（可选）
 4. 启动 deadline 看门狗协程
 5. 注册路由 + http.Server.ListenAndServe
 6. 信号处理（SIGINT/SIGTERM -> Shutdown）
```

**关闭顺序**（`Shutdown`）：

```
 1. http.Server.SetKeepAlivesEnabled(false) -> Shutdown(10s timeout)
 2. HistoryStore.Close()（先 cancel context 使 prune 协程中断，再 db.Close）
 3. WorkPool.Shutdown()
 4. close(stopCh)
```

### 4.2 TaskStore（含单例约束）

```go
type TaskStore struct {
    mu    sync.RWMutex
    tasks map[string]*Task   // taskID -> Task

    // 单例约束
    startMu  sync.Mutex     // 串行化 start 流程，防止 TOCTOU
    activeID string         // 当前 active 任务 ID（"" 表示无）

    dataDir string

    // 终态回调（用于触发归档）
    onTerminal func(task *Task)

    // Admin 重启时恢复的活跃任务 ID
    recoveredIDs []string
}
```

**关键方法**：

| 方法 | 说明 |
|---|---|
| `Create(t *Task) error` | 创建任务，自动持久化 |
| `Get(id string) (*Task, bool)` | 获取任务浅拷贝 |
| `List() []*Task` | 列出所有任务浅拷贝 |
| `ListByState(state TaskState) []*Task` | 按状态过滤 |
| `Update(id string, fn func(*Task)) error` | 更新任务（自动持久化） |
| `Delete(id string) error` | 删除任务（仅终态可删） |
| `Transition(id string, from, to TaskState) (*Task, error)` | 状态转换（校验合法性 + 持久化 + 终态回调） |
| `StartTask(id string) (*Task, error)` | 启动入口（单例约束） |
| `ActiveTask() *Task` | 获取当前活跃任务浅拷贝 |
| `ActiveTaskID() string` | 获取当前活跃任务 ID |
| `HasActive() bool` | 是否有活跃任务 |
| `SetOnTerminal(fn func(task *Task))` | 设置终态回调 + 触发恢复任务归档 |

**单例约束实现要点**：

`StartTask` 通过 `startMu`（Mutex）串行化并发启动请求。流程：
1. 获取 `startMu` 锁
2. 获取 `mu`（RLock）检查 `activeID`
3. 如果 `activeID != ""`，返回 `TASK_CONFLICT` 错误（含活跃任务信息）
4. 调用 `Transition(id, TaskPending, TaskStarting)`
5. 获取 `mu`（Lock）设置 `activeID = id`

**activeID 维护**：

| 触发 | 动作 |
|---|---|
| `StartTask` 成功 | `activeID = id` |
| `Transition(running -> stopping)` | 不变（仍 active） |
| `Transition(* -> stopped/failed)` | `activeID = ""` + 触发 `onTerminal` 回调 |
| Admin 重启时 active 任务恢复为 failed | `activeID = ""` + 通过 `recoveredIDs` 延迟触发归档 |

**状态转换矩阵**：

| 当前 | 允许转到 | 触发场景 | 占据单例位 |
|---|---|---|---|
| `pending` | `starting`、`failed`、`stopped` | `POST /tasks/{id}/start` / 内部错误 / 取消 | 否 |
| `starting` | `running`、`failed` | 全部 Agent 接受 / 任意 Agent 失败 | 是 |
| `running` | `stopping`、`stopped`、`failed` | `POST /tasks/{id}/stop` / deadline / 全节点失效 | 是 |
| `stopping` | `stopped`、`failed` | 全部 Agent 上报 done / 超时强制完成 | 是 |
| `stopped`、`failed` | （终态） | -- | 否 |

**合法转换校验**（`validTransition`）：

```go
func validTransition(from, to TaskState) bool {
    switch from {
    case TaskPending:
        return to == TaskStarting || to == TaskFailed || to == TaskStopped
    case TaskStarting:
        return to == TaskRunning || to == TaskFailed
    case TaskRunning:
        return to == TaskStopping || to == TaskStopped || to == TaskFailed
    case TaskStopping:
        return to == TaskStopped || to == TaskFailed
    default:
        return false
    }
}
```

**持久化策略**：

- 每次状态变更后写 `data/tasks/{id}.json`（原子写入：先写 `.tmp` 再 rename）
- 启动时扫描所有 `*.json`：终态直接载入；active 状态（starting/running/stopping）重置为 `failed`，`errorMsg="admin restart, task lost"`
- 清理旧版残留的 `.tmp.json` 文件

### 4.3 AgentRegistry

```go
type AgentRegistry struct {
    mu       sync.RWMutex
    agents   map[string]*AgentNode
    cfg      RegistryConfig
    onChange func(agentID string, from, to AgentStatus)

    unhealthyThreshold time.Duration
    offlineThreshold   time.Duration
}
```

**与计划的差异**：
- `onChange` 回调在锁外触发（使用 `statusChange` 结构体收集变更，解锁后批量触发），避免 `agents.mu -> tasks.mu` 的 AB-BA 死锁。
- offline 的 Agent 会被**直接删除**（而非仅标记），任务侧通过 `onChange` 回调处理。
- 同 ID 重新注册时直接覆盖旧 entry（不拒绝），旧节点的关联任务由 `onAgentStatusChange` 处理。

**心跳超时分级**：

```
heartbeatLag = now - lastHeartbeatAt

heartbeatLag <  30s   -> idle / busy（保持业务状态）
30s <= lag    <  60s   -> unhealthy
60s <= lag             -> offline + 删除
```

阈值通过 `RegistryConfig` 配置，由 `utils.ParseDurationDefault` 解析。

**关键方法**：

| 方法 | 说明 |
|---|---|
| `Register(node *AgentNode) error` | 注册/重新注册 Agent（同 ID 覆盖） |
| `Heartbeat(agentID string, req HeartbeatRequest) error` | 处理心跳，更新 CurrentTaskID/CurrentBots |
| `Touch(agentID, appVersion string)` | 刷新心跳时间（任何 Agent 请求都视为 keepalive） |
| `Deregister(agentID string) error` | 注销 Agent |
| `Get(agentID string) (*AgentNode, bool)` | 获取 Agent（返回内部引用，仅只读使用） |
| `List() []*AgentNode` | 列出所有 Agent |
| `ListByStatus(status AgentStatus) []*AgentNode` | 按状态过滤 |
| `UpdateStress(agentID string, snap *monitor.CollectorSnapshot, at time.Time)` | 更新压测快照 |
| `UpdateSystem(agentID string, snap *SystemSnapshot, at time.Time)` | 更新系统快照 |
| `StartHealthChecker(ctx context.Context)` | 启动心跳超时检测协程（5s 轮询） |

**健康检测流程**（`scanAndMarkStatus`）：

每 5 秒遍历所有 Agent，检查 `now - LastHeartbeatAt`：
- 超过 `offlineThreshold` -> 标记 `offline`，**直接从 map 中删除**
- 超过 `unhealthyThreshold` -> 标记 `unhealthy`
- 收集所有状态变更，解锁后通过 `fireOnChange` 批量触发回调

**状态恢复**（`touchLocked`）：

unhealthy/offline 的 Agent 在收到任何请求（心跳/上报/done）时自动恢复：
- `CurrentTaskID != ""` -> 恢复为 `busy`
- `CurrentTaskID == ""` -> 恢复为 `idle`

### 4.4 Assigner

```go
type Assigner struct{}
```

**分配算法**：

1. 过滤 `idle` 且 `MaxBots > 0` 的 Agent
2. 总容量校验：`sum(agent.MaxBots) >= task.TotalBots`
3. **Debug 模式**：优先分配到单个容量足够的 Agent（选 MaxBots 最小的），无单节点容量够时降级为比例分配
4. **比例分配**：按 `MaxBots` 比例分配 `TotalBots`，余数按最大小数部分补齐，保证 `sum(bots) == totalBots`

```go
func (a *Assigner) Assign(task *Task, agents []*AgentNode, startNumber int) ([]Assignment, error)
func (a *Assigner) proportionalAssign(task *Task, agents []*AgentNode, startNumber int) []Assignment
```

**与计划的差异**：计划中的"均匀分配"被替换为"按 MaxBots 比例分配"，新增 Debug 模式单节点优先分配。

### 4.5 MetricsAggregator

```go
type MetricsAggregator struct {
    registry *AgentRegistry
}
```

**聚合规则**：

| 类别 | 字段 | 聚合函数 |
|---|---|---|
| 压测 | 所有匹配 `currentTaskID == taskID` 的 Agent 的 LatestStress | `monitor.MergeSnapshots`（重建直方图） |
| 系统 | 所有 `status != offline` 的 Agent 的 LatestSystem | CPU 按核数加权平均、内存求和、网络求和、取最大 CPU Agent |

**关键禁忌**：绝不简单平均 P99（数学错误）。必须通过 `monitor.MergeSnapshots` 重建合并直方图后插值计算百分位数。

### 4.6 AgentDispatcher

```go
type AgentDispatcher struct {
    httpClient *http.Client  // Timeout 30s
}
```

**方法**：

| 方法 | Agent 端点 | 重试次数 |
|---|---|---|
| `AssignTask(addr string, assignment TaskAssignment)` | `POST /agent/v1/task` | 2 |
| `Stop(addr, taskID string)` | `POST /agent/v1/stop` | 2 |
| `Shutdown(addr string)` | `POST /agent/v1/shutdown` | 1 |
| `Version(addr string) (string, error)` | `GET /agent/v1/version` | 0 |

**重试策略**：指数退避，初始 1s，上限 10s。

**与计划的差异**：移除了 `Upgrade` 和 `Restart` 方法（自动升级已废弃）。

### 4.7 HistoryStore（MySQL 历史归档）

```go
type HistoryStore struct {
    cfg    HistoryConfig
    db     *sql.DB
    prune  time.Duration
    cancel context.CancelFunc
}
```

**关键方法**：

| 方法 | 说明 |
|---|---|
| `Archive(ctx, task, finalStress, finalSys)` | 终态归档（事务写入 6 张表） |
| `List(ctx, filter)` | 分页查询 + 多维过滤 |
| `Get(ctx, id)` | 查询详情（含分配/报告/聚合/事件） |
| `GetConfig(ctx, id)` | 查询配置归档 |
| `GetTimeseries(ctx, id)` | 查询时序采样数据 |
| `AllTags(ctx)` | 全部去重标签（JSON_TABLE 或降级扫描） |
| `UpsertMeta(ctx, id, stageIndex, req)` | 更新 starred/tags/note（stageIndex<=0=任务级，>=1=段落级，统一写 task_meta） |
| `Delete(ctx, id, force)` | 删除（starred 需要 force=true） |
| `AppendTimeseries(ctx, taskID, point)` | 追加时序采样点 |
| `PruneExpired(ctx, now)` | 清理过期记录（事务级联删除） |
| `StartPruneLoop(ctx)` | 启动定时清理协程（24h 间隔） |
| `Close()` | cancel context + db.Close() |

**归档事务**（6 张表，按顺序写入）：

1. `task_history`（ON DUPLICATE KEY UPDATE）
2. `task_assignment`（批量 INSERT）
3. `task_report`（批量 INSERT；含 reset 段落报告，按连续段号写入；末段额外写最终报告）
4. `task_aggregated`（ON DUPLICATE KEY UPDATE；`-1` 整体 + 各 reset 段落 `MergeSnapshots` 聚合）
5. `task_config_archive`（ON DUPLICATE KEY UPDATE）
6. `task_agent_events`（批量 INSERT）

**阶段段落归档**（`admin/stage_plan.go` + `history.go`）：含 `reset=true` 的渐进式加压任务，
`buildStagePlan` 把 reset 边界映射为连续 1-based 段落号；`task_report` / `task_aggregated` / `task_timeseries`
均按段号写入，三者对齐，支持按 `stageIndex` 查询单段详情与时序。reset 任务的 `stage_index=-1` 等于末段
（Agent 每段 `Reset()` 采集器）。详见 `docs/ramp-up.md` §6.6。新增索引 `task_report.idx_task_stage`、
`task_timeseries.idx_task_stage_elapsed`；旧库升级见 `deploy/upgrade.sql` 的 `sb_upgrade_stage_history()`（幂等）。

**与计划的差异**：
- 使用标准 `database/sql` 而非 `sqlx`
- 事务内先插入再提交，无显式幂等检查（依赖 ON DUPLICATE KEY UPDATE）
- 启动时自动移除旧版本遗留的物理外键约束（`dropLegacyForeignKeys`）

### 4.8 Sampler（运行期时序采集）

```go
type Sampler struct {
    interval   time.Duration        // 默认 10s
    aggregator *MetricsAggregator
    history    *HistoryStore
    registry   *AgentRegistry
    tasks      *TaskStore           // 读取活跃任务的 reset 进度，为采样点打段号

    mu      sync.Mutex
    current *samplerJob
}

type samplerJob struct {
    taskID    string
    startedAt time.Time
    cancel    context.CancelFunc
}
```

每 `interval` 秒执行：
1. `AggregateStress(taskID)` -> JSON -> `AppendTimeseries`
2. `AggregateSystem()` -> JSON -> `AppendTimeseries`
3. `currentStageIndex(taskID)`：reset 任务按「已观测 reset 上报数 + 1」给采样点打段号（非 reset/非 ramp 为 -1）

写入失败仅 log warn，不影响下次采样。

### 4.9 Agent 状态变更处理

AdminServer 注册了 `onAgentStatusChange` 回调，当 Agent 状态变更时触发：

| 场景 | 处理 |
|---|---|
| busy/unhealthy -> idle（重新注册） | 合成 `failed` report，记录 `restarted` 事件，检查是否所有节点失效 |
| offline -> idle/busy（恢复） | 记录 `reconnected` 事件 |
| * -> offline（离线） | 记录 `offline` 事件；stopping 状态下合成 report 并可能转 stopped；running 状态下检查是否所有节点失效 |

**autoStopTask**（自动停止任务）：
- 触发条件：所有分配节点都已失效（offline 或已合成 report）
- TaskStarting 阶段：直接转 TaskFailed
- TaskRunning 阶段：转 TaskStopping -> 发送 stop -> 合成 report -> 转 TaskFailed

**stopTimeout**（停止超时安全网）：
- 停止任务后启动 30s 定时器
- 30s 后任务仍在 stopping，为未上报节点合成 report 并转 stopped

---

## 5. HTTP API 完整路由表

### 5.1 Agent 上行 API（7 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `POST` | `/sbot/agent/register` | `handleAgentRegister` | Agent 注册 |
| `POST` | `/sbot/agent/{id}/heartbeat` | `handleAgentHeartbeat` | 心跳 |
| `POST` | `/sbot/agent/{id}/deregister` | `handleAgentDeregister` | 主动注销 |
| `POST` | `/sbot/agent/stress` | `handleAgentStressReport` | 压测指标上报 |
| `POST` | `/sbot/agent/system` | `handleAgentSystemReport` | 系统指标上报 |
| `POST` | `/sbot/agent/{id}/task/{tid}/done` | `handleAgentTaskDone` | 任务完成上报 |
| `GET` | `/sbot/agent/{id}/pending-task` | `handleAgentPendingTask` | 拉取待执行任务 |

### 5.2 前端-资源基线（1 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `POST` | `/sbot/resources/baseline` | `handleUpdateBaseline` | 前端推送 IDB 资源到磁盘基线 |

### 5.3 前端-任务（7 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `POST` | `/sbot/tasks` | `handleCreateTask` | 创建任务（multipart） |
| `GET` | `/sbot/tasks` | `handleListTasks` | 列出任务（支持 state 过滤 + 分页） |
| `GET` | `/sbot/tasks/{id}` | `handleGetTask` | 任务详情 |
| `GET` | `/sbot/tasks/{id}/config/{path...}` | `handleGetTaskConfig` | 下载任务配置文件（Agent 也用此 URL） |
| `POST` | `/sbot/tasks/{id}/start` | `handleStartTask` | 启动任务（分配 + 推送） |
| `POST` | `/sbot/tasks/{id}/stop` | `handleStopTask` | 停止任务 |
| `DELETE` | `/sbot/tasks/{id}` | `handleDeleteTask` | 删除任务（仅终态） |

### 5.4 前端-Agent（5 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `GET` | `/sbot/agents` | `handleListAgents` | 列出所有 Agent（含简要指标） |
| `GET` | `/sbot/agents/{id}` | `handleGetAgent` | Agent 详情 |
| `DELETE` | `/sbot/agents/{id}` | `handleDeleteAgent` | 删除 Agent（仅 offline 可删） |
| `POST` | `/sbot/agents/{id}/shutdown` | `handleShutdownAgent` | 关机命令 |
| `POST` | `/sbot/agents/shutdown-all` | `handleShutdownAllAgents` | 全部关机 |

### 5.5 前端-指标（7 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `GET` | `/sbot/metrics` | `handleGetMetrics` | 当前任务的集群压测聚合 |
| `GET` | `/sbot/metrics/summary` | `handleGetMetricsSummary` | 文本摘要 |
| `GET` | `/sbot/metrics/agents` | `handleGetAgentMetrics` | 各 Agent 最新压测快照 |
| `GET` | `/sbot/metrics/agents/{id}` | `handleGetSingleAgentMetrics` | 指定 Agent 压测快照 |
| `GET` | `/sbot/system` | `handleGetSystem` | 集群系统聚合 |
| `GET` | `/sbot/system/agents` | `handleGetSystemAgents` | 各 Agent 系统快照 |
| `GET` | `/sbot/system/agents/{id}` | `handleGetSystemAgent` | 指定 Agent 系统快照 |

### 5.6 历史归档（10 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `GET` | `/sbot/history` | `handleListHistory` | 历史任务列表（分页 + 过滤） |
| `GET` | `/sbot/history/tags` | `handleGetHistoryTags` | 全部去重标签 |
| `GET` | `/sbot/history/{id}` | `handleGetHistory` | 历史任务详情 |
| `PUT` | `/sbot/history/{id}` | `handleUpdateHistory` | 更新 starred/tags/note |
| `DELETE` | `/sbot/history/{id}` | `handleDeleteHistory` | 删除（starred 需要 force=true） |
| `GET` | `/sbot/history/{id}/agents` | `handleGetHistoryAgents` | 各 Agent 完成报告 |
| `GET` | `/sbot/history/{id}/config` | `handleGetHistoryConfig` | 任务配置归档 |
| `GET` | `/sbot/history/{id}/timeseries` | `handleGetHistoryTimeseries` | 时序采样数据 |
| `POST` | `/sbot/history/{id}/clone` | `handleCloneHistory` | 用历史配置创建新任务（pending） |
| `GET` | `/sbot/history/compare` | `handleCompareHistory` | 多任务对比（2~5 个） |

### 5.7 日志（6 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `GET` | `/sbot/logs/admin` | `handleGetAdminLogs` | Admin 本地 Ring buffer 日志 |
| `GET` | `/sbot/logs/agents/{id}` | `handleGetAgentLogs` | 代理转发 Agent 日志 |
| `GET` | `/sbot/logs/admin/files` | `handleListAdminLogFiles` | 列出 Admin 日志文件 |
| `GET` | `/sbot/logs/admin/files/{name}` | `handleDownloadAdminLogFile` | 下载 Admin 日志文件 |
| `GET` | `/sbot/logs/agents/{id}/files` | `handleListAgentLogFiles` | 代理列出 Agent 日志文件 |
| `GET` | `/sbot/logs/agents/{id}/files/{name}` | `handleDownloadAgentLogFile` | 代理下载 Agent 日志文件 |

### 5.8 基线资源读取（8 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `GET` | `/sbot/baseline/proto/index.json` | `handleBaselineProtoIndex` | 列出 proto 文件 |
| `GET` | `/sbot/baseline/proto/{name}` | `handleBaselineProtoFile` | 下载 proto 文件 |
| `GET` | `/sbot/baseline/scripts/index.json` | `handleBaselineScriptIndex` | 列出 Lua 脚本 |
| `GET` | `/sbot/baseline/scripts/{name}` | `handleBaselineScriptFile` | 下载 Lua 脚本 |
| `GET` | `/sbot/baseline/adapter/index.json` | `handleBaselineCodecIndex` | 列出 adapter 基线文件 |
| `GET` | `/sbot/baseline/adapter/{name}` | `handleBaselineCodecFile` | 下载指定 codec/errors 文件 |
| `GET` | `/sbot/baseline/flow/flow.json` | `handleBaselineFlow` | 下载 flow.json |
| `GET` | `/sbot/baseline/config.json` | `handleBaselineConfig` | 下载 config.json |

### 5.9 错误码（1 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `GET` | `/sbot/api/error-codes` | `handleErrorCodeIndex` | 列出所有错误码 |

### 5.10 静态资源（1 个端点）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `*` | `/` | `http.FileServer` | 前端静态资源托管 |

**总计：53 个端点**

---

## 6. 完整 MySQL DDL（8 张表）

```sql
-- ===== 1. 任务历史主表 =====
CREATE TABLE IF NOT EXISTS task_history (
    id              VARCHAR(32)  NOT NULL PRIMARY KEY,
    name            VARCHAR(255) NOT NULL DEFAULT '',
    state           VARCHAR(32)  NOT NULL DEFAULT '',
    total_bots      INT          NOT NULL DEFAULT 0,
    agent_count     INT          NOT NULL DEFAULT 0,
    created_at      DATETIME(3)  NOT NULL,
    started_at      DATETIME(3)  NULL,
    stopped_at      DATETIME(3)  NULL,
    duration_sec    INT          NOT NULL DEFAULT 0,
    error_msg       TEXT,
    debug_mode      TINYINT(1)   NOT NULL DEFAULT 0,
    config_summary  JSON         NULL,
    stage_count     INT          NOT NULL DEFAULT 0,
    INDEX idx_state (state),
    INDEX idx_created (created_at DESC),
    INDEX idx_started (started_at),
    INDEX idx_stopped (stopped_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- 注：starred/tags/note 已迁出到 task_meta（见第 8 表），task_history 不再保存元数据。

-- ===== 2. 集群分配快照 =====
CREATE TABLE IF NOT EXISTS task_assignment (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    start_number    INT          NOT NULL DEFAULT 0,
    total_bots      INT          NOT NULL DEFAULT 0,
    INDEX idx_task (task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 3. Agent 完成报告 =====
CREATE TABLE IF NOT EXISTS task_report (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    agent_name      VARCHAR(255) NOT NULL DEFAULT '',
    result          VARCHAR(32)  NOT NULL DEFAULT '',
    error_msg       TEXT,
    finished_at     DATETIME(3)  NULL,
    final_snapshot  JSON         NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    INDEX idx_task (task_id),
    INDEX idx_task_stage (task_id, stage_index)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 4. 集群聚合终态快照 =====
CREATE TABLE IF NOT EXISTS task_aggregated (
    task_id         VARCHAR(32)  NOT NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    final_stress    JSON         NULL,
    final_system    JSON         NULL,
    PRIMARY KEY (task_id, stage_index)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 5. 时序采样数据 =====
CREATE TABLE IF NOT EXISTS task_timeseries (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    sampled_at      DATETIME(3)  NOT NULL,
    elapsed_sec     INT          NOT NULL DEFAULT 0,
    data_type       VARCHAR(32)  NOT NULL,
    snapshot        JSON         NOT NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    INDEX idx_task_type (task_id, data_type, elapsed_sec),
    INDEX idx_task_stage_elapsed (task_id, stage_index, elapsed_sec)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 6. 任务配置归档 =====
CREATE TABLE IF NOT EXISTS task_config_archive (
    task_id         VARCHAR(32)  NOT NULL PRIMARY KEY,
    flow_json       MEDIUMBLOB   NULL,
    proto_files     MEDIUMBLOB   NULL,
    lua_scripts     MEDIUMBLOB   NULL,
    robot_config    JSON         NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 7. Agent 事件记录 =====
CREATE TABLE IF NOT EXISTS task_agent_events (
    id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(32)  NOT NULL,
    agent_id        VARCHAR(64)  NOT NULL,
    agent_name      VARCHAR(255) NOT NULL DEFAULT '',
    event_type      VARCHAR(32)  NOT NULL,
    timestamp       DATETIME(3)  NOT NULL,
    detail          TEXT,
    INDEX idx_task (task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 8. 统一元数据（收藏/标签/备注）=====
-- 与 task_report/aggregated/timeseries 同构：(task_id, stage_index) 键，stage_index=-1 为任务级
-- （所有任务），>=1 为 reset 渐进式加压的各阶段段落。行按需懒创建。
CREATE TABLE IF NOT EXISTS task_meta (
    task_id         VARCHAR(32)  NOT NULL,
    stage_index     INT          NOT NULL DEFAULT -1,
    starred         TINYINT(1)   NOT NULL DEFAULT 0,
    tags            JSON         NULL,
    note            TEXT,
    updated_at      DATETIME(3)  NOT NULL,
    PRIMARY KEY (task_id, stage_index),
    INDEX idx_starred (starred)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

**与计划的差异**：
- 主键 `id` 长度从 `VARCHAR(36)` 改为 `VARCHAR(32)`（使用 16 字节随机 hex 而非 UUID）
- 所有子表**不使用物理外键**（符合项目约定"数据库只用逻辑外键"），启动时自动清理旧版本遗留的外键约束
- `task_history` 新增 `stage_count` 字段（渐进式加压阶段数）
- `task_report` 新增 `agent_name`、`stage_index` 字段
- `task_aggregated` 使用复合主键 `(task_id, stage_index)`
- `task_timeseries` 新增 `stage_index` 字段
- `task_config_archive` 使用 `MEDIUMBLOB` 而非 `MEDIUMTEXT`（存储二进制数据）
- 新增第 7 张表 `task_agent_events`（计划中无此表）
- 新增第 8 张表 `task_meta`（统一收藏/标签/备注，主键 `(task_id, stage_index)`，`-1`=任务级、`>=1`=reset 段落）；`task_history` 的 `starred/tags/note` 三列已迁入此表

---

## 7. 任务状态机时序

```
[创建] --create--> pending
pending --start--> starting --任意 agent 失败--> failed
                   '--所有 agent 接受--> running --stop/deadline--> stopping
                                                     '--全部 agent done--> stopped
                                                     '--全节点失效--> failed
starting --全节点失效--> failed
stopping --全部 agent done--> stopped
stopping --超时 30s--> stopped（强制合成 report）
```

**完成判定**：
- `TaskCompletionReport` 到达 -> 记入 `task.Reports[agentID]`
- `len(task.Reports) == len(task.Assignments)` -> 所有 agent 已 done -> 转到 stopped
- 阶段完成报告（`StageIndex > 0`）存入 `StageReports`，不触发状态转换
- 停止超时安全网：30s 后未完成的节点自动合成 `stopped` report

**自然完成路径**：
1. Agent 完成任务 -> `POST /sbot/agent/{id}/task/{tid}/done`（`result=completed`）
2. Admin 收到 report -> 存入 `task.Reports`
3. 最后一个 report 到达 -> `running -> stopped`

**手动停止路径**：
1. 前端 -> `POST /sbot/tasks/{id}/stop` -> `running -> stopping`
2. 向在线节点发送 stop RPC
3. 为已离线节点合成 `stopped` report
4. Agent 完成后上报 done（`result=stopped`）
5. 最后一个 report 到达 -> `stopping -> stopped`
6. 安全网：30s 后强制合成

**Deadline 超时路径**：
1. DeadlineWatchdog 每 5s 检查
2. `task.Config.Deadline != nil && now.After(*Deadline)` -> `autoStopTask`
3. 调用 `running -> stopping -> ...` 链路

---

## 8. 锁顺序说明

Admin 中存在多个互斥锁，必须严格遵守获取顺序以避免死锁：

| 锁 | 保护对象 | 持有者 |
|---|---|---|
| `tasks.startMu` | 串行化任务启动 | `TaskStore.StartTask` |
| `tasks.mu` (RWMutex) | tasks map + activeID | 所有 TaskStore 方法 |
| `agents.mu` (RWMutex) | agents map | 所有 AgentRegistry 方法 |
| `sampler.mu` | samplerJob | `Sampler.Start/Stop` |
| `history.db` | MySQL 连接 | 所有 HistoryStore 方法 |

**锁获取顺序**（从外到内）：

```
1. tasks.startMu -> tasks.mu（StartTask 内部）
2. agents.mu（任何 Agent 操作）
3. tasks.mu（任何 Task 操作）
4. 绝不：tasks.mu -> agents.mu（AB-BA 死锁）
```

**关键防死锁设计**：
- `AgentRegistry.fireOnChange`：在锁**外**触发回调，收集 `statusChange` 列表后解锁再调用 `onChange`
- `handleAgentTaskDone`：先调用 `agents.Touch/Heartbeat`（操作 agents.mu），再调用 `tasks.Update`（操作 tasks.mu），最后在 Update 外部调用 `tasks.Transition`
- `TaskStore.Transition`：终态回调通过 `json.Marshal/Unmarshal` 深拷贝后异步执行（`utils.GetWorkPool().Go`），避免在 tasks.mu 锁内触发 agents.mu 操作

---

## 9. 配置参数完整说明

### 9.1 Config 结构

```go
type Config struct {
    Port          int            `json:"port"`          // HTTP 监听端口，默认 7718
    PublicURL     string         `json:"publicUrl"`     // 外部可达 URL（必需）
    StaticDir     string         `json:"staticDir"`     // 前端静态文件目录，默认 "cmd/web/dist"
    AgentRegistry RegistryConfig `json:"agentRegistry"` // Agent 注册与健康管理
    History       HistoryConfig  `json:"history"`       // 历史归档
    Log           LogConfig      `json:"log"`           // 日志
    Daemon        bool           `json:"daemon"`        // 守护进程模式（仅 Linux）
}
```

### 9.2 RegistryConfig

```go
type RegistryConfig struct {
    UnhealthyAfter string `json:"unhealthyAfter"` // 心跳超时后标记 unhealthy，默认 "30s"
    OfflineAfter   string `json:"offlineAfter"`   // 超过此时间标记 offline 并删除，默认 "60s"
}
```

### 9.3 HistoryConfig

```go
type HistoryConfig struct {
    Enabled       bool        `json:"enabled"`       // 是否启用 MySQL 历史归档，默认 true
    MySQL         MySQLConfig `json:"mysql"`         // MySQL 连接配置
    RetentionDays int         `json:"retentionDays"` // 历史数据保留天数，默认 90
}
```

### 9.4 MySQLConfig

```go
type MySQLConfig struct {
    Host            string `json:"host"`            // 主机地址
    Port            int    `json:"port"`            // 端口号，默认 3306
    User            string `json:"user"`            // 用户名
    Password        string `json:"password"`        // 密码
    Database        string `json:"database"`        // 数据库名
    MaxOpenConns    int    `json:"maxOpenConns"`    // 最大打开连接数，默认 10
    MaxIdleConns    int    `json:"maxIdleConns"`    // 最大空闲连接数，默认 5
    ConnMaxLifetime string `json:"connMaxLifetime"` // 连接最大存活时间，默认 "1h"
}
```

DSN 格式：`{user}:{password}@tcp({host}:{port})/{database}?parseTime=true&loc=Local`

### 9.5 LogConfig

```go
type LogConfig struct {
    Level      string `json:"level"`      // 日志级别，默认 "info"
    Path       string `json:"path"`       // 日志文件路径，默认 "log/admin.log"
    MaxSizeMB  int    `json:"maxSizeMB"`  // 单个日志文件最大体积（MB），默认 100
    MaxBackups int    `json:"maxBackups"` // 保留的旧日志文件数，默认 10
}
```

### 9.6 完整默认值

```go
func DefaultConfig() Config {
    return Config{
        Port:      7718,
        StaticDir: "cmd/web/dist",
        AgentRegistry: RegistryConfig{
            UnhealthyAfter: "30s",
            OfflineAfter:   "60s",
        },
        History: HistoryConfig{
            Enabled:       true,
            RetentionDays: 90,
            MySQL: MySQLConfig{
                MaxOpenConns:    10,
                MaxIdleConns:    5,
                ConnMaxLifetime: "1h",
            },
        },
        Log: LogConfig{
            Level:      "info",
            Path:       "log/admin.log",
            MaxSizeMB:  100,
            MaxBackups: 10,
        },
    }
}
```

### 9.7 校验规则

- `Port` 必须大于 0（必需）
- `PublicURL` 不能为空（必需）
- `AgentRegistry.UnhealthyAfter` 如果非空必须能被 `time.ParseDuration` 解析
- `AgentRegistry.OfflineAfter` 如果非空必须能被 `time.ParseDuration` 解析

---

## 10. 错误系统

### 10.1 Error 类型

```go
type Error struct {
    Code       string         `json:"code"`
    HTTPStatus int            `json:"-"`
    Message    string         `json:"message"`
    Details    map[string]any `json:"details,omitempty"`
}
```

### 10.2 预定义错误码

| 变量 | Code | HTTP Status | 说明 |
|---|---|---|---|
| `ErrTaskNotFound` | `TASK_NOT_FOUND` | 404 | 任务不存在 |
| `ErrTaskConflict` | `TASK_CONFLICT` | 409 | 任务单例冲突 |
| `ErrTaskInvalidState` | `TASK_INVALID_STATE` | 409 | 任务状态不允许此操作 |
| `ErrAgentNotFound` | `AGENT_NOT_FOUND` | 404 | Agent 不存在 |
| `ErrAgentBusy` | `AGENT_BUSY` | 409 | Agent 忙碌 |
| `ErrAgentOffline` | `AGENT_OFFLINE` | 409 | Agent 离线 |
| `ErrCapacityExceeded` | `CAPACITY_EXCEEDED` | 400 | 集群容量不足 |
| `ErrInvalidArgument` | `INVALID_ARGUMENT` | 400 | 参数错误 |
| `ErrHistoryDisabled` | `HISTORY_DISABLED` | 503 | 历史模块未启用 |
| `ErrHistoryNotFound` | `HISTORY_NOT_FOUND` | 404 | 历史记录不存在 |
| `ErrInternal` | `INTERNAL_ERROR` | 500 | 内部错误 |
| `ErrStarredProtected` | `HISTORY_STARRED` | 409 | 已标星记录受保护 |

### 10.3 错误响应格式

```json
{
  "code": "TASK_CONFLICT",
  "message": "another task is currently active",
  "details": {
    "activeTaskId": "abc123",
    "activeName": "200v200",
    "activeState": "running",
    "startedAt": "2026-05-23T10:00:00+08:00"
  }
}
```

---

## 11. 持久化方案

### 11.1 任务（JSON 文件）

```
data/tasks/
  {taskID}.json       -- Task 完整 JSON（格式化缩进）
```

- 写入时机：每次 `TaskStore.Update/Transition` 完成后
- 原子写入：先写 `{id}.json.tmp`，再 `os.Rename` 为 `{id}.json`
- 启动时扫描：终态直接载入；active 状态重置为 failed
- 清理旧版残留的 `.tmp.json` 文件

### 11.2 MySQL 历史归档

- 终态时事务写入 6 张表
- 运行期 Sampler 每 10s 写入 `task_timeseries`
- 每天自动清理过期记录（starred 的不受清理影响）

### 11.3 不持久化

- AgentRegistry：所有 Agent 重启后重新注册
- 指标快照：仅内存，前端轮询时取最新
- 升级进度：仅内存（升级中 Admin 重启视为放弃）

---

## 12. 进程入口（cmd/admin/main.go）

```go
var Version = "dev"  // 编译时注入：-ldflags "-X main.Version=v1.0.0"

func main() {
    // 1. 进程级顶层 recover
    // 2. 解析 -config 和 -d 参数
    // 3. -d 模式：fork 子进程
    // 4. 加载配置（LoadConfig）
    // 5. 初始化日志（zap + RingBuffer 5000 条）
    // 6. NewAdminServer(cfg)
    // 7. srv.Run()
}
```

**启动命令**：

```bash
# 前台运行
go run ./cmd/admin -config conf/admin-config.json

# 守护进程（仅 Linux）
go run ./cmd/admin -config conf/admin-config.json -d

# 编译
go build -o admin.exe ./cmd/admin -ldflags "-X main.Version=v1.0.0"
```

---

## 13. 与计划的差异汇总

| 差异项 | 计划设计 | 实际实现 | 原因 |
|---|---|---|---|
| BinaryStore / UpgradeOrchestrator | 完整实现 | **未实现** | 自动升级改为运维手动重启 |
| 路由前缀 | `/api/` | `/sbot/` | 前后端统一路径规划 |
| 任务 ID | UUID（36 字符） | 随机 hex（32 字符） | 使用 `crypto/rand` 生成 |
| 分配算法 | 均匀分配 | 按 MaxBots 比例分配 + Debug 单节点优先 | 更合理的负载分布 |
| MySQL 交互 | sqlx | 标准 database/sql | 减少依赖 |
| Agent 重新注册 | 拒绝（要求先 deregister） | 覆盖（同 ID 直接覆盖） | Agent 异常重启场景更友好 |
| Agent offline 处理 | 标记 offline 保留 | 标记 offline + **删除** | 防止注册表无限增长 |
| 外键约束 | 物理外键 + CASCADE | 无物理外键，应用层级联 | 符合项目约定 |
| 新增表 | 5 张 | 7 张（新增 task_agent_events） | Agent 状态变化事件追踪 |
| StageReports | 无 | 有 | 渐进式加压阶段完成报告 |
| AgentEvents | 无 | 有 | 任务期间 Agent 状态变化事件 |
| TaskConfig.Codecs/ErrorMap | 无 | 有 | 声明式 codec 与错误码映射上传 |
| TaskAssignment 字段 | 简化 | 完整（含 ConfigURL/ConfigFiles/RampUp） | Agent 配置下载机制 |
| Sampler interval 配置 | 从 config 读取 | 硬编码 10s | 简化实现 |
| PruneRunAt 配置 | HH:MM 格式 | 固定 24h 间隔 | 简化实现 |
| handlers_history.go | 独立文件 | 独立文件（已实现） | 按计划 |
| 基线资源 API | 无 | 8 个端点 | 前端 IDB 与磁盘基线同步 |
| Shutdown Agent API | 无 | 2 个端点（单个 + 全部） | 运维需要远程关机能力 |

---

## 14. HTTP 状态码约定

| 场景 | 状态 |
|---|---|
| 成功（查询/返回数据） | `200` |
| 资源创建成功 | `201` |
| 异步接受（启动/停止任务） | `202` |
| 无内容（删除成功/无待执行任务） | `204` |
| 客户端参数错误 | `400` |
| 资源未找到 | `404` |
| 状态冲突（单例/状态不允许） | `409` |
| 服务不可用（历史模块未启用） | `503` |
| 服务端内部错误 | `500` |

---

## 15. Panic 安全网

所有 HTTP handler 被 `recoverMiddleware` 包裹：

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

进程级顶层 recover（`cmd/admin/main.go`）：

```go
defer func() {
    if rec := recover(); rec != nil {
        fmt.Fprintf(os.Stderr, "[ADMIN] 顶层 panic: %v\n%s\n", rec, debug.Stack())
        stresslog.Error("[ADMIN] 顶层 panic", ...)
        os.Exit(2)
    }
}()
```

---

## 16. 协议契约（强约束）

### 16.1 Admin 必须遵守

1. 所有暴露给 Agent 的接口（`/sbot/agent/*`、`/sbot/tasks/{id}/config/*`）**禁止做用户认证**
2. `RegisterResponse.HeartbeatTtl` 必须等于 `agentRegistry.unhealthyAfter`
3. 任务下发 `TaskAssignment.ConfigURL` 必须可被 Agent 直接 GET（不带任何 header）
4. ConfigURL 拼接：`{publicUrl}/sbot/tasks/{id}/config/{path}`，`publicUrl` 必须配置正确

### 16.2 Admin 假定 Agent 提供

1. 监听 `/agent/v1/{task,stop,shutdown,version,status,logs,logs/files}` 全部端点
2. 任务下发后异步执行，202 Accepted 立即返回
3. 任务完成后通过 `/sbot/agent/{id}/task/{tid}/done` 上报结果

### 16.3 跨模块字段对齐

| 来源 | 对齐对象 | 备注 |
|---|---|---|
| `monitor.CollectorSnapshot` | Agent stress 上报 + Admin 聚合 | 字段必须完全一致 |
| `SystemSnapshot` | Agent system 上报 + Admin 聚合 | 同上 |
| `TaskAssignment` | Admin 下发 + Agent 接收 | 在 `agent/types.go` 与 `admin/types.go` 镜像 |
| `RegisterRequest/Response` | 双向同步 | 同上 |

任何一方修改字段必须同时改另一方。

---

## 17. 关键 Handler 行为说明

### 17.1 handleAgentStressReport

- 丢弃过期 stress 报告：`agent.CurrentTaskID` 为空或 `report.TaskID != currentTaskID` 时返回 `{"status":"stale"}`
- 任何 Agent 请求都视为 keepalive（调用 `agents.Touch`）
- 心跳时间更新在 stress 数据更新之前

### 17.2 handleCreateTask

- multipart/form-data，最大 32MB
- flow.json 为必需文件
- proto 文件：字段名前缀 `proto/` 或 `proto`
- lua 脚本：字段名前缀 `scripts/` 或 `scripts`
- adapter 下接收多份 `*_codec.json`，`errors.json` 可选
- rampUp 校验：stages count 之和必须等于 totalBots
- 自动将上传资源写入磁盘基线目录（`conf/`），使前端下次同步时 IDB 与基线一致

### 17.3 handleStartTask

1. `TaskStore.StartTask`（单例约束入口）
2. `Assigner.Assign`（分配 Agent）
3. 启动 Sampler
4. 异步 `startTaskBackground`：
   - 读取任务配置，构建 `TaskAssignment`
   - 按 `scaleRampUp` 缩放各 Agent 的 rampUp 阶段
   - 向各 Agent 发送 AssignTask RPC（2 次重试）
   - 全部成功 -> `starting -> running`
   - 任一失败 -> 向已接受任务的 Agent 发送 stop，`starting -> failed`

### 17.4 handleStopTask

1. 校验任务状态为 running
2. `running -> stopping`
3. 向在线节点发送 stop RPC
4. 为已离线节点合成 `stopped` report（`synthesizeOfflineReports`）
5. 如果全部节点已有 report -> 直接 `stopping -> stopped`
6. 否则启动 30s 超时安全网（`startStopTimeout`）

### 17.5 handleAgentTaskDone

1. Touch + Heartbeat 刷新心跳，标记 Agent 回 idle
2. 如果 `StageIndex > 0`：存入 `StageReports`（阶段完成报告），不触发状态转换
3. 如果是最终报告：存入 `Reports`，检查是否全部完成
4. Transition 在 Update 外部调用，避免死锁

### 17.6 handleCloneHistory

1. 从 MySQL 获取原始任务配置
2. 创建新 pending 任务（名称追加 " (clone)"，支持 body 中 name 覆盖）
3. **不自动启动**，留给用户确认参数

---

## 18. 辅助工具函数

| 函数 | 文件 | 说明 |
|---|---|---|
| `stringOr(v, fallback, label)` | helpers.go | 字符串配置默认值（空则用 fallback，打印警告） |
| `intOr(v, fallback, label)` | helpers.go | 整数配置默认值（<=0 则用 fallback，打印警告） |
| `secsOr(v, fallback, label)` | helpers.go | int 秒数 -> Go duration 字符串（如 "5s"） |
| `parseLogQueryParams(r)` | helpers.go | 解析日志查询参数（afterSeq, limit） |
| `parseUint64OrDefault(s, def)` | helpers.go | 解析 uint64 参数 |
| `parseIntOrDefault(s, def)` | handlers.go | 解析 int 参数 |
| `parseBoolOrDefault(s, def)` | handlers.go | 解析 bool 参数 |
| `parseTimeOrDefault(s, def)` | handlers.go | 解析 RFC3339 时间参数 |
| `parseTagsFromQuery(r, key)` | handlers.go | 解析 URL query 中的 tag 列表 |
| `normalizeAddr(addr)` | agent_dispatcher.go | 去除 http:// 和 https:// 前缀 |
| `safeWriteFile(dir, name, data)` | handlers.go | 安全写文件（防路径穿越，自动创建目录） |
| `listDirFiles(dir, ext)` | handlers.go | 列出目录中指定后缀的文件 |
| `serveBaselineFile(w, r, dir, key)` | handlers.go | 提供基线文件（防路径穿越） |
| `listLogFiles(logPath)` | handlers.go | 列出日志文件（同目录同前缀） |
| `serveLogFile(w, r, dir, name)` | handlers.go | 提供日志文件下载 |
| `scaleRampUp(cfg, totalBots, assignedBots)` | handlers.go | 按比例缩放各 stage 的 count（分布式分配） |
| `generateID()` | admin.go | 生成 32 字符随机 hex ID |
| `buildFinalStressFromReports(task)` | admin.go | 从 agent 终止报告聚合最终快照（优先于心跳聚合） |
| `buildConfigSummary(task)` | history.go | 从 Task 构建 ConfigSummary |
| `buildListWhere(f HistoryFilter)` | history.go | 构建历史列表查询 WHERE 子句（防注入） |
