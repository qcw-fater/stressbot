package workpool

import (
	"sync"
	"testing"
	"time"

	"stressbot/internal/stresslog"

	"github.com/panjf2000/ants/v2"
	"go.uber.org/zap"
)

func TestDefaultInitializesConcurrently(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	workPool = nil
	workPoolOnce = sync.Once{}
	t.Cleanup(func() {
		if workPool != nil {
			workPool.Shutdown()
		}
		workPool = nil
		workPoolOnce = sync.Once{}
	})

	const callers = 32
	results := make(chan *Pool, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- Default()
		}()
	}
	wg.Wait()
	close(results)

	var first *Pool
	for pool := range results {
		if first == nil {
			first = pool
			continue
		}
		if pool != first {
			t.Fatal("Default() 返回了不同的池实例")
		}
	}
}

// 关闭超时（任务没能在窗口内退出）时底层 ants 池也必须释放，否则每次超时关闭漏一个池。
func TestShutdownReleasesPoolOnTimeout(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())

	pool, err := ants.NewPool(0)
	if err != nil {
		t.Fatalf("创建 ants 池失败: %v", err)
	}
	p := &Pool{pool: pool, stopCh: make(chan struct{})}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	if err := p.SubmitWithStop(func(_ <-chan struct{}) { <-release }); err != nil {
		t.Fatalf("提交阻塞任务失败: %v", err)
	}

	p.shutdown(50 * time.Millisecond)

	if !pool.IsClosed() {
		t.Fatal("关闭超时后底层 ants 池仍未释放")
	}
}

// 正常路径（任务在窗口内退出）同样要释放底层池。
func TestShutdownReleasesPoolOnSuccess(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())

	pool, err := ants.NewPool(0)
	if err != nil {
		t.Fatalf("创建 ants 池失败: %v", err)
	}
	p := &Pool{pool: pool, stopCh: make(chan struct{})}

	finished := make(chan struct{})
	if err := p.Submit(func() { close(finished) }); err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}
	<-finished

	p.shutdown(5 * time.Second)

	if !pool.IsClosed() {
		t.Fatal("正常关闭后底层 ants 池仍未释放")
	}
}
