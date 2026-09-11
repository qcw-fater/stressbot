package agent

import (
	"testing"

	"stressbot/internal/stresslog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestRegisterExistingAgentIsNormalLifecycleLog(t *testing.T) {
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })

	registry := NewRegistry(Config{}, nil)
	if err := registry.Register(&Node{ID: "agent-001", Name: "测试节点", Status: Idle}); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	if err := registry.Register(&Node{ID: "agent-001", Name: "测试节点", Status: Idle}); err != nil {
		t.Fatalf("重新注册失败: %v", err)
	}

	entries := logs.FilterMessage("agent 重新注册").All()
	if len(entries) != 1 {
		t.Fatalf("重新注册日志数量 = %d，期望 1", len(entries))
	}
	if entries[0].Level != zapcore.InfoLevel {
		t.Fatalf("重新注册日志级别 = %s，期望 info", entries[0].Level)
	}
}
