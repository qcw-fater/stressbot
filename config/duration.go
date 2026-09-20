package config

import (
	"fmt"
	"strings"
	"time"
)

// 时长配置项的统一口径：**未填才回退默认，写错一律报错**。
//
// 不把「解析失败」和「值非正」并进「未填」一起回退默认值：那样配置打错不会被发现，
// 告警淹在启动日志里，等压测跑完才看出生效的超时窗口根本不是配的那个。
// fallback 只服务「这一项没填」这一种情形。
//
// 两个变体的区别只在 0 是否合法：
//   - ParseDuration：0 是合法的显式值，语义由调用方定义（如 MySQL 超时 0 = 不限制）。
//   - ParsePositiveDuration：0 非法。间隔与超时类配置取 0 会退化成忙循环或立即超时，
//     这类项必须拒绝，不能让它悄悄生效。

// ParseDuration 解析时长配置项：留空回退 fallback，非法或负数报错，0 视为合法显式值。
func ParseDuration(raw string, fallback time.Duration, key string) (time.Duration, error) {
	value, ok, err := parseDurationRaw(raw, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return fallback, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("%s 不能为负数，当前 %q", key, raw)
	}
	return value, nil
}

// ParsePositiveDuration 解析必须为正的时长配置项（间隔、超时）：留空回退 fallback，
// 非法或 <= 0 报错。
func ParsePositiveDuration(raw string, fallback time.Duration, key string) (time.Duration, error) {
	value, ok, err := parseDurationRaw(raw, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return fallback, nil
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s 必须大于 0，当前 %q", key, raw)
	}
	return value, nil
}

// parseDurationRaw 解析时长文本；第二个返回值为 false 表示该项未填。
func parseDurationRaw(raw, key string) (time.Duration, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false, nil
	}
	value, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, false, fmt.Errorf("%s 不是合法时长 %q: %w", key, raw, err)
	}
	return value, true, nil
}
