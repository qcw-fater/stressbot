package flow

import (
	"fmt"
	"strconv"
	"strings"
)

type conditionKind uint8

const (
	conditionState conditionKind = iota + 1
	conditionLua
	conditionUnsupported
)

type conditionNodeKind uint8

const (
	conditionNodeLiteral conditionNodeKind = iota + 1
	conditionNodePath
	conditionNodeUnary
	conditionNodeBinary
	conditionNodeRuntimeError
)

type conditionOperator uint8

const (
	conditionOpNone conditionOperator = iota
	conditionOpOr
	conditionOpAnd
	conditionOpNot
	conditionOpEqual
	conditionOpNotEqual
	conditionOpGreater
	conditionOpGreaterEqual
	conditionOpLess
	conditionOpLessEqual
	conditionOpAdd
	conditionOpSubtract
	conditionOpMultiply
	conditionOpDivide
	conditionOpModulo
	conditionOpNegate
)

type conditionNodeIndex int32

const noConditionNode conditionNodeIndex = -1

type conditionNode struct {
	kind    conditionNodeKind
	op      conditionOperator
	left    conditionNodeIndex
	right   conditionNodeIndex
	value   any
	text    string
	evalErr error
}

type conditionProgram struct {
	root  conditionNodeIndex
	nodes []conditionNode
}

// CompiledCondition 是加载期生成、运行期只读的条件程序。
// 它不持有 Robot 或 state.Store，同一实例可以由多个 Robot 并发共享。
type CompiledCondition struct {
	source  string
	kind    conditionKind
	script  string
	program *conditionProgram
}

type conditionCompiler struct {
	conditions map[string]*CompiledCondition
}

func newConditionCompiler() *conditionCompiler {
	return &conditionCompiler{conditions: make(map[string]*CompiledCondition)}
}

func (c *conditionCompiler) compile(source string) (*CompiledCondition, error) {
	source = strings.TrimSpace(source)
	if condition := c.conditions[source]; condition != nil {
		return condition, nil
	}
	condition, err := compileCondition(source)
	if err != nil {
		return nil, err
	}
	c.conditions[source] = condition
	return condition, nil
}

// Source 返回去除首尾空白后的原始条件文本。
func (c *CompiledCondition) Source() string {
	if c == nil {
		return ""
	}
	return c.source
}

// LuaScript 返回 Lua 条件引用。第二个返回值仅在条件使用 lua: 前缀时为 true。
func (c *CompiledCondition) LuaScript() (string, bool) {
	if c == nil || c.kind != conditionLua {
		return "", false
	}
	return c.script, true
}

// compileCondition 把完整条件文本编译为不可变程序。
// state 值和运行期类型不在这里读取或固定。
func compileCondition(source string) (*CompiledCondition, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return &CompiledCondition{
			source: source,
			kind:   conditionState,
			program: &conditionProgram{
				root:  0,
				nodes: []conditionNode{{kind: conditionNodeLiteral, value: true}},
			},
		}, nil
	}
	if strings.HasPrefix(source, PrefixLua) {
		return &CompiledCondition{
			source: source,
			kind:   conditionLua,
			script: source[len(PrefixLua):],
		}, nil
	}
	if !strings.HasPrefix(source, PrefixState) {
		return &CompiledCondition{source: source, kind: conditionUnsupported}, nil
	}

	input := strings.TrimSpace(source[len(PrefixState):])
	if input == "" {
		return &CompiledCondition{
			source: source,
			kind:   conditionState,
			program: &conditionProgram{
				root:  0,
				nodes: []conditionNode{{kind: conditionNodeLiteral, value: true}},
			},
		}, nil
	}

	tokens, err := tokenize(input)
	if err != nil {
		return nil, fmt.Errorf("词法错误: %w", err)
	}
	compiler := conditionParser{tokens: tokens}
	root, err := compiler.parseOr()
	if err != nil {
		return nil, err
	}
	if !compiler.atEnd() {
		token := compiler.peek()
		return nil, fmt.Errorf("存在多余 token %q（位置 %d）", token.lit, token.pos)
	}
	return &CompiledCondition{
		source: source,
		kind:   conditionState,
		program: &conditionProgram{
			root:  root,
			nodes: compiler.nodes,
		},
	}, nil
}

type conditionParser struct {
	tokens []token
	pos    int
	nodes  []conditionNode
}

func (p *conditionParser) peek() token {
	return p.tokens[p.pos]
}

func (p *conditionParser) next() token {
	token := p.tokens[p.pos]
	if token.kind != tokEOF {
		p.pos++
	}
	return token
}

func (p *conditionParser) atEnd() bool {
	return p.peek().kind == tokEOF
}

func (p *conditionParser) addNode(node conditionNode) conditionNodeIndex {
	p.nodes = append(p.nodes, node)
	return conditionNodeIndex(len(p.nodes) - 1)
}

func (p *conditionParser) addUnary(op conditionOperator, child conditionNodeIndex) conditionNodeIndex {
	return p.addNode(conditionNode{
		kind:  conditionNodeUnary,
		op:    op,
		left:  child,
		right: noConditionNode,
	})
}

func (p *conditionParser) addBinary(op conditionOperator, left, right conditionNodeIndex) conditionNodeIndex {
	return p.addNode(conditionNode{
		kind:  conditionNodeBinary,
		op:    op,
		left:  left,
		right: right,
	})
}

func (p *conditionParser) parseOr() (conditionNodeIndex, error) {
	left, err := p.parseAnd()
	if err != nil {
		return noConditionNode, err
	}
	for p.peek().kind == tokOp && p.peek().lit == "||" {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return noConditionNode, err
		}
		left = p.addBinary(conditionOpOr, left, right)
	}
	return left, nil
}

func (p *conditionParser) parseAnd() (conditionNodeIndex, error) {
	left, err := p.parseUnary()
	if err != nil {
		return noConditionNode, err
	}
	for p.peek().kind == tokOp && p.peek().lit == "&&" {
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return noConditionNode, err
		}
		left = p.addBinary(conditionOpAnd, left, right)
	}
	return left, nil
}

func (p *conditionParser) parseUnary() (conditionNodeIndex, error) {
	if p.peek().kind == tokOp && p.peek().lit == "!" {
		p.next()
		child, err := p.parseUnary()
		if err != nil {
			return noConditionNode, err
		}
		return p.addUnary(conditionOpNot, child), nil
	}
	return p.parseComparison()
}

func (p *conditionParser) parseComparison() (conditionNodeIndex, error) {
	left, err := p.parseArithmetic()
	if err != nil {
		return noConditionNode, err
	}
	token := p.peek()
	if token.kind != tokOp {
		return left, nil
	}
	op, ok := comparisonOperator(token.lit)
	if !ok {
		return left, nil
	}
	p.next()
	right, err := p.parseArithmetic()
	if err != nil {
		return noConditionNode, err
	}
	return p.addBinary(op, left, right), nil
}

func (p *conditionParser) parseArithmetic() (conditionNodeIndex, error) {
	left, err := p.parseTerm()
	if err != nil {
		return noConditionNode, err
	}
	for {
		token := p.peek()
		if token.kind != tokOp || (token.lit != "+" && token.lit != "-") {
			return left, nil
		}
		p.next()
		right, err := p.parseTerm()
		if err != nil {
			return noConditionNode, err
		}
		op := conditionOpAdd
		if token.lit == "-" {
			op = conditionOpSubtract
		}
		left = p.addBinary(op, left, right)
	}
}

func (p *conditionParser) parseTerm() (conditionNodeIndex, error) {
	left, err := p.parseFactor()
	if err != nil {
		return noConditionNode, err
	}
	for {
		token := p.peek()
		if token.kind != tokOp || (token.lit != "*" && token.lit != "/" && token.lit != "%") {
			return left, nil
		}
		p.next()
		right, err := p.parseFactor()
		if err != nil {
			return noConditionNode, err
		}
		op := conditionOpMultiply
		switch token.lit {
		case "/":
			op = conditionOpDivide
		case "%":
			op = conditionOpModulo
		}
		left = p.addBinary(op, left, right)
	}
}

func (p *conditionParser) parseFactor() (conditionNodeIndex, error) {
	token := p.peek()
	switch {
	case token.kind == tokNumber:
		p.next()
		value, err := parseConditionNumber(token.lit)
		if err != nil {
			return p.addNode(conditionNode{
				kind:    conditionNodeRuntimeError,
				evalErr: err,
			}), nil
		}
		return p.addNode(conditionNode{kind: conditionNodeLiteral, value: value}), nil
	case token.kind == tokString:
		p.next()
		return p.addNode(conditionNode{kind: conditionNodeLiteral, value: token.lit}), nil
	case token.kind == tokPath:
		p.next()
		return p.addNode(conditionNode{kind: conditionNodePath, text: token.lit}), nil
	case token.kind == tokLParen:
		p.next()
		node, err := p.parseOr()
		if err != nil {
			return noConditionNode, err
		}
		closing := p.peek()
		if closing.kind != tokRParen {
			return noConditionNode, fmt.Errorf("缺少右括号 )（位置 %d）", closing.pos)
		}
		p.next()
		return node, nil
	case token.kind == tokOp && token.lit == "-":
		p.next()
		child, err := p.parseFactor()
		if err != nil {
			return noConditionNode, err
		}
		return p.addUnary(conditionOpNegate, child), nil
	default:
		return noConditionNode, fmt.Errorf("意外的 token %q（位置 %d）", token.lit, token.pos)
	}
}

func parseConditionNumber(literal string) (any, error) {
	if strings.Contains(literal, ".") {
		value, err := strconv.ParseFloat(literal, 64)
		if err != nil {
			return nil, fmt.Errorf("无效数字字面量 %q", literal)
		}
		return value, nil
	}
	value, err := strconv.ParseInt(literal, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("整数字面量溢出 %q", literal)
	}
	return value, nil
}

func comparisonOperator(operator string) (conditionOperator, bool) {
	switch operator {
	case "==":
		return conditionOpEqual, true
	case "!=":
		return conditionOpNotEqual, true
	case ">":
		return conditionOpGreater, true
	case ">=":
		return conditionOpGreaterEqual, true
	case "<":
		return conditionOpLess, true
	case "<=":
		return conditionOpLessEqual, true
	default:
		return conditionOpNone, false
	}
}

func (op conditionOperator) text() string {
	switch op {
	case conditionOpEqual:
		return "=="
	case conditionOpNotEqual:
		return "!="
	case conditionOpGreater:
		return ">"
	case conditionOpGreaterEqual:
		return ">="
	case conditionOpLess:
		return "<"
	case conditionOpLessEqual:
		return "<="
	case conditionOpAdd:
		return "+"
	case conditionOpSubtract:
		return "-"
	case conditionOpMultiply:
		return "*"
	case conditionOpDivide:
		return "/"
	case conditionOpModulo:
		return "%"
	default:
		return ""
	}
}
