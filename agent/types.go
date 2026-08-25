package agent

// Status 表示 Agent 运行状态。
type Status string

// Agent 运行状态枚举：StatusIdle 空闲（可接收新任务），StatusBusy 有任务执行中。
const (
	StatusIdle Status = "idle"
	StatusBusy Status = "busy"
)
