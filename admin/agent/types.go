package agent

import (
	"time"

	"stressbot/monitor"
)

// Status represents an Admin-side Agent availability state.
type Status string

// 节点可用状态：Idle 空闲、Busy 执行任务中、Unhealthy 心跳滞后超过阈值、
// Offline 心跳超时（健康扫描会将其从注册表清理并触发离线回调）。
const (
	Idle      Status = "idle"
	Busy      Status = "busy"
	Unhealthy Status = "unhealthy"
	Offline   Status = "offline"
)

// Node is the registry's current view of a connected Agent.
type Node struct {
	ID             string     `json:"agentId"`
	Name           string     `json:"name"`
	Address        string     `json:"address"`
	AppVersion     string     `json:"appVersion"`
	MaxBots        int        `json:"maxBots"`
	StressInterval string     `json:"stressInterval"`
	SystemInterval string     `json:"systemInterval"`
	StaticInfo     StaticInfo `json:"staticInfo"`

	Status          Status    `json:"status"`
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
	CurrentTaskID   string    `json:"currentTaskId,omitempty"`
	CurrentBots     int       `json:"currentBots"`

	LatestStress    *monitor.CollectorSnapshot `json:"-"`
	LatestSystem    *SystemSnapshot            `json:"-"`
	StressUpdatedAt time.Time                  `json:"stressUpdatedAt"`
	SystemUpdatedAt time.Time                  `json:"systemUpdatedAt"`
}

// StaticInfo 是节点注册时上报的静态环境信息；StartedAt 标识进程实例，
// Registry 以其变化识别节点重启并触发 onRestart 回调。
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

// HeartbeatRequest 是节点心跳上报体：当前状态、执行中的任务与机器人数量。
// busy 心跳必须携带 currentTaskId，idle 心跳不得携带。
type HeartbeatRequest struct {
	AgentID       string `json:"agentId"`
	Timestamp     string `json:"timestamp"`
	Status        string `json:"status"`
	CurrentTaskID string `json:"currentTaskId,omitempty"`
	CurrentBots   int    `json:"currentBots"`
	AppVersion    string `json:"appVersion"`
}

// SystemSnapshot 是节点系统指标快照：宿主 CPU/内存/网络与 Agent 进程级指标。
// Sequence 单调递增，Registry 据此丢弃乱序与重复上报。
type SystemSnapshot struct {
	Timestamp time.Time `json:"timestamp"`
	Sequence  uint64    `json:"sequence"`

	HostCPUPercent         *float64 `json:"hostCpuPercent"`
	HostMemTotalBytes      *uint64  `json:"hostMemTotalBytes"`
	HostMemUsedBytes       *uint64  `json:"hostMemUsedBytes"`
	HostMemPercent         *float64 `json:"hostMemPercent"`
	HostNetSendBytesPerSec *float64 `json:"hostNetSendBytesPerSec"`
	HostNetRecvBytesPerSec *float64 `json:"hostNetRecvBytesPerSec"`

	ProcessCPUPercent *float64 `json:"processCpuPercent"`
	ProcessRSSBytes   *uint64  `json:"processRssBytes"`
	ProcessHeapBytes  uint64   `json:"processHeapBytes"`
	ProcessGoroutines int      `json:"processGoroutines"`
	ProcessThreads    *int32   `json:"processThreads"`
	ProcessFDs        *int32   `json:"processFds"`
}
