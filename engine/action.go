package engine

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"stressbot/adapter"
	"stressbot/errcode"
	"stressbot/protox"
	"stressbot/state"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// 声明式动作默认超时与轮询间隔。
const (
	DefaultRequestTimeoutSec = 10   // tcpRequest / udpRequest 默认超时（秒）
	DefaultListenTimeoutSec  = 60   // tcpListen / udpListen 默认超时（秒）
	DefaultPollMs            = 100  // 轮询间隔（毫秒）
	DefaultHeartbeatMs       = 3000 // 心跳默认间隔（毫秒）

	// 计时细分级别，控制 ActionExecutor 中哪些时间点被记录。
	TimingLevelRTTOnly = 0 // 默认：仅 WireRTT + SendCost
	TimingLevelCodec   = 1 // 增加 EncodeCost
	TimingLevelFull    = 2 // 增加 BuildCost / ParseStoreCost

	randomStringCharsetLower    = "abcdefghijklmnopqrstuvwxyz"
	randomStringCharsetUpper    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	randomStringCharsetNumeric  = "0123456789"
	randomStringCharsetAlpha    = randomStringCharsetLower + randomStringCharsetUpper
	randomStringCharsetAlphanum = randomStringCharsetAlpha + randomStringCharsetNumeric
)

var randomStringCharsetAliases = map[string]string{
	"lower":    randomStringCharsetLower,
	"upper":    randomStringCharsetUpper,
	"alpha":    randomStringCharsetAlpha,
	"numeric":  randomStringCharsetNumeric,
	"alphanum": randomStringCharsetAlphanum,
}

// RequestTiming 单次 request-response 的耗时拆解。
type RequestTiming struct {
	SendCost             time.Duration
	WireRTT              time.Duration
	DecodeWait           time.Duration
	DecodeCost           time.Duration
	DispatchToActionWait time.Duration
}

// ClientTiming 单次 action 的客户端侧耗时拆解。
type ClientTiming struct {
	BuildCost      time.Duration
	EncodeCost     time.Duration
	SendCost       time.Duration
	DecodeWait     time.Duration
	DecodeCost     time.Duration
	DispatchWait   time.Duration
	ParseStoreCost time.Duration
}

// ActionTiming 单次 action 执行的耗时拆解。
type ActionTiming struct {
	Requests []RequestTiming
	// FailedRequests 本 action 内「发出去但没等回响应帧」的请求数（超时 / 连接断开 /
	// 发送失败）。这类请求算不出 WireRTT，但它们恰恰是服务端表现最差的那批样本——
	// 若不单独记，它们就从 RTT Apdex 的分母里彻底消失，导致服务端越是超时、Apdex 越高。
	// 计分时按 frustrated 计入分母，见 monitor.RecordAction。
	//
	// 不含两类：客户端主动取消（ctx cancel，与服务端无关）；服务端正常回了业务错误码
	// （HeaderErr——响应帧到了，WireRTT 有效，按正常样本分档）。
	FailedRequests int
	// ListenWaits 监听类命中的等待时长样本：开始等待 → 帧被内核收到。
	//
	// 与 Requests 分开统计，因为两者是不同的物理量：请求-响应测的是服务端处理单个
	// 请求的延迟（毫秒级，有公认阈值，可打 Apdex）；监听等待测的是服务端业务时长
	// （匹配、等队友，秒级，没有普遍阈值，打 Apdex 是伪精度）。混进同一个分布或
	// 共用同一个 T，两个数都会失去意义。
	ListenWaits []time.Duration
	// ListenReady 命中时消息已在队列中的次数——等待时长不可测，只计次不产样本。
	// 计成 0ms 样本会把 P50 拉向 0，正是低估的来源。
	ListenReady int
	// ListenTimeouts 监听超时次数。不进等待时长分布（没有时延值，且会把 P99 顶到
	// 超时上限并掩盖真实分布），单独成率。
	ListenTimeouts int
	Client         ClientTiming
}

// AddRequest 追加一次 request-response 样本。
func (t *ActionTiming) AddRequest(req RequestTiming) {
	if req.WireRTT <= 0 {
		return
	}
	t.Requests = append(t.Requests, req)
	t.Client.SendCost += req.SendCost
	t.Client.DecodeWait += req.DecodeWait
	t.Client.DecodeCost += req.DecodeCost
	t.Client.DispatchWait += req.DispatchToActionWait
}

// AddFailedRequest 记一次「无响应帧」的请求失败（超时 / 断连 / 发送失败）。
func (t *ActionTiming) AddFailedRequest() {
	t.FailedRequests++
}

// AddListenHit 按等待时长的可测性记一次监听命中。
func (t *ActionTiming) AddListenHit(wait time.Duration, kind ListenWaitKind) {
	switch kind {
	case ListenWaitMeasured:
		t.ListenWaits = append(t.ListenWaits, wait)
	case ListenWaitReady:
		t.ListenReady++
	}
}

// AddListenTimeout 记一次监听超时。
func (t *ActionTiming) AddListenTimeout() {
	t.ListenTimeouts++
}

// ListenWaitKind 一次监听命中的等待时长可测性。
type ListenWaitKind int

const (
	// ListenWaitUnknown 没有帧到达时刻（网络层未透传 / 测试桩），不产样本也不计次。
	ListenWaitUnknown ListenWaitKind = iota
	// ListenWaitReady 帧在开始等待之前就已到达，等待时长不可测。
	ListenWaitReady
	// ListenWaitMeasured 等待时长可测。
	ListenWaitMeasured
)

// ClassifyListenWait 判定一次监听命中的等待时长。
//
// 三态而非「算不出就记 0」：记 0 会把「消息早就在队列里」和「服务端瞬间响应」混为一谈，
// 而这两者对压测的含义完全相反——前者说明测量起点选错了，后者才是服务端快。
func ClassifyListenWait(waitStart, recvFrameAt time.Time) (time.Duration, ListenWaitKind) {
	if waitStart.IsZero() || recvFrameAt.IsZero() {
		return 0, ListenWaitUnknown
	}
	if !recvFrameAt.After(waitStart) {
		return 0, ListenWaitReady
	}
	return recvFrameAt.Sub(waitStart), ListenWaitMeasured
}

// WireRTTSum 返回本 action 内所有 RTT 样本总和。
func (t *ActionTiming) WireRTTSum() time.Duration {
	var total time.Duration
	for _, req := range t.Requests {
		if req.WireRTT > 0 {
			total += req.WireRTT
		}
	}
	return total
}

// ActionExecutor 声明式动作执行器。
// 根据 ActionDef 的 Pattern 分派到具体的执行方法，处理消息构建、发送、接收和状态存储。
//
// encode 侧（protocolEncode / ExpectedRouteKey / DescribeError）通过 resolver 按
// "<proto>:<service>"（proto 由 pattern 推导，service=def.Service）Resolve 出该连接的
// Go SchemaAdapter。Resolve nil 时由调用方 fail loud（ErrEncodeFailed），不静默兜底；
// 业务 encode/decode/dial/心跳/listen/Lua 网络 API 全程经 CodecResolver，无 Lua codec 生产路径。
type ActionExecutor struct {
	netSender   NetSender             // 网络发送委托，由 Robot 层实现
	store       *state.Store          // Robot 状态存储，保存服务器响应字段和中间变量
	factory     *protox.Factory       // 动态 protobuf 消息工厂，用于创建/序列化/解析 proto 消息
	resolver    adapter.CodecResolver // 按 "<proto>:<service>" 解析每连接的 Go codec adapter
	timingLevel int                   // 计时细分级别：0=rtt, 1=codec, 2=full
	// coopSleep 协作式休眠（由 Robot 调度器注入）：声明式 listen 轮询间隔走它，
	// 等待期间 drain Robot mailbox（跑 listen 回调等），不饿死协作式工作。
	// nil（engine 独立运行/测试）时回退裸 time.After + ctx，保持 engine 对 robot 解耦。
	coopSleep func(ctx context.Context, d time.Duration) error
	// coopIO 协作式阻塞 I/O（由 Robot 调度器注入 sched.runIO）：声明式 httpRequest / tcpConnect /
	// udpConnect 的阻塞调用经它在后台 goroutine 执行，调用 goroutine 等待期间 drain Robot mailbox。
	// 与 Lua await_*（share/http/connect）同源（同一 runIO 原语），声明式与脚本两条路径协作式语义一致。
	// nil（engine 独立运行/测试）时直接同步调 job，保持 engine 对 robot 解耦。
	coopIO func(job func()) error
}

// SetCooperativeSleeper 注入协作式休眠后端（Robot 调度器）。
// 注入后声明式 listen 的轮询间隔不再裸 time.Sleep，而是在等待窗口内 drain Robot mailbox，
// 与 Lua await / 节点延迟同源——actor 运行时「任何阻塞点都不裸阻塞」的统一约束覆盖到声明式动作。
func (ae *ActionExecutor) SetCooperativeSleeper(f func(ctx context.Context, d time.Duration) error) {
	ae.coopSleep = f
}

// SetCooperativeIO 注入协作式阻塞 I/O 后端（Robot 调度器 sched.runIO）。
// 注入后声明式 httpRequest / tcpConnect / udpConnect 的阻塞调用不再裸阻塞执行器，而是在后台
// goroutine 执行、等待期间 drain Robot mailbox——与 Lua await_*（share/http/connect）同一原语。
func (ae *ActionExecutor) SetCooperativeIO(f func(job func()) error) {
	ae.coopIO = f
}

// runIO 协作式执行一次阻塞 I/O：注入了 coopIO 则走调度器（后台跑 + drain mailbox），否则直接
// 同步调 job（engine 独立运行无 mailbox 可 drain）。job 的结果经其自身闭包捕获，调用方在 runIO
// 返回后读取。
func (ae *ActionExecutor) runIO(job func()) error {
	if ae.coopIO != nil {
		return ae.coopIO(job)
	}
	job()
	return nil
}

// sleep 协作式休眠 d：注入了 coopSleep 则走调度器（drain mailbox），否则裸 time.After + ctx。
// 返回非 nil（= ctx.Err()）表示被取消，调用方据此提前退出。
func (ae *ActionExecutor) sleep(ctx context.Context, d time.Duration) error {
	if ae.coopSleep != nil {
		return ae.coopSleep(ctx, d)
	}
	if d <= 0 {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	if ctx == nil {
		time.Sleep(d)
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// NetExchange 单次 TCP/UDP 请求或监听得到的网络交换结果。
type NetExchange struct {
	Body          []byte
	HeaderErr     uint64
	SendWireBytes int
	RecvWireBytes int
	Timing        RequestTiming
	// RecvFrameAt 响应帧被内核收到的时刻，由网络层原样透传（零值表示该路径未提供）。
	//
	// 监听类动作的等待时长必须用它算，不能用动作的 wallClock：监听队列会缓存消息，
	// 动作开始等待时消息可能早就到了，用 wallClock 会把等待测成接近 0，
	// 匹配、进场这类耗时会被系统性低估。见 ClassifyListenWait。
	RecvFrameAt time.Time
}

// HTTPExchange 单次 HTTP 请求得到的交换结果。
// SendWireBytes / RecvWireBytes 表示 HTTP message bytes，不含 TCP/IP/TLS record 开销。
type HTTPExchange struct {
	StatusCode    int
	Body          []byte
	SendWireBytes int
	RecvWireBytes int
	NetLatency    time.Duration
}

// NetSender 网络发送委托接口。
// 由 Robot 层实现，封装 TCP/UDP 连接管理和 HTTP 请求能力。
//
// 注意：TCPRequest / UDPRequest 返回的 Timing.WireRTT 表示
// "Send 完成 → 收到完整响应帧"，不含 decode、响应解析和 state 写入等客户端开销。
type NetSender interface {
	// ── 发送 / 请求-响应 ─────────────────────────────────────────────────

	// TCPSend 向指定服务发送 TCP 数据包（不等响应），返回发送字节数。
	TCPSend(service string, packet []byte) (int, error)
	// UDPSend 向指定服务发送 UDP 数据包（不等响应），返回发送字节数。
	UDPSend(service string, data []byte) (int, error)
	// TCPRequest 发送 TCP 请求并等待匹配 routeKey 的响应。
	// 返回发送 WireBytes、接收 WireBytes、响应体、协议头错误码和网络耗时。timeout 可选，覆盖默认超时。
	TCPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) (*NetExchange, error)
	// UDPRequest 发送 UDP 请求并等待匹配 routeKey 的响应。
	// 返回值语义同 TCPRequest。
	UDPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) (*NetExchange, error)
	// ConnectTCP 建立到指定地址的 TCP 连接，按 service 名注册。
	ConnectTCP(service, address string) error
	// ConnectUDP 建立到指定地址的 UDP 连接，按 service 名注册。
	ConnectUDP(service, address string) error
	// HTTPRequest 发送 HTTP 请求，返回状态码、响应体、纯网络往返时长（含 body 读完）和可能的错误。
	HTTPRequest(url, method, contentType string, body []byte) (*HTTPExchange, error)

	// ── 连接管理 ──────────────────────────────────────────────────────────

	// CloseTCP 关闭指定服务的 TCP 连接。
	CloseTCP(service string)
	// CloseUDP 关闭指定服务的 UDP 连接。
	CloseUDP(service string)

	// ── 监听 ──────────────────────────────────────────────────────────────

	// GetTCPListenResp 非阻塞获取 TCP 监听的最近一次响应（含协议头错误码）。
	GetTCPListenResp(service string, routeKey string) *NetExchange
	// GetUDPListenResp 非阻塞获取 UDP 监听的最近一次响应（含协议头错误码）。
	GetUDPListenResp(service string, routeKey string) *NetExchange
	// EnsureTCPListener 为指定 routeKey 注册监听占位（callback=nil，轮询模式）。
	// 由 Lua ensure_tcp_listener 调用（queueSize 固定为 1，大容量请用 flow listenRefs 的 queueSize 配置）。
	EnsureTCPListener(service string, routeKey string, queueSize int)
	// EnsureUDPListener 为指定 routeKey 注册监听占位（callback=nil，轮询模式）。
	// 由 Lua ensure_udp_listener 调用（queueSize 固定为 1，大容量请用 flow listenRefs 的 queueSize 配置）。
	EnsureUDPListener(service string, routeKey string, queueSize int)

	// ── 加密密钥 ──────────────────────────────────────────────────────────

	// GetTCPSecretKey 获取指定服务 TCP 连接的加密密钥。
	GetTCPSecretKey(service string) []byte
	// SetTCPSecretKey 设置指定服务 TCP 连接的加密密钥。
	SetTCPSecretKey(service string, key []byte)
	// GetUDPSecretKey 获取指定服务 UDP 连接的加密密钥。
	GetUDPSecretKey(service string) []byte
	// SetUDPSecretKey 设置指定服务 UDP 连接的加密密钥。
	SetUDPSecretKey(service string, key []byte)
}

// NewActionExecutor 创建声明式动作执行器。
// resolver 按 "<proto>:<service>" 解析每连接的 Go codec adapter（T2-C2 起 encode 侧）。
// timingLevel: 0=仅 RTT, 1=含编解码, 2=完整客户端细分。
func NewActionExecutor(store *state.Store, sender NetSender, factory *protox.Factory, resolver adapter.CodecResolver, timingLevel int) *ActionExecutor {
	return &ActionExecutor{
		netSender:   sender,
		store:       store,
		factory:     factory,
		resolver:    resolver,
		timingLevel: timingLevel,
	}
}

// Execute 执行声明式动作，返回发送/接收字节数、网络耗时拆解和错误。
//
// timing 仅在确实发生 send→recv 网络窗口的 pattern 中填充（request/listen/http）；
// send-only / connect / close / state 类动作 timing 为零值，monitor 据此跳过 latency 直方图。
func (ae *ActionExecutor) Execute(ctx context.Context, def *ActionDef) (sendBytes, recvBytes int, timing ActionTiming, err error) {
	switch def.Pattern {
	case PatternTCPSend:
		sendBytes, timing, err = ae.execSend("tcp", def)
	case PatternTCPRequest:
		sendBytes, recvBytes, timing, err = ae.execRequest("tcp", def)
	case PatternTCPConnect:
		err = ae.execTCPConnect(def)
	case PatternTCPClose:
		err = ae.execTCPClose(def)
	case PatternTCPListen:
		recvBytes, timing, err = ae.execListen(ctx, "tcp", def)
	case PatternUDPSend:
		sendBytes, timing, err = ae.execSend("udp", def)
	case PatternUDPRequest:
		sendBytes, recvBytes, timing, err = ae.execRequest("udp", def)
	case PatternUDPConnect:
		err = ae.execUDPConnect(def)
	case PatternUDPClose:
		err = ae.execUDPClose(def)
	case PatternUDPListen:
		recvBytes, timing, err = ae.execListen(ctx, "udp", def)
	case PatternHTTPRequest:
		sendBytes, recvBytes, timing, err = ae.execHTTPRequest(def)
	case PatternClearState:
		err = ae.execClearState(def)
	case PatternSetState:
		err = ae.execSetState(def)
	default:
		err = NewActionError(errcode.ErrUnknownPattern, "pattern="+def.Pattern)
	}
	return
}

// buildBody 构建消息体字节（序列化 proto 消息）。
// 直接复用当前执行器构造 proto body（不再经 BuildProtoBody 新建临时 ActionExecutor），
// 与心跳 proto 模式共享同一 bindFields 语义，行为保持不变。
// 为保留旧错误上下文（"action=Name field=..."），将 ActionDef.Name 注入 bindFields actionName。
func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error) {
	body, _, err := ae.BuildProtoBody(def.C2SProto, def.Bindings, def.Name)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// BuildProtoBody 按 c2sProto + bindings 构造 proto 消息并序列化为 body 字节。
//
// 复用现有 binding 解析（bindFields 同语义），供 tcpSend/tcpRequest（经 buildBody）
// 与心跳 proto 模式（HeartbeatConfig.C2SProto + Bindings）共享。Go-only，不碰 Lua。
//
// 参数：
//   - c2sProto：proto 全名；空串 → 返回 (nil, false, nil)（空 body，与 buildBody 旧行为对齐）；
//   - bindings：字段绑定列表（与 ActionDef.Bindings 同构，复用 bindFields 的 condition/optional/required/map 等全套语义）；
//   - store：线程安全 state.Store（ActionExecutor 传 ae.store；心跳闭包传 robot.state）；
//   - factory：protox.Factory（ActionExecutor 传 ae.factory；心跳闭包传 robot.factory）；
//   - actionName：错误上下文标识（tcpSend 传 ActionDef.Name；心跳传 action 名或空串）。
//
// 返回：
//   - body：序列化后的字节流（c2sProto 空时为 nil）；
//   - skip：当前实现恒为 false（保留给未来 binding 缺失「跳过」语义，与心跳 SkipWhenMissing 对齐预留）；
//   - err：proto 创建/绑定/序列化失败（含 ActionError，中文上下文，不静默兜底）。
//
// 不变量：buildBody 重构后 tcpSend/request 行为零变化（既有测试全绿）。
func BuildProtoBody(c2sProto string, bindings []FieldBind, store *state.Store, factory *protox.Factory, actionName string) (body []byte, skip bool, err error) {
	// 一次性调用（无常驻执行器）时的便捷入口：临时 ActionExecutor 仅承载 store+factory。
	// 高频路径（tcpSend/request 经 buildBody、心跳 tick）应改持有可复用执行器并调 (*ActionExecutor).BuildProtoBody，
	// 避免每次分配临时执行器。
	ae := &ActionExecutor{store: store, factory: factory}
	return ae.BuildProtoBody(c2sProto, bindings, actionName)
}

// BuildProtoBody 用当前执行器构造并序列化 proto 消息 body。
//
// 复用 ae.bindFields 的完整 binding 解析语义（condition/optional/required/map 等），
// 供 tcpSend/tcpRequest（经 buildBody）与心跳 proto 模式共享，且可复用同一执行器实例，
// 消除每次发送 / 每个心跳 tick 新建临时 ActionExecutor 的分配。Go-only，不碰 Lua。
//
//   - c2sProto 为空 → 返回 (nil,false,nil)（空 body，与旧 buildBody 行为对齐）；
//   - skip 恒为 false（保留给未来 binding 缺失「跳过」语义）；
//   - err 含 proto 创建/绑定/序列化失败（ActionError，中文上下文）。
func (ae *ActionExecutor) BuildProtoBody(c2sProto string, bindings []FieldBind, actionName string) (body []byte, skip bool, err error) {
	if c2sProto == "" {
		return nil, false, nil
	}
	msg, err := ae.factory.Create(c2sProto)
	if err != nil {
		return nil, false, NewActionError(errcode.ErrCreateMsg, "action="+actionName+" proto="+c2sProto, err)
	}
	if err := ae.bindFields(msg, bindings, actionName); err != nil {
		return nil, false, err
	}
	body, err = ae.factory.Serialize(msg)
	if err != nil {
		return nil, false, NewActionError(errcode.ErrSerialize, "action="+actionName+" proto="+c2sProto, err)
	}
	return body, false, nil
}

// bindFields 将字段绑定列表应用到 proto 消息。
//
// 每个绑定的处理流程（跳过优先级从高到低）：
//  1. Condition 检查：condition 表达式求值为 false → 跳过该绑定
//  2. nil 值处理：resolveFieldValue 返回 nil 时，
//     - Optional=true → 静默跳过
//     - Required=true 或隐式必需类型（state/stateFirst/stateRandom 等）→ 返回错误
//     - 其余情况 → 静默跳过（后续 StoreAs 和 proto 赋值也会跳过）
//  3. StoreAs 写入：值非 nil 且配置了 StoreAs → 写入 state
//  4. 空 Field 跳过：Field 为空字符串 → 跳过 proto 赋值（仅 StoreAs 的绑定会走到这里）
//  5. proto 赋值：调用 Factory.SetField 写入消息字段
func (ae *ActionExecutor) bindFields(msg proto.Message, bindings []FieldBind, actionName string) error {
	for i := range bindings {
		fb := &bindings[i]

		// 1) condition 为 false 时跳过
		if !ae.evaluateCondition(fb.Condition) {
			continue
		}

		value, err := ae.resolveFieldValueStrict(fb, actionName, fb.Field)
		if err != nil {
			return err
		}

		// 2) nil 值：optional 跳过，required 报错
		if value == nil {
			if fb.Optional {
				continue
			}
			if fb.Required || isImplicitRequired(fb.Type) {
				return NewActionError(errcode.ErrBindField, "action="+actionName+" field="+fb.Field)
			}
		}

		// 3) StoreAs：同时写 state（即使 Field 为空也写入）
		if fb.StoreAs != "" && value != nil {
			ae.store.SetPath(fb.StoreAs, value)
		}

		// 4) 值仍为 nil 或 Field 为空 → 跳过 proto 赋值
		if value == nil || fb.Field == "" {
			continue
		}

		if err := ae.factory.SetField(msg, fb.Field, value); err != nil {
			return NewActionError(errcode.ErrBindField, "action="+actionName+" field="+fb.Field, err)
		}
	}
	return nil
}

func (ae *ActionExecutor) resolveFieldValueStrict(fb *FieldBind, actionName, fieldName string) (any, error) {
	if fb.Type != BindMap {
		return ae.resolveFieldValue(fb), nil
	}

	return ae.resolveMapValueStrict(fb, actionName, fieldName)
}

func (ae *ActionExecutor) resolveMapValueStrict(fb *FieldBind, actionName, fieldName string) (any, error) {
	result := make(map[any]any, len(fb.Entries))
	for _, entry := range fb.Entries {
		if !isComparableMapKey(entry.Key) {
			return nil, NewActionError(errcode.ErrBindField, fmt.Sprintf("action=%s field=%s mapKey=%v key 不可比较", actionName, fieldName, entry.Key))
		}
		if !ae.evaluateCondition(entry.Value.Condition) {
			continue
		}
		if entry.Value.Type == BindMap {
			return nil, NewActionError(errcode.ErrBindField, fmt.Sprintf("action=%s field=%s mapKey=%v 不支持嵌套 map", actionName, fieldName, entry.Key))
		}

		value := ae.resolveFieldValue(&entry.Value)
		if value == nil {
			if entry.Value.Optional {
				continue
			}
			if entry.Value.Required || isImplicitRequired(entry.Value.Type) {
				return nil, NewActionError(errcode.ErrBindField, fmt.Sprintf("action=%s field=%s mapKey=%v value 缺失", actionName, fieldName, entry.Key))
			}
			continue
		}
		result[entry.Key] = value
	}
	return result, nil
}

func isComparableMapKey(key any) bool {
	if key == nil {
		return true
	}
	return reflect.TypeOf(key).Comparable()
}

// resolveFieldValue 解析字段绑定值，返回 Go 原生类型。
func (ae *ActionExecutor) resolveFieldValue(fb *FieldBind) any {
	var val any

	switch fb.Type {
	case BindFixed, "":
		val = fb.Value

	case BindState:
		if fb.Source == "" {
			return nil
		}
		val = ae.store.Get(fb.Source)

	case BindStateFirst:
		// 零拷贝取首元素（不再 GetList 全表拷贝）。
		v, ok := ae.store.PickFromList(fb.Source, func(int) int { return 0 })
		if !ok {
			return nil
		}
		val = v

	case BindStateRandom:
		if len(fb.Filters) == 0 {
			// 无过滤器：读锁内直接随机取一个，避免 GetList 全表拷贝。
			v, ok := ae.store.PickFromList(fb.Source, rand.Intn)
			if !ok {
				return nil
			}
			val = v
		} else {
			// 有过滤器：需先拿到（快照）列表再筛选。
			list := ae.store.GetList(fb.Source)
			if len(list) == 0 {
				return nil
			}
			filtered := ae.applyFilters(list, fb.Filters)
			if len(filtered) == 0 {
				return nil
			}
			val = filtered[rand.Intn(len(filtered))]
		}

	case BindStateRandomN:
		list := ae.store.GetList(fb.Source)
		if len(list) == 0 {
			return nil
		}
		filtered := ae.applyFilters(list, fb.Filters)
		if len(filtered) == 0 {
			return nil
		}
		n := fb.Count
		if n <= 0 {
			n = 1
		}
		picked := pickN(filtered, n)
		if fb.Path != "" {
			result := make([]any, 0, len(picked))
			for _, item := range picked {
				v := navigatePath(item, fb.Path)
				if v != nil {
					result = append(result, v)
				}
			}
			return result
		}
		return picked

	case BindStateMapKey:
		// 零拷贝随机取一个 key（不再构造 keys 切片）。
		k, ok := ae.store.PickMapKey(fb.Source, rand.Intn)
		if !ok {
			return nil
		}
		return k

	case BindStateMapValue:
		if len(fb.Filters) == 0 {
			// 无过滤器：读锁内直接随机取一个 value，避免 GetMap 全表拷贝。
			v, ok := ae.store.PickMapValue(fb.Source, rand.Intn)
			if !ok {
				return nil
			}
			val = v
		} else {
			// 有过滤器：需全量 values 再筛选。
			m := ae.store.GetMap(fb.Source)
			if len(m) == 0 {
				return nil
			}
			values := make([]any, 0, len(m))
			for _, v := range m {
				values = append(values, v)
			}
			values = ae.applyFilters(values, fb.Filters)
			if len(values) == 0 {
				return nil
			}
			val = values[rand.Intn(len(values))]
		}

	case BindRandomPick:
		if len(fb.Values) == 0 {
			return nil
		}
		val = fb.Values[rand.Intn(len(fb.Values))]

	case BindRandomPickN:
		if len(fb.Values) == 0 {
			return nil
		}
		n := fb.Count
		if n <= 0 {
			n = 1
		}
		return pickN(fb.Values, n)

	case BindRandomPickMap:
		if len(fb.Values) == 0 || fb.KeySource == "" {
			return nil
		}
		keyVal := ae.store.Get(fb.KeySource)
		if keyVal == nil {
			return nil
		}
		keyStr := fmt.Sprintf("%v", keyVal)
		for _, entry := range fb.Values {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			k, _ := m["key"]
			if fmt.Sprintf("%v", k) != keyStr {
				continue
			}
			items, ok := m["values"].([]any)
			if !ok || len(items) == 0 {
				return nil
			}
			return items[rand.Intn(len(items))]
		}
		return nil

	case BindRandomExclude:
		var pool []any
		if len(fb.Values) > 0 {
			pool = fb.Values
		} else if fb.Source != "" {
			pool = ae.store.GetList(fb.Source)
		}
		if len(pool) == 0 {
			return nil
		}
		var exclude any
		if fb.ExcludeSource != "" {
			exclude = ae.store.Get(fb.ExcludeSource)
		}
		filtered := make([]any, 0, len(pool))
		for _, it := range pool {
			if !state.DeepEqual(it, exclude) {
				filtered = append(filtered, it)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		val = filtered[rand.Intn(len(filtered))]

	case BindRandomInt:
		lo, hi := fb.Min, fb.Max
		if lo >= hi {
			return lo
		}
		return rand.Intn(hi-lo+1) + lo

	case BindRandomFloat:
		lo, hi := float64(fb.Min), float64(fb.Max)
		if lo >= hi {
			return lo
		}
		prec := fb.Precision
		if prec <= 0 {
			prec = 2
		}
		v := lo + rand.Float64()*(hi-lo)
		scale := math.Pow10(prec)
		return math.Round(v*scale) / scale

	case BindRandomBool:
		return rand.Intn(2) == 1

	case BindRandomString:
		length := fb.Length
		if length <= 0 {
			length = 8
		}
		charset := resolveRandomStringCharset(fb.Charset)
		return randomStringCharset(length, charset)

	case BindListSize:
		return ae.store.ListLen(fb.Source)

	case BindMap:
		return nil

	default:
		stresslog.Debug("[ACTION] 未知 binding type，按 fixed 处理",
			zap.String("type", fb.Type), zap.String("field", fb.Field))
		val = fb.Value
	}

	if fb.Path != "" && val != nil {
		val = navigatePath(val, fb.Path)
	}

	if fb.Wrap && val != nil {
		return []any{val}
	}

	return val
}

// navigatePath 按点分路径从嵌套 map/list 中提取值。
func navigatePath(v any, path string) any {
	return state.NavigatePath(v, path)
}

// navigatePathValues 按点分路径从嵌套 map/list 中提取一个或多个值。
// 与 navigatePath 不同，它只用于 filters，支持 [] 数组通配，不影响 binding.path / store.field 的单值语义。
func navigatePathValues(v any, path string) []any {
	if path == "" {
		return []any{v}
	}
	values := []any{v}
	for _, key := range state.SplitPath(path) {
		next := make([]any, 0, len(values))
		for _, current := range values {
			if current == nil {
				continue
			}
			if key == "[]" {
				if list, ok := current.([]any); ok {
					next = append(next, list...)
				}
				continue
			}
			switch c := current.(type) {
			case map[string]any:
				if val, ok := c[key]; ok {
					next = append(next, val)
				}
			case []any:
				idxStr := strings.Trim(key, "[]")
				var idx int
				_, err := fmt.Sscanf(idxStr, "%d", &idx)
				if err == nil && idx >= 0 && idx < len(c) {
					next = append(next, c[idx])
				}
			case state.PathNavigator:
				// 惰性导航值（wire-first 存储的 *protox.WireValue / Overlay 等）：
				// 逐段消费，保持与 map 分支一致的"存在才推进"语义。
				if val, found := c.NavigateSegs([]string{key}); found {
					next = append(next, val)
				}
			}
		}
		if len(next) == 0 {
			return nil
		}
		values = next
	}
	return values
}

// resolveAddress 解析 address 配置：支持 "state:key" 取 StateStore，否则按字面量
func (ae *ActionExecutor) resolveAddress(addr string) string {
	if strings.HasPrefix(addr, PrefixState) {
		key := addr[len(PrefixState):]
		return ae.store.GetString(key)
	}
	return addr
}

// execTCPConnect 建立 TCP 连接
func (ae *ActionExecutor) execTCPConnect(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return NewActionError(errcode.ErrAddrEmpty, "action="+def.Name+" service="+def.Service)
	}
	// 协作式 Class B：拨号阻塞至连接建立，在后台 goroutine 执行，等待窗口内执行器 drain mailbox。
	var err error
	if submitErr := ae.runIO(func() { err = ae.netSender.ConnectTCP(def.Service, addr) }); submitErr != nil {
		return submitErr
	}
	if err != nil {
		return err
	}
	stresslog.Debug("[ACTION] ConnectTCP 成功", zap.String("action", def.Name), zap.String("service", def.Service), zap.String("address", addr))
	return nil
}

// execUDPConnect 建立 UDP 连接
func (ae *ActionExecutor) execUDPConnect(def *ActionDef) error {
	addr := ae.resolveAddress(def.Address)
	if addr == "" {
		return NewActionError(errcode.ErrAddrEmpty, "action="+def.Name+" service="+def.Service)
	}
	// 协作式 Class B：拨号阻塞至连接建立，在后台 goroutine 执行，等待窗口内执行器 drain mailbox。
	var err error
	if submitErr := ae.runIO(func() { err = ae.netSender.ConnectUDP(def.Service, addr) }); submitErr != nil {
		return submitErr
	}
	if err != nil {
		return err
	}
	stresslog.Debug("[ACTION] ConnectUDP 成功", zap.String("action", def.Name), zap.String("service", def.Service), zap.String("address", addr))
	return nil
}

// execTCPClose 关闭 TCP 连接
func (ae *ActionExecutor) execTCPClose(def *ActionDef) error {
	ae.netSender.CloseTCP(def.Service)
	stresslog.Debug("[ACTION] TCPClose 成功", zap.String("action", def.Name), zap.String("service", def.Service))
	return nil
}

// execUDPClose 关闭 UDP 连接
func (ae *ActionExecutor) execUDPClose(def *ActionDef) error {
	ae.netSender.CloseUDP(def.Service)
	stresslog.Debug("[ACTION] UDPClose 成功", zap.String("action", def.Name), zap.String("service", def.Service))
	return nil
}

// execClearState 清除 StateStore 中的多个 key
func (ae *ActionExecutor) execClearState(def *ActionDef) error {
	if err := validateClearStateKeys(def.Name, def.Keys); err != nil {
		return NewActionError(errcode.ErrStateConfig, "action="+def.Name, err)
	}
	for _, key := range def.Keys {
		ae.store.Delete(key)
	}
	stresslog.Debug("[ACTION] ClearState 成功", zap.String("action", def.Name), zap.Strings("keys", def.Keys))
	return nil
}

// execSetState 从 bindings 设置 StateStore
func (ae *ActionExecutor) execSetState(def *ActionDef) error {
	for i := range def.Bindings {
		fb := &def.Bindings[i]

		if !ae.evaluateCondition(fb.Condition) {
			continue
		}

		val := ae.resolveFieldValue(fb)
		if val == nil {
			continue
		}
		ae.store.SetPath(fb.Field, val)
	}
	return nil
}

// execHTTPRequest HTTP 请求
func (ae *ActionExecutor) execHTTPRequest(def *ActionDef) (int, int, ActionTiming, error) {
	resolvedURL := ae.resolveAddress(def.URL)
	if resolvedURL == "" {
		return 0, 0, ActionTiming{}, NewActionError(errcode.ErrURLEmpty, "action="+def.Name)
	}

	method := def.Method
	if method == "" {
		method = "POST"
	}
	contentType := def.ContentType
	if contentType == "" {
		contentType = ContentJSON
	}

	var body []byte
	if len(def.Bindings) > 0 {
		switch contentType {
		case ContentJSON:
			bodyMap := make(map[string]any)
			for i := range def.Bindings {
				fb := &def.Bindings[i]
				if !ae.evaluateCondition(fb.Condition) {
					continue
				}
				val := ae.resolveFieldValue(fb)
				if val == nil {
					continue
				}
				bodyMap[fb.Field] = val
			}
			var err error
			body, err = json.Marshal(bodyMap)
			if err != nil {
				return 0, 0, ActionTiming{}, NewActionError(errcode.ErrMarshalBody, "action="+def.Name+" type=json", err)
			}
		case ContentForm:
			// application/x-www-form-urlencoded：直接编码为 k=v&k2=v2，
			// 而非 JSON（此前误用 json.Marshal 导致发送 {"k":"v"} 与 form 头不匹配）。
			formData := make(url.Values)
			for i := range def.Bindings {
				fb := &def.Bindings[i]
				if !ae.evaluateCondition(fb.Condition) {
					continue
				}
				val := ae.resolveFieldValue(fb)
				if val == nil {
					continue
				}
				formData.Set(fb.Field, fmt.Sprintf("%v", val))
			}
			body = []byte(formData.Encode())
		default:
			stresslog.Warn("[ACTION] 未知 contentType，将发送原始字节",
				zap.String("action", def.Name), zap.String("contentType", contentType))
		}
	}

	// 协作式 Class B：HTTP Do 在后台 goroutine 执行（往返可能较慢），等待窗口内执行器 drain
	// mailbox。后续 timing/store/parse 均在 runIO 返回后于执行器 goroutine 串行处理。
	var exchange *HTTPExchange
	var err error
	if submitErr := ae.runIO(func() {
		exchange, err = ae.netSender.HTTPRequest(resolvedURL, method, contentType, body)
	}); submitErr != nil {
		return 0, 0, ActionTiming{}, submitErr
	}
	if exchange == nil {
		exchange = &HTTPExchange{}
	}
	respBody := exchange.Body
	var timing ActionTiming
	if exchange.NetLatency > 0 {
		timing.AddRequest(RequestTiming{WireRTT: exchange.NetLatency})
	}
	if err != nil {
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, err
	}

	// 非 2xx 状态码视为请求失败
	if exchange.StatusCode < 200 || exchange.StatusCode >= 300 {
		// body 截断进日志（可能很长，不进 detail）
		bodyForLog := respBody
		if len(bodyForLog) > 512 {
			bodyForLog = bodyForLog[:512]
		}
		stresslog.Warn("[ACTION] HTTP 响应非 2xx",
			zap.String("action", def.Name),
			zap.String("url", resolvedURL), zap.String("method", method),
			zap.Int("statusCode", exchange.StatusCode),
			zap.Int("respBodyLen", len(respBody)),
			zap.ByteString("body", bodyForLog))
		// detail 只保留 status 文本（http.StatusText），不含 body
		hdetail := fmt.Sprintf("action=%s status=%d", def.Name, exchange.StatusCode)
		if statusText := http.StatusText(exchange.StatusCode); statusText != "" {
			hdetail += " " + statusText
		}
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, NewActionError(errcode.ErrHTTPStatus, hdetail)
	}

	if len(def.Store) > 0 && len(respBody) > 0 {
		if contentType == ContentJSON {
			var respMap map[string]any
			if err := json.Unmarshal(respBody, &respMap); err != nil {
				stresslog.Warn("[ACTION] HTTP 响应 JSON 解析失败",
					zap.String("action", def.Name),
					zap.Int("statusCode", exchange.StatusCode),
					zap.Error(err))
			} else {
				ae.storeResponse(def.Store, respMap)
			}
		}
	}

	stresslog.Debug("[ACTION] HTTPRequest 成功",
		zap.String("action", def.Name),
		zap.String("url", resolvedURL), zap.String("method", method),
		zap.Int("statusCode", exchange.StatusCode),
		zap.Int("reqBodyLen", len(body)), zap.Int("respBodyLen", len(respBody)),
		zap.Duration("netLatency", exchange.NetLatency))
	return exchange.SendWireBytes, exchange.RecvWireBytes, timing, nil
}

// parseAndStoreResponse 校验 S2C 响应字节并按 store 映射存储字段。
//
// wire-first（M1/M4）：不再为存储解码整条消息——留存形态是原始 wire 字节：
//   - 结构校验（protox.ValidateWire）取代「解码即校验」，保持与 proto.Unmarshal
//     相同的合法性判定（非法字节仍报 ErrParseFailed）；
//   - 整存映射（Field==""）存 *protox.WireValue（字节快照 + 惰性导航视图）；
//   - 路径映射（Field!=""）在 wire 上导航取值（语义与 GetFieldForStore 等价，
//     由影子验证守护），message 终端产出子 WireValue，经保留规划（PlanWireRetention）
//     决定复制小 span 还是共享整包快照；
//   - 影子验证失配的 schema 自动降级回旧解码路径（parseAndStoreDecoded）。
//
// 动作响应不走广播去重：请求-响应的内容逐机器人唯一（登录、各类查询带自身数据），
// 进共享缓存必然 miss 且会把真正复用的广播条目从 LRU 挤出去（线上 018→019 实测污染）。
func (ae *ActionExecutor) parseAndStoreResponse(def *ActionDef, respBody []byte) error {
	if len(def.Store) == 0 {
		return nil
	}

	if def.S2CProto == "" {
		for _, m := range def.Store {
			if m.Field == "" {
				ae.store.SetPath(m.Setter, respBody)
			}
		}
		return nil
	}

	md, ok := ae.factory.MessageDescriptor(def.S2CProto)
	if !ok {
		return NewActionError(errcode.ErrParseFailed, "action="+def.Name+" proto="+def.S2CProto+" 未找到消息类型")
	}
	if protox.WireDegraded(md) {
		return ae.parseAndStoreDecoded(def, respBody)
	}

	// respBody 可能是网络缓冲区视图，留存前必须独立快照（与旧路径 Parse 的读取时序等价安全）。
	snapshot := protox.WireSnapshot(respBody)
	if err := protox.ValidateWire(md, snapshot); err != nil {
		return NewActionError(errcode.ErrParseFailed, "action="+def.Name+" proto="+def.S2CProto, err)
	}

	stresslog.Debug("[ACTION] TCPResponseProto", zap.String("proto", def.S2CProto), zap.Int("bodyLen", len(respBody)))

	ae.storeResponseWire(def.Store, protox.NewWireValue(md, snapshot))
	return nil
}

// parseAndStoreDecoded 旧解码存储路径：影子验证失配降级的 schema 走这里
// （解码整条消息，整存 Freeze、路径 GetFieldForStore）。
func (ae *ActionExecutor) parseAndStoreDecoded(def *ActionDef, respBody []byte) error {
	respMsg, err := ae.factory.Parse(def.S2CProto, respBody)
	if err != nil {
		return NewActionError(errcode.ErrParseFailed, "action="+def.Name+" proto="+def.S2CProto, err)
	}
	ae.storeResponseProto(def.Store, respMsg)
	return nil
}

// storeResponseWire 从 wire 值按 store 映射取值并写入 state。
//
// 整存映射直接存 wv；路径映射经 wv 惰性导航（存在性/取值语义与解码路径等价）。
// 路径产物先过保留规划：无整存映射时，若全部子 span 字节和小于整包，逐个复制为
// 独立缓冲（不钉整包快照）；有整存映射时共享同一快照（本就常驻）。
func (ae *ActionExecutor) storeResponseWire(mappings []StoreMapping, wv *protox.WireValue) {
	debugOn := stresslog.DebugEnabled()
	hasWhole := false
	var pathSetters []string
	var pathVals []any
	for _, m := range mappings {
		if m.Field == "" {
			hasWhole = true
			continue
		}
		val, ok := wv.Navigate(m.Field)
		if !ok {
			if debugOn {
				stresslog.Debug("[ACTION] storeResponse 字段未找到",
					zap.String("field", m.Field), zap.String("setter", m.Setter))
			}
			continue
		}
		pathSetters = append(pathSetters, m.Setter)
		pathVals = append(pathVals, val)
	}
	protox.PlanWireRetention(pathVals, len(wv.Raw()), hasWhole)
	for i, setter := range pathSetters {
		ae.store.SetPath(setter, pathVals[i])
		if debugOn {
			stresslog.Debug("[ACTION] storeResponse 存储",
				zap.String("setter", setter), zap.String("type", fmt.Sprintf("%T", pathVals[i])))
		}
	}
	if hasWhole {
		for _, m := range mappings {
			if m.Field == "" {
				ae.store.SetPath(m.Setter, wv)
			}
		}
	}
}

// storeResponseProto 从 proto 响应消息按 store 映射取值并写入 state。
//
// Field != "" 的映射直接 GetFieldForStore 按路径取值，不展开无关的大字段（如未被引用的
// repeated 列表），目标子树保持可变 map（支持配置子路径覆写与脚本改写）。
// GetFieldForStore 与 navigatePath(GetFieldMap) 语义等价（见其 godoc 及
// TestGetFieldForStoreEquivalence）。
//
// Field == "" 的整存映射存不可变 protox.Frozen 引用（P1a）：不再 GetFieldMap 展开成
// map[string]any 装箱树常驻 state（大消息 × 数千机器人 = GB 级常驻），路径读取经
// state.PathNavigator 惰性取值，Lua robot.get 在边界现场转真 table（脚本语义不变）。
// respMsg 解码后仅经本方法存入 state、无后续写方，满足 Freeze 的不可变契约。
func (ae *ActionExecutor) storeResponseProto(mappings []StoreMapping, respMsg proto.Message) {
	debugOn := stresslog.DebugEnabled()
	var frozen *protox.Frozen
	for _, m := range mappings {
		if m.Field == "" {
			if frozen == nil {
				frozen = protox.Freeze(respMsg)
			}
			ae.store.SetPath(m.Setter, frozen)
			continue
		}
		val, ok := ae.factory.GetFieldForStore(respMsg, m.Field)
		if !ok {
			if debugOn {
				stresslog.Debug("[ACTION] storeResponse 字段未找到",
					zap.String("field", m.Field), zap.String("setter", m.Setter))
			}
			continue
		}
		ae.store.SetPath(m.Setter, val)
		if debugOn {
			stresslog.Debug("[ACTION] storeResponse 存储",
				zap.String("field", m.Field), zap.String("setter", m.Setter),
				zap.String("type", fmt.Sprintf("%T", val)))
		}
	}
}

// handleHeaderError 处理服务端返回的非零 headerErr：解析响应、构造错误描述。
// 将 4 处重复的 headerErr 处理逻辑统一收敛到此方法。
//
// T2-C2 起 DescribeError 按 def.Service + pattern(proto) 推 server 串 Resolve 取 adapter。
// Resolve nil 时 DescribeError 返回空串（与未配置 errors.json 等价），不在此 fail loud——
// headerErr 描述缺失不致命，仅 detail 不含人类可读前缀；上层仍按 NewActionError 上抛原错误码。
func (ae *ActionExecutor) handleHeaderError(proto string, def *ActionDef, headerErr uint64, routeKey string, respBody []byte) *ActionError {
	ae.parseAndStoreResponse(def, respBody)
	desc := ae.describeError(proto, def.Service, headerErr)
	detail := "service=" + def.Service + " route=" + routeKey
	if desc != "" {
		detail = desc + ": " + detail
	}
	return NewActionError(errcode.ErrorCode(headerErr), detail)
}

// storeResponse 将响应字段存储到 StateStore
func (ae *ActionExecutor) storeResponse(mappings []StoreMapping, fieldMap map[string]any) {
	debugOn := stresslog.DebugEnabled()
	if debugOn {
		stresslog.Debug("[ACTION] storeResponse",
			zap.Int("mappingCount", len(mappings)), zap.Int("fieldCount", len(fieldMap)))
	}

	for _, m := range mappings {
		if m.Field == "" {
			ae.store.SetPath(m.Setter, fieldMap)
		} else {
			val := navigatePath(fieldMap, m.Field)
			if val != nil {
				ae.store.SetPath(m.Setter, val)
				if debugOn {
					stresslog.Debug("[ACTION] storeResponse 存储",
						zap.String("field", m.Field), zap.String("setter", m.Setter),
						zap.String("type", fmt.Sprintf("%T", val)))
				}
			} else if debugOn {
				stresslog.Debug("[ACTION] storeResponse 字段未找到",
					zap.String("field", m.Field), zap.String("setter", m.Setter))
			}
		}
	}
}

// applyFilters 按过滤条件筛选列表项
func (ae *ActionExecutor) applyFilters(list []any, filters []FilterDef) []any {
	if len(filters) == 0 {
		return list
	}
	result := make([]any, 0, len(list))
	for _, item := range list {
		if ae.matchFilters(item, filters) {
			result = append(result, item)
		}
	}
	return result
}

// matchFilters 检查列表项是否满足所有过滤条件
func (ae *ActionExecutor) matchFilters(item any, filters []FilterDef) bool {
	for _, f := range filters {
		fieldVals := navigatePathValues(item, f.Path)

		var targetVal any
		if f.Source != "" {
			targetVal = ae.store.Get(f.Source)
		} else {
			targetVal = f.Value
		}

		if !matchFilterValues(fieldVals, targetVal, f.Op, f.Mode) {
			return false
		}
	}
	return true
}

// matchFilterValues 按 mode 聚合多个 path 取值的比较结果。
func matchFilterValues(values []any, targetVal any, op string, mode string) bool {
	if len(values) == 0 {
		return mode == FilterModeNone
	}

	switch mode {
	case FilterModeAll:
		for _, v := range values {
			if !state.CompareValues(v, targetVal, op) {
				return false
			}
		}
		return true
	case FilterModeNone:
		for _, v := range values {
			if state.CompareValues(v, targetVal, op) {
				return false
			}
		}
		return true
	case FilterModeAny, "":
		for _, v := range values {
			if state.CompareValues(v, targetVal, op) {
				return true
			}
		}
		return false
	default:
		for _, v := range values {
			if state.CompareValues(v, targetVal, op) {
				return true
			}
		}
		return false
	}
}

// evaluateCondition 评估条件表达式（仅 state: 前缀）。返回 true 表示条件满足（或无条件）。
func (ae *ActionExecutor) evaluateCondition(cond string) bool {
	if cond == "" {
		return true
	}
	return EvalCondition(cond, ae.store)
}

// pickN 从列表中随机选择 N 个不重复元素
func pickN(list []any, n int) []any {
	if n >= len(list) {
		result := make([]any, len(list))
		copy(result, list)
		rand.Shuffle(len(result), func(i, j int) {
			result[i], result[j] = result[j], result[i]
		})
		return result
	}
	result := make([]any, 0, n)
	used := make(map[int]bool, n)
	for len(result) < n {
		idx := rand.Intn(len(list))
		if !used[idx] {
			used[idx] = true
			result = append(result, list[idx])
		}
	}
	return result
}

// ── 协议策略辅助方法 ─────────────────────────────────────────────────

// protocolSend sends a packet via TCP or UDP, returning send bytes.
func (ae *ActionExecutor) protocolSend(protocol, service string, packet []byte) (int, error) {
	if protocol == "udp" {
		return ae.netSender.UDPSend(service, packet)
	}
	return ae.netSender.TCPSend(service, packet)
}

// protocolEncode encodes a packet via TCP or UDP adapter resolved by "<proto>:<service>".
//
// proto 由调用方按 pattern 推导（"tcp" / "udp"）；service 来自 ActionDef.Service。
// Resolve nil（该连接未配置 codec）时返回 nil，调用方（execSend / execRequest）必须将其
// 翻译为 ErrEncodeFailed fail loud（不静默兜底）。
func (ae *ActionExecutor) protocolEncode(proto, service string, route any, body, key []byte) []byte {
	adp := ae.resolveAdapter(proto, service)
	if adp == nil {
		return nil
	}
	if proto == "udp" {
		return adp.EncodeUDP(route, body, key)
	}
	return adp.EncodeTCP(route, body, key)
}

// resolveAdapter 按 "<proto>:<service>" 从 resolver 解析该连接的 codec adapter。
// proto 必须是 "tcp" / "udp"；非空 service 由调用方保证（ActionDef.Service 已校验）。
// 未映射返回 nil，调用方 fail loud。
func (ae *ActionExecutor) resolveAdapter(proto, service string) adapter.Adapter {
	if ae.resolver == nil {
		return nil
	}
	return ae.resolver.Resolve(proto + ":" + service)
}

// expectedRouteKey 按 "<proto>:<service>" Resolve 出 adapter 后计算 routeKey。
// Resolve nil 时无法计算 routeKey，返回空串；正常请求路径应已在 encode 阶段
// 因缺 codec fail-loud。该返回值仅作为防御性兜底。
func (ae *ActionExecutor) expectedRouteKey(proto, service string, route any) string {
	adp := ae.resolveAdapter(proto, service)
	if adp == nil {
		return ""
	}
	return adp.ExpectedRouteKey(route)
}

// describeError 按 "<proto>:<service>" Resolve 出 adapter 后描述 headerErr。
// Resolve nil 时返回空串（与 errors.json 未配置等价，非致命）。
func (ae *ActionExecutor) describeError(proto, service string, code uint64) string {
	adp := ae.resolveAdapter(proto, service)
	if adp == nil {
		return ""
	}
	return adp.DescribeError(code)
}

// protocolSecretKey returns the encryption key for the given protocol.
func (ae *ActionExecutor) protocolSecretKey(protocol, service string) []byte {
	if protocol == "udp" {
		return ae.netSender.GetUDPSecretKey(service)
	}
	return ae.netSender.GetTCPSecretKey(service)
}

// protocolRequest sends a request and waits for response.
func (ae *ActionExecutor) protocolRequest(protocol, service string, packet []byte, routeKey string, timeout ...time.Duration) (*NetExchange, error) {
	if protocol == "udp" {
		return ae.netSender.UDPRequest(service, packet, routeKey, timeout...)
	}
	return ae.netSender.TCPRequest(service, packet, routeKey, timeout...)
}

// protocolListenResp gets the latest listen response.
func (ae *ActionExecutor) protocolListenResp(protocol, service, routeKey string) *NetExchange {
	if protocol == "udp" {
		return ae.netSender.GetUDPListenResp(service, routeKey)
	}
	return ae.netSender.GetTCPListenResp(service, routeKey)
}

// ── 统一执行方法 ─────────────────────────────────────────────────────

// execSend sends a message without waiting for response.
func (ae *ActionExecutor) execSend(protocol string, def *ActionDef) (int, ActionTiming, error) {
	body, err := ae.buildBody(def)
	if err != nil {
		return 0, ActionTiming{}, err
	}

	routeKey := ae.expectedRouteKey(protocol, def.Service, def.Route)
	secretKey := ae.protocolSecretKey(protocol, def.Service)
	var encodeStart time.Time
	if ae.timingLevel >= TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := ae.protocolEncode(protocol, def.Service, def.Route, body, secretKey)
	var timing ActionTiming
	if ae.timingLevel >= TimingLevelCodec && !encodeStart.IsZero() {
		timing.Client.EncodeCost = time.Since(encodeStart)
	}
	if packet == nil {
		return 0, timing, NewActionError(errcode.ErrEncodeFailed,
			"action="+def.Name+" service="+def.Service+" route="+routeKey+
				"；codec 未映射（resolver.Resolve("+protocol+":"+def.Service+") nil）")
	}

	sendStart := time.Now()
	n, err := ae.protocolSend(protocol, def.Service, packet)
	timing.Client.SendCost = time.Since(sendStart)
	if err != nil {
		return 0, timing, err
	}

	label := "TCPSend"
	if protocol == "udp" {
		label = "UDPSend"
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[ACTION] 发送完成",
			zap.String("action", def.Name), zap.String("service", def.Service),
			zap.String("transport", protocol), zap.String("pattern", label), zap.String("routeKey", routeKey),
			zap.String("c2sProto", def.C2SProto), zap.Int("bodyLen", len(body)), zap.Int("pktLen", n))
	}
	return len(packet), timing, nil
}

// execRequest sends a request and waits for response.
func (ae *ActionExecutor) execRequest(protocol string, def *ActionDef) (int, int, ActionTiming, error) {
	var buildStart time.Time
	if ae.timingLevel >= TimingLevelFull {
		buildStart = time.Now()
	}
	body, err := ae.buildBody(def)
	var buildCost time.Duration
	if ae.timingLevel >= TimingLevelFull && !buildStart.IsZero() {
		buildCost = time.Since(buildStart)
	}
	if err != nil {
		return 0, 0, ActionTiming{Client: ClientTiming{BuildCost: buildCost}}, err
	}

	routeKey := ae.expectedRouteKey(protocol, def.Service, def.Route)
	label := "TCPRequest"
	if protocol == "udp" {
		label = "UDPRequest"
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[ACTION] 请求发送",
			zap.String("action", def.Name), zap.String("service", def.Service),
			zap.String("transport", protocol), zap.String("pattern", label), zap.String("routeKey", routeKey),
			zap.String("c2sProto", def.C2SProto), zap.String("s2cProto", def.S2CProto),
			zap.Int("bodyLen", len(body)))
	}

	secretKey := ae.protocolSecretKey(protocol, def.Service)
	var encodeStart time.Time
	if ae.timingLevel >= TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := ae.protocolEncode(protocol, def.Service, def.Route, body, secretKey)
	var encodeCost time.Duration
	if ae.timingLevel >= TimingLevelCodec && !encodeStart.IsZero() {
		encodeCost = time.Since(encodeStart)
	}
	if packet == nil {
		return 0, 0, ActionTiming{Client: ClientTiming{BuildCost: buildCost, EncodeCost: encodeCost}}, NewActionError(errcode.ErrEncodeFailed,
			"action="+def.Name+" service="+def.Service+" route="+routeKey+
				"；codec 未映射（resolver.Resolve("+protocol+":"+def.Service+") nil）")
	}

	var reqTimeout []time.Duration
	if def.Timeout > 0 {
		reqTimeout = append(reqTimeout, time.Duration(def.Timeout)*time.Second)
	}
	exchange, err := ae.protocolRequest(protocol, def.Service, packet, routeKey, reqTimeout...)
	if exchange == nil {
		exchange = &NetExchange{SendWireBytes: len(packet)}
	}
	respBody := exchange.Body
	var timing ActionTiming
	timing.Client.BuildCost += buildCost
	timing.Client.EncodeCost += encodeCost
	timing.AddRequest(exchange.Timing)
	if err != nil {
		// 请求层失败（超时 / 断连 / 发送失败）：没有响应帧就没有 WireRTT，
		// 但必须进 RTT Apdex 的分母记 frustrated，否则最慢的样本集体缺席。
		timing.AddFailedRequest()
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, err
	}

	if exchange.HeaderErr != 0 {
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, ae.handleHeaderError(protocol, def, exchange.HeaderErr, routeKey, respBody)
	}

	var parseStart time.Time
	if ae.timingLevel >= TimingLevelFull {
		parseStart = time.Now()
	}
	if err := ae.parseAndStoreResponse(def, respBody); err != nil {
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, err
	}
	if ae.timingLevel >= TimingLevelFull && !parseStart.IsZero() {
		timing.Client.ParseStoreCost += time.Since(parseStart)
	}

	if stresslog.DebugEnabled() {
		stresslog.Debug("[ACTION] 请求成功",
			zap.String("action", def.Name), zap.String("service", def.Service),
			zap.String("transport", protocol), zap.String("pattern", label), zap.String("routeKey", routeKey),
			zap.String("s2cProto", def.S2CProto),
			zap.Int("respBodyLen", len(respBody)), zap.Duration("wireRTT", exchange.Timing.WireRTT))
	}
	return exchange.SendWireBytes, exchange.RecvWireBytes, timing, nil
}

// execListen 轮询消费已通过 ListenRefs 预缓存的推送消息。
// 不再自行注册监听；若对应 route 未在前驱节点通过 listenRefs 预注册，将始终超时。
func (ae *ActionExecutor) execListen(ctx context.Context, protocol string, def *ActionDef) (int, ActionTiming, error) {
	timeout := def.Timeout
	if timeout <= 0 {
		timeout = DefaultListenTimeoutSec
	}
	pollMs := def.PollMs
	if pollMs <= 0 {
		pollMs = DefaultPollMs
	}

	routeKey := ae.expectedRouteKey(protocol, def.Service, def.Route)
	label := "TCPListen"
	if protocol == "udp" {
		label = "UDPListen"
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[ACTION] 监听开始",
			zap.String("action", def.Name), zap.String("service", def.Service),
			zap.String("transport", protocol), zap.String("pattern", label), zap.String("routeKey", routeKey),
			zap.String("s2cProto", def.S2CProto),
			zap.Int("timeoutSec", timeout), zap.Int("pollMs", pollMs))
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	start := time.Now()
	pollCount := 0

	for time.Now().Before(deadline) {
		if ctx != nil && ctx.Err() != nil {
			return 0, ActionTiming{}, ctx.Err()
		}
		pollCount++
		exchange := ae.protocolListenResp(protocol, def.Service, routeKey)
		if exchange != nil {
			respBody := exchange.Body
			var timing ActionTiming
			// 等待时长以帧被内核收到的时刻为终点，而非本轮轮询发现它的时刻——
			// 否则测出来的是「轮询间隔的取整」，pollMs 越大偏得越多。
			timing.AddListenHit(ClassifyListenWait(start, exchange.RecvFrameAt))
			if exchange.HeaderErr != 0 {
				return exchange.RecvWireBytes, timing, ae.handleHeaderError(protocol, def, exchange.HeaderErr, routeKey, respBody)
			}
			var parseStart time.Time
			if ae.timingLevel >= TimingLevelFull {
				parseStart = time.Now()
			}
			if err := ae.parseAndStoreResponse(def, respBody); err != nil {
				return exchange.RecvWireBytes, timing, err
			}
			if ae.timingLevel >= TimingLevelFull && !parseStart.IsZero() {
				timing.Client.ParseStoreCost += time.Since(parseStart)
			}
			if stresslog.DebugEnabled() {
				stresslog.Debug("[ACTION] 监听成功",
					zap.String("action", def.Name), zap.String("service", def.Service),
					zap.String("transport", protocol), zap.String("pattern", label), zap.String("routeKey", routeKey),
					zap.String("s2cProto", def.S2CProto),
					zap.Int("respBodyLen", len(respBody)),
					zap.Int("pollCount", pollCount))
			}
			return exchange.RecvWireBytes, timing, nil
		}
		// 协作式休眠：等待窗口内 drain Robot mailbox（跑 listen 回调），不饿死协作式工作。
		// ctx 取消时提前返回（下一轮 ctx 检查也会兜底）。
		if err := ae.sleep(ctx, time.Duration(pollMs)*time.Millisecond); err != nil {
			return 0, ActionTiming{}, err
		}
	}

	elapsed := time.Since(start)
	var timeoutTiming ActionTiming
	timeoutTiming.AddListenTimeout()
	return 0, timeoutTiming, NewActionError(errcode.ErrListenTimeout,
		"action="+def.Name+" service="+def.Service+" route="+routeKey+
			fmt.Sprintf(" timeout=%ds polls=%d elapsed=%v", timeout, pollCount, elapsed)+
			"；route 未通过 listenRefs 预注册，请在前驱节点添加 listenRefs 并设置 listen=null")
}

// resolveRandomStringCharset 将字符集别名解析为实际字符池，未知非空值按自定义字符集处理。
func resolveRandomStringCharset(charset string) string {
	charset = strings.TrimSpace(charset)
	if charset == "" {
		return randomStringCharsetAlphanum
	}
	if resolved, ok := randomStringCharsetAliases[charset]; ok {
		return resolved
	}
	return charset
}

// randomStringCharset 生成指定字符集的随机字符串
func randomStringCharset(length int, charset string) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
