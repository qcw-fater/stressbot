package robot

import "testing"

func TestManagerRobotIdentityUsesTaskGlobalIndex(t *testing.T) {
	cfg := ManagerConfig{
		AccountPrefix: "bot_",
		StartNumber:   100,
		StartIndex:    6,
	}

	id, index, account := cfg.robotIdentity(0)
	if index != 6 {
		t.Errorf("index = %d, want 6", index)
	}
	if id != 106 {
		t.Errorf("id = %d, want 106", id)
	}
	if account != "bot_106" {
		t.Errorf("account = %q, want %q", account, "bot_106")
	}
}

func TestManagerRobotIdentityKeepsGlobalIndexAcrossBatches(t *testing.T) {
	cfg := ManagerConfig{
		AccountPrefix: "bot_",
		StartNumber:   20,
		StartIndex:    40,
	}

	id, index, account := cfg.robotIdentity(7)
	if index != 47 {
		t.Errorf("index = %d, want 47", index)
	}
	if id != 67 {
		t.Errorf("id = %d, want 67", id)
	}
	if account != "bot_67" {
		t.Errorf("account = %q, want %q", account, "bot_67")
	}
}
