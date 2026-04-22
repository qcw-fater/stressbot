package adapter

import (
	"fmt"
	"runtime"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// LuaAdapter 通过 gopher-lua LState 池调用适配器脚本实现 Adapter 接口。
//
// 热路径优化策略：
//   - headerSize / bodyLenInfo 在初始化时从 Lua 一次性获取并缓存到 Go 字段。
//   - BodyLength() 基于缓存的元信息在 Go 层原生实现，零 Lua 调用。
//   - HeaderSize() 直接返回缓存值。
//   - Encode() / Decode() / EncodeUDP() / ExpectedResponseKey() 从有界 channel 池获取 LState 执行 Lua，完成后归还。
type LuaAdapter struct {
	states      chan *lua.LState   // 有界 channel 池（容量 = poolSize）
	scriptProto *lua.FunctionProto // 预编译的适配器脚本

	// 初始化时缓存的元信息（热路径零 Lua 调用）
	headerSize  int            // 消息头大小，HeaderSize() 直接返回
	bodyLenInfo BodyLengthInfo // BodyLength() 纯 Go 计算，无需调 Lua
}

// NewLuaAdapter 创建并初始化 Lua 适配器池。
// scriptPath: codec.lua 路径；poolSize: LState 池大小（建议 = CPU 核心数）。
func NewLuaAdapter(poolSize int, scriptPath string) (*LuaAdapter, error) {
	if poolSize <= 0 {
		poolSize = runtime.NumCPU()
	}

	// Step 1: 编译脚本
	tmpL := lua.NewState()
	defer tmpL.Close()
	fn, err := tmpL.LoadFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("编译适配器脚本 %s 失败: %w", scriptPath, err)
	}
	fnProto := fn.Proto

	adp := &LuaAdapter{
		states:      make(chan *lua.LState, poolSize),
		scriptProto: fnProto,
	}

	// Step 2: 预创建 LState 池，每个 LState 执行一次脚本注册函数
	for i := 0; i < poolSize; i++ {
		L := adp.newLState()
		if err := adp.initLState(L); err != nil {
			return nil, fmt.Errorf("初始化 LState[%d] 失败: %w", i, err)
		}
		adp.states <- L
	}

	// Step 3: 初始化时获取元信息并缓存到 Go 字段
	L := adp.acquire()
	defer adp.release(L)

	if err := adp.cacheMetaInfo(L); err != nil {
		return nil, fmt.Errorf("获取适配器元信息失败: %w", err)
	}

	return adp, nil
}

// newLState 创建已注册 bit + zlib 模块的 LState
func (a *LuaAdapter) newLState() *lua.LState {
	L := lua.NewState()
	L.PreloadModule("bit", LoadBitModule)
	RegisterZlibModule(L)
	return L
}

// initLState 在 LState 中执行适配器脚本，将函数缓存到 registry。
func (a *LuaAdapter) initLState(L *lua.LState) error {
	fn := L.NewFunctionFromProto(a.scriptProto)
	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		return fmt.Errorf("执行适配器脚本失败: %w", err)
	}

	fnNames := []string{
		"header_size", "body_length_info", "encode", "encode_udp",
		"decode", "expected_response_key",
	}
	reg := L.Get(lua.RegistryIndex)
	for _, name := range fnNames {
		fn := L.GetGlobal(name)
		if fn == lua.LNil {
			return fmt.Errorf("适配器脚本缺少必需函数: %s()", name)
		}
		L.SetField(reg, "__adapter_"+name, fn)
		L.SetGlobal(name, lua.LNil)
	}
	return nil
}

// cacheMetaInfo 调用 Lua 的元信息函数，缓存到 Go 结构体字段。
func (a *LuaAdapter) cacheMetaInfo(L *lua.LState) error {
	reg := L.Get(lua.RegistryIndex)

	// header_size()
	fn := L.GetField(reg, "__adapter_header_size")
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
		return fmt.Errorf("调用 header_size() 失败: %w", err)
	}
	a.headerSize = int(lua.LVAsNumber(L.Get(-1)))
	L.Pop(1)

	// body_length_info() → table { offset, field_type, includes_header }
	fn = L.GetField(reg, "__adapter_body_length_info")
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
		return fmt.Errorf("调用 body_length_info() 失败: %w", err)
	}
	tbl, ok := L.Get(-1).(*lua.LTable)
	L.Pop(1)
	if !ok {
		return fmt.Errorf("body_length_info() 必须返回 table")
	}
	a.bodyLenInfo = BodyLengthInfo{
		Offset:         int(lua.LVAsNumber(tbl.RawGetString("offset"))),
		FieldType:      lua.LVAsString(tbl.RawGetString("field_type")),
		IncludesHeader: lua.LVAsBool(tbl.RawGetString("includes_header")),
	}

	return nil
}

// ─── Adapter 接口实现 ────────────────────────────────────────────────────────

// HeaderSize 返回消息头大小（零 Lua 调用）
func (a *LuaAdapter) HeaderSize() int { return a.headerSize }

// BodyLength 纯 Go 实现，使用缓存的元信息（零 Lua 调用）。
func (a *LuaAdapter) BodyLength(headerData []byte) int {
	return ReadBodyLength(headerData, a.bodyLenInfo, a.headerSize)
}

// Encode 调用 Lua encode(route, body, secret_key) 函数，用于 TCP 包编码。
func (a *LuaAdapter) Encode(route any, body []byte, secretKey []byte) []byte {
	L := a.acquire()
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__adapter_encode")

	routeVal := RouteToLuaValue(L, route)

	var bodyVal lua.LValue = lua.LNil
	if len(body) > 0 {
		bodyVal = lua.LString(string(body))
	}

	var keyVal lua.LValue = lua.LNil
	if len(secretKey) > 0 {
		keyVal = lua.LString(string(secretKey))
	}

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal, bodyVal, keyVal); err != nil {
		stresslog.Error("[ADAPTER] encode() 调用失败", zap.Error(err))
		return nil
	}

	result := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)
	return result
}

// Decode 调用 Lua decode(data, secret_key) 函数，返回 responseKey + body + headerErr。
func (a *LuaAdapter) Decode(data []byte, secretKey []byte) (string, []byte, uint16) {
	L := a.acquire()
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__adapter_decode")

	dataVal := lua.LString(string(data))

	var keyVal lua.LValue = lua.LNil
	if len(secretKey) > 0 {
		keyVal = lua.LString(string(secretKey))
	}

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 3, Protect: true}, dataVal, keyVal); err != nil {
		stresslog.Error("[ADAPTER] decode() 调用失败", zap.Error(err))
		return "", nil, 0
	}

	headerErr := uint16(lua.LVAsNumber(L.Get(-1)))
	body := []byte(lua.LVAsString(L.Get(-2)))
	responseKey := lua.LVAsString(L.Get(-3))
	L.Pop(3)
	return responseKey, body, headerErr
}

// EncodeUDP 调用 Lua encode_udp(route, body, secret_key) 函数，用于 UDP 包编码。
func (a *LuaAdapter) EncodeUDP(route any, body []byte, secretKey []byte) []byte {
	L := a.acquire()
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__adapter_encode_udp")

	routeVal := RouteToLuaValue(L, route)
	var bodyVal lua.LValue = lua.LNil
	if len(body) > 0 {
		bodyVal = lua.LString(string(body))
	}
	var keyVal lua.LValue = lua.LNil
	if len(secretKey) > 0 {
		keyVal = lua.LString(string(secretKey))
	}

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal, bodyVal, keyVal); err != nil {
		stresslog.Error("[ADAPTER] encode_udp() 调用失败", zap.Error(err))
		return nil
	}
	result := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)
	return result
}

// ExpectedResponseKey 调用 Lua expected_response_key(route) 函数。
func (a *LuaAdapter) ExpectedResponseKey(route any) string {
	L := a.acquire()
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__adapter_expected_response_key")

	routeVal := RouteToLuaValue(L, route)

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal); err != nil {
		stresslog.Error("[ADAPTER] expected_response_key() 调用失败", zap.Error(err))
		return ""
	}

	key := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	return key
}

// acquire 从池中获取 LState（阻塞等待）
func (a *LuaAdapter) acquire() *lua.LState { return <-a.states }

// release 将 LState 归还到池中
func (a *LuaAdapter) release(L *lua.LState) { a.states <- L }
