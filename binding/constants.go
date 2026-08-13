// Package binding 定义流程动作中的字段取值、过滤和响应存储模型。
package binding

// 字段绑定类型常量。
const (
	BindFixed         = "fixed"
	BindState         = "state"
	BindStateFirst    = "stateFirst"
	BindStateRandom   = "stateRandom"
	BindStateRandomN  = "stateRandomN"
	BindStateMapKey   = "stateMapKey"
	BindStateMapValue = "stateMapValue"
	BindRandomPick    = "randomPick"
	BindRandomPickN   = "randomPickN"
	BindRandomPickMap = "randomPickMap"
	BindRandomExclude = "randomExclude"
	BindRandomInt     = "randomInt"
	BindRandomFloat   = "randomFloat"
	BindRandomBool    = "randomBool"
	BindRandomString  = "randomString"
	BindListSize      = "listSize"
	BindMap           = "map"
)

// FilterMode 过滤聚合模式常量。
const (
	FilterModeAny  = "any"
	FilterModeAll  = "all"
	FilterModeNone = "none"
)

// IsImplicitRequired 判断绑定类型是否在缺失时必须中止当前动作。
func IsImplicitRequired(bindingType string) bool {
	switch bindingType {
	case BindState, BindStateFirst, BindStateRandom, BindStateRandomN, BindStateMapKey, BindStateMapValue:
		return true
	default:
		return false
	}
}
