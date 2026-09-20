package task

import (
	"fmt"
	"sync"
	"time"

	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
)

// Adapter lets the task state-machine own lifecycle behavior without
// depending on Admin's HTTP-facing task representation.
type Adapter[T any, S comparable] struct {
	ID              func(T) string
	State           func(T) S
	SetState        func(T, S)
	IsActive        func(S) bool
	Recover         func(T, time.Time)
	SetStartedAt    func(T, time.Time)
	SetStoppedAt    func(T, time.Time)
	Clone           func(T) T
	TerminalClone   func(T) T
	ConflictDetails func(T) map[string]any
	Load            func(string) ([]T, error)
	Save            func(string, T) error
	Remove          func(string, string) error
	Duplicate       func() error
	NotFound        func() error
	InvalidState    func(string) error
	Conflict        func(map[string]any) error
}

// Repository is a concurrency-safe task repository and lifecycle state machine.
type Repository[T any, S comparable] struct {
	mu    sync.RWMutex
	tasks map[string]T

	startMu  sync.Mutex
	activeID string
	dataDir  string
	adapter  Adapter[T, S]

	onTerminal   func(T)
	recoveredIDs []string
}

// NewRepository loads a generic task repository from dataDir.
func NewRepository[T any, S comparable](dataDir string, adapter Adapter[T, S]) (*Repository[T, S], error) {
	store := &Repository[T, S]{tasks: make(map[string]T), dataDir: dataDir, adapter: adapter}
	items, err := adapter.Load(dataDir)
	if err != nil {
		return nil, fmt.Errorf("加载任务失败: %w", err)
	}
	now := time.Now()
	for _, item := range items {
		if adapter.IsActive(adapter.State(item)) {
			stresslog.Info("恢复活跃任务为 failed", zap.String("taskID", adapter.ID(item)), zap.String("oldState", fmt.Sprint(adapter.State(item))))
			adapter.Recover(item, now)
			store.recoveredIDs = append(store.recoveredIDs, adapter.ID(item))
		}
		store.tasks[adapter.ID(item)] = item
	}
	return store, nil
}

// SetOnTerminal 注册终态回调，任务进入终态时以 TerminalClone 副本异步调用；
// 加载期被恢复为终态的任务会立即补放一次回调（此时尚未注册），随后清空缓冲。
func (s *Repository[T, S]) SetOnTerminal(fn func(T)) {
	s.onTerminal = fn
	for _, id := range s.recoveredIDs {
		if item, ok := s.tasks[id]; ok {
			fn(item)
		}
	}
	s.recoveredIDs = nil
}

// Create 新建任务并落盘；ID 已存在时返回 Duplicate 错误且不覆盖原任务。
func (s *Repository[T, S]) Create(item T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.adapter.ID(item)
	if _, exists := s.tasks[id]; exists {
		return s.adapter.Duplicate()
	}
	s.tasks[id] = item
	return s.adapter.Save(s.dataDir, item)
}

// Get 按 ID 取任务的深拷贝（Clone），不存在时返回零值和 false；返回值可安全并发修改。
func (s *Repository[T, S]) Get(id string) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.tasks[id]
	if !ok {
		var zero T
		return zero, false
	}
	return s.adapter.Clone(item), true
}

// List 返回全部任务的 Clone 副本，顺序由 map 遍历决定。
func (s *Repository[T, S]) List() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]T, 0, len(s.tasks))
	for _, item := range s.tasks {
		out = append(out, s.adapter.Clone(item))
	}
	return out
}

// ListByState 返回处于指定状态的任务 Clone 副本。
func (s *Repository[T, S]) ListByState(state S) []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]T, 0)
	for _, item := range s.tasks {
		if s.adapter.State(item) == state {
			out = append(out, s.adapter.Clone(item))
		}
	}
	return out
}

// Update 在持锁下对任务应用 update 变更并落盘；任务不存在时返回 NotFound 错误。
func (s *Repository[T, S]) Update(id string, update func(T)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tasks[id]
	if !ok {
		return s.adapter.NotFound()
	}
	update(item)
	return s.adapter.Save(s.dataDir, item)
}

// Delete 删除任务并移除落盘文件；活跃任务不允许删除，返回 InvalidState 错误。
func (s *Repository[T, S]) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tasks[id]
	if !ok {
		return s.adapter.NotFound()
	}
	if s.adapter.IsActive(s.adapter.State(item)) {
		return s.adapter.InvalidState("cannot delete active task")
	}
	delete(s.tasks, id)
	return s.adapter.Remove(s.dataDir, id)
}

// Transition 以 CAS 语义执行状态转换：校验当前状态等于 from 且转换边合法（validTransition），
// 成功后按目标状态补记 startedAt/stoppedAt、落盘；进入活跃态时登记 activeID，进入终态时
// 清除 activeID 并异步触发 onTerminal。activeID 与任务状态始终在同一把锁内一起变更。
func (s *Repository[T, S]) Transition(id string, from, to S) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tasks[id]
	if !ok {
		var zero T
		return zero, s.adapter.NotFound()
	}
	if s.adapter.State(item) != from {
		var zero T
		return zero, s.adapter.InvalidState(fmt.Sprintf("expected state %v, got %v", from, s.adapter.State(item)))
	}
	if !validTransition(fmt.Sprint(from), fmt.Sprint(to)) {
		var zero T
		return zero, s.adapter.InvalidState(fmt.Sprintf("invalid transition %v → %v", from, to))
	}

	s.adapter.SetState(item, to)
	stresslog.Info("[TASK] 状态转换", zap.String("taskID", id), zap.String("from", fmt.Sprint(from)), zap.String("to", fmt.Sprint(to)))
	now := time.Now()
	switch fmt.Sprint(to) {
	case "running":
		s.adapter.SetStartedAt(item, now)
	case "stopped", "failed":
		s.adapter.SetStoppedAt(item, now)
	}
	if err := s.adapter.Save(s.dataDir, item); err != nil {
		stresslog.Warn("[ADMIN] 保存任务文件失败", zap.String("id", id), zap.Error(err))
	}
	// activeID 的登记与清除都收在这把锁里，与状态写入同一个临界区。
	//
	// 早先的做法是 Begin 在 Transition 返回后再补一句 s.activeID = id。中间那段窗口里任务
	// 已经是 starting（活跃态）而 activeID 还是空串，后果有两个：轻的是 HasActive/ActiveTask
	// 在这一瞬谎报「无活跃任务」；重的是若此刻另一路把它推到 failed，下面的清除分支因
	// s.activeID != id 什么也没清，等 Begin 回来补登记，activeID 就永久指向一个已终结的
	// 任务——后续所有启动被 Conflict 挡死，只能重启 Admin。
	if s.adapter.IsActive(to) {
		if s.activeID != "" && s.activeID != id {
			// 走到这里说明单活跃任务约束在上游被绕过了（Begin 是唯一的准入口）。
			// 以当前这次转换为准修复指向，同时把异常留证。
			stresslog.Warn("[TASK] 进入活跃态时已存在其它活跃任务，单活跃约束被绕过",
				zap.String("taskID", id), zap.String("previousActiveID", s.activeID))
		}
		s.activeID = id
	} else {
		if s.activeID == id {
			s.activeID = ""
		}
		if s.onTerminal != nil {
			clone := s.adapter.TerminalClone(item)
			workpool.Default().Go(func() { s.onTerminal(clone) })
		}
	}
	return item, nil
}

// ActiveTaskID 返回当前活跃任务 ID，无活跃任务时为空串。
func (s *Repository[T, S]) ActiveTaskID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeID
}

// ActiveTask 返回当前活跃任务的 Clone 副本，无活跃任务时返回零值。
func (s *Repository[T, S]) ActiveTask() T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeID != "" {
		if item, ok := s.tasks[s.activeID]; ok {
			return s.adapter.Clone(item)
		}
	}
	var zero T
	return zero
}

// HasActive 报告是否存在活跃任务（单活跃任务约束的快速判定）。
func (s *Repository[T, S]) HasActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeID != ""
}

// Begin 启动入口：在 startMu 串行化下先检查单活跃任务约束（冲突时带活跃任务详情返回
// Conflict），再执行 pending→starting 转换——activeID 由 Transition 在同一把 mu 锁内登记。
//
// 「检查」与「转换」之间放开了 mu，靠 startMu 保证期间不会有别的任务变成活跃态：
// pending→starting 是唯一能凭空产生活跃任务的边，而它只能经由本方法、已被 startMu 串行化；
// 其余进入活跃态的转换都发生在那个已经持有 activeID 的任务身上（此时上面的检查已返回
// Conflict，走不到这里）。
func (s *Repository[T, S]) Begin(id string, pending, starting S) (T, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	s.mu.RLock()
	if s.activeID != "" {
		if active, ok := s.tasks[s.activeID]; ok {
			details := s.adapter.ConflictDetails(active)
			s.mu.RUnlock()
			var zero T
			return zero, s.adapter.Conflict(details)
		}
	}
	s.mu.RUnlock()

	return s.Transition(id, pending, starting)
}

func validTransition(from, to string) bool {
	switch from {
	case "pending":
		return to == "starting" || to == "failed" || to == "stopped"
	case "starting":
		return to == "running" || to == "failed"
	case "running":
		return to == "stopping" || to == "stopped" || to == "failed"
	case "stopping":
		return to == "running" || to == "stopped" || to == "failed"
	default:
		return false
	}
}
