package timerpool

import (
	"testing"
	"time"
)

// TestTimerPoolReuseNoStaleFire 验证池化 timer 的核心不变量：
// 复用（含"上一任已到期未消费"与"上一任提前 Stop"两种归还形态）后
// Reset 的新窗口内不会收到旧触发，且到期能正常触发。
// 依赖 Go 1.23+ timer 语义（无缓冲通道、Stop/Reset 丢弃未消费触发）。
func TestTimerPoolReuseNoStaleFire(t *testing.T) {
	// 形态 1：到期但未消费 C，直接归还。
	t1 := GetTimer(time.Microsecond)
	time.Sleep(5 * time.Millisecond) // 已到期，触发悬而未收
	PutTimer(t1)

	// 复用：短窗口内不应立即收到旧触发。
	t2 := GetTimer(50 * time.Millisecond)
	select {
	case <-t2.C:
		t.Fatal("复用 timer 立即收到旧触发（stale fire）")
	case <-time.After(10 * time.Millisecond):
	}
	// 到期后应正常触发。
	select {
	case <-t2.C:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("复用 timer 未在预期窗口内到期")
	}
	PutTimer(t2)

	// 形态 2：未到期提前归还（PutTimer 内部 Stop）。
	t3 := GetTimer(time.Hour)
	PutTimer(t3)
	t4 := GetTimer(10 * time.Millisecond)
	select {
	case <-t4.C:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("提前归还后复用的 timer 未到期触发")
	}
	PutTimer(t4)
}
