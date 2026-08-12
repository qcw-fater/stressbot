package agent

import (
	"fmt"
	"time"

	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/sharedstate"
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

// TaskCompletionReport 任务完成报告。
type TaskCompletionReport struct {
	AgentID       string                     `json:"agentId"`
	TaskID        string                     `json:"taskId"`
	Result        TaskResult                 `json:"result"`
	ErrorMsg      string                     `json:"errorMsg,omitempty"`
	FinishedAt    time.Time                  `json:"finishedAt"`
	FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
	// StageIndex 阶段段落标识：-1/0=最终（兼容）报告；>0=reset 边界阶段段落报告，
	// 值为即将进入的配置阶段下标（0-based），由 Admin 归档时映射为连续 1-based 段落号。
	StageIndex    int                  `json:"stageIndex,omitempty"`
	CleanupStatus *robot.CleanupStatus `json:"cleanupStatus,omitempty"`
}

// --- Admin → Agent 请求 ---

// TaskAssignment 任务下发（Admin Push 到 Agent）。
type TaskAssignment struct {
	TaskID            string            `json:"taskId"`
	TaskName          string            `json:"taskName"`
	StartNumber       int               `json:"startNumber"` // 任务账号编号基数，所有 Agent 相同
	StartIndex        *int              `json:"startIndex"`  // 本 Agent 的任务全局机器人序号起点（0-based）
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
	LogLevel     string        `json:"logLevel,omitempty"`
	BundleDigest []byte        `json:"-"`
	BundleSize   int64         `json:"-"`
	BundleDir    string        `json:"-"`
	RampUp       *RampUpConfig `json:"rampUp,omitempty"`
	// Shared 共享状态运行时下发（含已解析的 Redis 连接信息与任务 runId）。
	// 仅当 Admin 检测到脚本使用 share 模块且服务器配置了 Redis 时才下发，否则为 nil。
	Shared *SharedRuntimeAssignment `json:"shared,omitempty"`
}

// Validate 校验 Admin 下发的任务分配契约。
func (a TaskAssignment) Validate() error {
	if a.StartIndex == nil {
		return fmt.Errorf("任务分配缺少必填字段 startIndex")
	}
	if *a.StartIndex < 0 {
		return fmt.Errorf("任务分配 startIndex 不能为负数（当前 %d）", *a.StartIndex)
	}
	return nil
}

// SharedRuntimeAssignment Admin → Agent 下发的共享状态运行时配置。
// RunID 由 Admin 统一生成（同一任务所有 Agent 一致），保证落在同一命名空间。
type SharedRuntimeAssignment struct {
	RunID string                  `json:"runId"`
	Redis sharedstate.RedisConfig `json:"redis"`
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

// --- 系统监控 ---

// SystemSnapshot 系统指标快照。
type SystemSnapshot struct {
	Timestamp time.Time `json:"timestamp"`
	Sequence  uint64    `json:"sequence"`

	// 主机指标描述整台机器。nil 表示本轮采集失败，或差分指标尚未建立基线。
	HostCPUPercent         *float64 `json:"hostCpuPercent"`
	HostMemTotalBytes      *uint64  `json:"hostMemTotalBytes"`
	HostMemUsedBytes       *uint64  `json:"hostMemUsedBytes"`
	HostMemPercent         *float64 `json:"hostMemPercent"`
	HostNetSendBytesPerSec *float64 `json:"hostNetSendBytesPerSec"`
	HostNetRecvBytesPerSec *float64 `json:"hostNetRecvBytesPerSec"`

	// ProcessCPUPercent 归一化为整台主机容量（0~100），不是按单核累计的 top 口径。
	ProcessCPUPercent *float64 `json:"processCpuPercent"`
	ProcessRSSBytes   *uint64  `json:"processRssBytes"`
	ProcessHeapBytes  uint64   `json:"processHeapBytes"`
	ProcessGoroutines int      `json:"processGoroutines"`
	ProcessThreads    *int32   `json:"processThreads"`
	// Windows 下 gopsutil 的 NumFDs 返回当前进程句柄数。
	ProcessFDs *int32 `json:"processFds"`
}

// StaticInfo 启动时一次性采集的静态信息。
type StaticInfo struct {
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	NumCPU        int       `json:"numCpu"`
	MemTotalBytes uint64    `json:"memTotalBytes"`
	GoVersion     string    `json:"goVersion"`
	KernelVer     string    `json:"kernelVer"`
	StartedAt     time.Time `json:"startedAt"`
}

// --- 通用 ---
