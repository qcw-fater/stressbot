package engine

import (
	"strconv"
	"strings"

	"go.uber.org/zap"

	"stressbot/state"
	stresslog "stressbot/utils/log"
)

// EvalCondition 求值 state: 前缀的条件表达式。
// 支持复合条件：&&、||、!、括号嵌套。
// 示例：state:hp > 0 && (state:alive || state:isAdmin)
func EvalCondition(expr string, s *state.Store) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}

	if !strings.HasPrefix(expr, "state:") {
		stresslog.Warn("[ENGINE] 条件表达式格式错误，仅支持 state: 前缀",
			zap.String("expr", expr))
		return false
	}
	return parseExpr(expr[6:], s)
}

// parseRHS 尝试将条件右值解析为数值类型，保留字符串回退。
// 支持 nil 字面量：返回 Go nil，用于 nil 比较（state:key == nil / state:key != nil）。
func parseRHS(s string) any {
	s = strings.TrimSpace(s)
	if s == "nil" {
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return s
}
