package script

import (
	"fmt"
	"math"
	"net/url"
	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/protox"
	stresslog "stressbot/utils/log"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// loadNetworkModule 加载 network 命名空间模块。
// Lua 用法：
//
//	local network = require("network")
//	network.connect_tcp(service, address)     → 建立 TCP 连接
//	network.connect_udp(service, address)     → 建立 UDP 连接
//	network.close_tcp(service)                → 关闭 TCP 连接
//	network.close_udp(service)                → 关闭 UDP 连接
//	network.tcp_request(service, route, msg [, s2c])
//	network.tcp_request_route(service, request_route, response_route, msg [, s2c])
//	network.udp_request(service, route, body [, s2c [, timeout]])
//	network.udp_request_route(service, request_route, response_route, body [, s2c])
//	network.http_request(url [, method [, content_type [, body]]])
//	network.tcp_send(service, route, msg)
//	network.udp_send(service, route, body)
//	network.tcp_listen(service, route [, s2c [, timeout]])
//	network.udp_listen(service, route [, s2c [, timeout]])
//	network.try_tcp_listen(service, route) → err, data  (非阻塞单次 pop)
//	network.try_udp_listen(service, route) → err, data  (非阻塞单次 pop)
//	network.set_tcp_secret_key(service, key)
//	network.set_udp_secret_key(service, key)
//	network.get_tcp_secret_key(service)
//	network.get_udp_secret_key(service)
//	network.ensure_tcp_listener(service, response_key)
//	network.ensure_udp_listener(service, response_key)
func loadNetworkModule(L *lua.LState) int {
	mod := L.NewTable()

	// 连接
	L.SetField(mod, "connect_tcp", L.NewFunction(networkConnectTCP))
	L.SetField(mod, "connect_udp", L.NewFunction(networkConnectUDP))
	L.SetField(mod, "close_tcp", L.NewFunction(networkCloseTCP))
	L.SetField(mod, "close_udp", L.NewFunction(networkCloseUDP))
	// 请求-响应
	// 请求-响应：协作式实现（actor 运行时统一约束：会等待的点全部 yield 让出，
	// 等待窗口内调度器 drain mailbox）。无独立"阻塞版"，故无 await_ 前缀别名。
	L.SetField(mod, "tcp_request", L.NewFunction(networkAwaitTCPRequest))
	L.SetField(mod, "tcp_request_route", L.NewFunction(networkAwaitTCPRequestRoute))
	L.SetField(mod, "udp_request", L.NewFunction(networkAwaitUDPRequest))
	L.SetField(mod, "udp_request_route", L.NewFunction(networkAwaitUDPRequestRoute))
	L.SetField(mod, "http_request", L.NewFunction(networkHTTPRequest))
	// 发送
	L.SetField(mod, "tcp_send", L.NewFunction(networkTCPSend))
	L.SetField(mod, "udp_send", L.NewFunction(networkUDPSend))
	// 监听
	// 监听：协作式实现（等待窗口内调度器 drain mailbox）。无独立"阻塞版"，故无 await_ 前缀别名。
	L.SetField(mod, "tcp_listen", L.NewFunction(networkAwaitTCPListen))
	L.SetField(mod, "udp_listen", L.NewFunction(networkAwaitUDPListen))
	// 非阻塞单次 pop：取最近一条缓存消息的原始 body
	L.SetField(mod, "try_tcp_listen", L.NewFunction(networkTryTCPListen))
	L.SetField(mod, "try_udp_listen", L.NewFunction(networkTryUDPListen))
	// 密钥
	L.SetField(mod, "set_tcp_secret_key", L.NewFunction(networkSetTCPSecretKey))
	L.SetField(mod, "set_udp_secret_key", L.NewFunction(networkSetUDPSecretKey))
	L.SetField(mod, "get_tcp_secret_key", L.NewFunction(networkGetTCPSecretKey))
	L.SetField(mod, "get_udp_secret_key", L.NewFunction(networkGetUDPSecretKey))
	// 监听器占位
	L.SetField(mod, "ensure_tcp_listener", L.NewFunction(networkEnsureTCPListener))
	L.SetField(mod, "ensure_udp_listener", L.NewFunction(networkEnsureUDPListener))

	L.Push(mod)
	return 1
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// headerErrDetail 拼 HeaderErr 的 detail：优先经 CodecResolver 取服务端错误码描述，
// 再附 service/route 上下文。描述缺失非致命——HeaderErr 错误码本身仍透传给调用方。
func headerErrDetail(ctx *Context, service string, code uint64, service2, routeKey string) string {
	detail := "service=" + service2 + " route=" + routeKey
	desc := resolveDescribeError(ctx, "tcp:"+service, code)
	if desc == "" {
		desc = resolveDescribeError(ctx, "udp:"+service, code)
	}
	if desc != "" {
		return desc + ": " + detail
	}
	return detail
}

// resolveDescribeError 经 CodecResolver 取指定连接的 adapter 后 DescribeError。
//
// Resolver nil、serverHint 为空或未命中时返回空串。headerErr 描述只是增强信息，
// 缺失非致命：错误码本身仍会被构造成 ActionError 上抛；与 engine.handleHeaderError 一致。
//
// serverHint 建议传该 Robot 的主连接（如 "tcp:logic"），命中率高；当前 Resolver 不支持枚举全部 server。
func resolveDescribeError(ctx *Context, serverHint string, code uint64) string {
	if ctx == nil || ctx.Resolver == nil {
		return ""
	}
	// 优先用 hint（通常为该 Robot 实际建立的连接，必然有 codec）。
	if serverHint != "" {
		if adp := ctx.Resolver.Resolve(serverHint); adp != nil {
			return adp.DescribeError(code)
		}
	}
	// hint 未命中：Resolver 不暴露枚举，且生产中 errors.json 全局同源，任一 adapter 即可。
	// 这里不做穷举——hint 为空或未命中说明配置异常，headerErr 描述降级为空串（错误码本身仍上抛）。
	return ""
}

// resolveRequestTimeoutSec 决定 Lua tcp_request / udp_request 的 timeout（秒）。
//
// 优先级（高到低）：
//  1. Lua 调用显式传入的第 5 个参数（始终最高优先级，便于脚本临时覆盖）
//  2. ctx.DefaultRequestTimeout（来自 robotConfig.timeoutSec，集中配置）
//  3. engine.DefaultRequestTimeoutSec（硬编码兜底，仅在 ctx 未注入时触发）
//
// 把这个逻辑收敛到一处，避免 TCP/UDP 两个 API 各写一遍导致漂移。
func resolveRequestTimeoutSec(L *lua.LState, ctx *Context) int {
	return resolveRequestTimeoutSecAt(L, ctx, 5)
}

// resolveRequestTimeoutSecAt 从指定参数位置读取 timeout（秒）。
// 新的 *_request_route API 多了 response_route 参数，timeout 位于第 6 个参数。
func resolveRequestTimeoutSecAt(L *lua.LState, ctx *Context, argIndex int) int {
	if argIndex > 0 && L.GetTop() >= argIndex && L.Get(argIndex) != lua.LNil {
		return L.CheckInt(argIndex)
	}
	if ctx != nil && ctx.DefaultRequestTimeout > 0 {
		return int(ctx.DefaultRequestTimeout / time.Second)
	}
	return engine.DefaultRequestTimeoutSec
}

// extractNetArgs 从 Lua 栈提取 service + route + msg + s2cProto。
func extractNetArgs(L *lua.LState) (service string, route lua.LValue, msg proto.Message, s2cProto string) {
	argIdx := 0
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			if pm, _, _ := unwrapProtoUD(v); pm != nil {
				msg = pm
			}
			continue
		}
		argIdx++
		switch argIdx {
		case 1:
			service = lua.LVAsString(v)
		case 2:
			route = v
		default:
			if s2cProto == "" {
				s2cProto = lua.LVAsString(v)
			}
		}
	}
	return
}

// buildPacket 构建完整 TCP 数据包。
//
// 走 CodecResolver：按 "tcp:"+service Resolve 出该连接的 Go SchemaAdapter 后 EncodeTCP
// （与 engine.ActionExecutor / 心跳 goBuilder 共享同一份 codec 映射，encode 双向一致）。
// Resolve nil（连接 codec 未映射）→ 返回 nil，调用方（doTCPRequest / networkTCPSend）fail loud
// （ErrEncodeFailed，detail 带 service 串，不静默兜底）。
func buildPacket(ctx *Context, service string, route lua.LValue, msgData []byte) []byte {
	if ctx == nil || ctx.Resolver == nil {
		return nil
	}
	adp := ctx.ResolveAdapter("tcp", service)
	if adp == nil {
		return nil
	}
	secretKey := ctx.NetSender.GetTCPSecretKey(service)
	goRoute := luaValueToRoute(route)
	return adp.EncodeTCP(goRoute, msgData, secretKey)
}

// luaValueToRoute 将 Lua table/nil/number/string 转换为 Go any。
// 支持 table 嵌套（递归转换为 map[string]any）。
func luaValueToRoute(v lua.LValue) any {
	switch v := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LNumber:
		n := float64(v)
		if n == math.Trunc(n) {
			return int64(n)
		}
		return n
	case lua.LString:
		return string(v)
	case lua.LBool:
		return bool(v)
	case *lua.LTable:
		result := make(map[string]any)
		v.ForEach(func(k, val lua.LValue) {
			key := lua.LVAsString(k)
			result[key] = luaValueToRoute(val)
		})
		return result
	default:
		return nil
	}
}

// serializeMsg 序列化 proto 消息为字节。
func serializeMsg(ctx *Context, msg proto.Message) ([]byte, error) {
	if msg == nil || ctx.Factory == nil {
		return nil, nil
	}
	return ctx.Factory.Serialize(msg)
}

// ---------------------------------------------------------------------------
// 连接
// ---------------------------------------------------------------------------

// networkConnectTCP 建立 TCP 连接。
// 签名：network.connect_tcp(service, address)
//
// 返回：err
// nil 成功 / err table 失败（code=6 取消 / code=2 连接关闭 等）。
func networkConnectTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	return awaitConnect(L, ctx, "tcp", service, address)
}

// networkConnectUDP 建立 UDP 连接。
// 签名：network.connect_udp(service, address)
//
// 返回：err
// nil 成功 / err table 失败（code=6 取消 / code=2 连接关闭 等）。
func networkConnectUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	return awaitConnect(L, ctx, "udp", service, address)
}

// awaitConnect 协作式拨号（Class B）：拨号阻塞至连接建立，故在后台协程执行，等待窗口内执行器
// 持续 drain 任务队列。拨号前后的 ctx 取消判定与错误转 err table 在 renderer 内做
// （执行器 goroutine，Context 非并发安全）。返回 err：lua.LNil（成功）/ err table（失败）。
func awaitConnect(L *lua.LState, ctx *Context, proto, service, address string) int {
	// 拨号前已取消：无需投递后台作业，直接返回取消 err table。
	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		return pushErr(L, int(errcode.ErrActionCanceled), "service="+service+" address="+address)
	}
	return awaitIO(L, "network.connect_"+proto, func() IORenderer {
		var err error
		if proto == "udp" {
			err = ctx.NetSender.ConnectUDP(service, address)
		} else {
			err = ctx.NetSender.ConnectTCP(service, address)
		}
		return func(L *lua.LState) []lua.LValue {
			if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
				return []lua.LValue{newErrTable(L, int(errcode.ErrActionCanceled), "service="+service+" address="+address)}
			}
			if err != nil {
				return []lua.LValue{errTableFromActionErr(L, err)}
			}
			return []lua.LValue{lua.LNil}
		}
	})
}

// networkCloseTCP 关闭 TCP 连接。
// 签名：network.close_tcp(service)
func networkCloseTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	ctx.NetSender.CloseTCP(service)
	return 0
}

// networkCloseUDP 关闭 UDP 连接。
// 签名：network.close_udp(service)
func networkCloseUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	ctx.NetSender.CloseUDP(service)
	return 0
}

// ---------------------------------------------------------------------------
// 请求-响应
// ---------------------------------------------------------------------------

// requestResultValues 把一次请求-响应等待的结果（命中/服务端错误/请求错误/取消）转成
// Lua 返回值 (err, data)。err 为 lua.LNil（成功）或 err table（失败）。
// 协作式 await_*_request 的 drive-loop 用。
func requestResultValues(L *lua.LState, ctx *Context, spec *WaitSpec, outcome WaitOutcome) []lua.LValue {
	pktLen := len(spec.Packet)
	ex := outcome.Exchange
	if ex == nil {
		ex = &engine.NetExchange{SendWireBytes: pktLen}
	}
	ctx.recordRequest(ex.Timing)
	ctx.recordBytes(ex.SendWireBytes, ex.RecvWireBytes)
	respBody := ex.Body

	if outcome.Canceled {
		return []lua.LValue{
			newErrTable(L, int(errcode.ErrActionCanceled), "service="+spec.Service+" route="+spec.RouteKey),
			lua.LNil,
		}
	}
	if outcome.Err != nil {
		// 请求层失败（超时 / 断连 / 发送失败）：没有响应帧就没有 WireRTT，
		// 但必须进 RTT Apdex 的分母记 frustrated，否则最慢的样本集体缺席。
		// 上面的 Canceled 分支已提前返回——客户端主动取消与服务端表现无关，不计。
		ctx.recordRequestFailure()
		return []lua.LValue{errTableFromActionErr(L, outcome.Err), lua.LNil}
	}
	if ex.HeaderErr != 0 {
		return []lua.LValue{
			newErrTable(L, int(ex.HeaderErr), headerErrDetail(ctx, spec.Service, ex.HeaderErr, spec.Service, spec.RouteKey)),
			lua.LString(string(respBody)),
		}
	}
	if spec.S2CProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		var parseStart time.Time
		if ctx.TimingLevel >= engine.TimingLevelFull {
			parseStart = time.Now()
		}
		respMsg, err := ctx.Factory.Parse(spec.S2CProto, respBody)
		if ctx.TimingLevel >= engine.TimingLevelFull && !parseStart.IsZero() {
			ctx.recordClientTiming(engine.ClientTiming{ParseStoreCost: time.Since(parseStart), Observed: engine.TimingStageParseStore})
		}
		if err != nil {
			return []lua.LValue{
				newErrTable(L, int(errcode.ErrParseFailed), "service="+spec.Service+" route="+spec.RouteKey),
				lua.LString(string(respBody)),
			}
		}
		// 携带 body 独立快照（wire-first）：脚本 robot.set(resp) 未改写时直接以该快照
		// 存 WireValue，免重编码免解码树常驻。respBody 可能是网络缓冲区视图，必须复制。
		return []lua.LValue{lua.LNil, wrapRespMessage(L, respMsg, protox.WireSnapshot(respBody))}
	}
	return []lua.LValue{lua.LNil, lua.LString(string(respBody))}
}

// networkAwaitTCPRequest 实现 network.tcp_request（协作式 TCP 请求-响应）：构建请求包后 yield，
// 等待窗口内调度器可 drain 任务队列；响应到达经通道 select 即时唤醒，RTT 使用底层消息时间戳。
// 签名：network.tcp_request(service, route, msg [, s2c_proto [, timeout_sec]])
func networkAwaitTCPRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}
	service, route, msg, s2cProto := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.tcp_request requires (service, route, msg [, s2c_proto [, timeout_sec]])")
		return 0
	}
	return doAwaitTCPRequest(L, ctx, service, route, route, msg, s2cProto, resolveRequestTimeoutSec(L, ctx))
}

func doAwaitTCPRequest(L *lua.LState, ctx *Context, service string, requestRoute, responseRoute lua.LValue, msg proto.Message, s2cProto string, timeout int) int {
	msgData, err := serializeMsg(ctx, msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}
	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := buildPacket(ctx, service, requestRoute, msgData)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart), Observed: engine.TimingStageEncode})
	}
	if packet == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service), lua.LNil)
	}
	tcpAdp := ctx.ResolveAdapter("tcp", service)
	if tcpAdp == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service+" routeKey 解析失败（codec 未映射）"), lua.LNil)
	}
	routeKey := tcpAdp.ExpectedRouteKey(luaValueToRoute(responseRoute))

	return awaitYield(L, "tcp_request", &WaitSpec{
		Kind:     WaitResponse,
		Duration: time.Duration(timeout) * time.Second,
		Proto:    "tcp",
		Service:  service,
		RouteKey: routeKey,
		S2CProto: s2cProto,
		Packet:   packet,
	})
}

// networkAwaitUDPRequest 实现 network.udp_request（协作式 UDP 请求-响应）。
// 签名：network.udp_request(service, route, body [, s2c_proto [, timeout_sec]])
func networkAwaitUDPRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	route := L.Get(2)
	var body []byte
	if L.GetTop() >= 3 {
		body = []byte(L.CheckString(3))
	}
	s2cProto := ""
	if L.GetTop() >= 4 {
		s2cProto = L.CheckString(4)
	}
	return doAwaitUDPRequest(L, ctx, service, route, route, body, s2cProto, resolveRequestTimeoutSec(L, ctx))
}

func doAwaitUDPRequest(L *lua.LState, ctx *Context, service string, requestRoute, responseRoute lua.LValue, body []byte, s2cProto string, timeout int) int {
	udpAdp := ctx.ResolveAdapter("udp", service)
	if udpAdp == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service+" codec 未映射（resolver.Resolve(udp:"+service+") nil）"), lua.LNil)
	}
	routeKey := udpAdp.ExpectedRouteKey(luaValueToRoute(responseRoute))
	udpKey := ctx.NetSender.GetUDPSecretKey(service)
	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := udpAdp.EncodeUDP(luaValueToRoute(requestRoute), body, udpKey)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart), Observed: engine.TimingStageEncode})
	}
	if packet == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service), lua.LNil)
	}

	return awaitYield(L, "udp_request", &WaitSpec{
		Kind:     WaitResponse,
		Duration: time.Duration(timeout) * time.Second,
		Proto:    "udp",
		Service:  service,
		RouteKey: routeKey,
		S2CProto: s2cProto,
		Packet:   packet,
	})
}

// networkAwaitTCPRequestRoute 实现 network.tcp_request_route（协作式 TCP 请求-响应，发送路由与
// 响应匹配路由分离）。
// 签名：network.tcp_request_route(service, request_route, response_route, msg [, s2c_proto [, timeout_sec]])
func networkAwaitTCPRequestRoute(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}
	if L.GetTop() < 4 {
		L.RaiseError("network.tcp_request_route requires (service, request_route, response_route, msg [, s2c_proto [, timeout_sec]])")
		return 0
	}
	service := L.CheckString(1)
	requestRoute := L.Get(2)
	responseRoute := L.Get(3)
	msg, _, _ := unwrapProtoUD(L.Get(4))
	if msg == nil {
		L.RaiseError("network.tcp_request_route requires proto message at arg 4")
		return 0
	}
	s2cProto := ""
	if L.GetTop() >= 5 && L.Get(5) != lua.LNil {
		s2cProto = L.CheckString(5)
	}
	return doAwaitTCPRequest(L, ctx, service, requestRoute, responseRoute, msg, s2cProto, resolveRequestTimeoutSecAt(L, ctx, 6))
}

// networkAwaitUDPRequestRoute 实现 network.udp_request_route（协作式 UDP 请求-响应，发送路由与
// 响应匹配路由分离）。
// 签名：network.udp_request_route(service, request_route, response_route, body [, s2c_proto [, timeout_sec]])
func networkAwaitUDPRequestRoute(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}
	if L.GetTop() < 4 {
		L.RaiseError("network.udp_request_route requires (service, request_route, response_route, body [, s2c_proto [, timeout_sec]])")
		return 0
	}
	service := L.CheckString(1)
	requestRoute := L.Get(2)
	responseRoute := L.Get(3)
	body := []byte(L.CheckString(4))
	s2cProto := ""
	if L.GetTop() >= 5 && L.Get(5) != lua.LNil {
		s2cProto = L.CheckString(5)
	}
	return doAwaitUDPRequest(L, ctx, service, requestRoute, responseRoute, body, s2cProto, resolveRequestTimeoutSecAt(L, ctx, 6))
}

// networkHTTPRequest 发送 HTTP 请求。
// 签名：network.http_request(url [, method [, content_type [, body]]])
//
// 返回：err, status_code(number), body(string)
// err 为 nil 表示无框架传输错误（status_code 即 HTTP 原始状态码 200/404/500 等）；
// err 为 table 表示框架传输错误（code ∈ errcode，status_code=0、body=""）。
// HTTP message bytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkHTTPRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	reqURL := L.CheckString(1)
	method := "POST"
	contentType := "form"
	var bodyTable *lua.LTable

	if L.GetTop() >= 2 {
		if s, ok := L.Get(2).(lua.LString); ok && s != "" {
			method = string(s)
		}
	}
	if L.GetTop() >= 3 {
		if s, ok := L.Get(3).(lua.LString); ok && s != "" {
			contentType = string(s)
		}
	}
	if L.GetTop() >= 4 {
		if tb, ok := L.Get(4).(*lua.LTable); ok {
			bodyTable = tb
		}
	}

	if reqURL == "" {
		L.RaiseError("network.http_request requires (url [, method [, content_type [, body]]])")
		return 0
	}

	var reqBody []byte
	if bodyTable != nil {
		formData := make(map[string]string)
		bodyTable.ForEach(func(k, val lua.LValue) {
			formData[lua.LVAsString(k)] = lua.LVAsString(val)
		})

		switch contentType {
		case "json":
			jsonBytes, err := jsonStdConfig.Marshal(formData)
			if err != nil {
				L.RaiseError("json marshal failed: %v", err)
				return 0
			}
			reqBody = jsonBytes
		default: // "form"
			values := make(url.Values)
			for k, v := range formData {
				values.Set(k, v)
			}
			reqBody = []byte(values.Encode())
		}
	}

	// 协作式 Class B：HTTP Do 在后台协程执行（HTTP 往返可能较慢），等待窗口内执行器持续 drain
	// 任务队列。指标累计（recordRequest/recordBytes）与错误转 err table 必须在 renderer 内做——
	// 它在执行器 goroutine 上调用，Context 非并发安全。
	return awaitIO(L, "network.http_request", func() IORenderer {
		exchange, err := ctx.NetSender.HTTPRequest(reqURL, method, contentType, reqBody)
		return func(L *lua.LState) []lua.LValue {
			if exchange == nil {
				exchange = &engine.HTTPExchange{}
			}
			ctx.recordRequest(engine.RequestTiming{WireRTT: exchange.NetLatency, Observed: engine.TimingStageRTT})
			ctx.recordBytes(exchange.SendWireBytes, exchange.RecvWireBytes)
			if err != nil {
				return []lua.LValue{errTableFromActionErr(L, err), lua.LNumber(0), lua.LString("")}
			}
			return []lua.LValue{lua.LNil, lua.LNumber(exchange.StatusCode), lua.LString(string(exchange.Body))}
		}
	})
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

// networkTCPSend TCP 发送（不等响应）。
// 签名：network.tcp_send(service, route, msg)
//
// 返回：err
// nil 成功 / err table 失败（code=1-5 网络层 / code=11 协议层）。
// 发送 WireBytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkTCPSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service, route, msg, _ := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.tcp_send requires (service, route, msg)")
		return 0
	}

	msgData, err := serializeMsg(ctx, msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}

	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := buildPacket(ctx, service, route, msgData)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart), Observed: engine.TimingStageEncode})
	}
	if packet == nil {
		return pushErr(L, int(errcode.ErrEncodeFailed), "service="+service)
	}

	var sendStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly {
		sendStart = time.Now()
	}
	n, err := ctx.NetSender.TCPSend(service, packet)
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly && !sendStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{SendCost: time.Since(sendStart), Observed: engine.TimingStageSend})
	}
	if err == nil {
		ctx.recordBytes(n, 0)
		L.Push(lua.LNil)
	} else {
		ctx.recordBytes(len(packet), 0)
		L.Push(errTableFromActionErr(L, err))
	}
	return 1
}

// networkUDPSend 发送 UDP 编码消息。
// 签名：network.udp_send(service, route, body)
//
// 返回：err
// nil 成功 / err table 失败（code=1-5 网络层 / code=11 协议层）。
// 发送 WireBytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkUDPSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	route := L.Get(2)
	var body []byte
	if L.GetTop() >= 3 {
		body = []byte(L.CheckString(3))
	}

	// encode 走 resolver.Resolve("udp:"+service) 出的 Go SchemaAdapter。
	// Resolve nil → fail loud（ErrEncodeFailed，detail 带 service 串）。
	adp := ctx.ResolveAdapter("udp", service)
	if adp == nil {
		return pushErr(L, int(errcode.ErrEncodeFailed), "service="+service+" codec 未映射（resolver.Resolve(udp:"+service+") nil）")
	}
	goRoute := luaValueToRoute(route)
	udpKey := ctx.NetSender.GetUDPSecretKey(service)
	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := adp.EncodeUDP(goRoute, body, udpKey)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart), Observed: engine.TimingStageEncode})
	}
	if packet == nil {
		return pushErr(L, int(errcode.ErrEncodeFailed), "service="+service)
	}
	var sendStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly {
		sendStart = time.Now()
	}
	n, err := ctx.NetSender.UDPSend(service, packet)
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly && !sendStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{SendCost: time.Since(sendStart), Observed: engine.TimingStageSend})
	}
	if err == nil {
		ctx.recordBytes(n, 0)
		L.Push(lua.LNil)
	} else {
		ctx.recordBytes(len(packet), 0)
		L.Push(errTableFromActionErr(L, err))
	}
	return 1
}

// ---------------------------------------------------------------------------
// 监听
// ---------------------------------------------------------------------------

// listenParams 解析 *_listen / await_*_listen 系列的公共入参并计算 routeKey。
//
// 入参约定：(service, route [, s2cProto [, timeout秒]])。
// routeKey 走 resolver.Resolve("<proto>:"+service) 出的 Go SchemaAdapter 计算
// （与 engine robotActionHandler.RegisterListen 同源）。codec 未映射时记框架错误并返回
// ok=false，调用方据此直接返回 (ErrEncodeFailed, nil)。
func listenParams(L *lua.LState, ctx *Context, protocol string) (service, routeKey, s2cProto string, timeout int, ok bool) {
	if L.GetTop() > 4 {
		L.ArgError(5, "事件化 listen 仅支持 4 个参数")
	}
	service = L.CheckString(1)
	adp := ctx.ResolveAdapter(protocol, service)
	if adp == nil {
		return service, "", "", 0, false
	}
	route := luaValueToRoute(L.Get(2))
	routeKey = adp.ExpectedRouteKey(route)

	timeout = engine.DefaultListenTimeoutSec
	if L.GetTop() >= 3 {
		s2cProto = L.CheckString(3)
	}
	if L.GetTop() >= 4 {
		timeout = L.CheckInt(4)
	}
	return service, routeKey, s2cProto, timeout, true
}

// listenResultValues 把一次监听等待的结果（命中/超时/取消）转成 Lua 返回值 (err, data)。
// err 为 lua.LNil（成功）或 err table（失败）。协作式 await_*_listen 的 drive-loop 用。
func listenResultValues(L *lua.LState, ctx *Context, spec *WaitSpec, outcome WaitOutcome) []lua.LValue {
	exchange := outcome.Exchange
	if exchange == nil {
		exchange = &engine.NetExchange{}
	}
	ctx.recordBytes(0, exchange.RecvWireBytes)
	respBody := exchange.Body

	if outcome.Canceled {
		return []lua.LValue{
			newErrTable(L, int(errcode.ErrActionCanceled), "service="+spec.Service+" route="+spec.RouteKey),
			lua.LNil,
		}
	}
	if outcome.TimedOut {
		// 超时不产等待时长样本（没有时延值，且会把 P99 顶到超时上限、掩盖真实分布），单独成率。
		ctx.recordListenTimeout()
		timeoutSec := int(spec.Duration / time.Second)
		stresslog.Debug("[SCRIPT] "+spec.Proto+"_listen 超时",
			zap.String("service", spec.Service), zap.String("routeKey", spec.RouteKey), zap.Int("timeout", timeoutSec),
			zap.String("hint", "请先调用 ensure_"+spec.Proto+"_listener() 预注册监听"))
		return []lua.LValue{
			newErrTable(L, int(errcode.ErrListenTimeout),
				fmt.Sprintf("service=%s route=%s timeout=%ds", spec.Service, spec.RouteKey, timeoutSec)),
			lua.LNil,
		}
	}
	// 命中即记等待时长——HeaderErr（服务端回了业务错误）同样算命中：帧到了，
	// 等待时长有效，服务端确实是这么快响应的，不该因为业务判定为失败就丢掉这个样本。
	ctx.recordListenHit(outcome.ListenWait, outcome.ListenWaitKind)
	if exchange.HeaderErr != 0 {
		return []lua.LValue{
			newErrTable(L, int(exchange.HeaderErr), headerErrDetail(ctx, spec.Service, exchange.HeaderErr, spec.Service, spec.RouteKey)),
			lua.LString(string(respBody)),
		}
	}
	if spec.S2CProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		// 大消息走 wire 惰性视图（D2，wire-first 消费侧）：脚本拿到持字节的只读
		// userdata，proto.get_field/get_path 按需 wire 扫描，整包解码消失——
		// 帧循环脚本每帧只读两三个字段，此前为此整树解码是消费侧最大 churn 源。
		// 历史脉络：独占瞬态解码已被证伪（029→031 剖面，churn 放大 60 倍 +
		// 协程钉解码树 +1.15GB）；共享解码（FrozenCache）解决了 churn 但仍常驻
		// 解码树（268MB）且单人局命中率仅 6%。视图形态无解码树，两个问题同时消失；
		// 协程挂起钉住的只是共享 wire 快照本身。
		// 字节本身经 WireShared 内容寻址去重（同帧 60 接收方共享同一 *WireValue
		// 快照），与留存侧同一套缓存——留存/消费两侧形态自此统一为 wire。
		// schema 降级回落共享解码路径（正确性优先于形态）。
		if len(respBody) >= protox.DedupMinBytes {
			if md, ok := ctx.Factory.MessageDescriptor(spec.S2CProto); ok && !protox.WireDegraded(md) {
				wv, err := ctx.Factory.WireShared(spec.S2CProto, respBody)
				if err != nil {
					// 校验失败 ≡ 解码必失败（差分 fuzz 保证等价）：按解析失败报错。
					return []lua.LValue{
						newErrTable(L, int(errcode.ErrParseFailed), "service="+spec.Service+" route="+spec.RouteKey),
						lua.LNil,
					}
				}
				return []lua.LValue{lua.LNil, wrapWireView(L, wv)}
			}
			frozen, err := ctx.Factory.ParseFrozenShared(spec.S2CProto, respBody)
			if err != nil {
				return []lua.LValue{
					newErrTable(L, int(errcode.ErrParseFailed), "service="+spec.Service+" route="+spec.RouteKey),
					lua.LNil,
				}
			}
			return []lua.LValue{lua.LNil, wrapFrozenMessage(L, frozen)}
		}
		// 小消息独占解码（重复留存可忽略，不值得哈希+快照）；消息可写（历史行为），
		// 未改写时 robot.set(resp) 经句柄携带的 body 快照零成本转 WireValue。
		respMsg, err := ctx.Factory.Parse(spec.S2CProto, respBody)
		if err != nil {
			return []lua.LValue{
				newErrTable(L, int(errcode.ErrParseFailed), "service="+spec.Service+" route="+spec.RouteKey),
				lua.LNil,
			}
		}
		return []lua.LValue{lua.LNil, wrapRespMessage(L, respMsg, protox.WireSnapshot(respBody))}
	}
	return []lua.LValue{lua.LNil, lua.LString(string(respBody))}
}

// networkAwaitTCPListen / networkAwaitUDPListen 实现 network.tcp_listen / udp_listen
// （协作式监听）：在监听等待处 yield，等待窗口内调度器可 drain 任务队列。
func networkAwaitTCPListen(L *lua.LState) int { return awaitNetworkListen(L, "tcp") }
func networkAwaitUDPListen(L *lua.LState) int { return awaitNetworkListen(L, "udp") }

func awaitNetworkListen(L *lua.LState, protocol string) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}
	service, routeKey, s2cProto, timeout, ok := listenParams(L, ctx, protocol)
	if !ok {
		// codec 未映射：直接返回错误，不进入协作式等待（无须 yield）。
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service+" codec 未映射（resolver.Resolve("+protocol+":"+service+") nil）"), lua.LNil)
	}
	spec := &WaitSpec{
		Kind:     WaitListen,
		Duration: time.Duration(timeout) * time.Second,
		Proto:    protocol,
		Service:  service,
		RouteKey: routeKey,
		S2CProto: s2cProto,
	}
	return awaitYield(L, protocol+"_listen", spec)
}

// networkTryTCPListen 非阻塞获取最近一条 TCP 监听消息（不解析 proto，返回原始 body）。
// 签名：network.try_tcp_listen(service, route)
//
// 返回：err(nil|table), data(string|nil)
//   - err=nil + data=string：取到一条消息的原始 body（**不解析 proto**，需要解析请用等待版 tcp_listen）。
//   - err=nil + data=nil：队列空、无新消息（非错误路径，不进失败统计）。
//   - err table：codec 未映射（code=ErrEncodeFailed）或服务端 HeaderErr（code=HeaderErr），data 为原始 body 字符串或 nil。
//
// 与等待版 tcp_listen 的差异：**单次非阻塞 pop**并立即返回。适用于高频 sync loop 「保最新」消费场景
// （如 battleAck 追踪：队列容量 1，每轮 pop 最新 ack 写 state）。
func networkTryTCPListen(L *lua.LState) int { return networkTryListen(L, "tcp") }

// networkTryUDPListen 非阻塞获取最近一条 UDP 监听消息（不解析 proto，返回原始 body）。
// 签名：network.try_udp_listen(service, route)
//
// 返回语义同 networkTryTCPListen。
func networkTryUDPListen(L *lua.LState) int { return networkTryListen(L, "udp") }

// networkTryListen 非阻塞单次 pop 的共享实现。
//
// 设计要点：
//   - 单次非阻塞 pop（GetTCP/UDPListenResp 内部走 per-queue 锁），无阻塞等待。
//   - 不解析 proto：try_* 是「原始 drain」原语，需 proto 解析的消费请用等待版 listen。
//   - queue 空时返回 (nil, nil)：非错误路径，脚本据此判定「无新消息」。
//   - 服务端 HeaderErr 返回 (err table, body)：err.code = HeaderErr。
//   - 接收 WireBytes 仍由 Context 累计（与 tcp_listen 一致）。
func networkTryListen(L *lua.LState, protocol string) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	// routeKey 走 resolver.Resolve("<proto>:"+service) 出的 Go SchemaAdapter。
	// Resolve nil → fail loud（与 network.tcp_listen / udp_listen 一致；try_* 虽是 drain 原语，
	// 但 routeKey 计算依赖 codec，缺 codec 必须暴露配置错误而非静默返回 timeout）。
	adp := ctx.ResolveAdapter(protocol, service)
	if adp == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service+" codec 未映射（resolver.Resolve("+protocol+":"+service+") nil）"), lua.LNil)
	}
	route := luaValueToRoute(L.Get(2))
	routeKey := adp.ExpectedRouteKey(route)

	var exchange *engine.NetExchange
	if protocol == "tcp" {
		exchange = ctx.NetSender.GetTCPListenResp(service, routeKey)
	} else {
		exchange = ctx.NetSender.GetUDPListenResp(service, routeKey)
	}

	if exchange == nil {
		// 队列空：返回 (nil, nil)（非错误路径，不构造 err table，避免污染失败统计）。
		L.Push(lua.LNil)
		L.Push(lua.LNil)
		return 2
	}

	ctx.recordBytes(0, exchange.RecvWireBytes)
	respBody := exchange.Body

	if exchange.HeaderErr != 0 {
		return pushResult(L,
			newErrTable(L, int(exchange.HeaderErr), headerErrDetail(ctx, service, exchange.HeaderErr, service, routeKey)),
			lua.LString(string(respBody)))
	}

	L.Push(lua.LNil)
	L.Push(lua.LString(string(respBody)))
	return 2
}

// ---------------------------------------------------------------------------
// 密钥
// ---------------------------------------------------------------------------

// networkSetTCPSecretKey 设置 TCP 连接的加密密钥。
// 签名：network.set_tcp_secret_key(service, key)
func networkSetTCPSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	key := []byte(L.CheckString(2))
	ctx.NetSender.SetTCPSecretKey(service, key)
	return 0
}

// networkSetUDPSecretKey 设置 UDP 连接的加密密钥。
// 签名：network.set_udp_secret_key(service, key)
func networkSetUDPSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	key := []byte(L.CheckString(2))
	ctx.NetSender.SetUDPSecretKey(service, key)
	return 0
}

// networkGetTCPSecretKey 获取 TCP 连接的加密密钥。
// 签名：network.get_tcp_secret_key(service)
// 返回：key(string|nil)
func networkGetTCPSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.Push(lua.LNil)
		return 1
	}
	service := L.CheckString(1)
	key := ctx.NetSender.GetTCPSecretKey(service)
	if key == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(key)))
	}
	return 1
}

// networkGetUDPSecretKey 获取 UDP 连接的加密密钥。
// 签名：network.get_udp_secret_key(service)
// 返回：key(string|nil)
func networkGetUDPSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.Push(lua.LNil)
		return 1
	}
	service := L.CheckString(1)
	key := ctx.NetSender.GetUDPSecretKey(service)
	if key == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(key)))
	}
	return 1
}

// ---------------------------------------------------------------------------
// 监听器占位
// ---------------------------------------------------------------------------

// networkEnsureTCPListener 为 TCP routeKey 注册监听器占位。
// tcp_listen 不再自动注册，Lua 脚本需在触发推送前显式调用此函数。
// 签名：network.ensure_tcp_listener(service, response_key)
// queueSize 固定为 1（Lua 不暴露 queueSize；大容量请用 flow listenRefs 的 queueSize 配置）。
func networkEnsureTCPListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	routeKey := L.CheckString(2)
	ctx.NetSender.EnsureTCPListener(service, routeKey, 1)
	return 0
}

// networkEnsureUDPListener 为 UDP routeKey 注册监听器占位。
// udp_listen 不再自动注册，Lua 脚本需在触发推送前显式调用此函数。
// 签名：network.ensure_udp_listener(service, response_key)
// queueSize 固定为 1（Lua 不暴露 queueSize；大容量请用 flow listenRefs 的 queueSize 配置）。
func networkEnsureUDPListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	routeKey := L.CheckString(2)
	ctx.NetSender.EnsureUDPListener(service, routeKey, 1)
	return 0
}

// ---------------------------------------------------------------------------
// adapter 模块（编解码适配器）已下线
// ---------------------------------------------------------------------------
//
// 历史 adapter Lua 模块曾暴露 codec 给业务脚本；现在业务 encode/decode
// 全走 Go CodecResolver（ctx.Resolver.Resolve），
// conf/scripts 经 grep 确认零依赖 adapter 模块，故整条 Lua 模块下线（registerAPIs 也同步
// 删除 PreloadModule("adapter")），不再向业务 LState 注入适配器脚本。
