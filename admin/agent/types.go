package agent

import (
	"time"

	"stressbot/monitor"
)

// Status represents an Admin-side Agent availability state.
type Status string

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

type HeartbeatRequest struct {
	AgentID       string `json:"agentId"`
	Timestamp     string `json:"timestamp"`
	Status        string `json:"status"`
	CurrentTaskID string `json:"currentTaskId,omitempty"`
	CurrentBots   int    `json:"currentBots"`
	AppVersion    string `json:"appVersion"`
}

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
