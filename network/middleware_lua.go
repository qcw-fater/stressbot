package network

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

const luaCtxTypeName = "packetContext"

// LuaMiddlewarePool 管理 Lua 中间件专用的 LState 池。
// 与 per-robot 的 RuntimePool 独立，中间件是协议级别、跨机器人共享的。
type LuaMiddlewarePool struct {
	states      chan *lua.LState              // 有界 channel 池
	precompiled map[string]*lua.FunctionProto // 脚本名 → 编译结果
}

// NewLuaMiddlewarePool 创建指定大小的 Lua 中间件池。
func NewLuaMiddlewarePool(size int) *LuaMiddlewarePool {
	if size <= 0 {
		size = runtime.NumCPU()
	}
	return &LuaMiddlewarePool{
		states:      make(chan *lua.LState, size),
		precompiled: make(map[string]*lua.FunctionProto),
	}
}

// LoadScripts 从指定目录加载所有 .lua 脚本并预编译。
// 脚本名 = 文件名（不含 .lua 扩展名），可在 header.json 的 middleware 数组中引用。
func (p *LuaMiddlewarePool) LoadScripts(dirs []string) error {
	tmpL := lua.NewState()
	defer tmpL.Close()

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("读取中间件脚本目录 %s 失败: %w", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
				continue
			}

			path := filepath.Join(dir, entry.Name())
			fn, err := tmpL.LoadFile(path)
			if err != nil {
				return fmt.Errorf("编译中间件脚本 %s 失败: %w", path, err)
			}

			name := strings.TrimSuffix(entry.Name(), ".lua")
			p.precompiled[name] = fn.Proto
		}
	}

	// 预创建 LState 并放入池
	for i := 0; i < cap(p.states); i++ {
		L := p.newLState()
		p.states <- L
	}

	if len(p.precompiled) > 0 {
		stresslog.Info("[MIDDLEWARE-LUA] 已加载中间件脚本", zap.Int("count", len(p.precompiled)))
	}

	return nil
}

// newLState 创建一个注册了中间件 API 的 LState。
func (p *LuaMiddlewarePool) newLState() *lua.LState {
	L := lua.NewState()
	registerLuaMiddlewareAPIs(L)
	return L
}

// registerLuaMiddlewareAPIs 注册中间件专用的 Lua API。
func registerLuaMiddlewareAPIs(L *lua.LState) {
	// bit 模块：基础位运算原语（Lua 5.1 不支持位运算符）
	L.PreloadModule("bit", loadBitModule)

	// 注册 packetContext 类型
	mt := L.NewTypeMetatable(luaCtxTypeName)
	L.SetField(mt, "__index", L.NewFunction(luaCtxIndex))
	L.SetField(mt, "__newindex", L.NewFunction(luaCtxNewIndex))
}

// Acquire 从池中获取一个 LState（若池耗尽则阻塞）。
func (p *LuaMiddlewarePool) Acquire() *lua.LState {
	return <-p.states
}

// Release 将 LState 归还到池中。
func (p *LuaMiddlewarePool) Release(L *lua.LState) {
	p.states <- L
}

// HasScript 检查脚本是否已编译。
func (p *LuaMiddlewarePool) HasScript(name string) bool {
	_, ok := p.precompiled[name]
	return ok
}

// ScriptNames 返回所有已编译的脚本名。
func (p *LuaMiddlewarePool) ScriptNames() []string {
	names := make([]string, 0, len(p.precompiled))
	for name := range p.precompiled {
		names = append(names, name)
	}
	return names
}

// CreateLuaMiddlewareFactory 创建 Lua 中间件工厂。
// 返回的闭包符合 RegisterMiddleware 的签名。
// 调用时从池获取 LState，执行 Lua middleware 函数，归还 LState。
func CreateLuaMiddlewareFactory(pool *LuaMiddlewarePool, scriptName string) func(ProtocolConfig) PacketMiddleware {
	return func(cfg ProtocolConfig) PacketMiddleware {
		return func(ctx *PacketContext, next func()) {
			L := pool.Acquire()
			defer pool.Release(L)

			runLuaMiddleware(L, pool, scriptName, ctx, next)
		}
	}
}

// runLuaMiddleware 执行 Lua 中间件函数。
// 加载脚本 → 获取 middleware(ctx, next) 函数 → 创建 ctx userdata 和 next 函数 → 调用。
func runLuaMiddleware(L *lua.LState, pool *LuaMiddlewarePool, scriptName string, ctx *PacketContext, next func()) {
	cacheKey := "__mw_" + scriptName
	reg := L.Get(lua.RegistryIndex)
	mwFn := L.GetField(reg, cacheKey)

	if mwFn == lua.LNil {
		proto, ok := pool.precompiled[scriptName]
		if !ok {
			stresslog.Error("[MIDDLEWARE-LUA] 脚本未找到", zap.String("script", scriptName))
			next()
			return
		}

		savedTop := L.GetTop()
		fn := L.NewFunctionFromProto(proto)
		L.Push(fn)
		if err := L.PCall(0, 0, nil); err != nil {
			stresslog.Error("[MIDDLEWARE-LUA] 加载脚本失败",
				zap.String("script", scriptName), zap.Error(err))
			L.SetTop(savedTop)
			next()
			return
		}

		mwFn = L.GetGlobal("middleware")
		if mwFn == lua.LNil {
			stresslog.Error("[MIDDLEWARE-LUA] 脚本未定义 middleware 函数",
				zap.String("script", scriptName))
			L.SetTop(savedTop)
			next()
			return
		}
		L.SetGlobal("middleware", lua.LNil)

		L.SetField(L.Get(lua.RegistryIndex), cacheKey, mwFn)
		L.SetTop(savedTop)
	}
	// 创建 ctx userdata
	ctxUD := L.NewUserData()
	ctxUD.Value = ctx
	L.SetMetatable(ctxUD, L.GetTypeMetatable(luaCtxTypeName))

	// 创建 next 函数
	nextFn := L.NewFunction(func(innerL *lua.LState) int {
		next()
		return 0
	})

	// 调用 middleware(ctx, next)
	if err := L.CallByParam(lua.P{Fn: mwFn, NRet: 0, Protect: true}, ctxUD, nextFn); err != nil {
		stresslog.Error("[MIDDLEWARE-LUA] 执行失败",
			zap.String("script", scriptName), zap.Error(err))
	}
}

// --- Lua ctx userdata metatable ---

// luaCtxIndex 处理 ctx.property 和 ctx:method() 的读取。
func luaCtxIndex(L *lua.LState) int {
	ud := L.CheckUserData(1)
	ctx, ok := ud.Value.(*PacketContext)
	if !ok {
		L.Push(lua.LNil)
		return 1
	}

	field := L.CheckString(2)

	switch field {
	case "direction":
		if ctx.Direction == PacketSend {
			L.Push(lua.LString("send"))
		} else {
			L.Push(lua.LString("recv"))
		}
	case "cmd":
		L.Push(lua.LNumber(ctx.Cmd))
	case "act":
		L.Push(lua.LNumber(ctx.Act))
	case "body":
		L.Push(lua.LString(string(ctx.Body)))
	case "flags":
		L.Push(lua.LNumber(ctx.Flags))
	case "secret_key":
		if ctx.SecretKey != nil {
			L.Push(lua.LString(string(ctx.SecretKey)))
		} else {
			L.Push(lua.LNil)
		}
	case "encrypt_offset":
		L.Push(lua.LNumber(ctx.EncryptOffset))

	// 方法
	case "set_body":
		L.Push(L.NewFunction(func(innerL *lua.LState) int {
			data := innerL.CheckString(2)
			ctx.Body = []byte(data)
			return 0
		}))
	case "set_flag":
		L.Push(L.NewFunction(func(innerL *lua.LState) int {
			bit := innerL.CheckInt(2)
			ctx.SetFlag(bit)
			return 0
		}))
	case "has_flag":
		L.Push(L.NewFunction(func(innerL *lua.LState) int {
			bit := innerL.CheckInt(2)
			innerL.Push(lua.LBool(ctx.HasFlag(bit)))
			return 1
		}))
	case "set_header_field":
		L.Push(L.NewFunction(func(innerL *lua.LState) int {
			name := innerL.CheckString(2)
			value := innerL.CheckInt(3)
			ctx.SetHeaderField(name, uint64(value))
			return 0
		}))
	case "get_header_field":
		L.Push(L.NewFunction(func(innerL *lua.LState) int {
			name := innerL.CheckString(2)
			innerL.Push(lua.LNumber(ctx.GetHeaderField(name)))
			return 1
		}))
	case "set_error":
		L.Push(L.NewFunction(func(innerL *lua.LState) int {
			msg := innerL.CheckString(2)
			ctx.SetError(errors.New(msg))
			return 0
		}))
	default:
		L.Push(lua.LNil)
	}
	return 1
}

// luaCtxNewIndex 处理 ctx.property = value 的写入。
func luaCtxNewIndex(L *lua.LState) int {
	ud := L.CheckUserData(1)
	ctx, ok := ud.Value.(*PacketContext)
	if !ok {
		return 0
	}

	field := L.CheckString(2)

	switch field {
	case "body":
		data := L.CheckString(3)
		ctx.Body = []byte(data)
	case "flags":
		ctx.Flags = uint8(L.CheckInt(3))
	}
	return 0
}

// --- bit 模块：Lua 5.1 基础位运算原语 ---

func loadBitModule(L *lua.LState) int {
	mod := L.NewTable()
	L.SetField(mod, "bxor", L.NewFunction(bitBxor))
	L.SetField(mod, "band", L.NewFunction(bitBand))
	L.SetField(mod, "bor", L.NewFunction(bitBor))
	L.SetField(mod, "bnot", L.NewFunction(bitBnot))
	L.SetField(mod, "lshift", L.NewFunction(bitLshift))
	L.SetField(mod, "rshift", L.NewFunction(bitRshift))
	L.SetField(mod, "rol", L.NewFunction(bitRol))
	L.Push(mod)
	return 1
}

func bitBxor(L *lua.LState) int {
	a := L.CheckInt(1)
	b := L.CheckInt(2)
	L.Push(lua.LNumber(a ^ b))
	return 1
}

func bitBand(L *lua.LState) int {
	a := L.CheckInt(1)
	b := L.CheckInt(2)
	L.Push(lua.LNumber(a & b))
	return 1
}

func bitBor(L *lua.LState) int {
	a := L.CheckInt(1)
	b := L.CheckInt(2)
	L.Push(lua.LNumber(a | b))
	return 1
}

func bitBnot(L *lua.LState) int {
	a := L.CheckInt(1)
	L.Push(lua.LNumber(^a))
	return 1
}

func bitLshift(L *lua.LState) int {
	a := L.CheckInt(1)
	n := L.CheckInt(2)
	L.Push(lua.LNumber(a << n))
	return 1
}

func bitRshift(L *lua.LState) int {
	a := L.CheckInt(1)
	n := L.CheckInt(2)
	L.Push(lua.LNumber(int(uint(a) >> n)))
	return 1
}

func bitRol(L *lua.LState) int {
	a := L.CheckInt(1)
	n := L.CheckInt(2)
	a = a & 0xFF
	if n < 0 {
		n = 8 + n
	}
	n = n % 8
	result := ((a << n) | (a >> (8 - n))) & 0xFF
	L.Push(lua.LNumber(result))
	return 1
}
