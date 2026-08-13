package task

import (
	"maps"
	"time"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

type TaskStore struct {
	*Store[*Task, TaskState]
}

func NewTaskStore(dataDir string) (*TaskStore, error) {
	store, err := NewStore(dataDir, Adapter[*Task, TaskState]{
		ID:       func(task *Task) string { return task.ID },
		State:    func(task *Task) TaskState { return task.State },
		SetState: func(task *Task, state TaskState) { task.State = state },
		IsActive: IsActiveState,
		Recover: func(task *Task, now time.Time) {
			task.State = TaskFailed
			task.ErrorMsg = "admin restart, task lost"
			task.StoppedAt = &now
		},
		SetStartedAt:  func(task *Task, now time.Time) { task.StartedAt = &now },
		SetStoppedAt:  func(task *Task, now time.Time) { task.StoppedAt = &now },
		Clone:         cloneTaskForRead,
		TerminalClone: cloneTaskForTerminal,
		ConflictDetails: func(task *Task) map[string]any {
			return map[string]any{
				"activeTaskId": task.ID,
				"activeName":   task.Name,
				"activeState":  string(task.State),
				"startedAt":    task.StartedAt,
			}
		},
		Load:   loadTaskFiles,
		Save:   saveTaskFile,
		Remove: removeTaskFile,
		Duplicate: func() error {
			return apierror.ErrTaskConflict.WithMessage("task already exists")
		},
		NotFound: func() error { return apierror.ErrTaskNotFound },
		InvalidState: func(message string) error {
			return apierror.ErrTaskInvalidState.WithMessage(message)
		},
		Conflict: func(details map[string]any) error {
			return apierror.ErrTaskConflict.WithDetails(details)
		},
	})
	if err != nil {
		return nil, err
	}
	return &TaskStore{Store: store}, nil
}

func (s *TaskStore) StartTask(id string) (*Task, error) {
	return s.Begin(id, TaskPending, TaskStarting)
}

func cloneTaskForRead(task *Task) *Task {
	copy := *task
	if task.Reports != nil {
		copy.Reports = make(map[string]TaskCompletionReport, len(task.Reports))
		maps.Copy(copy.Reports, task.Reports)
	}
	copy.Assignments = append([]Assignment(nil), task.Assignments...)
	copy.SucceededAgents = append([]string(nil), task.SucceededAgents...)
	copy.StageReports = append([]TaskCompletionReport(nil), task.StageReports...)
	copy.AgentEvents = append([]AgentEvent(nil), task.AgentEvents...)
	return &copy
}

func cloneTaskForTerminal(task *Task) *Task {
	var copy Task
	data, err := json.Marshal(task)
	if err != nil {
		return cloneTaskForRead(task)
	}
	if err := json.Unmarshal(data, &copy); err != nil {
		return cloneTaskForRead(task)
	}
	return &copy
}
