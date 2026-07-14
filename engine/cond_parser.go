// Package engine 提供流程执行引擎。
// cond_parser.go 是条件表达式的递归下降解释器（严格类型，无隐式转换）。
//
// 文法（优先级低→高）：
//
//	expr       → or
//	or         → and ("||" and)*
//	and        → unary ("&&" unary)*
//	unary      → "!" unary | comparison
//	comparison → arith (comp_op arith)?
//	arith      → term (("+"|"-") term)*
//	term       → factor (("*"|"/"|"%") factor)*
//	factor     → NUMBER | STRING | PATH | "(" expr ")" | "-" factor
//	comp_op    → == | != | > | >= | < | <=
//
// 字面量只有数字和带引号字符串；裸标识符恒为 state 路径。无 true/false/nil 关键字。
//
// 求值采用 inline 解释（不建 AST）：EvalCondition 每次调用 store 都变化，缓存无收益。
// 短路：&& 在左操作数 effective-false 时跳过右侧求值，|| 在 effective-true 时跳过——
// 通过 skip 标志在解析结构的同时跳过求值（右侧 token 仍被正确消费）。
package engine

import (
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"stressbot/state"
	stresslog "stressbot/utils/log"
)

// parseExpr 解析并求值条件表达式（已剥离 state: 前缀），返回布尔结果。
// 任何类型不匹配、key 缺失、除零、多余 token 等错误 → 记录一条 warn。
//
// 错误语义（local-false）：出错的子表达式视为 effective-false 并记录 firstErr；
// 短路照常进行；顶层若 firstErr 非空打一条 warn，但返回的是实际计算出的布尔结果
// （如 missing || fallback，fallback 为真时结果为 true，同时 warn 一次）。
// 仅当结果不是 bool（如裸数值顶层、或结构错误）时才返回 false。
func parseExpr(input string, s *state.Store) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return true
	}

	toks, err := tokenize(input)
	if err != nil {
		stresslog.Warn("[ENGINE] 条件表达式词法错误",
			zap.String("expr", input), zap.Error(err))
		return false
	}

	p := &parser{toks: toks, store: s}
	val, _ := p.parseOr()

	// 末尾必须消费到 EOF，否则结构错误
	if !p.atEnd() {
		t := p.peek()
		stresslog.Warn("[ENGINE] 条件表达式存在多余 token",
			zap.String("expr", input), zap.String("token", t.lit), zap.Int("pos", t.pos))
		return false
	}

	if p.firstErr != nil {
		stresslog.Warn("[ENGINE] 条件表达式求值失败",
			zap.String("expr", input), zap.Error(p.firstErr))
	}

	// 返回实际计算出的布尔结果（若已是 bool）。吸收过错误的子表达式会产出合法 bool，
	// 此时仍返回它；只有结果本身不是 bool（裸数值/字符串顶层、或错误致 val 为 nil）才 false。
	if b, ok := val.(bool); ok {
		return b
	}
	if p.firstErr == nil {
		stresslog.Warn("[ENGINE] 条件表达式结果非布尔",
			zap.String("expr", input), zap.String("type", fmt.Sprintf("%T", val)))
	}
	return false
}

// parser 递归下降解释器。pos 始终指向当前 token（末尾保证指向 EOF 哨兵）。
type parser struct {
	toks     []token
	pos      int
	store    *state.Store
	firstErr error // 首个错误，只记一次
	skip     bool  // 短路跳过：仅消费 token、不求值/不报错
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) atEnd() bool { return p.toks[p.pos].kind == tokEOF }

// recordErr 记录首个错误（只记一次）。
func (p *parser) recordErr(err error) {
	if p.firstErr == nil && err != nil {
		p.firstErr = err
	}
}

// setErrorf 记录首个错误（格式化）。
func (p *parser) setErrorf(format string, args ...any) {
	if p.firstErr == nil {
		p.firstErr = fmt.Errorf(format, args...)
	}
}

// effectiveBool 把 (value, err) 转成 effective 布尔用于布尔运算。
// 任何错误（传入 err 或值非 bool）→ 记录 firstErr 并返回 false。
// 调用方需保证不在 skip 模式下调用。
func (p *parser) effectiveBool(v any, err error) bool {
	if err != nil {
		p.recordErr(err)
		return false
	}
	b, e := asBool(v)
	if e != nil {
		p.recordErr(e)
		return false
	}
	return b
}

// parseOr 解析 or。有 || 时各操作数 asBool、在 true 时短路；单操作数透传原值。
func (p *parser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if !(p.peek().kind == tokOp && p.peek().lit == "||") {
		return left, err // 单操作数透传，不 boolify
	}
	if p.skip {
		// 外层已在短路：消费所有 || 操作数但不求值
		for p.peek().kind == tokOp && p.peek().lit == "||" {
			p.next()
			_, _ = p.parseAnd()
		}
		return nil, nil
	}
	b := p.effectiveBool(left, err)
	for p.peek().kind == tokOp && p.peek().lit == "||" {
		p.next() // 消费 ||
		if b {
			p.skip = true // 短路：跳过右侧求值
		}
		r, rerr := p.parseAnd()
		p.skip = false
		if !b {
			b = p.effectiveBool(r, rerr) // b 已为 false：b = false || r
		}
	}
	return b, nil
}

// parseAnd 解析 and。有 && 时各操作数 asBool、在 false 时短路；单操作数透传原值。
func (p *parser) parseAnd() (any, error) {
	left, err := p.parseUnary()
	if !(p.peek().kind == tokOp && p.peek().lit == "&&") {
		return left, err // 单操作数透传，不 boolify
	}
	if p.skip {
		for p.peek().kind == tokOp && p.peek().lit == "&&" {
			p.next()
			_, _ = p.parseUnary()
		}
		return nil, nil
	}
	b := p.effectiveBool(left, err)
	for p.peek().kind == tokOp && p.peek().lit == "&&" {
		p.next() // 消费 &&
		if !b {
			p.skip = true // 短路：跳过右侧求值
		}
		r, rerr := p.parseUnary()
		p.skip = false
		if b {
			b = p.effectiveBool(r, rerr) // b 已为 true：b = true && r
		}
	}
	return b, nil
}

// parseUnary 处理 ! 前缀取反（要求操作数为 bool）。
func (p *parser) parseUnary() (any, error) {
	if p.peek().kind == tokOp && p.peek().lit == "!" {
		p.next() // 消费 !
		v, err := p.parseUnary()
		if p.skip {
			return nil, nil
		}
		return !p.effectiveBool(v, err), nil
	}
	return p.parseComparison()
}

// parseComparison 解析比较：arith (comp_op arith)?。有 comp_op 返回 bool，否则透传 arith 值。
func (p *parser) parseComparison() (any, error) {
	left, err := p.parseArith()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tokOp && isCompOp(t.lit) {
		op := t.lit
		p.next() // 消费 comp_op
		right, rerr := p.parseArith()
		if rerr != nil {
			return nil, rerr
		}
		if p.skip {
			return nil, nil
		}
		eq, e := strictCompare(left, right, op)
		if e != nil {
			p.setErrorf("%v", e)
			return nil, p.firstErr
		}
		return eq, nil
	}
	return left, nil
}

// parseArith 解析加减（优先级低于乘除模）。
func (p *parser) parseArith() (any, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tokOp || (t.lit != "+" && t.lit != "-") {
			break
		}
		op := t.lit
		p.next()
		right, rerr := p.parseTerm()
		if rerr != nil {
			return nil, rerr
		}
		if !p.skip {
			v, e := evalArith(op, left, right)
			if e != nil {
				p.setErrorf("%v", e)
				return nil, p.firstErr
			}
			left = v
		}
	}
	return left, nil
}

// parseTerm 解析乘除模。
func (p *parser) parseTerm() (any, error) {
	left, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tokOp || (t.lit != "*" && t.lit != "/" && t.lit != "%") {
			break
		}
		op := t.lit
		p.next()
		right, rerr := p.parseFactor()
		if rerr != nil {
			return nil, rerr
		}
		if !p.skip {
			v, e := evalArith(op, left, right)
			if e != nil {
				p.setErrorf("%v", e)
				return nil, p.firstErr
			}
			left = v
		}
	}
	return left, nil
}

// parseFactor 解析最高优先级的因子。
func (p *parser) parseFactor() (any, error) {
	t := p.peek()
	switch {
	case t.kind == tokNumber:
		p.next()
		if p.skip {
			return nil, nil
		}
		return parseNumberLit(t.lit, p)

	case t.kind == tokString:
		p.next()
		if p.skip {
			return nil, nil
		}
		return t.lit, nil

	case t.kind == tokPath:
		p.next()
		if p.skip {
			return nil, nil
		}
		v := p.store.GetPath(t.lit)
		if v == nil {
			p.setErrorf("state 路径 %q 不存在", t.lit)
			return nil, p.firstErr
		}
		return v, nil

	case t.kind == tokLParen:
		p.next() // 消费 (
		v, err := p.parseOr()
		if t2 := p.peek(); t2.kind != tokRParen {
			p.setErrorf("缺少右括号 )（位置 %d）", t2.pos)
			return nil, p.firstErr
		}
		p.next() // 消费 )
		return v, err

	case t.kind == tokOp && t.lit == "-":
		p.next() // 消费 -
		f, ferr := p.parseFactor()
		if p.skip {
			return nil, nil
		}
		if ferr != nil {
			return nil, ferr
		}
		v, e := negate(f)
		if e != nil {
			p.setErrorf("%v", e)
			return nil, p.firstErr
		}
		return v, nil
	}

	p.setErrorf("意外的 token %q（位置 %d）", t.lit, t.pos)
	// 推进一格避免死循环
	p.next()
	return nil, p.firstErr
}

// parseNumberLit 解析数字字面量：无小数点必须装入 int64（溢出报错，不退化为 float）；
// 有小数点为 float64。
func parseNumberLit(lit string, p *parser) (any, error) {
	if strings.Contains(lit, ".") {
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			p.setErrorf("无效数字字面量 %q", lit)
			return nil, p.firstErr
		}
		return f, nil
	}
	i, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		p.setErrorf("整数字面量溢出 %q", lit)
		return nil, p.firstErr
	}
	return i, nil
}

// isCompOp 判断是否为比较运算符。
func isCompOp(s string) bool {
	switch s {
	case "==", "!=", ">", ">=", "<", "<=":
		return true
	}
	return false
}
