package metrics

import (
	"time"

	"stressbot/admin/agent"
)

// ── 系统指标（Agent 上报）─────────────────────────────

// ── 聚合快照 ─────────────────────────────────────────

// ClusterSystemSnapshot 集群系统资源聚合快照。
type ClusterSystemSnapshot struct {
	Timestamp time.Time `json:"timestamp"`

	AgentCount      int     `json:"agentCount"`
	OnlineCount     int     `json:"onlineCount"`
	UnhealthyCount  int     `json:"unhealthyCount"`
	OfflineCount    int     `json:"offlineCount"`
	ReportingAgents int     `json:"reportingAgents"`
	StaleAgents     int     `json:"staleAgents"`
	MissingAgents   int     `json:"missingAgents"`
	CoverageRatio   float64 `json:"coverageRatio"`

	HostCPUReportingAgents int      `json:"hostCpuReportingAgents"`
	AvgHostCPUPercent      *float64 `json:"avgHostCpuPercent"`
	MaxHostCPUPercent      *float64 `json:"maxHostCpuPercent"`
	HotHostCPUAgentID      string   `json:"hotHostCpuAgentId,omitempty"`
	HotHostCPUAgentName    string   `json:"hotHostCpuAgentName,omitempty"`

	HostMemoryReportingAgents int      `json:"hostMemoryReportingAgents"`
	AvgHostMemPercent         *float64 `json:"avgHostMemPercent"`
	MaxHostMemPercent         *float64 `json:"maxHostMemPercent"`
	HotHostMemAgentID         string   `json:"hotHostMemAgentId,omitempty"`
	HotHostMemAgentName       string   `json:"hotHostMemAgentName,omitempty"`
	TotalHostMemBytes         *uint64  `json:"totalHostMemBytes"`
	UsedHostMemBytes          *uint64  `json:"usedHostMemBytes"`

	HostNetSendReportingAgents  int      `json:"hostNetSendReportingAgents"`
	HostNetRecvReportingAgents  int      `json:"hostNetRecvReportingAgents"`
	TotalHostNetSendBytesPerSec *float64 `json:"totalHostNetSendBytesPerSec"`
	TotalHostNetRecvBytesPerSec *float64 `json:"totalHostNetRecvBytesPerSec"`

	ProcessCPUReportingAgents int      `json:"processCpuReportingAgents"`
	AvgProcessCPUPercent      *float64 `json:"avgProcessCpuPercent"`
	MaxProcessCPUPercent      *float64 `json:"maxProcessCpuPercent"`
	HotProcessCPUAgentID      string   `json:"hotProcessCpuAgentId,omitempty"`
	HotProcessCPUAgentName    string   `json:"hotProcessCpuAgentName,omitempty"`

	ProcessRSSReportingAgents int     `json:"processRssReportingAgents"`
	TotalProcessRSSBytes      *uint64 `json:"totalProcessRssBytes"`
	MaxProcessRSSBytes        *uint64 `json:"maxProcessRssBytes"`
	HotProcessRSSAgentID      string  `json:"hotProcessRssAgentId,omitempty"`
	HotProcessRSSAgentName    string  `json:"hotProcessRssAgentName,omitempty"`
	TotalProcessHeapBytes     *uint64 `json:"totalProcessHeapBytes"`
	TotalProcessGoroutines    *int    `json:"totalProcessGoroutines"`

	ProcessThreadsReportingAgents int    `json:"processThreadsReportingAgents"`
	TotalProcessThreads           *int32 `json:"totalProcessThreads"`
	ProcessFDsReportingAgents     int    `json:"processFdsReportingAgents"`
	TotalProcessFDs               *int32 `json:"totalProcessFds"`
	MaxProcessFDs                 *int32 `json:"maxProcessFds"`
	HotProcessFDsAgentID          string `json:"hotProcessFdsAgentId,omitempty"`
	HotProcessFDsAgentName        string `json:"hotProcessFdsAgentName,omitempty"`

	Agents []AgentSystemBrief `json:"agents"`
}

// AgentSystemBrief 单个 Agent 的系统资源摘要。
type AgentSystemBrief struct {
	AgentID            string     `json:"agentId"`
	Name               string     `json:"name"`
	Status             string     `json:"status"`
	IsStale            bool       `json:"isStale"`
	SampledAt          *time.Time `json:"sampledAt"`
	ReceivedAt         *time.Time `json:"receivedAt"`
	SnapshotAgeSeconds *float64   `json:"snapshotAgeSeconds"`
	LastHeartbeatAt    time.Time  `json:"lastHeartbeatAt"`

	HostCPUPercent         *float64 `json:"hostCpuPercent"`
	HostMemPercent         *float64 `json:"hostMemPercent"`
	HostNetSendBytesPerSec *float64 `json:"hostNetSendBytesPerSec"`
	HostNetRecvBytesPerSec *float64 `json:"hostNetRecvBytesPerSec"`
	ProcessCPUPercent      *float64 `json:"processCpuPercent"`
	ProcessRSSBytes        *uint64  `json:"processRssBytes"`
	ProcessHeapBytes       *uint64  `json:"processHeapBytes"`
	ProcessGoroutines      *int     `json:"processGoroutines"`
	ProcessThreads         *int32   `json:"processThreads"`
	ProcessFDs             *int32   `json:"processFds"`
}

// AgentListItem is the list-facing Agent projection. Resource values are only
// populated while the latest system snapshot is fresh by the Admin clock.
type AgentListItem struct {
	AgentID       string            `json:"agentId"`
	Name          string            `json:"name"`
	Address       string            `json:"address"`
	AppVersion    string            `json:"appVersion"`
	MaxBots       int               `json:"maxBots"`
	Status        agent.AgentStatus `json:"status"`
	CurrentTaskID string            `json:"currentTaskId"`
	CurrentBots   int               `json:"currentBots"`
	StaticInfo    agent.StaticInfo  `json:"staticInfo"`

	LastHeartbeatAt          time.Time  `json:"lastHeartbeatAt"`
	StressUpdatedAt          *time.Time `json:"stressUpdatedAt"`
	SystemUpdatedAt          *time.Time `json:"systemUpdatedAt"`
	SystemStale              bool       `json:"systemStale"`
	SystemSnapshotAgeSeconds *float64   `json:"systemSnapshotAgeSeconds"`

	HostCPUPercent    *float64 `json:"hostCpuPercent"`
	HostMemPercent    *float64 `json:"hostMemPercent"`
	ProcessCPUPercent *float64 `json:"processCpuPercent"`
	ProcessRSSBytes   *uint64  `json:"processRssBytes"`
	ProcessGoroutines *int     `json:"processGoroutines"`
}

// HistoryTrendPoint is one persisted interval sample produced by the metrics sampler.
type HistoryTrendPoint struct {
	SampledAt         time.Time `json:"sampledAt"`
	ElapsedSec        int       `json:"elapsedSec"`
	StageIndex        int       `json:"stageIndex"`
	WindowFrom        time.Time `json:"windowFrom"`
	WindowTo          time.Time `json:"windowTo"`
	SampleCount       int64     `json:"sampleCount"`
	HistoryBatchToken []byte    `json:"-"`
	TotalQPS          float64   `json:"totalQps"`
	RTTApdex          *float64  `json:"rttApdex"`
	ListenWaitP99Ms   *float64  `json:"listenWaitP99Ms"`
	RTTAvgMs          *float64  `json:"rttAvgMs"`
	RTTP50Ms          *float64  `json:"rttP50Ms"`
	RTTP90Ms          *float64  `json:"rttP90Ms"`
	RTTP95Ms          *float64  `json:"rttP95Ms"`
	RTTP99Ms          *float64  `json:"rttP99Ms"`

	ActiveConnections  *int64   `json:"activeConnections"`
	ClosedConnections  *int64   `json:"closedConnections"`
	DroppedConnections *int64   `json:"droppedConnections"`
	NetSendBytesPerSec *float64 `json:"netSendBytesPerSec"`
	NetRecvBytesPerSec *float64 `json:"netRecvBytesPerSec"`
	AssignedAgents     *int     `json:"assignedAgents"`
	ReportingAgents    *int     `json:"reportingAgents"`
	ReportingCoverage  *float64 `json:"reportingCoverage"`

	TotalDurationAvgMs *float64 `json:"totalDurationAvgMs"`
	TotalDurationP95Ms *float64 `json:"totalDurationP95Ms"`
	TotalDurationP99Ms *float64 `json:"totalDurationP99Ms"`
	ClientAvgMs        *float64 `json:"nonRTTAvgMs"`
	EncodeAvgMs        *float64 `json:"encodeAvgMs"`
	DecodeAvgMs        *float64 `json:"decodeAvgMs"`

	BotsRunning int     `json:"botsRunning"`
	BotsErrored int     `json:"botsErrored"`
	SendKBps    float64 `json:"sendKBps"`
	RecvKBps    float64 `json:"recvKBps"`

	AvgCPUPercent float64 `json:"avgCpuPercent"`
	MaxCPUPercent float64 `json:"maxCpuPercent"`
	AvgMemPercent float64 `json:"avgMemPercent"`
	MaxMemPercent float64 `json:"maxMemPercent"`
	Goroutines    int     `json:"goroutines"`
	Threads       int     `json:"threads"`
	FDs           int     `json:"fds"`
	OnlineCount   int     `json:"onlineCount"`
	OfflineCount  int     `json:"offlineCount"`
}
