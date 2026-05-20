package utils

import (
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// ParseDurationDefault 解析 duration 字符串，空字符串或非法值返回 fallback。
// 空值（未配置）用 Info 日志；非法值（配置错误）用 Warn 日志。
func ParseDurationDefault(s string, fallback time.Duration, label ...string) time.Duration {
	tag := "duration"
	if len(label) > 0 {
		tag = label[0]
	}
	if s == "" {
		stresslog.Info("[CONFIG] 配置未填写，使用默认值",
			zap.String("key", tag),
			zap.String("default", fallback.String()))
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		stresslog.Warn("[CONFIG] 配置值非法，使用默认值",
			zap.String("key", tag),
			zap.String("value", s),
			zap.String("default", fallback.String()),
			zap.NamedError("parseError", err))
		return fallback
	}
	return d
}
