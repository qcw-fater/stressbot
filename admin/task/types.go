package task

import (
	"time"

	json "stressbot/internal/jsonx"
	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/state/shared"
)

// ── Task ──────────────────────────────────────────────

// State 表示任务生命周期状态。
type State string

const (
	// Pending 表示任务已创建，等待启动。
	Pending State = "pending"
	// Starting 表示任务正在启动（分配 Agent、推送配置）。
	Starting State = "starting"
	// Running 表示任务执行中。
	Running State = "running"
	// Stopping 表示任务正在停止（等待 Agent 上报完成）。
	Stopping State = "stopping"
	// Stopped 表示任务已正常停止。
	Stopped State = "stopped"
	// Failed 表示任务失败。
	Failed State = "failed"
)

// IsActiveState 返回该状态是否占据单例位。
func IsActiveState(s State) bool {
	return s == Starting || s == Running || s == Stopping
}

// Result 表示任务结束原因。
type Result string

const (
	// ResultCompleted 任务自然完成。
	ResultCompleted Result = "completed"
	// ResultStopped 任务被手动停止。
	ResultStopped Result = "stopped"
	// ResultFailed 任务失败。
	ResultFailed Result = "failed"
)

// Task 代表一次压测任务的完整数据。
type Task struct {
	// ID 唯一标识。
	ID string `json:"id"`
	// Name 任务名称。
	Name string `json:"name"`
	// State 当前状态。
	State State `json:"state"`
	// TotalBots 总机器人数量。
	TotalBots int `json:"totalBots"`
	// Config 任务配置（流程、proto、脚本等）。
	Config Config `json:"config"`
	// Assignments Agent 分配方案。
	Assignments []Assignment `json:"assignments,omitempty"`
	// SucceededAgents 实际成功接收任务的 Agent ID 列表（部分 Agent 可能推送失败）。
	SucceededAgents []string `json:"succeededAgents,omitempty"`
	// Reports Agent 最终完成报告，key 为 agentID。
	Reports map[string]CompletionReport `json:"reports,omitempty"`
	// CleanupSummary 所有节点最终清理状态汇总。
	CleanupSummary *robot.CleanupStatus `json:"cleanupSummary,omitempty"`
	// StageReports reset 边界阶段段落报告。
	// 仅在有 reset=true 的渐进式加压任务中产生：每次 reset 前，Agent 快照并上报该段落的累计指标，
	// StageIndex 为「即将进入的配置阶段下标」（0-based，>=1）。归档时映射为连续 1-based 段落号。
	StageReports []CompletionReport `json:"stageReports,omitempty"`
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
	// SharedUsed 任务脚本是否使用了共享状态（自动检测，require("share")）。
	SharedUsed bool `json:"sharedUsed,omitempty"`
	// SharedRunID 共享状态命名空间 runId（= 任务 ID），任务终态时据此统一清理。
	SharedRunID string `json:"sharedRunId,omitempty"`
	// FlowTemplateID 来源流程模板 ID（可选，逻辑外键）。从模板启动时记录用于溯源；
	// 历史实际配置仍以 task_config_archive.flow_json 为准，不反查此 ID 取 flow。
	FlowTemplateID string `json:"flowTemplateId,omitempty"`
}

// Config 包含创建任务时上传的所有资源。
type Config struct {
	// FlowJSON flow.json 原始内容。
	FlowJSON json.RawMessage `json:"flowJson"`
	// ProtoFiles proto 文件名 → 内容。
	ProtoFiles map[string][]byte `json:"protoFiles,omitempty"`
	// LuaScripts Lua 脚本名 → 内容。
	LuaScripts map[string][]byte `json:"luaScripts,omitempty"`
	// Codecs 每连接一份的声明式 codec 文件（文件名 → 字节，如 tcp_logic_codec.json）。
	Codecs map[string][]byte `json:"codecs,omitempty"`
	// ErrorMap 共享 errors.json 字节。
	ErrorMap []byte `json:"errorMap,omitempty"`
	// RobotConfig 任务级运行时配置。
	RobotConfig RobotConfig `json:"robotConfig"`
	// Deadline 任务截止时间，超过后 Agent 自动停止。
	Deadline *time.Time `json:"deadline,omitempty"`
}

// BundleFlowJSON implements bundle.Source without coupling task models to the bundle package.
func (c *Config) BundleFlowJSON() []byte              { return c.FlowJSON }
func (c *Config) BundleProtoFiles() map[string][]byte { return c.ProtoFiles }
func (c *Config) BundleLuaScripts() map[string][]byte { return c.LuaScripts }
func (c *Config) BundleCodecs() map[string][]byte     { return c.Codecs }
func (c *Config) BundleErrorMap() []byte              { return c.ErrorMap }

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
	// Type 事件类型："offline"（心跳超时）| "reconnected"（分配节点恢复）| "restarted"（Agent 重新注册，任务丢失）。
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

// Dispatch 表示 Admin 向 Agent 下发的任务运行参数。
type Dispatch struct {
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// TaskName 任务名称。
	TaskName string `json:"taskName"`
	// StartNumber 任务账号编号基数，所有 Agent 相同。
	StartNumber int `json:"startNumber"`
	// StartIndex 本 Agent 在任务全局机器人序号中的起点（0-based，不含 StartNumber 偏移）。
	StartIndex int `json:"startIndex"`
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
	// RampUp 渐进式加压配置（已按比例缩放）。
	RampUp *RampUpConfig `json:"rampUp,omitempty"`
	// Shared 共享状态运行时下发（含 Redis 连接与任务 runId）。
	// 仅当任务脚本使用 share 且服务器配置了 Redis 时下发，否则为 nil。
	Shared *SharedRuntimeAssignment `json:"shared,omitempty"`
}

// SharedRuntimeAssignment Admin → Agent 下发的共享状态运行时配置。
// RunID 由 Admin 统一生成（= 任务 ID），保证同一任务所有 Agent 落在同一命名空间。
//
// Redis.Password 随内网 gRPC 控制面下发；部署必须保证控制端口只在受控私网可达。
type SharedRuntimeAssignment struct {
	RunID string             `json:"runId"`
	Redis shared.RedisConfig `json:"redis"`
}

// ── Agent ─────────────────────────────────────────────

// ── 上报报文 ──────────────────────────────────────────

// CompletionReport 表示 Agent 上报的任务完成结果。
type CompletionReport struct {
	// AgentID Agent 唯一标识。
	AgentID string `json:"agentId"`
	// TaskID 任务 ID。
	TaskID string `json:"taskId"`
	// Result 完成结果。
	Result Result `json:"result"`
	// ErrorMsg 错误信息。
	ErrorMsg string `json:"errorMsg,omitempty"`
	// FinishedAt 完成时间。
	FinishedAt time.Time `json:"finishedAt"`
	// FinalSnapshot 最终压测指标快照。
	// 既可表示整体最终快照（普通/无 reset 任务），也可表示某个 reset 阶段段落结束时的快照
	// （有 reset 任务：Agent 在 reset 前快照并随后 Reset 采集器，故该快照仅覆盖当前段落）。
	FinalSnapshot *monitor.CollectorSnapshot `json:"finalSnapshot"`
	// StageIndex 阶段段落标识。
	// -1 / 0：最终（兼容）报告，不作为阶段段落报告；
	// > 0：有 reset 的渐进式加压任务中，reset 边界产生的阶段段落报告，
	//      值为「即将进入的配置阶段下标」（0-based），归档时映射为连续 1-based 段落号。
	StageIndex int `json:"stageIndex,omitempty"`
	// CleanupStatus Agent 侧资源清理结果。
	CleanupStatus *robot.CleanupStatus `json:"cleanupStatus,omitempty"`
}
