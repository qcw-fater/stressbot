package admin

import (
	"container/list"
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
	mu               sync.RWMutex
	commands         map[string]*memoryCommand
	pendingByAgent   map[string][]*memoryCommand
	terminalCommands *list.List
	next             uint64
	capacity         int
}

func NewMemoryCommandStore(capacity int) *MemoryCommandStore {
	if capacity <= 0 {
		capacity = 100_000
	}
	return &MemoryCommandStore{
		commands:         make(map[string]*memoryCommand),
		pendingByAgent:   make(map[string][]*memoryCommand),
		terminalCommands: list.New(),
		capacity:         capacity,
	}
}

func (s *MemoryCommandStore) CreateBatch(ctx context.Context, commands []*controlv1.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if !s.makeRoomLocked(len(commands)) {
		return ErrCommandStoreFull
	}
	now := time.Now().UnixNano()
	for _, command := range commands {
		s.next++
		command.Sequence = s.next
		if command.CreatedAtUnixNano == 0 {
			command.CreatedAtUnixNano = now
		}
		record := &memoryCommand{
			command: proto.Clone(command).(*controlv1.Command),
			state:   "pending",
		}
		s.commands[command.CommandId] = record
		s.pendingByAgent[command.AgentId] = append(s.pendingByAgent[command.AgentId], record)
	}
	return nil
}

func (s *MemoryCommandStore) makeRoomLocked(needed int) bool {
	overflow := len(s.commands) + needed - s.capacity
	if overflow <= 0 {
		return true
	}
	if overflow > s.terminalCommands.Len() {
		return false
	}
	for range overflow {
		oldest := s.terminalCommands.Front()
		record := oldest.Value.(*memoryCommand)
		delete(s.commands, record.command.CommandId)
		s.terminalCommands.Remove(oldest)
	}
	return true
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
	records := s.pendingByAgent[agentID]
	start := sort.Search(len(records), func(i int) bool {
		return records[i].command.Sequence > after
	})
	count := min(limit, len(records)-start)
	out := make([]*controlv1.Command, 0, count)
	for _, record := range records[start : start+count] {
		out = append(out, proto.Clone(record.command).(*controlv1.Command))
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
		s.removePendingLocked(record)
		s.terminalCommands.PushBack(record)
	}
	return nil
}

func (s *MemoryCommandStore) removePendingLocked(target *memoryCommand) {
	agentID := target.command.AgentId
	records := s.pendingByAgent[agentID]
	for i, record := range records {
		if record != target {
			continue
		}
		copy(records[i:], records[i+1:])
		records[len(records)-1] = nil
		records = records[:len(records)-1]
		if len(records) == 0 {
			delete(s.pendingByAgent, agentID)
		} else {
			s.pendingByAgent[agentID] = records
		}
		return
	}
}
