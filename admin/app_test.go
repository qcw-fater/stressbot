package admin

import (
	"testing"
	"time"
)

// 停止安全网的等待必须可被进程关闭打断：裸 Sleep 会占住协程 60s，
// 曾表现为退出时 workpool shutdown timeout、remaining=1。
func TestWaitStopGuardInterruptedByStop(t *testing.T) {
	stopCh := make(chan struct{})
	done := make(chan bool, 1)
	go func() { done <- waitStopGuard(stopCh, time.Minute) }()

	close(stopCh)
	select {
	case fired := <-done:
		if fired {
			t.Fatal("stopCh 关闭后应立即返回 false，不应等满超时")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopCh 关闭后 waitStopGuard 未及时返回")
	}
}

func TestWaitStopGuardFiresAfterTimeout(t *testing.T) {
	if !waitStopGuard(make(chan struct{}), 5*time.Millisecond) {
		t.Fatal("超时到达应返回 true")
	}
}
