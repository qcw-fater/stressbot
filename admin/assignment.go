package admin

import (
)

// Assigner 任务分配器。
type Assigner struct{}

func NewAssigner() *Assigner {
	return &Assigner{}
}

// Assign 将任务分配给可用 Agent。
//
// startNumber 为账号编号起点（来自 task.Config.RobotConfig.StartNumber）。
// 各 agent 的 Assignment.StartNumber 在该起点上累加，最终账号 =
// AccountPrefix + (startNumber + 全局序号)。
func (a *Assigner) Assign(task *Task, agents []*AgentNode, startNumber int) ([]Assignment, error) {
	// 过滤可用 Agent
	var available []*AgentNode
	for _, ag := range agents {
		if ag.Status == AgentIdle && ag.MaxBots > 0 {
			available = append(available, ag)
		}
	}
	if len(available) == 0 {
		return nil, ErrCapacityExceeded.WithMessage("no idle agents available")
	}

	// 容量校验
	var totalCapacity int
	for _, ag := range available {
		totalCapacity += ag.MaxBots
	}
	if totalCapacity < task.TotalBots {
		return nil, ErrCapacityExceeded.WithDetails(map[string]any{
			"required":       task.TotalBots,
			"available":      totalCapacity,
			"availableAgents": len(available),
		})
	}

	if startNumber < 0 {
		startNumber = 0 // 防御：负数没有业务意义，强制归 0
	}
	return a.uniformAssign(task, available, startNumber), nil
}

func (a *Assigner) uniformAssign(task *Task, agents []*AgentNode, startNumber int) []Assignment {
	n := len(agents)
	base := task.TotalBots / n
	rem := task.TotalBots % n

	out := make([]Assignment, 0, n)
	cursor := startNumber

	for i, agent := range agents {
		bots := base
		if i < rem {
			bots++
		}
		out = append(out, Assignment{
			TaskID:      task.ID,
			AgentID:     agent.ID,
			AgentName:   agent.Name,
			StartNumber: cursor,
			TotalBots:   bots,
		})
		cursor += bots
	}
	return out
}
