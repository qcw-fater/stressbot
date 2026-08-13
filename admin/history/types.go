package history

import (
	"time"

	"stressbot/admin/metrics"
	admintask "stressbot/admin/task"
	json "stressbot/internal/jsonx"
	"stressbot/monitor"
	"stressbot/robot"
)

func buildStagePlan(cfg *admintask.RampUpConfig) admintask.StagePlan {
	return admintask.BuildStagePlan(cfg)
}

// ── 历史归档 ─────────────────────────────────────────

// HistoryRecord 历史任务列表中的单条记录。
type HistoryRecord struct {
	// ID 任务 ID。
	ID string `json:"id"`
	// Name 任务名称。
	Name string `json:"name"`
	// State 最终状态。
	State admintask.TaskState `json:"state"`
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
	// DebugMode 是否为调试模式任务。
	DebugMode bool `json:"debugMode"`

	// ConfigSummary 配置摘要。
	ConfigSummary ConfigSummary `json:"configSummary"`
	// StageCount 渐进式加压阶段数（0 表示一次性创建）。
	StageCount int `json:"stageCount,omitempty"`

	// ── 阶段历史展示字段（虚拟，不落 task_history 子行）──

	// RecordKind 记录类型："task"（父任务，默认/空）或 "stage"（阶段段落子记录）。
	RecordKind string `json:"recordKind,omitempty"`
	// ParentID 阶段段落子记录所属父任务 ID。
	ParentID string `json:"parentId,omitempty"`
	// StageIndex 阶段段落连续 1-based 段落号（仅 RecordKind=="stage"）。
	StageIndex int `json:"stageIndex,omitempty"`
	// StageLabel 段落展示标签，如「第 2 轮 · S3-S4」。
	StageLabel string `json:"stageLabel,omitempty"`
	// StageFrom/StageTo 段落覆盖的配置阶段范围（1-based，含端点）。
	StageFrom int `json:"stageFrom,omitempty"`
	StageTo   int `json:"stageTo,omitempty"`
	// HasResetStages 父任务是否含 reset 阶段（决定列表是否展开为阶段组）。
	HasResetStages bool `json:"hasResetStages,omitempty"`
	// Children 阶段段落子记录（仅有 reset 的父任务且 includeStages 时填充）。
	Children []HistoryRecord `json:"children,omitempty"`

	// ── 阶段段落指标摘要（从 task_aggregated 提取，仅 recordKind=="stage" 有值）──

	// TotalActions 阶段段落总动作采样数。
	TotalActions int `json:"totalActions,omitempty"`
	// SuccessRate 阶段段落整体成功率（0-1）。
	SuccessRate float64 `json:"successRate,omitempty"`
	// AvgRttMs 阶段段落加权平均 RTT（毫秒）。
	AvgRttMs *float64 `json:"avgRttMs,omitempty"`
	// P95RttMs 阶段段落加权平均 P95 RTT（毫秒）。
	P95RttMs *float64 `json:"p95RttMs,omitempty"`
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
	Assignments []admintask.Assignment `json:"assignments"`
	// AgentReports 各 Agent 完成报告。
	AgentReports []HistoryAgentReport `json:"agentReports"`
	// AgentEvents Agent 状态变化事件。
	AgentEvents []admintask.AgentEvent `json:"agentEvents,omitempty"`
	// FinalSnapshot 最终聚合压测指标。
	FinalSnapshot monitor.CollectorSnapshot `json:"finalSnapshot"`
	// FinalSystem 最终聚合系统指标。
	FinalSystem metrics.ClusterSystemSnapshot `json:"finalSystem"`
}

// HistoryDetailResponse 历史详情页展示响应，只包含界面实际消费的数据。
type HistoryDetailResponse struct {
	HistoryRecord
	// AgentReports 各节点完成结果摘要。
	AgentReports []HistoryAgentReportSummary `json:"agentReports"`
	// AgentEvents 节点状态变化事件。
	AgentEvents []admintask.AgentEvent `json:"agentEvents,omitempty"`
	// FinalSnapshot 最终聚合压测指标摘要。
	FinalSnapshot HistoryStressSnapshotSummary `json:"finalSnapshot"`
	// FinalSystem 最终聚合系统指标摘要。
	FinalSystem HistorySystemSummary `json:"finalSystem"`
}

// HistoryStressSnapshotSummary 历史详情页使用的压测指标摘要。
type HistoryStressSnapshotSummary struct {
	Timestamp    time.Time                  `json:"timestamp"`
	UptimeSec    float64                    `json:"uptimeSeconds"`
	TotalActions int64                      `json:"totalActions"`
	ApdexT       int                        `json:"apdexT"`
	TimingDetail monitor.TimingDetailLevel  `json:"timingDetail"`
	Summary      HistoryMetricsSummary      `json:"summary"`
	Robots       monitor.RobotSnapshot      `json:"robots"`
	Connections  monitor.ConnectionSnapshot `json:"connections"`
	Bandwidth    monitor.BandwidthSnapshot  `json:"bandwidth"`
	Actions      []HistoryActionSummary     `json:"actions"`
}

// HistoryMetricsSummary 是历史详情保留的跨动作统一汇总。
type HistoryMetricsSummary struct {
	SampleCount               int64                   `json:"sampleCount"`
	SuccessCount              int64                   `json:"successCount"`
	FailureCount              int64                   `json:"failureCount"`
	TimeoutCount              int64                   `json:"timeoutCount"`
	CanceledCount             int64                   `json:"canceledCount"`
	Executing                 int64                   `json:"executing"`
	SuccessRate               float64                 `json:"successRate"`
	RTTApdex                  float64                 `json:"rttApdex"`
	RTTApdexSampleCount       int64                   `json:"rttApdexSampleCount"`
	RTT                       HistoryHistogramSummary `json:"rtt"`
	ListenWait                HistoryHistogramSummary `json:"listenWait"`
	TotalDuration             HistoryHistogramSummary `json:"totalDuration"`
	ClientAvgMs               float64                 `json:"nonRTTAvgMs"`
	BuildAvgMs                float64                 `json:"buildAvgMs"`
	EncodeAvgMs               float64                 `json:"encodeAvgMs"`
	SendAvgMs                 float64                 `json:"sendAvgMs"`
	DecodeWaitAvgMs           float64                 `json:"decodeWaitAvgMs"`
	DecodeAvgMs               float64                 `json:"decodeAvgMs"`
	DispatchToActionWaitAvgMs float64                 `json:"dispatchToActionWaitAvgMs"`
	ParseStoreAvgMs           float64                 `json:"parseStoreAvgMs"`
	AvgQPS                    float64                 `json:"avgQps"`
}

// HistoryActionSummary 历史 action 表格和报告使用的展示字段。
type HistoryActionSummary struct {
	Name                      string                  `json:"name"`
	SampleCount               int64                   `json:"sampleCount"`
	SuccessCount              int64                   `json:"successCount"`
	FailureCount              int64                   `json:"failureCount"`
	TimeoutCount              int64                   `json:"timeoutCount"`
	CanceledCount             int64                   `json:"canceledCount"`
	Executing                 int64                   `json:"executing"`
	SuccessRate               float64                 `json:"successRate"`
	AvgSendBytes              float64                 `json:"avgSendBytes"`
	AvgRecvBytes              float64                 `json:"avgRecvBytes"`
	Kind                      monitor.ActionKind      `json:"kind"`
	RTTApdex                  float64                 `json:"rttApdex"`
	RTTApdexSampleCount       int64                   `json:"rttApdexSampleCount"`
	RTT                       HistoryHistogramSummary `json:"rtt"`
	ListenWait                HistoryHistogramSummary `json:"listenWait"`
	ListenWaitSampleCount     int64                   `json:"listenWaitSampleCount"`
	ListenTimeoutRate         float64                 `json:"listenTimeoutRate"`
	TotalDuration             HistoryHistogramSummary `json:"totalDuration"`
	ClientAvgMs               float64                 `json:"nonRTTAvgMs"`
	BuildAvgMs                float64                 `json:"buildAvgMs"`
	EncodeAvgMs               float64                 `json:"encodeAvgMs"`
	SendAvgMs                 float64                 `json:"sendAvgMs"`
	DecodeWaitAvgMs           float64                 `json:"decodeWaitAvgMs"`
	DecodeAvgMs               float64                 `json:"decodeAvgMs"`
	DispatchToActionWaitAvgMs float64                 `json:"dispatchToActionWaitAvgMs"`
	ParseStoreAvgMs           float64                 `json:"parseStoreAvgMs"`
	RTTSampleCount            int64                   `json:"rttSampleCount"`
	TotalDurationSampleCount  int64                   `json:"totalDurationSampleCount"`
	AvgQPS                    float64                 `json:"avgQps"`
	Errors                    []monitor.ErrorEntry    `json:"errors,omitempty"`
}

// HistoryHistogramSummary 历史界面需要的 RTT 分位摘要。
type HistoryHistogramSummary struct {
	Count int64    `json:"count"`
	MinMs *float64 `json:"minMs"`
	MaxMs *float64 `json:"maxMs"`
	AvgMs *float64 `json:"avgMs"`
	P50Ms *float64 `json:"p50Ms"`
	P90Ms *float64 `json:"p90Ms"`
	P95Ms *float64 `json:"p95Ms"`
	P99Ms *float64 `json:"p99Ms"`
}

// HistorySystemSummary 历史详情页使用的集群系统资源摘要。
type HistorySystemSummary struct {
	AvgCPUPercent    float64  `json:"avgCpuPercent"`
	MaxCPUPercent    float64  `json:"maxCpuPercent"`
	HotAgentName     string   `json:"hotAgentName,omitempty"`
	AvgMemPercent    float64  `json:"avgMemPercent"`
	MaxMemPercent    float64  `json:"maxMemPercent"`
	HotMemAgentName  string   `json:"hotMemAgentName,omitempty"`
	TotalMemMB       uint64   `json:"totalMemMB"`
	UsedMemMB        uint64   `json:"usedMemMB"`
	TotalNetSendKBps *float64 `json:"totalNetSendKBps"`
	TotalNetRecvKBps *float64 `json:"totalNetRecvKBps"`
	TotalGoroutines  int      `json:"totalGoroutines"`
	TotalThreads     int32    `json:"totalThreads"`
	TotalFDs         int32    `json:"totalFds"`
}

// HistoryAgentReport 单个 Agent 的历史完成报告。
type HistoryAgentReport struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// AgentName 显示名称。
	AgentName string `json:"agentName"`
	// Result 完成结果。
	Result admintask.TaskResult `json:"result"`
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
	Result        admintask.TaskResult `json:"result"`
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
	// IncludeStages 是否为有 reset 的渐进式加压父记录展开阶段段落子记录。
	IncludeStages bool
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
	// ParentID 阶段段落对比项所属父任务 ID（整体项为空）。
	ParentID string `json:"parentId,omitempty"`
	// StageIndex 对比项阶段段落号；-1 表示整体/最终。
	StageIndex int `json:"stageIndex,omitempty"`
	// StageLabel 阶段段落展示标签。
	StageLabel string `json:"stageLabel,omitempty"`
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
	Kind                     monitor.ActionKind      `json:"kind"`
	RTTApdex                 float64                 `json:"rttApdex"`
	RTT                      HistoryHistogramSummary `json:"rtt"`
	RTTSampleCount           int64                   `json:"rttSampleCount"`
	ListenWait               HistoryHistogramSummary `json:"listenWait"`
	ListenWaitSampleCount    int64                   `json:"listenWaitSampleCount"`
	TotalDuration            HistoryHistogramSummary `json:"totalDuration"`
	TotalDurationSampleCount int64                   `json:"totalDurationSampleCount"`
	// AvgSendBytes 仅用于给缺 Kind 的老归档就地推断类别（有发送字节即发送类）。
	AvgSendBytes float64 `json:"avgSendBytes"`
}

// CompareDiff 历史任务对比差异。
type CompareDiff struct {
	// Actions action 名称 → 各任务的 P99 延迟数组。
	Actions map[string][]*float64 `json:"actions"`
}

// ── 时序采样 ─────────────────────────────────────────

// HistoryTrendPointResponse 历史趋势图响应点。
type HistoryTrendPointResponse struct {
	// SampledAt 采样时间。
	SampledAt time.Time `json:"sampledAt"`
	// ElapsedSec 距任务启动的秒数。
	ElapsedSec int `json:"elapsedSec"`
	// StageIndex 采样点所属阶段/阶段段落索引。
	// -1：非渐进式或未记录；> 0：有 reset 任务中该采样点所属的连续 1-based 段落号。
	StageIndex  int       `json:"stageIndex"`
	WindowFrom  time.Time `json:"windowFrom"`
	WindowTo    time.Time `json:"windowTo"`
	SampleCount int64     `json:"sampleCount"`
	// TotalQPS 集群总 QPS。
	TotalQPS float64 `json:"totalQps"`
	// RTTApdex 按 RTT 样本数计算；当前窗口没有 Apdex 样本时为空。
	RTTApdex *float64 `json:"rttApdex"`
	// ListenWaitP99Ms 由合并后的等待分布计算；当前窗口没有等待样本时为空。
	ListenWaitP99Ms *float64 `json:"listenWaitP99Ms"`
	// RTTAvgMs 平均 RTT。
	RTTAvgMs *float64 `json:"rttAvgMs"`
	RTTP50Ms *float64 `json:"rttP50Ms"`
	RTTP90Ms *float64 `json:"rttP90Ms"`
	// RTTP95Ms P95 RTT。
	RTTP95Ms *float64 `json:"rttP95Ms"`
	// RTTP99Ms P99 RTT。
	RTTP99Ms           *float64 `json:"rttP99Ms"`
	ActiveConnections  *int64   `json:"activeConnections"`
	ClosedConnections  *int64   `json:"closedConnections"`
	DroppedConnections *int64   `json:"droppedConnections"`
	NetSendBytesPerSec *float64 `json:"netSendBytesPerSec"`
	NetRecvBytesPerSec *float64 `json:"netRecvBytesPerSec"`
	AssignedAgents     *int     `json:"assignedAgents"`
	ReportingAgents    *int     `json:"reportingAgents"`
	ReportingCoverage  *float64 `json:"reportingCoverage"`
	// TotalDurationAvgMs 平均总耗时。
	TotalDurationAvgMs *float64 `json:"totalDurationAvgMs"`
	// TotalDurationP95Ms P95 总耗时。
	TotalDurationP95Ms *float64 `json:"totalDurationP95Ms"`
	// TotalDurationP99Ms P99 总耗时。
	TotalDurationP99Ms *float64 `json:"totalDurationP99Ms"`
	// ClientAvgMs 客户端平均耗时。
	ClientAvgMs *float64 `json:"nonRTTAvgMs"`
	// EncodeAvgMs 编码平均耗时。
	EncodeAvgMs *float64 `json:"encodeAvgMs"`
	// DecodeAvgMs 解码平均耗时。
	DecodeAvgMs *float64 `json:"decodeAvgMs"`
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
	// AvgMemPercent 集群内存使用率（按总内存加权）。
	AvgMemPercent float64 `json:"avgMemPercent"`
	// MaxMemPercent 最高节点内存使用率。
	MaxMemPercent float64 `json:"maxMemPercent"`
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
	TaskID      string                `json:"taskId"`
	Name        string                `json:"name"`
	TotalBots   int                   `json:"totalBots"`
	RobotConfig admintask.RobotConfig `json:"robotConfig"`
}

// HistoryConfigArchiveResponse 历史完整配置归档响应。
type HistoryConfigArchiveResponse struct {
	TaskID      string                `json:"taskId"`
	Name        string                `json:"name"`
	TotalBots   int                   `json:"totalBots"`
	RobotConfig admintask.RobotConfig `json:"robotConfig"`
	FlowJSON    json.RawMessage       `json:"flowJson"`
	ProtoFiles  map[string]string     `json:"protoFiles"`
	Scripts     map[string]string     `json:"scripts"`
}
