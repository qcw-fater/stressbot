package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/internal/stresslog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
)

func TestRunSupervisorLogsRetryContext(t *testing.T) {
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })

	ctx, cancel := context.WithCancel(context.Background())
	errUnavailable := errors.New("控制面不可用")
	calls := 0
	err := RunSupervisor(ctx, SupervisorConfig{
		Address: "127.0.0.1:7720", ReconnectInterval: time.Millisecond,
		ReconnectMaxInterval: time.Millisecond, ReconnectMaxRetries: -1, MaxMessageSize: 1024,
	}, func(context.Context, *grpc.ClientConn) error {
		calls++
		if calls == 1 {
			return errUnavailable
		}
		cancel()
		return context.Canceled
	})
	if err != nil {
		t.Fatalf("RunSupervisor() = %v，期望正常取消", err)
	}

	entries := logs.FilterMessage("[AGENT] gRPC 控制面不可用，准备重连").All()
	if len(entries) != 1 {
		t.Fatalf("重连日志数量 = %d，期望 1；全部日志: %v", len(entries), logs.All())
	}
	fields := entries[0].ContextMap()
	if fields["adminAddress"] != "127.0.0.1:7720" || fields["retryAttempt"] != int64(1) {
		t.Fatalf("重连日志上下文字段不完整: %v", fields)
	}
	if calls != 2 {
		t.Fatalf("连接尝试次数 = %d，期望首次失败后真实重连一次", calls)
	}
}

func TestRunSupervisorDoesNotLogRetryAfterCancellation(t *testing.T) {
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })

	ctx, cancel := context.WithCancel(context.Background())
	err := RunSupervisor(ctx, SupervisorConfig{
		Address: "127.0.0.1:7720", ReconnectInterval: time.Millisecond,
		ReconnectMaxInterval: time.Millisecond, ReconnectMaxRetries: -1, MaxMessageSize: 1024,
	}, func(context.Context, *grpc.ClientConn) error {
		cancel()
		return errors.New("关闭期间连接中断")
	})
	if err != nil {
		t.Fatalf("RunSupervisor() = %v，期望正常取消", err)
	}
	if entries := logs.FilterMessage("[AGENT] gRPC 控制面不可用，准备重连").All(); len(entries) != 0 {
		t.Fatalf("关闭后不应记录准备重连: %v", entries)
	}
}

// 健康会话（存活超过最大退避间隔）断线后，退避与重试计数必须重置：
// 否则历史故障会把退避永久推高、重试预算被永久消耗，后续任何一次
// 例行断线都可能因重连超过租约时长而被误杀任务。
func TestRunSupervisorResetsBackoffAfterHealthySession(t *testing.T) {
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RunSupervisor(ctx, SupervisorConfig{
		Address: "127.0.0.1:7720", ReconnectInterval: time.Millisecond,
		ReconnectMaxInterval: 5 * time.Millisecond, ReconnectMaxRetries: 2, MaxMessageSize: 1024,
	}, func(context.Context, *grpc.ClientConn) error {
		calls++
		switch calls {
		case 3:
			// 第三次连接存活超过最大退避间隔，视为健康会话后断线。
			time.Sleep(8 * time.Millisecond)
			return errors.New("健康会话断线")
		case 5:
			cancel()
			return context.Canceled
		default:
			return errors.New("连接失败")
		}
	})
	if err != nil {
		t.Fatalf("RunSupervisor() = %v，期望重试预算在健康会话后恢复", err)
	}

	entries := logs.FilterMessage("[AGENT] gRPC 控制面不可用，准备重连").All()
	var attempts []int64
	for _, entry := range entries {
		value, _ := entry.ContextMap()["retryAttempt"].(int64)
		attempts = append(attempts, value)
	}
	// 两次失败 → 健康会话断线（计数已重置）→ 两次失败 → 取消。
	// 若未重置，第三次失败时 attempts=3 已超过 ReconnectMaxRetries=2，
	// RunSupervisor 会提前放弃并返回错误。
	want := []int64{1, 2, 1, 2}
	if len(attempts) != len(want) {
		t.Fatalf("重连日志序列 = %v，期望 %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("重连日志序列 = %v，期望 %v", attempts, want)
		}
	}
}

// 存活不足最大退避间隔的短命连接不触发重置：握手级失败继续按既有退避升级，
// 防止快速失败循环被错误地当作新故障序列、退避永远停在初始间隔。
func TestRunSupervisorKeepsBackoffAfterShortLivedSession(t *testing.T) {
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RunSupervisor(ctx, SupervisorConfig{
		Address: "127.0.0.1:7720", ReconnectInterval: time.Millisecond,
		ReconnectMaxInterval: time.Hour, ReconnectMaxRetries: -1, MaxMessageSize: 1024,
	}, func(context.Context, *grpc.ClientConn) error {
		calls++
		if calls == 4 {
			cancel()
			return context.Canceled
		}
		return errors.New("连接失败")
	})
	if err != nil {
		t.Fatalf("RunSupervisor() = %v，期望正常取消", err)
	}

	entries := logs.FilterMessage("[AGENT] gRPC 控制面不可用，准备重连").All()
	var attempts []int64
	for _, entry := range entries {
		value, _ := entry.ContextMap()["retryAttempt"].(int64)
		attempts = append(attempts, value)
	}
	want := []int64{1, 2, 3}
	if len(attempts) != len(want) {
		t.Fatalf("重连日志序列 = %v，期望 %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("重连日志序列 = %v，期望 %v", attempts, want)
		}
	}
}
