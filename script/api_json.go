package script

import (
	"encoding/json"

	lua "github.com/yuin/gopher-lua"
)

// loadJsonModule 加载 json 命名空间模块。
// Lua 用法：
//
//	local json = require("json")
//	local t = json.decode('{"key":"value"}')  → Lua table
//	local s = json.encode({key = "value"})    → JSON string
func loadJsonModule(L *lua.LState) int {
	mod := L.NewTable()

	L.SetField(mod, "decode", L.NewFunction(jsonDecode))
	L.SetField(mod, "encode", L.NewFunction(jsonEncode))

	L.Push(mod)
	return 1
}

// jsonDecode json.decode(str) — 解码 JSON 字符串为 Lua table
func jsonDecode(L *lua.LState) int {
	str := L.CheckString(1)

	var data any
	if err := json.Unmarshal([]byte(str), &data); err != nil {
		L.RaiseError("json decode failed: %v", err)
		return 0
	}

	L.Push(goValueToLua(L, data))
	return 1
}

// jsonEncode json.encode(table) — 编码 Lua table 为 JSON 字符串
func jsonEncode(L *lua.LState) int {
	val := L.Get(1)
	data := luaToGoValue(val)

	bytes, err := json.Marshal(data)
	if err != nil {
		L.RaiseError("json encode failed: %v", err)
		return 0
	}

	L.Push(lua.LString(string(bytes)))
	return 1
}

// luaTableToGoMap 将 Lua table 转换为 Go map 或 slice（供 JSON 编码使用）
func luaTableToGoMap(tbl *lua.LTable) any {
	// 判断是数组还是对象
	isArray := true
	maxIdx := 0

	tbl.ForEach(func(k, v lua.LValue) {
		if n, ok := k.(lua.LNumber); ok {
			idx := int(n)
			if idx > maxIdx {
				maxIdx = idx
			}
		} else {
			isArray = false
		}
	})

	if isArray && maxIdx > 0 {
		arr := make([]any, maxIdx)
		tbl.ForEach(func(k, v lua.LValue) {
			if n, ok := k.(lua.LNumber); ok {
				idx := int(n)
				if idx >= 1 && idx <= maxIdx {
					arr[idx-1] = luaToGoValue(v)
				}
			}
		})
		return arr
	}

	m := make(map[string]any)
	tbl.ForEach(func(k, v lua.LValue) {
		m[lua.LVAsString(k)] = luaToGoValue(v)
	})
	return m
}
