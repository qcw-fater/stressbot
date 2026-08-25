package flow

import (
	"errors"
	"fmt"

	"go.uber.org/zap"

	"stressbot/internal/stresslog"
	"stressbot/state"
)

type conditionEvalContext struct {
	store    *state.Store
	firstErr error
}

func (c *conditionEvalContext) recordError(err error) {
	if c.firstErr == nil && err != nil {
		c.firstErr = err
	}
}

func (c *conditionEvalContext) effectiveBool(value any, err error) bool {
	if err != nil {
		c.recordError(err)
		return false
	}
	result, err := asBool(value)
	if err != nil {
		c.recordError(err)
		return false
	}
	return result
}

// EvalState 使用本次传入的 Store 执行已编译 state 条件。
func (c *CompiledCondition) EvalState(store *state.Store) bool {
	if c == nil {
		if stresslog.GetLogger() != nil {
			stresslog.Error("[ENGINE] 条件表达式尚未编译")
		}
		return false
	}
	if c.kind != conditionState || c.program == nil {
		if stresslog.GetLogger() != nil {
			stresslog.Warn("[ENGINE] 条件表达式格式错误，仅支持 state: 前缀",
				zap.String("expr", c.source))
		}
		return false
	}
	return c.program.eval(c.source, store)
}

func (p *conditionProgram) eval(source string, store *state.Store) bool {
	context := conditionEvalContext{store: store}
	value, err := p.evalNode(p.root, &context)
	context.recordError(err)
	if context.firstErr != nil && stresslog.GetLogger() != nil {
		stresslog.Warn("[ENGINE] 条件表达式求值失败",
			zap.String("expr", source), zap.Error(context.firstErr))
	}
	if result, ok := value.(bool); ok {
		return result
	}
	if context.firstErr == nil && stresslog.GetLogger() != nil {
		stresslog.Warn("[ENGINE] 条件表达式结果非布尔",
			zap.String("expr", source), zap.String("type", fmt.Sprintf("%T", value)))
	}
	return false
}

func (p *conditionProgram) evalNode(index conditionNodeIndex, context *conditionEvalContext) (any, error) {
	if index < 0 || int(index) >= len(p.nodes) {
		err := fmt.Errorf("条件 AST 节点索引越界: %d", index)
		context.recordError(err)
		return nil, err
	}
	node := &p.nodes[index]
	switch node.kind {
	case conditionNodeLiteral:
		return node.value, nil
	case conditionNodePath:
		if context.store == nil {
			err := errors.New("state store 为空")
			context.recordError(err)
			return nil, err
		}
		value := context.store.GetPath(node.text)
		if value == nil {
			err := fmt.Errorf("state 路径 %q 不存在", node.text)
			context.recordError(err)
			return nil, err
		}
		return value, nil
	case conditionNodeRuntimeError:
		context.recordError(node.evalErr)
		return nil, node.evalErr
	case conditionNodeUnary:
		return p.evalUnary(node, context)
	case conditionNodeBinary:
		return p.evalBinary(node, context)
	default:
		err := fmt.Errorf("未知条件 AST 节点类型: %d", node.kind)
		context.recordError(err)
		return nil, err
	}
}

func (p *conditionProgram) evalUnary(node *conditionNode, context *conditionEvalContext) (any, error) {
	value, err := p.evalNode(node.left, context)
	switch node.op {
	case conditionOpNot:
		return !context.effectiveBool(value, err), nil
	case conditionOpNegate:
		if err != nil {
			return nil, err
		}
		result, err := negate(value)
		context.recordError(err)
		return result, err
	default:
		err := fmt.Errorf("未知一元运算符: %d", node.op)
		context.recordError(err)
		return nil, err
	}
}

func (p *conditionProgram) evalBinary(node *conditionNode, context *conditionEvalContext) (any, error) {
	left, err := p.evalNode(node.left, context)
	switch node.op {
	case conditionOpOr:
		leftBool := context.effectiveBool(left, err)
		if leftBool {
			return true, nil
		}
		right, rightErr := p.evalNode(node.right, context)
		return context.effectiveBool(right, rightErr), nil
	case conditionOpAnd:
		leftBool := context.effectiveBool(left, err)
		if !leftBool {
			return false, nil
		}
		right, rightErr := p.evalNode(node.right, context)
		return context.effectiveBool(right, rightErr), nil
	}
	if err != nil {
		return nil, err
	}
	right, err := p.evalNode(node.right, context)
	if err != nil {
		return nil, err
	}

	var result any
	switch node.op {
	case conditionOpEqual, conditionOpNotEqual, conditionOpGreater,
		conditionOpGreaterEqual, conditionOpLess, conditionOpLessEqual:
		result, err = strictCompare(left, right, node.op.text())
	case conditionOpAdd, conditionOpSubtract, conditionOpMultiply,
		conditionOpDivide, conditionOpModulo:
		result, err = evalArith(node.op.text(), left, right)
	default:
		err = fmt.Errorf("未知二元运算符: %d", node.op)
	}
	context.recordError(err)
	return result, err
}
