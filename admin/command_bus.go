package admin

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"stressbot/controlplane/controlv1"

	"google.golang.org/protobuf/proto"
)

type cachedCommand struct {
	id      string
	command *controlv1.Command
}

const commandDispatchWindow = 16

type CommandBus struct {
	store    CommandStore
	sessions *SessionRegistry
	mu       sync.Mutex
	capacity int
	order    *list.List
	items    map[string]*list.Element
}

func NewCommandBus(store CommandStore, sessions *SessionRegistry, capacity int) *CommandBus {
	if capacity <= 0 {
		capacity = 8192
	}
	return &CommandBus{store: store, sessions: sessions, capacity: capacity, order: list.New(), items: make(map[string]*list.Element)}
}

func (b *CommandBus) CreateBatch(ctx context.Context, commands []*controlv1.Command) error {
	now := time.Now().UnixNano()
	for _, command := range commands {
		if command.CommandId == "" {
			command.CommandId = generateID()
		}
		if command.CreatedAtUnixNano == 0 {
			command.CreatedAtUnixNano = now
		}
	}
	if err := b.store.CreateBatch(ctx, commands); err != nil {
		return err
	}
	for _, command := range commands {
		b.cache(command)
		b.sessions.WakeCommands(command.AgentId)
	}
	return nil
}

func (b *CommandBus) Resolve(ctx context.Context, id string) (*controlv1.Command, error) {
	b.mu.Lock()
	if element := b.items[id]; element != nil {
		b.order.MoveToFront(element)
		command := proto.Clone(element.Value.(*cachedCommand).command).(*controlv1.Command)
		b.mu.Unlock()
		return command, nil
	}
	b.mu.Unlock()
	command, err := b.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	b.cache(command)
	return proto.Clone(command).(*controlv1.Command), nil
}

func (b *CommandBus) Replay(ctx context.Context, session *agentSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session.wakeCommands()
	return nil
}

// PendingBatch is called only by the session send owner. lastSent is a
// delivery cursor, not an ACK cursor: it lets a healthy session stream each
// journal record once while reconnect creates a fresh cursor for replay.
func (b *CommandBus) PendingBatch(ctx context.Context, session *agentSession) ([]*controlv1.Command, error) {
	capacity := session.commandCapacity()
	if capacity <= 0 {
		return nil, nil
	}
	commands, err := b.store.Pending(ctx, session.agentID, session.lastSent.Load(), capacity)
	if err != nil {
		return nil, err
	}
	for _, command := range commands {
		b.cache(command)
		session.lastSent.Store(command.Sequence)
		session.markCommandSent(command.CommandId)
	}
	return commands, nil
}

func (b *CommandBus) Acknowledge(ctx context.Context, ack *controlv1.CommandAck) (*controlv1.Command, error) {
	if ack == nil || ack.CommandId == "" {
		return nil, fmt.Errorf("命令 ACK 缺少 commandId")
	}
	command, err := b.Resolve(ctx, ack.CommandId)
	if err != nil {
		return nil, err
	}
	if command.AgentId != ack.AgentId || command.Sequence != ack.Sequence || command.TaskId != ack.TaskId {
		return nil, fmt.Errorf("命令 ACK 与日志记录不匹配")
	}
	if err := b.store.Acknowledge(ctx, ack.CommandId, ack.Status, ack.Reason); err != nil {
		return nil, err
	}
	return command, nil
}

func (b *CommandBus) cache(command *controlv1.Command) {
	copyCommand := proto.Clone(command).(*controlv1.Command)
	b.mu.Lock()
	defer b.mu.Unlock()
	if element := b.items[command.CommandId]; element != nil {
		element.Value.(*cachedCommand).command = copyCommand
		b.order.MoveToFront(element)
		return
	}
	element := b.order.PushFront(&cachedCommand{id: command.CommandId, command: copyCommand})
	b.items[command.CommandId] = element
	if b.order.Len() > b.capacity {
		oldest := b.order.Back()
		delete(b.items, oldest.Value.(*cachedCommand).id)
		b.order.Remove(oldest)
	}
}
