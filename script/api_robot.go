package script

import (
	"fmt"
	"strconv"

	"stressbot/protox"

	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/proto"
)

const maxSafeInt = 1 << 53 // 9007199254740992，Lua double 可精确表示的最大整数

// loadRobotModule 加载 robot 命名空间模块。
// Lua 用法：
//
//	local r = require("robot")
//	r.get("key")           → 读取状态值
//	r.set("key", value)    → 写入状态值
//	r.has("key")           → 检查 key 是否存在
//	r.delete("key")        → 删除单个 key
//	r.clear("key")         → 删除指定 key（无参时清空全部）
//	r.increment("key")     → 原子递增并返回新值
//	r.keys()               → 返回所有 key 列表
//	r.get_path("a.b[0].c") → 按路径读取嵌套值
//	r.set_path("a.b[0].c", v) → 按路径写入嵌套值（自动创建中间 map）
//	r.get_id()             → 返回机器人编号
//	r.get_index()          → 返回任务全局序号（0-based，不含 startNumber 偏移）
//	r.get_account()        → 返回账号名
//	r.get_context()        → 检查 context 是否已取消
//	r.error(code, detail)  → 构造 err table
func loadRobotModule(L *lua.LState) int {
	mod := L.NewTable()

	// 读写
	L.SetField(mod, "get", L.NewFunction(robotGet))
	L.SetField(mod, "set", L.NewFunction(robotSet))
	L.SetField(mod, "has", L.NewFunction(robotHas))
	L.SetField(mod, "delete", L.NewFunction(robotDelete))
	L.SetField(mod, "clear", L.NewFunction(robotClear))
	L.SetField(mod, "increment", L.NewFunction(robotIncrement))
	L.SetField(mod, "keys", L.NewFunction(robotKeys))
	L.SetField(mod, "get_path", L.NewFunction(robotGetPath))
	L.SetField(mod, "set_path", L.NewFunction(robotSetPath))
	// 元信息
	L.SetField(mod, "get_id", L.NewFunction(robotGetID))
	L.SetField(mod, "get_index", L.NewFunction(robotGetIndex))
	L.SetField(mod, "get_account", L.NewFunction(robotGetAccount))
	L.SetField(mod, "get_context", L.NewFunction(robotGetContext))
	L.SetField(mod, "error", L.NewFunction(robotError))

	L.Push(mod)
	return 1
}

// robotIndex robot 对象的 __index 元方法。
// 支持 r:get("key")、r:set("key", val) 等方法调用。
func robotIndex(L *lua.LState) int {
	method := L.CheckString(2)
	switch method {
	case "get":
		L.Push(L.NewFunction(robotGet))
	case "set":
		L.Push(L.NewFunction(robotSet))
	case "has":
		L.Push(L.NewFunction(robotHas))
	case "delete":
		L.Push(L.NewFunction(robotDelete))
	case "clear":
		L.Push(L.NewFunction(robotClear))
	case "increment":
		L.Push(L.NewFunction(robotIncrement))
	case "keys":
		L.Push(L.NewFunction(robotKeys))
	case "get_path":
		L.Push(L.NewFunction(robotGetPath))
	case "set_path":
		L.Push(L.NewFunction(robotSetPath))
	case "get_id":
		L.Push(L.NewFunction(robotGetID))
	case "get_index":
		L.Push(L.NewFunction(robotGetIndex))
	case "get_account":
		L.Push(L.NewFunction(robotGetAccount))
	case "get_context":
		L.Push(L.NewFunction(robotGetContext))
	case "error":
		L.Push(L.NewFunction(robotError))
	default:
		L.RaiseError("unknown robot method: %s", method)
	}
	return 1
}

// ---------------------------------------------------------------------------
// 读写
// ---------------------------------------------------------------------------

// robotGet robot.get(key) — 获取状态值
func robotGet(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNil)
		return 1
	}

	key := L.CheckString(L.GetTop())
	val := ctx.Store.Get(key)
	L.Push(goValueToLua(L, val))
	return 1
}

// robotSet robot.set(key, value) — 设置状态值
func robotSet(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}

	base := firstRobotArg(L)
	if L.GetTop() < base+1 {
		L.RaiseError("robot.set requires (key, value)")
		return 0
	}

	key := lua.LVAsString(L.Get(base))
	value := luaToGoValue(L.Get(base + 1))
	ctx.Store.Set(key, value)
	return 0
}

// robotHas robot.has(key) — 检查 key 是否存在
func robotHas(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LBool(false))
		return 1
	}

	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.RaiseError("robot.has requires (key)")
		return 0
	}

	key := lua.LVAsString(L.Get(base))
	L.Push(lua.LBool(ctx.Store.Has(key)))
	return 1
}

// robotDelete robot.delete(key) — 删除单个 key（必须传 key）
func robotDelete(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.RaiseError("robot.delete requires (key)")
		return 0
	}
	key := lua.LVAsString(L.Get(base))
	ctx.Store.Delete(key)
	return 0
}

// robotClear robot.clear(key?) — 删除指定 key 或清空全部
func robotClear(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	base := firstRobotArg(L)
	if L.GetTop() < base {
		ctx.Store.Clear()
		return 0
	}
	key := lua.LVAsString(L.Get(base))
	ctx.Store.Delete(key)
	return 0
}

// robotIncrement robot.increment(key) — 原子递增
func robotIncrement(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNumber(0))
		return 1
	}

	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.RaiseError("robot.increment requires (key)")
		return 0
	}

	key := lua.LVAsString(L.Get(base))
	v := ctx.Store.Increment(key)
	L.Push(lua.LNumber(v))
	return 1
}

// robotKeys robot.keys() — 返回所有 key 列表
func robotKeys(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(L.CreateTable(0, 0))
		return 1
	}
	keys := ctx.Store.Keys()
	tb := L.CreateTable(len(keys), 0)
	for i, k := range keys {
		tb.RawSetInt(i+1, lua.LString(k))
	}
	L.Push(tb)
	return 1
}

// robotError 构造 err table 供脚本 return。
// Lua: robot.error(code, detail) → {code=number, detail=string}
func robotError(L *lua.LState) int {
	base := firstRobotArg(L)
	code := L.CheckInt(base)
	detail := L.CheckString(base + 1)
	L.Push(newErrTable(L, code, detail))
	return 1
}

// robotGetPath robot.get_path("a.b[0].c") — 按路径读取嵌套值
func robotGetPath(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNil)
		return 1
	}
	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.Push(lua.LNil)
		return 1
	}
	path := lua.LVAsString(L.Get(base))
	val := ctx.Store.GetPath(path)
	L.Push(goValueToLua(L, val))
	return 1
}

// robotSetPath robot.set_path(path, value) — 按路径写入嵌套 map/list（自动创建中间节点）
func robotSetPath(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	base := firstRobotArg(L)
	if L.GetTop() < base+1 {
		L.RaiseError("robot.set_path requires (path, value)")
		return 0
	}
	path := lua.LVAsString(L.Get(base))
	value := luaToGoValue(L.Get(base + 1))
	ctx.Store.SetPath(path, value)
	return 0
}

// ---------------------------------------------------------------------------
// 元信息
// ---------------------------------------------------------------------------

// robotGetID robot.get_id() — 获取机器人编号
func robotGetID(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(ctx.RobotID))
	return 1
}

// robotGetIndex robot.get_index() — 获取任务全局序号（0-based，不含 startNumber 偏移）
func robotGetIndex(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(ctx.Index))
	return 1
}

// robotGetAccount robot.get_account() — 获取账号名
func robotGetAccount(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil {
		L.Push(lua.LString(""))
		return 1
	}
	L.Push(lua.LString(ctx.Account))
	return 1
}

// robotGetContext robot.get_context() — 检查 context 是否已取消
func robotGetContext(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Ctx == nil {
		L.Push(lua.LBool(false))
		return 1
	}
	L.Push(lua.LBool(ctx.Ctx.Err() != nil))
	return 1
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// firstRobotArg 返回首个业务参数的栈索引，避免每次调用都分配参数切片。
// 以方法形式调用（r:set(...)）时栈位 1 是 robot 的 LUserData（self），需跳过返回 2；
// 以模块形式调用（robot.set(...)）时首参即在栈位 1。
func firstRobotArg(L *lua.LState) int {
	if _, ok := L.Get(1).(*lua.LUserData); ok {
		return 2
	}
	return 1
}

// goValueToLua 将 Go 值转换为 Lua 值
func goValueToLua(L *lua.LState, val any) lua.LValue {
	if val == nil {
		return lua.LNil
	}
	switch v := val.(type) {
	case bool:
		return lua.LBool(v)
	case int:
		return lua.LNumber(v)
	case int32:
		return lua.LNumber(v)
	case int64:
		if v > maxSafeInt || v < -maxSafeInt {
			return lua.LString(strconv.FormatInt(v, 10))
		}
		return lua.LNumber(v)
	case uint:
		return lua.LNumber(v)
	case uint32:
		return lua.LNumber(v)
	case uint64:
		if v > maxSafeInt {
			return lua.LString(strconv.FormatUint(v, 10))
		}
		return lua.LNumber(v)
	case float64:
		return lua.LNumber(v)
	case float32:
		return lua.LNumber(v)
	case string:
		return lua.LString(v)
	case []byte:
		return lua.LString(string(v))
	case []any:
		// 精确预分配数组容量，避免 L.NewTable() 默认 32 槽 array + 32 槽 hash 的双重浪费
		tb := L.CreateTable(len(v), 0)
		for i, elem := range v {
			tb.RawSetInt(i+1, goValueToLua(L, elem))
		}
		return tb
	case map[string]any:
		// 仅预分配 hash 容量，省掉默认 32 槽 array 的 512 字节空转
		tb := L.CreateTable(0, len(v))
		for k, elem := range v {
			tb.RawSetString(k, goValueToLua(L, elem))
		}
		return tb
	case *protox.Frozen:
		// 整存 proto 引用（P1a）：现场转真 Lua table。protoMessageToLuaTable 与
		// GetFieldMap 同一套跳过/默认值规则，脚本看到的 table 与旧的展开 map 版逐字一致
		// （type(v)=="table"、proto3 默认值字段在场、可自由遍历/改写副本）。
		// 转换产物是临时 Lua 对象，Go 侧不再为整存消息常驻装箱树。
		return protoMessageToLuaTable(L, v.Message())
	default:
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

// luaToGoValue 将 Lua 值转换为 Go 值
func luaToGoValue(v lua.LValue) any {
	switch v.Type() {
	case lua.LTNil:
		return nil
	case lua.LTBool:
		return bool(v.(lua.LBool))
	case lua.LTNumber:
		return float64(v.(lua.LNumber))
	case lua.LTString:
		return string(v.(lua.LString))
	case lua.LTTable:
		tb := v.(*lua.LTable)
		isArray := true
		maxIdx := 0
		count := 0
		tb.ForEach(func(k, val lua.LValue) {
			count++
			if k.Type() == lua.LTNumber {
				idx := int(lua.LVAsNumber(k))
				if idx > maxIdx {
					maxIdx = idx
				}
			} else {
				isArray = false
			}
		})

		// 仅"稠密"表按 maxIdx 分配：isArray 只保证键全为数字，maxIdx 可能远大于实际元素数。
		// 稀疏表（如 {[1e9]=1}：count=1 而 maxIdx=1e9）若按 maxIdx 分配会申请十亿级切片 → OOM。
		// maxIdx == count 表示下标连续 1..N，分配量恒等于元素数；否则退化为 map（键转字符串）。
		if isArray && maxIdx > 0 && maxIdx == count {
			arr := make([]any, maxIdx)
			tb.ForEach(func(k, val lua.LValue) {
				idx := int(lua.LVAsNumber(k)) - 1
				if idx >= 0 && idx < maxIdx {
					arr[idx] = luaToGoValue(val)
				}
			})
			return arr
		}

		// 按实际键数预分配 map，避免边写边扩容的多次 rehash
		m := make(map[string]any, count)
		tb.ForEach(func(k, val lua.LValue) {
			m[lua.LVAsString(k)] = luaToGoValue(val)
		})
		return m
	case lua.LTUserData:
		if ud, ok := v.(*lua.LUserData); ok {
			if pm, ok := ud.Value.(proto.Message); ok {
				return pm
			}
		}
		return nil
	default:
		return nil
	}
}
