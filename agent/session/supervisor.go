package session

import (
	"context"
	"fmt"
	"time"

	"stressbot/internal/retry"
	"stressbot/internal/stresslog"
	"stressbot/internal/timerpool"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// SupervisorConfig 定义 gRPC 控制面重连策略。
type SupervisorConfig struct {
	Address              string
	ReconnectInterval    time.Duration
	ReconnectMaxInterval time.Duration
	ReconnectMaxRetries  int
	MaxMessageSize       int
}

// RunSupervisor 维护到 Admin 的长连接，并在可恢复错误后指数退避重连。
func RunSupervisor(ctx context.Context, cfg SupervisorConfig, runConnection func(context.Context, *grpc.ClientConn) error) error {
	policy := retry.NewExponentialBackOff(retry.Policy{Initial: cfg.ReconnectInterval, Max: cfg.ReconnectMaxInterval, Factor: 2, Jitter: 1})
	attempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		conn, err := grpc.NewClient(cfg.Address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cfg.MaxMessageSize), grpc.MaxCallSendMsgSize(cfg.MaxMessageSize)),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 2 * time.Minute, Timeout: 20 * time.Second, PermitWithoutStream: true}),
		)
		if err == nil {
			connectedAt := time.Now()
			err = runConnection(ctx, conn)
			healthy := time.Since(connectedAt) >= cfg.ReconnectMaxInterval
			_ = conn.Close()
			// 存活超过一个最大退避间隔的会话视为健康连接：其断线是新一次
			// 偶发故障的第一次失败，重置退避与重试计数，让重连始终从初始
			// 间隔起步；更短的存活可能是握手级失败，继续按既有退避升级。
			if healthy {
				policy.Reset()
				attempts = 0
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		code := status.Code(err)
		if code == codes.Unauthenticated || code == codes.PermissionDenied || code == codes.FailedPrecondition {
			return fmt.Errorf("gRPC 控制面不可恢复错误: %w", err)
		}
		attempts++
		if cfg.ReconnectMaxRetries >= 0 && attempts > cfg.ReconnectMaxRetries {
			return err
		}
		delay := policy.NextBackOff()
		if delay == backoff.Stop {
			delay = cfg.ReconnectMaxInterval
		}
		stresslog.Warn("[AGENT] gRPC 控制面不可用，准备重连",
			zap.String("adminAddress", cfg.Address),
			zap.Int("retryAttempt", attempts),
			zap.Duration("backoff", delay),
			zap.Error(err))
		timer := timerpool.GetTimer(delay)
		select {
		case <-ctx.Done():
			timerpool.PutTimer(timer)
			return nil
		case <-timer.C:
			timerpool.PutTimer(timer)
		}
	}
}
