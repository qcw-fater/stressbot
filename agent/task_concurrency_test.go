package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func TestSubmitTaskConcurrentOnlyOneReservation(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	a := &Agent{ctx: context.Background()}
	var accepted atomic.Int32
	var submitted atomic.Int32
	var wg sync.WaitGroup

	const attempts = 64
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := a.submitTaskWithSubmit(
				&TaskAssignment{TaskID: fmt.Sprintf("task-%d", i)},
				func(func()) error {
					submitted.Add(1)
					return nil
				},
			)
			if err == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted tasks = %d, want 1", got)
	}
	if got := submitted.Load(); got != 1 {
		t.Fatalf("submitted tasks = %d, want 1", got)
	}

	a.mu.Lock()
	a.taskCancel()
	a.currentTask = nil
	a.taskCancel = nil
	a.status = StatusIdle
	a.mu.Unlock()
	a.taskWG.Done() // 测试 submitter 不执行已接受的任务体。
}

func TestCancelOldTaskConcurrentWithReplacementNeverCancelsNewTask(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())

	for i := range 200 {
		_, oldCancel := context.WithCancel(context.Background())
		newCtx, newCancel := context.WithCancel(context.Background())
		a := &Agent{
			currentTask: &TaskAssignment{TaskID: "old-task"},
			taskCancel:  oldCancel,
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			a.cancelTask("old-task", "test")
		}()
		go func() {
			defer wg.Done()
			<-start
			a.mu.Lock()
			a.currentTask = &TaskAssignment{TaskID: "new-task"}
			a.taskCancel = newCancel
			a.mu.Unlock()
		}()
		close(start)
		wg.Wait()

		select {
		case <-newCtx.Done():
			t.Fatalf("iteration %d: old stop canceled replacement task", i)
		default:
		}
		oldCancel()
		newCancel()
	}
}
