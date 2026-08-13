// Package flow 提供流程执行引擎。
// cond_compare.go 提供条件表达式的严格类型数值分类、比较与算术。
//
// store 的值域闭合且无指针。数值可能以多种 Go 数值类型出现：
//   - 内置字段 id/index 经 state.Set 注入为原生 int（见 robot.go）
//   - proto 反射 fromScalarValue 产出 int64/uint64/float64
//   - JSON/Lua 路径产出 float64/int64
//
// 这里统一用 classifyNumber 归一化，覆盖全部数值类型，但绝不隐式转换 bool/string/
// []byte/list/map——非数值一律拒绝。
package flow

import (
	"fmt"
	"math"
)

// numRep 是值的规范数值表示，集中处理所有数值 Go 类型。
type numRep struct {
	ok    bool    // 是数值
	isInt bool    // 整数类型（int*/uint*，不含 float）
	i     int64   // int64 视图（isInt && !bigU 时精确）
	u     uint64  // 原始 uint64（仅 uint/uint64 类型时有效，用于精确比较）
	isU   bool    // 是否 uint/uint64 类型
	bigU  bool    // uint/uint64 超过 MaxInt64，无法精确表示为 int64
	f     float64 // float64 视图（浮点或降级比较用，uint64>2^53 会失真）
}

// classifyNumber 把任意值归一化为规范数值表示。bool/string/[]byte 等返回 ok=false。
func classifyNumber(v any) numRep {
	switch n := v.(type) {
	case int:
		return numRep{ok: true, isInt: true, i: int64(n), f: float64(n)}
	case int8:
		return numRep{ok: true, isInt: true, i: int64(n), f: float64(n)}
	case int16:
		return numRep{ok: true, isInt: true, i: int64(n), f: float64(n)}
	case int32:
		return numRep{ok: true, isInt: true, i: int64(n), f: float64(n)}
	case int64:
		return numRep{ok: true, isInt: true, i: n, f: float64(n)}
	case uint:
		if uint64(n) > math.MaxInt64 {
			return numRep{ok: true, isInt: true, isU: true, u: uint64(n), bigU: true, f: float64(n)}
		}
		return numRep{ok: true, isInt: true, isU: true, u: uint64(n), i: int64(n), f: float64(n)}
	case uint8:
		return numRep{ok: true, isInt: true, isU: true, u: uint64(n), i: int64(n), f: float64(n)}
	case uint16:
		return numRep{ok: true, isInt: true, isU: true, u: uint64(n), i: int64(n), f: float64(n)}
	case uint32:
		return numRep{ok: true, isInt: true, isU: true, u: uint64(n), i: int64(n), f: float64(n)}
	case uint64:
		if n > math.MaxInt64 {
			return numRep{ok: true, isInt: true, isU: true, u: n, bigU: true, f: float64(n)}
		}
		return numRep{ok: true, isInt: true, isU: true, u: n, i: int64(n), f: float64(n)}
	case float32:
		return numRep{ok: true, f: float64(n)}
	case float64:
		return numRep{ok: true, f: n}
	}
	return numRep{}
}

// isNumber 判断值是否为任意数值类型（不含 bool/string）。
func isNumber(v any) bool {
	return classifyNumber(v).ok
}

// asBool 要求值为 bool，否则返回错误（布尔上下文禁止隐式 truthy）。
func asBool(v any) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("布尔上下文需要 bool，实际 %T", v)
}

// cmpNumbersExact 精确比较两个数值，返回 -1/0/1。
//
// 两个都是整数类型时按整数精确比较（正确处理 int/int64/uint64 混合，含负数与
// uint64>MaxInt64 的玩家 ID——浮点比较会失真）。任一为浮点时退化为 float64 比较。
func cmpNumbersExact(a, b any) int {
	ra := classifyNumber(a)
	rb := classifyNumber(b)

	if ra.isInt && rb.isInt {
		// 用 isU 区分无符号：非 isU 的整数（int/int64）可能为负
		aNeg := !ra.isU && ra.i < 0
		bNeg := !rb.isU && rb.i < 0
		switch {
		case aNeg && bNeg:
			return cmpInt64(ra.i, rb.i)
		case aNeg: // a<0, b>=0
			return -1
		case bNeg: // a>=0, b<0
			return 1
		default: // 都非负：按 uint64 精确比较
			au := ra.u
			if !ra.isU {
				au = uint64(ra.i)
			}
			bu := rb.u
			if !rb.isU {
				bu = uint64(rb.i)
			}
			return cmpUint64(au, bu)
		}
	}
	return cmpFloat64(ra.f, rb.f)
}

func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpUint64(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpFloat64(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// strictCompare 严格类型比较（comp_op ∈ == != > >= < <=）。
//
// == != 仅同类型；> >= < <= 仅数值。nil 不会到达这里（PATH 解析为 nil 时
// parseFactor 已报错）。跨类型与非标量（[]byte/[]any/map）一律报错，不兜底。
func strictCompare(a, b any, op string) (bool, error) {
	aIsNum := isNumber(a)
	bIsNum := isNumber(b)
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	aBool, aIsBool := a.(bool)
	bBool, bIsBool := b.(bool)

	if !aIsNum && !aIsStr && !aIsBool {
		return false, fmt.Errorf("操作数不是标量类型: %T", a)
	}
	if !bIsNum && !bIsStr && !bIsBool {
		return false, fmt.Errorf("操作数不是标量类型: %T", b)
	}

	switch op {
	case "==", "!=":
		var eq bool
		switch {
		case aIsNum && bIsNum:
			eq = cmpNumbersExact(a, b) == 0
		case aIsStr && bIsStr:
			eq = aStr == bStr
		case aIsBool && bIsBool:
			eq = aBool == bBool
		default:
			return false, fmt.Errorf("%s 类型不匹配（%T 与 %T）", op, a, b)
		}
		if op == "==" {
			return eq, nil
		}
		return !eq, nil
	case ">", ">=", "<", "<=":
		if !(aIsNum && bIsNum) {
			return false, fmt.Errorf("%s 需要数值操作数（%T 与 %T）", op, a, b)
		}
		c := cmpNumbersExact(a, b)
		switch op {
		case ">":
			return c > 0, nil
		case ">=":
			return c >= 0, nil
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		}
	}
	return false, fmt.Errorf("未知比较运算符 %s", op)
}

// evalArith 严格算术（op ∈ + - * / %）。
//
// 两操作数必须数值。整数算术要求两边都是整数且都不溢出（uint64>MaxInt64 不可精确
// 整数运算，报错）。% 仅整数；/ 两边整型→整除（向零截断），任一浮点→浮点除；
// 不做字符串拼接。除零、取模零报错。
func evalArith(op string, a, b any) (any, error) {
	ra := classifyNumber(a)
	rb := classifyNumber(b)
	if !ra.ok || !rb.ok {
		return nil, fmt.Errorf("%s 需要数值操作数（%T 与 %T）", op, a, b)
	}

	intArith := ra.isInt && rb.isInt && !ra.bigU && !rb.bigU

	switch op {
	case "%":
		if !intArith {
			return nil, fmt.Errorf("%% 需要整数操作数（%T 与 %T）", a, b)
		}
		if rb.i == 0 {
			return nil, fmt.Errorf("%% by zero")
		}
		return ra.i % rb.i, nil
	case "/":
		if intArith {
			if rb.i == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return ra.i / rb.i, nil
		}
		if rb.f == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		return ra.f / rb.f, nil
	case "*":
		if intArith {
			return ra.i * rb.i, nil
		}
		return ra.f * rb.f, nil
	case "+":
		if intArith {
			return ra.i + rb.i, nil
		}
		return ra.f + rb.f, nil
	case "-":
		if intArith {
			return ra.i - rb.i, nil
		}
		return ra.f - rb.f, nil
	}
	return nil, fmt.Errorf("未知算术运算符 %s", op)
}

// negate 对数值取负（一元负号）。
// uint/uint64 超出 int64 范围时报错（防溢出回绕成巨大正数）。
func negate(v any) (any, error) {
	r := classifyNumber(v)
	if !r.ok {
		return nil, fmt.Errorf("一元负号需要数值，实际 %T", v)
	}
	if r.bigU {
		return nil, fmt.Errorf("一元负号：uint64 超出 int64 范围")
	}
	if r.isInt {
		return -r.i, nil
	}
	return -r.f, nil
}
