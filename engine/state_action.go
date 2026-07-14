package engine

import "fmt"

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
		if def == nil || def.Pattern != PatternClearState {
			continue
		}
		if err := validateClearStateKeys(name, def.Keys); err != nil {
			return err
		}
	}
	return nil
}
