package script

import (
	"fmt"
	"strconv"
	"stressbot/state"

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
//	r.get_id()             → 返回机器人编号
//	r.get_account()        → 返回账号名
//	r.get_context()        → 检查 context 是否已取消
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
	// 元信息
	L.SetField(mod, "get_id", L.NewFunction(robotGetID))
	L.SetField(mod, "get_account", L.NewFunction(robotGetAccount))
	L.SetField(mod, "get_context", L.NewFunction(robotGetContext))

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
	case "get_id":
		L.Push(L.NewFunction(robotGetID))
	case "get_account":
		L.Push(L.NewFunction(robotGetAccount))
	case "get_context":
		L.Push(L.NewFunction(robotGetContext))
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

	args := extractArgs(L, L.GetTop())
	if len(args) < 2 {
		L.RaiseError("robot.set requires (key, value)")
		return 0
	}

	key := lua.LVAsString(args[0])
	value := luaToGoValue(args[1])
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

	args := extractArgs(L, L.GetTop())
	if len(args) < 1 {
		L.RaiseError("robot.has requires (key)")
		return 0
	}

	key := lua.LVAsString(args[0])
	L.Push(lua.LBool(ctx.Store.Has(key)))
	return 1
}

// robotDelete robot.delete(key) — 删除单个 key（必须传 key）
func robotDelete(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	args := extractArgs(L, L.GetTop())
	if len(args) == 0 {
		L.RaiseError("robot.delete requires (key)")
		return 0
	}
	key := lua.LVAsString(args[0])
	ctx.Store.Delete(key)
	return 0
}

// robotClear robot.clear(key?) — 删除指定 key 或清空全部
func robotClear(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	args := extractArgs(L, L.GetTop())
	if len(args) == 0 {
		ctx.Store.Clear()
		return 0
	}
	key := lua.LVAsString(args[0])
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

	args := extractArgs(L, L.GetTop())
	if len(args) < 1 {
		L.RaiseError("robot.increment requires (key)")
		return 0
	}

	key := lua.LVAsString(args[0])
	v := ctx.Store.Increment(key)
	L.Push(lua.LNumber(v))
	return 1
}

// robotKeys robot.keys() — 返回所有 key 列表
func robotKeys(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		tb := L.NewTable()
		L.Push(tb)
		return 1
	}
	keys := ctx.Store.Keys()
	tb := L.NewTable()
	for i, k := range keys {
		tb.RawSetInt(i+1, lua.LString(k))
	}
	L.Push(tb)
	return 1
}

// robotGetPath robot.get_path("a.b[0].c") — 按路径读取嵌套值
func robotGetPath(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNil)
		return 1
	}
	args := extractArgs(L, L.GetTop())
	if len(args) == 0 {
		L.Push(lua.LNil)
		return 1
	}
	path := lua.LVAsString(args[0])
	val := navigatePath(ctx.Store, path)
	L.Push(goValueToLua(L, val))
	return 1
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

// navigatePath 按路径 "a.b[0].c" 读取值。
// 顶层 a 从 Store 中获取，随后依次下钻 map/list。
func navigatePath(store interface {
	Get(key string) any
}, path string) any {
	if path == "" {
		return nil
	}
	segments := state.SplitPath(path)
	if len(segments) == 0 {
		return nil
	}
	var cur any = store.Get(segments[0])
	for i := 1; i < len(segments); i++ {
		seg := segments[i]
		if cur == nil {
			return nil
		}
		if len(seg) >= 2 && seg[0] == '[' && seg[len(seg)-1] == ']' {
			idxStr := seg[1 : len(seg)-1]
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				return nil
			}
			switch list := cur.(type) {
			case []any:
				if idx >= 0 && idx < len(list) {
					cur = list[idx]
				} else {
					return nil
				}
			default:
				return nil
			}
			continue
		}
		switch m := cur.(type) {
		case map[string]any:
			cur = m[seg]
		default:
			return nil
		}
	}
	return cur
}

// extractArgs 从 Lua 栈中提取参数，跳过 LUserData（self）
func extractArgs(L *lua.LState, top int) []lua.LValue {
	args := make([]lua.LValue, 0, top)
	for i := 1; i <= top; i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		args = append(args, v)
	}
	return args
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
		tb := L.NewTable()
		for i, elem := range v {
			tb.RawSetInt(i+1, goValueToLua(L, elem))
		}
		return tb
	case map[string]any:
		tb := L.NewTable()
		for k, elem := range v {
			tb.RawSetString(k, goValueToLua(L, elem))
		}
		return tb
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
		tb.ForEach(func(k, val lua.LValue) {
			if k.Type() == lua.LTNumber {
				idx := int(lua.LVAsNumber(k))
				if idx > maxIdx {
					maxIdx = idx
				}
			} else {
				isArray = false
			}
		})

		if isArray && maxIdx > 0 {
			arr := make([]any, maxIdx)
			tb.ForEach(func(k, val lua.LValue) {
				idx := int(lua.LVAsNumber(k)) - 1
				if idx >= 0 && idx < maxIdx {
					arr[idx] = luaToGoValue(val)
				}
			})
			return arr
		}

		m := make(map[string]any)
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
