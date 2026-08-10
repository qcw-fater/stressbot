package agent

import (
	"time"

	"github.com/cenkalti/backoff/v4"
)

// newExponentialBackoff 构造带 jitter 的指数退避（随机因子 0.5）。
//
// 收敛 agent/admin 两端原本各自手写的指数退避循环，统一交给 cenkalti/backoff：
//   - Multiplier=2.0：每次退避区间翻倍，与原手写逻辑一致；
//   - RandomizationFactor=0.5：实际等待在区间 ±50% 内随机，防止多 Agent 同步
//     重连/重试造成的惊群（原手写实现均无 jitter，这是真实缺陷）；
//   - MaxElapsedTime=0：永不自动停止——终止时机由调用方控制（WithMaxRetries
//     限制次数、WithContext 响应取消，或周期循环里自行 break）。
//
// 注意：ExponentialBackOff 非并发安全，每个调用点各自构造一个实例。
func newExponentialBackoff(initial, max time.Duration) *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(initial),
		backoff.WithMaxInterval(max),
	)
	b.MaxElapsedTime = 0 // 无限重试，由调用方控制终止
	b.RandomizationFactor = 0.5
	b.Multiplier = 2.0
	return b
}
