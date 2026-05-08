package admin

import "strconv"

func stringOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func intOr(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// secsOr int 秒数 → Go duration 字符串（"5s"）。
func secsOr(v, fallback int) string {
	if v <= 0 {
		v = fallback
	}
	return strconv.Itoa(v) + "s"
}
