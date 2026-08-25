package command

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/protobuf/proto"
)

type memoryCommand struct {
	command *controlpb.Command
	state   string
}

// MemoryStore 是 Store 的内存实现：命令按 ID 索引，每个 Agent 维持按
// Sequence 递增的未决队列，落终态的命令进入 FIFO 供容量满时优先淘汰。
type MemoryStore struct {
	mu               sync.RWMutex
	commands         map[string]*memoryCommand
	pendingByAgent   map[string][]*memoryCommand
	terminalCommands *list.List
	next             uint64
	capacity         int
}

// NewMemoryStore 创建内存命令日志；capacity 为命令总条数上限，非正数回退 100000。
func NewMemoryStore(capacity int) *MemoryStore {
	if capacity <= 0 {
		capacity = 100_000
	}
	return &MemoryStore{
		commands:         make(map[string]*memoryCommand),
		pendingByAgent:   make(map[string][]*memoryCommand),
		terminalCommands: list.New(),
		capacity:         capacity,
	}
}

// CreateBatch 批量写入命令：先整批校验身份字段、body 类型与 ID 唯一性
// （任一失败整批拒绝），再腾出容量（不足返回 ErrCommandStoreFull），
// 逐条分配全局递增 Sequence 后以 pending 状态入队并克隆留存。
func (s *MemoryStore) CreateBatch(ctx context.Context, commands []*controlpb.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		if command == nil || command.CommandId == "" || command.AgentId == "" {
			return errors.New("命令身份字段无效")
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
			command: proto.Clone(command).(*controlpb.Command),
			state:   "pending",
		}
		s.commands[command.CommandId] = record
		s.pendingByAgent[command.AgentId] = append(s.pendingByAgent[command.AgentId], record)
	}
	return nil
}

func (s *MemoryStore) makeRoomLocked(needed int) bool {
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

// Get 按命令 ID 读取，返回 proto 克隆；不存在时返回 ErrCommandNotFound。
func (s *MemoryStore) Get(ctx context.Context, id string) (*controlpb.Command, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.commands[id]
	if !ok {
		return nil, ErrCommandNotFound
	}
	return proto.Clone(record.command).(*controlpb.Command), nil
}

// Pending 返回指定 Agent 中 Sequence 大于 after 的未决命令（二分定位游标），
// 单批上限 limit，非法值回退 commandReplayBatchSize；结果为 proto 克隆。
func (s *MemoryStore) Pending(ctx context.Context, agentID string, after uint64, limit int) ([]*controlpb.Command, error) {
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
	out := make([]*controlpb.Command, 0, count)
	for _, record := range records[start : start+count] {
		out = append(out, proto.Clone(record.command).(*controlpb.Command))
	}
	return out, nil
}

// Acknowledge 把 pending 命令落为终态（APPLIED/DUPLICATE → acked，
// REJECTED → rejected）并从未决队列挪入终态 FIFO；重复 ACK 幂等无效果。
func (s *MemoryStore) Acknowledge(ctx context.Context, id string, status controlpb.CommandAckStatus, _ string) error {
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

func (s *MemoryStore) removePendingLocked(target *memoryCommand) {
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
