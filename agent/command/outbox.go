package command

import (
	"container/list"
	"fmt"
	"sync"

	"stressbot/controlplane/pb"

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

func (o *OutcomeOutbox) Record(ack *controlpb.CommandAck) error {
	if ack == nil || ack.CommandId == "" {
		return fmt.Errorf("命令结果缺少 commandId")
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
			return fmt.Errorf("命令结果待确认队列已满")
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
