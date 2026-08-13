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

// Store is the concurrency-safe task repository and lifecycle state machine.
type Store[T any, S comparable] struct {
	mu    sync.RWMutex
	tasks map[string]T

	startMu  sync.Mutex
	activeID string
	dataDir  string
	adapter  Adapter[T, S]

	onTerminal   func(T)
	recoveredIDs []string
}

func NewStore[T any, S comparable](dataDir string, adapter Adapter[T, S]) (*Store[T, S], error) {
	store := &Store[T, S]{tasks: make(map[string]T), dataDir: dataDir, adapter: adapter}
	items, err := adapter.Load(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
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

func (s *Store[T, S]) SetOnTerminal(fn func(T)) {
	s.onTerminal = fn
	for _, id := range s.recoveredIDs {
		if item, ok := s.tasks[id]; ok {
			fn(item)
		}
	}
	s.recoveredIDs = nil
}

func (s *Store[T, S]) Create(item T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.adapter.ID(item)
	if _, exists := s.tasks[id]; exists {
		return s.adapter.Duplicate()
	}
	s.tasks[id] = item
	return s.adapter.Save(s.dataDir, item)
}

func (s *Store[T, S]) Get(id string) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.tasks[id]
	if !ok {
		var zero T
		return zero, false
	}
	return s.adapter.Clone(item), true
}

func (s *Store[T, S]) List() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]T, 0, len(s.tasks))
	for _, item := range s.tasks {
		out = append(out, s.adapter.Clone(item))
	}
	return out
}

func (s *Store[T, S]) ListByState(state S) []T {
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

func (s *Store[T, S]) Update(id string, update func(T)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tasks[id]
	if !ok {
		return s.adapter.NotFound()
	}
	update(item)
	return s.adapter.Save(s.dataDir, item)
}

func (s *Store[T, S]) Delete(id string) error {
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

func (s *Store[T, S]) Transition(id string, from, to S) (T, error) {
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
	if !s.adapter.IsActive(to) {
		if s.activeID == id {
			s.activeID = ""
		}
		if s.onTerminal != nil {
			copy := s.adapter.TerminalClone(item)
			workpool.GetWorkPool().Go(func() { s.onTerminal(copy) })
		}
	}
	return item, nil
}

func (s *Store[T, S]) ActiveTaskID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeID
}

func (s *Store[T, S]) ActiveTask() T {
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

func (s *Store[T, S]) HasActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeID != ""
}

func (s *Store[T, S]) Begin(id string, pending, starting S) (T, error) {
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

	item, err := s.Transition(id, pending, starting)
	if err != nil {
		var zero T
		return zero, err
	}
	s.mu.Lock()
	s.activeID = id
	s.mu.Unlock()
	return item, nil
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
