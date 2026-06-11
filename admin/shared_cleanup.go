package admin

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"stressbot/sharedstate"
	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// sharedCleanupRetryInterval 待清理 runId 的定时重试间隔。
const sharedCleanupRetryInterval = 60 * time.Second

// sharedCleanupQueue 记录「待清理的共享状态命名空间 runId」，并持久化到磁盘，
// 使 Redis 临时不可用或 Admin 重启时仍能在后续重试清理，避免无 TTL 的 key 永久泄漏。
type sharedCleanupQueue struct {
	mu      sync.Mutex
	pending map[string]struct{}
	path    string
}

func newSharedCleanupQueue(dataDir string) *sharedCleanupQueue {
	q := &sharedCleanupQueue{
		pending: make(map[string]struct{}),
		path:    filepath.Join(dataDir, "shared-cleanup-pending.json"),
	}
	q.load()
	return q
}

// load 从磁盘恢复待清理列表（best-effort）。
func (q *sharedCleanupQueue) load() {
	data, err := os.ReadFile(q.path)
	if err != nil {
		return
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		stresslog.Warn("[ADMIN] 待清理列表解析失败，忽略", zap.String("file", q.path), zap.Error(err))
		return
	}
	for _, id := range ids {
		q.pending[id] = struct{}{}
	}
	if len(q.pending) > 0 {
		stresslog.Info("[ADMIN] 恢复待清理共享状态", zap.Int("count", len(q.pending)))
	}
}

// persistLocked 把当前列表原子写盘（调用方须持锁）。
func (q *sharedCleanupQueue) persistLocked() {
	ids := make([]string, 0, len(q.pending))
	for id := range q.pending {
		ids = append(ids, id)
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		stresslog.Warn("[ADMIN] 待清理列表写盘失败", zap.Error(err))
		return
	}
	if err := os.Rename(tmp, q.path); err != nil {
		os.Remove(tmp)
		stresslog.Warn("[ADMIN] 待清理列表落盘失败", zap.Error(err))
	}
}

func (q *sharedCleanupQueue) add(runID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[runID]; ok {
		return
	}
	q.pending[runID] = struct{}{}
	q.persistLocked()
}

func (q *sharedCleanupQueue) remove(runID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[runID]; !ok {
		return
	}
	delete(q.pending, runID)
	q.persistLocked()
}

func (q *sharedCleanupQueue) list() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	ids := make([]string, 0, len(q.pending))
	for id := range q.pending {
		ids = append(ids, id)
	}
	return ids
}

// enqueueSharedCleanup 登记一个待清理 runId 并立即尝试清理一次（失败则留待定时重试）。
func (s *AdminServer) enqueueSharedCleanup(runID string) {
	if runID == "" || !s.cfg.SharedEnabled() || s.sharedCleanup == nil {
		return
	}
	s.sharedCleanup.add(runID)
	utils.GetWorkPool().Go(func() { s.attemptSharedCleanup(runID) })
}

// attemptSharedCleanup 尝试清理单个 runId；成功后从待清理列表移除。
func (s *AdminServer) attemptSharedCleanup(runID string) bool {
	if !s.cfg.SharedEnabled() {
		return false
	}
	resolved, err := s.cfg.Shared.Redis.Resolve()
	if err != nil {
		stresslog.Error("[ADMIN] 共享状态清理：配置解析失败", zap.String("runId", runID), zap.Error(err))
		return false
	}
	store, err := sharedstate.NewRedisStore(resolved, runID)
	if err != nil {
		stresslog.Warn("[ADMIN] 共享状态清理：连接 Redis 失败，稍后重试",
			zap.String("runId", runID), zap.Error(err))
		return false
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Cleanup(ctx); err != nil {
		stresslog.Warn("[ADMIN] 共享状态清理失败，稍后重试", zap.String("runId", runID), zap.Error(err))
		return false
	}
	s.sharedCleanup.remove(runID)
	stresslog.Info("[ADMIN] 共享状态已清理", zap.String("runId", runID))
	return true
}

// startSharedCleanupRetry 定时重试待清理列表（Admin 启动时调用）。
func (s *AdminServer) startSharedCleanupRetry(ctx context.Context) {
	if s.sharedCleanup == nil || !s.cfg.SharedEnabled() {
		return
	}
	// 启动即尝试一次，处理上次进程残留
	for _, id := range s.sharedCleanup.list() {
		runID := id
		utils.GetWorkPool().Go(func() { s.attemptSharedCleanup(runID) })
	}
	utils.GetWorkPool().Go(func() {
		ticker := time.NewTicker(sharedCleanupRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, id := range s.sharedCleanup.list() {
					s.attemptSharedCleanup(id)
				}
			}
		}
	})
}
