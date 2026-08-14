package runner

import (
	"time"

	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"
	"stressbot/network"

	"go.uber.org/zap"
)

const cleanupTimeout = 30 * time.Second

type dialerStopper interface{ Stop() error }

// StopDialer stops the network engine with a bounded wait. If the shared work
// pool rejects cleanup, it falls back to synchronous cleanup immediately.
func StopDialer(dialer *network.Dialer) {
	stopDialerWithSubmit(dialer, workpool.Default().Submit)
}

func stopDialerWithSubmit(dialer dialerStopper, submit func(func()) error) {
	done := make(chan struct{})
	stop := func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				stresslog.DPanic("[TASK] 停止网络引擎时发生 panic", zap.Any("error", recovered))
			}
		}()
		if err := dialer.Stop(); err != nil {
			stresslog.Warn("[TASK] 停止网络引擎失败", zap.Error(err))
		}
	}
	if err := submit(stop); err != nil {
		stresslog.Warn("[TASK] 提交网络引擎清理任务失败，改为同步停止", zap.Error(err))
		stop()
		return
	}
	timer := time.NewTimer(cleanupTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		stresslog.Warn("[TASK] 停止网络引擎超时，强制返回（资源由 OS 回收）", zap.Duration("timeout", cleanupTimeout))
	}
}
