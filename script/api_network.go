package script

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"stressbot/engine"
	"stressbot/errcode"
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
//	network.udp_request(service, route, body [, s2c [, timeout [, poll]]])
//	network.udp_request_route(service, request_route, response_route, body [, s2c])
//	network.http_request(url [, method [, content_type [, body]]])
//	network.tcp_send(service, route, msg)
//	network.udp_send(service, route, body)
//	network.tcp_listen(service, route [, s2c [, timeout [, poll]]])
//	network.udp_listen(service, route [, s2c [, timeout [, poll]]])
//	network.try_tcp_listen(service, route) → code, raw_body  (非阻塞单次 pop)
//	network.try_udp_listen(service, route) → code, raw_body  (非阻塞单次 pop)
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
	L.SetField(mod, "tcp_request", L.NewFunction(networkTCPRequest))
	L.SetField(mod, "tcp_request_route", L.NewFunction(networkTCPRequestRoute))
	L.SetField(mod, "udp_request", L.NewFunction(networkUDPRequest))
	L.SetField(mod, "udp_request_route", L.NewFunction(networkUDPRequestRoute))
	L.SetField(mod, "http_request", L.NewFunction(networkHTTPRequest))
	// 发送
	L.SetField(mod, "tcp_send", L.NewFunction(networkTCPSend))
	L.SetField(mod, "udp_send", L.NewFunction(networkUDPSend))
	// 监听
	L.SetField(mod, "tcp_listen", L.NewFunction(networkTCPListen))
	L.SetField(mod, "udp_listen", L.NewFunction(networkUDPListen))
	// 非阻塞单次 pop（不轮询、不 sleep）：取最近一条缓存消息的原始 body
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

// errToCode 从 ActionError 提取错误码，透传给 Lua。
// 所有网络层错误都是 ActionError（含 errcode 体系的具体码），
// 非 ActionError 降级为 -1（理论上不会走到）。
func errToCode(err error) int {
	if actionErr, ok := errors.AsType[*engine.ActionError](err); ok {
		return int(actionErr.ErrorCode())
	}
	return -1
}

func rememberActionErr(ctx *Context, err error) {
	if ctx == nil || err == nil {
		return
	}
	if actionErr, ok := errors.AsType[*engine.ActionError](err); ok {
		ctx.SetLastActionError(actionErr)
	}
}

func rememberFrameworkErr(ctx *Context, code errcode.ErrorCode, detail string) {
	if ctx == nil {
		return
	}
	ctx.SetLastActionError(engine.NewActionError(code, detail))
}

// resolveDescribeError 经 CodecResolver 取任一已知连接的 adapter 后 DescribeError。
//
// DescribeError codec 无关（共享 errors.json，全连接同源），故任一已声明的 server 命中即可；
// Resolver nil 或所有 server 均未映射 → 返回空串（headerErr 描述是增强信息而非核心路径，
// 缺失非致命——headerErr 错误码本身仍按 NewServerError 上抛，与 2-C2-Go 的 handleHeaderError
// fail-loud 策略不对称是有意设计）。
//
// serverHint 建议传该 Robot 的主连接（如 "tcp:logic"），命中率高；空串时回退首声明的 server。
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

func rememberHeaderErr(ctx *Context, code uint64, service, routeKey string) {
	if ctx == nil {
		return
	}
	detail := "service=" + service + " route=" + routeKey
	// DescribeError 经 CodecResolver（codec 无关，取该连接或主连接的 adapter 即可）。
	// server 串按 service 推断：监听/请求路径下 service 即连接标识，proto 未知故双探测。
	// 任一命中即取描述；未命中（codec 未映射）→ 描述降级空串，headerErr 错误码仍上抛。
	desc := resolveDescribeError(ctx, "tcp:"+service, code)
	if desc == "" {
		desc = resolveDescribeError(ctx, "udp:"+service, code)
	}
	if desc != "" {
		detail = desc + ": " + detail
	}
	ctx.SetLastActionError(engine.NewServerError(code, detail))
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
		if ud, ok := v.(*lua.LUserData); ok {
			if pm, ok := ud.Value.(proto.Message); ok {
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
// T2-C2-Lua 起走 CodecResolver：按 "tcp:"+service Resolve 出该连接的 Go SchemaAdapter 后
// EncodeTCP（与 engine.ActionExecutor / 心跳 goBuilder 共享同一份 codec 映射，encode 双向一致）。
// Resolve nil（连接 codec 未映射）→ 返回 nil，调用方（doTCPRequest / networkTCPSend）fail loud
// （ErrEncodeFailed，detail 带 service 串，不静默兜底）。
func buildPacket(ctx *Context, service string, route lua.LValue, msgData []byte) []byte {
	if ctx == nil || ctx.Resolver == nil {
		return nil
	}
	adp := ctx.Resolver.Resolve("tcp:" + service)
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
// 返回：code(number)
// code=0 成功 / errcode 错误码（6=取消 / 2=连接关闭）。
func networkConnectTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, "service="+service+" address="+address)
		L.Push(lua.LNumber(errcode.ErrActionCanceled))
		return 1
	}
	err := ctx.NetSender.ConnectTCP(service, address)
	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, "service="+service+" address="+address)
		L.Push(lua.LNumber(errcode.ErrActionCanceled))
		return 1
	}
	if err != nil {
		rememberActionErr(ctx, err)
		L.Push(lua.LNumber(errToCode(err)))
	} else {
		L.Push(lua.LNumber(0))
	}
	return 1
}

// networkConnectUDP 建立 UDP 连接。
// 签名：network.connect_udp(service, address)
//
// 返回：code(number)
// code=0 成功 / errcode 错误码（6=取消 / 2=连接关闭）。
func networkConnectUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, "service="+service+" address="+address)
		L.Push(lua.LNumber(errcode.ErrActionCanceled))
		return 1
	}
	err := ctx.NetSender.ConnectUDP(service, address)
	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, "service="+service+" address="+address)
		L.Push(lua.LNumber(errcode.ErrActionCanceled))
		return 1
	}
	if err != nil {
		rememberActionErr(ctx, err)
		L.Push(lua.LNumber(errToCode(err)))
	} else {
		L.Push(lua.LNumber(0))
	}
	return 1
}

// networkCloseTCP 关闭 TCP 连接。
// 签名：network.close_tcp(service)
//
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
//
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

// networkTCPRequest TCP 请求-响应。
// 签名：network.tcp_request(service, route, msg [, s2c_proto])
//
// 返回：err(table|nil), data(string|userdata|nil)
// err=nil 成功；失败时 err={code, detail}。
// WireBytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkTCPRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service, route, msg, s2cProto := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.tcp_request requires (service, route, msg [, s2c_proto [, timeout_sec]])")
		return 0
	}

	return doTCPRequest(L, ctx, service, route, route, msg, s2cProto, resolveRequestTimeoutSec(L, ctx))
}

// networkTCPRequestRoute TCP 请求-响应，发送路由和响应匹配路由分离。
// 签名：network.tcp_request_route(service, request_route, response_route, msg [, s2c_proto [, timeout_sec]])
//
// request_route 用于编码请求包，response_route 用于计算等待响应的 routeKey。
func networkTCPRequestRoute(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
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
	ud, ok := L.Get(4).(*lua.LUserData)
	if !ok {
		L.RaiseError("network.tcp_request_route requires proto message at arg 4")
		return 0
	}
	msg, ok := ud.Value.(proto.Message)
	if !ok {
		L.RaiseError("network.tcp_request_route requires proto message at arg 4")
		return 0
	}
	s2cProto := ""
	if L.GetTop() >= 5 && L.Get(5) != lua.LNil {
		s2cProto = L.CheckString(5)
	}

	return doTCPRequest(L, ctx, service, requestRoute, responseRoute, msg, s2cProto, resolveRequestTimeoutSecAt(L, ctx, 6))
}

func doTCPRequest(L *lua.LState, ctx *Context, service string, requestRoute, responseRoute lua.LValue, msg proto.Message, s2cProto string, timeout int) int {
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
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart)})
	}
	if packet == nil {
		detail := "service=" + service + " codec 未映射"
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), detail), lua.LNil)
	}

	// ExpectedRouteKey 走 resolver.Resolve("tcp:"+service) 出的 Go SchemaAdapter
	// （与 encode 同源，避免双 codec 漂移）。Resolve nil → fail loud（理论上 encode 已 fail，
	// 这里防御性兜 nil routeKey 会导致 RequestResponse 永久等不到响应）。
	if ctx.Resolver == nil {
		detail := "service=" + service + " routeKey 解析失败（codec 未映射）"
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), detail), lua.LNil)
	}
	tcpAdp := ctx.Resolver.Resolve("tcp:" + service)
	if tcpAdp == nil {
		detail := "service=" + service + " routeKey 解析失败（codec 未映射）"
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), detail), lua.LNil)
	}
	routeKey := tcpAdp.ExpectedRouteKey(luaValueToRoute(responseRoute))
	pktLen := len(packet)

	exchange, reqErr := ctx.NetSender.TCPRequest(service, packet, routeKey,
		time.Duration(timeout)*time.Second)
	if exchange == nil {
		exchange = &engine.NetExchange{SendWireBytes: pktLen}
	}
	ctx.recordRequest(exchange.Timing)
	ctx.recordBytes(exchange.SendWireBytes, exchange.RecvWireBytes)
	respBody := exchange.Body

	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		detail := "service=" + service + " route=" + routeKey
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrActionCanceled), detail), lua.LNil)
	}
	if reqErr != nil {
		rememberActionErr(ctx, reqErr)
		return pushResult(L, errTableFromActionErr(L, reqErr), lua.LNil)
	}
	if exchange.HeaderErr != 0 {
		detail := "service=" + service + " route=" + routeKey
		if desc := resolveDescribeError(ctx, "tcp:"+service, exchange.HeaderErr); desc != "" {
			detail = desc + ": " + detail
		}
		rememberHeaderErr(ctx, exchange.HeaderErr, service, routeKey)
		return pushResult(L, newErrTable(L, int(exchange.HeaderErr), detail), lua.LString(string(respBody)))
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		var parseStart time.Time
		if ctx.TimingLevel >= engine.TimingLevelFull {
			parseStart = time.Now()
		}
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if ctx.TimingLevel >= engine.TimingLevelFull && !parseStart.IsZero() {
			ctx.recordClientTiming(engine.ClientTiming{ParseStoreCost: time.Since(parseStart)})
		}
		if err != nil {
			detail := "service=" + service + " route=" + routeKey
			rememberFrameworkErr(ctx, errcode.ErrParseFailed, detail)
			return pushResult(L, newErrTable(L, int(errcode.ErrParseFailed), detail), lua.LString(string(respBody)))
		}
		return pushResult(L, lua.LNil, wrapProtoMessage(L, respMsg))
	}

	return pushResult(L, lua.LNil, lua.LString(string(respBody)))
}

// networkUDPRequest UDP 请求-响应。
// 签名：network.udp_request(service, route, body [, s2c_proto [, timeout_sec [, poll_ms]]])
//
// 返回：err(table|nil), data(string|userdata|nil)
// err=nil 成功；失败时 err={code, detail}。
// WireBytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkUDPRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
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
	return doUDPRequest(L, ctx, service, route, route, body, s2cProto, resolveRequestTimeoutSec(L, ctx))
}

// networkUDPRequestRoute UDP 请求-响应，发送路由和响应匹配路由分离。
// 签名：network.udp_request_route(service, request_route, response_route, body [, s2c_proto [, timeout_sec]])
//
// request_route 用于编码请求包，response_route 用于计算等待响应的 routeKey。
func networkUDPRequestRoute(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
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

	return doUDPRequest(L, ctx, service, requestRoute, responseRoute, body, s2cProto, resolveRequestTimeoutSecAt(L, ctx, 6))
}

func doUDPRequest(L *lua.LState, ctx *Context, service string, requestRoute, responseRoute lua.LValue, body []byte, s2cProto string, timeout int) int {
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	// T2-C2-Lua：encode + ExpectedRouteKey 全走 resolver.Resolve("udp:"+service) 出的 Go SchemaAdapter
	// （与 TCP 侧 doTCPRequest / engine.ActionExecutor 同源）。Resolve nil → fail loud。
	if ctx.Resolver == nil {
		detail := "service=" + service + " codec 未映射（resolver.Resolve(udp:" + service + ") nil）"
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), detail), lua.LNil)
	}
	udpAdp := ctx.Resolver.Resolve("udp:" + service)
	if udpAdp == nil {
		detail := "service=" + service + " codec 未映射（resolver.Resolve(udp:" + service + ") nil）"
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), detail), lua.LNil)
	}
	routeKey := udpAdp.ExpectedRouteKey(luaValueToRoute(responseRoute))
	udpKey := ctx.NetSender.GetUDPSecretKey(service)
	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := udpAdp.EncodeUDP(luaValueToRoute(requestRoute), body, udpKey)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart)})
	}
	if packet == nil {
		detail := "service=" + service + " codec 未映射"
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), detail), lua.LNil)
	}

	pktLen := len(packet)
	exchange, reqErr := ctx.NetSender.UDPRequest(
		service, packet, routeKey,
		time.Duration(timeout)*time.Second,
	)
	if exchange == nil {
		exchange = &engine.NetExchange{SendWireBytes: pktLen}
	}
	ctx.recordRequest(exchange.Timing)
	ctx.recordBytes(exchange.SendWireBytes, exchange.RecvWireBytes)
	respBody := exchange.Body

	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		// 脚本上下文被取消（robot.Stop / 任务停止）。区别于 reqErr 携带的 CONN_DROPPED：
		// 后者是底层连接被对端断开；这里是本地主动取消，归类为 ACTION_CANCELED 避免被
		// 误判为网络异常污染失败率统计。
		detail := "service=" + service + " route=" + routeKey
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, detail)
		return pushResult(L, newErrTable(L, int(errcode.ErrActionCanceled), detail), lua.LNil)
	}
	if reqErr != nil {
		rememberActionErr(ctx, reqErr)
		return pushResult(L, errTableFromActionErr(L, reqErr), lua.LNil)
	}
	if exchange.HeaderErr != 0 {
		detail := "service=" + service + " route=" + routeKey
		if desc := resolveDescribeError(ctx, "udp:"+service, exchange.HeaderErr); desc != "" {
			detail = desc + ": " + detail
		}
		rememberHeaderErr(ctx, exchange.HeaderErr, service, routeKey)
		return pushResult(L, newErrTable(L, int(exchange.HeaderErr), detail), lua.LString(string(respBody)))
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		var parseStart time.Time
		if ctx.TimingLevel >= engine.TimingLevelFull {
			parseStart = time.Now()
		}
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if ctx.TimingLevel >= engine.TimingLevelFull && !parseStart.IsZero() {
			ctx.recordClientTiming(engine.ClientTiming{ParseStoreCost: time.Since(parseStart)})
		}
		if err != nil {
			detail := "service=" + service + " route=" + routeKey
			rememberFrameworkErr(ctx, errcode.ErrParseFailed, detail)
			return pushResult(L, newErrTable(L, int(errcode.ErrParseFailed), detail), lua.LString(string(respBody)))
		}
		return pushResult(L, lua.LNil, wrapProtoMessage(L, respMsg))
	}

	return pushResult(L, lua.LNil, lua.LString(string(respBody)))
}

// networkHTTPRequest 发送 HTTP 请求。
// 签名：network.http_request(url [, method [, content_type [, body]]])
//
// 返回：status_code(number), body(string)
// 1-99=框架传输错误（errcode）/ 其他=HTTP 原始状态码（200/404/500 等）。
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

	exchange, err := ctx.NetSender.HTTPRequest(reqURL, method, contentType, reqBody)
	if exchange == nil {
		exchange = &engine.HTTPExchange{}
	}
	ctx.recordRequest(engine.RequestTiming{WireRTT: exchange.NetLatency})
	ctx.recordBytes(exchange.SendWireBytes, exchange.RecvWireBytes)
	if err != nil {
		rememberActionErr(ctx, err)
		L.Push(lua.LNumber(errToCode(err)))
		L.Push(lua.LString(""))
		return 2
	}

	L.Push(lua.LNumber(exchange.StatusCode))
	L.Push(lua.LString(string(exchange.Body)))
	return 2
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

// networkTCPSend TCP 发送（不等响应）。
// 签名：network.tcp_send(service, route, msg)
//
// 返回：code(number)
// code=0 成功 / errcode 错误码（1-5 网络层 / 11 协议层）。
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
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart)})
	}
	if packet == nil {
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, "service="+service)
		L.Push(lua.LNumber(errcode.ErrEncodeFailed))
		return 1
	}

	var sendStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly {
		sendStart = time.Now()
	}
	n, err := ctx.NetSender.TCPSend(service, packet)
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly && !sendStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{SendCost: time.Since(sendStart)})
	}
	if err == nil {
		ctx.recordBytes(n, 0)
		L.Push(lua.LNumber(0))
	} else {
		ctx.recordBytes(len(packet), 0)
		rememberActionErr(ctx, err)
		L.Push(lua.LNumber(errToCode(err)))
	}
	return 1
}

// networkUDPSend 发送 UDP 编码消息。
// 签名：network.udp_send(service, route, body)
//
// 返回：code(number)
// code=0 成功 / errcode 错误码（1-5 网络层 / 11 协议层）。
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

	// T2-C2-Lua：encode 走 resolver.Resolve("udp:"+service) 出的 Go SchemaAdapter。
	// Resolve nil → fail loud（ErrEncodeFailed，detail 带 service 串）。
	adp := ctx.Resolver.Resolve("udp:" + service)
	if adp == nil {
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, "service="+service+" codec 未映射（resolver.Resolve(udp:"+service+") nil）")
		L.Push(lua.LNumber(errcode.ErrEncodeFailed))
		return 1
	}
	goRoute := luaValueToRoute(route)
	udpKey := ctx.NetSender.GetUDPSecretKey(service)
	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := adp.EncodeUDP(goRoute, body, udpKey)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart)})
	}
	if packet == nil {
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, "service="+service)
		L.Push(lua.LNumber(errcode.ErrEncodeFailed))
		return 1
	}
	var sendStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly {
		sendStart = time.Now()
	}
	n, err := ctx.NetSender.UDPSend(service, packet)
	if ctx.TimingLevel >= engine.TimingLevelRTTOnly && !sendStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{SendCost: time.Since(sendStart)})
	}
	if err == nil {
		ctx.recordBytes(n, 0)
		L.Push(lua.LNumber(0))
	} else {
		ctx.recordBytes(len(packet), 0)
		rememberActionErr(ctx, err)
		L.Push(lua.LNumber(errToCode(err)))
	}
	return 1
}

// ---------------------------------------------------------------------------
// 监听
// ---------------------------------------------------------------------------

// networkTCPListen 等待 TCP 监听消息。
// 签名：network.tcp_listen(service, route [, s2c_proto [, timeout_sec [, poll_ms]]])
//
// 返回：code(number), data(string|userdata|nil)
// code: 0=成功 / 31=超时 / 6=取消 / 12=解析失败 / 其他非零=服务端 HeaderErr。
// 接收 WireBytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkTCPListen(L *lua.LState) int { return networkListen(L, "tcp") }

// networkUDPListen 等待 UDP 监听消息。
// 签名：network.udp_listen(service, route [, s2c_proto [, timeout_sec [, poll_ms]]])
//
// 返回：code(number), data(string|userdata|nil)
// code: 0=成功 / 31=超时 / 6=取消 / 12=解析失败 / 其他非零=服务端 HeaderErr。
// 接收 WireBytes 由 Context 自动累计，不返回给 Lua 脚本。
func networkUDPListen(L *lua.LState) int { return networkListen(L, "udp") }

// networkListen 通用监听实现，通过 protocol 参数区分 TCP/UDP。
func networkListen(L *lua.LState, protocol string) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	// T2-C2-Lua：routeKey 走 resolver.Resolve("<proto>:"+service) 出的 Go SchemaAdapter 计算
	// （与 engine robotActionHandler.RegisterListen 同源，闭环双 codec）。Resolve nil → fail loud。
	adp := ctx.Resolver.Resolve(protocol + ":" + service)
	if adp == nil {
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, "service="+service+" codec 未映射（resolver.Resolve("+protocol+":"+service+") nil）")
		L.Push(lua.LNumber(errcode.ErrEncodeFailed))
		L.Push(lua.LNil)
		return 2
	}
	route := luaValueToRoute(L.Get(2))
	routeKey := adp.ExpectedRouteKey(route)

	var s2cProto string
	timeout := engine.DefaultListenTimeoutSec
	pollMs := engine.DefaultPollMs

	if L.GetTop() >= 3 {
		s2cProto = L.CheckString(3)
	}
	if L.GetTop() >= 4 {
		timeout = L.CheckInt(4)
	}
	if L.GetTop() >= 5 {
		pollMs = L.CheckInt(5)
	}
	if pollMs <= 0 {
		pollMs = engine.DefaultPollMs
	}

	var exchange *engine.NetExchange
	var timedOut bool

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		if protocol == "tcp" {
			exchange = ctx.NetSender.GetTCPListenResp(service, routeKey)
		} else {
			exchange = ctx.NetSender.GetUDPListenResp(service, routeKey)
		}
		if exchange != nil {
			break
		}
		if ctx.Ctx != nil {
			select {
			case <-time.After(time.Duration(pollMs) * time.Millisecond):
			case <-ctx.Ctx.Done():
			}
		} else {
			time.Sleep(time.Duration(pollMs) * time.Millisecond)
		}
		if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
			break
		}
	}
	if exchange == nil && (ctx.Ctx == nil || ctx.Ctx.Err() == nil) {
		timedOut = true
	}
	if exchange == nil {
		exchange = &engine.NetExchange{}
	}
	ctx.recordBytes(0, exchange.RecvWireBytes)
	respBody := exchange.Body

	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		rememberFrameworkErr(ctx, errcode.ErrActionCanceled, "service="+service+" route="+routeKey)
		L.Push(lua.LNumber(errcode.ErrActionCanceled))
		L.Push(lua.LNil)
		return 2
	}
	if timedOut {
		stresslog.Debug("[SCRIPT] "+protocol+"_listen 超时",
			zap.String("service", service), zap.String("routeKey", routeKey), zap.Int("timeout", timeout),
			zap.String("hint", "请先调用 ensure_"+protocol+"_listener() 预注册监听"))
		rememberFrameworkErr(ctx, errcode.ErrListenTimeout,
			fmt.Sprintf("service=%s route=%s timeout=%ds pollMs=%d", service, routeKey, timeout, pollMs))
		L.Push(lua.LNumber(errcode.ErrListenTimeout))
		L.Push(lua.LNil)
		return 2
	}
	if exchange.HeaderErr != 0 {
		rememberHeaderErr(ctx, exchange.HeaderErr, service, routeKey)
		L.Push(lua.LNumber(exchange.HeaderErr))
		L.Push(lua.LString(string(respBody)))
		return 2
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			rememberFrameworkErr(ctx, errcode.ErrParseFailed, "service="+service+" route="+routeKey)
			L.Push(lua.LNumber(errcode.ErrParseFailed))
			L.Push(lua.LNil)
			return 2
		}
		L.Push(lua.LNumber(0))
		L.Push(wrapProtoMessage(L, respMsg))
		return 2
	}

	L.Push(lua.LNumber(0))
	L.Push(lua.LString(string(respBody)))
	return 2
}

// networkTryTCPListen 非阻塞获取最近一条 TCP 监听消息（不解析 proto，返回原始 body）。
// 签名：network.try_tcp_listen(service, route)
//
// 返回：code(number), data(string|nil)
//   - code=0：取到一条消息，data 为原始 body 字符串（**不解析 proto**，需要解析请用阻塞版 tcp_listen）。
//   - code=31（ErrListenTimeout）：队列空、无新消息，data=nil。
//   - 其他非零：服务端 HeaderErr，data 为原始 body 字符串。
//
// 与阻塞版 tcp_listen 的差异：**单次非阻塞 pop**，不轮询、不 sleep。适用于高频 sync loop 「保最新」消费场景
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
//   - 不解析 proto：try_* 是「原始 drain」原语，需 proto 解析的消费请用阻塞版 listen。
//   - queue 空时返回 ErrListenTimeout（code=31），data=nil；与阻塞超时同码便于脚本统一处理。
//   - 接收 WireBytes 仍由 Context 累计（与 tcp_listen 一致）。
func networkTryListen(L *lua.LState, protocol string) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Resolver == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	// T2-C2-Lua：routeKey 走 resolver.Resolve("<proto>:"+service) 出的 Go SchemaAdapter。
	// Resolve nil → fail loud（与阻塞版 networkListen 一致；try_* 虽是 drain 原语，
	// 但 routeKey 计算依赖 codec，缺 codec 必须暴露配置错误而非静默返回 timeout）。
	adp := ctx.Resolver.Resolve(protocol + ":" + service)
	if adp == nil {
		rememberFrameworkErr(ctx, errcode.ErrEncodeFailed, "service="+service+" codec 未映射（resolver.Resolve("+protocol+":"+service+") nil）")
		L.Push(lua.LNumber(errcode.ErrEncodeFailed))
		L.Push(lua.LNil)
		return 2
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
		// 队列空：返回超时码（非错误路径，不记 LastActionError，避免污染失败统计）。
		L.Push(lua.LNumber(errcode.ErrListenTimeout))
		L.Push(lua.LNil)
		return 2
	}

	ctx.recordBytes(0, exchange.RecvWireBytes)
	respBody := exchange.Body

	if exchange.HeaderErr != 0 {
		rememberHeaderErr(ctx, exchange.HeaderErr, service, routeKey)
		L.Push(lua.LNumber(exchange.HeaderErr))
		L.Push(lua.LString(string(respBody)))
		return 2
	}

	L.Push(lua.LNumber(0))
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
// adapter 模块（编解码适配器）已下线（T2-C2-Lua）
// ---------------------------------------------------------------------------
//
// 历史 adapter Lua 模块曾暴露 codec 给业务脚本。T2-C2-Lua 起业务
// encode/decode 全走 Go CodecResolver（ctx.Resolver.Resolve），
// conf/scripts 经 grep 确认零依赖 adapter 模块，故整条 Lua 模块下线（registerAPIs 也同步
// 删除 PreloadModule("adapter")），不再向业务 LState 注入适配器脚本。
