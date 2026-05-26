package adapter

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
)

// lstateAcquireTimeout 从 Lua 池获取 LState 的超时时间。
const lstateAcquireTimeout = 30 * time.Second

// defaultPoolMultiplier 默认池大小相对 NumCPU 的放大系数。
//
// 历史 = 1（仅 NumCPU 个 LState），在 10000+ 连接场景下会发生：
//   - gnet 事件循环（decode）与 1000+ robot goroutine（encode）抢同一池
//   - acquire 阻塞 → 事件循环停滞 → 心跳/响应被服务端判定掉线 → CONN_DROPPED 雪崩
//
// 调整为 4（每核 4 个 LState）：
//   - 单个 LState 内存约 0.5-1MB，8 核机器默认 32 个 ≈ 32MB，可接受
//   - 配合 network 层异步 decode（per-connection goroutine），池竞争从 gnet 事件循环
//     转移到普通 goroutine，短暂排队不再放大成事件循环冻结
const defaultPoolMultiplier = 4

// defaultPoolCap 默认池大小上限（防止超多核机器创建过多 LState 浪费内存）。
const defaultPoolCap = 128

// SuggestedPoolSize 推荐的 LState 池大小：max(1, min(NumCPU * defaultPoolMultiplier, defaultPoolCap))。
// 调用方在不显式指定 poolSize 时应使用此值。
func SuggestedPoolSize() int {
	n := runtime.NumCPU() * defaultPoolMultiplier
	if n < 1 {
		n = 1
	}
	if n > defaultPoolCap {
		n = defaultPoolCap
	}
	return n
}

// LuaAdapter 通过 gopher-lua LState 池调用适配器脚本实现 Adapter 接口。
//
// 热路径优化策略：
//   - headerSize / bodyLenInfo 在初始化时从 Lua 一次性获取并缓存到 Go 字段。
//   - BodyLength() 基于缓存的元信息在 Go 层原生实现，零 Lua 调用。
//   - HeaderSize() 直接返回缓存值。
//   - Encode() / Decode() / EncodeUDP() / ExpectedRouteKey() 从有界 channel 池获取 LState 执行 Lua，完成后归还。
type LuaAdapter struct {
	states      chan *lua.LState   // 有界 channel 池，容量 = poolSize
	scriptProto *lua.FunctionProto // 预编译的适配器脚本字节码

	// 初始化时从 Lua 缓存的元信息（热路径零 Lua 调用）
	headerSize  int            // 消息头固定字节数，HeaderSize() 直接返回此值
	bodyLenInfo BodyLengthInfo // 消息体长度解析元信息，BodyLength() 纯 Go 计算

	// error.lua 错误码映射（可选功能）
	hasErrorMap    bool     // 是否成功加载了 error.lua
	errorDescCache sync.Map // uint64 -> string 永久缓存，避免高频 headerErr 反复调用 Lua
}

// 编译时接口断言
var _ Adapter = (*LuaAdapter)(nil)

// NewLuaAdapter 创建并初始化 Lua 适配器池。
// scriptPath: codec.lua 路径；poolSize: LState 池大小（≤0 使用 SuggestedPoolSize）。
// errorMapPath: error.lua 路径（可选，空字符串表示不加载错误码映射）。
func NewLuaAdapter(poolSize int, scriptPath string, errorMapPath string) (*LuaAdapter, error) {
	if poolSize <= 0 {
		poolSize = SuggestedPoolSize()
	}
	if poolSize > defaultPoolCap {
		poolSize = defaultPoolCap
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
			// 清理已创建的 LState 避免资源泄漏
			adp.closeAll()
			return nil, fmt.Errorf("初始化 LState[%d] 失败: %w", i, err)
		}
		adp.states <- L
	}

	// Step 3: 初始化时获取元信息并缓存到 Go 字段
	L := adp.acquire()
	if L == nil {
		adp.closeAll()
		return nil, fmt.Errorf("获取 LState 超时")
	}
	defer adp.release(L)

	if err := adp.cacheMetaInfo(L); err != nil {
		return nil, fmt.Errorf("获取适配器元信息失败: %w", err)
	}

	// Step 4: 可选 — 加载 error.lua 错误码映射到已创建的 LState 池
	if errorMapPath != "" {
		if data, err := os.ReadFile(errorMapPath); err == nil {
			loaded := true
			for i := 0; i < poolSize; i++ {
				LS := adp.acquire()
				if LS == nil {
					loaded = false
					break
				}
				if err := LS.DoString(string(data)); err != nil {
					stresslog.Warn("[ADAPTER] error.lua 加载失败", zap.Error(err))
					loaded = false
					adp.release(LS)
					break
				}
				// 缓存 describe_error 函数到 registry
				fn := LS.GetGlobal("describe_error")
				if fn == lua.LNil {
					stresslog.Warn("[ADAPTER] error.lua 缺少 describe_error 函数")
					loaded = false
					adp.release(LS)
					break
				}
				reg := LS.Get(lua.RegistryIndex)
				LS.SetField(reg, "__adapter_describe_error", fn)
				LS.SetGlobal("describe_error", lua.LNil)
				adp.release(LS)
			}
			if loaded {
				adp.hasErrorMap = true
			}
		} else {
			stresslog.Warn("[ADAPTER] error.lua 文件读取失败，跳过错误码映射", zap.Error(err))
		}
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
		"header_size", "body_length", "encode_tcp", "encode_udp",
		"decode_tcp", "decode_udp", "expected_route_key",
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

	// body_length() → table { offset, field_type, includes_header }
	fn = L.GetField(reg, "__adapter_body_length")
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
		return fmt.Errorf("调用 body_length() 失败: %w", err)
	}
	tbl, ok := L.Get(-1).(*lua.LTable)
	L.Pop(1)
	if !ok {
		return fmt.Errorf("body_length() 必须返回 table")
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

// EncodeTCP 调用 Lua encode_tcp(route, body, secret_key) 编码 TCP 数据包。
func (a *LuaAdapter) EncodeTCP(route any, body []byte, secretKey []byte) []byte {
	return a.encode("__adapter_encode_tcp", route, body, secretKey)
}

// EncodeUDP 调用 Lua encode_udp(route, body, secret_key) 编码 UDP 数据包。
func (a *LuaAdapter) EncodeUDP(route any, body []byte, secretKey []byte) []byte {
	return a.encode("__adapter_encode_udp", route, body, secretKey)
}

// encode 通用编码实现，调用指定 Lua 函数。
func (a *LuaAdapter) encode(fnName string, route any, body []byte, secretKey []byte) []byte {
	L := a.acquire()
	if L == nil {
		return nil
	}
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, fnName)

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
		stresslog.Error("[ADAPTER] encode 调用失败", zap.String("fn", fnName), zap.Any("route", route), zap.Error(err))
		return nil
	}

	result := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)
	return result
}

// DecodeTCP 调用 Lua decode_tcp(data, secret_key) 解码 TCP 数据包。
func (a *LuaAdapter) DecodeTCP(data []byte, secretKey []byte) (string, []byte, uint64) {
	return a.decode("__adapter_decode_tcp", data, secretKey)
}

// DecodeUDP 调用 Lua decode_udp(data, secret_key) 解码 UDP 数据包。
func (a *LuaAdapter) DecodeUDP(data []byte, secretKey []byte) (string, []byte, uint64) {
	return a.decode("__adapter_decode_udp", data, secretKey)
}

// decode 通用解码实现，调用指定 Lua 函数。
func (a *LuaAdapter) decode(fnName string, data []byte, secretKey []byte) (string, []byte, uint64) {
	L := a.acquire()
	if L == nil {
		return "", nil, 0
	}
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, fnName)

	dataVal := lua.LString(string(data))

	var keyVal lua.LValue = lua.LNil
	if len(secretKey) > 0 {
		keyVal = lua.LString(string(secretKey))
	}

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 3, Protect: true}, dataVal, keyVal); err != nil {
		stresslog.Error("[ADAPTER] decode 调用失败", zap.String("fn", fnName), zap.Int("dataLen", len(data)), zap.Error(err))
		return "", nil, 0
	}

	headerErr := uint64(lua.LVAsNumber(L.Get(-1)))
	body := []byte(lua.LVAsString(L.Get(-2)))
	routeKey := lua.LVAsString(L.Get(-3))
	L.Pop(3)
	return routeKey, body, headerErr
}

// ExpectedRouteKey 调用 Lua expected_route_key(route) 函数。
func (a *LuaAdapter) ExpectedRouteKey(route any) string {
	L := a.acquire()
	if L == nil {
		return ""
	}
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__adapter_expected_route_key")

	routeVal := RouteToLuaValue(L, route)

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal); err != nil {
		stresslog.Error("[ADAPTER] expected_route_key() 调用失败", zap.Any("route", route), zap.Error(err))
		return ""
	}

	key := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	return key
}

// acquire 从池中获取 LState，超时 30 秒后返回错误。
func (a *LuaAdapter) acquire() *lua.LState {
	select {
	case L := <-a.states:
		return L
	case <-time.After(lstateAcquireTimeout):
		stresslog.Error("[ADAPTER] acquire LState 超时（池耗尽）")
		return nil
	}
}

// release 将 LState 归还到池中（非阻塞，池满时关闭 LState 防止泄漏）
func (a *LuaAdapter) release(L *lua.LState) {
	select {
	case a.states <- L:
	default:
		stresslog.Warn("[ADAPTER] release LState 池已满，关闭溢出 LState")
		L.Close()
	}
}

// Close 关闭适配器，释放所有 LState 资源。
func (a *LuaAdapter) Close() {
	a.closeAll()
}

// closeAll 清理池中所有 LState
func (a *LuaAdapter) closeAll() {
	for {
		select {
		case L := <-a.states:
			L.Close()
		default:
			return
		}
	}
}

// DescribeError 将服务端错误码映射为可读描述。
// 第一次查询后结果（含空字符串）会被永久缓存。error.lua 运行时不可变，重启才能更新。
func (a *LuaAdapter) DescribeError(code uint64) string {
	if !a.hasErrorMap {
		return ""
	}
	if v, ok := a.errorDescCache.Load(code); ok {
		return v.(string)
	}
	desc := a.callDescribeError(code)
	a.errorDescCache.Store(code, desc) // 即使是 "" 也缓存，避免重复查询
	return desc
}

// callDescribeError 从 Lua 池获取 LState，调用 describe_error(code) 获取错误描述。
func (a *LuaAdapter) callDescribeError(code uint64) string {
	L := a.acquire()
	if L == nil {
		return ""
	}
	defer a.release(L)

	reg := L.Get(lua.RegistryIndex)
	fn := L.GetField(reg, "__adapter_describe_error")

	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LNumber(code)); err != nil {
		stresslog.Error("[ADAPTER] describe_error() 调用失败", zap.Error(err))
		return ""
	}

	desc := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	return desc
}
