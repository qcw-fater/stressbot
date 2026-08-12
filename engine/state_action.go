package engine

import (
	"fmt"
	"strings"
)

var protectedStateKeys = map[string]struct{}{
	"id":      {},
	"index":   {},
	"account": {},
}

// IsProtectedStateKey reports keys injected for every robot and required for its lifecycle.
func IsProtectedStateKey(key string) bool {
	_, ok := protectedStateKeys[key]
	return ok
}

func validateClearStateKeys(actionName string, keys []string) error {
	for _, key := range keys {
		if IsProtectedStateKey(key) {
			return fmt.Errorf("action %q clearState 不允许清除内置状态 %q", actionName, key)
		}
	}
	return nil
}

// PrepareTaskFlow 在流程反序列化后检查状态动作约束，并编译所有条件表达式。
func PrepareTaskFlow(flow *TaskFlow) error {
	if flow == nil {
		return nil
	}
	compiler := newConditionCompiler()
	for name, def := range flow.Actions {
		if def == nil {
			continue
		}
		if def.Pattern == PatternClearState {
			if err := validateClearStateKeys(name, def.Keys); err != nil {
				return err
			}
		}
		for i := range def.Bindings {
			where := fmt.Sprintf("action %q bindings[%d]", name, i)
			if err := compiler.prepareBinding(where, &def.Bindings[i]); err != nil {
				return err
			}
		}
	}
	for id, node := range flow.Nodes {
		if node == nil {
			continue
		}
		if err := compiler.prepare(
			fmt.Sprintf("节点 %q condition", id), node.Condition, &node.compiledCondition,
		); err != nil {
			return err
		}
		if err := compiler.prepare(
			fmt.Sprintf("节点 %q breakCondition", id), node.BreakCondition, &node.compiledBreakCondition,
		); err != nil {
			return err
		}
		for i := range node.Cases {
			where := fmt.Sprintf("节点 %q cases[%d]", id, i)
			if err := compiler.prepare(where, node.Cases[i].Condition, &node.Cases[i].compiledCondition); err != nil {
				return err
			}
		}
	}
	return nil
}

// PrepareFieldBindings 编译不属于 TaskFlow 的字段绑定，例如 codec 心跳绑定。
func PrepareFieldBindings(bindings []FieldBind) error {
	compiler := newConditionCompiler()
	for i := range bindings {
		if err := compiler.prepareBinding(fmt.Sprintf("bindings[%d]", i), &bindings[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *conditionCompiler) prepare(where, source string, target **CompiledCondition) error {
	source = strings.TrimSpace(source)
	if source == "" {
		*target = nil
		return nil
	}
	condition, err := c.compile(source)
	if err != nil {
		return fmt.Errorf("%s 条件表达式语法错误 %q: %w", where, source, err)
	}
	*target = condition
	return nil
}

func (c *conditionCompiler) prepareBinding(where string, binding *FieldBind) error {
	if binding == nil {
		return nil
	}
	if err := c.prepare(where, binding.Condition, &binding.compiledCondition); err != nil {
		return err
	}
	for i := range binding.Entries {
		childWhere := fmt.Sprintf("%s entries[%d]", where, i)
		if err := c.prepareBinding(childWhere, &binding.Entries[i].Value); err != nil {
			return err
		}
	}
	return nil
}

func matchingCondition(source string, compiled *CompiledCondition) *CompiledCondition {
	if compiled == nil || compiled.Source() != strings.TrimSpace(source) {
		return nil
	}
	return compiled
}

func (n *Node) preparedCondition() *CompiledCondition {
	return matchingCondition(n.Condition, n.compiledCondition)
}

func (n *Node) preparedBreakCondition() *CompiledCondition {
	return matchingCondition(n.BreakCondition, n.compiledBreakCondition)
}

func (c *SwitchCase) preparedCondition() *CompiledCondition {
	return matchingCondition(c.Condition, c.compiledCondition)
}

func (b *FieldBind) preparedCondition() *CompiledCondition {
	return matchingCondition(b.Condition, b.compiledCondition)
}
