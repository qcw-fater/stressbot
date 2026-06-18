package network

import (
	"strconv"
	"sync"
)

// listenQueue 是固定容量的监听消息环形队列（network 包内部类型，不导出）。
//
// 设计意图：替代旧的单槽 map[string]*Message 缓存（同 routeKey 新消息覆盖旧消息）。
// 每个 routeKey 一份队列；容量固定，队满时覆盖最旧元素（丢最旧、保最新），
// 并在 dropped 字段累计丢弃数。
//
// 并发模型（关键）：
//   - listenLoop goroutine → dispatchListen → Push
//   - 主流程 goroutine → GetListenResp → Pop
//   - 二者并发：本队列自带 sync.Mutex 串行化 Push/Pop/Clear/Dropped，
//     不依赖 Connection.mu（Connection.mu 仅保护 listenQueues 这个 map 的键操作）。
//     Push/Pop 均在 Connection.mu 释放后执行，无锁序交叉、无死锁。
//
// 写入位置统一用 (head+size)%capacity 派生，不单独存 tail；Push/Pop 各 O(1)。
type listenQueue struct {
	buf      []*Message
	head     int    // 最旧元素下标（下一个出队位置）
	size     int    // 当前元素数
	capacity int    // 固定容量，构造后不变；前置条件：capacity >= 1
	dropped  uint64 // 队满覆盖累计丢弃数（不被 Clear 重置，累计指标）
	mu       sync.Mutex
}

// newListenQueue 构造一个固定容量的监听队列。
//
// 前置条件：capacity >= 1。本任务唯一创建点（dispatchListen 按需创建）传
// defaultListenQueueSize（=1）。capacity<1 是调用方的编程错误，这里 panic 暴露，
// 不做静默 clamp（与「禁止兼容性兜底」一致）。
func newListenQueue(capacity int) *listenQueue {
	if capacity < 1 {
		panic("newListenQueue: capacity 必须 >= 1，当前值=" + strconv.Itoa(capacity))
	}
	return &listenQueue{
		buf:      make([]*Message, capacity),
		capacity: capacity,
	}
}

// Push 入队一条消息。队满时覆盖最旧元素，dropped++，size 不变（保持 capacity）。
// 返回 dropped bool：true 表示本次 Push 因队满丢弃了一条最旧消息。
func (q *listenQueue) Push(m *Message) (dropped bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.size < q.capacity {
		// 未满：写入 (head+size)%capacity，size++。
		q.buf[(q.head+q.size)%q.capacity] = m
		q.size++
		return false
	}
	// 已满：覆盖 head（最旧），head 前进一格，新消息位于 (head-1+capacity)%capacity（最新）。
	q.buf[q.head] = m
	q.head = (q.head + 1) % q.capacity
	q.dropped++
	return true
}

// Pop 按入队顺序出队一条（FIFO）。空队列返回 (nil, false)。
func (q *listenQueue) Pop() (*Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.size == 0 {
		return nil, false
	}
	m := q.buf[q.head]
	q.buf[q.head] = nil // 助 GC，避免悬挂引用
	q.head = (q.head + 1) % q.capacity
	q.size--
	return m, true
}

// Dropped 返回队满覆盖累计丢弃数。该值是累计指标，不被 Clear 重置。
func (q *listenQueue) Dropped() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

// Clear 清空队列：head=0、size=0，并把 buf 各槽置 nil 助 GC。
// capacity 保持不变；dropped 累计指标不被重置。
func (q *listenQueue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.buf {
		q.buf[i] = nil
	}
	q.head = 0
	q.size = 0
}
