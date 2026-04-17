package state

// Accessor 定义状态字段的类型化访问器。
// 注册后可供声明式 action 的 binding 系统通过名称引用 Robot 状态。
// 例如 binding: {"type": "state", "source": "account"} 会调用 Get 方法。
type Accessor struct {
	Get func(s *Store) any
	Set func(s *Store, val any)
}

// Registry 状态访问器注册表
type Registry struct {
	accessors map[string]Accessor
}

// NewRegistry 创建新的访问器注册表
func NewRegistry() *Registry {
	return &Registry{accessors: make(map[string]Accessor)}
}

// Register 注册一个状态访问器
func (r *Registry) Register(name string, get func(s *Store) any, set func(s *Store, val any)) {
	r.accessors[name] = Accessor{Get: get, Set: set}
}

// GetState 通过注册的 accessor 获取状态值
func (r *Registry) GetState(name string, s *Store) (any, bool) {
	if acc, ok := r.accessors[name]; ok {
		return acc.Get(s), true
	}
	// 未注册的 accessor，直接从 store 取
	if v := s.Get(name); v != nil {
		return v, true
	}
	return nil, false
}

// SetState 通过注册的 accessor 设置状态值
func (r *Registry) SetState(name string, s *Store, val any) bool {
	if acc, ok := r.accessors[name]; ok && acc.Set != nil {
		acc.Set(s, val)
		return true
	}
	// 未注册的 accessor，直接写入 store
	s.Set(name, val)
	return true
}

// Has 检查是否存在指定名称的访问器
func (r *Registry) Has(name string) bool {
	_, ok := r.accessors[name]
	return ok
}

// List 列出所有已注册的访问器名称
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.accessors))
	for name := range r.accessors {
		names = append(names, name)
	}
	return names
}
