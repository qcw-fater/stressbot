package flow

import (
	"fmt"
	"strings"

	"stressbot/binding"
)

var protectedStateKeys = map[string]struct{}{
	"id":      {},
	"index":   {},
	"account": {},
}

// IsProtectedStateKey 判断该键是否为每个机器人注入、且其生命周期依赖的内置状态。
func IsProtectedStateKey(key string) bool {
	_, ok := protectedStateKeys[key]
	return ok
}

// ValidateClearStateKeys 检查 clearState 动作不会删除机器人生命周期内置状态。
func ValidateClearStateKeys(actionName string, keys []string) error {
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
			if err := ValidateClearStateKeys(name, def.Keys); err != nil {
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
		// listenRefs 指向 listens 表是跨表引用，JSON Schema 查不到（它只校验形状）。
		// 漏掉这层校验时，listen 名字写错的后果是那条监听永远不触发，而流程照跑不误——
		// 压测跑完才发现推送一条没收到。放在加载期一次性挡掉。
		for i, ref := range node.ListenRefs {
			if ref.Listen == "" {
				continue // 空 listen 表示只建缓存队列、不挂回调，是合法用法
			}
			if _, ok := flow.Listens[ref.Listen]; !ok {
				return fmt.Errorf("节点 %q listenRefs[%d] 引用的 listen %q 在 listens 中无定义", id, i, ref.Listen)
			}
		}
	}
	return nil
}

// PrepareFieldBindings 编译不属于 TaskFlow 的字段绑定，例如 codec 心跳绑定。
func PrepareFieldBindings(bindings []binding.FieldBind) error {
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

func (c *conditionCompiler) prepareBinding(where string, fieldBinding *binding.FieldBind) error {
	if fieldBinding == nil {
		return nil
	}
	source := strings.TrimSpace(fieldBinding.Condition)
	if source == "" {
		fieldBinding.SetPreparedCondition(nil)
	} else {
		condition, err := c.compile(source)
		if err != nil {
			return fmt.Errorf("%s 条件表达式语法错误 %q: %w", where, source, err)
		}
		fieldBinding.SetPreparedCondition(condition)
	}
	for i := range fieldBinding.Entries {
		childWhere := fmt.Sprintf("%s entries[%d]", where, i)
		if err := c.prepareBinding(childWhere, &fieldBinding.Entries[i].Value); err != nil {
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

// PreparedCondition 返回与当前条件文本匹配的预编译条件。
func (n *Node) PreparedCondition() *CompiledCondition {
	return matchingCondition(n.Condition, n.compiledCondition)
}

// PreparedBreakCondition 返回与当前循环退出条件匹配的预编译条件。
func (n *Node) PreparedBreakCondition() *CompiledCondition {
	return matchingCondition(n.BreakCondition, n.compiledBreakCondition)
}

// PreparedCondition 返回与当前 switch 分支文本匹配的预编译条件。
func (c *SwitchCase) PreparedCondition() *CompiledCondition {
	return matchingCondition(c.Condition, c.compiledCondition)
}
