package agent

// AgentStatus Agent 运行状态。
type AgentStatus string

const (
	StatusIdle AgentStatus = "idle"
	StatusBusy AgentStatus = "busy"
)
