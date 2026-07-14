package admin

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
	var available []*AgentNode
	for _, ag := range agents {
		if ag.Status == AgentIdle && ag.MaxBots > 0 {
			available = append(available, ag)
		}
	}
	if len(available) == 0 {
		return nil, ErrCapacityExceeded.WithMessage("no idle agents available")
	}

	var totalCapacity int
	for _, ag := range available {
		totalCapacity += ag.MaxBots
	}
	if totalCapacity < task.TotalBots {
		return nil, ErrCapacityExceeded.WithDetails(map[string]any{
			"required":        task.TotalBots,
			"available":       totalCapacity,
			"availableAgents": len(available),
		})
	}

	if startNumber < 0 {
		startNumber = 0
	}

	// 调试模式：优先分配到单个 agent
	if task.Config.RobotConfig.DebugMode {
		var best *AgentNode
		for _, ag := range available {
			if ag.MaxBots >= task.TotalBots {
				if best == nil || ag.MaxBots < best.MaxBots {
					best = ag
				}
			}
		}
		if best != nil {
			return []Assignment{{
				TaskID:      task.ID,
				AgentID:     best.ID,
				AgentName:   best.Name,
				StartNumber: startNumber,
				TotalBots:   task.TotalBots,
			}}, nil
		}
		// 无单 agent 容量够 → 降级为比例分配
	}

	return a.proportionalAssign(task, available, startNumber), nil
}

// assignmentStartIndex 返回分片在任务全局机器人序号中的起点，不包含账号编号基数偏移。
func assignmentStartIndex(assignment Assignment, taskStartNumber int) int {
	return assignment.StartNumber - taskStartNumber
}

// proportionalAssign 按 maxBots 比例分配，保证 sum(bots) == totalBots。
func (a *Assigner) proportionalAssign(task *Task, agents []*AgentNode, startNumber int) []Assignment {
	totalCapacity := 0
	for _, ag := range agents {
		totalCapacity += ag.MaxBots
	}

	n := len(agents)
	bots := make([]int, n)
	assigned := 0

	for i, ag := range agents {
		bots[i] = task.TotalBots * ag.MaxBots / totalCapacity
		assigned += bots[i]
	}

	// 余数按最大剩余小数分配
	remainder := task.TotalBots - assigned
	for i := 0; i < remainder; i++ {
		bestIdx := 0
		bestFrac := -1.0
		for j, ag := range agents {
			exact := float64(task.TotalBots) * float64(ag.MaxBots) / float64(totalCapacity)
			frac := exact - float64(bots[j])
			if frac > bestFrac {
				bestFrac = frac
				bestIdx = j
			}
		}
		bots[bestIdx]++
	}

	out := make([]Assignment, 0, n)
	cursor := startNumber
	for i, ag := range agents {
		out = append(out, Assignment{
			TaskID:      task.ID,
			AgentID:     ag.ID,
			AgentName:   ag.Name,
			StartNumber: cursor,
			TotalBots:   bots[i],
		})
		cursor += bots[i]
	}
	return out
}
