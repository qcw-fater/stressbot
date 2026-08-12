package utils

import (
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// ParseDurationDefault 解析 duration 字符串，空字符串或非法值返回 fallback。
// 空值和非法值均用 Warn 日志提醒用户（logger 未初始化时跳过，供单元测试使用）。
func ParseDurationDefault(s string, fallback time.Duration, label ...string) time.Duration {
	tag := "duration"
	if len(label) > 0 {
		tag = label[0]
	}
	if s == "" {
		if stresslog.GetLogger() != nil {
			stresslog.Warn("[CONFIG] 配置未填写，使用默认值",
				zap.String("key", tag),
				zap.String("default", fallback.String()))
		}
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		if stresslog.GetLogger() != nil {
			stresslog.Warn("[CONFIG] 配置值非法，使用默认值",
				zap.String("key", tag),
				zap.String("value", s),
				zap.String("default", fallback.String()),
				zap.NamedError("parseError", err))
		}
		return fallback
	}
	return d
}

// ParseIntDefault 返回整数值，零值回退 fallback 并打 Warn 日志（logger 未初始化时跳过）。
func ParseIntDefault(v, fallback int, label ...string) int {
	tag := "int"
	if len(label) > 0 {
		tag = label[0]
	}
	if v == 0 {
		if stresslog.GetLogger() != nil {
			stresslog.Warn("[CONFIG] 配置未填写，使用默认值",
				zap.String("key", tag),
				zap.Int("default", fallback))
		}
		return fallback
	}
	return v
}
