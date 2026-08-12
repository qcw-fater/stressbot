package agent

import "testing"

func TestResolveAllowsPlaintextGRPCControlPlane(t *testing.T) {
	cfg := Config{
		ID:                "agent-001",
		AdminAddress:      "127.0.0.1:7720",
		MetricsInterval:   "5s",
		HeartbeatInterval: "10s",
	}
	if _, err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
}
