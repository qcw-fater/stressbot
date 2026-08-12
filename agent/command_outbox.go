package agent

import (
	"container/list"
	"fmt"
	"sync"

	"stressbot/controlplane/controlv1"

	"google.golang.org/protobuf/proto"
)

type commandOutcome struct {
	ack       *controlv1.CommandAck
	pending   bool
	confirmed bool
	element   *list.Element
}

// CommandOutcomeOutbox retains the exact outcome (including rejection) until
// Admin confirms durable receipt. Session generations only replay snapshots;
// they never consume or delete outcomes themselves.
type CommandOutcomeOutbox struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*commandOutcome
	order    *list.List
	notify   chan struct{}
}

func NewCommandOutcomeOutbox(capacity int) *CommandOutcomeOutbox {
	if capacity <= 0 {
		capacity = 4096
	}
	return &CommandOutcomeOutbox{capacity: capacity, items: make(map[string]*commandOutcome), order: list.New(), notify: make(chan struct{}, 1)}
}

func (o *CommandOutcomeOutbox) FindAndReplay(commandID string) *controlv1.CommandAck {
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
	return proto.Clone(item.ack).(*controlv1.CommandAck)
}

func (o *CommandOutcomeOutbox) Record(ack *controlv1.CommandAck) error {
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
	item := &commandOutcome{ack: proto.Clone(ack).(*controlv1.CommandAck), pending: true}
	item.element = o.order.PushFront(item)
	o.items[ack.CommandId] = item
	o.wakeLocked()
	return nil
}

func (o *CommandOutcomeOutbox) Snapshot() []*controlv1.CommandAck {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*controlv1.CommandAck, 0)
	for element := o.order.Back(); element != nil; element = element.Prev() {
		item := element.Value.(*commandOutcome)
		if item.pending {
			out = append(out, proto.Clone(item.ack).(*controlv1.CommandAck))
		}
	}
	return out
}

func (o *CommandOutcomeOutbox) Confirm(receipt *controlv1.CommandReceipt) bool {
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

func (o *CommandOutcomeOutbox) Wake() {
	o.mu.Lock()
	o.wakeLocked()
	o.mu.Unlock()
}

func (o *CommandOutcomeOutbox) wakeLocked() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}
