package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

func (a *Agent) connectionSupervisor(ctx context.Context) error {
	policy := utils.NewExponentialBackOff(utils.RetryPolicy{Initial: a.cfg.ReconnectInterval, Max: a.cfg.ReconnectMaxInterval, Factor: 2, Jitter: 1})
	attempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		conn, err := grpc.NewClient(a.cfg.AdminAddress,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxControlMessageSize), grpc.MaxCallSendMsgSize(maxControlMessageSize)),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 2 * time.Minute, Timeout: 20 * time.Second, PermitWithoutStream: true}),
		)
		if err == nil {
			err = a.runConnection(ctx, conn)
			_ = conn.Close()
		}
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			if ctx.Err() != nil {
				return nil
			}
		}
		code := status.Code(err)
		if code == codes.Unauthenticated || code == codes.PermissionDenied || code == codes.FailedPrecondition {
			return fmt.Errorf("gRPC 控制面不可恢复错误: %w", err)
		}
		attempts++
		if a.cfg.ReconnectMaxRetries >= 0 && attempts > a.cfg.ReconnectMaxRetries {
			return err
		}
		delay := policy.NextBackOff()
		if delay == backoff.Stop {
			delay = a.cfg.ReconnectMaxInterval
		}
		stresslog.Warn("[AGENT] gRPC 控制面断开，准备重连", zap.Duration("backoff", delay), zap.Error(err))
		timer := utils.GetTimer(delay)
		select {
		case <-ctx.Done():
			utils.PutTimer(timer)
			return nil
		case <-timer.C:
			utils.PutTimer(timer)
		}
	}
}
