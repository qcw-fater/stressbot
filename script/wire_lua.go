package script

import (
	"stressbot/protox"

	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ── WireValue → Lua table 直转出口（D1 直转器的 Lua 侧）──────────────
//
// robot.get / get_path 整读 wire 存储值时，旧路径是 `dynamicpb 全量解码 →
// protoMessageToLuaTable 遍历解码树建表`——每次整读一棵纯过路的解码树（8000 人
// 剖面：解码 4% CPU + 由此拉动的 GC）。本出口把 WalkWire 的遍历事件直接落成
// Lua 表，中间树消失；产物与 protoMessageToLuaTable(dynamicpb 解码) 逐字一致
// （标量装箱共用 goValueToLua 的标量分支，表容量预分配同款规则），由
// wire_lua_test.go 的差分用例守护；walker 语义本身另有 protox 侧差分 fuzz +
// 线上影子采样（MaterializeAllowed）兜底。

// wireValueToLuaTable 尝试直转。返回 ok=false（schema 降级 / 采样失配 / 结构
// 损坏）时调用方回落解码路径。
func wireValueToLuaTable(L *lua.LState, wv *protox.WireValue) (lua.LValue, bool) {
	if !wv.MaterializeAllowed() {
		return lua.LNil, false
	}
	root := newLuaTreeSink(L, wv.Desc())
	if err := wv.Walk(root); err != nil {
		// ValidateWire 通过的字节上 Walk 失败 = 扫描器 bug：留证据日志并降级，
		// 绝不静默回退（否则 bug 无从排查）。
		wv.ReportWireFailure("lua-materialize", err)
		return lua.LNil, false
	}
	return root.tb, true
}

// luaTreeSink message 层级出口：hash 容量按字段总数预分配（与 protoMessageToLuaTable 一致）。
type luaTreeSink struct {
	L  *lua.LState
	tb *lua.LTable
}

func newLuaTreeSink(L *lua.LState, md protoreflect.MessageDescriptor) *luaTreeSink {
	return &luaTreeSink{L: L, tb: L.CreateTable(0, md.Fields().Len())}
}

func (s *luaTreeSink) Scalar(fd protoreflect.FieldDescriptor, v any) {
	// goValueToLua 的标量分支与 protoScalarToLua 装箱逐字一致（大整数转字符串、
	// bytes 转 string）；容器类型不会出现在 Scalar 事件里。
	s.tb.RawSetString(string(fd.Name()), goValueToLua(s.L, v))
}

func (s *luaTreeSink) Message(fd protoreflect.FieldDescriptor) protox.WireTreeSink {
	child := newLuaTreeSink(s.L, fd.Message())
	s.tb.RawSetString(string(fd.Name()), child.tb)
	return child
}

func (s *luaTreeSink) List(fd protoreflect.FieldDescriptor, n int) protox.WireListSink {
	tb := s.L.CreateTable(n, 0)
	s.tb.RawSetString(string(fd.Name()), tb)
	return &luaListSink{L: s.L, tb: tb, elemDesc: listElemDesc(fd)}
}

func (s *luaTreeSink) Map(fd protoreflect.FieldDescriptor, n int) protox.WireMapSink {
	tb := s.L.CreateTable(0, n)
	s.tb.RawSetString(string(fd.Name()), tb)
	return &luaMapSink{L: s.L, tb: tb, valDesc: mapValDesc(fd)}
}

func listElemDesc(fd protoreflect.FieldDescriptor) protoreflect.MessageDescriptor {
	if fd.Kind() == protoreflect.MessageKind {
		return fd.Message()
	}
	return nil
}

func mapValDesc(fd protoreflect.FieldDescriptor) protoreflect.MessageDescriptor {
	if fd.MapValue().Kind() == protoreflect.MessageKind {
		return fd.MapValue().Message()
	}
	return nil
}

type luaListSink struct {
	L        *lua.LState
	tb       *lua.LTable
	elemDesc protoreflect.MessageDescriptor
	next     int
}

func (l *luaListSink) ScalarElem(v any) {
	l.next++
	l.tb.RawSetInt(l.next, goValueToLua(l.L, v))
}

func (l *luaListSink) MessageElem() protox.WireTreeSink {
	child := newLuaTreeSink(l.L, l.elemDesc)
	l.next++
	l.tb.RawSetInt(l.next, child.tb)
	return child
}

type luaMapSink struct {
	L       *lua.LState
	tb      *lua.LTable
	valDesc protoreflect.MessageDescriptor
}

func (m *luaMapSink) ScalarEntry(key string, v any) {
	m.tb.RawSetString(key, goValueToLua(m.L, v))
}

func (m *luaMapSink) MessageEntry(key string) protox.WireTreeSink {
	child := newLuaTreeSink(m.L, m.valDesc)
	m.tb.RawSetString(key, child.tb)
	return child
}
