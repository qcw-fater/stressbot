package binding

import (
	"strings"

	"stressbot/state"
)

// PreparedCondition 是加载期编译后的只读字段条件。
// 具体编译产物由 flow 提供，binding 只保存和消费最小求值契约。
type PreparedCondition interface {
	Source() string
	EvalState(store *state.Store) bool
}

// MapEntryBind 定义 proto map 字段中的单个 entry。
type MapEntryBind struct {
	Key   any       `json:"key"`
	Value FieldBind `json:"value"`
}

// FieldBind 定义 C2S proto 字段的值来源和转换参数。
type FieldBind struct {
	Field         string         `json:"field"`
	Type          string         `json:"type"`
	Value         any            `json:"value"`
	Source        string         `json:"source"`
	Path          string         `json:"path"`
	Values        []any          `json:"values"`
	Entries       []MapEntryBind `json:"entries"`
	Required      bool           `json:"required"`
	Filters       []FilterDef    `json:"filters"`
	Min           int            `json:"min"`
	Max           int            `json:"max"`
	Precision     int            `json:"precision"`
	Length        int            `json:"length"`
	Count         int            `json:"count"`
	Charset       string         `json:"charset"`
	ExcludeSource string         `json:"excludeSource"`
	Optional      bool           `json:"optional"`
	Wrap          bool           `json:"wrap"`
	StoreAs       string         `json:"storeAs"`
	KeySource     string         `json:"keySource"`
	Condition     string         `json:"condition"`

	preparedCondition PreparedCondition
}

// IsRequired 判断字段缺失是否需要报错或跳过当前动作。
func (b *FieldBind) IsRequired() bool {
	if b.Optional {
		return false
	}
	return b.Required || IsImplicitRequired(b.Type)
}

// SetPreparedCondition 保存由 flow 在加载期编译的不可变条件。
func (b *FieldBind) SetPreparedCondition(condition PreparedCondition) {
	b.preparedCondition = condition
}

// PreparedCondition 返回与当前条件文本匹配的预编译条件。
func (b *FieldBind) PreparedCondition() PreparedCondition {
	if b.preparedCondition == nil || b.preparedCondition.Source() != strings.TrimSpace(b.Condition) {
		return nil
	}
	return b.preparedCondition
}
