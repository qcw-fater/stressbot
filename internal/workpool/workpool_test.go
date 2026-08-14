package workpool

import (
	"sync"
	"testing"

	"stressbot/internal/stresslog"

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
			t.Fatal("Default() returned different pool instances")
		}
	}
}
