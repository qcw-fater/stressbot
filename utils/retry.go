package utils

import (
	"errors"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// ErrRetryStopped 表示重试在下一次操作执行前被调用方终止。
var ErrRetryStopped = errors.New("重试已停止")

// RetryPolicy 描述指数退避策略。Jitter 为 0 时关闭抖动，取值范围为 [0, 1]。
type RetryPolicy struct {
	Initial time.Duration
	Max     time.Duration
	Factor  float64
	Jitter  float64
}

// NewExponentialBackOff 使用 cenkalti/backoff 构造无总时长上限的指数退避实例。
// 实例非并发安全，每条重试链路必须独占一个实例。
func NewExponentialBackOff(policy RetryPolicy) *backoff.ExponentialBackOff {
	initial := policy.Initial
	if initial <= 0 {
		initial = time.Second
	}
	maxInterval := policy.Max
	if maxInterval <= 0 {
		maxInterval = 30 * time.Second
	}
	if maxInterval < initial {
		maxInterval = initial
	}
	factor := policy.Factor
	if factor <= 1 {
		factor = 2
	}
	jitter := policy.Jitter
	if jitter < 0 {
		jitter = 0
	} else if jitter > 1 {
		jitter = 1
	}

	return backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(initial),
		backoff.WithMaxInterval(maxInterval),
		backoff.WithMultiplier(factor),
		backoff.WithRandomizationFactor(jitter),
		backoff.WithMaxElapsedTime(0),
	)
}

// RetryWithStop 执行 op，直到成功、退避策略停止、遇到永久错误或 stop 关闭。
// 等待使用项目 timer 池；stop 已关闭时不会再发起一次 op。
func RetryWithStop(stop <-chan struct{}, op func() error, notify func(error, time.Duration), b backoff.BackOff) error {
	if b == nil {
		return errors.New("重试退避策略不能为空")
	}
	b.Reset()

	for {
		if stopped(stop) {
			return ErrRetryStopped
		}

		err := op()
		if err == nil {
			return nil
		}
		var permanent *backoff.PermanentError
		if errors.As(err, &permanent) {
			return permanent.Err
		}

		wait := b.NextBackOff()
		if wait == backoff.Stop {
			return err
		}
		if notify != nil {
			notify(err, wait)
		}
		if !waitRetry(stop, wait) {
			return ErrRetryStopped
		}
	}
}

func stopped(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func waitRetry(stop <-chan struct{}, delay time.Duration) bool {
	timer := GetTimer(delay)
	defer PutTimer(timer)
	if stop == nil {
		<-timer.C
		return true
	}
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}
