// Package engine 提供流程执行引擎。
// cond_eval.go 是条件表达式求值的公开入口。
package engine

import (
	"strings"

	"go.uber.org/zap"

	"stressbot/state"
	stresslog "stressbot/utils/log"
)

// EvalCondition 求值 state: 前缀的条件表达式。
// 支持复合条件：&&、||、!、括号、算术（+ - * / %）与比较。
// 示例：state:hp > 0 && (alive || isAdmin)、state:index % 2 == 0。
// 严格类型、无隐式转换：详见 cond_parser.go 顶部文法与纪律说明。
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
