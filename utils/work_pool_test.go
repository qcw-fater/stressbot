package utils

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/panjf2000/ants/v2"
	"go.uber.org/zap"

	stresslog "stressbot/utils/log"
)

// TestMain 初始化日志，避免 Shutdown 内 log.Info 命中 nil logger。
func TestMain(m *testing.M) {
	stresslog.ReplaceLogger(zap.NewNop())
	os.Exit(m.Run())
}

// newTestWorkPool 构造一个独立的 WorkPool 实例（不走全局 workPoolOnce 单例），
// 供 Shutdown / context 行为测试，避免污染全局协程池影响其他测试。
func newTestWorkPool(t *testing.T) *WorkPool {
	t.Helper()
	pool, err := ants.NewPool(0)
	if err != nil {
		t.Fatalf("ants.NewPool 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkPool{
		pool:   pool,
		stopCh: make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		cfg:    &PoolConfig{ShutdownTimeout: time.Second},
	}
}

// TestWorkPoolContextCanceledOnShutdown 验证方案 B 的核心不变量：
// Shutdown 后 Context() 返回的 ctx 必须被取消，从而派生到所有以它为父级的子 ctx
// （如 Robot 生命周期 ctx），实现异常路径（直接 Shutdown 不 StopAll）下的优雅停止。
func TestWorkPoolContextCanceledOnShutdown(t *testing.T) {
	p := newTestWorkPool(t)
	ctx := p.Context()

	if err := ctx.Err(); err != nil {
		t.Fatalf("新建池的 ctx 不应已取消: %v", err)
	}

	p.Shutdown()

	if err := ctx.Err(); err != context.Canceled {
		t.Fatalf("Shutdown 后 ctx 应为 context.Canceled，实际 %v", err)
	}
}

// TestWorkPoolContextPropagatesToChild 验证派生语义：
// 以 WorkPool.Context() 为父级创建的子 ctx，在 Shutdown 时也会被取消。
func TestWorkPoolContextPropagatesToChild(t *testing.T) {
	p := newTestWorkPool(t)
	child, cancelChild := context.WithCancel(p.Context())
	defer cancelChild()

	if err := child.Err(); err != nil {
		t.Fatalf("子 ctx 不应已取消: %v", err)
	}

	p.Shutdown()

	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("Shutdown 后子 ctx 未在 1s 内取消")
	}
}

// TestWorkPoolShutdownIdempotent 验证 Shutdown 幂等：多次调用不 panic
// （stopCh 只 close 一次由 stopped CAS 保证；cancel 多次调用安全）。
func TestWorkPoolShutdownIdempotent(t *testing.T) {
	p := newTestWorkPool(t)
	p.Shutdown()
	p.Shutdown() // 不应 panic（重复 close(stopCh) 由 stopped CAS 防护）

	if err := p.Context().Err(); err != context.Canceled {
		t.Fatalf("Shutdown 后 ctx 应为 context.Canceled，实际 %v", err)
	}
}
