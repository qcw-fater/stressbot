package admin

import (
)

// Assigner 任务分配器。
type Assigner struct{}

func NewAssigner() *Assigner {
	return &Assigner{}
}

// Assign 将任务分配给可用 Agent。
func (a *Assigner) Assign(task *Task, agents []*AgentNode) ([]Assignment, error) {
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

	return a.uniformAssign(task, available), nil
}

func (a *Assigner) uniformAssign(task *Task, agents []*AgentNode) []Assignment {
	n := len(agents)
	base := task.TotalBots / n
	rem := task.TotalBots % n

	out := make([]Assignment, 0, n)
	cursor := 0

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
