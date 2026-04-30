package agent

import (
	"encoding/json"
	"time"

	"stressbot/monitor"
)

// AgentStatus Agent 运行状态。
type AgentStatus string

const (
	StatusIdle      AgentStatus = "idle"
	StatusBusy      AgentStatus = "busy"
	StatusDraining  AgentStatus = "draining"  // 正在 drain，不接受新任务
	StatusUpgrading AgentStatus = "upgrading"
)

// TaskResult 任务完成结果。
type TaskResult string

const (
	TaskCompleted TaskResult = "completed"
	TaskStopped   TaskResult = "stopped"
	TaskFailed    TaskResult = "failed"
)

// --- Agent → Admin 请求 ---

// RegisterRequest 注册请求。
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

// RegisterResponse 注册响应。
type RegisterResponse struct {
	AgentID         string `json:"agentId"`
	HeartbeatTTL    string `json:"heartbeatTtl"`
	StressEndpoint  string `json:"stressEndpoint"`
	SystemEndpoint  string `json:"systemEndpoint"`
}

// HeartbeatRequest 心跳请求。
type HeartbeatRequest struct {
	AgentID       string `json:"agentId"`
	Timestamp     string `json:"timestamp"`
	Status        string `json:"status"`        // idle | busy
	CurrentTaskID string `json:"currentTaskId"` // status=busy 时存在
	CurrentBots   int    `json:"currentBots"`
	AppVersion    string `json:"appVersion"`
}

// StressReport 压测指标上报。
type StressReport struct {
	AgentID    string                    `json:"agentId"`
	TaskID     string                    `json:"taskId"`
	ReportedAt time.Time                 `json:"reportedAt"`
	Snapshot   *monitor.CollectorSnapshot `json:"snapshot"`
}

// SystemReport 系统指标上报。
type SystemReport struct {
	AgentID    string         `json:"agentId"`
	ReportedAt time.Time      `json:"reportedAt"`
	Snapshot   SystemSnapshot `json:"snapshot"`
}

// TaskCompletionReport 任务完成报告。
type TaskCompletionReport struct {
	AgentID       string                    `json:"agentId"`
	TaskID        string                    `json:"taskId"`
	Result        TaskResult                `json:"result"`
	ErrorMsg      string                    `json:"errorMsg,omitempty"`
	FinishedAt    time.Time                 `json:"finishedAt"`
	FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
}

// DeregisterRequest 注销请求（best-effort）。
type DeregisterRequest struct {
	AgentID string `json:"agentId"`
}

// --- Admin → Agent 请求 ---

// TaskAssignment 任务下发（Admin Push 到 Agent）。
type TaskAssignment struct {
	TaskID      string `json:"taskId"`
	Name        string `json:"name"`
	TotalBots   int    `json:"totalBots"`
	StartNumber int    `json:"startNumber"`

	// 配置拉取
	ConfigBase string         `json:"configBase"` // 基础 URL
	ConfigFiles []ConfigFileRef `json:"configFiles"`

	// 机器人参数
	RobotConfig RobotConfig `json:"robotConfig"`

	// 截止时间（可选，超时自动停止）
	Deadline *time.Time `json:"deadline,omitempty"`
}

// ConfigFileRef 配置文件引用。
type ConfigFileRef struct {
	Path   string `json:"path"`   // 相对路径，如 "flow.json"、"proto/c2s.proto"
	URL    string `json:"url"`    // 完整下载 URL
	SHA256 string `json:"sha256"` // 校验哈希（空表示不校验）
}

// RobotConfig 机器人运行参数。
type RobotConfig struct {
	AccountPrefix string            `json:"accountPrefix"`
	AuthAddr      string            `json:"authAddr"`
	AuthExtra     map[string]string `json:"authExtra"`
	MainService   string            `json:"mainService"`
	Concurrency   int               `json:"concurrency"`
	TimeoutSec    int               `json:"timeoutSec"`
	ApdexT        int               `json:"apdexT"`
}

// UpgradeRequest 升级请求（Admin Push 到 Agent）。
type UpgradeRequest struct {
	URL     string `json:"url"`     // 新版本下载地址
	SHA256  string `json:"sha256"`  // SHA256 校验
	Version string `json:"version"` // 新版本号
}

// AgentStatusResponse Agent 状态查询响应。
type AgentStatusResponse struct {
	AgentID       string `json:"agentId"`
	Status        string `json:"status"`
	CurrentTaskID string `json:"currentTaskId,omitempty"`
	AppVersion    string `json:"appVersion"`
	Uptime        string `json:"uptime"`
}

// --- 系统监控 ---

// SystemSnapshot 系统指标快照。
type SystemSnapshot struct {
	Timestamp time.Time `json:"timestamp"`

	// CPU
	CPUPercent float64   `json:"cpuPercent"`
	CPUPerCore []float64 `json:"cpuPerCore"`
	LoadAvg1   float64   `json:"loadAvg1"`
	LoadAvg5   float64   `json:"loadAvg5"`
	LoadAvg15  float64   `json:"loadAvg15"`

	// 内存
	MemTotalMB uint64  `json:"memTotalMB"`
	MemUsedMB  uint64  `json:"memUsedMB"`
	MemPercent float64 `json:"memPercent"`
	SwapUsedMB uint64  `json:"swapUsedMB"`

	// 进程
	ProcessRssMB  uint64 `json:"processRssMB"`
	ProcessHeapMB uint64 `json:"processHeapMB"`
	ProcessSysMB  uint64 `json:"processSysMB"`
	NumGoroutine  int    `json:"numGoroutine"`
	NumThread     int32  `json:"numThread"`
	NumFD         int32  `json:"numFd"`

	// 网络速率（差分计算）
	NetSendKBps float64 `json:"netSendKBps"`
	NetRecvKBps float64 `json:"netRecvKBps"`

	// GC
	GCCount      uint32  `json:"gcCount"`
	GCPauseAvgMs float64 `json:"gcPauseAvgMs"`
}

// StaticInfo 启动时一次性采集的静态信息。
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

// --- 通用 ---

// ErrorResponse Admin 错误响应。
type ErrorResponse struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}
