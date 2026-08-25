package script

import (
	"encoding/json"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	lua "github.com/yuin/gopher-lua"
)

// jsonStdConfig 与 encoding/json 行为兼容的 sonic 配置（HTML 转义 + map key 排序），
// 保证 json.encode 输出稳定且与旧实现一致。
var jsonStdConfig = sonic.ConfigStd

// loadJSONModule 加载 json 命名空间模块。
// Lua 用法：
//
//	local json = require("json")
//	local t = json.decode('{"key":"value"}')  → Lua table
//	local s = json.encode({key = "value"})    → JSON string
func loadJSONModule(L *lua.LState) int {
	mod := L.NewTable()

	L.SetField(mod, "decode", L.NewFunction(jsonDecode))
	L.SetField(mod, "encode", L.NewFunction(jsonEncode))

	L.Push(mod)
	return 1
}

// luaJSONFrame 记录正在构建的容器表及其填充进度。
type luaJSONFrame struct {
	tb      *lua.LTable
	isArray bool
	arrIdx  int    // 数组下一个写入下标（1-based）
	key     string // 对象当前待写入的 key
}

// luaJSONBuilder 实现 ast.Visitor，在 sonic 流式解析 JSON 的同时直接构建 Lua 表，
// 跳过中间 map[string]any / []any 这棵 Go 对象树，省掉一整份分配。
type luaJSONBuilder struct {
	L     *lua.LState
	stack []luaJSONFrame
	root  lua.LValue
}

// addValue 把一个标量/容器值挂到当前栈顶容器（数组追加 / 对象按 key 写入），
// 栈为空时即为根值。
func (b *luaJSONBuilder) addValue(v lua.LValue) {
	n := len(b.stack)
	if n == 0 {
		b.root = v
		return
	}
	f := &b.stack[n-1]
	if f.isArray {
		f.tb.RawSetInt(f.arrIdx, v)
		f.arrIdx++
	} else {
		f.tb.RawSetString(f.key, v)
	}
}

func (b *luaJSONBuilder) pushContainer(tb *lua.LTable, isArray bool) {
	b.addValue(tb) // 先挂到父容器，再压栈供子元素填充
	b.stack = append(b.stack, luaJSONFrame{tb: tb, isArray: isArray, arrIdx: 1})
}

func (b *luaJSONBuilder) OnNull() error           { b.addValue(lua.LNil); return nil }
func (b *luaJSONBuilder) OnBool(v bool) error     { b.addValue(lua.LBool(v)); return nil }
func (b *luaJSONBuilder) OnString(v string) error { b.addValue(lua.LString(v)); return nil }

func (b *luaJSONBuilder) OnInt64(v int64, n json.Number) error {
	// 与 goValueToLua / protoScalarToLua 一致：超出 double 精度范围的整数转字符串避免精度丢失
	if v > maxSafeInt || v < -maxSafeInt {
		b.addValue(lua.LString(n.String()))
	} else {
		b.addValue(lua.LNumber(v))
	}
	return nil
}

func (b *luaJSONBuilder) OnFloat64(v float64, n json.Number) error {
	// 超过 int64 范围的整数字面量会以 float64 回调，这里继续按 maxSafeInt 规则转字符串，
	// 避免 18446744073709551615 这类 JSON 整数在 Lua number 中丢精度。
	if jsonIntegerLiteralExceedsSafeInt(n.String()) {
		b.addValue(lua.LString(n.String()))
	} else {
		b.addValue(lua.LNumber(v))
	}
	return nil
}

func jsonIntegerLiteralExceedsSafeInt(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	const maxSafeIntText = "9007199254740992"
	if len(s) != len(maxSafeIntText) {
		return len(s) > len(maxSafeIntText)
	}
	return s > maxSafeIntText
}

func (b *luaJSONBuilder) OnObjectBegin(capacity int) error {
	b.pushContainer(b.L.CreateTable(0, capacity), false)
	return nil
}

func (b *luaJSONBuilder) OnObjectKey(key string) error {
	b.stack[len(b.stack)-1].key = key
	return nil
}

func (b *luaJSONBuilder) OnObjectEnd() error {
	b.stack = b.stack[:len(b.stack)-1]
	return nil
}

func (b *luaJSONBuilder) OnArrayBegin(capacity int) error {
	b.pushContainer(b.L.CreateTable(capacity, 0), true)
	return nil
}

func (b *luaJSONBuilder) OnArrayEnd() error {
	b.stack = b.stack[:len(b.stack)-1]
	return nil
}

// jsonDecode json.decode(str) — 解码 JSON 字符串为 Lua table。
// 使用 sonic 流式解析 + 直接构建 Lua 表，不经过中间 Go map/slice 树。
func jsonDecode(L *lua.LState) int {
	str := L.CheckString(1)

	b := &luaJSONBuilder{L: L, root: lua.LNil}
	if err := ast.Preorder(str, b, nil); err != nil {
		L.RaiseError("json decode failed: %v", err)
		return 0
	}

	L.Push(b.root)
	return 1
}

// jsonEncode json.encode(table) — 编码 Lua table 为 JSON 字符串
func jsonEncode(L *lua.LState) int {
	val := L.Get(1)
	data := luaToGoValue(val)

	bytes, err := jsonStdConfig.Marshal(data)
	if err != nil {
		L.RaiseError("json encode failed: %v", err)
		return 0
	}

	L.Push(lua.LString(string(bytes)))
	return 1
}
