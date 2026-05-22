package script

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"stressbot/engine"
	stresslog "stressbot/utils/log"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// withReleasedMu 临时释放 mu，执行 fn 后重新获取。
// 通过 defer 保证即使 fn panic 也能重新获取 mu，防止锁状态不一致。
func withReleasedMu(mu *sync.Mutex, fn func()) {
	if mu != nil {
		mu.Unlock()
		defer mu.Lock()
	}
	fn()
}

// loadNetworkModule 加载 network 命名空间模块。
// Lua 用法：
//
//	local network = require("network")
//	network.connect_tcp(service, address)     → 建立 TCP 连接
//	network.connect_udp(service, address)     → 建立 UDP 连接
//	network.close_tcp(service)                → 关闭 TCP 连接
//	network.close_udp(service)                → 关闭 UDP 连接
//	network.tcp_request(service, route, msg [, s2c])
//	network.udp_request(service, route, body [, s2c [, timeout [, poll]]])
//	network.http_request(url [, method [, content_type [, body]]])
//	network.tcp_send(service, route, msg)
//	network.udp_send(service, route, body)
//	network.tcp_listen(service, route [, s2c [, timeout [, poll]]])
//	network.udp_listen(service, route [, s2c [, timeout [, poll]]])
//	network.set_tcp_secret_key(service, key)
//	network.set_udp_secret_key(service, key)
//	network.get_tcp_secret_key(service)
//	network.get_udp_secret_key(service)
//	network.ensure_tcp_listener(service, response_key)
//	network.ensure_udp_listener(service, response_key)
//	network.register_tcp_heartbeat(service, interval_ms, route, builder)
//	network.register_udp_heartbeat(service, interval_ms, route, builder)
func loadNetworkModule(L *lua.LState) int {
	mod := L.NewTable()

	// 连接
	L.SetField(mod, "connect_tcp", L.NewFunction(networkConnectTCP))
	L.SetField(mod, "connect_udp", L.NewFunction(networkConnectUDP))
	L.SetField(mod, "close_tcp", L.NewFunction(networkCloseTCP))
	L.SetField(mod, "close_udp", L.NewFunction(networkCloseUDP))
	// 请求-响应
	L.SetField(mod, "tcp_request", L.NewFunction(networkTCPRequest))
	L.SetField(mod, "udp_request", L.NewFunction(networkUDPRequest))
	L.SetField(mod, "http_request", L.NewFunction(networkHTTPRequest))
	// 发送
	L.SetField(mod, "tcp_send", L.NewFunction(networkTCPSend))
	L.SetField(mod, "udp_send", L.NewFunction(networkUDPSend))
	// 监听
	L.SetField(mod, "tcp_listen", L.NewFunction(networkTCPListen))
	L.SetField(mod, "udp_listen", L.NewFunction(networkUDPListen))
	// 密钥
	L.SetField(mod, "set_tcp_secret_key", L.NewFunction(networkSetTCPSecretKey))
	L.SetField(mod, "set_udp_secret_key", L.NewFunction(networkSetUDPSecretKey))
	L.SetField(mod, "get_tcp_secret_key", L.NewFunction(networkGetTCPSecretKey))
	L.SetField(mod, "get_udp_secret_key", L.NewFunction(networkGetUDPSecretKey))
	// 监听器占位
	L.SetField(mod, "ensure_tcp_listener", L.NewFunction(networkEnsureTCPListener))
	L.SetField(mod, "ensure_udp_listener", L.NewFunction(networkEnsureUDPListener))
	// 心跳
	L.SetField(mod, "register_tcp_heartbeat", L.NewFunction(networkRegisterTCPHeartbeat))
	L.SetField(mod, "register_udp_heartbeat", L.NewFunction(networkRegisterUDPHeartbeat))

	L.Push(mod)
	return 1
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// pushRequestResult 将请求结果压入 Lua 栈，消除重复的 4 行 push 模式。
func pushRequestResult(L *lua.LState, code int, data lua.LValue, sent, recv int) int {
	L.Push(lua.LNumber(code))
	L.Push(data)
	L.Push(lua.LNumber(sent))
	L.Push(lua.LNumber(recv))
	return 4
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

// buildPacket 构建完整 TCP 数据包
func buildPacket(ctx *Context, service string, route lua.LValue, msgData []byte) []byte {
	secretKey := ctx.NetSender.GetTCPSecretKey(service)
	goRoute := luaValueToRoute(route)
	return ctx.Adapter.EncodeTCP(goRoute, msgData, secretKey)
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
func networkConnectTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	err := ctx.NetSender.ConnectTCP(service, address)
	L.Push(lua.LBool(err == nil))
	return 1
}

// networkConnectUDP 建立 UDP 连接。
// 签名：network.connect_udp(service, address)
func networkConnectUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}
	service := L.CheckString(1)
	address := L.CheckString(2)
	err := ctx.NetSender.ConnectUDP(service, address)
	L.Push(lua.LBool(err == nil))
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

// ---------------------------------------------------------------------------
// 请求-响应
// ---------------------------------------------------------------------------

// networkTCPRequest TCP 请求-响应。
// 签名：network.tcp_request(service, route, msg [, s2c_proto])
//
// 返回：code(number), data(string|userdata|nil), sent(number), recv(number)
// code=0 成功 / -1 请求失败 / -2 解析失败
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

	timeout := engine.DefaultRequestTimeoutSec
	if L.GetTop() >= 5 {
		timeout = L.CheckInt(5)
	}

	msgData, err := serializeMsg(ctx, msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}

	packet := buildPacket(ctx, service, route, msgData)
	if packet == nil {
		return pushRequestResult(L, -1, lua.LNil, 0, 0)
	}

	goRoute := luaValueToRoute(route)
	routeKey := ctx.Adapter.ExpectedRouteKey(goRoute)
	pktLen := len(packet)

	var respBody []byte
	var headerErr uint64
	var reqErr error
	withReleasedMu(ctx.LuaMu, func() {
		respBody, headerErr, reqErr = ctx.NetSender.TCPRequest(service, packet, routeKey,
			time.Duration(timeout)*time.Second)
	})

	if reqErr != nil {
		return pushRequestResult(L, -1, lua.LNil, pktLen, 0)
	}
	if headerErr != 0 {
		return pushRequestResult(L, int(headerErr), lua.LString(string(respBody)), pktLen, len(respBody))
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			return pushRequestResult(L, -2, lua.LString(string(respBody)), pktLen, len(respBody))
		}
		return pushRequestResult(L, 0, wrapProtoMessage(L, respMsg), pktLen, len(respBody))
	}

	return pushRequestResult(L, 0, lua.LString(string(respBody)), pktLen, len(respBody))
}

// networkUDPRequest UDP 请求-响应。
// 签名：network.udp_request(service, route, body [, s2c_proto [, timeout_sec [, poll_ms]]])
//
// 返回：code(number), data(string|userdata|nil), sent(number), recv(number)
func networkUDPRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
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
	timeout := engine.DefaultRequestTimeoutSec
	if L.GetTop() >= 5 {
		timeout = L.CheckInt(5)
	}

	goRoute := luaValueToRoute(route)
	routeKey := ctx.Adapter.ExpectedRouteKey(goRoute)
	udpKey := ctx.NetSender.GetUDPSecretKey(service)
	packet := ctx.Adapter.EncodeUDP(goRoute, body, udpKey)
	if packet == nil {
		return pushRequestResult(L, -1, lua.LNil, 0, 0)
	}

	pktLen := len(packet)
	var respBody []byte
	var headerErr uint64
	var reqErr error

	withReleasedMu(ctx.LuaMu, func() {
		respBody, headerErr, reqErr = ctx.NetSender.UDPRequest(
			service, packet, routeKey,
			time.Duration(timeout)*time.Second,
		)
	})

	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		return pushRequestResult(L, -1, lua.LNil, pktLen, 0)
	}
	if reqErr != nil {
		return pushRequestResult(L, -1, lua.LNil, pktLen, 0)
	}
	if headerErr != 0 {
		return pushRequestResult(L, int(headerErr), lua.LString(string(respBody)), pktLen, len(respBody))
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			return pushRequestResult(L, -2, lua.LString(string(respBody)), pktLen, len(respBody))
		}
		return pushRequestResult(L, 0, wrapProtoMessage(L, respMsg), pktLen, len(respBody))
	}

	return pushRequestResult(L, 0, lua.LString(string(respBody)), pktLen, len(respBody))
}

// networkHTTPRequest 发送 HTTP 请求。
// 签名：network.http_request(url [, method [, content_type [, body]]])
//
// 返回：status_code(number), body(string), sent(number), recv(number)
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
	sent := 0
	if bodyTable != nil {
		formData := make(map[string]string)
		bodyTable.ForEach(func(k, val lua.LValue) {
			formData[lua.LVAsString(k)] = lua.LVAsString(val)
		})

		switch contentType {
		case "json":
			jsonBytes, err := json.Marshal(formData)
			if err != nil {
				L.RaiseError("json marshal failed: %v", err)
				return 0
			}
			reqBody = jsonBytes
			sent = len(reqBody)
		default: // "form"
			values := make(url.Values)
			for k, v := range formData {
				values.Set(k, v)
				sent += len(k) + 1 + len(v) + 1
			}
			reqBody = []byte(values.Encode())
		}
	}

	var statusCode int
	var respBody []byte
	var err error
	withReleasedMu(ctx.LuaMu, func() {
		statusCode, respBody, err = ctx.NetSender.HTTPRequest(reqURL, method, contentType, reqBody)
	})
	if err != nil {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LString(err.Error()))
		L.Push(lua.LNumber(sent))
		L.Push(lua.LNumber(0))
		return 4
	}

	L.Push(lua.LNumber(statusCode))
	L.Push(lua.LString(string(respBody)))
	L.Push(lua.LNumber(sent))
	L.Push(lua.LNumber(len(respBody)))
	return 4
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

// networkTCPSend TCP 发送（不等响应）。
// 签名：network.tcp_send(service, route, msg)
//
// 返回：code(number), sent(number)
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
		L.Push(lua.LNumber(0))
		return 2
	}

	n, err := ctx.NetSender.TCPSend(service, packet)
	if err == nil {
		L.Push(lua.LNumber(0))
		L.Push(lua.LNumber(n))
	} else {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNumber(len(packet)))
	}
	return 2
}

// networkUDPSend 发送 UDP 编码消息。
// 签名：network.udp_send(service, route, body)
//
// 返回：code(number), sent(number)
func networkUDPSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	route := L.Get(2)
	var body []byte
	if L.GetTop() >= 3 {
		body = []byte(L.CheckString(3))
	}

	goRoute := luaValueToRoute(route)
	udpKey := ctx.NetSender.GetUDPSecretKey(service)
	packet := ctx.Adapter.EncodeUDP(goRoute, body, udpKey)
	if packet == nil {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNumber(0))
		return 2
	}
	n, err := ctx.NetSender.UDPSend(service, packet)
	if err == nil {
		L.Push(lua.LNumber(0))
		L.Push(lua.LNumber(n))
	} else {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNumber(len(packet)))
	}
	return 2
}

// ---------------------------------------------------------------------------
// 监听
// ---------------------------------------------------------------------------

// networkTCPListen 等待 TCP 监听消息。
// 签名：network.tcp_listen(service, route [, s2c_proto [, timeout_sec [, poll_ms]]])
//
// 返回：data(string|userdata|nil), recv(number)
func networkTCPListen(L *lua.LState) int { return networkListen(L, "tcp") }

// networkUDPListen 等待 UDP 监听消息。
// 签名：network.udp_listen(service, route [, s2c_proto [, timeout_sec [, poll_ms]]])
//
// 返回：data(string|userdata|nil), recv(number)
func networkUDPListen(L *lua.LState) int { return networkListen(L, "udp") }

// networkListen 通用监听实现，通过 protocol 参数区分 TCP/UDP。
func networkListen(L *lua.LState, protocol string) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	route := luaValueToRoute(L.Get(2))
	routeKey := ctx.Adapter.ExpectedRouteKey(route)

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

	if protocol == "tcp" {
		ctx.NetSender.EnsureTCPListener(service, routeKey)
	} else {
		ctx.NetSender.EnsureUDPListener(service, routeKey)
	}

	var respBody []byte
	var timedOut bool
	var headerErr uint64

	withReleasedMu(ctx.LuaMu, func() {
		deadline := time.Now().Add(time.Duration(timeout) * time.Second)
		for time.Now().Before(deadline) {
			if protocol == "tcp" {
				respBody, headerErr = ctx.NetSender.GetTCPListenResp(service, routeKey)
			} else {
				respBody, headerErr = ctx.NetSender.GetUDPListenResp(service, routeKey)
			}
			if respBody != nil {
				return
			}
			time.Sleep(time.Duration(pollMs) * time.Millisecond)
			if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
				return
			}
		}
		if respBody == nil {
			timedOut = true
		}
	})

	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		L.Push(lua.LNil)
		L.Push(lua.LNumber(0))
		return 2
	}
	if timedOut {
		stresslog.Debug("[SCRIPT] "+protocol+"_listen 超时",
			zap.String("service", service), zap.String("routeKey", routeKey), zap.Int("timeout", timeout))
		L.Push(lua.LNil)
		L.Push(lua.LNumber(0))
		return 2
	}
	if headerErr != 0 {
		L.Push(lua.LString(string(respBody)))
		L.Push(lua.LNumber(len(respBody)))
		return 2
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LNumber(len(respBody)))
			return 2
		}
		L.Push(wrapProtoMessage(L, respMsg))
		L.Push(lua.LNumber(len(respBody)))
		return 2
	}

	L.Push(lua.LString(string(respBody)))
	L.Push(lua.LNumber(len(respBody)))
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
// 签名：network.ensure_tcp_listener(service, response_key)
func networkEnsureTCPListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	routeKey := L.CheckString(2)
	ctx.NetSender.EnsureTCPListener(service, routeKey)
	return 0
}

// networkEnsureUDPListener 为 UDP routeKey 注册监听器占位。
// 签名：network.ensure_udp_listener(service, response_key)
func networkEnsureUDPListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	routeKey := L.CheckString(2)
	ctx.NetSender.EnsureUDPListener(service, routeKey)
	return 0
}

// ---------------------------------------------------------------------------
// 心跳
// ---------------------------------------------------------------------------

// heartbeatProto 区分 TCP/UDP 心跳的编码方式。
type heartbeatProto int

const (
	hbProtoTCP heartbeatProto = iota
	hbProtoUDP
)

// networkRegisterTCPHeartbeat 注册 TCP 心跳。
// 签名：network.register_tcp_heartbeat(service, interval_ms, route, builder)
func networkRegisterTCPHeartbeat(L *lua.LState) int {
	return registerHeartbeat(L, hbProtoTCP)
}

// networkRegisterUDPHeartbeat 注册 UDP 心跳。
// 签名：network.register_udp_heartbeat(service, interval_ms, route, builder)
func networkRegisterUDPHeartbeat(L *lua.LState) int {
	return registerHeartbeat(L, hbProtoUDP)
}

// registerHeartbeat 心跳注册的共享实现。
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
		intervalMs = engine.DefaultHeartbeatMs
	}

	hbRegKey := fmt.Sprintf("__hb_%s_%s__", protoName, service)
	if builderFn != nil {
		L.SetField(L.Get(lua.RegistryIndex), hbRegKey, builderFn)
	}

	luaMu := ctx.LuaMu
	adp := ctx.Adapter
	builder := func() []byte {
		if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
			return nil
		}
		var body []byte
		if builderFn != nil && luaMu != nil {
			func() {
				luaMu.Lock()
				defer luaMu.Unlock()
				savedTop := L.GetTop()
				if err := L.CallByParam(lua.P{Fn: builderFn, NRet: 1, Protect: true}); err != nil {
					L.SetTop(savedTop)
					stresslog.Warn("[SCRIPT] 心跳 builder Lua 调用失败",
						zap.String("proto", protoName), zap.String("service", service), zap.Error(err))
					return
				}
				ret := L.Get(-1)
				body = []byte(lua.LVAsString(ret))
				L.SetTop(savedTop)
			}()
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

// ---------------------------------------------------------------------------
// adapter 模块（编解码适配器，高级用法）
// ---------------------------------------------------------------------------

// loadAdapterModule 暴露适配器编解码给 Lua 业务脚本。
func loadAdapterModule(L *lua.LState) int {
	mod := L.NewTable()
	L.SetField(mod, "encode_tcp", L.NewFunction(adapterEncodeTCP))
	L.SetField(mod, "encode_udp", L.NewFunction(adapterEncodeUDP))
	L.SetField(mod, "decode_tcp", L.NewFunction(adapterDecodeTCP))
	L.SetField(mod, "decode_udp", L.NewFunction(adapterDecodeUDP))
	L.SetField(mod, "expected_route_key", L.NewFunction(adapterExpectedRouteKey))
	L.Push(mod)
	return 1
}

// adapterEncodeTCP TCP 编码。
// 签名：adapter.encode_tcp(route, body [, key])
// 返回：packet(string|nil)
func adapterEncodeTCP(L *lua.LState) int { return adapterEncode(L, "tcp") }

// adapterEncodeUDP UDP 编码。
// 签名：adapter.encode_udp(route, body [, key])
// 返回：packet(string|nil)
func adapterEncodeUDP(L *lua.LState) int { return adapterEncode(L, "udp") }

// adapterEncode 通用编码实现。
func adapterEncode(L *lua.LState, protocol string) int {
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

	var result []byte
	switch protocol {
	case "tcp":
		result = ctx.Adapter.EncodeTCP(route, body, key)
	default:
		result = ctx.Adapter.EncodeUDP(route, body, key)
	}

	if result == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(result)))
	}
	return 1
}

// adapterDecodeTCP 解码 TCP 数据包。
// 签名：adapter.decode_tcp(data [, key])
// 返回：response_key(string), body(string), header_err(number)
func adapterDecodeTCP(L *lua.LState) int { return adapterDecode(L, "tcp") }

// adapterDecodeUDP 解码 UDP 数据包。
// 签名：adapter.decode_udp(data [, key])
// 返回：response_key(string), body(string), header_err(number)
func adapterDecodeUDP(L *lua.LState) int { return adapterDecode(L, "udp") }

// adapterDecode 通用解码实现。
func adapterDecode(L *lua.LState, protocol string) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LNil)
		L.Push(lua.LNil)
		L.Push(lua.LNumber(0))
		return 3
	}
	data := []byte(L.CheckString(1))
	var key []byte
	if L.GetTop() >= 2 {
		key = []byte(L.CheckString(2))
	}

	var routeKey string
	var body []byte
	var headerErr uint64
	switch protocol {
	case "tcp":
		routeKey, body, headerErr = ctx.Adapter.DecodeTCP(data, key)
	default:
		routeKey, body, headerErr = ctx.Adapter.DecodeUDP(data, key)
	}

	L.Push(lua.LString(routeKey))
	L.Push(lua.LString(string(body)))
	L.Push(lua.LNumber(headerErr))
	return 3
}

// adapterExpectedRouteKey 根据路由计算期望的响应路由键。
// 签名：adapter.expected_route_key(route)
// 返回：response_key(string)
func adapterExpectedRouteKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Adapter == nil {
		L.Push(lua.LString(""))
		return 1
	}
	route := luaValueToRoute(L.Get(1))
	key := ctx.Adapter.ExpectedRouteKey(route)
	L.Push(lua.LString(key))
	return 1
}
