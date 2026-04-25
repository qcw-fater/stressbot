package script

import (
	"fmt"
	"math"
	stresslog "stressbot/utils/log"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// loadNetworkModule 加载 network 命名空间模块。
func loadNetworkModule(L *lua.LState) int {
	mod := L.NewTable()

	L.SetField(mod, "connect_tcp", L.NewFunction(networkConnectTCP))
	L.SetField(mod, "connect_udp", L.NewFunction(networkConnectUDP))
	L.SetField(mod, "close_tcp", L.NewFunction(networkCloseTCP))
	L.SetField(mod, "close_udp", L.NewFunction(networkCloseUDP))
	L.SetField(mod, "exchange_key", L.NewFunction(networkExchangeKey))
	L.SetField(mod, "request", L.NewFunction(networkRequest))
	L.SetField(mod, "http_post", L.NewFunction(networkHTTPPost))
	L.SetField(mod, "tcp_send", L.NewFunction(networkTCPSend))
	L.SetField(mod, "udp_send", L.NewFunction(networkUDPSend))
	L.SetField(mod, "udp_send_msg", L.NewFunction(networkUDPSendMsg))
	L.SetField(mod, "wait_listen", L.NewFunction(networkWaitListen))
	L.SetField(mod, "set_tcp_secret_key", L.NewFunction(networkSetTCPSecretKey))
	L.SetField(mod, "set_udp_secret_key", L.NewFunction(networkSetUDPSecretKey))
	L.SetField(mod, "ensure_listener", L.NewFunction(networkEnsureListener))
	L.SetField(mod, "register_tcp_heartbeat", L.NewFunction(networkRegisterTCPHeartbeat))
	L.SetField(mod, "register_udp_heartbeat", L.NewFunction(networkRegisterUDPHeartbeat))
	L.SetField(mod, "get_tcp_secret_key", L.NewFunction(networkGetTCPSecretKey))
	L.SetField(mod, "get_udp_secret_key", L.NewFunction(networkGetUDPSecretKey))

	L.Push(mod)
	return 1
}

// networkConnectTCP 建立 TCP 连接。
// 签名：network.connect_tcp(service, address)
//
//	service: 连接名（如 "logic"、"battle"）
//	address: 服务器地址（如 "127.0.0.1:9001"）
func networkConnectTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	ok := ctx.NetSender.ConnectTCP(service, address)
	L.Push(lua.LBool(ok))
	return 1
}

// networkConnectUDP 建立 UDP 连接。
// 签名：network.connect_udp(service, address)
//
//	service: 连接名（如 "battle"）
//	address: 服务器地址（如 "127.0.0.1:9002"）
func networkConnectUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	ok := ctx.NetSender.ConnectUDP(service, address)
	L.Push(lua.LBool(ok))
	return 1
}

// networkExchangeKey 发送空包交换 TCP 密钥。
// 签名：network.exchange_key(service [, route])
//
//	service: TCP 连接名
//	route:   可选路由 table（如 {cmd=1, act=1}），nil 时使用默认交换路由
func networkExchangeKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)

	// 可选第 2 参数：route（table 或 nil）
	var goRoute any
	if L.GetTop() >= 2 {
		goRoute = luaValueToRoute(L.Get(2))
	}

	packet := ctx.Adapter.EncodeTCP(goRoute, nil, nil)
	if packet == nil {
		L.Push(lua.LBool(false))
		return 1
	}
	respKey := ctx.Adapter.ExpectedResponseKey(goRoute)

	respBody, ok := ctx.NetSender.TCPRequest(service, packet, respKey)
	if !ok || len(respBody) == 0 {
		L.Push(lua.LBool(false))
		return 1
	}

	ctx.NetSender.SetTCPSecretKey(service, respBody)
	L.Push(lua.LBool(true))
	return 1
}

// extractNetArgs 从 Lua 栈提取 service + route + msg + s2cProto。
// 支持两种调用方式（向后兼容）：
//
//	新：network.request("logic", {cmd=1, act=1}, c2sMsg, "RespProto")
//	旧：network.request("logic", 1, 1, c2sMsg, "RespProto")
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
			route = v // table 或 nil
		default:
			if s2cProto == "" {
				s2cProto = lua.LVAsString(v)
			}
		}
	}
	return
}

// buildPacket 构建完整数据包
func buildPacket(ctx *Context, service string, route lua.LValue, msgData []byte) []byte {
	secretKey := ctx.NetSender.GetTCPSecretKey(service)
	goRoute := luaValueToRoute(route)
	return ctx.Adapter.EncodeTCP(goRoute, msgData, secretKey)
}

// luaValueToRoute 将 Lua table/nil 转换为 Go any
func luaValueToRoute(v lua.LValue) any {
	if v == lua.LNil {
		return nil
	}
	if tbl, ok := v.(*lua.LTable); ok {
		result := make(map[string]any)
		tbl.ForEach(func(k, val lua.LValue) {
			key := lua.LVAsString(k)
			switch v := val.(type) {
			case lua.LNumber:
				n := float64(v)
				if n == math.Trunc(n) {
					result[key] = int64(n)
				} else {
					result[key] = n
				}
			case lua.LString:
				result[key] = string(v)
			case lua.LBool:
				result[key] = bool(v)
			}
		})
		return result
	}
	return nil
}

// serializeMsg 序列化 proto 消息为字节。
func serializeMsg(ctx *Context, msg proto.Message) ([]byte, error) {
	if msg == nil || ctx.Factory == nil {
		return nil, nil
	}
	return ctx.Factory.Serialize(msg)
}

// networkRequest TCP 请求-响应。
// 签名：network.request(service, route, msg [, s2c_proto])
//
//	route:     路由 table（如 {cmd=1, act=1}）
//	msg:       C2S proto 消息（由 proto.create 创建）
//	s2c_proto: 响应 proto 全名（可选，提供时解析为结构化消息）
//
// 返回：code(number), data(string|userdata)
//
//	code=0:  成功，data 为解析后的消息或原始字节
//	code=-1: 请求失败
//	code=-2: 解析失败，data 为原始字节
func networkRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service, route, msg, s2cProto := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.request requires (service, route, msg [, s2c_proto])")
		return 0
	}

	msgData, err := serializeMsg(ctx, msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}

	packet := buildPacket(ctx, service, route, msgData)
	if packet == nil {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNil)
		return 2
	}

	goRoute := luaValueToRoute(route)
	respKey := ctx.Adapter.ExpectedResponseKey(goRoute)

	// 释放 luaMu，避免 TCPRequest 阻塞期间阻塞心跳 builder
	if ctx.LuaMu != nil {
		ctx.LuaMu.Unlock()
	}
	respBody, ok := ctx.NetSender.TCPRequest(service, packet, respKey)
	// 重新获取 luaMu，后续操作需要 Lua 状态
	if ctx.LuaMu != nil {
		ctx.LuaMu.Lock()
	}

	if !ok {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNil)
		return 2
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			L.Push(lua.LNumber(-2))
			L.Push(lua.LString(string(respBody)))
			return 2
		}
		ud := wrapProtoMessage(L, respMsg)
		L.Push(lua.LNumber(0))
		L.Push(ud)
		return 2
	}

	L.Push(lua.LNumber(0))
	L.Push(lua.LString(string(respBody)))
	return 2
}

// networkHTTPPost 发送 HTTP POST 表单请求。
// 签名：network.http_post(path, form_data)
//
//	path:      请求路径（如 "/api/login"）
//	form_data: 表单数据 table（如 {key="value"}）
//
// 返回：status_code(number), body(string)
func networkHTTPPost(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	var path string
	var formData map[string]string

	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if s, ok := v.(lua.LString); ok && path == "" {
			path = string(s)
		} else if tb, ok := v.(*lua.LTable); ok {
			formData = make(map[string]string)
			tb.ForEach(func(k, val lua.LValue) {
				formData[lua.LVAsString(k)] = lua.LVAsString(val)
			})
		}
	}

	if path == "" {
		L.RaiseError("network.http_post requires (path, form_data)")
		return 0
	}

	statusCode, body, err := ctx.NetSender.HTTPPost(path, formData)
	if err != nil {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LString(err.Error()))
		return 2
	}

	L.Push(lua.LNumber(statusCode))
	L.Push(lua.LString(string(body)))
	return 2
}

// networkTCPSend TCP 发送（不等响应）。
// 签名：network.tcp_send(service, route, msg)
//
//	route: 路由 table（如 {cmd=6, act=5}）
//	msg:   C2S proto 消息（由 proto.create 创建）
//
// 返回：code(number) 0=成功, -1=失败
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

	packet := buildPacket(ctx, service, route, msgData)
	if packet == nil {
		L.Push(lua.LNumber(-1))
		return 1
	}

	ok, _ := ctx.NetSender.TCPSend(service, packet)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkUDPSend 发送原始 UDP 数据（不做编码）。
// 签名：network.udp_send(service, data)
//
//	service: UDP 连接名
//	data:    原始字节串
//
// 返回：code(number) 0=成功, -1=失败
func networkUDPSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	var data []byte
	for i := 2; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if data == nil {
			data = []byte(lua.LVAsString(v))
		}
	}

	if data == nil {
		L.RaiseError("network.udp_send requires (service, data)")
		return 0
	}

	ok, _ := ctx.NetSender.UDPSend(service, data)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkUDPSendMsg 发送 UDP 消息。
// 签名：network.udp_send_msg(service, route, body)
//
//	route: 路由 table（如 {cmd=4, act=11}）
//	body:  消息体字节
func networkUDPSendMsg(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	route := L.Get(2)
	var body []byte
	if L.GetTop() >= 3 {
		body = []byte(L.CheckString(3))
	}

	goRoute := luaValueToRoute(route)
	udpKey := ctx.NetSender.GetUDPSecretKey(L.CheckString(1))
	packet := ctx.Adapter.EncodeUDP(goRoute, body, udpKey)
	if packet == nil {
		L.Push(lua.LNumber(-1))
		return 1
	}
	ok, _ := ctx.NetSender.UDPSend(L.CheckString(1), packet)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkWaitListen 等待监听消息。
// 签名：network.wait_listen(service, route [, s2c_proto [, timeout_sec [, poll_ms]]])
//
//	route:       路由 table（如 {cmd=3, act=1}），通过 ExpectedResponseKey 计算响应键
//	s2c_proto:   响应 proto 全名（可选）
//	timeout_sec: 超时秒数（默认 60）
//	poll_ms:     轮询间隔毫秒（默认 100）
func networkWaitListen(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	route := luaValueToRoute(L.Get(2))
	responseKey := ctx.Adapter.ExpectedResponseKey(route)

	var s2cProto string
	timeout := 60
	pollMs := 100

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
		pollMs = 100
	}

	ctx.NetSender.EnsureTCPListener(service, responseKey)

	// 轮询期间释放 luaMu，允许心跳 builder 运行
	if ctx.LuaMu != nil {
		ctx.LuaMu.Unlock()
	}

	var respBody []byte
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	timedOut := false

	for time.Now().Before(deadline) {
		respBody = ctx.NetSender.GetTCPListenResp(service, responseKey)
		if respBody != nil {
			break
		}

		time.Sleep(time.Duration(pollMs) * time.Millisecond)

		if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
			break
		}
	}

	if respBody == nil {
		if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
			if ctx.LuaMu != nil {
				ctx.LuaMu.Lock()
			}
			L.Push(lua.LNil)
			return 1
		}
		timedOut = true
	}

	// 重新获取 luaMu，后续操作需要 Lua 状态
	if ctx.LuaMu != nil {
		ctx.LuaMu.Lock()
	}

	if timedOut {
		stresslog.Warn("[SCRIPT] wait_listen 超时",
			zap.String("service", service), zap.String("responseKey", responseKey), zap.Int("timeout", timeout))
		L.Push(lua.LNil)
		return 1
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(wrapProtoMessage(L, respMsg))
		return 1
	}

	L.Push(lua.LString(string(respBody)))
	return 1
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

// networkSetTCPSecretKey 设置 TCP 连接的加密密钥。
// 签名：network.set_tcp_secret_key(service, key)
//
//	service: TCP 连接名
//	key:     密钥字节串
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
//
//	service: UDP 连接名
//	key:     密钥字节串
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
//
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
//
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

// networkEnsureListener 为 responseKey 注册监听器占位。
// 签名：network.ensure_listener(service, response_key)
//
//	service:       连接名
//	response_key:  响应路由键（字符串）
func networkEnsureListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	responseKey := L.CheckString(2)
	ctx.NetSender.EnsureTCPListener(service, responseKey)
	return 0
}

// heartbeatProto 用于区分 TCP/UDP 心跳的编码方式
type heartbeatProto int

const (
	hbProtoTCP heartbeatProto = iota
	hbProtoUDP
)

// networkRegisterTCPHeartbeat 注册 TCP 心跳。
// 签名：network.register_heartbeat_tcp(service, interval_ms, route, builder)
//
//	   或：network.register_heartbeat_tcp(service, builder, route)
func networkRegisterTCPHeartbeat(L *lua.LState) int {
	return registerHeartbeat(L, hbProtoTCP)
}

// networkRegisterUDPHeartbeat 注册 UDP 心跳。
// 签名：network.register_heartbeat_udp(service, interval_ms, route, builder)
//
//	   或：network.register_heartbeat_udp(service, builder, route)
func networkRegisterUDPHeartbeat(L *lua.LState) int {
	return registerHeartbeat(L, hbProtoUDP)
}

// registerHeartbeat 心跳注册的共享实现。
// 解析参数、构建 builder 闭包、调用对应的注册方法。
func registerHeartbeat(L *lua.LState, proto heartbeatProto) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	protoName := "tcp"
	if proto == hbProtoUDP {
		protoName = "udp"
	}

	var goRoute any
	var intervalMs int
	var builderFn *lua.LFunction

	v2 := L.Get(2)
	if fn, ok := v2.(*lua.LFunction); ok {
		builderFn = fn
	} else {
		intervalMs = int(lua.LVAsNumber(v2))
		goRoute = luaValueToRoute(L.Get(3))
		if L.GetTop() >= 4 {
			if fn, ok := L.Get(4).(*lua.LFunction); ok {
				builderFn = fn
			}
		}
	}

	if intervalMs <= 0 {
		intervalMs = 3000
	}

	hbRegKey := fmt.Sprintf("__hb_%s_%s__", protoName, service)
	if builderFn != nil {
		L.SetField(L.Get(lua.RegistryIndex), hbRegKey, builderFn)
	}

	luaMu := ctx.LuaMu
	adp := ctx.Adapter
	builder := func() []byte {
		var body []byte
		if builderFn != nil && luaMu != nil {
			luaMu.Lock()
			savedTop := L.GetTop()
			if err := L.CallByParam(lua.P{Fn: builderFn, NRet: 1, Protect: true}); err != nil {
				L.SetTop(savedTop)
				luaMu.Unlock()
				stresslog.Warn("[SCRIPT] 心跳 builder Lua 调用失败",
					zap.String("proto", protoName), zap.String("service", service), zap.Error(err))
				return nil
			}
			ret := L.Get(-1)
			body = []byte(lua.LVAsString(ret))
			L.SetTop(savedTop)
			luaMu.Unlock()
		}

		if proto == hbProtoUDP {
			secretKey := ctx.NetSender.GetUDPSecretKey(service)
			return adp.EncodeUDP(goRoute, body, secretKey)
		}
		secretKey := ctx.NetSender.GetTCPSecretKey(service)
		return adp.EncodeTCP(goRoute, body, secretKey)
	}

	if proto == hbProtoUDP {
		ctx.NetSender.RegisterUDPHeartbeat(service, intervalMs, builder)
	} else {
		ctx.NetSender.RegisterTCPHeartbeat(service, intervalMs, builder)
	}
	return 0
}

// loadAdapterModule 暴露适配器编解码给 Lua 业务脚本。
func loadAdapterModule(L *lua.LState) int {
	mod := L.NewTable()
	L.SetField(mod, "encode_tcp", L.NewFunction(adapterEncodeTCP))
	L.SetField(mod, "encode_udp", L.NewFunction(adapterEncodeUDP))
	L.SetField(mod, "decode_tcp", L.NewFunction(adapterDecodeTCP))
	L.SetField(mod, "decode_udp", L.NewFunction(adapterDecodeUDP))
	L.SetField(mod, "expected_response_key", L.NewFunction(adapterExpectedResponseKey))
	L.Push(mod)
	return 1
}

// adapterEncodeTCP TCP 编码：将路由 + 消息体 + 密钥编码为完整数据包。
// 签名：adapter.encode_tcp(route, body [, key])
//
//	route: 路由 table（如 {cmd=1, act=1}）
//	body:  消息体字节串
//	key:   加密密钥（可选）
//
// 返回：packet(string|nil)
func adapterEncodeTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LNil)
		return 1
	}
	route := luaValueToRoute(L.Get(1))
	body := []byte(L.CheckString(2))
	var key []byte
	if L.GetTop() >= 3 {
		key = []byte(L.CheckString(3))
	}
	result := ctx.Adapter.EncodeTCP(route, body, key)
	if result == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(result)))
	}
	return 1
}

// adapterEncodeUDP UDP 编码：将路由 + 消息体 + 密钥编码为完整 UDP 数据包。
// 签名：adapter.encode_udp(route, body [, key])
//
//	route: 路由 table（如 {cmd=4, act=11}）
//	body:  消息体字节串
//	key:   加密密钥（可选）
//
// 返回：packet(string|nil)
func adapterEncodeUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LNil)
		return 1
	}
	route := luaValueToRoute(L.Get(1))
	body := []byte(L.CheckString(2))
	var key []byte
	if L.GetTop() >= 3 {
		key = []byte(L.CheckString(3))
	}
	result := ctx.Adapter.EncodeUDP(route, body, key)
	if result == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(result)))
	}
	return 1
}

// adapterDecodeTCP 解码 TCP 数据包：从加密数据中提取 responseKey 和消息体。
// 签名：adapter.decode_tcp(data [, key])
//
//	data: 加密数据字节串
//	key:  解密密钥（可选）
//
// 返回：response_key(string), body(string), header_err(number)
func adapterDecodeTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LNil)
		L.Push(lua.LNil)
		return 2
	}
	data := []byte(L.CheckString(1))
	var key []byte
	if L.GetTop() >= 2 {
		key = []byte(L.CheckString(2))
	}
	responseKey, body, headerErr := ctx.Adapter.DecodeTCP(data, key)
	L.Push(lua.LString(responseKey))
	L.Push(lua.LString(string(body)))
	L.Push(lua.LNumber(headerErr))
	return 3
}

// adapterDecodeUDP 解码 UDP 数据包：从加密数据中提取 responseKey 和消息体。
// 签名：adapter.decode_udp(data [, key])
//
//	data: 加密数据字节串
//	key:  解密密钥（可选）
//
// 返回：response_key(string), body(string), header_err(number)
func adapterDecodeUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LNil)
		L.Push(lua.LNil)
		return 2
	}
	data := []byte(L.CheckString(1))
	var key []byte
	if L.GetTop() >= 2 {
		key = []byte(L.CheckString(2))
	}
	responseKey, body, headerErr := ctx.Adapter.DecodeUDP(data, key)
	L.Push(lua.LString(responseKey))
	L.Push(lua.LString(string(body)))
	L.Push(lua.LNumber(headerErr))
	return 3
}

// adapterExpectedResponseKey 根据路由计算期望的响应路由键。
// 签名：adapter.expected_response_key(route)
//
//	route: 路由 table（如 {cmd=1, act=1}）
//
// 返回：response_key(string)
func adapterExpectedResponseKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LString(""))
		return 1
	}
	route := luaValueToRoute(L.Get(1))
	key := ctx.Adapter.ExpectedResponseKey(route)
	L.Push(lua.LString(key))
	return 1
}
