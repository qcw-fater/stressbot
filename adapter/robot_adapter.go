package adapter

import (
	"sync"

	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
)

// RobotAdapter 是 codec 适配器的 Robot 私有版本。
//
// 设计目标：消除全局 LuaAdapter 的 LState 池跨 Robot 抢夺问题。每个 Robot 在自己
// 的 LState（来自 script.RuntimePool）上注册一份 codec.lua 函数副本，所有
// encode/decode/expected_route_key 调用都直接在该 LState 上执行，不再有跨 Robot
// 排队等候 LState 的尾延迟。
//
// 互斥策略（接受单 Robot 内串行，换跨 Robot 不阻塞）：
//
//	业务 Lua 脚本执行（持 luaMu）             ─┐
//	│  → 内调 network.tcp_request → encode   │
//	│  → 内调 adapter.encode_tcp 等           │  → 走 *Locked 版本
//	listen 回调脚本（持 luaMu）                │     （不再 Lock）
//	│  → 同上                                 │
//	心跳 builder（TryLock 持 luaMu 后调用）    ─┘
//
//	声明式 ActionExecutor（不持 luaMu，主 goroutine）  ─┐
//	│  → 编解码调 Adapter.EncodeTCP 等              │  → 走自动加锁版本
//	Connection.decodeLoop（不持 luaMu）                │     （内部 Lock）
//	│  → 调 Adapter.DecodeTCP/UDP                   │
//	────────────────────────────────────────────────┘
//
// HeaderSize / BodyLength / DescribeError 不需要每 Robot 一份：
//   - HeaderSize / BodyLength 是元信息（纯 Go 字段），直接代理到 parent；
//   - DescribeError 命中率极高（错误码集中），全局 sync.Map 缓存比 per-Robot 更省内存。
type RobotAdapter struct {
	parent *LuaAdapter // 共享元信息、DescribeError 缓存、codec.lua 字节码
	L      *lua.LState // 该 Robot 私有 LState（由 script.RuntimePool 管理生命周期）
	luaMu  *sync.Mutex // 该 Robot 的 luaMu，保护 L 的并发访问
}

// 编译时接口断言：RobotAdapter 实现 Adapter 接口（自动加锁版本满足接口）
var _ Adapter = (*RobotAdapter)(nil)

// ─── 元信息代理（零 Lua 调用）─────────────────────────────────────────────

// HeaderSize 返回消息头大小，代理 parent（纯 Go 缓存字段）。
func (r *RobotAdapter) HeaderSize() int { return r.parent.HeaderSize() }

// BodyLength 解析消息体长度，代理 parent（纯 Go 实现）。
func (r *RobotAdapter) BodyLength(header []byte) int { return r.parent.BodyLength(header) }

// DescribeError 描述服务端错误码，代理 parent（sync.Map 缓存）。
func (r *RobotAdapter) DescribeError(code uint64) string { return r.parent.DescribeError(code) }

// Close 释放资源。RobotAdapter 本身不拥有 LState（由 script.RuntimePool 管理），无事可做。
func (r *RobotAdapter) Close() {}

// ─── 自动加锁版本（实现 Adapter 接口；供未持锁的调用方使用）───────────────

// EncodeTCP 编码 TCP 数据包。自动加锁版本，内部 Lock luaMu。
// 调用方：声明式 ActionExecutor（不持 luaMu，主 goroutine）。
func (r *RobotAdapter) EncodeTCP(route any, body, secretKey []byte) []byte {
	r.luaMu.Lock()
	defer r.luaMu.Unlock()
	return r.EncodeTCPLocked(route, body, secretKey)
}

// EncodeUDP 编码 UDP 数据包。自动加锁版本，内部 Lock luaMu。
// 调用方：声明式 ActionExecutor（不持 luaMu，主 goroutine）。
func (r *RobotAdapter) EncodeUDP(route any, body, secretKey []byte) []byte {
	r.luaMu.Lock()
	defer r.luaMu.Unlock()
	return r.EncodeUDPLocked(route, body, secretKey)
}

// DecodeTCP 解码 TCP 数据包。自动加锁版本，内部 Lock luaMu。
// 调用方：Connection.decodeLoop（per-connection goroutine，不持 luaMu）。
func (r *RobotAdapter) DecodeTCP(data, secretKey []byte) (string, []byte, uint64) {
	r.luaMu.Lock()
	defer r.luaMu.Unlock()
	return r.DecodeTCPLocked(data, secretKey)
}

// DecodeUDP 解码 UDP 数据包。自动加锁版本，内部 Lock luaMu。
// 调用方：Connection.decodeLoop（per-connection goroutine，不持 luaMu）。
func (r *RobotAdapter) DecodeUDP(data, secretKey []byte) (string, []byte, uint64) {
	r.luaMu.Lock()
	defer r.luaMu.Unlock()
	return r.DecodeUDPLocked(data, secretKey)
}

// ExpectedRouteKey 计算期望的响应路由键。自动加锁版本。
// 调用方：声明式 ActionExecutor / robotActionHandler.RegisterListen。
func (r *RobotAdapter) ExpectedRouteKey(route any) string {
	r.luaMu.Lock()
	defer r.luaMu.Unlock()
	return r.ExpectedRouteKeyLocked(route)
}

// ─── *Locked 版本（假设调用方已持 luaMu，供业务 Lua API / 心跳 builder 使用）─

// EncodeTCPLocked 编码 TCP 数据包。**要求调用方已持 luaMu**。
// 调用方：业务 Lua 脚本内调 network.tcp_request → buildPacket → EncodeTCPLocked。
func (r *RobotAdapter) EncodeTCPLocked(route any, body, secretKey []byte) []byte {
	return r.callEncode("__robot_adapter_encode_tcp", route, body, secretKey)
}

// EncodeUDPLocked 编码 UDP 数据包。**要求调用方已持 luaMu**。
func (r *RobotAdapter) EncodeUDPLocked(route any, body, secretKey []byte) []byte {
	return r.callEncode("__robot_adapter_encode_udp", route, body, secretKey)
}

// DecodeTCPLocked 解码 TCP 数据包。**要求调用方已持 luaMu**。
func (r *RobotAdapter) DecodeTCPLocked(data, secretKey []byte) (string, []byte, uint64) {
	return r.callDecode("__robot_adapter_decode_tcp", data, secretKey)
}

// DecodeUDPLocked 解码 UDP 数据包。**要求调用方已持 luaMu**。
func (r *RobotAdapter) DecodeUDPLocked(data, secretKey []byte) (string, []byte, uint64) {
	return r.callDecode("__robot_adapter_decode_udp", data, secretKey)
}

// ExpectedRouteKeyLocked 计算响应路由键。**要求调用方已持 luaMu**。
func (r *RobotAdapter) ExpectedRouteKeyLocked(route any) string {
	L := r.L
	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__robot_adapter_expected_route_key")
	routeVal := RouteToLuaValue(L, route)
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal); err != nil {
		stresslog.Error("[ADAPTER] expected_route_key 调用失败 (robot-local)", zap.Error(err))
		return ""
	}
	key := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	return key
}

// ─── 内部实现（已持锁路径上的 Lua 调用）─────────────────────────────────

// callEncode 调用已注册到 registry 的 encode 函数。调用方需已持 luaMu。
func (r *RobotAdapter) callEncode(fnRegKey string, route any, body, secret []byte) []byte {
	L := r.L
	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, fnRegKey)
	routeVal := RouteToLuaValue(L, route)
	bodyVal := optionalLString(body)
	keyVal := optionalLString(secret)
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal, bodyVal, keyVal); err != nil {
		stresslog.Error("[ADAPTER] encode 调用失败 (robot-local)",
			zap.String("fn", fnRegKey), zap.Any("route", route), zap.Error(err))
		return nil
	}
	out := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)
	return out
}

// callDecode 调用已注册到 registry 的 decode 函数。调用方需已持 luaMu。
func (r *RobotAdapter) callDecode(fnRegKey string, data, secret []byte) (string, []byte, uint64) {
	L := r.L
	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, fnRegKey)
	dataVal := lua.LString(string(data))
	keyVal := optionalLString(secret)
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 3, Protect: true}, dataVal, keyVal); err != nil {
		stresslog.Error("[ADAPTER] decode 调用失败 (robot-local)",
			zap.String("fn", fnRegKey), zap.Int("dataLen", len(data)), zap.Error(err))
		return "", nil, 0
	}
	headerErr := uint64(lua.LVAsNumber(L.Get(-1)))
	body := []byte(lua.LVAsString(L.Get(-2)))
	routeKey := lua.LVAsString(L.Get(-3))
	L.Pop(3)
	return routeKey, body, headerErr
}

// optionalLString 把空字节切片转成 LNil，避免 Lua 端收到空字符串误判为有效 body / key。
func optionalLString(b []byte) lua.LValue {
	if len(b) == 0 {
		return lua.LNil
	}
	return lua.LString(string(b))
}
