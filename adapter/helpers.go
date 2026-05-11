package adapter

import (
	"encoding/binary"
	"fmt"
	"math"

	lua "github.com/yuin/gopher-lua"
)

// BodyLengthInfo 消息体长度字段元信息。
// 初始化时从 Lua 适配器脚本的 body_length_info() 函数获取并缓存。
type BodyLengthInfo struct {
	Offset         int    // header 中 body length 字段的字节偏移
	FieldType      string // 字段类型："uint32_le"/"uint16_le"/"uint32_be"/"uint16_be"
	IncludesHeader bool   // length 值是否包含 header 自身大小（true 则减去 HeaderSize）
}

// ReadBodyLength 从 header 字节中原生读取 body 长度。
// 纯 Go 实现，无任何 Lua 调用。
func ReadBodyLength(headerData []byte, info BodyLengthInfo, headerSize int) int {
	switch info.FieldType {
	case "uint32_le":
		if len(headerData) < info.Offset+4 {
			return 0
		}
		raw := binary.LittleEndian.Uint32(headerData[info.Offset:])
		n := int(raw)
		if info.IncludesHeader {
			n -= headerSize
		}
		if n < 0 {
			n = 0
		}
		return n
	case "uint32_be":
		if len(headerData) < info.Offset+4 {
			return 0
		}
		raw := binary.BigEndian.Uint32(headerData[info.Offset:])
		n := int(raw)
		if info.IncludesHeader {
			n -= headerSize
		}
		if n < 0 {
			n = 0
		}
		return n
	case "uint16_le":
		if len(headerData) < info.Offset+2 {
			return 0
		}
		raw := binary.LittleEndian.Uint16(headerData[info.Offset:])
		n := int(raw)
		if info.IncludesHeader {
			n -= headerSize
		}
		if n < 0 {
			n = 0
		}
		return n
	case "uint16_be":
		if len(headerData) < info.Offset+2 {
			return 0
		}
		raw := binary.BigEndian.Uint16(headerData[info.Offset:])
		n := int(raw)
		if info.IncludesHeader {
			n -= headerSize
		}
		if n < 0 {
			n = 0
		}
		return n
	default:
		if len(headerData) < info.Offset+4 {
			return 0
		}
		raw := binary.LittleEndian.Uint32(headerData[info.Offset:])
		n := int(raw)
		if info.IncludesHeader {
			n -= headerSize
		}
		if n < 0 {
			n = 0
		}
		return n
	}
}

// RouteToLuaValue 将 Go 的 route any 转换为 Lua 值。
// JSON 中的数值反序列化为 float64，整数值统一转为 int 以保证路由键字符串一致。
func RouteToLuaValue(L *lua.LState, route any) lua.LValue {
	if route == nil {
		return lua.LNil
	}
	switch v := route.(type) {
	case map[string]any:
		tbl := L.NewTable()
		for k, val := range v {
			L.SetField(tbl, k, RouteToLuaValue(L, val))
		}
		return tbl
	case float64:
		if v == math.Trunc(v) && !math.IsInf(v, 0) {
			return lua.LNumber(int64(v))
		}
		return lua.LNumber(v)
	case string:
		return lua.LString(v)
	case bool:
		return lua.LBool(v)
	case int:
		return lua.LNumber(v)
	case int64:
		return lua.LNumber(v)
	default:
		return lua.LString(fmt.Sprintf("%v", v))
	}
}

// LoadBitModule 注册 bit 模块到 LState。
// Lua 5.1 不支持位运算符，通过此模块提供位运算原语。
func LoadBitModule(L *lua.LState) int {
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
