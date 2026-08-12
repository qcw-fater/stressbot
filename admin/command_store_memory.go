package admin

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"stressbot/controlplane/controlv1"

	"google.golang.org/protobuf/proto"
)

type memoryCommand struct {
	command *controlv1.Command
	state   string
}

type MemoryCommandStore struct {
	mu       sync.RWMutex
	commands map[string]*memoryCommand
	next     uint64
	capacity int
}

func NewMemoryCommandStore(capacity int) *MemoryCommandStore {
	if capacity <= 0 {
		capacity = 100_000
	}
	return &MemoryCommandStore{commands: make(map[string]*memoryCommand), capacity: capacity}
}

func (s *MemoryCommandStore) CreateBatch(ctx context.Context, commands []*controlv1.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.makeRoomLocked(len(commands))
	if len(s.commands)+len(commands) > s.capacity {
		return ErrCommandStoreFull
	}
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		if command == nil || command.CommandId == "" || command.AgentId == "" {
			return fmt.Errorf("命令身份字段无效")
		}
		if _, err := commandKind(command); err != nil {
			return err
		}
		if _, exists := s.commands[command.CommandId]; exists {
			return fmt.Errorf("命令 ID 重复: %s", command.CommandId)
		}
		if _, exists := seen[command.CommandId]; exists {
			return fmt.Errorf("批次命令 ID 重复: %s", command.CommandId)
		}
		seen[command.CommandId] = struct{}{}
	}
	for _, command := range commands {
		s.next++
		command.Sequence = s.next
		if command.CreatedAtUnixNano == 0 {
			command.CreatedAtUnixNano = time.Now().UnixNano()
		}
		s.commands[command.CommandId] = &memoryCommand{
			command: proto.Clone(command).(*controlv1.Command),
			state:   "pending",
		}
	}
	return nil
}

func (s *MemoryCommandStore) makeRoomLocked(needed int) {
	overflow := len(s.commands) + needed - s.capacity
	if overflow <= 0 {
		return
	}
	candidates := make([]*memoryCommand, 0, len(s.commands))
	for _, record := range s.commands {
		if record.state != "pending" {
			candidates = append(candidates, record)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].command.Sequence < candidates[j].command.Sequence })
	for i := 0; i < overflow && i < len(candidates); i++ {
		delete(s.commands, candidates[i].command.CommandId)
	}
}

func (s *MemoryCommandStore) Get(ctx context.Context, id string) (*controlv1.Command, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.commands[id]
	if !ok {
		return nil, ErrCommandNotFound
	}
	return proto.Clone(record.command).(*controlv1.Command), nil
}

func (s *MemoryCommandStore) Pending(ctx context.Context, agentID string, after uint64, limit int) ([]*controlv1.Command, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > commandReplayBatchSize {
		limit = commandReplayBatchSize
	}
	out := make([]*controlv1.Command, 0, min(limit, len(s.commands)))
	for _, record := range s.commands {
		if record.state == "pending" && record.command.AgentId == agentID && record.command.Sequence > after {
			out = append(out, proto.Clone(record.command).(*controlv1.Command))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryCommandStore) Acknowledge(ctx context.Context, id string, status controlv1.CommandAckStatus, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := commandState(status)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.commands[id]
	if !ok {
		return ErrCommandNotFound
	}
	if record.state == "pending" {
		record.state = state
	}
	return nil
}

func (s *MemoryCommandStore) CancelPendingTaskCommands(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.commands {
		if record.state == "pending" && record.command.TaskId != "" {
			record.state = "rejected"
		}
	}
	return nil
}

func (s *MemoryCommandStore) Close() error { return nil }
