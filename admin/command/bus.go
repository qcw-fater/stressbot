// Package command 实现 Admin 下发给 Agent 的控制命令日志与调度：
// 命令的写入、按会话的未决投递与 ACK 确认（Bus + Store），以及
// 启动/停止/关停命令的组装编排（Dispatcher）。
package command

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	"stressbot/admin/grpcapi"
	controlpb "stressbot/controlplane/pb"

	"google.golang.org/protobuf/proto"
)

type cachedCommand struct {
	id      string
	command *controlpb.Command
}

// Bus 是命令投递总线：写入经 Store 持久化并唤醒目标 Agent 会话，
// 读取以 LRU 缓存加速按 ID 解析（返回 proto 克隆，调用方可安全修改）。
type Bus struct {
	store    Store
	sessions *grpcapi.SessionRegistry
	idgen    func() string
	mu       sync.Mutex
	capacity int
	order    *list.List
	items    map[string]*list.Element
}

// NewBus 创建命令总线；capacity 为 LRU 缓存条数上限（非正数回退 8192），
// idgen 用于给缺少 CommandId 的命令补齐 ID。
func NewBus(store Store, sessions *grpcapi.SessionRegistry, capacity int, idgen func() string) *Bus {
	if capacity <= 0 {
		capacity = 8192
	}
	return &Bus{store: store, sessions: sessions, idgen: idgen, capacity: capacity, order: list.New(), items: make(map[string]*list.Element)}
}

// CreateBatch 补齐命令的 CommandId 与 CreatedAtUnixNano 后批量写入 Store，
// 随后逐条进入缓存并唤醒对应 Agent 的会话以触发投递。
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

// Resolve 按命令 ID 解析：优先命中 LRU 缓存（并刷新热度），未命中回落
// Store 读取后回填缓存；两种路径都返回 proto 克隆。
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

// Replay 请求会话重放未决命令：仅发出命令就绪信号，由会话发送循环重新
// 调用 PendingBatch 拉取补发；ctx 已取消时直接返回错误。
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

// Acknowledge 处理 Agent 的命令 ACK：校验 AgentId/Sequence/TaskId 与日志
// 记录一致后写入 Store 落终态，并返回被确认的命令。
func (b *Bus) Acknowledge(ctx context.Context, ack *controlpb.CommandAck) (*controlpb.Command, error) {
	if ack == nil || ack.CommandId == "" {
		return nil, errors.New("命令 ACK 缺少 commandId")
	}
	command, err := b.Resolve(ctx, ack.CommandId)
	if err != nil {
		return nil, err
	}
	if command.AgentId != ack.AgentId || command.Sequence != ack.Sequence || command.TaskId != ack.TaskId {
		return nil, errors.New("命令 ACK 与日志记录不匹配")
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
