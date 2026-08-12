package admin

import (
	"testing"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func TestAcceptTaskReportDoesNotClearNewTask(t *testing.T) {
	originalLogger := stresslog.GetLogger()
	stresslog.ReplaceLogger(zap.NewNop())
	if originalLogger != nil {
		t.Cleanup(func() {
			stresslog.ReplaceLogger(originalLogger)
		})
	}

	tasks, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewTaskStore() error = %v", err)
	}
	if err := tasks.Create(&Task{
		ID:          "old-task",
		Name:        "old task",
		State:       TaskStopped,
		Assignments: []Assignment{{AgentID: "agent-1"}},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	agents := NewAgentRegistry(RegistryConfig{}, nil)
	if err := agents.Register(&AgentNode{
		ID:            "agent-1",
		Status:        AgentBusy,
		CurrentTaskID: "new-task",
		CurrentBots:   3,
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	server := &AdminServer{tasks: tasks, agents: agents}
	if err := server.acceptTaskReport(TaskCompletionReport{AgentID: "agent-1", TaskID: "old-task", Result: ResultCompleted}); err != nil {
		t.Fatalf("acceptTaskReport() error = %v", err)
	}
	node, ok := agents.Get("agent-1")
	if !ok {
		t.Fatal("agent was removed")
	}
	if node.CurrentTaskID != "new-task" {
		t.Errorf("CurrentTaskID = %q, want %q", node.CurrentTaskID, "new-task")
	}
	if node.CurrentBots != 3 {
		t.Errorf("CurrentBots = %d, want 3", node.CurrentBots)
	}
	if node.Status != AgentBusy {
		t.Errorf("Status = %q, want %q", node.Status, AgentBusy)
	}
}

func TestAgentRegistryCompleteTaskClearsMatchingTask(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	agents := NewAgentRegistry(RegistryConfig{}, nil)
	if err := agents.Register(&AgentNode{
		ID:            "agent-1",
		Status:        AgentBusy,
		CurrentTaskID: "task-a",
		CurrentBots:   3,
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	completed, err := agents.CompleteTask("agent-1", "task-a")
	if err != nil {
		t.Fatalf("CompleteTask() error = %v", err)
	}
	if !completed {
		t.Fatal("CompleteTask() completed = false, want true")
	}
	node, ok := agents.Get("agent-1")
	if !ok {
		t.Fatal("agent was removed")
	}
	if node.CurrentTaskID != "" {
		t.Errorf("CurrentTaskID = %q, want empty", node.CurrentTaskID)
	}
	if node.CurrentBots != 0 {
		t.Errorf("CurrentBots = %d, want 0", node.CurrentBots)
	}
}
