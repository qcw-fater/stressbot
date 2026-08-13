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

// SplitGlobalValues distributes a global concurrency value in proportion to
// assigned bots while preserving the exact total.
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
