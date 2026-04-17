package script

import (
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"

	"google.golang.org/protobuf/proto"
)

// 说明：心跳 builder 的并发保护改为使用 ScriptContext.LuaMu
// （每个 Robot 独占 LState，与主流程/监听回调共享同一把锁）。

// loadNetworkModule 加载 network 命名空间模块。
// Lua 用法：
//
//	local network = require("network")
//	-- TCP 连接
//	network.connect_tcp("logic", "127.0.0.1:8080")
//	-- 密钥交换
//	network.exchange_key("logic")
//	-- TCP 请求-响应
//	local code, resp = network.request("logic", 1, 1, c2s_msg, "LoginPlayerS2C")
//	-- TCP 发送不等待
//	network.send("logic", 6, 15, c2s_msg)
//	-- HTTP POST
//	local code, body = network.http_post("/login", form_data)
//	-- UDP 发送
//	network.udp_send(data)
//	-- UDP 连接
//	network.connect_udp("127.0.0.1:9090")
//	-- 等待监听消息
//	local resp = network.wait_listen("logic", 3, 1, "MatchSucceedS2C", 600)
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

	L.Push(mod)
	return 1
}

// networkConnectTCP network.connect_tcp(service, address) — 建立 TCP 连接
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

// networkConnectUDP network.connect_udp(address) — 建立 UDP 连接
func networkConnectUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	address := L.CheckString(1)

	ok := ctx.NetSender.ConnectUDP(address)
	L.Push(lua.LBool(ok))
	return 1
}

// networkExchangeKey network.exchange_key(service) — 密钥交换
// 发送空消息（CMD=0, ACT=0）获取通信密钥，并设置到连接上。
func networkExchangeKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Protocol == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)

	// 构建空报文（CMD=0, ACT=0，无加密）
	packet := ctx.Protocol.BuildPacket(0, 0, nil, nil)

	// 发送并等待响应
	respBody, ok := ctx.NetSender.TCPRequest(service, 0, 0, packet)
	if !ok || len(respBody) == 0 {
		L.Push(lua.LBool(false))
		return 1
	}

	// 设置密钥到连接
	ctx.NetSender.SetSecretKey(service, respBody)

	L.Push(lua.LBool(true))
	return 1
}

// extractNetArgs 从 Lua 栈提取网络操作参数。
// 返回: service, cmd, act, protoMsg, s2cProtoName
func extractNetArgs(L *lua.LState) (string, uint8, uint8, proto.Message, string) {
	top := L.GetTop()
	var service string
	var cmd, act uint8
	var msg proto.Message
	var s2cProto string

	argIdx := 0
	for i := 1; i <= top; i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			// 检查是否是 proto.Message
			if ud, ok := v.(*lua.LUserData); ok {
				if pm, ok := ud.Value.(proto.Message); ok {
					msg = pm
					continue
				}
			}
			continue // 跳过其他 userdata
		}

		argIdx++
		switch argIdx {
		case 1:
			service = lua.LVAsString(v)
		case 2:
			cmd = uint8(lua.LVAsNumber(v))
		case 3:
			act = uint8(lua.LVAsNumber(v))
		default:
			// 第 4+ 个字符串参数为 S2C proto 名称
			if s2cProto == "" {
				s2cProto = lua.LVAsString(v)
			}
		}
	}

	return service, cmd, act, msg, s2cProto
}

// networkRequest network.request(service, cmd, act, c2s_msg [, s2c_proto]) — TCP 请求-响应
func networkRequest(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service, cmd, act, msg, s2cProto := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.request requires (service, cmd, act, msg [, s2c_proto])")
		return 0
	}

	// 构建发送报文
	packet, err := buildPacket(ctx, service, cmd, act, msg)
	if err != nil {
		L.RaiseError("build packet failed: %v", err)
		return 0
	}

	// 发送请求并等待响应
	respBody, ok := ctx.NetSender.TCPRequest(service, cmd, act, packet)
	if !ok {
		L.Push(lua.LNumber(-1)) // code = -1 表示发送失败
		L.Push(lua.LNil)
		return 2
	}

	// 解析响应
	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			L.Push(lua.LNumber(-2)) // code = -2 表示解析失败
			L.Push(lua.LString(string(respBody)))
			return 2
		}

		ud := L.NewUserData()
		ud.Value = respMsg
		mt := L.NewTable()
		L.SetField(mt, "__index", L.NewFunction(protoMsgIndex))
		L.SetMetatable(ud, mt)

		L.Push(lua.LNumber(0)) // code = 0 表示成功
		L.Push(ud)
		return 2
	}

	// 没有指定 S2C proto，返回原始字节
	L.Push(lua.LNumber(0))
	L.Push(lua.LString(string(respBody)))
	return 2
}

// networkSend network.send(service, cmd, act, msg) — TCP 发送（不等响应）
func networkSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	service, cmd, act, msg, _ := extractNetArgs(L)
	if service == "" {
		L.RaiseError("network.send requires (service, cmd, act, msg)")
		return 0
	}

	packet, err := buildPacket(ctx, service, cmd, act, msg)
	if err != nil {
		L.RaiseError("build packet failed: %v", err)
		return 0
	}

	ok, _ := ctx.NetSender.TCPSend(service, cmd, act, packet)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkHTTPPost network.http_post(path, form_data) — HTTP POST
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

// networkUDPSend network.udp_send(data) — UDP 发送
func networkUDPSend(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	var data []byte
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if data == nil {
			data = []byte(lua.LVAsString(v))
		}
	}

	if data == nil {
		L.RaiseError("network.udp_send requires (data)")
		return 0
	}

	ok := ctx.NetSender.UDPSend(data)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkUDPSendMsg network.udp_send_msg(cmd, act, body) — UDP 发送带协议头的报文
func networkUDPSendMsg(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		L.RaiseError("network not available")
		return 0
	}

	cmd := uint8(L.CheckInt(1))
	act := uint8(L.CheckInt(2))

	var body []byte
	if L.GetTop() >= 3 {
		body = []byte(L.CheckString(3))
	}

	ok := ctx.NetSender.UDPSendPacket(cmd, act, body)
	if ok {
		L.Push(lua.LNumber(0))
	} else {
		L.Push(lua.LNumber(-1))
	}
	return 1
}

// networkWaitListen network.wait_listen(service, cmd, act [, s2c_proto [, timeout_sec]]) — 等待监听消息
// 轮询 GetListenResp，收到消息后解析并返回 proto 对象。
// 返回: proto_msg 或 nil
func networkWaitListen(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Protocol == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	cmd := uint8(L.CheckInt(2))
	act := uint8(L.CheckInt(3))

	var s2cProto string
	timeout := 60 // 默认 60 秒

	top := L.GetTop()
	if top >= 4 {
		s2cProto = L.CheckString(4)
	}
	if top >= 5 {
		timeout = L.CheckInt(5)
	}

	cmdAct := ctx.Protocol.CmdAct(cmd, act)

	// 确保该 cmd/act 已注册监听（动态注册 battle 等连接的监听器）
	ctx.NetSender.EnsureListener(service, cmd, act)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		respBody := ctx.NetSender.GetListenResp(service, cmdAct)
		if respBody != nil {
			if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
				respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
				if err != nil {
					L.Push(lua.LNil)
					return 1
				}
				ud := L.NewUserData()
				ud.Value = respMsg
				mt := L.NewTable()
				L.SetField(mt, "__index", L.NewFunction(protoMsgIndex))
				L.SetMetatable(ud, mt)
				L.Push(ud)
				return 1
			}
			// 无 proto 解析，返回原始字节
			L.Push(lua.LString(string(respBody)))
			return 1
		}

		// 短暂休眠避免 CPU 空转
		time.Sleep(100 * time.Millisecond)

		// 检查上下文是否已取消
		if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
			L.Push(lua.LNil)
			return 1
		}
	}

	// 超时
	fmt.Printf("[SCRIPT] wait_listen 超时: service=%s cmd=%d act=%d timeout=%ds\n",
		service, cmd, act, timeout)
	L.Push(lua.LNil)
	return 1
}

// buildPacket 构建完整的网络报文（含加密和 BCC 校验）
// 从指定服务的连接获取加密密钥
func buildPacket(ctx *ScriptContext, service string, cmd, act uint8, msg proto.Message) ([]byte, error) {
	var body []byte
	var err error

	if msg != nil && ctx.Factory != nil {
		body, err = ctx.Factory.Serialize(msg)
		if err != nil {
			return nil, err
		}
	}

	var secretKey []byte
	if ctx.NetSender != nil && service != "" {
		secretKey = ctx.NetSender.GetSecretKey(service)
	}
	return ctx.Protocol.BuildPacket(cmd, act, body, secretKey), nil
}

// buildRawPacket 构建原始报文（使用指定密钥）
func buildRawPacket(ctx *ScriptContext, cmd, act uint8, body []byte, secretKey []byte) []byte {
	return ctx.Protocol.BuildPacket(cmd, act, body, secretKey)
}

// networkCloseTCP network.close_tcp(service) — 关闭指定服务的 TCP 连接
func networkCloseTCP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	ctx.NetSender.CloseTCP(service)
	return 0
}

// networkCloseUDP network.close_udp() — 关闭 UDP 连接
func networkCloseUDP(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	ctx.NetSender.CloseUDP()
	return 0
}

// networkSetSecretKey network.set_secret_key(service, key_bytes) — 设置 TCP 连接的加密密钥
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

// networkSetUDPSecretKey network.set_udp_secret_key(key_bytes) — 设置 UDP 连接的加密密钥
func networkSetUDPSecretKey(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	key := []byte(L.CheckString(1))
	ctx.NetSender.SetUDPSecretKey(key)
	return 0
}

// networkEnsureListener network.ensure_listener(service, cmd, act) — 为 cmd/act 注册监听器占位
func networkEnsureListener(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil {
		return 0
	}
	service := L.CheckString(1)
	cmd := uint8(L.CheckInt(2))
	act := uint8(L.CheckInt(3))
	ctx.NetSender.EnsureListener(service, cmd, act)
	return 0
}

// heartbeatLuaMu 保护 LState 并发调用
// 注：gopher-lua 的 LState 不是并发安全的，必须串行调用。
// 每次心跳触发时加锁，通过临时 LState 或主 LState 调用 builder。
// 这里选择：将 builder 函数存储在主 LState 的 registry 中，每次加锁取出并调用。

// networkRegisterHeartbeat network.register_heartbeat(target, service, cmd, act, interval_ms, builder_func)
// target: "tcp"/"udp"；builder_func 返回 body 字符串（bytes）。
// 每次心跳触发时调用 builder_func 取最新 body。
func networkRegisterHeartbeat(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Protocol == nil {
		L.RaiseError("network not available")
		return 0
	}

	target := L.CheckString(1)
	service := L.CheckString(2)
	cmd := uint8(L.CheckInt(3))
	act := uint8(L.CheckInt(4))
	intervalMs := L.CheckInt(5)

	var builderFn *lua.LFunction
	if L.GetTop() >= 6 {
		v := L.Get(6)
		if fn, ok := v.(*lua.LFunction); ok {
			builderFn = fn
		}
	}

	// 将 builder 函数存入 registry，防止被 GC
	hbRegKey := fmt.Sprintf("__hb_%s_%s_%d_%d__", target, service, cmd, act)
	if builderFn != nil {
		L.SetField(L.Get(lua.RegistryIndex), hbRegKey, builderFn)
	}

	// 心跳在独立 goroutine 中触发，但 LState 非并发安全，
	// 必须与同一 Robot 的主流程、监听回调共享同一把锁（ScriptContext.LuaMu）。
	// 此处在注册阶段快照该锁，心跳触发时按需加锁。
	luaMu := ctx.LuaMu
	builder := func() []byte {
		if luaMu != nil {
			luaMu.Lock()
			defer luaMu.Unlock()
		}

		if builderFn != nil {
			savedTop := L.GetTop()
			defer L.SetTop(savedTop)

			if err := L.CallByParam(lua.P{Fn: builderFn, NRet: 1, Protect: true}); err != nil {
				return nil
			}
			ret := L.Get(-1)
			body := []byte(lua.LVAsString(ret))
			// UDP：采用 offset 加密；TCP：整包加密
			if target == "udp" {
				encOffset := ctx.Protocol.UDPEncryptOffset()
				udpKey := ctx.NetSender.GetUDPSecretKey()
				if len(body) <= encOffset || len(udpKey) == 0 {
					return ctx.Protocol.BuildPacket(cmd, act, body, nil)
				}
				return ctx.Protocol.BuildPacketWithOffset(cmd, act, body, udpKey, encOffset)
			}
			secretKey := ctx.NetSender.GetSecretKey(service)
			return ctx.Protocol.BuildPacket(cmd, act, body, secretKey)
		}

		// 无 builder_func，发空包
		if target == "udp" {
			return ctx.Protocol.BuildPacket(cmd, act, nil, nil)
		}
		secretKey := ctx.NetSender.GetSecretKey(service)
		return ctx.Protocol.BuildPacket(cmd, act, nil, secretKey)
	}

	ctx.NetSender.RegisterHeartbeat(target, service, intervalMs, builder)
	return 0
}

// networkRequestWait network.request_wait(service, sendCmd, sendAct, msg, respCmd, respAct [, s2c_proto])
// 发送请求并等待指定 cmd/act 的响应（响应可以与请求的 cmd/act 不同）。
// 用于 MainLoadOk(CMD=2,ACT=16) 等待 LoginPlayerDataS2C(CMD=1,ACT=2) 等跨 CMD 场景。
// 返回: code, resp (与 network.request 相同)
func networkRequestWait(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.NetSender == nil || ctx.Protocol == nil {
		L.RaiseError("network not available")
		return 0
	}

	service := L.CheckString(1)
	sendCmd := uint8(L.CheckInt(2))
	sendAct := uint8(L.CheckInt(3))
	respCmd := uint8(L.CheckInt(5))
	respAct := uint8(L.CheckInt(6))

	// 提取 proto msg（第 4 个参数，可能是 userdata）
	var msg proto.Message
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if ud, ok := v.(*lua.LUserData); ok {
			if pm, ok := ud.Value.(proto.Message); ok {
				msg = pm
				break
			}
		}
	}

	var s2cProto string
	if L.GetTop() >= 7 {
		s2cProto = L.CheckString(7)
	}

	// 构建发送报文
	packet, err := buildPacket(ctx, service, sendCmd, sendAct, msg)
	if err != nil {
		L.RaiseError("build packet failed: %v", err)
		return 0
	}

	// 发送并等待指定响应
	respBody, ok := ctx.NetSender.TCPRequestFor(service, sendCmd, sendAct, packet, respCmd, respAct)
	if !ok {
		L.Push(lua.LNumber(-1))
		L.Push(lua.LNil)
		return 2
	}

	// 解析响应
	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if err != nil {
			fmt.Printf("[SCRIPT] request_wait 解析失败: proto=%s bodyLen=%d err=%v\n",
				s2cProto, len(respBody), err)
			L.Push(lua.LNumber(-2))
			L.Push(lua.LString(err.Error()))
			return 2
		}

		ud := L.NewUserData()
		ud.Value = respMsg
		mt := L.NewTable()
		L.SetField(mt, "__index", L.NewFunction(protoMsgIndex))
		L.SetMetatable(ud, mt)

		L.Push(lua.LNumber(0))
		L.Push(ud)
		return 2
	}

	L.Push(lua.LNumber(0))
	L.Push(lua.LString(string(respBody)))
	return 2
}
