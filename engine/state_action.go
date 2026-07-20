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

// ValidateStateActions checks state-action invariants immediately after flow decoding.
func ValidateStateActions(flow *TaskFlow) error {
	if flow == nil {
		return nil
	}
	for name, def := range flow.Actions {
		if def == nil {
			continue
		}
		if def.Pattern == PatternClearState {
			if err := validateClearStateKeys(name, def.Keys); err != nil {
				return err
			}
		}
		// 校验动作字段绑定上的条件表达式语法（fail-closed）。
		for i := range def.Bindings {
			if err := validateBindingCondition(fmt.Sprintf("action %q bindings[%d]", name, i), &def.Bindings[i]); err != nil {
				return err
			}
		}
	}
	// 校验控制流节点上的条件表达式语法（loop/boolean 的 condition、loop 的 breakCondition、switch 的 cases）。
	for id, node := range flow.Nodes {
		if node == nil {
			continue
		}
		if err := validateFlowCondition(fmt.Sprintf("节点 %q condition", id), node.Condition); err != nil {
			return err
		}
		if err := validateFlowCondition(fmt.Sprintf("节点 %q breakCondition", id), node.BreakCondition); err != nil {
			return err
		}
		for i := range node.Cases {
			if err := validateFlowCondition(fmt.Sprintf("节点 %q cases[%d]", id, i), node.Cases[i].Condition); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateFlowCondition 对单个条件表达式做加载期语法校验。
// 仅校验 state: 前缀表达式（内置比较文法）；lua: 前缀引用脚本、其它前缀交由运行时处理；空条件跳过。
func validateFlowCondition(where, cond string) error {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return nil
	}
	if !strings.HasPrefix(cond, PrefixState) {
		return nil
	}
	if err := ValidateConditionSyntax(cond[len(PrefixState):]); err != nil {
		return fmt.Errorf("%s 条件表达式语法错误 %q: %w", where, cond, err)
	}
	return nil
}

// validateBindingCondition 递归校验字段绑定（含 map 类型的 entries）上的条件表达式语法。
func validateBindingCondition(where string, fb *FieldBind) error {
	if fb == nil {
		return nil
	}
	if err := validateFlowCondition(where, fb.Condition); err != nil {
		return err
	}
	for i := range fb.Entries {
		if err := validateBindingCondition(fmt.Sprintf("%s entries[%d]", where, i), &fb.Entries[i].Value); err != nil {
			return err
		}
	}
	return nil
}
