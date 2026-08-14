package agent

// Status 表示 Agent 运行状态。
type Status string

const (
	StatusIdle Status = "idle"
	StatusBusy Status = "busy"
)
