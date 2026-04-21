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
