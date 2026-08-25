// Package task 提供 Admin 侧压测任务的存储、状态机流转、Agent 分配与完成汇报汇总。
package task

import (
	"maps"
	"time"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

// Store 保存 Admin 任务并执行其生命周期状态转换。
type Store struct {
	*Repository[*Task, State]
}

// NewStore 从指定目录加载 Admin 任务。
func NewStore(dataDir string) (*Store, error) {
	store, err := NewRepository(dataDir, Adapter[*Task, State]{
		ID:       func(task *Task) string { return task.ID },
		State:    func(task *Task) State { return task.State },
		SetState: func(task *Task, state State) { task.State = state },
		IsActive: IsActiveState,
		Recover: func(task *Task, now time.Time) {
			task.State = Failed
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
	return &Store{Repository: store}, nil
}

// StartTask 将任务从 Pending 推进到 Starting（校验状态机合法性与单活跃任务约束）。
func (s *Store) StartTask(id string) (*Task, error) {
	return s.Begin(id, Pending, Starting)
}

func cloneTaskForRead(task *Task) *Task {
	clone := *task
	if task.Reports != nil {
		clone.Reports = make(map[string]CompletionReport, len(task.Reports))
		maps.Copy(clone.Reports, task.Reports)
	}
	clone.Assignments = append([]Assignment(nil), task.Assignments...)
	clone.SucceededAgents = append([]string(nil), task.SucceededAgents...)
	clone.StageReports = append([]CompletionReport(nil), task.StageReports...)
	clone.AgentEvents = append([]AgentEvent(nil), task.AgentEvents...)
	return &clone
}

func cloneTaskForTerminal(task *Task) *Task {
	var clone Task
	data, err := json.Marshal(task)
	if err != nil {
		return cloneTaskForRead(task)
	}
	if err := json.Unmarshal(data, &clone); err != nil {
		return cloneTaskForRead(task)
	}
	return &clone
}
