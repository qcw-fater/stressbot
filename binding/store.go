package binding

// StoreMapping 定义 S2C 响应字段到机器人状态的映射。
type StoreMapping struct {
	Field  string `json:"field"`
	Setter string `json:"setter"`
}
