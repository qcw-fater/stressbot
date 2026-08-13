package task

import (
	"fmt"
	"time"

	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/state/shared"
)

// TaskResult 是 Agent 任务的终态。
type TaskResult string

const (
	TaskCompleted TaskResult = "completed"
	TaskStopped   TaskResult = "stopped"
	TaskFailed    TaskResult = "failed"
)

// TaskCompletionReport 是 Agent 向 Admin 确认的任务最终报告。
type TaskCompletionReport struct {
	AgentID       string                     `json:"agentId"`
	TaskID        string                     `json:"taskId"`
	Result        TaskResult                 `json:"result"`
	ErrorMsg      string                     `json:"errorMsg,omitempty"`
	FinishedAt    time.Time                  `json:"finishedAt"`
	FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
	StageIndex    int                        `json:"stageIndex,omitempty"`
	CleanupStatus *robot.CleanupStatus       `json:"cleanupStatus,omitempty"`
}

// TaskAssignment 是 Admin 下发给单个 Agent 的任务分片。
type TaskAssignment struct {
	TaskID            string                   `json:"taskId"`
	TaskName          string                   `json:"taskName"`
	StartNumber       int                      `json:"startNumber"`
	StartIndex        *int                     `json:"startIndex"`
	TotalBots         int                      `json:"totalBots"`
	AccountPrefix     string                   `json:"accountPrefix"`
	ConcurrentNum     int                      `json:"concurrentNum"`
	MainService       string                   `json:"mainService"`
	StateExtra        map[string]string        `json:"stateExtra"`
	HeartbeatInterval string                   `json:"heartbeatInterval"`
	TCPTimeout        string                   `json:"tcpTimeout"`
	HTTPTimeout       string                   `json:"httpTimeout"`
	ApdexT            int                      `json:"apdexT"`
	LogLevel          string                   `json:"logLevel,omitempty"`
	BundleDigest      []byte                   `json:"-"`
	BundleSize        int64                    `json:"-"`
	BundleDir         string                   `json:"-"`
	RampUp            *RampUpConfig            `json:"rampUp,omitempty"`
	Shared            *SharedRuntimeAssignment `json:"shared,omitempty"`
}

// Validate 校验任务分片必须包含全局机器人起始下标。
func (a TaskAssignment) Validate() error {
	if a.StartIndex == nil {
		return fmt.Errorf("任务分配缺少必填字段 startIndex")
	}
	if *a.StartIndex < 0 {
		return fmt.Errorf("任务分配 startIndex 不能为负数（当前 %d）", *a.StartIndex)
	}
	return nil
}

// SharedRuntimeAssignment 是任务级共享状态配置。
type SharedRuntimeAssignment struct {
	RunID string             `json:"runId"`
	Redis shared.RedisConfig `json:"redis"`
}

// RampUpConfig 定义渐进加压阶段。
type RampUpConfig struct {
	Stages []RampUpStage `json:"stages"`
}

// RampUpStage 是单个加压阶段。
type RampUpStage struct {
	Count       int  `json:"count"`
	Concurrency int  `json:"concurrency,omitempty"`
	HoldSec     int  `json:"holdSec,omitempty"`
	Reset       bool `json:"reset,omitempty"`
}
