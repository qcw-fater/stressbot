// Package engine 提供流程引擎，驱动压测机器人的行为。
// flow.go 定义流程图结构（TaskFlow/Node/Action/Callback），
// 所有结构均可从 JSON 配置加载，无需硬编码行为逻辑。
package engine

// TaskFlow 流程图定义。
// 由一组 Node 组成的有向图，从 "main" 节点开始串行执行。
// nodes 使用 JSON object（key = 节点 ID），无需自定义反序列化。
type TaskFlow struct {
	DefaultDelayMs int                     `json:"defaultDelayMs"` // 全局节点间默认延迟（毫秒）。0=引擎默认(1000ms)，<0=禁用
	Nodes          map[string]*Node        `json:"nodes"`          // 节点映射，key 为节点 ID
	Actions        map[string]*ActionDef   `json:"actions"`        // 动作定义映射
	Callbacks      map[string]*CallbackDef `json:"callbacks"`      // 回调定义映射
}

// Node 流程节点。
// 每种 type 只使用其对应的字段，其余字段留空。
//
// 节点类型：
//   - sequence: 顺序执行 next 中列出的所有子节点
//   - action:   执行一个声明式动作或 Lua 脚本，唯一产生副作用的节点
//   - loop:     循环执行单个 body 节点，支持次数/前置条件/后置条件
//   - boolean:  对 condition 求值，跳转到 trueNext 或 falseNext
//   - weighted: 按 options 中的权重随机选择一路节点执行
//   - wait:     用户显式等待，与全局默认延迟无关
//   - break:    产生 errBreak 信号，中断最近的 loop
//   - continue: 产生 errContinue 信号，跳过本次迭代剩余步骤
type Node struct {
	Type string `json:"type"` // 节点类型

	// ── sequence 专用 ────────────────────────────────────────────
	Next []string `json:"next"` // 按顺序依次执行的子节点 ID 列表

	// ── loop 专用 ────────────────────────────────────────────────
	Body           string `json:"body"`           // 循环体节点 ID（单个）；多步骤时指向一个 sequence 节点
	LoopCount      int    `json:"loopCount"`      // 循环次数；≤0 = 无限循环
	Condition      string `json:"condition"`      // 前置条件：每次迭代开始前求值，false 时退出循环
	BreakCondition string `json:"breakCondition"` // 后置条件：每次迭代结束后求值，true 时退出循环

	// ── boolean 专用 ─────────────────────────────────────────────
	// Condition 字段同 loop，此处复用：boolean 的分支判断条件
	TrueNext  string `json:"trueNext"`  // 条件为 true 时跳转的节点 ID（空 = 不跳转）
	FalseNext string `json:"falseNext"` // 条件为 false 时跳转的节点 ID（空 = 不跳转）

	// ── action 专用 ─────────────────────────────────────────────
	Action          string      `json:"action"`          // 引用 actions 表中的动作名称
	ErrorStrategy   string      `json:"errorStrategy"`   // "abort" = 中断整个流程；"skip" = 跳过当前层级；空/"ignore" = 忽略继续
	ListenCallbacks []ListenRef `json:"listenCallbacks"` // 动作执行后注册的持久化推送监听

	// ── weighted 专用 ─────────────────────────────────────────────
	Options []WeightedOption `json:"options"` // 加权选项列表

	// ── wait 专用 ─────────────────────────────────────────────────
	WaitMs  int `json:"waitMs"`  // 固定等待时长（毫秒）；仅当 WaitMin/WaitMax 都 ≤ 0 时生效
	WaitMin int `json:"waitMin"` // 随机等待最小值（毫秒，含）；与 WaitMax 配合使用
	WaitMax int `json:"waitMax"` // 随机等待最大值（毫秒，含）；WaitMin/WaitMax 同时 > 0 时随机模式优先

	// ── 通用（action 节点有效）────────────────────────────
	DelayMs int `json:"delayMs"` // > 0: 使用此值；= 0: 使用 TaskFlow.DefaultDelayMs；< 0: 禁用延迟
}

// WeightedOption 加权选项，用于 weighted 节点。
type WeightedOption struct {
	Node   string `json:"node"`   // 目标节点 ID
	Weight int    `json:"weight"` // 权重值
}

// ListenRef 监听回调引用，定义在节点上。
// 当节点执行时（通常是连接节点），注册对应的推送监听。
// Route 为不透明路由，运行时通过 adapter.ExpectedResponseKey(route) 计算实际响应键。
type ListenRef struct {
	Route    any    `json:"route"`    // 不透明路由（与 ActionDef.Route 格式一致）
	Server   string `json:"server"`   // 连接名，格式：协议:服务名（如 "tcp:logic"、"udp:udp"）
	Callback string `json:"callback"` // 回调定义名称（引用 callbacks 表）
}

// 动作模式常量。
const (
	PatternTCPSend     = "tcpSend"
	PatternTCPRequest  = "tcpRequest"
	PatternTCPConnect  = "tcpConnect"
	PatternTCPClose    = "tcpClose"
	PatternTCPListen   = "tcpListen"
	PatternUDPSend     = "udpSend"
	PatternUDPRequest  = "udpRequest"
	PatternUDPConnect  = "udpConnect"
	PatternUDPClose    = "udpClose"
	PatternUDPListen   = "udpListen"
	PatternHTTPRequest = "httpRequest"
	PatternSetState    = "setState"
	PatternClearState  = "clearState"
	PatternLua         = "lua"
)

// ActionDef 动作定义。
// Pattern 决定执行方式（取值见 Pattern 常量）。
type ActionDef struct {
	Name        string         `json:"-"`           // 运行时回填（actions map 的 key），不参与序列化
	Pattern     string         `json:"pattern"`     // 动作模式
	Service     string         `json:"service"`     // 目标服务名
	Route       any            `json:"route"`       // 不透明路由，原样传给 adapter.EncodeTCP
	Script      string         `json:"script"`      // Lua 脚本路径（lua 模式）
	Address     string         `json:"address"`     // 连接地址（connect 模式），可用 state:key 形式
	C2SProto    string         `json:"c2sProto"`    // 客户端消息 proto 全名
	S2CProto    string         `json:"s2cProto"`    // 服务器响应 proto 全名
	Bindings    []FieldBind    `json:"bindings"`    // C2S 字段绑定
	Store       []StoreMapping `json:"store"`       // S2C 响应字段 -> 状态存储映射
	Timeout     int            `json:"timeout"`     // 超时秒数（listen 模式）
	PollMs      int            `json:"pollMs"`      // 轮询间隔毫秒（listen 模式，默认 100）
	Keys        []string       `json:"keys"`        // clearState 要清除的 key 列表
	Optional    bool           `json:"optional"`    // 可选动作：依赖缺失时静默跳过
	URL         string         `json:"url"`         // HTTP 请求 URL（httpRequest 模式），支持 state: 前缀
	Method      string         `json:"method"`      // HTTP 方法（httpRequest 模式）：POST(默认) / GET
	ContentType string         `json:"contentType"` // HTTP 内容类型（httpRequest 模式）：json(默认) / form
}

// FieldBind C2S 字段绑定定义。
// Type 决定值来源：
//   - fixed:         固定值（Value 字段）
//   - state:         从 StateStore 读取（Source 为 key）
//   - stateFirst:    从 StateStore 列表取第一个元素（Source 为 key），空列表返回 nil 触发跳过
//   - stateRandom:   从 StateStore 中的列表随机选一个（Source 为 key），支持 Filters 过滤
//   - stateRandomN:  从 StateStore 的列表随机选 N 个（Count 个）不重复
//   - stateMapKey:   从 state map 中随机选一个 key
//   - stateMapValue: 从 state map 中随机选一个 value（支持 Path 取嵌套字段）
//   - randomPick:    从 Values 列表随机选一个
//   - randomPickN:   从 Values 列表随机选 N 个（Count 个）
//   - randomPickMap: 按 KeySource 从 Values 映射表 [{key,values}] 中查找并随机选一个
//   - randomInt:     随机整数（Min/Max 字段）
//   - randomBool:    随机布尔（无参数）
//   - randomString:  随机字符串（Length + Charset 字段）
//   - randomExclude: 从 Values 或 state list 中随机选一个，且排除 ExcludeSource
//   - listSize:      返回 state 列表长度（int）
//
// Path 支持用 | 分隔多条候选路径，按顺序尝试，返回第一个非 nil 的值。
// 例如 "mailUid|gid" 会先尝试 mailUid，不存在则取 gid。
//
// Wrap 为 true 时，将单个值包装为 []any{val}，用于 repeated 字段赋单个元素的场景。
//
// Path 支持用 | 分隔多条候选路径，按顺序尝试，返回第一个非 nil 的值。
// 例如 "mailUid|gid" 会先尝试 mailUid，不存在则取 gid。
//
// Wrap 为 true 时，将单个值包装为 []any{val}，用于 repeated 字段赋单个元素的场景。
type FieldBind struct {
	Field         string      `json:"field"`         // 目标 proto 字段名（支持嵌套如 "heroList[0].heroId"）
	Type          string      `json:"type"`          // 绑定类型：fixed / state / stateFirst / stateRandom / stateRandomN / stateMapKey / stateMapValue / randomPick / randomPickMap / randomExclude / randomInt / randomFloat / randomString / listSize
	Value         any         `json:"value"`         // fixed: 固定值
	Source        string      `json:"source"`        // 数据来源 state key（state/stateFirst/stateRandom/stateRandomN/stateMapKey/stateMapValue/randomPick/randomExclude 使用）
	Path          string      `json:"path"`          // 从 state 值中导航取子字段（如 "items[0].itemId"）
	Values        []any       `json:"values"`        // randomPick/randomPickN/randomPickMap/randomExclude: 候选值列表
	Required      bool        `json:"required"`      // true = 字段缺失时动作报错（不再静默跳过）
	Filters       []FilterDef `json:"filters"`       // stateMapValue/stateMapKey: 过滤条件列表
	Min           int         `json:"min"`           // randomInt/randomFloat: 最小值（含）
	Max           int         `json:"max"`           // randomInt/randomFloat: 最大值（含）
	Precision     int         `json:"precision"`     // randomFloat: 小数位数（默认 2）
	Length        int         `json:"length"`        // randomString: 字符串长度
	Count         int         `json:"count"`         // stateRandomN/randomPickN: 选取数量
	Charset       string      `json:"charset"`       // randomString: 字符集（alpha/numeric/alphanum）
	ExcludeSource string      `json:"excludeSource"` // randomExclude: 从 state 读取排除列表
	Optional      bool        `json:"optional"`      // true = 即使 isRequired() 的类型也允许字段为空（跳过该字段）
	Wrap          bool        `json:"wrap"`          // true = 赋值给 repeated 字段时将单值包装为 [val]
	StoreAs       string      `json:"storeAs"`       // 将解析结果存入 state 的 key（中间变量，供后续 binding 通过 source 引用）
	KeySource     string      `json:"keySource"`     // randomPickMap: 从 state 读取 map 的 key 列表
	Condition     string      `json:"condition"`     // 可选条件表达式：不满足时跳过本绑定（state: 或 lua: 前缀）
}

// isRequired 判断字段绑定是否为必需（缺失时触发动作跳过或报错）。
// Required: true 时返回 true（调用方应报错）；隐式 required 类型也返回 true（调用方应跳过）。
func (fb *FieldBind) isRequired() bool {
	if fb.Optional {
		return false
	}
	return fb.Required || isImplicitRequired(fb.Type)
}

// isImplicitRequired 判断绑定类型是否属于隐式必需（缺失时触发动作跳过）。
func isImplicitRequired(btype string) bool {
	switch btype {
	case "stateFirst", "stateRandom", "stateRandomN", "stateMapKey", "stateMapValue":
		return true
	}
	return false
}

// FilterDef 列表过滤条件定义。
type FilterDef struct {
	Path   string `json:"path"`   // 字段导航路径（如 "items[].itemId"）
	Op     string `json:"op"`     // 比较运算符（==, !=, >, <, >=, <=）
	Value  any    `json:"value"`  // 比较目标值（固定值）
	Source string `json:"source"` // 比较目标值（从 state 读取的 key）
}

// StoreMapping S2C 响应字段 -> StateStore 映射。
type StoreMapping struct {
	Field  string `json:"field"`  // S2C 响应中的字段名（支持嵌套如 "heroList[0].heroId"，空字符串表示存储整个 fieldMap）
	Setter string `json:"setter"` // 写入 StateStore 的 key
}

// CallbackDef 回调定义。
type CallbackDef struct {
	S2CProto string         `json:"s2cProto"` // 解析推送消息的 proto 全名
	Store    []StoreMapping `json:"store"`    // 响应字段到 StateStore 的映射
	Script   string         `json:"script"`   // Lua 回调脚本路径
}

// GetNode 获取指定 ID 的节点
func (tf *TaskFlow) GetNode(id string) (*Node, bool) {
	n, ok := tf.Nodes[id]
	return n, ok
}

// GetAction 获取指定名称的动作定义
func (tf *TaskFlow) GetAction(name string) (*ActionDef, bool) {
	a, ok := tf.Actions[name]
	return a, ok
}

// GetCallback 获取指定名称的回调定义
func (tf *TaskFlow) GetCallback(name string) (*CallbackDef, bool) {
	c, ok := tf.Callbacks[name]
	return c, ok
}
