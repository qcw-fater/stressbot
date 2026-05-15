package utils

import (
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// ParseDurationDefault 解析 duration 字符串，空字符串或非法值返回 fallback 并记录警告。
func ParseDurationDefault(s string, fallback time.Duration, label ...string) time.Duration {
	tag := "duration"
	if len(label) > 0 {
		tag = label[0]
	}
	if s == "" {
		stresslog.Warn("[CONFIG] 配置为空，使用默认值",
			zap.String("key", tag),
			zap.String("default", fallback.String()))
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		stresslog.Warn("[CONFIG] 配置非法，使用默认值",
			zap.String("key", tag),
			zap.String("value", s),
			zap.String("default", fallback.String()),
			zap.NamedError("parseError", err))
		return fallback
	}
	return d
}
