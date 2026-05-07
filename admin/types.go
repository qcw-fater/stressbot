package admin

import (
	"encoding/json"
	"time"

	"stressbot/monitor"
)

// ── Task ──────────────────────────────────────────────

type TaskState string

const (
	TaskPending  TaskState = "pending"
	TaskStarting TaskState = "starting"
	TaskRunning  TaskState = "running"
	TaskStopping TaskState = "stopping"
	TaskStopped  TaskState = "stopped"
	TaskFailed   TaskState = "failed"
)

// IsActiveState 返回该状态是否占据单例位。
func IsActiveState(s TaskState) bool {
	return s == TaskStarting || s == TaskRunning || s == TaskStopping
}

type TaskResult string

const (
	ResultCompleted TaskResult = "completed"
	ResultStopped   TaskResult = "stopped"
	ResultFailed    TaskResult = "failed"
)

type Task struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name"`
	State       TaskState                    `json:"state"`
	TotalBots   int                          `json:"totalBots"`
	Config      TaskConfig                   `json:"config"`
	Assignments []Assignment                 `json:"assignments,omitempty"`
	Reports     map[string]TaskCompletionReport `json:"reports,omitempty"`
	CreatedAt   time.Time                    `json:"createdAt"`
	StartedAt   *time.Time                   `json:"startedAt,omitempty"`
	StoppedAt   *time.Time                   `json:"stoppedAt,omitempty"`
	ErrorMsg    string                       `json:"errorMsg,omitempty"`
}

type TaskConfig struct {
	FlowJSON    json.RawMessage   `json:"flowJson"`
	ProtoFiles  map[string][]byte `json:"protoFiles,omitempty"`
	LuaScripts  map[string][]byte `json:"luaScripts,omitempty"`
	HeaderJSON  json.RawMessage   `json:"headerJson,omitempty"`
	RobotConfig RobotConfig       `json:"robotConfig"`
	Deadline    *time.Time        `json:"deadline,omitempty"`
}

// RobotConfig 任务级运行时配置（前端 → admin → agent）。
//
// 字段分层语义：
//   - 必填（authAddr / concurrency / timeoutSec）：每个任务一定要的核心参数；
//   - 可选（其余）：留空时由 admin 用合理默认值填充，再下发到 agent。
//
// 设计取舍：超时类字段统一用 int 秒数（accountPrefix/mainService 等用 string），
// 让前端表单足够直观；admin 在转换为 TaskAssignment 时把 int 秒 → "Ns" duration 字符串。
type RobotConfig struct {
	// ── 必填 ──
	AuthAddr    string `json:"authAddr"`
	Concurrency int    `json:"concurrency"`
	TimeoutSec  int    `json:"timeoutSec"` // 兼容旧字段：作为 TCPTimeout 兜底

	// ── 业务可变（前端可改，影响每个任务的鉴权/连接行为）──

	// AccountPrefix 账号前缀，用于区分压测批次，留空 = "bot_"。
	AccountPrefix string `json:"accountPrefix,omitempty"`
	// StartNumber 账号编号起点；admin 在分配时把它作为各 agent cursor 的起点，
	// 即第 N 个机器人的账号 = AccountPrefix + (StartNumber + N)。
	// 用法场景：
	//   - 已有 bot_0~bot_99 在线时另起任务，可以设 100 避免账号撞车；
	//   - 不同业务线想用不同账号区段。
	// 留空（0）= 从 0 开始。
	StartNumber int `json:"startNumber,omitempty"`
	// MainService 主连接服务名，留空 = "logic"。
	MainService string `json:"mainService,omitempty"`
	// AuthExtra Auth 请求时附带的扩展字段（version/channel/platform 等），
	// 在 lua 脚本中可通过 robot.get("version") 取到。
	// 必须配置，否则 lua 取到 nil 兜底默认值，可能导致鉴权失败。
	AuthExtra map[string]string `json:"authExtra,omitempty"`

	// ── 性能/超时（前端可改，但通常用默认值）──

	HeartbeatSec   int `json:"heartbeatSec,omitempty"`   // 心跳间隔秒，默认 5
	HTTPTimeoutSec int `json:"httpTimeoutSec,omitempty"` // HTTP 超时秒，默认 10
	ApdexT         int `json:"apdexT,omitempty"`         // Apdex 满意阈值毫秒，默认 100
	// 注：reportInterval / monitor 上报频率属于 agent 进程级配置（跨任务），
	//     不在 RobotConfig 里暴露；如需调整请改 agent-config.json 后重启 agent。

	// ── 日志 ──

	// LogLevel 任务期临时切换 Agent 进程日志等级（debug/info/warn/error）。
	// 空 = 沿用 Agent 启动配置；任务结束自动恢复。
	LogLevel string `json:"logLevel,omitempty"`
}

// ── Assignment ────────────────────────────────────────

type Assignment struct {
	TaskID      string `json:"taskId"`
	AgentID     string `json:"agentId"`
	AgentName   string `json:"agentName"`
	StartNumber int    `json:"startNumber"`
	TotalBots   int    `json:"totalBots"`
}

// TaskAssignment Admin → Agent 下发的任务分配。
type TaskAssignment struct {
	TaskID            string            `json:"taskId"`
	TaskName          string            `json:"taskName"`
	StartNumber       int               `json:"startNumber"`
	TotalBots         int               `json:"totalBots"`
	AccountPrefix     string            `json:"accountPrefix"`
	ConcurrentNum     int               `json:"concurrentNum"`
	MainService       string            `json:"mainService"`
	AuthAddress       string            `json:"authAddress"`
	AuthExtra         map[string]string `json:"authExtra"`
	HeartbeatInterval string            `json:"heartbeatInterval"`
	TCPTimeout        string            `json:"tcpTimeout"`
	HTTPTimeout       string            `json:"httpTimeout"`
	ApdexT            int               `json:"apdexT"`
	LogLevel          string            `json:"logLevel,omitempty"` // 任务期临时切换 Agent 日志等级；空 = 沿用 Agent 启动配置
	ConfigURL         string            `json:"configUrl"`
	ConfigFiles       []string          `json:"configFiles"` // 可下载的配置文件列表，如 ["flow.json", "proto/a.proto"]
}

// ── Agent ─────────────────────────────────────────────

type AgentStatus string

const (
	AgentIdle      AgentStatus = "idle"
	AgentBusy      AgentStatus = "busy"
	AgentUnhealthy AgentStatus = "unhealthy"
	AgentOffline   AgentStatus = "offline"
)

type AgentNode struct {
	ID             string        `json:"agentId"`
	Name           string        `json:"name"`
	Address        string        `json:"address"`
	AppVersion     string        `json:"appVersion"`
	MaxBots        int           `json:"maxBots"`
	StressInterval string        `json:"stressInterval"`
	SystemInterval string        `json:"systemInterval"`
	StaticInfo     StaticInfo    `json:"staticInfo"`

	Status         AgentStatus   `json:"status"`
	LastHeartbeatAt time.Time    `json:"lastHeartbeatAt"`
	CurrentTaskID  string        `json:"currentTaskId,omitempty"`
	CurrentBots    int           `json:"currentBots"`

	LatestStress     *monitor.CollectorSnapshot `json:"-"`
	LatestSystem     *SystemSnapshot            `json:"-"`
	StressUpdatedAt  time.Time                  `json:"stressUpdatedAt,omitempty"`
	SystemUpdatedAt  time.Time                  `json:"systemUpdatedAt,omitempty"`
}

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

// ── 上报报文 ──────────────────────────────────────────

type StressReport struct {
	AgentID    string                    `json:"agentId"`
	TaskID     string                    `json:"taskId"`
	ReportedAt time.Time                 `json:"reportedAt"`
	Snapshot   monitor.CollectorSnapshot `json:"snapshot"`
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
	FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
}

// ── 注册 / 心跳 ──────────────────────────────────────

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

// ── 系统指标（Agent 上报）─────────────────────────────

type SystemSnapshot struct {
	Timestamp time.Time `json:"timestamp"`

	CPUPercent float64   `json:"cpuPercent"`
	CPUPerCore []float64 `json:"cpuPerCore,omitempty"`
	LoadAvg1   float64   `json:"loadAvg1,omitempty"`
	LoadAvg5   float64   `json:"loadAvg5,omitempty"`
	LoadAvg15  float64   `json:"loadAvg15,omitempty"`

	MemTotalMB    uint64  `json:"memTotalMB"`
	MemUsedMB     uint64  `json:"memUsedMB"`
	MemPercent    float64 `json:"memPercent"`
	SwapUsedMB    uint64  `json:"swapUsedMB,omitempty"`
	ProcessRssMB  uint64  `json:"processRssMB"`
	ProcessHeapMB uint64  `json:"processHeapMB"`
	ProcessSysMB  uint64  `json:"processSysMB"`

	NumGoroutine int    `json:"numGoroutine"`
	NumThread    int32  `json:"numThread"`
	NumFD        int32  `json:"numFd,omitempty"`
	GCCount      uint32 `json:"gcCount"`
	GCPauseAvgMs float64 `json:"gcPauseAvgMs,omitempty"`

	NetSendKBps float64 `json:"netSendKBps"`
	NetRecvKBps float64 `json:"netRecvKBps"`
}

// ── 聚合快照 ─────────────────────────────────────────

// ClusterSystemSnapshot 集群系统资源快照（聚合）。
//
// 字段命名约定：聚合字段统一带 `total*` 前缀，与单 Agent 上报字段
// (NumGoroutine / NetSendKBps) 区分；这与 docs/api-monitor.md §8 与
// 前端 `ClusterSystemSnapshot` 类型保持一致。
type ClusterSystemSnapshot struct {
	Timestamp        time.Time          `json:"timestamp"`
	AgentCount       int                `json:"agentCount"`
	OnlineCount      int                `json:"onlineCount"`
	OfflineCount     int                `json:"offlineCount"`
	AvgCPUPercent    float64            `json:"avgCpuPercent"`
	MaxCPUPercent    float64            `json:"maxCpuPercent"`
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

// ── 历史归档 ─────────────────────────────────────────

type HistoryRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	State       TaskState `json:"state"`
	TotalBots   int       `json:"totalBots"`
	AgentCount  int       `json:"agentCount"`
	CreatedAt   time.Time `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	StoppedAt   *time.Time `json:"stoppedAt,omitempty"`
	DurationSec int       `json:"durationSec"`
	ErrorMsg    string    `json:"errorMsg,omitempty"`

	Starred bool     `json:"starred"`
	Tags    []string `json:"tags"`
	Note    string   `json:"note"`

	ConfigSummary ConfigSummary `json:"configSummary"`
}

type ConfigSummary struct {
	AuthAddr    string `json:"authAddr"`
	Concurrency int    `json:"concurrency"`
	TimeoutSec  int    `json:"timeoutSec"`
	FlowSizeKB  int    `json:"flowSizeKB"`
	ProtoCount  int    `json:"protoCount"`
	ScriptCount int    `json:"scriptCount"`
}

type HistoryDetail struct {
	HistoryRecord
	Assignments   []Assignment              `json:"assignments"`
	AgentReports  []HistoryAgentReport      `json:"agentReports"`
	FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
	FinalSystem   ClusterSystemSnapshot     `json:"finalSystem"`
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
	State         string
	StartedAfter  time.Time
	StartedBefore time.Time
	Tags          []string
	TagsAll       []string
	Starred       *bool
	Search        string
	Limit         int
	Offset        int
	OrderBy       string
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
	Tasks []HistoryDetail            `json:"tasks"`
	Diff  CompareDiff                `json:"diff"`
}

type CompareDiff struct {
	Actions map[string][]float64 `json:"actions"` // actionName → [taskA_p99, taskB_p99, ...]
}

// ── 时序采样 ─────────────────────────────────────────

type TimeseriesPoint struct {
	TaskID     string          `json:"taskId"`
	SampledAt  time.Time       `json:"sampledAt"`
	ElapsedSec int             `json:"elapsedSec"`
	DataType   string          `json:"dataType"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

type TimeseriesResponse struct {
	TaskID string            `json:"taskId"`
	Stress []TimeseriesPoint `json:"stress"`
	System []TimeseriesPoint `json:"system"`
}
