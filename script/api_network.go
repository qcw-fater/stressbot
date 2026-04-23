package script

import (
	"fmt"
	"math"
	"time"

	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"

	"google.golang.org/protobuf/proto"
)

func loadNetworkModule(L *lua.LState) int {
	mod := L.NewTable()

	L.SetField(mod, "connect_tcp", L.NewFunction(networkConnectTCP))
	L.SetField(mod, "connect_udp", L.NewFunction(networkConnectUDP))
	L.SetField(mod, "close_tcp", L.NewFunction(networkCloseTCP))
	L.SetField(mod, "close_udp", L.NewFunction(networkCloseUDP))
	L.SetField(mod, "exchange_key", L.NewFunction(networkExchangeKey))
	L.SetField(mod, "request", L.NewFunction(networkRequest))
	L.SetField(mod, "send", L.NewFunction(networkSend))
	L.SetField(mod, "http_post", L.NewFunction(networkHTTPPost))
	L.SetField(mod, "udp_send", L.NewFunction(networkUDPSend))
	L.SetField(mod, "udp_send_msg", L.NewFunction(networkUDPSendMsg))
	L.SetField(mod, "wait_listen", L.NewFunction(networkWaitListen))
	L.SetField(mod, "request_wait", L.NewFunction(networkRequestWait))
	L.SetField(mod, "set_secret_key", L.NewFunction(networkSetSecretKey))
	L.SetField(mod, "set_udp_secret_key", L.NewFunction(networkSetUDPSecretKey))
	L.SetField(mod, "ensure_listener", L.NewFunction(networkEnsureListener))
	L.SetField(mod, "register_heartbeat", L.NewFunction(networkRegisterHeartbeat))
	L.SetField(mod, "get_secret_key", L.NewFunction(networkGetSecretKey))
	L.SetField(mod, "get_udp_secret_key", L.NewFunction(networkGetUDPSecretKey))

	L.Push(mod)
	return 1
}

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

// networkExchangeKey 发送空包获取密钥
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

	packet := ctx.Adapter.Encode(goRoute, nil, nil)
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

	ctx.NetSender.SetSecretKey(service, respBody)
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
	secretKey := ctx.NetSender.GetSecretKey(service)
	goRoute := luaValueToRoute(route)
	return ctx.Adapter.Encode(goRoute, msgData, secretKey)
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

func serializeMsg(ctx *Context, msg proto.Message) ([]byte, error) {
	if msg == nil || ctx.Factory == nil {
		return nil, nil
	}
	return ctx.Factory.Serialize(msg)
}

// networkRequest TCP 请求-响应
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

	respBody, ok := ctx.NetSender.TCPRequest(service, packet, respKey)
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

// networkSend TCP 发送（不等响应）
func networkSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service, route, msg, _ := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.send requires (service, route, msg)")
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

	ok := ctx.NetSender.UDPSend(service, data)
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
	ok := ctx.NetSender.UDPSend(L.CheckString(1), packet)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkWaitListen 等待监听消息。
// 签名：network.wait_listen(service, route [, s2c_proto [, timeout_sec]])
//
//	route:       路由 table（如 {cmd=3, act=1}），通过 ExpectedResponseKey 计算响应键
//	s2c_proto:   响应 proto 全名（可选）
//	timeout_sec: 超时秒数（默认 60）
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

	if L.GetTop() >= 3 {
		s2cProto = L.CheckString(3)
	}
	if L.GetTop() >= 4 {
		timeout = L.CheckInt(4)
	}

	ctx.NetSender.EnsureListener(service, responseKey)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		respBody := ctx.NetSender.GetListenResp(service, responseKey)
		if respBody != nil {
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

		time.Sleep(100 * time.Millisecond)

		if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
			L.Push(lua.LNil)
			return 1
		}
	}

	stresslog.Warn("[SCRIPT] wait_listen 超时",
		zap.String("service", service), zap.String("responseKey", responseKey), zap.Int("timeout", timeout))
	L.Push(lua.LNil)
	return 1
}

func networkCloseTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	ctx.NetSender.CloseTCP(service)
	return 0
}

func networkCloseUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	ctx.NetSender.CloseUDP(service)
	return 0
}

func networkSetSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	key := []byte(L.CheckString(2))
	ctx.NetSender.SetSecretKey(service, key)
	return 0
}

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

func networkGetSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.Push(lua.LNil)
		return 1
	}
	service := L.CheckString(1)
	key := ctx.NetSender.GetSecretKey(service)
	if key == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(key)))
	}
	return 1
}

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

// networkEnsureListener 为 responseKey 注册监听器占位
func networkEnsureListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	// 支持旧格式 (cmd, act) 和新格式 (responseKey)
	v2 := L.Get(2)
	if _, isNum := v2.(lua.LNumber); isNum {
		cmd := int(lua.LVAsNumber(v2))
		act := L.CheckInt(3)
		responseKey := fmt.Sprintf("%d:%d", cmd, act)
		ctx.NetSender.EnsureListener(service, responseKey)
	} else {
		responseKey := lua.LVAsString(v2)
		ctx.NetSender.EnsureListener(service, responseKey)
	}
	return 0
}

// networkRegisterHeartbeat 注册心跳。
// 签名：network.register_heartbeat(target, service, interval_ms, route, builder)
//	   或：network.register_heartbeat(target, service, builder, route)
//
//	target:     "tcp" 或 "udp"
//	service:    服务名
//	interval_ms: 心跳间隔毫秒
//	route:      路由 table（如 {cmd=2, act=1}）
//	builder:    心跳体构建函数（可选）
func networkRegisterHeartbeat(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	target := L.CheckString(1)
	service := L.CheckString(2)

	var goRoute any
	var intervalMs int
	var builderFn *lua.LFunction

	v3 := L.Get(3)
	if fn, ok := v3.(*lua.LFunction); ok {
		builderFn = fn
	} else {
		intervalMs = int(lua.LVAsNumber(v3))
		goRoute = luaValueToRoute(L.Get(4))
		if L.GetTop() >= 5 {
			if fn, ok := L.Get(5).(*lua.LFunction); ok {
				builderFn = fn
			}
		}
	}

	hbRegKey := fmt.Sprintf("__hb_%s_%s_%d__", target, service, time.Now().UnixNano())
	if builderFn != nil {
		L.SetField(L.Get(lua.RegistryIndex), hbRegKey, builderFn)
	}

	luaMu := ctx.LuaMu
	adp := ctx.Adapter
	builder := func() []byte {
		if luaMu != nil {
			luaMu.Lock()
			defer luaMu.Unlock()
		}

		var body []byte
		if builderFn != nil {
			savedTop := L.GetTop()
			defer L.SetTop(savedTop)

			if err := L.CallByParam(lua.P{Fn: builderFn, NRet: 1, Protect: true}); err != nil {
				return nil
			}
			ret := L.Get(-1)
			body = []byte(lua.LVAsString(ret))
		}

		if target == "udp" {
			udpKey := ctx.NetSender.GetUDPSecretKey(service)
			return adp.EncodeUDP(goRoute, body, udpKey)
		}
		secretKey := ctx.NetSender.GetSecretKey(service)
		return adp.Encode(goRoute, body, secretKey)
	}

	ctx.NetSender.RegisterHeartbeat(target, service, intervalMs, builder)
	return 0
}

// networkRequestWait 发送请求并等待指定响应路由。
// 签名：network.request_wait(service, send_route, msg, resp_route, s2c_proto)
//
//	send_route: 发送路由 table（如 {cmd=2, act=16}）
//	resp_route: 期望的响应路由 table（如 {cmd=1, act=2}），nil 时使用 send_route
//	s2c_proto:  响应 proto 全名
func networkRequestWait(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Adapter == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	sendRoute := luaValueToRoute(L.Get(2))

	var msg proto.Message
	var respRouteLua lua.LValue
	var s2cProto string

	for i := 3; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if ud, ok := v.(*lua.LUserData); ok {
			if pm, ok := ud.Value.(proto.Message); ok {
				msg = pm
			}
			continue
		}
		if tbl, ok := v.(*lua.LTable); ok && respRouteLua == nil {
			respRouteLua = tbl
			continue
		}
		if s2cProto == "" {
			s2cProto = lua.LVAsString(v)
		}
	}

	msgData, err := serializeMsg(ctx, msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}

	secretKey := ctx.NetSender.GetSecretKey(service)
	packet := ctx.Adapter.Encode(sendRoute, msgData, secretKey)
	if packet == nil {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNil)
		return 2
	}

	respRouteGo := luaValueToRoute(respRouteLua)
	if respRouteGo == nil {
		respRouteGo = sendRoute
	}
	respKey := ctx.Adapter.ExpectedResponseKey(respRouteGo)
	respBody, ok := ctx.NetSender.TCPRequest(service, packet, respKey)
	if !ok {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNil)
		return 2
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			stresslog.Warn("[SCRIPT] request_wait 解析失败",
				zap.String("proto", s2cProto), zap.Int("bodyLen", len(respBody)), zap.Error(err))
			L.Push(lua.LNumber(-2))
			L.Push(lua.LString(err.Error()))
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

// loadAdapterModule 暴露适配器编解码给 Lua 业务脚本。
func loadAdapterModule(L *lua.LState) int {
	mod := L.NewTable()
	L.SetField(mod, "encode", L.NewFunction(adapterEncode))
	L.SetField(mod, "encode_udp", L.NewFunction(adapterEncodeUDP))
	L.SetField(mod, "decode", L.NewFunction(adapterDecode))
	L.SetField(mod, "expected_response_key", L.NewFunction(adapterExpectedResponseKey))
	L.Push(mod)
	return 1
}

func adapterEncode(L *lua.LState) int {
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
	result := ctx.Adapter.Encode(route, body, key)
	if result == nil {
		L.Push(lua.LNil)
	} else {
		L.Push(lua.LString(string(result)))
	}
	return 1
}

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

func adapterDecode(L *lua.LState) int {
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
	responseKey, body, _ := ctx.Adapter.Decode(data, key)
	L.Push(lua.LString(responseKey))
	L.Push(lua.LString(string(body)))
	return 2
}

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
