// Package timerpool 提供全局 time.Timer 池：高频等待点通过 Get/Put 复用
// timer 消除逐次分配，正确性依赖 Go 1.23+ 的无缓冲 timer channel 语义。
package timerpool

import (
	"sync"
	"time"
)

// ── 全局 timer 池 ─────────────────────────────────────────────
//
// 高频等待点（战斗帧循环 poll、每请求超时窗口）此前每次 time.NewTimer：
// 8000 机器人 × 每帧一只，剖面周期内 timer 分配 20GB+ 级。池化后 timer
// 生命周期与等待点解耦，常驻量 ∝ 并发等待数（sync.Pool 随 GC 清空）。
//
// 正确性依赖 Go 1.23+ 的 timer 语义（go.mod ≥1.23 自动启用）：timer channel
// 无缓冲，Stop/Reset 保证其后不会收到旧触发——归还前 Stop 即可，无需排空，
// 复用方 Reset 后不会看到上一任使用者的到期信号。
//
// 用法约束：PutTimer 之后不得再引用该 timer（含其 C）；同一 timer 不得并发
// 使用（get→用→put 单协程串行）。

var timerPool sync.Pool

// GetTimer 从池中取一只已装配到 d 的 timer（无可用则新建）。
func GetTimer(d time.Duration) *time.Timer {
	if v := timerPool.Get(); v != nil {
		t := v.(*time.Timer)
		t.Reset(d)
		return t
	}
	return time.NewTimer(d)
}

// PutTimer 停止并归还 timer。归还后调用方不得再持有引用。
func PutTimer(t *time.Timer) {
	t.Stop()
	timerPool.Put(t)
}
