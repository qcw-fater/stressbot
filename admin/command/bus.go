package command

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"stressbot/admin/grpcapi"
	"stressbot/controlplane/pb"

	"google.golang.org/protobuf/proto"
)

type cachedCommand struct {
	id      string
	command *controlpb.Command
}

type Bus struct {
	store    Store
	sessions *grpcapi.SessionRegistry
	idgen    func() string
	mu       sync.Mutex
	capacity int
	order    *list.List
	items    map[string]*list.Element
}

func NewBus(store Store, sessions *grpcapi.SessionRegistry, capacity int, idgen func() string) *Bus {
	if capacity <= 0 {
		capacity = 8192
	}
	return &Bus{store: store, sessions: sessions, idgen: idgen, capacity: capacity, order: list.New(), items: make(map[string]*list.Element)}
}

func (b *Bus) CreateBatch(ctx context.Context, commands []*controlpb.Command) error {
	now := time.Now().UnixNano()
	for _, command := range commands {
		if command.CommandId == "" {
			command.CommandId = b.idgen()
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

func (b *Bus) Resolve(ctx context.Context, id string) (*controlpb.Command, error) {
	b.mu.Lock()
	if element := b.items[id]; element != nil {
		b.order.MoveToFront(element)
		command := proto.Clone(element.Value.(*cachedCommand).command).(*controlpb.Command)
		b.mu.Unlock()
		return command, nil
	}
	b.mu.Unlock()
	command, err := b.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	b.cache(command)
	return proto.Clone(command).(*controlpb.Command), nil
}

func (b *Bus) Replay(ctx context.Context, session *grpcapi.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session.WakeCommands()
	return nil
}

// PendingBatch is called only by the session send owner. lastSent is a
// delivery cursor, not an ACK cursor: it lets a healthy session stream each
// journal record once while reconnect creates a fresh cursor for replay.
func (b *Bus) PendingBatch(ctx context.Context, session *grpcapi.Session) ([]*controlpb.Command, error) {
	capacity := session.CommandCapacity()
	if capacity <= 0 {
		return nil, nil
	}
	commands, err := b.store.Pending(ctx, session.AgentID(), session.LastSent().Load(), capacity)
	if err != nil {
		return nil, err
	}
	for _, command := range commands {
		b.cache(command)
		session.LastSent().Store(command.Sequence)
		session.MarkCommandSent(command.CommandId)
	}
	return commands, nil
}

func (b *Bus) Acknowledge(ctx context.Context, ack *controlpb.CommandAck) (*controlpb.Command, error) {
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

func (b *Bus) cache(command *controlpb.Command) {
	copyCommand := proto.Clone(command).(*controlpb.Command)
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
