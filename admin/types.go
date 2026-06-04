package admin

import (
	"time"

	"stressbot/monitor"
	"stressbot/robot"
	json "stressbot/utils/jsonx"
)

// ── Task ──────────────────────────────────────────────

// TaskState 表示任务生命周期状态。
type TaskState string

const (
	// TaskPending 任务已创建，等待启动。
	TaskPending TaskState = "pending"
	// TaskStarting 任务正在启动（分配 Agent、推送配置）。
	TaskStarting TaskState = "starting"
	// TaskRunning 任务执行中。
	TaskRunning TaskState = "running"
	// TaskStopping 任务正在停止（等待 Agent 上报完成）。
	TaskStopping TaskState = "stopping"
	// TaskStopped 任务已正常停止。
	TaskStopped TaskState = "stopped"
	// TaskFailed 任务失败。
	TaskFailed TaskState = "failed"
)

// IsActiveState 返回该状态是否占据单例位。
func IsActiveState(s TaskState) bool {
	return s == TaskStarting || s == TaskRunning || s == TaskStopping
}

// TaskResult 表示任务结束原因。
type TaskResult string

const (
	// ResultCompleted 任务自然完成。
	ResultCompleted TaskResult = "completed"
	// ResultStopped 任务被手动停止。
	ResultStopped TaskResult = "stopped"
	// ResultFailed 任务失败。
	ResultFailed TaskResult = "failed"
)

// Task 代表一次压测任务的完整数据。
type Task struct {
	// ID 唯一标识。
	ID string `json:"id"`
	// Name 任务名称。
	Name string `json:"name"`
	// State 当前状态。
	State TaskState `json:"state"`
	// TotalBots 总机器人数量。
	TotalBots int `json:"totalBots"`
	// Config 任务配置（流程、proto、脚本等）。
	Config TaskConfig `json:"config"`
	// Assignments Agent 分配方案。
	Assignments []Assignment `json:"assignments,omitempty"`
	// SucceededAgents 实际成功接收任务的 Agent ID 列表（部分 Agent 可能推送失败）。
	SucceededAgents []string `json:"succeededAgents,omitempty"`
	// Reports Agent 最终完成报告，key 为 agentID。
	Reports map[string]TaskCompletionReport `json:"reports,omitempty"`
	// CleanupSummary 所有节点最终清理状态汇总。
	CleanupSummary *robot.CleanupStatus `json:"cleanupSummary,omitempty"`
	// StageReports 渐进式加压阶段完成报告（reset 阶段中间报告）。
	StageReports []TaskCompletionReport `json:"stageReports,omitempty"`
	// AgentEvents 任务期间 Agent 状态变化事件。
	AgentEvents []AgentEvent `json:"agentEvents,omitempty"`
	// CreatedAt 创建时间。
	CreatedAt time.Time `json:"createdAt"`
	// StartedAt 启动时间。
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// StoppedAt 停止时间。
	StoppedAt *time.Time `json:"stoppedAt,omitempty"`
	// ErrorMsg 错误信息（失败时填写）。
	ErrorMsg string `json:"errorMsg,omitempty"`
}

// TaskConfig 包含创建任务时上传的所有资源。
type TaskConfig struct {
	// FlowJSON flow.json 原始内容。
	FlowJSON json.RawMessage `json:"flowJson"`
	// ProtoFiles proto 文件名 → 内容。
	ProtoFiles map[string][]byte `json:"protoFiles,omitempty"`
	// LuaScripts Lua 脚本名 → 内容。
	LuaScripts map[string][]byte `json:"luaScripts,omitempty"`
	// AdapterScript codec.lua 内容。
	AdapterScript []byte `json:"adapterScript,omitempty"`
	// ErrorMapScript error.lua 内容。
	ErrorMapScript []byte `json:"errorMapScript,omitempty"`
	// RobotConfig 任务级运行时配置。
	RobotConfig RobotConfig `json:"robotConfig"`
	// Deadline 任务截止时间，超过后 Agent 自动停止。
	Deadline *time.Time `json:"deadline,omitempty"`
}

// RobotConfig 任务级运行时配置（前端 → admin → agent）。
// 超时字段统一用 int 秒数，admin 转为 "Ns" duration 字符串下发。
type RobotConfig struct {
	// Concurrency 并发启动数。
	Concurrency int `json:"concurrency"`
	// TimeoutSec TCP 请求超时秒数。
	TimeoutSec int `json:"timeoutSec"`

	// AccountPrefix 账号名前缀，空则使用 "bot_"。
	AccountPrefix string `json:"accountPrefix,omitempty"`
	// StartNumber 起始编号。
	StartNumber int `json:"startNumber,omitempty"`
	// MainService 主服务名。
	MainService string `json:"mainService,omitempty"`
	// StateExtra 初始状态额外键值对。
	StateExtra map[string]string `json:"stateExtra,omitempty"`

	// HeartbeatSec 心跳间隔秒数。
	HeartbeatSec int `json:"heartbeatSec,omitempty"`
	// HTTPTimeoutSec HTTP 请求超时秒数。
	HTTPTimeoutSec int `json:"httpTimeoutSec,omitempty"`
	// ApdexT Apdex 阈值（毫秒）。
	ApdexT int `json:"apdexT,omitempty"`

	// DebugMode 是否开启调试模式。
	DebugMode bool `json:"debugMode,omitempty"`
	// LogLevel 临时日志等级（覆盖全局配置）。
	LogLevel string `json:"logLevel,omitempty"`
	// RampUp 渐进式加压配置。
	RampUp *RampUpConfig `json:"rampUp,omitempty"`
}

// RampUpConfig 渐进式加压配置。
type RampUpConfig struct {
	// Stages 加压阶段列表。
	Stages []RampUpStage `json:"stages"`
}

// RampUpStage 单个加压阶段。
type RampUpStage struct {
	// Count 本阶段新增 bot 数（增量值）。
	Count int `json:"count"`
	// Concurrency 覆盖全局并发数，0 或空则用全局值。
	Concurrency int `json:"concurrency,omitempty"`
	// HoldSec 阶段间等待秒数。
	HoldSec int `json:"holdSec,omitempty"`
	// Reset 开始本阶段前清空所有已有机器人。
	Reset bool `json:"reset,omitempty"`
}

// ── Assignment ────────────────────────────────────────

// AgentEvent 记录任务期间 Agent 节点的状态变化事件（离线、重连等）。
type AgentEvent struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// AgentName Agent 显示名称。
	AgentName string `json:"agentName"`
	// Type 事件类型："offline" | "reconnected" | "deregistered"。
	Type string `json:"type"`
	// Timestamp 事件发生时间。
	Timestamp time.Time `json:"timestamp"`
	// Detail 事件详情。
	Detail string `json:"detail,omitempty"`
}

// Assignment 表示一个 Agent 被分配的 bot 子集。
type Assignment struct {
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// AgentID Agent ID。
	AgentID string `json:"agentId"`
	// AgentName Agent 显示名称。
	AgentName string `json:"agentName"`
	// StartNumber 本 Agent 的 bot 起始编号。
	StartNumber int `json:"startNumber"`
	// TotalBots 本 Agent 分配的 bot 数量。
	TotalBots int `json:"totalBots"`
}

// TaskAssignment Admin → Agent 下发的任务分配。
type TaskAssignment struct {
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// TaskName 任务名称。
	TaskName string `json:"taskName"`
	// StartNumber 本 Agent 的 bot 起始编号。
	StartNumber int `json:"startNumber"`
	// TotalBots 本 Agent 分配的 bot 数量。
	TotalBots int `json:"totalBots"`
	// AccountPrefix 账号名前缀。
	AccountPrefix string `json:"accountPrefix"`
	// ConcurrentNum 并发启动数。
	ConcurrentNum int `json:"concurrentNum"`
	// MainService 主服务名。
	MainService string `json:"mainService"`
	// StateExtra 初始状态额外键值对。
	StateExtra map[string]string `json:"stateExtra"`
	// HeartbeatInterval 心跳间隔（duration 字符串）。
	HeartbeatInterval string `json:"heartbeatInterval"`
	// TCPTimeout TCP 请求超时（duration 字符串）。
	TCPTimeout string `json:"tcpTimeout"`
	// HTTPTimeout HTTP 请求超时（duration 字符串）。
	HTTPTimeout string `json:"httpTimeout"`
	// ApdexT Apdex 阈值（毫秒）。
	ApdexT int `json:"apdexT"`
	// LogLevel 临时日志等级。
	LogLevel string `json:"logLevel,omitempty"`
	// ConfigURL 配置文件下载基础 URL。
	ConfigURL string `json:"configUrl"`
	// ConfigFiles 需要下载的配置文件相对路径列表。
	ConfigFiles []string `json:"configFiles"`
	// RampUp 渐进式加压配置（已按比例缩放）。
	RampUp *RampUpConfig `json:"rampUp,omitempty"`
}

// ── Agent ─────────────────────────────────────────────

// AgentStatus 表示 Agent 节点的在线状态。
type AgentStatus string

const (
	// AgentIdle 空闲，可接受任务。
	AgentIdle AgentStatus = "idle"
	// AgentBusy 执行任务中。
	AgentBusy AgentStatus = "busy"
	// AgentUnhealthy 心跳超时但仍在线。
	AgentUnhealthy AgentStatus = "unhealthy"
	// AgentOffline 已离线。
	AgentOffline AgentStatus = "offline"
)

// AgentNode 表示一个已注册的 Agent 节点。
type AgentNode struct {
	// ID Agent 唯一标识。
	ID string `json:"agentId"`
	// Name 显示名称。
	Name string `json:"name"`
	// Address HTTP 地址（host:port）。
	Address string `json:"address"`
	// AppVersion 应用版本号。
	AppVersion string `json:"appVersion"`
	// MaxBots 最大可承载 bot 数。
	MaxBots int `json:"maxBots"`
	// StressInterval 压测指标上报间隔。
	StressInterval string `json:"stressInterval"`
	// SystemInterval 系统指标上报间隔。
	SystemInterval string `json:"systemInterval"`
	// StaticInfo 静态硬件信息（注册时上报，不变）。
	StaticInfo StaticInfo `json:"staticInfo"`

	// Status 当前在线状态。
	Status AgentStatus `json:"status"`
	// LastHeartbeatAt 最后一次心跳时间。
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
	// CurrentTaskID 正在执行的任务 ID，空闲时为空。
	CurrentTaskID string `json:"currentTaskId,omitempty"`
	// CurrentBots 当前承载的 bot 数。
	CurrentBots int `json:"currentBots"`

	// LatestStress 最新压测指标快照（不序列化到前端列表）。
	LatestStress *monitor.CollectorSnapshot `json:"-"`
	// LatestSystem 最新系统指标快照（不序列化到前端列表）。
	LatestSystem *SystemSnapshot `json:"-"`
	// StressUpdatedAt 压测指标最后更新时间。
	StressUpdatedAt time.Time `json:"stressUpdatedAt,omitempty"`
	// SystemUpdatedAt 系统指标最后更新时间。
	SystemUpdatedAt time.Time `json:"systemUpdatedAt,omitempty"`
}

// StaticInfo Agent 节点的静态硬件与环境信息。
type StaticInfo struct {
	// Hostname 主机名。
	Hostname string `json:"hostname"`
	// OS 操作系统。
	OS string `json:"os"`
	// Arch 架构。
	Arch string `json:"arch"`
	// NumCPU CPU 核心数。
	NumCPU int `json:"numCpu"`
	// MemTotalMB 总内存（MB）。
	MemTotalMB uint64 `json:"memTotalMB"`
	// GoVersion Go 版本。
	GoVersion string `json:"goVersion"`
	// KernelVer 内核版本。
	KernelVer string `json:"kernelVer"`
	// StartedAt Agent 进程启动时间。
	StartedAt time.Time `json:"startedAt"`
}

// ── 上报报文 ──────────────────────────────────────────

// StressReport Agent 上报的压测指标。
type StressReport struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// ReportedAt 上报时间。
	ReportedAt time.Time `json:"reportedAt"`
	// Snapshot 压测指标快照。
	Snapshot *monitor.CollectorSnapshot `json:"snapshot"`
}

// SystemReport Agent 上报的系统资源指标。
type SystemReport struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// ReportedAt 上报时间。
	ReportedAt time.Time `json:"reportedAt"`
	// Snapshot 系统指标快照。
	Snapshot SystemSnapshot `json:"snapshot"`
}

// TaskCompletionReport Agent 任务完成报告。
type TaskCompletionReport struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// Result 完成结果。
	Result TaskResult `json:"result"`
	// ErrorMsg 错误信息。
	ErrorMsg string `json:"errorMsg,omitempty"`
	// FinishedAt 完成时间。
	FinishedAt time.Time `json:"finishedAt"`
	// FinalSnapshot 最终压测指标快照。
	FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
	// StageIndex 阶段索引（渐进式加压阶段重置时使用）。
	// -1 或零值表示最终报告，>= 0 表示该阶段的完成报告。
	StageIndex int `json:"stageIndex,omitempty"`
	// CleanupStatus Agent 侧资源清理结果。
	CleanupStatus *robot.CleanupStatus `json:"cleanupStatus,omitempty"`
}

// ── 注册 / 心跳 ──────────────────────────────────────

// RegisterRequest Agent 注册请求。
type RegisterRequest struct {
	// AgentID Agent 唯一标识（客户端生成）。
	AgentID string `json:"agentId"`
	// Name 显示名称。
	Name string `json:"name"`
	// Address HTTP 地址。
	Address string `json:"address"`
	// AppVersion 应用版本号。
	AppVersion string `json:"appVersion"`
	// MaxBots 最大可承载 bot 数。
	MaxBots int `json:"maxBots"`
	// StressInterval 压测指标上报间隔。
	StressInterval string `json:"stressInterval"`
	// SystemInterval 系统指标上报间隔。
	SystemInterval string `json:"systemInterval"`
	// StaticInfo 静态硬件信息。
	StaticInfo StaticInfo `json:"staticInfo"`
}

// RegisterResponse Agent 注册响应。
type RegisterResponse struct {
	// AgentID 回传的 Agent ID。
	AgentID string `json:"agentId"`
	// HeartbeatTTL 心跳超时阈值。
	HeartbeatTTL string `json:"heartbeatTtl"`
	// StressEndpoint 压测指标上报路径。
	StressEndpoint string `json:"stressEndpoint"`
	// SystemEndpoint 系统指标上报路径。
	SystemEndpoint string `json:"systemEndpoint"`
}

// HeartbeatRequest Agent 心跳请求。
type HeartbeatRequest struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// Timestamp 心跳时间戳。
	Timestamp string `json:"timestamp"`
	// Status 当前状态（idle/busy）。
	Status string `json:"status"`
	// CurrentTaskID 正在执行的任务 ID。
	CurrentTaskID string `json:"currentTaskId,omitempty"`
	// CurrentBots 当前承载的 bot 数。
	CurrentBots int `json:"currentBots"`
	// AppVersion 应用版本号。
	AppVersion string `json:"appVersion"`
}

// ── 系统指标（Agent 上报）─────────────────────────────

// SystemSnapshot Agent 节点的系统资源快照。
type SystemSnapshot struct {
	// Timestamp 采集时间。
	Timestamp time.Time `json:"timestamp"`

	// CPUPercent 总 CPU 使用率（0~100）。
	CPUPercent float64 `json:"cpuPercent"`
	// CPUPerCore 每核 CPU 使用率。
	CPUPerCore []float64 `json:"cpuPerCore,omitempty"`
	// LoadAvg1 1 分钟平均负载。
	LoadAvg1 float64 `json:"loadAvg1,omitempty"`
	// LoadAvg5 5 分钟平均负载。
	LoadAvg5 float64 `json:"loadAvg5,omitempty"`
	// LoadAvg15 15 分钟平均负载。
	LoadAvg15 float64 `json:"loadAvg15,omitempty"`

	// MemTotalMB 总内存（MB）。
	MemTotalMB uint64 `json:"memTotalMB"`
	// MemUsedMB 已用内存（MB）。
	MemUsedMB uint64 `json:"memUsedMB"`
	// MemPercent 内存使用率（0~100）。
	MemPercent float64 `json:"memPercent"`
	// SwapUsedMB 已用 Swap（MB）。
	SwapUsedMB uint64 `json:"swapUsedMB,omitempty"`
	// ProcessRssMB 进程 RSS（MB）。
	ProcessRssMB uint64 `json:"processRssMB"`
	// ProcessHeapMB 进程堆内存（MB）。
	ProcessHeapMB uint64 `json:"processHeapMB"`
	// ProcessSysMB 进程系统内存（MB）。
	ProcessSysMB uint64 `json:"processSysMB"`

	// NumGoroutine goroutine 数量。
	NumGoroutine int `json:"numGoroutine"`
	// NumThread OS 线程数量。
	NumThread int32 `json:"numThread"`
	// NumFD 打开的文件描述符数。
	NumFD int32 `json:"numFd,omitempty"`
	// GCCount GC 次数。
	GCCount uint32 `json:"gcCount"`
	// GCPauseAvgMs GC 平均暂停时间（ms）。
	GCPauseAvgMs float64 `json:"gcPauseAvgMs,omitempty"`

	// NetSendKBps 网络发送速率（KB/s）。
	NetSendKBps float64 `json:"netSendKBps"`
	// NetRecvKBps 网络接收速率（KB/s）。
	NetRecvKBps float64 `json:"netRecvKBps"`
}

// ── 聚合快照 ─────────────────────────────────────────

// ClusterSystemSnapshot 集群系统资源聚合快照。
type ClusterSystemSnapshot struct {
	// Timestamp 聚合时间。
	Timestamp time.Time `json:"timestamp"`
	// AgentCount 已注册 Agent 总数。
	AgentCount int `json:"agentCount"`
	// OnlineCount 在线 Agent 数。
	OnlineCount int `json:"onlineCount"`
	// OfflineCount 离线 Agent 数。
	OfflineCount int `json:"offlineCount"`
	// AvgCPUPercent 集群平均 CPU 使用率。
	AvgCPUPercent float64 `json:"avgCpuPercent"`
	// MaxCPUPercent 集群最大 CPU 使用率。
	MaxCPUPercent float64 `json:"maxCpuPercent"`
	// HotAgentID CPU 最高的 Agent ID。
	HotAgentID string `json:"hotAgentId,omitempty"`
	// HotAgentName CPU 最高的 Agent 名称。
	HotAgentName string `json:"hotAgentName,omitempty"`
	// TotalMemMB 集群总内存（MB）。
	TotalMemMB uint64 `json:"totalMemMB"`
	// UsedMemMB 集群已用内存（MB）。
	UsedMemMB uint64 `json:"usedMemMB"`
	// TotalNetSendKBps 集群总发送速率。
	TotalNetSendKBps float64 `json:"totalNetSendKBps"`
	// TotalNetRecvKBps 集群总接收速率。
	TotalNetRecvKBps float64 `json:"totalNetRecvKBps"`
	// TotalGoroutines 集群总 goroutine 数。
	TotalGoroutines int `json:"totalGoroutines"`
	// TotalThreads 集群总线程数。
	TotalThreads int32 `json:"totalThreads"`
	// TotalFDs 集群总文件描述符数。
	TotalFDs int32 `json:"totalFds"`
	// Agents 各 Agent 的系统资源摘要。
	Agents []AgentSystemBrief `json:"agents"`
}

// AgentSystemBrief 单个 Agent 的系统资源摘要。
type AgentSystemBrief struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// Name 显示名称。
	Name string `json:"name"`
	// Status 在线状态。
	Status string `json:"status"`
	// CPUPercent CPU 使用率。
	CPUPercent float64 `json:"cpuPercent"`
	// MemPercent 内存使用率。
	MemPercent float64 `json:"memPercent"`
	// NumGoroutine goroutine 数量。
	NumGoroutine int `json:"numGoroutine"`
	// NetSendKBps 网络发送速率。
	NetSendKBps float64 `json:"netSendKBps"`
	// NetRecvKBps 网络接收速率。
	NetRecvKBps float64 `json:"netRecvKBps"`
	// LastSeen 最后心跳距今秒数。
	LastSeen int64 `json:"lastSeen"`
}

// ── 历史归档 ─────────────────────────────────────────

// HistoryRecord 历史任务列表中的单条记录。
type HistoryRecord struct {
	// ID 任务 ID。
	ID string `json:"id"`
	// Name 任务名称。
	Name string `json:"name"`
	// State 最终状态。
	State TaskState `json:"state"`
	// TotalBots 总 bot 数。
	TotalBots int `json:"totalBots"`
	// AgentCount 参与 Agent 数。
	AgentCount       int `json:"agentCount"`
	ActiveAgentCount int `json:"activeAgentCount"`
	// CreatedAt 创建时间。
	CreatedAt time.Time `json:"createdAt"`
	// StartedAt 启动时间。
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// StoppedAt 停止时间。
	StoppedAt *time.Time `json:"stoppedAt,omitempty"`
	// DurationSec 运行时长（秒）。
	DurationSec int `json:"durationSec"`
	// ErrorMsg 错误信息。
	ErrorMsg string `json:"errorMsg,omitempty"`

	// Starred 是否收藏。
	Starred bool `json:"starred"`
	// Tags 标签列表。
	Tags []string `json:"tags"`
	// Note 备注。
	Note string `json:"note"`

	// ConfigSummary 配置摘要。
	ConfigSummary ConfigSummary `json:"configSummary"`
	// StageCount 渐进式加压阶段数（0 表示一次性创建）。
	StageCount int `json:"stageCount,omitempty"`
}

// ConfigSummary 历史任务的配置摘要。
type ConfigSummary struct {
	// Concurrency 并发数。
	Concurrency int `json:"concurrency"`
	// TimeoutSec 超时秒数。
	TimeoutSec int `json:"timeoutSec"`
	// FlowSizeKB flow.json 大小（KB）。
	FlowSizeKB int `json:"flowSizeKB"`
	// ProtoCount proto 文件数。
	ProtoCount int `json:"protoCount"`
	// ScriptCount Lua 脚本数。
	ScriptCount int `json:"scriptCount"`
}

// HistoryDetail 历史任务详情归档模型，保留完整内部数据供调试和兼容使用。
type HistoryDetail struct {
	HistoryRecord
	// Assignments Agent 分配方案。
	Assignments []Assignment `json:"assignments"`
	// AgentReports 各 Agent 完成报告。
	AgentReports []HistoryAgentReport `json:"agentReports"`
	// AgentEvents Agent 状态变化事件。
	AgentEvents []AgentEvent `json:"agentEvents,omitempty"`
	// FinalSnapshot 最终聚合压测指标。
	FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
	// FinalSystem 最终聚合系统指标。
	FinalSystem ClusterSystemSnapshot `json:"finalSystem"`
}

// HistoryDetailResponse 历史详情页展示响应，只包含界面实际消费的数据。
type HistoryDetailResponse struct {
	HistoryRecord
	// AgentReports 各节点完成结果摘要。
	AgentReports []HistoryAgentReportSummary `json:"agentReports"`
	// AgentEvents 节点状态变化事件。
	AgentEvents []AgentEvent `json:"agentEvents,omitempty"`
	// FinalSnapshot 最终聚合压测指标摘要。
	FinalSnapshot HistoryStressSnapshotSummary `json:"finalSnapshot"`
	// FinalSystem 最终聚合系统指标摘要。
	FinalSystem HistorySystemSummary `json:"finalSystem"`
}

// HistoryStressSnapshotSummary 历史详情页使用的压测指标摘要。
type HistoryStressSnapshotSummary struct {
	Timestamp    time.Time                  `json:"timestamp,omitempty"`
	UptimeSec    float64                    `json:"uptimeSeconds"`
	TotalActions int64                      `json:"totalActions"`
	Connections  monitor.ConnectionSnapshot `json:"connections"`
	Actions      []HistoryActionSummary     `json:"actions"`
}

// HistoryActionSummary 历史 action 表格和报告使用的展示字段。
type HistoryActionSummary struct {
	Name                     string                  `json:"name"`
	SampleCount              int64                   `json:"sampleCount"`
	SuccessCount             int64                   `json:"successCount"`
	FailureCount             int64                   `json:"failureCount"`
	TimeoutCount             int64                   `json:"timeoutCount"`
	CanceledCount            int64                   `json:"canceledCount"`
	Executing                int64                   `json:"executing"`
	SuccessRate              float64                 `json:"successRate"`
	AvgSendBytes             float64                 `json:"avgSendBytes"`
	AvgRecvBytes             float64                 `json:"avgRecvBytes"`
	RTTApdex                 float64                 `json:"rttApdex"`
	TotalDurationApdex       float64                 `json:"totalDurationApdex"`
	RTT                      HistoryHistogramSummary `json:"rtt"`
	TotalDuration            HistoryHistogramSummary `json:"totalDuration"`
	ClientAvgMs              float64                 `json:"clientAvgMs"`
	EncodeAvgMs              float64                 `json:"encodeAvgMs"`
	DecodeAvgMs              float64                 `json:"decodeAvgMs"`
	ParseStoreAvgMs          float64                 `json:"parseStoreAvgMs"`
	RTTSampleCount           int64                   `json:"rttSampleCount"`
	TotalDurationSampleCount int64                   `json:"totalDurationSampleCount"`
	AvgQPS                   float64                 `json:"avgQps"`
	Errors                   []monitor.ErrorEntry    `json:"errors,omitempty"`
}

// HistoryHistogramSummary 历史界面需要的 RTT 分位摘要。
type HistoryHistogramSummary struct {
	MaxMs float64 `json:"maxMs"`
	AvgMs float64 `json:"avgMs"`
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	P99Ms float64 `json:"p99Ms"`
}

// HistorySystemSummary 历史详情页使用的集群系统资源摘要。
type HistorySystemSummary struct {
	AvgCPUPercent    float64 `json:"avgCpuPercent"`
	MaxCPUPercent    float64 `json:"maxCpuPercent"`
	HotAgentName     string  `json:"hotAgentName,omitempty"`
	TotalMemMB       uint64  `json:"totalMemMB"`
	UsedMemMB        uint64  `json:"usedMemMB"`
	TotalNetSendKBps float64 `json:"totalNetSendKBps"`
	TotalNetRecvKBps float64 `json:"totalNetRecvKBps"`
	TotalGoroutines  int     `json:"totalGoroutines"`
	TotalThreads     int32   `json:"totalThreads"`
	TotalFDs         int32   `json:"totalFds"`
}

// HistoryAgentReport 单个 Agent 的历史完成报告。
type HistoryAgentReport struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// AgentName 显示名称。
	AgentName string `json:"agentName"`
	// Result 完成结果。
	Result TaskResult `json:"result"`
	// ErrorMsg 错误信息。
	ErrorMsg string `json:"errorMsg,omitempty"`
	// FinishedAt 完成时间。
	FinishedAt time.Time `json:"finishedAt"`
	// FinalSnapshot 该 Agent 的最终压测指标。
	FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
	// CleanupStatus Agent 侧资源清理结果。
	CleanupStatus *robot.CleanupStatus `json:"cleanupStatus,omitempty"`
}

// HistoryAgentReportSummary 单个节点的历史完成结果摘要。
type HistoryAgentReportSummary struct {
	AgentID       string               `json:"agentId"`
	AgentName     string               `json:"agentName"`
	Result        TaskResult           `json:"result"`
	ErrorMsg      string               `json:"errorMsg,omitempty"`
	FinishedAt    time.Time            `json:"finishedAt"`
	CleanupStatus *robot.CleanupStatus `json:"cleanupStatus,omitempty"`
}

// HistoryFilter 历史任务查询过滤条件。
type HistoryFilter struct {
	// State 按状态过滤。
	State string
	// StartedAfter 开始时间下界。
	StartedAfter time.Time
	// StartedBefore 开始时间上界。
	StartedBefore time.Time
	// Tags 包含任一标签。
	Tags []string
	// TagsAll 包含全部标签。
	TagsAll []string
	// Starred 仅收藏。
	Starred *bool
	// Search 关键词搜索（名称/ID/标签/备注）。
	Search string
	// Limit 返回条数上限。
	Limit int
	// Offset 分页偏移。
	Offset int
	// OrderBy 排序字段。
	OrderBy string
}

// HistoryListResponse 历史任务列表响应。
type HistoryListResponse struct {
	// Total 符合条件的总数。
	Total int `json:"total"`
	// Items 当前页记录。
	Items []HistoryRecord `json:"items"`
}

// UpdateHistoryRequest 更新历史任务元数据请求。
type UpdateHistoryRequest struct {
	// Starred 收藏状态。
	Starred *bool `json:"starred,omitempty"`
	// Tags 标签列表（全量替换）。
	Tags *[]string `json:"tags,omitempty"`
	// Note 备注。
	Note *string `json:"note,omitempty"`
}

// CompareResponse 历史任务对比响应。
type CompareResponse struct {
	// Tasks 对比任务的轻量指标列表。
	Tasks []HistoryCompareTask `json:"tasks"`
	// Diff 差异对比数据。
	Diff CompareDiff `json:"diff"`
}

// HistoryCompareTask 历史对比页使用的任务摘要。
type HistoryCompareTask struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	StartedAt     *time.Time             `json:"startedAt,omitempty"`
	DurationSec   int                    `json:"durationSec"`
	TotalBots     int                    `json:"totalBots"`
	FinalSnapshot HistoryCompareSnapshot `json:"finalSnapshot"`
}

// HistoryCompareSnapshot 历史对比页使用的压测摘要。
type HistoryCompareSnapshot struct {
	TotalActions int64                  `json:"totalActions"`
	Actions      []HistoryCompareAction `json:"actions"`
}

// HistoryCompareAction 历史对比页使用的 action 指标。
type HistoryCompareAction struct {
	Name                     string                  `json:"name"`
	SampleCount              int64                   `json:"sampleCount"`
	RTTApdex                 float64                 `json:"rttApdex"`
	TotalDurationApdex       float64                 `json:"totalDurationApdex"`
	RTT                      HistoryHistogramSummary `json:"rtt"`
	TotalDuration            HistoryHistogramSummary `json:"totalDuration"`
	TotalDurationSampleCount int64                   `json:"totalDurationSampleCount"`
}

// CompareDiff 历史任务对比差异。
type CompareDiff struct {
	// Actions action 名称 → 各任务的 P99 延迟数组。
	Actions map[string][]float64 `json:"actions"`
}

// ── 时序采样 ─────────────────────────────────────────

// HistoryTrendPoint 历史趋势采样点。
type HistoryTrendPoint struct {
	// SampledAt 采样时间。
	SampledAt time.Time `json:"sampledAt"`
	// ElapsedSec 距任务启动的秒数。
	ElapsedSec int `json:"elapsedSec"`
	// TotalQPS 集群总 QPS。
	TotalQPS float64 `json:"totalQps"`
	// RTTApdex 按 RTT 样本数加权后的 Apdex。
	RTTApdex float64 `json:"rttApdex"`
	// TotalDurationApdex 按总耗时样本数加权后的 Apdex。
	TotalDurationApdex float64 `json:"totalDurationApdex"`
	// RTTAvgMs 平均 RTT。
	RTTAvgMs float64 `json:"rttAvgMs"`
	// RTTP95Ms P95 RTT。
	RTTP95Ms float64 `json:"rttP95Ms"`
	// RTTP99Ms P99 RTT。
	RTTP99Ms float64 `json:"rttP99Ms"`
	// TotalDurationAvgMs 平均总耗时。
	TotalDurationAvgMs float64 `json:"totalDurationAvgMs"`
	// TotalDurationP95Ms P95 总耗时。
	TotalDurationP95Ms float64 `json:"totalDurationP95Ms"`
	// TotalDurationP99Ms P99 总耗时。
	TotalDurationP99Ms float64 `json:"totalDurationP99Ms"`
	// ClientAvgMs 客户端平均耗时。
	ClientAvgMs float64 `json:"clientAvgMs"`
	// EncodeAvgMs 编码平均耗时。
	EncodeAvgMs float64 `json:"encodeAvgMs"`
	// DecodeAvgMs 解码平均耗时。
	DecodeAvgMs float64 `json:"decodeAvgMs"`
	// BotsRunning 运行中机器人数量。
	BotsRunning int `json:"botsRunning"`
	// BotsErrored 异常机器人数量。
	BotsErrored int `json:"botsErrored"`
	// SendKBps 发送带宽 KB/s。
	SendKBps float64 `json:"sendKBps"`
	// RecvKBps 接收带宽 KB/s。
	RecvKBps float64 `json:"recvKBps"`
	// AvgCPUPercent 平均 CPU 使用率。
	AvgCPUPercent float64 `json:"avgCpuPercent"`
	// MaxCPUPercent 最高 CPU 使用率。
	MaxCPUPercent float64 `json:"maxCpuPercent"`
	// MemPercent 集群内存使用率。
	MemPercent float64 `json:"memPercent"`
	// Goroutines goroutine 总数。
	Goroutines int `json:"goroutines"`
	// Threads 线程总数。
	Threads int `json:"threads"`
	// FDs 文件描述符总数。
	FDs int `json:"fds"`
	// OnlineCount 在线节点数。
	OnlineCount int `json:"onlineCount"`
	// OfflineCount 离线节点数。
	OfflineCount int `json:"offlineCount"`
}

// HistoryTrendPointResponse 历史趋势图响应点，只包含当前图表消费的字段。
type HistoryTrendPointResponse struct {
	// SampledAt 采样时间。
	SampledAt time.Time `json:"sampledAt"`
	// ElapsedSec 距任务启动的秒数。
	ElapsedSec int `json:"elapsedSec"`
	// TotalQPS 集群总 QPS。
	TotalQPS float64 `json:"totalQps"`
	// RTTApdex 按 RTT 样本数加权后的 Apdex；旧数据完成迁移前可能为空。
	RTTApdex *float64 `json:"rttApdex"`
	// TotalDurationApdex 按总耗时样本数加权后的 Apdex；旧数据未采集总耗时时为空。
	TotalDurationApdex *float64 `json:"totalDurationApdex"`
	// SendKBps 发送带宽 KB/s。
	SendKBps float64 `json:"sendKBps"`
	// RecvKBps 接收带宽 KB/s。
	RecvKBps float64 `json:"recvKBps"`
	// AvgCPUPercent 平均 CPU 使用率。
	AvgCPUPercent float64 `json:"avgCpuPercent"`
}

// TimeseriesResponse 时序数据查询响应。
type TimeseriesResponse struct {
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// Points 趋势采样点。
	Points []HistoryTrendPointResponse `json:"points"`
	// Sampled 是否经过读取侧降采样。
	Sampled bool `json:"sampled"`
	// OriginalCount 原始点数。
	OriginalCount int `json:"originalCount"`
	// MaxPoints 本次查询最大返回点数。
	MaxPoints int `json:"maxPoints"`
}

// HistoryConfigSummaryResponse 历史配置摘要响应。
type HistoryConfigSummaryResponse struct {
	TaskID      string      `json:"taskId"`
	Name        string      `json:"name"`
	TotalBots   int         `json:"totalBots"`
	RobotConfig RobotConfig `json:"robotConfig"`
}

// HistoryConfigArchiveResponse 历史完整配置归档响应。
type HistoryConfigArchiveResponse struct {
	TaskID      string            `json:"taskId"`
	Name        string            `json:"name"`
	TotalBots   int               `json:"totalBots"`
	RobotConfig RobotConfig       `json:"robotConfig"`
	FlowJSON    json.RawMessage   `json:"flowJson"`
	ProtoFiles  map[string]string `json:"protoFiles"`
	Scripts     map[string]string `json:"scripts"`
}
