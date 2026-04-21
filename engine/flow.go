// Package engine 提供流程引擎，驱动压测机器人的行为。
// flow.go 定义流程图结构（TaskFlow/Node/Action/Callback），
// 所有结构均可从 JSON 配置加载，无需硬编码行为逻辑。
package engine

import (
	"encoding/json"
)

// TaskFlow 流程图定义。
// 由一组 Node 组成的 DAG（有向无环图），从 startNode 开始执行。
type TaskFlow struct {
	StartNode string                  `json:"startNode"` // 起始节点 ID
	Nodes     map[string]*Node        `json:"nodes"`     // 节点映射（ID -> Node）
	Actions   map[string]*ActionDef   `json:"actions"`   // 动作定义映射（名称 -> 定义）
	Callbacks map[string]*CallbackDef `json:"callbacks"` // 回调定义映射（名称 -> 定义）
}

// Node 流程节点。
// 每种 type 定义不同的执行语义：
//   - start:    起始节点，串行执行所有 next 子节点（兼容旧 Robot 行为）
//   - sequence: 顺序节点，按 next 数组依次执行
//   - action:   动作节点，执行指定 action（声明式或 Lua）
//   - loop:     循环节点，循环执行 next 中的节点
//   - boolean:  条件分支节点，根据条件选择执行路径
//   - weighted: 加权随机节点，按权重随机选择执行路径
//   - wait:     等待节点，暂停指定时间
//
// 为了兼容旧 Robot 工具的 flow.json 结构，同时支持新旧两套字段别名：
//   - 旧: trueBranch/falseBranch/action(+boolean)、options(+weighted)、value(+wait)、listen
//   - 新: trueNext/falseNext/condition、next(+weighted)、waitSeconds、listenCallbacks
type Node struct {
	ID              string      `json:"id"`              // 节点唯一标识
	Type            string      `json:"type"`            // 节点类型
	Next            []NextNode  `json:"next"`            // 下游节点列表
	Action          string      `json:"action"`          // 动作名称（action/boolean 类型）
	BreakOff        bool        `json:"breakOff"`        // 是否中断流程（错误即停）
	LoopCount       int         `json:"loopCount"`       // 循环次数（loop 类型），-1 为无限
	Condition       string      `json:"condition"`       // 条件表达式（boolean 类型，新式）
	TrueNext        string      `json:"trueNext"`        // 条件为真时跳转的节点（新式）
	FalseNext       string      `json:"falseNext"`       // 条件为假时跳转的节点（新式）
	WaitSeconds     float64     `json:"waitSeconds"`     // 等待秒数（wait 类型，新式）
	ListenCallbacks []ListenRef `json:"listenCallbacks"` // 在此节点注册的监听回调（新式）
	DelayMs         int         `json:"delayMs"`         // 动作后延迟（毫秒）
}

// nodeRaw 用于自定义反序列化 Node 的辅助结构，同时捕获新旧字段名。
type nodeRaw struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	Next            []NextNode  `json:"next"`
	Action          string      `json:"action"`
	BreakOff        bool        `json:"breakOff"`
	LoopCount       int         `json:"loopCount"`
	Condition       string      `json:"condition"`
	TrueNext        string      `json:"trueNext"`
	FalseNext       string      `json:"falseNext"`
	TrueBranch      string      `json:"trueBranch"`  // 旧式别名
	FalseBranch     string      `json:"falseBranch"` // 旧式别名
	WaitSeconds     float64     `json:"waitSeconds"`
	Value           float64     `json:"value"`   // 旧式 wait 秒数
	Options         []NextNode  `json:"options"` // 旧式 weighted 子节点
	Listen          []ListenRef `json:"listen"`  // 旧式监听
	ListenCallbacks []ListenRef `json:"listenCallbacks"`
	DelayMs         int         `json:"delayMs"`
}

// UnmarshalJSON 解析 Node，兼容新旧两套字段名。
func (n *Node) UnmarshalJSON(data []byte) error {
	var raw nodeRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	n.ID = raw.ID
	n.Type = raw.Type
	n.Next = raw.Next
	n.Action = raw.Action
	n.BreakOff = raw.BreakOff
	n.LoopCount = raw.LoopCount
	n.Condition = raw.Condition
	n.TrueNext = firstNonEmpty(raw.TrueNext, raw.TrueBranch)
	n.FalseNext = firstNonEmpty(raw.FalseNext, raw.FalseBranch)
	n.WaitSeconds = raw.WaitSeconds
	if n.WaitSeconds == 0 && raw.Value > 0 {
		n.WaitSeconds = raw.Value
	}
	// weighted 节点旧式写法 options 合并到 next
	if len(n.Next) == 0 && len(raw.Options) > 0 {
		n.Next = raw.Options
	}
	// listen 别名合并
	n.ListenCallbacks = raw.ListenCallbacks
	if len(n.ListenCallbacks) == 0 && len(raw.Listen) > 0 {
		n.ListenCallbacks = raw.Listen
	}
	n.DelayMs = raw.DelayMs
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// NextNode 下游节点引用。
// Weight 仅在 weighted 类型节点中生效。
type NextNode struct {
	Node   string `json:"node"`   // 目标节点 ID
	Weight int    `json:"weight"` // 权重（weighted 类型）
}

// ListenRef 监听回调引用，定义在节点上。
// 当节点执行时（通常是连接节点），注册对应的推送监听。
type ListenRef struct {
	Cmd      uint8  `json:"cmd"`      // CMD 路由
	Act      uint8  `json:"act"`      // ACT 路由
	Server   string `json:"server"`   // 服务名（如 "logic"）
	Callback string `json:"callback"` // 回调定义名称（引用 callbacks 表）
}

// ActionDef 动作定义。
// Pattern 决定执行方式：
//   - tcpSend:      TCP 发送（不等响应）
//   - tcpRequest:   TCP 请求-响应（默认响应 cmd/act 与发送相同）
//     若指定 RespCmd/RespAct 则等待跨 cmd/act 响应
//   - httpPost:     HTTP POST 请求（form 或 JSON，取决于 BodyType）
//   - lua:          Lua 脚本执行
//   - connect:      TCP 连接建立
//   - connectUDP:   UDP 连接建立
//   - exchangeKey:  发送 (0,0) 空包获取密钥并设置到连接
//   - close:        关闭连接（Target=tcp/udp，默认 tcp，配合 Service 使用）
//   - clearState:   清除 StateStore 中的多个 key
//   - udpSendProto: UDP 发送 proto 消息
//   - udpSendRaw:   UDP 发送自定义二进制（RawBody 描述字段）
//   - waitListen:   等待监听消息（别名 listenWait）
//   - sleep:        暂停一段时间（Delay 毫秒）
type ActionDef struct {
	Pattern    string         `json:"pattern"`    // 动作模式
	Service    string         `json:"service"`    // 目标服务名
	Cmd        uint8          `json:"cmd"`        // CMD 路由
	Act        uint8          `json:"act"`        // ACT 路由
	RespCmd    uint8          `json:"respCmd"`    // 期望响应 CMD（可选，默认同 Cmd）
	RespAct    uint8          `json:"respAct"`    // 期望响应 ACT（可选，默认同 Act）
	Path       string         `json:"path"`       // HTTP 路径（httpPost 模式）
	Script     string         `json:"script"`     // Lua 脚本路径（lua 模式）
	Address    string         `json:"address"`    // 连接地址（connect 模式），可用 state:key 形式
	C2SProto   string         `json:"c2sProto"`   // 客户端消息 proto 全名
	S2CProto   string         `json:"s2cProto"`   // 服务器响应 proto 全名
	FormFields []FieldBind    `json:"formFields"` // HTTP 表单字段绑定
	Bindings   []FieldBind    `json:"bindings"`   // C2S 字段绑定
	Store      []StoreMapping `json:"store"`      // S2C 响应字段 -> 状态存储映射
	Timeout    int            `json:"timeout"`    // 超时秒数（waitListen 模式）
	Target     string         `json:"target"`     // close 模式目标: "tcp" 或 "udp"
	Keys       []string       `json:"keys"`       // clearState 要清除的 key 列表
	RawBody    []RawField     `json:"rawBody"`    // udpSendRaw 二进制字段描述
	Delay      int            `json:"delay"`      // 动作执行后延迟（毫秒）
	Optional   bool           `json:"optional"`   // 可选动作：依赖缺失时静默跳过
	SecretArg  string         `json:"secretArg"`  // exchangeKey 时也把密钥存到 state key

	// Heartbeat 相关字段（registerHeartbeat 模式）
	IntervalMs int `json:"intervalMs"` // 心跳间隔（毫秒）
}

// RawField 二进制字段描述。
// 用于 udpSendRaw 构建自定义二进制消息体（如 UDPHeartData、UDPFrameData）。
// Type 取值: u8/u16/u32/u64/i8/i16/i32/i64/bytes/time_ms/random_u16
type RawField struct {
	Name    string `json:"name"`    // 字段名（仅日志用）
	Type    string `json:"type"`    // 类型
	Value   any    `json:"value"`   // 固定值
	Source  string `json:"source"`  // 从 StateStore 读取
	Counter string `json:"counter"` // 从 StateStore 自增读取
	Min     int    `json:"min"`     // random_u16 最小值
	Max     int    `json:"max"`     // random_u16 最大值
	Length  int    `json:"length"`  // bytes 固定长度（未指定时按 Value/Source 长度）
}

// FieldBind C2S 字段绑定定义。
// Type 决定值来源：
//   - fixed:         固定值（Value 字段）
//   - state:         从 StateStore 读取（Source 为 key）
//   - stateRandom:   从 StateStore 中的列表随机选一个（Source 为 key），支持 Filters 过滤
//     Path 支持选列表元素的嵌套字段（如 "modeId"）
//   - stateRandomN:  从 StateStore 的列表随机选 N 个（Count 个）不重复
//   - stateMapKey:   从 state map 中随机选一个 key
//   - stateMapValue: 从 state map 中随机选一个 value（支持 Path 取嵌套字段）
//   - randomPick:    从 Values 列表随机选一个
//   - randomPickN:   从 Values 列表随机选 N 个
//   - randomInt:     随机整数（Min/Max 字段）
//   - randomBool:    随机布尔
//   - randomString:  随机字符串（Length 字段）
//   - randomExclude: 从 Values 或 state list 中随机选一个，且排除 ExcludeSource
//   - listSize:      返回 state 列表长度（int）
//
// 当 Required 为 true 且解析后的值为 nil 时，整个动作将被跳过（不发送消息）。
// stateRandom 在列表为空或过滤后为空时也会触发跳过。
type FieldBind struct {
	Field         string      `json:"field"`         // proto 字段名
	Type          string      `json:"type"`          // 绑定类型
	Value         any         `json:"value"`         // 固定值（fixed 类型）
	Source        string      `json:"source"`        // StateStore key（state/stateRandom 类型）
	Path          string      `json:"path"`          // 嵌套字段路径，点分格式
	Values        []any       `json:"values"`        // 候选值列表（randomPick 类型）
	Required      bool        `json:"required"`      // 必需字段：值为 nil 时跳过整个动作
	Filters       []FilterDef `json:"filters"`       // 过滤条件（stateRandom 类型，随机选取前先过滤）
	Min           int         `json:"min"`           // 最小值（randomInt 类型）
	Max           int         `json:"max"`           // 最大值（randomInt 类型）
	Length        int         `json:"length"`        // 字符串长度（randomString 类型）
	Count         int         `json:"count"`         // 选 N 个（stateRandomN/randomPickN 类型）
	Charset       string      `json:"charset"`       // 字符集（randomString 类型）
	ExcludeSource string      `json:"excludeSource"` // 排除源 StateStore key
	Optional      bool        `json:"optional"`      // 标记为非必需；值为 nil 时整个字段跳过（不跳整个动作）
}

// isRequired 判断绑定是否为必需字段。
// stateRandom 默认必需（从空列表选取无意义），其他类型默认非必需。
// Optional=true 可强制非必需。
func (fb *FieldBind) isRequired() bool {
	if fb.Optional {
		return false
	}
	if fb.Required {
		return true
	}
	switch fb.Type {
	case "stateRandom", "stateRandomN", "stateMapKey", "stateMapValue":
		return true
	}
	return false
}

// FilterDef 列表过滤条件定义。
// 用于 stateRandom 类型，在随机选取前过滤列表项。
// 对应旧工具中 utils.RandSilenceFilterOne 的 predicate。
//
// 示例：排除自身
//
//	{"path": "baseData.playerId", "op": "neq", "source": "playerId"}
//
// 示例：等级大于 10
//
//	{"path": "level", "op": "gt", "value": 10}
type FilterDef struct {
	Path   string `json:"path"`   // 列表项中的嵌套字段路径
	Op     string `json:"op"`     // 比较操作：eq/neq/gt/gte/lt/lte/contains/in/timeWindow/dailyTimeWindow/notNil/isNil
	Value  any    `json:"value"`  // 字面量值（与 Source 二选一）
	Source string `json:"source"` // StateStore key（与 Value 二选一）
}

// StoreMapping S2C 响应字段 -> StateStore 映射。
// 收到响应后，将指定 proto 字段的值写入 StateStore。
type StoreMapping struct {
	Field  string `json:"field"`  // proto 字段名（为空表示整个响应）
	Path   string `json:"path"`   // 嵌套字段路径，点分格式如 "data.baseInfo.name"
	Setter string `json:"setter"` // StateStore key
}

// CallbackDef 回调定义。
// 收到推送消息时的处理方式：声明式 store 或 Lua 脚本。
type CallbackDef struct {
	S2CProto string         `json:"s2cProto"` // 服务器推送 proto 全名
	Store    []StoreMapping `json:"store"`    // 声明式字段存储
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

// taskFlowRaw 用于自定义反序列化的辅助结构。
// JSON 中 nodes 是数组格式，需要转换为 map。
type taskFlowRaw struct {
	StartNode string                  `json:"startNode"`
	Nodes     []*Node                 `json:"nodes"`
	Actions   map[string]*ActionDef   `json:"actions"`
	Callbacks map[string]*CallbackDef `json:"callbacks"`
}

// UnmarshalJSON 自定义反序列化 TaskFlow。
// 将 nodes 数组按 id 转换为 map[string]*Node。
func (tf *TaskFlow) UnmarshalJSON(data []byte) error {
	var raw taskFlowRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	tf.StartNode = raw.StartNode
	tf.Actions = raw.Actions
	tf.Callbacks = raw.Callbacks

	tf.Nodes = make(map[string]*Node, len(raw.Nodes))
	for _, node := range raw.Nodes {
		if node != nil && node.ID != "" {
			tf.Nodes[node.ID] = node
		}
	}

	return nil
}
