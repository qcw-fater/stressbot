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
// 超时字段统一用 int 秒数，admin 转为 "Ns" duration 字符串下发。
type RobotConfig struct {
	AuthAddr    string `json:"authAddr"`
	Concurrency int    `json:"concurrency"`
	TimeoutSec  int    `json:"timeoutSec"`

	AccountPrefix string            `json:"accountPrefix,omitempty"`
	StartNumber   int               `json:"startNumber,omitempty"`
	MainService   string            `json:"mainService,omitempty"`
	AuthExtra     map[string]string `json:"authExtra,omitempty"`

	HeartbeatSec   int `json:"heartbeatSec,omitempty"`
	HTTPTimeoutSec int `json:"httpTimeoutSec,omitempty"`
	ApdexT         int `json:"apdexT,omitempty"`

	DebugMode bool   `json:"debugMode,omitempty"`
	LogLevel  string `json:"logLevel,omitempty"`
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
	LogLevel          string            `json:"logLevel,omitempty"`
	ConfigURL         string            `json:"configUrl"`
	ConfigFiles       []string          `json:"configFiles"`
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

// ClusterSystemSnapshot 集群系统资源聚合快照。
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
