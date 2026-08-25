package command

import (
	"container/list"
	"errors"
	"sync"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/protobuf/proto"
)

type commandOutcome struct {
	ack       *controlpb.CommandAck
	pending   bool
	confirmed bool
	element   *list.Element
}

// OutcomeOutbox retains the exact outcome (including rejection) until
// Admin confirms durable receipt. Session generations only replay snapshots;
// they never consume or delete outcomes themselves.
type OutcomeOutbox struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*commandOutcome
	order    *list.List
	notify   chan struct{}
}

// NewOutcomeOutbox creates a bounded command outcome outbox.
func NewOutcomeOutbox(capacity int) *OutcomeOutbox {
	if capacity <= 0 {
		capacity = 4096
	}
	return &OutcomeOutbox{capacity: capacity, items: make(map[string]*commandOutcome), order: list.New(), notify: make(chan struct{}, 1)}
}

// FindAndReplay 按 commandID 查找已留存的结果：命中则将其重置为待确认并唤醒发送循环，
// 返回结果的深拷贝；未命中返回 nil。用于重复投递的命令直接重放既有结论而不重新执行。
func (o *OutcomeOutbox) FindAndReplay(commandID string) *controlpb.CommandAck {
	o.mu.Lock()
	defer o.mu.Unlock()
	item := o.items[commandID]
	if item == nil {
		return nil
	}
	item.pending = true
	item.confirmed = false
	o.order.MoveToFront(item.element)
	o.wakeLocked()
	return proto.Clone(item.ack).(*controlpb.CommandAck)
}

// Record 克隆并留存一条命令结果，置为待确认并唤醒发送循环；
// 已存在同 commandID 的结果则重置其状态。容量已满时优先淘汰已确认项，
// 全部处于待确认则返回错误（调用方应据此终止控制会话）。
func (o *OutcomeOutbox) Record(ack *controlpb.CommandAck) error {
	if ack == nil || ack.CommandId == "" {
		return errors.New("命令结果缺少 commandId")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if item := o.items[ack.CommandId]; item != nil {
		item.pending = true
		item.confirmed = false
		o.order.MoveToFront(item.element)
		o.wakeLocked()
		return nil
	}
	for len(o.items) >= o.capacity {
		var evict *list.Element
		for element := o.order.Back(); element != nil; element = element.Prev() {
			if !element.Value.(*commandOutcome).pending {
				evict = element
				break
			}
		}
		if evict == nil {
			return errors.New("命令结果待确认队列已满")
		}
		item := evict.Value.(*commandOutcome)
		delete(o.items, item.ack.CommandId)
		o.order.Remove(evict)
	}
	item := &commandOutcome{ack: proto.Clone(ack).(*controlpb.CommandAck), pending: true}
	item.element = o.order.PushFront(item)
	o.items[ack.CommandId] = item
	o.wakeLocked()
	return nil
}

// Snapshot 返回当前所有待确认结果的克隆（按淘汰优先级从高到低，即最旧在前），
// 供会话重建时批量重放；不改变各条目的确认状态。
func (o *OutcomeOutbox) Snapshot() []*controlpb.CommandAck {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*controlpb.CommandAck, 0)
	for element := o.order.Back(); element != nil; element = element.Prev() {
		item := element.Value.(*commandOutcome)
		if item.pending {
			out = append(out, proto.Clone(item.ack).(*controlpb.CommandAck))
		}
	}
	return out
}

// Confirm 按 CommandReceipt 确认 Admin 已持久接收对应结果（要求 sequence 匹配），
// 确认后的条目仍保留用于重放，但变为可淘汰；返回是否确认成功。
func (o *OutcomeOutbox) Confirm(receipt *controlpb.CommandReceipt) bool {
	if receipt == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	item := o.items[receipt.CommandId]
	if item == nil || item.ack.Sequence != receipt.Sequence {
		return false
	}
	item.pending = false
	item.confirmed = true
	o.order.MoveToFront(item.element)
	return true
}

// Wake 手动向通知通道发送一次边沿信号，唤醒等待结果发送的循环。
func (o *OutcomeOutbox) Wake() {
	o.mu.Lock()
	o.wakeLocked()
	o.mu.Unlock()
}

// Notifications 返回结果队列的边沿通知通道。
func (o *OutcomeOutbox) Notifications() <-chan struct{} { return o.notify }

func (o *OutcomeOutbox) wakeLocked() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}
