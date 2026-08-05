package admin

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// TaskStore 任务存储（内存 + JSON 文件持久化）。
type TaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*Task

	// 单例约束
	startMu  sync.Mutex
	activeID string

	dataDir string

	// 终态回调（用于触发归档）
	onTerminal func(task *Task)

	// Admin 重启时恢复的活跃任务 ID（SetOnTerminal 后触发归档）
	recoveredIDs []string
}

func NewTaskStore(dataDir string) (*TaskStore, error) {
	ts := &TaskStore{
		tasks:   make(map[string]*Task),
		dataDir: dataDir,
	}

	// 从文件恢复
	tasks, err := loadTaskFiles(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}

	now := time.Now()
	var recoveredIDs []string
	for _, t := range tasks {
		if IsActiveState(t.State) {
			// 活跃任务在 Admin 重启后重置为 failed
			stresslog.Info("恢复活跃任务为 failed",
				zap.String("taskID", t.ID),
				zap.String("oldState", string(t.State)))
			t.State = TaskFailed
			t.ErrorMsg = "admin restart, task lost"
			t.StoppedAt = &now
			recoveredIDs = append(recoveredIDs, t.ID)
		}
		ts.tasks[t.ID] = t
	}

	// 记录需要归档的恢复任务（SetOnTerminal 后触发）
	ts.recoveredIDs = recoveredIDs

	return ts, nil
}

// SetOnTerminal 设置终态回调，并触发 Admin 重启后恢复任务的归档。
func (ts *TaskStore) SetOnTerminal(fn func(task *Task)) {
	ts.onTerminal = fn
	// 触发恢复任务的归档
	for _, id := range ts.recoveredIDs {
		if t, ok := ts.tasks[id]; ok {
			ts.onTerminal(t)
		}
	}
	ts.recoveredIDs = nil
}

// Create 创建任务。
func (ts *TaskStore) Create(t *Task) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if _, exists := ts.tasks[t.ID]; exists {
		return ErrTaskConflict.WithMessage("task already exists")
	}
	ts.tasks[t.ID] = t
	return saveTaskFile(ts.dataDir, t)
}

// cloneForRead 返回 Task 的读安全副本：在浅拷贝基础上，对会被 Update 回调就地改写的
// 引用型集合字段（Reports map、Assignments/SucceededAgents/StageReports/AgentEvents 切片）
// 做一层拷贝，避免调用方遍历副本时与持锁的 Update 并发读写同一底层容器触发 fatal data race。
// StartedAt/StoppedAt 等指针字段在 Update 内以「整体重新赋值」方式更新（不就地改写指向对象），
// 浅拷贝持有旧指针值即为一致快照，无需深拷。CleanupSummary 亦为一次性整体赋值。
// 调用方必须持有 ts.mu（读锁或写锁）。
func cloneForRead(t *Task) *Task {
	cp := *t
	if t.Reports != nil {
		cp.Reports = make(map[string]TaskCompletionReport, len(t.Reports))
		maps.Copy(cp.Reports, t.Reports)
	}
	if t.Assignments != nil {
		cp.Assignments = append([]Assignment(nil), t.Assignments...)
	}
	if t.SucceededAgents != nil {
		cp.SucceededAgents = append([]string(nil), t.SucceededAgents...)
	}
	if t.StageReports != nil {
		cp.StageReports = append([]TaskCompletionReport(nil), t.StageReports...)
	}
	if t.AgentEvents != nil {
		cp.AgentEvents = append([]AgentEvent(nil), t.AgentEvents...)
	}
	return &cp
}

// Get 获取任务副本。
func (ts *TaskStore) Get(id string) (*Task, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	t, ok := ts.tasks[id]
	if !ok {
		return nil, false
	}
	return cloneForRead(t), true
}

// List 列出所有任务副本。
func (ts *TaskStore) List() []*Task {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]*Task, 0, len(ts.tasks))
	for _, t := range ts.tasks {
		out = append(out, cloneForRead(t))
	}
	return out
}

// ListByState 按状态列出任务副本。
func (ts *TaskStore) ListByState(state TaskState) []*Task {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	var out []*Task
	for _, t := range ts.tasks {
		if t.State == state {
			out = append(out, cloneForRead(t))
		}
	}
	return out
}

// Update 更新任务（自动持久化）。
func (ts *TaskStore) Update(id string, fn func(*Task)) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := ts.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	fn(t)
	return saveTaskFile(ts.dataDir, t)
}

// Delete 删除任务（仅终态可删）。
func (ts *TaskStore) Delete(id string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := ts.tasks[id]
	if !ok {
		return ErrTaskNotFound
	}
	if IsActiveState(t.State) {
		return ErrTaskInvalidState.WithMessage("cannot delete active task")
	}
	delete(ts.tasks, id)
	return removeTaskFile(ts.dataDir, id)
}

// Transition 状态转换。
func (ts *TaskStore) Transition(id string, from, to TaskState) (*Task, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := ts.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	if t.State != from {
		return nil, ErrTaskInvalidState.WithMessage(
			fmt.Sprintf("expected state %s, got %s", from, t.State))
	}
	if !validTransition(from, to) {
		return nil, ErrTaskInvalidState.WithMessage(
			fmt.Sprintf("invalid transition %s → %s", from, to))
	}

	t.State = to
	stresslog.Info("[TASK] 状态转换",
		zap.String("taskID", id),
		zap.String("from", string(from)),
		zap.String("to", string(to)))
	now := time.Now()

	switch to {
	case TaskRunning:
		t.StartedAt = &now
	case TaskStopped, TaskFailed:
		t.StoppedAt = &now
	}

	if err := saveTaskFile(ts.dataDir, t); err != nil {
		stresslog.Warn("[ADMIN] 保存任务文件失败", zap.String("id", id), zap.Error(err))
	}

	// 终态时清理 activeID 并触发回调
	if !IsActiveState(to) {
		if ts.activeID == id {
			ts.activeID = ""
		}
		if ts.onTerminal != nil {
			// 深拷贝避免异步归档的 data race
			var taskCopy Task
			if data, err := json.Marshal(t); err == nil {
				json.Unmarshal(data, &taskCopy)
			} else {
				stresslog.Warn("[TASK] 深拷贝序列化失败", zap.String("taskID", id), zap.Error(err))
			}
			taskRef := &taskCopy
			utils.GetWorkPool().Go(func() { ts.onTerminal(taskRef) })
		}
	}

	return t, nil
}

// ActiveTaskID returns the active task ID, or empty string if none.
func (ts *TaskStore) ActiveTaskID() string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.activeID
}

// ActiveTask 返回当前活跃任务的读安全副本（无则返回 nil）。
// 引用型集合字段（Reports/Assignments/... ）已由 cloneForRead 拷贝，可安全只读遍历。
func (ts *TaskStore) ActiveTask() *Task {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if ts.activeID == "" {
		return nil
	}
	t, ok := ts.tasks[ts.activeID]
	if !ok {
		return nil
	}
	return cloneForRead(t)
}

// HasActive 是否有活跃任务。
func (ts *TaskStore) HasActive() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.activeID != ""
}

// StartTask 启动任务（单例约束入口）。
func (ts *TaskStore) StartTask(id string) (*Task, error) {
	ts.startMu.Lock()
	defer ts.startMu.Unlock()

	// 在锁内检查活跃任务，释放锁前拷贝所需字段
	ts.mu.RLock()
	if ts.activeID != "" {
		active := ts.tasks[ts.activeID]
		if active != nil {
			activeID := active.ID
			activeName := active.Name
			activeState := string(active.State)
			activeStartedAt := active.StartedAt
			ts.mu.RUnlock()
			return nil, ErrTaskConflict.WithDetails(map[string]any{
				"activeTaskId": activeID,
				"activeName":   activeName,
				"activeState":  activeState,
				"startedAt":    activeStartedAt,
			})
		}
		ts.mu.RUnlock()
	} else {
		ts.mu.RUnlock()
	}

	// 状态转换 pending → starting
	task, err := ts.Transition(id, TaskPending, TaskStarting)
	if err != nil {
		return nil, err
	}

	// 标记 activeID
	ts.mu.Lock()
	ts.activeID = id
	ts.mu.Unlock()

	return task, nil
}

// validTransition 校验状态转换是否合法。
func validTransition(from, to TaskState) bool {
	switch from {
	case TaskPending:
		return to == TaskStarting || to == TaskFailed || to == TaskStopped
	case TaskStarting:
		return to == TaskRunning || to == TaskFailed
	case TaskRunning:
		return to == TaskStopping || to == TaskStopped || to == TaskFailed
	case TaskStopping:
		return to == TaskStopped || to == TaskFailed
	default:
		return false
	}
}
