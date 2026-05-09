docs/admin-implementation.md# Admin 服务端实施方案

> **角色定位**：Admin 是分布式压测系统的**控制中枢**，对前端提供管控 API（任务/Agent/指标/系统/二进制），对 Agent 提供注册/心跳/上报接口和命令下发能力。
> **本文档目标读者**：负责 `admin/` 包及 `cmd/admin/` 入口的开发者。
> **前置阅读**：`docs/design-distributed-master.md` §5 「Admin API 设计」、§7 「压测指标聚合」、§8 「Agent 系统监控」、§11 「Admin Server 设计」、§12 「任务分配算法」、§14 「故障处理」、§15 「热更新」、§16 「任务单例约束」、§17 「历史压测记录」。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- Admin 二进制：`admin.exe`（Linux：`admin`），来自 `cmd/admin`
- 默认监听端口：`:8080`
- 数据目录：`data/`（任务持久化、二进制存储）
- 静态资源目录：`web/dist/`（前端构建产物）
- **Agent 主文档** = `docs/agent-implementation.md`，所有协议契约可双向交叉验证

## 1. 模块职责

Admin 同时承担五类职责：

1. **集群管理**：维护 Agent 注册表（增删改查、健康检测、版本追踪）
2. **任务编排**：接收前端请求 → 单例校验 → 分配账号范围 → 推送任务 → 监控完成
3. **指标聚合**：合并多 Agent 上报的压测/系统指标，生成集群快照供前端轮询
4. **历史归档**：任务运行期定时采样 + 终态归档至 MySQL，支持回查、对比、克隆、标签
5. **运维支持**：二进制存储与分发、滚动升级编排、Agent 命令下发、前端静态资源托管

不做的事：
- 不做用户认证（内网工具，假定可信网络）
- 不做指标告警（监控系统职责由外部 Prometheus 等承担，可后续接入）
- 不做任务排队调度（任务单例约束，并发启动直接拒绝）

## 2. 包结构与文件清单

```
admin/
  admin.go               — AdminServer 主结构、HTTP 路由、生命周期
  config.go              — Config 解析与校验
  types.go               — 共享 DTO（与 agent/types.go 镜像）
  task.go                — Task 实体 + TaskStore 实现 + 状态机 + 单例约束
  agent.go               — AgentNode + AgentRegistry + 心跳超时检测
  assignment.go          — Assigner：分配算法
  aggregator.go          — MetricsAggregator：压测/系统指标聚合
  binary.go              — BinaryStore：二进制存储、SHA256、下载/上传
  upgrade.go             — UpgradeOrchestrator：单点 / 滚动升级
  history.go             — HistoryStore：MySQL 持久化、查询、标签
  history_schema.go      — DDL 与 Schema 升级
  sampler.go             — Sampler：运行期定时采集时序数据
  handlers.go            — 全部 HTTP handler（前端 + Agent）
  handlers_history.go    — 历史压测相关 handler
  agent_dispatcher.go    — Admin → Agent 调用（含重试 / 健康检测）
  persist.go             — JSON 文件持久化（Task 实时态）
  errors.go              — Admin 内部错误类型
  *_test.go

cmd/admin/
  main.go                — Admin 进程入口

conf/
  admin-config.json      — Admin 配置文件
```

**新增依赖**：

```bash
go get github.com/google/uuid
go get github.com/go-sql-driver/mysql
go get github.com/jmoiron/sqlx           # 可选，简化 SQL 写法
```

实时态用本地 JSON（`data/tasks/*.json`），历史归档用 MySQL。

## 3. 数据模型（DTO）

### 3.1 Task

```go
type Task struct {
    ID          string     `json:"id"`              // UUID
    Name        string     `json:"name"`
    State       TaskState  `json:"state"`
    TotalBots   int        `json:"totalBots"`       // 集群总机器人数
    Config      TaskConfig `json:"config"`          // 任务配置
    Assignments []Assignment `json:"assignments"`   // 分配快照（taskID + agentID + 范围）
    CreatedAt   time.Time  `json:"createdAt"`
    StartedAt   *time.Time `json:"startedAt,omitempty"`
    StoppedAt   *time.Time `json:"stoppedAt,omitempty"`
    ErrorMsg    string     `json:"errorMsg,omitempty"`
    Reports     map[string]TaskCompletionReport `json:"reports,omitempty"` // agentID → 完成报告
}

type TaskState string
const (
    TaskPending  TaskState = "pending"   // 已创建未启动
    TaskStarting TaskState = "starting"  // 正在向 Agent 推送
    TaskRunning  TaskState = "running"   // 至少一个 Agent 已开始执行
    TaskStopping TaskState = "stopping"  // 收到停止指令，等待 Agent 上报 done
    TaskStopped  TaskState = "stopped"   // 全部 Agent 已 done（所有结果）
    TaskFailed   TaskState = "failed"    // 启动阶段失败
)

type TaskConfig struct {
    FlowJSON     json.RawMessage      `json:"flowJson"`       // 任务流程（直接保存原始 JSON）
    ProtoFiles   map[string][]byte    `json:"protoFiles"`     // 文件名 → 内容（base64 自动）
    LuaScripts   map[string][]byte    `json:"luaScripts"`     // 文件名 → 内容
    HeaderJSON   json.RawMessage      `json:"headerJson"`     // 协议头配置
    RobotConfig  RobotConfig          `json:"robotConfig"`
    Deadline     *time.Time           `json:"deadline,omitempty"`
}

type RobotConfig struct {
    AuthAddr     string `json:"authAddr"`
    Concurrency  int    `json:"concurrency"`
    TimeoutSec   int    `json:"timeoutSec"`
}
```

### 3.2 Assignment

```go
type Assignment struct {
    TaskID      string `json:"taskId"`
    AgentID     string `json:"agentId"`
    StartNumber int    `json:"startNumber"`  // 账号起始
    TotalBots   int    `json:"totalBots"`    // 本节点机器人数
}
```

### 3.3 AgentNode

```go
type AgentNode struct {
    ID                string        `json:"agentId"`
    Name              string        `json:"name"`
    Address           string        `json:"address"`
    AppVersion        string        `json:"appVersion"`
    MaxBots           int           `json:"maxBots"`
    StressInterval    string        `json:"stressInterval"`
    SystemInterval   string        `json:"systemInterval"`
    StaticInfo        StaticInfo    `json:"staticInfo"`

    // 运行时状态
    Status            AgentStatus   `json:"status"`
    LastHeartbeatAt   time.Time     `json:"lastHeartbeatAt"`
    CurrentTaskID     string        `json:"currentTaskId,omitempty"`
    CurrentBots       int           `json:"currentBots"`

    // 最新指标快照（聚合端点用）
    LatestStress      *StressSnapshot `json:"-"` // 不直接 JSON 输出
    LatestSystem      *SystemSnapshot `json:"-"`
    StressUpdatedAt   time.Time     `json:"stressUpdatedAt,omitempty"`
    SystemUpdatedAt   time.Time     `json:"systemUpdatedAt,omitempty"`
}

type AgentStatus string
const (
    AgentIdle      AgentStatus = "idle"
    AgentBusy      AgentStatus = "busy"
    AgentUnhealthy AgentStatus = "unhealthy" // 心跳超 30s 未到
    AgentOffline   AgentStatus = "offline"   // 心跳超 60s 未到
)
```

### 3.4 StressReport / SystemReport

```go
// Agent → Admin 上行
type StressReport struct {
    AgentID    string                       `json:"agentId"`
    TaskID     string                       `json:"taskId"`
    ReportedAt time.Time                    `json:"reportedAt"`
    Snapshot   monitor.CollectorSnapshot    `json:"snapshot"`
}

type SystemReport struct {
    AgentID    string         `json:"agentId"`
    ReportedAt time.Time      `json:"reportedAt"`
    Snapshot   SystemSnapshot `json:"snapshot"`
}

type TaskCompletionReport struct {
    AgentID       string                    `json:"agentId"`
    TaskID        string                    `json:"taskId"`
    Result        TaskResult                `json:"result"`        // completed | stopped | failed
    ErrorMsg      string                    `json:"errorMsg,omitempty"`
    FinishedAt    time.Time                 `json:"finishedAt"`
    FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
}
```

### 3.5 BinaryMeta

```go
type BinaryMeta struct {
    Version    string    `json:"version"`     // "v1.2.0"
    Filename   string    `json:"filename"`    // "agent-v1.2.0.exe"
    OS         string    `json:"os"`          // "windows"|"linux"
    Arch       string    `json:"arch"`        // "amd64"
    SHA256     string    `json:"sha256"`
    SizeBytes  int64     `json:"sizeBytes"`
    UploadedAt time.Time `json:"uploadedAt"`
}

type UpgradeStatus struct {
    InProgress       bool      `json:"inProgress"`
    Version          string    `json:"version"`
    StartedAt        time.Time `json:"startedAt,omitempty"`
    Total            int       `json:"total"`
    Completed        int       `json:"completed"`
    Failed           int       `json:"failed"`
    CurrentAgentID   string    `json:"currentAgentId,omitempty"`
    PerAgent         map[string]AgentUpgradeState `json:"perAgent"` // agentID → 当前阶段
}

type AgentUpgradeState struct {
    Phase     string    `json:"phase"`    // queued | sent | upgrading | success | failed
    StartedAt time.Time `json:"startedAt,omitempty"`
    Error     string    `json:"error,omitempty"`
}
```

### 3.6 HistoryRecord（历史归档）

```go
type HistoryRecord struct {
    ID          string     `json:"id" db:"id"`
    Name        string     `json:"name" db:"name"`
    State       TaskState  `json:"state" db:"state"`           // 仅 stopped/failed
    TotalBots   int        `json:"totalBots" db:"total_bots"`
    AgentCount  int        `json:"agentCount" db:"agent_count"`
    CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
    StartedAt   *time.Time `json:"startedAt,omitempty" db:"started_at"`
    StoppedAt   *time.Time `json:"stoppedAt,omitempty" db:"stopped_at"`
    DurationSec int        `json:"durationSec" db:"duration_sec"`
    ErrorMsg    string     `json:"errorMsg,omitempty" db:"error_msg"`

    // 用户标记（PUT 接口可修改）
    Starred     bool      `json:"starred" db:"starred"`
    Tags        []string  `json:"tags" db:"tags"`           // JSON array
    Note        string    `json:"note" db:"note"`

    // 配置摘要（用于列表页快速预览，避免拉完整 config）
    ConfigSummary ConfigSummary `json:"configSummary" db:"config_summary"`
}

type ConfigSummary struct {
    AuthAddr     string `json:"authAddr"`
    Concurrency  int    `json:"concurrency"`
    TimeoutSec   int    `json:"timeoutSec"`
    FlowSizeKB   int    `json:"flowSizeKB"`
    ProtoCount   int    `json:"protoCount"`
    ScriptCount  int    `json:"scriptCount"`
}

type HistoryDetail struct {
    HistoryRecord

    Assignments     []Assignment              `json:"assignments"`
    AgentReports    []HistoryAgentReport      `json:"agentReports"`
    FinalSnapshot   monitor.CollectorSnapshot `json:"finalSnapshot"`
    FinalSystem     ClusterSystemSnapshot     `json:"finalSystem"`
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
    State          string    // stopped/failed/全部
    StartedAfter   time.Time
    StartedBefore  time.Time
    Tags           []string  // 命中任意一个即匹配
    TagsAll        []string  // 必须全部匹配
    Starred        *bool
    Search         string    // 模糊匹配 name + note
    Limit          int       // 默认 20
    Offset         int
    OrderBy        string    // started_at desc | duration_sec desc | etc
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
    // 对比维度（可选 server 端预算或前端自算）
    Diff  CompareDiff     `json:"diff"`
}

type CompareDiff struct {
    // 关键指标的差异表，比如 P99 / Apdex / SuccessRate / QPS
    Actions map[string][]float64 `json:"actions"` // actionName → [taskA_p99, taskB_p99, ...]
}
```

### 3.7 时序采样点

```go
type TimeseriesPoint struct {
    TaskID      string    `json:"taskId" db:"task_id"`
    SampledAt   time.Time `json:"sampledAt" db:"sampled_at"`
    ElapsedSec  int       `json:"elapsedSec" db:"elapsed_sec"`
    DataType    string    `json:"dataType" db:"data_type"` // "stress" | "system"
    Snapshot    json.RawMessage `json:"snapshot" db:"snapshot"`
}

type TimeseriesResponse struct {
    TaskID string             `json:"taskId"`
    Stress []TimeseriesPoint  `json:"stress"`
    System []TimeseriesPoint  `json:"system"`
}
```

## 4. 核心组件设计

### 4.1 AdminServer

```go
type AdminServer struct {
    cfg          Config

    tasks        *TaskStore
    agents       *AgentRegistry
    binaries     *BinaryStore
    aggregator   *MetricsAggregator
    upgrader     *UpgradeOrchestrator
    dispatcher   *AgentDispatcher
    assigner     *Assigner

    logsProxyClient *http.Client // Agent 日志代理（5s 超时）

    history      *HistoryStore  // 可选，cfg.History.Enabled=false 时为 nil
    sampler      *Sampler       // 同上

    httpSrv      *http.Server
    stopCh       chan struct{}
    wg           sync.WaitGroup
}

func New(cfg Config) (*AdminServer, error)
func (s *AdminServer) Run() error      // 阻塞
func (s *AdminServer) Shutdown(ctx context.Context) error
```

**启动顺序**：

```
 1. 加载 config
 2. （可选）初始化 HistoryStore：连接 MySQL → 执行 Schema 迁移 → 启动定时清理
    - 连接失败 → fail-fast（除非 history.enabled=false）
 3. 初始化 TaskStore（从 data/tasks/ 恢复未结束任务）
 4. 初始化 AgentRegistry（清空 - Agent 重启会重新注册）
 5. 初始化 BinaryStore（扫描 data/binaries/）
 6. 初始化 MetricsAggregator
 7. 初始化 UpgradeOrchestrator
 8. 初始化 AgentDispatcher（HTTP client pool）
 9. （可选）初始化 Sampler，注入 aggregator + history（用于运行期采样）
10. 启动 AgentRegistry 心跳超时检测协程
11. 注册路由 + http.Server.ListenAndServe
```

**关闭顺序**：

```
1. http.Server.Shutdown(ctx)
2. 停止 Sampler / 心跳检测协程
3. 持久化 TaskStore（最终落盘）
4. 关闭 MySQL 连接池
5. 不主动通知 Agent（Agent 自治继续运行）
```

### 4.2 TaskStore（含单例约束）

```go
type TaskStore struct {
    mu       sync.RWMutex
    tasks    map[string]*Task   // taskID → Task

    // 单例约束相关
    startMu  sync.Mutex         // 串行化 start 流程，防止 TOCTOU
    activeID string             // 当前 active 任务 ID（"" 表示无）

    dataDir  string
}

func NewTaskStore(dataDir string) (*TaskStore, error) {
    // 启动时扫描 dataDir/tasks/*.json，恢复未结束任务
    // active 状态的任务恢复为 failed（避免幽灵 active）
}

// 状态变更必须通过此方法（内部已加锁）
func (s *TaskStore) Transition(taskID string, from, to TaskState) (*Task, error)

// 单例查询
func (s *TaskStore) ActiveTask() *Task              // 无则返回 nil
func (s *TaskStore) HasActive() bool

// CRUD
func (s *TaskStore) Create(t *Task) error
func (s *TaskStore) Get(id string) (*Task, bool)
func (s *TaskStore) List() []*Task
func (s *TaskStore) ListByState(state TaskState) []*Task
func (s *TaskStore) Update(id string, fn func(*Task)) error  // 自动持久化
func (s *TaskStore) Delete(id string) error                  // 仅允许 stopped/failed

// 启动入口（外部调用此方法，而不是直接 Transition）
// 内部串行化：拿 startMu → 检查 activeID → Transition → 设置 activeID
func (s *TaskStore) StartTask(id string) (*Task, error)
```

**单例约束实现要点**：

```go
func (s *TaskStore) StartTask(id string) (*Task, error) {
    s.startMu.Lock()
    defer s.startMu.Unlock()

    // 1. 单例检查（在锁内）
    if active := s.activeUnlocked(); active != nil {
        return nil, &ConflictError{
            Code: "TASK_CONFLICT",
            Details: map[string]any{
                "activeTaskId": active.ID,
                "activeName":   active.Name,
                "activeState":  active.State,
                "startedAt":    active.StartedAt,
            },
        }
    }

    // 2. 状态转换
    task, err := s.Transition(id, TaskPending, TaskStarting)
    if err != nil {
        return nil, err
    }

    // 3. 标记 activeID（仍在锁内，保证强一致）
    s.mu.Lock()
    s.activeID = id
    s.mu.Unlock()

    return task, nil
}

// activeUnlocked 必须在 startMu 持有时调用
func (s *TaskStore) activeUnlocked() *Task {
    s.mu.RLock()
    defer s.mu.RUnlock()
    if s.activeID == "" { return nil }
    return s.tasks[s.activeID]
}
```

**activeID 维护**：

| 触发 | 动作 |
|---|---|
| `StartTask` 成功 | `activeID = id` |
| `Transition(running → stopping)` | 不变（仍 active） |
| `Transition(* → stopped/failed)` | `activeID = ""` |
| Admin 重启时 active 任务恢复为 failed | `activeID = ""` |

**状态转换矩阵**：

| 当前 | 允许转到 | 触发 | 占据单例位 |
|---|---|---|---|
| `pending` | `starting`、`failed`、`stopped`（取消） | `POST /tasks/{id}/start` / 内部错误 / `DELETE` | ❌ pending 不占 |
| `starting` | `running`、`failed` | 全部 Agent 接受 / 任意 Agent 失败 | ✅ |
| `running` | `stopping` | `POST /tasks/{id}/stop` / 任务自然到达 deadline | ✅ |
| `stopping` | `stopped`、`failed` | 全部 Agent 上报 done | ✅ |
| `stopped`、`failed` | （终态） | — | ❌ |

**持久化策略**：

- 每次状态变更后写 `data/tasks/{id}.json`（覆盖写）
- 终态任务的 JSON 保留 7 天作冷备（即使 MySQL 已归档，仍便于排错）
- 启动时扫描所有 `*.json`：终态直接载入；active 状态视为崩溃前在跑，重置为 `failed` 并记录 `errorMsg="admin restart, task lost"`（不主动尝试恢复，避免账号冲突）

**与 HistoryStore 的协作**：

任务进入终态时（`Transition(* → stopped/failed)`），AdminServer 监听该转换并触发：

```go
go func() {
    if s.history != nil {
        if err := s.history.Archive(task, finalSnap, finalSys); err != nil {
            log.Printf("archive task %s failed: %v", task.ID, err)
            // 失败不阻塞，下次启动时会扫描 data/tasks 看到终态任务再尝试归档
        }
    }
}()
```

### 4.3 AgentRegistry

```go
type AgentRegistry struct {
    mu       sync.RWMutex
    agents   map[string]*AgentNode

    cfg      RegistryConfig
    onChange func(agentID string, from, to AgentStatus) // upgrade orchestrator 订阅
}

type RegistryConfig struct {
    UnhealthyAfter time.Duration  // 默认 30s
    OfflineAfter   time.Duration  // 默认 60s
}

func (r *AgentRegistry) Register(node *AgentNode) error
func (r *AgentRegistry) Heartbeat(agentID string, hb HeartbeatRequest) error
func (r *AgentRegistry) Deregister(agentID string) error
func (r *AgentRegistry) Get(agentID string) (*AgentNode, bool)
func (r *AgentRegistry) List() []*AgentNode
func (r *AgentRegistry) ListByStatus(status AgentStatus) []*AgentNode
func (r *AgentRegistry) UpdateStress(agentID string, snap monitor.CollectorSnapshot, at time.Time)
func (r *AgentRegistry) UpdateSystem(agentID string, snap SystemSnapshot, at time.Time)

// 后台心跳检测协程
func (r *AgentRegistry) startHealthChecker(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            r.scanAndMarkStatus()
        }
    }
}
```

**心跳超时分级**：

```
heartbeatLag = now - lastHeartbeatAt

heartbeatLag <  30s   → idle / busy（保持业务状态）
30s ≤ lag    <  60s   → unhealthy（前端橙色警告，仍尝试推送）
60s ≤ lag             → offline（前端红色，不再分配新任务）
```

注册时如果同名 Agent 已存在但状态为 offline，直接覆盖；其他状态拒绝注册（要求先 deregister 或等离线）。

### 4.4 Assigner

```go
type Assigner struct{}

func (a *Assigner) Assign(task *Task, agents []*AgentNode) ([]Assignment, error) {
    // 1. 过滤 idle 且 status != offline/unhealthy 的 agent
    // 2. 总容量校验：sum(agent.MaxBots) >= task.TotalBots
    // 3. 均匀分配，剩余按机器序号补齐
    // 4. 计算每个 agent 的 startNumber（起始账号 = 前序总和）
}
```

**分配算法**（默认均匀）：

```go
func (a *Assigner) uniformAssign(task *Task, agents []*AgentNode) []Assignment {
    n := len(agents)
    base := task.TotalBots / n
    rem  := task.TotalBots % n

    out := make([]Assignment, 0, n)
    cursor := 0  // 累计 startNumber 偏移
    baseAccount := task.RobotConfig.StartAccount  // 任务级别起始账号

    for i, agent := range agents {
        bots := base
        if i < rem { bots++ }
        out = append(out, Assignment{
            TaskID:      task.ID,
            AgentID:     agent.ID,
            StartNumber: baseAccount + cursor,
            TotalBots:   bots,
        })
        cursor += bots
    }
    return out
}
```

**未来扩展点**（当前不实现）：按 MaxBots 加权分配、按 CPU/Mem 健康度过滤热点节点。

### 4.5 MetricsAggregator

```go
type MetricsAggregator struct {
    registry *AgentRegistry
}

// 压测聚合：合并所有 busy 状态 agent 的最新 stress 快照
func (a *MetricsAggregator) AggregateStress(taskID string) ClusterStressSnapshot {
    // 1. 取所有 busy 且 currentTaskID == taskID 的 agent
    // 2. 累加 robots / connections / bandwidth / totalActions
    // 3. actions 按 name groupBy，桶计数累加，重新计算 P50/P90/P95/P99
    // 4. Apdex 用 satisfied/tolerating 计数累加重新计算
    // 5. errors 按 msg merge sum count
}

// 系统聚合：所有 status != offline 的 agent
func (a *MetricsAggregator) AggregateSystem() ClusterSystemSnapshot {
    // 详见主文档 §8.5
}
```

**聚合规则速查**：

| 类别 | 字段 | 聚合函数 |
|---|---|---|
| 计数器 | `robots.started/running/stopped/errored` | sum |
| 计数器 | `connections.*` | sum |
| 计数器 | `bandwidth.totalSendBytes/totalRecvBytes` | sum |
| 速率 | `bandwidth.sendMBps/recvMBps` | 重算 = sum(bytes) / max(uptime) |
| 计数器 | `action.successCount/failureCount/timeoutCount/skippedCount/executing` | sum |
| 直方图 | `latencyBuckets[]` | 按位 sum |
| 直方图 | `latencySumNs` | sum |
| 计算 | `latency.minMs/maxMs` | min / max（每节点最小最大里取） |
| 计算 | `latency.avgMs` | latencySumNs / 1e6 / sum(latencyCount) |
| 计算 | `latency.p50/p90/p95/p99` | 在合并桶上插值（详见 monitor.LatencyHistogram 算法） |
| 计算 | `apdex` | (sum(satisfied) + sum(tolerating)*0.5) / sum(total) |
| 计算 | `successRate` | sum(success) / sum(sample) |
| 计算 | `avgQps` | sum(sample) / max(uptime) |
| 错误 | `errors[]` | 按 msg group sum |

**关键禁忌**：**绝不**简单平均 P99（数学错误）。必须重建合并直方图后插值。

> ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程。设计文档保留供参考。

### 4.6 BinaryStore

```go
type BinaryStore struct {
    dir string  // data/binaries
    mu  sync.RWMutex
    metas map[string]BinaryMeta // filename → meta
}

func (s *BinaryStore) Upload(r io.Reader, version, os, arch string) (BinaryMeta, error) {
    // 1. 原子写入 dir/tmp-{uuid}
    // 2. 计算 SHA256
    // 3. rename → dir/agent-{version}-{os}-{arch}.exe（或省略 os/arch）
    // 4. 同时写 .sha256 sidecar 文件
    // 5. 更新 metas
}

func (s *BinaryStore) List() []BinaryMeta
func (s *BinaryStore) Get(filename string) (BinaryMeta, bool)
func (s *BinaryStore) Open(filename string) (io.ReadCloser, error)
func (s *BinaryStore) Delete(filename string) error
```

**文件命名规则**（统一）：

```
agent-{version}.exe                  ← Windows 默认
agent-{version}                      ← Linux 默认
agent-{version}-{os}-{arch}.exe      ← 多平台（可选）
agent-{version}.sha256               ← 校验和文件
```

**上传约束**：
- 单文件最大 200MB
- 文件名只允许 `[a-zA-Z0-9._-]`
- 同 version 重复上传需 `force=true` 才允许覆盖

### 4.7 UpgradeOrchestrator

```go
type UpgradeOrchestrator struct {
    agents     *AgentRegistry
    binaries   *BinaryStore
    dispatcher *AgentDispatcher
    publicURL  string  // Admin 对外可访问的地址（写入 config）

    mu      sync.Mutex
    current *upgradeJob  // 同时仅允许一个滚动升级
}

type upgradeJob struct {
    version    string
    targets    []string                       // agentID 列表
    perAgent   map[string]*AgentUpgradeState  // 进度
    startedAt  time.Time
    cancelCh   chan struct{}
}

// 单点升级
func (o *UpgradeOrchestrator) UpgradeOne(agentID, version string) error

// 滚动升级（同步阻塞，上层用 goroutine 调）
func (o *UpgradeOrchestrator) UpgradeAll(version string) error

// 状态查询
func (o *UpgradeOrchestrator) Status() *UpgradeStatus

// 取消
func (o *UpgradeOrchestrator) Cancel() error
```

**滚动升级流程**：

```
for each agent in targets:
  1. 标记 perAgent[id].Phase = "sent"
  2. POST agent /agent/v1/upgrade { url, sha256, version }
     - 失败：phase = "failed"，错误记录，跳过该 agent，继续下一台（不中断滚动）
  3. 等待 agent 重新注册：
     - 轮询 registry，agent.AppVersion == version 且 status != offline
     - 超时 5min → phase = "failed"，错误 "upgrade timeout"
     - 成功 → phase = "success"
  4. 切到下一台，间隔 5s 让集群稳定
```

**重要语义**：滚动升级允许部分失败，最终 `Status()` 中 `Failed > 0` 即代表存在失败节点，前端展示明细让运维介入。

### 4.8 AgentDispatcher

```go
type AgentDispatcher struct {
    httpClient *http.Client  // Timeout 30s
}

func (d *AgentDispatcher) AssignTask(addr string, a Assignment, cfg TaskAssignment) error
func (d *AgentDispatcher) Stop(addr, taskID string) error
func (d *AgentDispatcher) Upgrade(addr string, req UpgradeRequest) error
func (d *AgentDispatcher) Restart(addr string) error
func (d *AgentDispatcher) Version(addr string) (string, error)
```

**通用模板**（带重试）：

```go
func (d *AgentDispatcher) post(addr, path string, body interface{}, retries int) (*http.Response, error) {
    backoff := 1 * time.Second
    for i := 0; i <= retries; i++ {
        resp, err := d.tryPost(addr, path, body)
        if err == nil && resp.StatusCode/100 == 2 {
            return resp, nil
        }
        if resp != nil { resp.Body.Close() }
        if i == retries { return nil, fmt.Errorf("after %d retries: %w", retries, err) }
        time.Sleep(backoff)
        backoff *= 2
        if backoff > 10*time.Second { backoff = 10 * time.Second }
    }
    return nil, errors.New("unreachable")
}
```

### 4.9 HistoryStore（MySQL 历史归档）

```go
type HistoryStore struct {
    db        *sqlx.DB
    cfg       HistoryConfig
    retention time.Duration
}

func NewHistoryStore(cfg HistoryConfig) (*HistoryStore, error) {
    // 1. sql.Open + Ping
    // 2. 自动迁移 Schema（执行 history_schema.go 中的 DDL）
    // 3. 启动定时清理协程
}

// 归档：在任务终态时调用一次，5 个表事务写入
func (h *HistoryStore) Archive(task *Task,
    finalStress monitor.CollectorSnapshot,
    finalSystem ClusterSystemSnapshot,
    cfgArchive ConfigArchive,
) error

// 查询
func (h *HistoryStore) List(filter HistoryFilter) (*HistoryListResponse, error)
func (h *HistoryStore) Get(id string) (*HistoryDetail, error)
func (h *HistoryStore) GetConfig(id string) (*ConfigArchive, error)
func (h *HistoryStore) GetTimeseries(id string) (*TimeseriesResponse, error)
func (h *HistoryStore) AllTags() ([]string, error)

// 修改
func (h *HistoryStore) UpdateMeta(id string, req UpdateHistoryRequest) error
func (h *HistoryStore) Delete(id string) error  // starred=true 时拒绝（除非 force=true）

// 时序写入（由 Sampler 调用）
func (h *HistoryStore) AppendTimeseries(taskID string, point TimeseriesPoint) error

// 维护
func (h *HistoryStore) PruneExpired(now time.Time) (deleted int, err error)
func (h *HistoryStore) Close() error
```

#### 4.9.1 完整 Schema DDL

```sql
-- ===== 任务历史主表 =====
CREATE TABLE IF NOT EXISTS task_history (
    id              VARCHAR(36)   NOT NULL,
    name            VARCHAR(200)  NOT NULL,
    state           VARCHAR(20)   NOT NULL,
    total_bots      INT           NOT NULL,
    agent_count     INT           NOT NULL,
    created_at      DATETIME(3)   NOT NULL,
    started_at      DATETIME(3)   NULL,
    stopped_at      DATETIME(3)   NULL,
    duration_sec    INT           NOT NULL DEFAULT 0,
    error_msg       TEXT          NULL,

    -- 用户标记
    starred         TINYINT(1)    NOT NULL DEFAULT 0,
    tags            JSON          NULL,
    note            TEXT          NULL,

    -- 配置摘要
    config_summary  JSON          NULL,

    PRIMARY KEY (id),
    INDEX idx_started_at (started_at DESC),
    INDEX idx_starred (starred, started_at DESC),
    INDEX idx_state (state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 集群分配快照 =====
CREATE TABLE IF NOT EXISTS task_assignment (
    id              BIGINT        NOT NULL AUTO_INCREMENT,
    task_id         VARCHAR(36)   NOT NULL,
    agent_id        VARCHAR(36)   NOT NULL,
    agent_name      VARCHAR(100)  NULL,
    start_number    INT           NOT NULL,
    total_bots      INT           NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_task_id (task_id),
    CONSTRAINT fk_assignment_task FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 各 Agent 完成报告 =====
CREATE TABLE IF NOT EXISTS task_report (
    id              BIGINT        NOT NULL AUTO_INCREMENT,
    task_id         VARCHAR(36)   NOT NULL,
    agent_id        VARCHAR(36)   NOT NULL,
    agent_name      VARCHAR(100)  NULL,
    result          VARCHAR(20)   NOT NULL,
    error_msg       TEXT          NULL,
    finished_at     DATETIME(3)   NOT NULL,
    final_snapshot  JSON          NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_task_id (task_id),
    CONSTRAINT fk_report_task FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 集群聚合终态快照 =====
CREATE TABLE IF NOT EXISTS task_aggregated (
    task_id         VARCHAR(36)   NOT NULL,
    final_snapshot  JSON          NOT NULL,
    final_system    JSON          NULL,
    PRIMARY KEY (task_id),
    CONSTRAINT fk_aggregated_task FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 时序采样数据 =====
CREATE TABLE IF NOT EXISTS task_timeseries (
    id              BIGINT        NOT NULL AUTO_INCREMENT,
    task_id         VARCHAR(36)   NOT NULL,
    sampled_at      DATETIME(3)   NOT NULL,
    elapsed_sec     INT           NOT NULL,
    data_type       VARCHAR(20)   NOT NULL,
    snapshot        JSON          NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_task_sampled (task_id, sampled_at),
    CONSTRAINT fk_timeseries_task FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ===== 任务配置归档（用于克隆/回放）=====
CREATE TABLE IF NOT EXISTS task_config_archive (
    task_id         VARCHAR(36)   NOT NULL,
    flow_json       MEDIUMTEXT    NOT NULL,
    proto_files     JSON          NULL,
    scripts         JSON          NULL,
    header_json     TEXT          NULL,
    robot_config    JSON          NOT NULL,
    PRIMARY KEY (task_id),
    CONSTRAINT fk_config_task FOREIGN KEY (task_id) REFERENCES task_history(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

#### 4.9.2 归档事务

```go
func (h *HistoryStore) Archive(task *Task, finalStress, finalSys, cfgArchive ...) error {
    tx, err := h.db.Beginx()
    if err != nil { return err }
    defer tx.Rollback()

    // 1. task_history
    if _, err := tx.NamedExec(`INSERT INTO task_history ...`, ...); err != nil { return err }

    // 2. task_assignment（批量）
    for _, a := range task.Assignments { tx.NamedExec(...) }

    // 3. task_report（批量）
    for _, r := range task.Reports { tx.NamedExec(...) }

    // 4. task_aggregated
    tx.Exec(`INSERT INTO task_aggregated ...`)

    // 5. task_config_archive
    tx.Exec(`INSERT INTO task_config_archive ...`)

    return tx.Commit()
}
```

幂等性：归档前先 `SELECT 1 FROM task_history WHERE id=? `，已存在则跳过（避免重启重复归档）。

#### 4.9.3 列表查询

```go
func (h *HistoryStore) List(f HistoryFilter) (*HistoryListResponse, error) {
    qb := goqu.From("task_history").Select("*")

    if f.State != "" { qb = qb.Where(goqu.C("state").Eq(f.State)) }
    if !f.StartedAfter.IsZero()  { qb = qb.Where(goqu.C("started_at").Gte(f.StartedAfter)) }
    if !f.StartedBefore.IsZero() { qb = qb.Where(goqu.C("started_at").Lte(f.StartedBefore)) }
    if f.Starred != nil          { qb = qb.Where(goqu.C("starred").Eq(*f.Starred)) }

    if len(f.Tags) > 0 {
        // tags 是 JSON Array，使用 JSON_OVERLAPS（MySQL 8.0+）
        // 任意一个匹配
        qb = qb.Where(goqu.L("JSON_OVERLAPS(tags, ?)", jsonArr(f.Tags)))
    }
    if len(f.TagsAll) > 0 {
        // 全部匹配：JSON_CONTAINS 多次
        for _, t := range f.TagsAll {
            qb = qb.Where(goqu.L(`JSON_CONTAINS(tags, JSON_QUOTE(?))`, t))
        }
    }
    if f.Search != "" {
        qb = qb.Where(goqu.Or(
            goqu.C("name").Like("%"+f.Search+"%"),
            goqu.C("note").Like("%"+f.Search+"%"),
        ))
    }

    // 排序
    order := "started_at desc"
    if f.OrderBy != "" { order = f.OrderBy }
    qb = qb.Order(goqu.L(order))

    // 分页
    if f.Limit <= 0 { f.Limit = 20 }
    qb = qb.Limit(uint(f.Limit)).Offset(uint(f.Offset))

    // 执行 + count
    ...
}
```

> 也可以不引入 goqu，直接拼接 SQL。这里用 goqu 仅作示例，团队可按习惯选择。

#### 4.9.4 定时清理

```go
func (h *HistoryStore) startPruneLoop(ctx context.Context) {
    ticker := time.NewTicker(24 * time.Hour) // 每天凌晨触发
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            cutoff := time.Now().Add(-h.retention)
            if n, err := h.PruneExpired(cutoff); err != nil {
                log.Printf("history prune: %v", err)
            } else if n > 0 {
                log.Printf("history prune: %d records deleted", n)
            }
        }
    }
}

// SQL：DELETE FROM task_history WHERE starred=0 AND started_at < ?
// 触发 ON DELETE CASCADE 级联删除关联表
```

#### 4.9.5 错误模式

| 场景 | 处理 |
|---|---|
| MySQL 连接断开（运行期） | `db.SetConnMaxLifetime` 自动重连；归档失败时记录 ERROR，不阻塞任务结束 |
| Schema 迁移失败 | Admin 启动失败（fail-fast） |
| 归档幂等冲突（duplicate key） | 视为已归档，跳过 |
| 查询超时 | 返回 500 + log，前端可重试 |

### 4.10 Sampler（运行期时序采集）

```go
type Sampler struct {
    interval     time.Duration
    aggregator   *MetricsAggregator
    history      *HistoryStore
    registry     *AgentRegistry

    mu       sync.Mutex
    current  *samplerJob
}

type samplerJob struct {
    taskID    string
    startedAt time.Time
    cancel    context.CancelFunc
}

func NewSampler(cfg SamplerConfig, agg *MetricsAggregator, hist *HistoryStore, reg *AgentRegistry) *Sampler

// 任务开始时调用
func (s *Sampler) Start(taskID string) error

// 任务结束时调用
func (s *Sampler) Stop(taskID string)
```

**核心循环**：

```go
func (s *Sampler) loop(ctx context.Context, taskID string, startedAt time.Time) {
    ticker := time.NewTicker(s.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done(): return
        case t := <-ticker.C:
            elapsed := int(t.Sub(startedAt).Seconds())

            // 压测快照
            stress := s.aggregator.AggregateStress(taskID)
            stressJSON, _ := json.Marshal(stress)
            _ = s.history.AppendTimeseries(taskID, TimeseriesPoint{
                TaskID: taskID, SampledAt: t, ElapsedSec: elapsed,
                DataType: "stress", Snapshot: stressJSON,
            })

            // 系统快照
            sys := s.aggregator.AggregateSystem()
            sysJSON, _ := json.Marshal(sys)
            _ = s.history.AppendTimeseries(taskID, TimeseriesPoint{
                TaskID: taskID, SampledAt: t, ElapsedSec: elapsed,
                DataType: "system", Snapshot: sysJSON,
            })
        }
    }
}
```

**写入失败处理**：单次失败仅 log warn，不影响下次。MySQL 连续 30s 写不进去就放弃本次任务的时序采样（继续 ticker，但跳过 INSERT，避免 ticker 累积）。

**单例约束的协同**：因为有任务单例，Sampler 同一时刻最多服务 1 个 taskID。`Start(newTaskID)` 时 `current != nil` 直接返回错误（调用方应当先 Stop）。

## 5. HTTP API 完整契约

### 5.1 前端 API

#### 任务

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/tasks` | 列出所有任务 |
| `POST` | `/api/tasks` | 创建任务（multipart：包含 `flow.json` + protos + scripts + 元数据） |
| `GET` | `/api/tasks/{id}` | 任务详情 |
| `POST` | `/api/tasks/{id}/start` | 启动任务（分配 + 推送） |
| `POST` | `/api/tasks/{id}/stop` | 停止任务 |
| `DELETE` | `/api/tasks/{id}` | 删除任务（仅 stopped/failed） |
| `GET` | `/api/tasks/{id}/config/{path...}` | 下载任务配置文件（Agent 也用此 URL） |

#### Agent

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/agents` | 列出所有 Agent（含简要指标） |
| `GET` | `/api/agents/{id}` | Agent 详情（完整 + 当前快照） |
| `DELETE` | `/api/agents/{id}` | 强制注销（仅 offline 状态可删） |

#### 历史

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/history` | 历史任务列表（分页 + 过滤） |
| `GET` | `/api/history/tags` | 全部使用过的 tags（autocomplete） |
| `GET` | `/api/history/{id}` | 历史任务详情（含聚合 finalSnapshot） |
| `PUT` | `/api/history/{id}` | 更新 starred / tags / note |
| `DELETE` | `/api/history/{id}` | 删除（starred=true 时需要 `?force=true`） |
| `GET` | `/api/history/{id}/agents` | 各 Agent 的完成报告 |
| `GET` | `/api/history/{id}/timeseries` | 时序采样数据（趋势图用） |
| `GET` | `/api/history/{id}/config` | 任务配置归档（下载 / 克隆使用） |
| `POST` | `/api/history/{id}/clone` | 用历史配置创建新任务（pending 状态，不立即启动） |
| `GET` | `/api/history/compare?ids=a,b,c` | 多任务对比（最多 5 个） |

#### 指标

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/metrics` | 当前任务的集群压测聚合（与 stressbot 单机 `/metrics` 同 schema） |
| `GET` | `/api/metrics/agents` | 各 Agent 的最新压测快照（数组，per-agent） |
| `GET` | `/api/metrics/agents/{id}` | 指定 Agent 的最新压测快照 |
| `GET` | `/api/metrics/summary` | 文本摘要 |
| `GET` | `/api/system` | 集群系统聚合 |
| `GET` | `/api/system/agents` | 各 Agent 系统快照 |
| `GET` | `/api/system/agents/{id}` | 指定 Agent 系统快照 |

#### 日志

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/logs/admin` | Admin 本地 Ring buffer 日志查询（`?afterSeq=N&limit=M`） |
| `GET` | `/api/logs/agents/{id}` | 代理转发：从指定 Agent 的 Ring buffer 查询日志 |
| `GET` | `/api/logs/admin/files` | 列出 Admin 本地日志文件 |
| `GET` | `/api/logs/admin/files/{name}` | 下载 Admin 指定日志文件 |
| `GET` | `/api/logs/agents/{id}/files` | 代理转发：列出指定 Agent 的日志文件 |
| `GET` | `/api/logs/agents/{id}/files/{name}` | 代理转发：下载指定 Agent 的日志文件 |

> ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程。设计文档保留供参考。

#### 二进制 / 升级

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/binaries` | 上传二进制（multipart：file + version + os + arch） |
| `GET` | `/api/binaries` | 列出所有版本 |
| `GET` | `/api/binaries/{filename}` | 下载二进制（公开，Agent 也用此 URL） |
| `DELETE` | `/api/binaries/{filename}` | 删除版本 |
| `POST` | `/api/agents/{id}/upgrade` | 单点升级 |
| `POST` | `/api/agents/upgrade-all` | 滚动升级 |
| `GET` | `/api/agents/upgrade-status` | 升级进度 |
| `POST` | `/api/agents/upgrade-cancel` | 取消滚动升级（已发出的不撤回） |

#### 静态资源

```go
mux.Handle("/", http.FileServer(http.Dir(cfg.StaticDir)))  // web/dist
```

#### 日志 handler 说明

| Handler | 功能 |
|---|---|
| `handleGetAdminLogs` | 从 `logview.GetRingBuffer()` 查询 Admin 本地日志。支持 `afterSeq`（游标）和 `limit` 参数，返回 `logview.QueryResult`。Ring buffer 未启用时返回空结果 |
| `handleGetAgentLogs` | 代理转发：验证 Agent 存在且非 offline，然后向 `http://{agent.Address}/agent/v1/logs` 发起 GET 请求，将响应透传给前端 |
| `handleListAdminLogFiles` | 根据当前日志文件路径（`stresslog.GetLogFilePath()`）扫描同目录下同前缀的所有日志文件，返回 `[]LogFileInfo`（含 `name`、`size`、`modTime`） |
| `handleDownloadAdminLogFile` | 校验文件名不含路径分隔符或 `..`，以 `text/plain` 附件形式通过 `http.ServeContent` 返回指定日志文件 |
| `handleListAgentLogFiles` | 代理转发：向 `http://{agent.Address}/agent/v1/logs/files` 发起 GET 请求，透传文件列表 |
| `handleDownloadAgentLogFile` | 代理转发：向 `http://{agent.Address}/agent/v1/logs/files/{name}` 发起 GET 请求，透传文件内容（含 Content-Disposition header） |

Agent 日志代理使用 `logsProxyClient`（`http.Client{Timeout: 5s}`），超时或不可达时返回 `AGENT_OFFLINE` 错误。文件下载代理使用独立的 60s 超时 client，以支持大文件传输。

### 5.2 Agent 上行 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/agent/register` | Agent 注册 |
| `POST` | `/api/agent/{id}/heartbeat` | 心跳 |
| `POST` | `/api/agent/{id}/deregister` | 主动注销 |
| `POST` | `/api/agent/stress` | 压测指标上报 |
| `POST` | `/api/agent/system` | 系统指标上报 |
| `POST` | `/api/agent/{id}/task/{tid}/done` | 任务完成上报 |
| `GET` | `/api/agent/{id}/pending-task` | 拉取待执行任务（轮询回退通道） |

### 5.3 详细字段

> 字段定义详见 §3 数据模型。所有 JSON 请求体使用 `application/json`，错误响应统一为：

```json
{
  "code": "TASK_NOT_FOUND",
  "message": "task xxx not found",
  "details": {}
}
```

**HTTP 状态码约定**：

| 场景 | 状态 |
|---|---|
| 成功（含异步接受） | `200`、`201`、`202` |
| 客户端参数错误 | `400` |
| 资源未找到 | `404` |
| 状态冲突（如 task 已 running） | `409` |
| 服务端错误 | `500` |

### 5.4 关键 handler 示例

#### POST /api/tasks（创建任务）

multipart 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 任务名 |
| `totalBots` | int | 总机器人数 |
| `flow.json` | file | 必需 |
| `proto/*` | file（可多个） | 字段名前缀 `proto/` |
| `scripts/*` | file | 字段名前缀 `scripts/` |
| `header.json` | file | 可选 |
| `robotConfig` | string (JSON) | RobotConfig 序列化 |
| `deadline` | string (RFC3339) | 可选 |

```go
func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
    if err := r.ParseMultipartForm(32 << 20); err != nil { /* 400 */ }
    // 解析字段...
    // 校验 flow.json 合法性（用 engine.ValidateFlow）
    // 生成 taskID = uuid
    // 写入 TaskStore
    // 返回 201 + taskID
}
```

#### POST /api/tasks/{id}/start

```go
func (h *Handlers) StartTask(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    if _, ok := h.tasks.Get(id); !ok { writeError(w, ErrTaskNotFound); return }

    // StartTask 内部已处理单例约束 + state transition
    task, err := h.tasks.StartTask(id)
    if err != nil {
        // 可能是 TASK_CONFLICT（已有 active）/ TASK_INVALID_STATE（不在 pending）
        writeError(w, err); return
    }

    agents := h.agents.ListByStatus(AgentIdle)
    assignments, err := h.assigner.Assign(task, agents)
    if err != nil {
        h.tasks.Transition(id, TaskStarting, TaskFailed)
        writeError(w, err); return
    }

    // 启动 Sampler（仅 history.enabled=true 时）
    if h.sampler != nil {
        _ = h.sampler.Start(id)
    }

    // 异步推送
    go h.startTaskBackground(task, assignments)

    w.WriteHeader(202)
    json.NewEncoder(w).Encode(map[string]any{
        "taskId": id, "assignments": assignments,
    })
}

func (h *Handlers) startTaskBackground(task *Task, as []Assignment) {
    var failed []string
    for _, a := range as {
        agent, _ := h.agents.Get(a.AgentID)
        if err := h.dispatcher.AssignTask(agent.Address, a, taskAssignmentDTO(task, a)); err != nil {
            failed = append(failed, a.AgentID)
        }
    }
    if len(failed) > 0 {
        h.tasks.Transition(task.ID, TaskStarting, TaskFailed)
        // 已接受任务的 Agent 反向调用 Stop
        if h.sampler != nil { h.sampler.Stop(task.ID) }
        return
    }
    h.tasks.Transition(task.ID, TaskStarting, TaskRunning)
}
```

**TASK_CONFLICT 错误响应**：

```json
{
  "code": "TASK_CONFLICT",
  "message": "another task is currently active",
  "details": {
    "activeTaskId": "task-abc",
    "activeName": "200v200 压测",
    "activeState": "running",
    "startedAt": "2026-04-29T10:00:00+08:00"
  }
}
```

#### Task 终态归档 hook（在 TaskStore.Transition 完成后异步触发）

```go
func (s *AdminServer) onTaskTerminal(task *Task) {
    // 1. 停止 Sampler
    if s.sampler != nil { s.sampler.Stop(task.ID) }

    // 2. 异步归档（不阻塞响应）
    if s.history == nil { return }
    go func() {
        finalStress := s.aggregator.AggregateStress(task.ID)
        finalSys    := s.aggregator.AggregateSystem()
        cfgArchive  := buildConfigArchive(task)
        if err := s.history.Archive(task, finalStress, finalSys, cfgArchive); err != nil {
            log.Printf("history archive failed: task=%s err=%v", task.ID, err)
        }
    }()
}
```

#### POST /api/history/{id}/clone

```go
func (h *Handlers) CloneHistory(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    cfg, err := h.history.GetConfig(id)
    if err != nil { writeError(w, err); return }

    newID := uuid.NewString()
    task := &Task{
        ID:        newID,
        Name:      cfg.Name + " (clone)",
        State:     TaskPending,
        TotalBots: cfg.TotalBots,
        Config:    cfg.AsTaskConfig(),
        CreatedAt: time.Now(),
    }
    if err := h.tasks.Create(task); err != nil { writeError(w, err); return }

    w.WriteHeader(201)
    json.NewEncoder(w).Encode(map[string]any{"id": newID})
}
```

> 克隆出的任务**不自动启动**，留给用户在前端确认参数后再点 Start。

#### POST /api/agent/register

```go
func (h *Handlers) AgentRegister(w http.ResponseWriter, r *http.Request) {
    var req RegisterRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil { /* 400 */ }

    node := &AgentNode{
        ID:              req.AgentID,
        Name:            req.Name,
        Address:         req.Address,
        AppVersion:      req.AppVersion,
        MaxBots:         req.MaxBots,
        StaticInfo:      req.StaticInfo,
        Status:          AgentIdle,
        LastHeartbeatAt: time.Now(),
    }
    if err := h.agents.Register(node); err != nil { /* 409 */ }

    w.WriteHeader(200)
    json.NewEncoder(w).Encode(RegisterResponse{
        AgentID:        req.AgentID,
        HeartbeatTtl:   "30s",
        StressEndpoint: "/api/agent/stress",
        SystemEndpoint: "/api/agent/system",
    })
}
```

## 6. 任务状态机时序

```
[创建] ─create─→ pending
pending ─start─→ starting ─任意 agent 失败─→ failed
                  └所有 agent 接受─→ running ─stop or deadline─→ stopping
                                              └任意 agent done─→ stopping
                                              └所有 agent done─→ stopped
stopping ─所有 agent done─→ stopped
```

**完成判定**：
- TaskCompletionReport 到达 → 在 task.Reports[agentID] 中记录
- 当 `len(task.Reports) == len(task.Assignments)` → 所有 agent 都已 done → 切到 stopped
- 任意 report.Result == failed → task.ErrorMsg 累加
- 所有 result == completed → 任务自然完成
- 任意 result == stopped → 视为正常停止

## 7. 配置文件 admin-config.json

```json
{
  "listenAddr": ":8080",
  "publicUrl": "http://192.168.1.100:8080",
  "staticDir": "web/dist",
  "dataDir": "data",
  "agentRegistry": {
    "unhealthyAfter": "30s",
    "offlineAfter": "60s"
  },
  "task": {
    "maxFlowSizeMB": 10,
    "deadlineDefault": "1h"
  },
  "history": {
    "enabled": true,
    "mysql": {
      "dsn": "stressbot:password@tcp(127.0.0.1:3306)/stressbot?parseTime=true&charset=utf8mb4&loc=Local",
      "maxOpenConns": 10,
      "maxIdleConns": 5,
      "connMaxLifetime": "1h"
    },
    "samplerInterval": "10s",
    "retentionDays": 90,
    "pruneRunAt": "03:00"
  },
  "log": {
    "level": "info",
    "path": "log/admin.log",
    "maxSizeMB": 100,
    "maxBackups": 10
  }
}
```

| 字段 | 默认 | 说明 |
|---|---|---|
| `listenAddr` | `:8080` | HTTP 监听 |
| `publicUrl` | — | Admin 对外可访问的完整 URL（用于 binary URL 拼接） |
| `staticDir` | `web/dist` | 前端静态资源 |
| `dataDir` | `data` | 任务/二进制持久化根目录 |
| `agentRegistry.unhealthyAfter` | `30s` | 心跳超时阈值 1 |
| `agentRegistry.offlineAfter` | `60s` | 心跳超时阈值 2 |
| `task.maxFlowSizeMB` | `10` | flow.json 上传上限 |
| `history.enabled` | `true` | 历史归档总开关；`false` 时跳过所有 MySQL 调用 |
| `history.mysql.dsn` | — | MySQL DSN（go-sql-driver/mysql 格式） |
| `history.mysql.maxOpenConns` | `10` | 连接池最大连接数 |
| `history.mysql.maxIdleConns` | `5` | 连接池空闲连接数 |
| `history.mysql.connMaxLifetime` | `1h` | 连接最大生命周期（避免 MySQL 主动断连） |
| `history.samplerInterval` | `10s` | 时序采样间隔（任务运行期） |
| `history.retentionDays` | `90` | 历史保留天数；超期且未 starred 的自动清理 |
| `history.pruneRunAt` | `03:00` | 每天清理任务的执行时刻（HH:MM） |

## 8. 持久化方案

### 8.1 任务

```
data/tasks/
  {taskID}.json       // Task 完整 JSON
```

写入时机：每次 `TaskStore.Update()` 完成后。

### 8.2 二进制

```
data/binaries/
  agent-v1.2.0.exe
  agent-v1.2.0.sha256
  agent-v1.2.0-linux-amd64
  agent-v1.2.0-linux-amd64.sha256
  ...
```

启动时扫描，根据文件名解析 metas。

### 8.3 不持久化

- AgentRegistry：所有 Agent 重启后重新注册
- 指标快照：仅内存，前端轮询时取最新
- 升级进度：仅内存（升级中 Admin 重启视为放弃，需运维确认）

## 9. 错误处理与日志

### 9.1 错误码

```go
// admin/errors.go
var (
    ErrTaskNotFound       = NewError("TASK_NOT_FOUND", 404)
    ErrTaskConflict       = NewError("TASK_CONFLICT", 409)       // 任务单例约束
    ErrTaskInvalidState   = NewError("TASK_INVALID_STATE", 409)
    ErrAgentNotFound      = NewError("AGENT_NOT_FOUND", 404)
    ErrAgentBusy          = NewError("AGENT_BUSY", 409)
    ErrAgentOffline       = NewError("AGENT_OFFLINE", 409)
    ErrCapacityExceeded   = NewError("CAPACITY_EXCEEDED", 400)
    ErrInvalidArgument    = NewError("INVALID_ARGUMENT", 400)
    ErrHistoryDisabled    = NewError("HISTORY_DISABLED", 503)    // 历史模块未启用
    ErrHistoryNotFound    = NewError("HISTORY_NOT_FOUND", 404)
    ErrStarredProtected   = NewError("HISTORY_STARRED", 409)     // 阻止删除已标星记录
)

type Error struct {
    Code       string
    HTTPStatus int
    Message    string
    Details    map[string]any
}
```

### 9.2 日志

使用 zap（与 server 项目对齐）或 stdlib log（更简单）。日志格式：

```
2026-04-29T10:30:00 INFO  agent-registered agentId=xxx name=agent-gz-01 addr=:7070
2026-04-29T10:30:05 INFO  task-started taskId=task-01 agents=3 totalBots=3000
2026-04-29T10:30:30 WARN  agent-unhealthy agentId=xxx lag=32s
2026-04-29T10:31:00 ERROR upgrade-failed agentId=xxx version=v1.2.0 err="connection refused"
```

## 10. 实施分阶段计划

| Phase | 内容 | 工时 |
|---|---|---|
| 1 | `admin/types.go`、`config.go`、`errors.go`：DTO 与配置 | 0.5 天 |
| 2 | `admin/persist.go` + `task.go`：TaskStore + 状态机 + 文件持久化 | 1 天 |
| 3 | `admin/agent.go`：AgentRegistry + 心跳超时检测 | 1 天 |
| 4 | `admin/handlers.go`（Agent 上行）：register / heartbeat / stress / system / done / pending-task | 1 天 |
| 5 | `admin/aggregator.go`：压测/系统聚合 | 1.5 天 |
| 6 | `admin/handlers.go`（前端读取）：tasks / agents / metrics / system | 0.75 天 |
| 7 | `admin/assignment.go` + `agent_dispatcher.go`：分配 + 推送任务 | 0.75 天 |
| 8 | `admin/handlers.go`（前端写入）：CreateTask / Start / Stop | 0.75 天 |
| 9 | `admin/binary.go`：BinaryStore + multipart 上传 + 下载 | 0.75 天 |
| 10 | `admin/upgrade.go`：UpgradeOrchestrator + 滚动 | 1 天 | ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程 |
| 11 | `admin/admin.go`：AdminServer 总装 + 路由 + 静态资源托管 | 0.5 天 |
| 12 | `cmd/admin/main.go`：入口 + 信号处理 + graceful shutdown | 0.25 天 |
| 13 | `admin/history.go` + `history_schema.go`：MySQL 表 + CRUD + 标签筛选 | 1 天 |
| 14 | `admin/sampler.go`：运行期定时采样 + 写入时序表 | 0.25 天 |
| 15 | `admin/handlers_history.go`：所有 `/api/history/*` handler（含 clone/compare） | 0.75 天 |
| 16 | `admin/admin.go` 集成：终态归档 hook + 启动 Sampler / 清理协程 | 0.25 天 |
| 17 | 单元测试 + httptest 集成测试（含 dockertest 跑 MySQL） | 2 天 |
| 18 | 与 Agent 联调（mock + 真 Agent） | 1 天 |

**总计：约 14.5 天**。

## 11. 单元测试清单

| 文件 | 关键测试 |
|---|---|
| `task_test.go` | TestStateMachineValid、TestStateMachineInvalid、TestPersistRoundtrip、TestDeleteTerminal、**TestSingletonStartConflict**（并发 start 仅一个成功）、**TestActiveCleanupOnTerminal**（终态时 activeID 释放）、**TestRecoveryDropsActive**（重启后 active 任务变 failed） |
| `agent_test.go` | TestRegisterDuplicate、TestHeartbeatUpdates、TestUnhealthyOffline、TestForceDelete |
| `aggregator_test.go` | TestSumCounters、TestMergeBuckets、TestRecomputePercentiles、TestApdexAggregation、TestErrorMerge |
| `assignment_test.go` | TestUniformSplit、TestRemainderDistribution、TestStartNumberConsecutive、TestCapacityCheck |
| `upgrade_test.go` | TestRolloutHappy、TestRolloutPartialFailure、TestPerAgentTimeout、TestCancel |
| `binary_test.go` | TestUploadAtomic、TestSHA256Sidecar、TestForceOverwrite、TestFilenameValidation |
| `handlers_test.go` | TestCreateTaskMultipart、TestStartTaskAssigns、TestStopTaskTriggersDispatcher、TestAgentRegister200、**TestStartTaskConflict409** |
| `history_test.go` | TestArchiveTransactional、TestArchiveIdempotent、TestListFilterByTag、TestListFilterByStarred、TestListSearchNote、TestUpdateMeta、TestDeleteStarredRequiresForce、TestPruneRespectsStarred、TestCloneCreatesPending |
| `sampler_test.go` | TestSamplerStartStop、TestSamplerWritesTimeseries、TestSamplerSurvivesTransientDBError |

## 12. 验收标准

### 12.1 基础功能

- [ ] `go build ./cmd/admin` 无编译错误
- [ ] 配置 `admin-config.json` 可正常解析
- [ ] 启动后能托管 `web/dist`，浏览器访问 `:8080` 看到前端
- [ ] Agent 注册 → 心跳 → 上报 → 注销 全流程正常
- [ ] 任务创建 → 启动（多 Agent）→ 运行 → 停止 → 完成 全流程正常
- [ ] 任务持久化：创建后重启 Admin，未启动任务可恢复（pending）；运行中任务恢复为 failed 且 errorMsg 明确
- [ ] 二进制上传 / 下载（含 SHA256 校验）正常
- [ ] 滚动升级：3 个 mock Agent，全部成功升级；模拟 1 个失败，最终状态 Failed=1 Completed=2 — ⚠️ **已废弃**：自动升级流程已废弃，升级改为运维手动重启 Agent 进程
- [ ] 聚合指标：3 个 Agent 各跑 100 机器人，`/api/metrics` 显示 success=300、P99 合理（不是平均值）

### 12.2 任务单例

- [ ] 创建 3 个 pending 任务全部成功（pending 不限制数量）
- [ ] 启动 1 个任务后，对其余 pending 任务调用 start 返回 `409 TASK_CONFLICT`，details 含 activeTaskId / activeName / activeState
- [ ] 第 1 个任务完成后，第 2 个任务可成功 start
- [ ] 用 `go test -race -run TestSingletonStartConflict -count=100` 验证并发安全
- [ ] Admin 重启时存在 starting/running/stopping 任务 → 启动后该任务变为 failed 且 activeID 清空

### 12.3 历史压测

- [ ] MySQL 不可达时 `history.enabled=true` 启动失败（fail-fast）
- [ ] `history.enabled=false` 时 admin 正常启动且不连 MySQL
- [ ] 跑完 1 个任务后 5 张表均有完整数据：`task_history` / `task_assignment` / `task_report` / `task_aggregated` / `task_config_archive`
- [ ] 时序表数据点数 = 任务时长 / samplerInterval（容差 ±1）
- [ ] `GET /api/history` 显示终态任务，分页 / 过滤 / 排序均生效
- [ ] `PUT /api/history/{id}` 设置 starred=true 后 DELETE 返回 409，加 `?force=true` 后可删除
- [ ] tags 多值过滤：`?tags=a&tags=b` (OR) 与 `?tagsAll=a&tagsAll=b` (AND) 行为符合预期
- [ ] `POST /api/history/{id}/clone` 创建一个新 pending 任务，配置完整一致
- [ ] `GET /api/history/compare?ids=a,b,c` 返回多任务对比数据（ids 数量 > 5 返回 400）
- [ ] 90 天清理：未 starred 的过期记录被删除（含级联删除关联表），starred 永不删
- [ ] 单元测试覆盖率 ≥ 70%（含 dockertest 跑 MySQL）
- [ ] httptest 集成测试覆盖所有 Agent 上行 API 与所有前端关键 API（含 history 系列）

## 13. 与其他模块的协议契约（强约束）

### 13.1 Admin 必须遵守

1. 所有暴露给 Agent 的接口（`/api/agent/*`、`/api/binaries/{file}`、`/api/tasks/{id}/config/*`）**禁止做用户认证**（Agent 不携带凭证）
2. `RegisterResponse.HeartbeatTtl` 必须等于 `agentRegistry.unhealthyAfter`
3. 任务下发 `TaskAssignment.ConfigFiles[].URL` 必须可被 Agent 直接 GET（不带任何 header）
4. `BinaryURL` 拼接：`{publicUrl}/api/binaries/{filename}`，`publicUrl` 必须配置正确否则 Agent 无法下载

### 13.2 Admin 假定 Agent 提供

1. 监听 `/agent/v1/{task,stop,version,status,logs,logs/files,logs/files/,healthz}` 全部端点
2. 任务下发后异步执行，202 Accepted 立即返回
3. 升级请求接受后通过 `/api/agent/{id}/task/{tid}/done` 上报中断（result=stopped）

### 13.3 跨模块字段对齐

| 来源 | 对齐对象 | 备注 |
|---|---|---|
| `monitor.CollectorSnapshot` | Agent stress 上报 + Admin 聚合 | 字段必须完全一致 |
| `SystemSnapshot` | Agent system 上报 + Admin 聚合 | 同上 |
| `TaskAssignment` | Admin 下发 + Agent 接收 | 在 `agent/types.go` 与 `admin/types.go` 镜像 |
| `RegisterRequest/Response` | 双向同步 | 同上 |

任何一方修改字段必须同时改另一方，CI 可加入 schema diff 检查。
