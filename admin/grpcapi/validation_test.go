package grpcapi

import "testing"

func TestRequireAgentID(t *testing.T) {
	if err := requireAgentID("agent-001"); err != nil {
		t.Fatalf("requireAgentID(valid) error = %v", err)
	}
	if err := requireAgentID(""); err == nil {
		t.Fatal("requireAgentID(empty) error = nil")
	}
}
