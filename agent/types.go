package agent

import (
	"encoding/json"
	"time"

	"stressbot/monitor"
)

// AgentStatus Agent 运行状态。
type AgentStatus string

const (
	StatusIdle AgentStatus = "idle"
	StatusBusy AgentStatus = "busy"
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
	AgentID        string `json:"agentId"`
	HeartbeatTTL   string `json:"heartbeatTtl"`
	StressEndpoint string `json:"stressEndpoint"`
	SystemEndpoint string `json:"systemEndpoint"`
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
	AgentID    string                     `json:"agentId"`
	TaskID     string                     `json:"taskId"`
	ReportedAt time.Time                  `json:"reportedAt"`
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
	AgentID       string                     `json:"agentId"`
	TaskID        string                     `json:"taskId"`
	Result        TaskResult                 `json:"result"`
	ErrorMsg      string                     `json:"errorMsg,omitempty"`
	FinishedAt    time.Time                  `json:"finishedAt"`
	FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
	StageIndex    int                        `json:"stageIndex,omitempty"`
}

// DeregisterRequest 注销请求（best-effort）。
type DeregisterRequest struct {
	AgentID string `json:"agentId"`
}

// --- Admin → Agent 请求 ---

// TaskAssignment 任务下发（Admin Push 到 Agent）。
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
	// LogLevel 可选值 debug/info/warn/error。
	// 任务执行期间临时切换 Agent 进程日志等级，结束后自动恢复。
	// 空字符串 = 沿用 Agent 启动时 agent-config.json 中的等级。
	LogLevel    string        `json:"logLevel,omitempty"`
	ConfigURL   string        `json:"configUrl"`
	ConfigFiles []string      `json:"configFiles"`
	RampUp      *RampUpConfig `json:"rampUp,omitempty"`
}

// RampUpConfig 渐进式加压配置。
type RampUpConfig struct {
	Stages []RampUpStage `json:"stages"`
}

// RampUpStage 单个加压阶段。
type RampUpStage struct {
	Count       int  `json:"count"`
	Concurrency int  `json:"concurrency,omitempty"`
	HoldSec     int  `json:"holdSec,omitempty"`
	Reset       bool `json:"reset,omitempty"`
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
