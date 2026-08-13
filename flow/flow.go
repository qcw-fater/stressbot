// Package flow 提供流程引擎，驱动压测机器人的行为。
// flow.go 定义流程图结构（TaskFlow / Node / ActionDef / ListenDef / ListenRef 等），
// 所有结构均可从 JSON 配置加载，无需硬编码行为逻辑。
package flow

import (
	"stressbot/binding"
	"stressbot/errcode"
)

// TaskFlow 流程图定义。
// 由一组 Node 组成的有向图，从 "main" 节点开始串行执行。
// nodes 使用 JSON object（key = 节点 ID），无需自定义反序列化。
type TaskFlow struct {
	DefaultDelayMs int                   `json:"defaultDelayMs"` // 全局节点间默认延迟（毫秒）。0=引擎默认(1000ms)，<0=禁用
	Nodes          map[string]*Node      `json:"nodes"`          // 节点映射，key 为节点 ID
	Actions        map[string]*ActionDef `json:"actions"`        // 动作定义映射
	Listens        map[string]*ListenDef `json:"listens"`        // 监听定义映射
}

// Node 流程节点。
// 每种 type 只使用其对应的字段，其余字段留空。
//
// 节点类型：
//   - sequence: 顺序执行 next 中列出的所有子节点
//   - action:   执行一个声明式动作或 Lua 脚本，唯一产生副作用的节点
//   - loop:     循环执行单个 body 节点，支持次数/前置条件/后置条件
//   - boolean:  对 condition 求值，跳转到 trueNext 或 falseNext
//   - switch:   按 cases 顺序求值，跳转到第一条命中分支；全未命中走 defaultNext
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
	LoopCount      int    `json:"loopCount"`      // 循环次数；<0 = 无限循环，=0 = 跳过循环体，>0 = 执行 N 次
	Condition      string `json:"condition"`      // 前置条件：每次迭代开始前求值，false 时退出循环
	BreakCondition string `json:"breakCondition"` // 后置条件：每次迭代结束后求值，true 时退出循环

	// ── boolean 专用 ─────────────────────────────────────────────
	// Condition 字段同 loop，此处复用：boolean 的分支判断条件
	TrueNext  string `json:"trueNext"`  // 条件为 true 时跳转的节点 ID（空 = 不跳转）
	FalseNext string `json:"falseNext"` // 条件为 false 时跳转的节点 ID（空 = 不跳转）

	// ── switch 专用 ──────────────────────────────────────────────
	Cases       []SwitchCase `json:"cases"`       // 按顺序匹配的条件分支
	DefaultNext string       `json:"defaultNext"` // 所有 case 未命中时跳转的节点 ID（空 = 正常结束）

	// ── action 专用 ─────────────────────────────────────────────
	Action     string      `json:"action"`     // 引用 actions 表中的动作名称
	OnError    *OnErrorDef `json:"onError"`    // 动作失败后的错误链路（ignoreCodes/handler/retry/strategy）
	ListenRefs []ListenRef `json:"listenRefs"` // 动作执行后注册的持久化推送监听引用

	// ── weighted 专用 ─────────────────────────────────────────────
	Options []WeightedOption `json:"options"` // 加权选项列表

	// ── wait 专用 ─────────────────────────────────────────────────
	WaitMs  int    `json:"waitMs"`  // 固定等待时长（毫秒）；仅当 WaitMin/WaitMax 都 ≤ 0 时生效
	WaitMin int    `json:"waitMin"` // 随机等待最小值（毫秒，含）；与 WaitMax 配合使用
	WaitMax int    `json:"waitMax"` // 随机等待最大值（毫秒，含）；WaitMin/WaitMax 同时 > 0 时随机模式优先
	Then    string `json:"then"`    // 等待完成后执行的唯一后继节点 ID；空表示流程在此结束

	// ── 通用（action 节点有效）────────────────────────────
	DelayMs int `json:"delayMs"` // > 0: 使用此值；= 0: 使用 TaskFlow.DefaultDelayMs；< 0: 禁用延迟

	compiledCondition      *CompiledCondition
	compiledBreakCondition *CompiledCondition
}

// WeightedOption 加权选项，用于 weighted 节点。
type WeightedOption struct {
	Node   string `json:"node"`   // 目标节点 ID
	Weight int    `json:"weight"` // 权重值
}

// OnErrorDef 定义 action 节点失败后的错误链路。
type OnErrorDef struct {
	IgnoreCodes []errcode.ErrorCode `json:"ignoreCodes"` // 命中后流程继续，但 monitor 保留原始失败样本
	Handler     string              `json:"handler"`     // 错误处理子流程节点 ID（调用边，不是普通 next）
	Retry       *RetryDef           `json:"retry"`       // 当前 action 的局部重试配置
	Strategy    string              `json:"strategy"`    // resume/skip/abort，空等同 resume
}

// RetryDef 定义 action 失败后的局部重试配置。
type RetryDef struct {
	MaxRetries   int `json:"maxRetries"`   // 额外重试次数；2 表示最多执行 1+2 次
	RetryDelayMs int `json:"retryDelayMs"` // 每次重试前的协作式等待毫秒数
}

// SwitchCase 表示 switch 节点的一条条件分支。
type SwitchCase struct {
	Condition string `json:"condition"` // 条件表达式，语法同 boolean/loop
	Next      string `json:"next"`      // 条件命中后执行的节点 ID

	compiledCondition *CompiledCondition
}

// ListenRef 监听引用，定义在节点上。
// 当节点执行时（通常是连接节点），注册对应的推送监听。
// Route 为不透明路由，运行时通过 protocol.ExpectedRouteKey(route) 计算实际 routeKey。
type ListenRef struct {
	Route  any    `json:"route"`  // 不透明路由（与 ActionDef.Route 格式一致）
	Server string `json:"server"` // 连接名，格式：协议:服务名（如 "tcp:logic"、"udp:udp"）
	Listen string `json:"listen"` // 监听定义名称（引用 listens 表），空 = 仅缓存不回调
	// QueueSize 监听缓存队列容量。
	//   - 未写（nil）→ 默认 1，与历史单槽语义逐字节等价；
	//   - 显式 > 0 → 按该值预创建环形队列；
	//   - 显式 <= 0 → 配置错误，注册时（robot.RegisterListen）报错，不静默 clamp。
	// 用 *int 区分「未写」与「显式 0」，符合全局约束「禁止兼容性兜底」。
	QueueSize *int `json:"queueSize,omitempty"`
}

// 动作模式常量。
const (
	PatternTCPSend     = "tcpSend"     // TCP 单向发送
	PatternTCPRequest  = "tcpRequest"  // TCP 请求-响应
	PatternTCPConnect  = "tcpConnect"  // TCP 连接建立
	PatternTCPClose    = "tcpClose"    // TCP 连接关闭
	PatternTCPListen   = "tcpListen"   // TCP 推送消息消费（事件等待 ListenRefs 预缓存）
	PatternUDPSend     = "udpSend"     // UDP 单向发送
	PatternUDPRequest  = "udpRequest"  // UDP 请求-响应
	PatternUDPConnect  = "udpConnect"  // UDP 连接建立
	PatternUDPClose    = "udpClose"    // UDP 连接关闭
	PatternUDPListen   = "udpListen"   // UDP 推送消息消费（事件等待 ListenRefs 预缓存）
	PatternHTTPRequest = "httpRequest" // HTTP 请求
	PatternSetState    = "setState"    // 设置状态变量
	PatternClearState  = "clearState"  // 清除状态变量
	PatternLua         = "lua"         // Lua 脚本执行（由 robot 层 ActionHandler 处理）
)

// 条件表达式前缀
const (
	PrefixState = "state:" // 状态存储前缀
	PrefixLua   = "lua:"   // Lua 脚本前缀
)

// 节点类型常量
const (
	NodeSequence = "sequence"
	NodeAction   = "action"
	NodeLoop     = "loop"
	NodeBoolean  = "boolean"
	NodeSwitch   = "switch"
	NodeWeighted = "weighted"
	NodeWait     = "wait"
	NodeBreak    = "break"
	NodeContinue = "continue"
)

// 内容类型常量
const (
	ContentJSON = "json"
	ContentForm = "form"
)

// onError.strategy 常量。
const (
	StrategyResume = "resume"
	StrategyAbort  = "abort"
	StrategySkip   = "skip"
)

// ActionDef 动作定义。
// Pattern 决定执行方式（取值见 Pattern 常量）。
type ActionDef struct {
	Name        string                 `json:"-"`           // 运行时回填（actions map 的 key），不参与序列化
	Pattern     string                 `json:"pattern"`     // 动作模式
	Service     string                 `json:"service"`     // 目标服务名
	Route       any                    `json:"route"`       // 不透明路由，原样传给 protocol.EncodeTCP
	Script      string                 `json:"script"`      // Lua 脚本路径（lua 模式）
	Address     string                 `json:"address"`     // 连接地址（connect 模式），可用 state:key 形式
	C2SProto    string                 `json:"c2sProto"`    // 客户端消息 proto 全名
	S2CProto    string                 `json:"s2cProto"`    // 服务器响应 proto 全名
	Bindings    []binding.FieldBind    `json:"bindings"`    // C2S 字段绑定
	Store       []binding.StoreMapping `json:"store"`       // S2C 响应字段 -> 状态存储映射
	Timeout     int                    `json:"timeout"`     // 超时秒数（request/listen 模式）
	Keys        []string               `json:"keys"`        // clearState 要清除的 key 列表
	URL         string                 `json:"url"`         // HTTP 请求 URL（httpRequest 模式），支持 state: 前缀
	Method      string                 `json:"method"`      // HTTP 方法（httpRequest 模式）：POST(默认) / GET
	ContentType string                 `json:"contentType"` // HTTP 内容类型（httpRequest 模式）：json(默认) / form
}

// ListenDef 回调定义。
type ListenDef struct {
	S2CProto string                 `json:"s2cProto"` // 解析推送消息的 proto 全名
	Store    []binding.StoreMapping `json:"store"`    // 响应字段到 StateStore 的映射
	Script   string                 `json:"script"`   // Lua listen 回调脚本；与 Store 互斥，推送到达后串行执行
}

// Node 获取指定 ID 的节点
func (tf *TaskFlow) Node(id string) (*Node, bool) {
	n, ok := tf.Nodes[id]
	return n, ok
}

// Action 获取指定名称的动作定义
func (tf *TaskFlow) Action(name string) (*ActionDef, bool) {
	a, ok := tf.Actions[name]
	return a, ok
}

// Listen 获取指定名称的监听定义
func (tf *TaskFlow) Listen(name string) (*ListenDef, bool) {
	l, ok := tf.Listens[name]
	return l, ok
}
