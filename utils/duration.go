package utils

import "time"

// ParseDurationDefault 解析 duration 字符串，空字符串或非法值返回 fallback。
func ParseDurationDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
