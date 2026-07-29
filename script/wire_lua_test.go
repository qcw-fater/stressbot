package script

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/proto"

	"stressbot/protox"
)

// ── D1 直转器 Lua 出口差分：wireValueToLuaTable ≡ protoMessageToLuaTable(解码) ──

// newWireLuaTestFactory 覆盖标量装箱边界（大整数、bytes、枚举）、嵌套/重复/映射/oneof。
func newWireLuaTestFactory(t *testing.T) *protox.Factory {
	t.Helper()
	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package wirelua;

enum Grade {
  GRADE_UNSPECIFIED = 0;
  GOLD = 1;
}

message Item {
  int32  id   = 1;
  string name = 2;
}

message Box {
  int64          big    = 1;
  uint64         ubig   = 2;
  double         d      = 3;
  string         s      = 4;
  bytes          bs     = 5;
  Grade          g      = 6;
  Item           main   = 7;
  repeated Item  items  = 8;
  repeated int64 nums   = 9;
  map<string, Item>  kids   = 10;
  map<int32, string> labels = 11;
  oneof pick {
    int32 pick_num = 20;
    Item  pick_it  = 21;
  }
  optional int32 opt_i = 22;
}
`
	protoPath := filepath.Join(dir, "wire_lua_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}
	loader := protox.NewLoader([]string{dir}, []string{"wire_lua_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}
	return protox.NewFactory(protox.NewRegistry(files))
}

// luaDeepEqual 深比较两个 Lua 值（table 递归、键集合完全一致、标量含类型相等）。
func luaDeepEqual(a, b lua.LValue) (bool, string) {
	if a.Type() != b.Type() {
		return false, fmt.Sprintf("类型不同: %v vs %v", a.Type(), b.Type())
	}
	ta, ok := a.(*lua.LTable)
	if !ok {
		if a != b {
			return false, fmt.Sprintf("标量不同: %v vs %v", a, b)
		}
		return true, ""
	}
	tb := b.(*lua.LTable)
	var diff string
	equal := true
	seen := 0
	ta.ForEach(func(k, va lua.LValue) {
		seen++
		vb := tb.RawGet(k)
		if ok2, d := luaDeepEqual(va, vb); !ok2 {
			equal = false
			if diff == "" {
				diff = fmt.Sprintf("键 %v: %s", k, d)
			}
		}
	})
	other := 0
	tb.ForEach(func(k, _ lua.LValue) { other++ })
	if other != seen {
		return false, fmt.Sprintf("键数量不同: %d vs %d", seen, other)
	}
	return equal, diff
}

// assertWireLuaEqual 直转表 vs「dynamicpb 解码 + protoMessageToLuaTable」逐字比对。
func assertWireLuaEqual(t *testing.T, L *lua.LState, f *protox.Factory, raw []byte) {
	t.Helper()
	md, ok := f.MessageDescriptor("wirelua.Box")
	if !ok {
		t.Fatal("未找到 wirelua.Box")
	}
	wv := protox.NewWireValue(md, protox.WireSnapshot(raw))

	got, ok := wireValueToLuaTable(L, wv)
	if !ok {
		t.Fatal("直转被拒绝（未降级时不应发生）")
	}
	oracleMsg, err := f.Parse("wirelua.Box", raw)
	if err != nil {
		t.Fatalf("oracle 解码失败: %v", err)
	}
	want := protoMessageToLuaTable(L, oracleMsg)
	if equal, diff := luaDeepEqual(got, want); !equal {
		t.Fatalf("Lua 表不一致: %s", diff)
	}
}

func TestWireValueToLuaTableParity(t *testing.T) {
	protox.SetWireShadowEnabled(false)
	t.Cleanup(func() { protox.SetWireShadowEnabled(true) })

	f := newWireLuaTestFactory(t)
	L := lua.NewState()
	defer L.Close()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("SetField 失败: %v", err)
		}
	}

	// 用例 1：大整数（超 maxSafeInt 转字符串）、bytes、枚举、嵌套、repeated、map、oneof。
	raw1 := buildWireLuaBox(t, f)
	assertWireLuaEqual(t, L, f, raw1)

	// 用例 2：oneof 切换 + wire 拼接合并（main 分段 merge、标量 last-wins）。
	box2, err := f.Create("wirelua.Box")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	must(f.SetField(box2, "main.name", "merged"))
	must(f.SetField(box2, "pick_it.id", int64(9)))
	must(f.SetField(box2, "s", "second-wins"))
	raw2 := append(append([]byte(nil), raw1...), mustMarshalScript(t, box2)...)
	assertWireLuaEqual(t, L, f, raw2)

	// 用例 3：空消息（全默认值：标量在场取默认、message/repeated/map 缺席）。
	empty, err := f.Create("wirelua.Box")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	assertWireLuaEqual(t, L, f, mustMarshalScript(t, empty))
}

func mustMarshalScript(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

// buildWireLuaBox 构造覆盖装箱边界与容器形态的 Box 消息 wire 字节。
func buildWireLuaBox(t *testing.T, f *protox.Factory) []byte {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("SetField 失败: %v", err)
		}
	}
	box, err := f.Create("wirelua.Box")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	must(f.SetField(box, "big", int64(1)<<60))
	must(f.SetField(box, "ubig", uint64(1)<<63))
	must(f.SetField(box, "d", 3.5))
	must(f.SetField(box, "s", "hello"))
	must(f.SetField(box, "bs", []byte{0x00, 0xff, 0x7f}))
	must(f.SetField(box, "g", int64(1)))
	must(f.SetField(box, "main.id", int64(7))) // main.name 保持默认
	must(f.SetField(box, "items[0].id", int64(1)))
	must(f.SetField(box, "items[1].name", "y"))
	must(f.SetField(box, "nums", []any{int64(10), int64(1) << 55}))
	kid, err := f.Create("wirelua.Item")
	if err != nil {
		t.Fatalf("创建 Item 失败: %v", err)
	}
	must(f.SetField(kid, "id", int64(3)))
	must(f.SetField(box, "kids", map[any]any{"a": kid}))
	must(f.SetField(box, "labels", map[any]any{int64(-2): "neg"}))
	must(f.SetField(box, "pick_num", int64(5)))
	return mustMarshalScript(t, box)
}

// TestWireViewProtoAPI wire 惰性视图（D2）与 Frozen 共享解码双读同一消息，
// 走完整 proto.* Lua API 面逐字比对；另验 set_field fail-loud 与 serialize 原字节。
func TestWireViewProtoAPI(t *testing.T) {
	protox.SetWireShadowEnabled(false)
	t.Cleanup(func() { protox.SetWireShadowEnabled(true) })

	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	t.Cleanup(L.Close)
	f := newWireLuaTestFactory(t)
	GetContext(L).Factory = f

	raw := buildWireLuaBox(t, f)
	md, ok := f.MessageDescriptor("wirelua.Box")
	if !ok {
		t.Fatal("未找到 wirelua.Box")
	}
	decoded, err := f.Parse("wirelua.Box", raw)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	L.SetGlobal("vmsg", wrapWireView(L, protox.NewWireValue(md, protox.WireSnapshot(raw))))
	L.SetGlobal("fmsg", wrapFrozenMessage(L, protox.Freeze(decoded)))

	script := `
local proto = require("proto")
local function read(m)
  local t = {}
  t.big       = proto.get_field(m, "big")
  t.s         = proto.get_field(m, "s")
  t.bs        = proto.get_field(m, "bs")
  t.g         = proto.get_field(m, "g")
  t.main      = proto.get_field(m, "main")
  t.main_name = proto.get_path(m, "main.name")   -- 已设置 message 的默认标量
  t.loser     = proto.get_path(m, "pick_it.name") -- oneof 落选 message 下钻 → ""
  t.opt       = proto.get_field(m, "opt_i")       -- 未设置 optional → 0
  t.kids      = proto.get_field(m, "kids")
  t.labels    = proto.get_field(m, "labels")
  t.items_n   = proto.list_size(m, "items")
  local it1   = proto.list_get(m, "items", 1)
  t.item1_id  = proto.get_field(it1, "id")
  t.iter      = {}
  for i, item in proto.iter_list(m, "items") do t.iter[i] = item end
  t.tree      = proto.get_field_map(m)
  return t
end
va = read(vmsg)
fa = read(fmsg)
vser = proto.serialize(vmsg)
local ok = pcall(function() proto.set_field(vmsg, "s", "x") end)
assert(not ok, "wire 视图 set_field 应 fail-loud")
`
	if err := L.DoString(script); err != nil {
		t.Fatalf("脚本执行失败: %v", err)
	}
	if equal, diff := luaDeepEqual(L.GetGlobal("va"), L.GetGlobal("fa")); !equal {
		t.Fatalf("视图与解码路径读数不一致: %s", diff)
	}
	if got := lua.LVAsString(L.GetGlobal("vser")); got != string(raw) {
		t.Fatalf("serialize 应返回原始字节: got %d bytes want %d", len(got), len(raw))
	}
}
