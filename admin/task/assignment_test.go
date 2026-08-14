package task

import (
	"testing"

	"stressbot/admin/agent"
)

func TestAssignmentStartIndexUsesTaskGlobalOrdinal(t *testing.T) {
	task := &Task{ID: "task-global-index", TotalBots: 10}
	agents := []*agent.Node{
		{ID: "agent-a", Name: "A", Status: agent.Idle, MaxBots: 6},
		{ID: "agent-b", Name: "B", Status: agent.Idle, MaxBots: 4},
	}

	assignments, err := NewAssigner().Assign(task, agents, 100)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("len(assignments) = %d, want 2", len(assignments))
	}

	wantStartNumbers := []int{100, 106}
	wantStartIndexes := []int{0, 6}
	for i, assignment := range assignments {
		if assignment.StartNumber != wantStartNumbers[i] {
			t.Errorf("assignments[%d].StartNumber = %d, want %d", i, assignment.StartNumber, wantStartNumbers[i])
		}
		if got := AssignmentStartIndex(assignment, 100); got != wantStartIndexes[i] {
			t.Errorf("AssignmentStartIndex(assignments[%d]) = %d, want %d", i, got, wantStartIndexes[i])
		}
	}
}

func TestAssignmentStartIndexIsZeroForDebugSingleAgent(t *testing.T) {
	task := &Task{
		ID:        "task-debug-index",
		TotalBots: 3,
		Config: Config{RobotConfig: RobotConfig{
			DebugMode: true,
		}},
	}
	agents := []*agent.Node{
		{ID: "agent-a", Name: "A", Status: agent.Idle, MaxBots: 10},
	}

	assignments, err := NewAssigner().Assign(task, agents, 50)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if got := AssignmentStartIndex(assignments[0], 50); got != 0 {
		t.Fatalf("AssignmentStartIndex() = %d, want 0", got)
	}
}
