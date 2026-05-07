package admin

import "strconv"

// stringOr 返回 v；当 v 为空字符串时返回 fallback。
func stringOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// intOr 返回 v；当 v <= 0 时返回 fallback。
//
// 选择 "v <= 0" 而非 "v == 0"：业务里的"超时秒/心跳秒/Apdex 阈值"等
// 都不可能是 0 或负数，零值即视为"未配置，用默认"。
func intOr(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// secsOr 把 int 秒数（前端友好格式）转成 Go duration 字符串（"5s"），
// 当 v <= 0 时使用 fallback 秒数。
//
// 用途：admin 把 RobotConfig 的 IntSec 字段 → TaskAssignment 的 duration 字符串字段，
// agent 端 parseDurationDefault 再解析为 time.Duration。
// 中间这一层 string 转换是 agent 协议历史遗留（早期支持 "5s"/"500ms" 混写），
// 保留不动以兼容。
func secsOr(v, fallback int) string {
	if v <= 0 {
		v = fallback
	}
	return strconv.Itoa(v) + "s"
}
