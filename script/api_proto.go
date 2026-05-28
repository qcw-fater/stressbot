package script

import (
	lua "github.com/yuin/gopher-lua"

	"google.golang.org/protobuf/proto"
)

// loadProtoModule 加载 proto 命名空间模块。
// Lua 用法：
//
//	local proto = require("proto")
//	local msg = proto.create("example.RequestC2S")
//	proto.set_field(msg, "PlayerId", 10001)
//	local playerId = proto.get_field(msg, "PlayerId")
//	local bytes = proto.serialize(msg)
//	local parsed = proto.parse("example.ResponseS2C", bytes)
//	local fields = proto.get_field_map(msg)
//	for i, item in proto.iter_list(msg, "items") do ... end
//	local n = proto.list_size(msg, "items")
//	local item = proto.list_get(msg, "items", 1)
func loadProtoModule(L *lua.LState) int {
	mod := L.NewTable()

	L.SetField(mod, "create", L.NewFunction(protoCreate))
	L.SetField(mod, "set_field", L.NewFunction(protoSetField))
	L.SetField(mod, "get_field", L.NewFunction(protoGetField))
	L.SetField(mod, "get_path", L.NewFunction(protoGetField))
	L.SetField(mod, "serialize", L.NewFunction(protoSerialize))
	L.SetField(mod, "parse", L.NewFunction(protoParse))
	L.SetField(mod, "get_field_map", L.NewFunction(protoGetFieldMap))
	L.SetField(mod, "iter_list", L.NewFunction(protoIterList))
	L.SetField(mod, "list_size", L.NewFunction(protoListSize))
	L.SetField(mod, "list_get", L.NewFunction(protoListGet))

	L.Push(mod)
	return 1
}

// protoMsgIndex proto 消息对象的 __index 元方法
func protoMsgIndex(L *lua.LState) int {
	method := L.CheckString(2)
	switch method {
	case "set_field":
		L.Push(L.NewFunction(protoSetField))
	case "get_field":
		L.Push(L.NewFunction(protoGetField))
	case "get_path":
		L.Push(L.NewFunction(protoGetField))
	case "serialize":
		L.Push(L.NewFunction(protoSerialize))
	case "get_field_map":
		L.Push(L.NewFunction(protoGetFieldMap))
	default:
		L.RaiseError("unknown proto message method: %s", method)
	}
	return 1
}

// checkProtoMsg 从栈中获取 proto.Message（跳过 self 参数）
func checkProtoMsg(L *lua.LState) proto.Message {
	top := L.GetTop()
	for i := 1; i <= top; i++ {
		v := L.Get(i)
		if ud, ok := v.(*lua.LUserData); ok {
			if msg, ok := ud.Value.(proto.Message); ok {
				return msg
			}
		}
	}
	L.RaiseError("expected proto message (userdata)")
	return nil
}

// wrapProtoMessage 将 proto.Message 包装为带 __index 元方法的 LUserData。
func wrapProtoMessage(L *lua.LState, msg proto.Message) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = msg
	mt := L.NewTable()
	L.SetField(mt, "__index", L.NewFunction(protoMsgIndex))
	L.SetMetatable(ud, mt)
	return ud
}

// protoCreate proto.create(name) — 创建动态 proto 消息
func protoCreate(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.RaiseError("proto factory not available")
		return 0
	}

	name := L.CheckString(1)
	msg, err := ctx.Factory.Create(name)
	if err != nil {
		L.RaiseError("create proto message failed: %v", err)
		return 0
	}

	ud := wrapProtoMessage(L, msg)

	L.Push(ud)
	return 1
}

// protoSetField proto.set_field(msg, field, value) — 设置 proto 字段
// value 支持标量、表、以及嵌套 proto 消息（LUserData）
func protoSetField(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.RaiseError("proto factory not available")
		return 0
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		return 0
	}

	// 找到 msg 在栈中的位置，然后按相对位置取参数
	msgIdx := 0
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if ud, ok := v.(*lua.LUserData); ok {
			if _, ok2 := ud.Value.(proto.Message); ok2 {
				msgIdx = i
				break
			}
		}
	}

	fieldName := L.CheckString(msgIdx + 1)
	fieldValue := L.CheckAny(msgIdx + 2)

	// 如果 value 是 LUserData 且包含 proto.Message，直接传递（用于嵌套消息）
	var goVal any
	if ud, ok := fieldValue.(*lua.LUserData); ok {
		if subMsg, ok2 := ud.Value.(proto.Message); ok2 {
			goVal = subMsg
		} else {
			goVal = luaToGoValue(fieldValue)
		}
	} else {
		goVal = luaToGoValue(fieldValue)
	}

	if err := ctx.Factory.SetField(msg, fieldName, goVal); err != nil {
		L.RaiseError("set field %s failed: %v", fieldName, err)
	}

	return 0
}

// protoGetField proto.get_field(msg, field) — 获取 proto 字段值
func protoGetField(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}

	// 提取字段名参数
	var fieldName string
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if fieldName == "" {
			fieldName = lua.LVAsString(v)
		}
	}

	if fieldName == "" {
		L.RaiseError("proto.get_field requires (msg, field)")
		return 0
	}

	val, err := ctx.Factory.GetField(msg, fieldName)
	if err != nil {
		L.RaiseError("get field %s failed: %v", fieldName, err)
		return 0
	}

	L.Push(goValueToLua(L, val))
	return 1
}

// protoSerialize proto.serialize(msg) — 序列化 proto 消息为字节
func protoSerialize(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}

	data, err := ctx.Factory.Serialize(msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}

	L.Push(lua.LString(string(data)))
	return 1
}

// protoParse proto.parse(name, data) — 反序列化字节为 proto 消息
func protoParse(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.RaiseError("proto factory not available")
		return 0
	}

	// 提取 name 和 data 参数
	var name string
	var data []byte

	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if name == "" {
			name = lua.LVAsString(v)
		} else if data == nil {
			data = []byte(lua.LVAsString(v))
		}
	}

	if name == "" || data == nil {
		L.RaiseError("proto.parse requires (name, data)")
		return 0
	}

	msg, err := ctx.Factory.Parse(name, data)
	if err != nil {
		L.RaiseError("parse proto %s failed: %v", name, err)
		return 0
	}

	ud := wrapProtoMessage(L, msg)

	L.Push(ud)
	return 1
}

// protoGetFieldMap proto.get_field_map(msg) — 获取所有字段（返回 table）
func protoGetFieldMap(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}

	fieldMap := ctx.Factory.GetFieldMap(msg)
	result := L.NewTable()
	for k, v := range fieldMap {
		result.RawSetString(k, goValueToLua(L, v))
	}

	L.Push(result)
	return 1
}

// protoIterList proto.iter_list(msg, field) → iterator
// for idx, item in proto.iter_list(msg, "items") do ... end
// item 为子 proto 消息（LUserData）。
func protoIterList(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}
	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}
	fieldName := findFirstStringArg(L)
	if fieldName == "" {
		L.RaiseError("proto.iter_list requires (msg, field)")
		return 0
	}
	val, err := ctx.Factory.GetField(msg, fieldName)
	if err != nil {
		L.Push(lua.LNil)
		return 1
	}
	list, _ := val.([]any)

	idx := 0
	iter := L.NewFunction(func(L *lua.LState) int {
		if idx >= len(list) {
			L.Push(lua.LNil)
			return 1
		}
		item := list[idx]
		idx++
		L.Push(lua.LNumber(idx))
		// 嵌套 proto 消息保留为 userdata
		if pm, ok := item.(proto.Message); ok {
			L.Push(wrapProtoMessage(L, pm))
		} else {
			L.Push(goValueToLua(L, item))
		}
		return 2
	})
	L.Push(iter)
	return 1
}

// protoListSize proto.list_size(msg, field) → 返回 list 字段长度
func protoListSize(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	fieldName := findFirstStringArg(L)
	n, err := ctx.Factory.GetListLen(msg, fieldName)
	if err != nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(n))
	return 1
}

// protoListGet proto.list_get(msg, field, idx) → 返回 list 字段第 idx 项 (1-based)
func protoListGet(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}
	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}
	// 收集非 userdata 参数
	var fieldName string
	var idx int = -1
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if fieldName == "" {
			fieldName = lua.LVAsString(v)
		} else if idx < 0 {
			idx = int(lua.LVAsNumber(v))
		}
	}
	i := idx - 1
	item, err := ctx.Factory.GetListItem(msg, fieldName, i)
	if err != nil {
		L.Push(lua.LNil)
		return 1
	}
	if pm, ok := item.(proto.Message); ok {
		L.Push(wrapProtoMessage(L, pm))
	} else {
		L.Push(goValueToLua(L, item))
	}
	return 1
}

// findFirstStringArg 从参数列表中找第一个字符串参数（跳过 userdata）
func findFirstStringArg(L *lua.LState) string {
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if v.Type() == lua.LTString {
			return string(v.(lua.LString))
		}
	}
	return ""
}
