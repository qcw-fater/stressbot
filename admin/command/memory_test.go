package command

import (
	"context"
	"errors"
	"testing"

	"stressbot/controlplane/pb"
)

func TestMemoryCommandStoreEvictsOldestTerminalCommand(t *testing.T) {
	store := NewMemoryStore(2)
	first := memoryTestCommand("command-1", "agent-1")
	second := memoryTestCommand("command-2", "agent-1")
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{first, second}); err != nil {
		t.Fatalf("CreateBatch(first, second) error = %v", err)
	}
	if err := store.Acknowledge(context.Background(), first.CommandId, controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED, ""); err != nil {
		t.Fatalf("Acknowledge(first) error = %v", err)
	}

	third := memoryTestCommand("command-3", "agent-1")
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{third}); err != nil {
		t.Fatalf("CreateBatch(third) error = %v", err)
	}
	if _, err := store.Get(context.Background(), first.CommandId); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("Get(first) error = %v, want ErrCommandNotFound", err)
	}
	pending, err := store.Pending(context.Background(), "agent-1", 0, 10)
	if err != nil {
		t.Fatalf("Pending() error = %v", err)
	}
	if len(pending) != 2 || pending[0].CommandId != second.CommandId || pending[1].CommandId != third.CommandId {
		t.Fatalf("Pending() = %#v, want command-2 then command-3", pending)
	}
}

func TestMemoryCommandStoreNeverEvictsPendingCommand(t *testing.T) {
	store := NewMemoryStore(1)
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{memoryTestCommand("command-1", "agent-1")}); err != nil {
		t.Fatalf("CreateBatch(first) error = %v", err)
	}
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{memoryTestCommand("command-2", "agent-1")}); !errors.Is(err, ErrCommandStoreFull) {
		t.Fatalf("CreateBatch(second) error = %v, want ErrCommandStoreFull", err)
	}
}

func TestMemoryCommandStoreInvalidBatchDoesNotEvictTerminalCommand(t *testing.T) {
	store := NewMemoryStore(1)
	first := memoryTestCommand("command-1", "agent-1")
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{first}); err != nil {
		t.Fatalf("CreateBatch(first) error = %v", err)
	}
	if err := store.Acknowledge(context.Background(), first.CommandId, controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED, ""); err != nil {
		t.Fatalf("Acknowledge(first) error = %v", err)
	}

	invalid := memoryTestCommand("command-2", "")
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{invalid}); err == nil {
		t.Fatal("CreateBatch(invalid) error = nil, want validation error")
	}
	if _, err := store.Get(context.Background(), first.CommandId); err != nil {
		t.Fatalf("Get(first) error = %v, failed batch must not mutate the store", err)
	}
}

func TestMemoryCommandStorePendingUsesPerAgentSequenceOrder(t *testing.T) {
	store := NewMemoryStore(4)
	first := memoryTestCommand("command-1", "agent-1")
	other := memoryTestCommand("command-2", "agent-2")
	last := memoryTestCommand("command-3", "agent-1")
	if err := store.CreateBatch(context.Background(), []*controlpb.Command{first, other, last}); err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	if err := store.Acknowledge(context.Background(), first.CommandId, controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED, ""); err != nil {
		t.Fatalf("Acknowledge(first) error = %v", err)
	}

	pending, err := store.Pending(context.Background(), "agent-1", 0, 10)
	if err != nil {
		t.Fatalf("Pending() error = %v", err)
	}
	if len(pending) != 1 || pending[0].CommandId != last.CommandId {
		t.Fatalf("Pending() = %#v, want only command-3", pending)
	}
}

func memoryTestCommand(id, agentID string) *controlpb.Command {
	return &controlpb.Command{
		CommandId: id,
		AgentId:   agentID,
		Body: &controlpb.Command_Shutdown{
			Shutdown: &controlpb.Shutdown{Reason: "test"},
		},
	}
}
