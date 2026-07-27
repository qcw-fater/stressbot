package script

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/proto"

	"stressbot/protox"
)

// newFrozenTestMessage 构造并经序列化→反序列化还原线上 wire 行为的测试消息
// （取默认值的字段不上线，验证边界转换保留 proto3 默认值）。
func newFrozenTestMessage(t *testing.T) proto.Message {
	t.Helper()
	_, msg := newFrozenTestFactoryMessage(t)
	return msg
}

// newFrozenTestFactoryMessage 同 newFrozenTestMessage，但把构造消息用的 Factory
// 一并返回（proto API 测试需要 ctx.Factory）。
func newFrozenTestFactoryMessage(t *testing.T) (*protox.Factory, proto.Message) {
	t.Helper()
	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package frozentest;

message Item {
  int32  id   = 1;
  string name = 2;
}

message Bag {
  int64         uid   = 1;
  string        title = 2;
  Item          main  = 3;
  repeated Item items = 4;
  repeated int32 nums = 5;
}
`
	protoPath := filepath.Join(dir, "frozen_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}
	loader := protox.NewLoader([]string{dir}, []string{"frozen_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}
	f := protox.NewFactory(protox.NewRegistry(files))

	bag, err := f.Create("frozentest.Bag")
	if err != nil {
		t.Fatalf("创建 Bag 失败: %v", err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("SetField 失败: %v", err)
		}
	}
	must(f.SetField(bag, "uid", int64(42)))
	must(f.SetField(bag, "main.id", int64(7))) // main.name 保持默认 ""
	must(f.SetField(bag, "items[0].id", int64(1)))
	must(f.SetField(bag, "items[1].name", "y"))
	must(f.SetField(bag, "nums", []any{10, 20, 30}))

	raw, err := f.Serialize(bag)
	if err != nil {
		t.Fatalf("Serialize 失败: %v", err)
	}
	parsed, err := f.Parse("frozentest.Bag", raw)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	return f, parsed
}

// TestRobotGetFrozen 校验整存 Frozen 后 Lua 边界语义不变：
// robot.get / robot.get_path 现场转真 table（type=="table"、proto3 默认值字段在场、
// 嵌套/列表可自由遍历），与旧的"展开 map 常驻 + goValueToLua"产出逐字一致。
func TestRobotGetFrozen(t *testing.T) {
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	defer L.Close()

	msg := newFrozenTestMessage(t)
	GetContext(L).Store.Set("loginResp", protox.Freeze(msg))

	if err := L.DoString(`
		local robot = require("robot")
		local v = robot.get("loginResp")
		tType = type(v)
		uid = v.uid
		mainId = v.main.id
		mainName = v.main.name
		mainNameType = type(v.main.name)
		titleType = type(v.title)      -- 顶层默认值 "" 同样在场
		itemCount = #v.items
		firstItemId = v.items[1].id
		secondItemName = v.items[2].name
		num2 = v.nums[2]
		pathMainId = robot.get_path("loginResp.main.id")
		local pl = robot.get_path("loginResp.items")
		pathListType = type(pl)
		pathSecondName = pl[2].name
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertGlobal := func(name string, want lua.LValue) {
		t.Helper()
		if got := L.GetGlobal(name); got != want {
			t.Fatalf("%s=%v (%T) want %v", name, got, got, want)
		}
	}
	assertGlobal("tType", lua.LString("table"))
	assertGlobal("uid", lua.LNumber(42))
	assertGlobal("mainId", lua.LNumber(7))
	assertGlobal("mainName", lua.LString(""))
	assertGlobal("mainNameType", lua.LString("string"))
	assertGlobal("titleType", lua.LString("string"))
	assertGlobal("itemCount", lua.LNumber(2))
	assertGlobal("firstItemId", lua.LNumber(1))
	assertGlobal("secondItemName", lua.LString("y"))
	assertGlobal("num2", lua.LNumber(20))
	assertGlobal("pathMainId", lua.LNumber(7))
	assertGlobal("pathListType", lua.LString("table"))
	assertGlobal("pathSecondName", lua.LString("y"))
}
