package task

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"
	"stressbot/state/shared"

	"go.uber.org/zap"
)

const sharedCleanupRetryInterval = 60 * time.Second

type SharedCleanup struct {
	redis   *shared.RedisConfig
	mu      sync.Mutex
	pending map[string]struct{}
	path    string
}

func NewSharedCleanup(dataDir string, redis *shared.RedisConfig) *SharedCleanup {
	cleanup := &SharedCleanup{
		redis: redis, pending: make(map[string]struct{}),
		path: filepath.Join(dataDir, "shared-cleanup-pending.json"),
	}
	cleanup.load()
	return cleanup
}

func (s *SharedCleanup) enabled() bool { return s != nil && s.redis != nil && s.redis.Enabled() }

func (s *SharedCleanup) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		stresslog.Warn("[ADMIN] 待清理列表解析失败，忽略", zap.String("file", s.path), zap.Error(err))
		return
	}
	for _, id := range ids {
		s.pending[id] = struct{}{}
	}
	if len(s.pending) > 0 {
		stresslog.Info("[ADMIN] 恢复待清理共享状态", zap.Int("count", len(s.pending)))
	}
}

func (s *SharedCleanup) persistLocked() {
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		stresslog.Warn("[ADMIN] 待清理列表写盘失败", zap.Error(err))
		return
	}
	if err := os.Rename(temporary, s.path); err != nil {
		_ = os.Remove(temporary)
		stresslog.Warn("[ADMIN] 待清理列表落盘失败", zap.Error(err))
	}
}

func (s *SharedCleanup) add(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pending[runID]; exists {
		return
	}
	s.pending[runID] = struct{}{}
	s.persistLocked()
}

func (s *SharedCleanup) remove(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pending[runID]; !exists {
		return
	}
	delete(s.pending, runID)
	s.persistLocked()
}

func (s *SharedCleanup) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	return ids
}

func (s *SharedCleanup) Enqueue(runID string) {
	if runID == "" || !s.enabled() {
		return
	}
	s.add(runID)
	workpool.GetWorkPool().Go(func() { s.attempt(runID) })
}

func (s *SharedCleanup) attempt(runID string) bool {
	if !s.enabled() {
		return false
	}
	resolved, err := s.redis.Resolve()
	if err != nil {
		stresslog.Error("[ADMIN] 共享状态清理：配置解析失败", zap.String("runId", runID), zap.Error(err))
		return false
	}
	store, err := shared.NewRedisStore(resolved, runID)
	if err != nil {
		stresslog.Warn("[ADMIN] 共享状态清理：连接 Redis 失败，稍后重试", zap.String("runId", runID), zap.Error(err))
		return false
	}
	defer func() { _ = store.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Cleanup(ctx); err != nil {
		stresslog.Warn("[ADMIN] 共享状态清理失败，稍后重试", zap.String("runId", runID), zap.Error(err))
		return false
	}
	s.remove(runID)
	stresslog.Info("[ADMIN] 共享状态已清理", zap.String("runId", runID))
	return true
}

func (s *SharedCleanup) Start(ctx context.Context) {
	if !s.enabled() {
		return
	}
	for _, id := range s.list() {
		runID := id
		workpool.GetWorkPool().Go(func() { s.attempt(runID) })
	}
	workpool.GetWorkPool().Go(func() {
		ticker := time.NewTicker(sharedCleanupRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, runID := range s.list() {
					s.attempt(runID)
				}
			}
		}
	})
}
