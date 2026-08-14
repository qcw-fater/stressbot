package network

import (
	"sync"
	"testing"
	"time"
)

// mkMsg 是测试辅助：构造一个仅带 RouteKey 与可区分 Data 的 *Message。
func mkMsg(payload byte) *Message {
	return &Message{RouteKey: "k", Data: []byte{payload}}
}

// msgPayload 返回消息 Data 的首字节，便于在断言中指代某条消息。
func msgPayload(m *Message) byte {
	if m == nil || len(m.Data) == 0 {
		return 0
	}
	return m.Data[0]
}

// --- newListenQueue 前置条件 ---

func TestNewListenQueue_CapacityPrecondition(t *testing.T) {
	t.Run("capacity>=1 正常构造", func(t *testing.T) {
		q := newListenQueue(1)
		if q == nil {
			t.Fatal("newListenQueue(1) 返回 nil")
		}
		if got := q.capacity; got != 1 {
			t.Fatalf("capacity = %d, want 1", got)
		}
		if got := len(q.buf); got != 1 {
			t.Fatalf("buf len = %d, want 1", got)
		}
	})

	t.Run("capacity<1 panic 暴露编程错误（不静默 clamp）", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("newListenQueue(0) 应当 panic，实际未 panic")
			}
		}()
		_ = newListenQueue(0)
	})
}

// --- Push/Pop 基本 FIFO ---

func TestListenQueue_PushPop_FIFO(t *testing.T) {
	q := newListenQueue(3)
	A, B, C := mkMsg('A'), mkMsg('B'), mkMsg('C')

	// 容量 3 下三条均未满，不应触发 dropped。
	for i, m := range []*Message{A, B, C} {
		if dropped := q.Push(m); dropped {
			t.Fatalf("第 %d 条 Push 不应触发 dropped，实际 dropped=true", i)
		}
	}

	// FIFO：依次 pop A/B/C。
	want := []byte{'A', 'B', 'C'}
	for i, w := range want {
		m, ok := q.Pop()
		if !ok {
			t.Fatalf("第 %d 次 Pop 应有值，实际 ok=false", i)
		}
		if got := msgPayload(m); got != w {
			t.Fatalf("第 %d 次 Pop payload = %q, want %q", i, got, w)
		}
	}

	// 队列耗尽：再 pop 返回 (nil,false)。
	if m, ok := q.Pop(); ok {
		t.Fatalf("空队列 Pop 应 ok=false，实际 ok=true m=%v", m)
	}
}

// --- 满队覆盖最旧 + dropped 计数 ---

func TestListenQueue_Push_FullEvictsOldest(t *testing.T) {
	q := newListenQueue(2)
	A, B, C := mkMsg('A'), mkMsg('B'), mkMsg('C')

	// A、B 填满。
	q.Push(A)
	q.Push(B)
	if got := q.Dropped(); got != 0 {
		t.Fatalf("填满后 Dropped = %d, want 0", got)
	}

	// push C：满，覆盖最旧 A，dropped=1，size 不变（保持 2）。
	if dropped := q.Push(C); !dropped {
		t.Fatal("push C 时满队，应返回 dropped=true")
	}
	if got := q.Dropped(); got != 1 {
		t.Fatalf("push C 后 Dropped = %d, want 1", got)
	}

	// FIFO pop：最旧的 A 已被丢弃，剩 B（次新）、C（最新），按入队顺序先 B 后 C。
	m1, ok := q.Pop()
	if !ok || msgPayload(m1) != 'B' {
		t.Fatalf("第 1 次 Pop = %q(ok=%v), want 'B'", msgPayload(m1), ok)
	}
	m2, ok := q.Pop()
	if !ok || msgPayload(m2) != 'C' {
		t.Fatalf("第 2 次 Pop = %q(ok=%v), want 'C'", msgPayload(m2), ok)
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("清空后再 Pop 应 ok=false")
	}
}

// --- 容量 1 等价单槽（保最新） ---

func TestListenQueue_Capacity1_EquivalentToSingleSlot(t *testing.T) {
	q := newListenQueue(1)
	A, B := mkMsg('A'), mkMsg('B')

	// 首条 push：未满。
	if dropped := q.Push(A); dropped {
		t.Fatal("容量 1 首条 push 不应 dropped")
	}
	// 第二条 push：满，覆盖最旧（即 A），dropped=true，buf 内只剩 B。
	if dropped := q.Push(B); !dropped {
		t.Fatal("容量 1 第二条 push 应 dropped=true")
	}
	if got := q.Dropped(); got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}

	// Pop 返回最新（B），与旧单槽「保最新」语义一致。
	m, ok := q.Pop()
	if !ok || msgPayload(m) != 'B' {
		t.Fatalf("Pop = %q(ok=%v), want 'B'（最新）", msgPayload(m), ok)
	}
	// 再 Pop 空。
	if _, ok := q.Pop(); ok {
		t.Fatal("容量 1 pop 后再 Pop 应 ok=false")
	}
}

// --- 多轮覆盖：dropped 累计 ---

func TestListenQueue_DroppedAccumulates(t *testing.T) {
	q := newListenQueue(1)
	// 连续 push 5 条到容量 1 队列：第一条不丢，后 4 条各丢一条最旧。
	for i := range 5 {
		q.Push(mkMsg(byte('A' + i)))
	}
	if got := q.Dropped(); got != 4 {
		t.Fatalf("Dropped = %d, want 4", got)
	}
	m, ok := q.Pop()
	if !ok || msgPayload(m) != 'E' {
		t.Fatalf("Pop = %q(ok=%v), want 'E'（最终最新）", msgPayload(m), ok)
	}
}

// --- Clear ---

func TestListenQueue_Clear(t *testing.T) {
	q := newListenQueue(3)
	q.Push(mkMsg('A'))
	q.Push(mkMsg('B'))

	// 触发一次 dropped 以验证 Clear 不重置累计指标。
	q.Push(mkMsg('C')) // 满
	q.Push(mkMsg('D')) // dropped=1
	beforeDropped := q.Dropped()
	if beforeDropped != 1 {
		t.Fatalf("Clear 前 Dropped = %d, want 1", beforeDropped)
	}

	q.Clear()

	// 清空后 Pop 应空。
	if _, ok := q.Pop(); ok {
		t.Fatal("Clear 后 Pop 应 ok=false")
	}
	// size 归零；通过 Push 一条后 Pop 一次成功、再 Pop 失败间接验证 size==0。
	q.Push(mkMsg('Z'))
	if _, ok := q.Pop(); !ok {
		t.Fatal("Clear 后重新 Push 一条，Pop 应 ok=true")
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("Clear 后 Push 一条 Pop 一次，再 Pop 应 ok=false")
	}
	// Dropped() 是累计指标，Clear 不重置。
	if got := q.Dropped(); got != beforeDropped {
		t.Fatalf("Clear 后 Dropped = %d, want %d（累计指标不应被 Clear 重置）", got, beforeDropped)
	}
	// capacity 不变：仍能容纳 3 条而不 dropped。
	q.Push(mkMsg('1'))
	q.Push(mkMsg('2'))
	q.Push(mkMsg('3'))
	if dropped := q.Push(mkMsg('4')); !dropped {
		t.Fatal("Clear 后 capacity 应保持 3：第 4 条 push 应触发 dropped=true")
	}
}

// --- Clear 把 buf 各槽置 nil 助 GC ---

func TestListenQueue_Clear_NilBufSlots(t *testing.T) {
	q := newListenQueue(2)
	q.Push(&Message{RouteKey: "k", Data: make([]byte, 1024)})
	q.Push(&Message{RouteKey: "k", Data: make([]byte, 1024)})
	q.Clear()
	for i, slot := range q.buf {
		if slot != nil {
			t.Fatalf("Clear 后 buf[%d] 仍非 nil，未助 GC", i)
		}
	}
}

// --- 并发 smoke：多 goroutine 交替 Push/Pop，无 panic、最终一致 ---

func TestListenQueue_ConcurrentSmoke(t *testing.T) {
	// 无 cgo 时仍能跑此 smoke；有 cgo 配合 -race 进一步验证。
	const goroutines = 8
	const perG = 50
	q := newListenQueue(4)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			<-start
			for range perG {
				// 一半 push，一半 pop；总操作数 goroutines*perG，
				// 内部 Push 与 Pop 严格 1:1，最终队列应空或近空（无死锁/无 panic）。
				q.Push(mkMsg(byte('a' + (id % 26))))
				if _, ok := q.Pop(); !ok {
					// Pop 失败是允许的（多个消费者竞争），不视为错误。
					_ = ok
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()

	// 最终一致性：drain 剩余，size 必然 >= 0 且 <= capacity。
	drained := 0
	for {
		if _, ok := q.Pop(); !ok {
			break
		}
		drained++
	}
	if drained > 4 {
		t.Fatalf("drain 后剩余 %d 条，超出 capacity 4（结构性异常）", drained)
	}
	// Dropped 一定是非负数（uint64 自然为正，断言无溢出语义即可）。
	_ = q.Dropped()
}

// TestListenQueue_ConcurrentSmoke_ManyKeys 验证容量 1 + 高并发覆盖场景下
// dropped 计数与最终 pop 的正确性（结构性论证线程安全的补充）。
func TestListenQueue_ConcurrentSmoke_Capacity1(t *testing.T) {
	q := newListenQueue(1)
	const goroutines = 16
	const perG = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for g := range goroutines {
		go func() {
			defer wg.Done()
			<-start
			for range perG {
				q.Push(mkMsg(byte('a' + (g % 26))))
			}
		}()
	}
	close(start)
	wg.Wait()

	// 总 push = goroutines*perG = 1600，容量 1，应丢弃 1599 条。
	if got := q.Dropped(); got != uint64(goroutines*perG-1) {
		t.Fatalf("Dropped = %d, want %d", got, goroutines*perG-1)
	}
	if _, ok := q.Pop(); !ok {
		t.Fatal("容量 1 高并发 push 后应有 1 条可 pop")
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("容量 1 pop 后再 Pop 应 ok=false")
	}
}

// --- 事件通知：channel 只做边沿唤醒，队列仍是唯一消息源 ---

func TestListenQueue_NotifyPushCoalescesAndDrains(t *testing.T) {
	q := newListenQueue(3)
	notify := q.Notify()

	select {
	case <-notify:
		t.Fatal("空队列不应存在通知")
	default:
	}

	q.Push(mkMsg('A'))
	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("Push 后未收到通知")
	}

	// 消费首个通知后继续 Push 两条：容量 1 的通知最多保留一个唤醒提示，
	// 消息本身仍全部留在容量 3 的 FIFO 队列里。
	q.Push(mkMsg('B'))
	q.Push(mkMsg('C'))
	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("后续 Push 未补充通知")
	}
	select {
	case <-notify:
		t.Fatal("连续 Push 的通知应合并为一个")
	default:
	}

	for _, want := range []byte{'A', 'B', 'C'} {
		got, ok := q.Pop()
		if !ok || msgPayload(got) != want {
			t.Fatalf("Pop = %q(ok=%v), want %q", msgPayload(got), ok, want)
		}
	}
	select {
	case <-notify:
		t.Fatal("队列清空后不应残留陈旧通知")
	default:
	}
}

func TestListenQueue_NotifyClearRemovesSignal(t *testing.T) {
	q := newListenQueue(1)
	notify := q.Notify()
	q.Push(mkMsg('A'))
	q.Clear()

	select {
	case <-notify:
		t.Fatal("Clear 后不应残留通知")
	default:
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("Clear 后队列应为空")
	}
}

func TestListenQueue_NotifyNoLostWakeAfterEmptyPop(t *testing.T) {
	q := newListenQueue(1)
	notify := q.Notify()
	checkedEmpty := make(chan struct{})
	result := make(chan *Message, 1)

	go func() {
		if _, ok := q.Pop(); ok {
			result <- nil
			return
		}
		close(checkedEmpty)
		select {
		case <-notify:
			m, _ := q.Pop()
			result <- m
		case <-time.After(time.Second):
			result <- nil
		}
	}()

	<-checkedEmpty
	q.Push(mkMsg('Z'))
	if got := <-result; got == nil || msgPayload(got) != 'Z' {
		t.Fatalf("空检查与等待之间的 Push 丢失：got=%v", got)
	}
}
