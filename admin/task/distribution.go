package task

import (
	"fmt"
	"math"

	"stressbot/admin/apierror"
)

// ExpectedAgents returns the agents allowed to submit completion reports.
func ExpectedAgents(task *Task) map[string]struct{} {
	out := make(map[string]struct{})
	if task == nil {
		return out
	}
	if len(task.SucceededAgents) > 0 {
		for _, agentID := range task.SucceededAgents {
			out[agentID] = struct{}{}
		}
		return out
	}
	for _, assignment := range task.Assignments {
		out[assignment.AgentID] = struct{}{}
	}
	return out
}

// ValidateDistributedConcurrency ensures every assigned agent can receive at
// least one concurrency slot when concurrency is explicitly configured.
func ValidateDistributedConcurrency(cfg RobotConfig, assignments []Assignment) error {
	agentCount := assignedAgentCount(assignments)
	if agentCount <= 1 {
		return nil
	}
	if cfg.Concurrency > 0 && cfg.Concurrency < agentCount {
		return apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("robotConfig.concurrency (%d) must be 0 or >= assigned agents (%d)", cfg.Concurrency, agentCount))
	}
	if cfg.RampUp == nil {
		return nil
	}
	for index, stage := range cfg.RampUp.Stages {
		if stage.Concurrency > 0 && stage.Concurrency < agentCount {
			return apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("rampUp.stages[%d].concurrency (%d) must be 0 or >= assigned agents (%d)", index, stage.Concurrency, agentCount))
		}
	}
	return nil
}

func assignedAgentCount(assignments []Assignment) int {
	count := 0
	for _, assignment := range assignments {
		if assignment.TotalBots > 0 {
			count++
		}
	}
	return count
}

// SplitGlobalValues 按各节点分得的机器人数等比拆分一个全局值（并发度），总和守恒。
//
// 每个分到机器人的节点保底 1（0 并发的节点一个机器人也起不来）。保底在机器人分布
// 倾斜时会把总和顶过 global——例如 global=10、10 个节点、其中一个占 91% 机器人时，
// 下取整得 9 + 9 个保底 = 18。因此下取整之后既要按最大余数补齐不足，也要从分得
// 最多的节点回收超发，两个方向都走完才能保证总和恰好等于 global。
//
// global 小于有效节点数时两个目标无法兼得（回收到每个节点都只剩 1 仍然超发），
// 此时保底优先、总和上浮到节点数。ValidateDistributedConcurrency 已在入口挡掉
// 显式配置的这种情况，这里只是兜住内部调用。
func SplitGlobalValues(global, totalBots int, assignments []Assignment) map[string]int {
	out := make(map[string]int, len(assignments))
	if global <= 0 || totalBots <= 0 || len(assignments) == 0 {
		return out
	}

	used := 0
	fractions := make([]float64, len(assignments))
	for index, assignment := range assignments {
		if assignment.TotalBots <= 0 {
			continue
		}
		exact := float64(global) * float64(assignment.TotalBots) / float64(totalBots)
		floor := max(int(math.Floor(exact)), 1)
		out[assignment.AgentID] = floor
		used += floor
		fractions[index] = exact - math.Floor(exact)
	}

	// 不足：按小数部分从大到小各补 1。
	for remainder := global - used; remainder > 0; remainder-- {
		bestIndex := -1
		bestFraction := -1.0
		for index, assignment := range assignments {
			if assignment.TotalBots > 0 && fractions[index] > bestFraction {
				bestFraction = fractions[index]
				bestIndex = index
			}
		}
		if bestIndex < 0 {
			break
		}
		out[assignments[bestIndex].AgentID]++
		fractions[bestIndex] = -1
	}

	// 超发：从当前分得最多的节点逐个回收，不把任何节点压到 1 以下。
	for surplus := used - global; surplus > 0; surplus-- {
		richestIndex := -1
		richest := 1
		for index, assignment := range assignments {
			if assignment.TotalBots <= 0 {
				continue
			}
			if value := out[assignment.AgentID]; value > richest {
				richest = value
				richestIndex = index
			}
		}
		if richestIndex < 0 {
			break // 全部已降到保底 1，无法继续回收
		}
		out[assignments[richestIndex].AgentID]--
	}
	return out
}

// ScaleRampUp scales stage counts for one agent while preserving the exact
// assigned bot total and the remaining stage semantics.
func ScaleRampUp(cfg *RampUpConfig, totalBots, assignedBots int, assignments []Assignment, agentID string) *RampUpConfig {
	if cfg == nil || totalBots <= 0 || assignedBots == totalBots || len(cfg.Stages) == 0 {
		return cfg
	}
	if assignedBots <= 0 {
		scaled := &RampUpConfig{Stages: make([]RampUpStage, 0, len(cfg.Stages))}
		for _, stage := range cfg.Stages {
			scaled.Stages = append(scaled.Stages, RampUpStage{Concurrency: stage.Concurrency, Reset: stage.Reset, HoldSec: stage.HoldSec})
		}
		return scaled
	}

	counts := make([]int, len(cfg.Stages))
	fractions := make([]float64, len(cfg.Stages))
	used := 0
	for index, stage := range cfg.Stages {
		exact := float64(stage.Count) * float64(assignedBots) / float64(totalBots)
		floor := max(int(math.Floor(exact)), 0)
		counts[index] = floor
		fractions[index] = exact - float64(floor)
		used += floor
	}
	for remainder := assignedBots - used; remainder > 0; remainder-- {
		bestIndex := -1
		bestFraction := -1.0
		for index, fraction := range fractions {
			if fraction > bestFraction {
				bestFraction = fraction
				bestIndex = index
			}
		}
		if bestIndex < 0 {
			break
		}
		counts[bestIndex]++
		fractions[bestIndex] = -1
	}

	scaled := &RampUpConfig{Stages: make([]RampUpStage, 0, len(cfg.Stages))}
	for index, stage := range cfg.Stages {
		concurrency := stage.Concurrency
		if concurrency > 0 {
			concurrency = SplitGlobalValues(concurrency, totalBots, assignments)[agentID]
		}
		scaled.Stages = append(scaled.Stages, RampUpStage{
			Count: counts[index], Concurrency: concurrency, Reset: stage.Reset, HoldSec: stage.HoldSec,
		})
	}
	return scaled
}
