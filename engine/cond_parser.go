package engine

import (
	"strings"

	"stressbot/state"
)

// parseExpr 解析布尔表达式（已剥离 state: 前缀）。
//
// 语法（优先级高→低）：
//
//	expr     → or_expr
//	or_expr  → and_expr ( "||" and_expr )*
//	and_expr → unary ( "&&" unary )*
//	unary    → "!" unary | atom
//	atom     → "(" expr ")" | comparison
//	comparison → key op value | key
//	op       → >= | <= | != | == | > | <
func parseExpr(input string, s *state.Store) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return true
	}
	return parseOr(input, s)
}

// parseOr 按 || 拆分（括号外层），任一为 true 即返回 true。
func parseOr(input string, s *state.Store) bool {
	parts := splitOutsideParens(input, "||")
	if len(parts) == 1 {
		return parseAnd(parts[0], s)
	}
	for _, p := range parts {
		if parseAnd(p, s) {
			return true
		}
	}
	return false
}

// parseAnd 按 && 拆分（括号外层），全部为 true 才返回 true。
func parseAnd(input string, s *state.Store) bool {
	parts := splitOutsideParens(input, "&&")
	if len(parts) == 1 {
		return parseUnary(parts[0], s)
	}
	for _, p := range parts {
		if !parseUnary(p, s) {
			return false
		}
	}
	return true
}

// parseUnary 处理 ! 前缀取反。
func parseUnary(input string, s *state.Store) bool {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "!") {
		return !parseUnary(strings.TrimSpace(input[1:]), s)
	}
	return parseAtom(input, s)
}

// parseAtom 匹配括号分组或原子条件。
func parseAtom(input string, s *state.Store) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return true
	}

	// 括号分组
	if strings.HasPrefix(input, "(") && findCloseParen(input) == len(input)-1 {
		return parseOr(input[1 : len(input)-1], s)
	}

	return evalAtom(input, s)
}

// evalAtom 求值单个原子条件：key op value 或 key（truthy）。
func evalAtom(input string, s *state.Store) bool {
	input = strings.TrimSpace(input)

	for _, op := range []string{">=", "<=", "!=", "==", ">", "<"} {
		if idx := findOpOutsideParens(input, op); idx > 0 {
			key := strings.TrimSpace(input[:idx])
			rhsStr := strings.TrimSpace(input[idx+len(op):])
			lhs := s.Get(key)
			if lhs == nil {
				return false
			}
			rhs := parseRHS(rhsStr)
			return state.CompareValues(lhs, rhs, op)
		}
	}

	val := s.Get(input)
	if val == nil {
		return false
	}
	switch v := val.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	default:
		return true
	}
}

// splitOutsideParens 按分隔符拆分字符串，跳过括号内的部分。
func splitOutsideParens(input, sep string) []string {
	var parts []string
	depth := 0
	start := 0
	i := 0
	for i < len(input) {
		switch input[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(input[i:], sep) {
			parts = append(parts, strings.TrimSpace(input[start:i]))
			i += len(sep)
			start = i
			continue
		}
		i++
	}
	parts = append(parts, strings.TrimSpace(input[start:]))
	return parts
}

// findCloseParen 找到与第一个 ( 匹配的 ) 位置，未匹配返回 -1。
func findCloseParen(input string) int {
	depth := 0
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findOpOutsideParens 在括号外层查找运算符位置。
func findOpOutsideParens(input, op string) int {
	depth := 0
	for i := 0; i <= len(input)-len(op); i++ {
		switch input[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && input[i:i+len(op)] == op {
			return i
		}
	}
	return -1
}
