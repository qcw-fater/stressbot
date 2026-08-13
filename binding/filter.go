package binding

// FilterDef 定义对候选值的字段过滤规则。
type FilterDef struct {
	Path   string `json:"path"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
	Source string `json:"source"`
	Mode   string `json:"mode"`
}
