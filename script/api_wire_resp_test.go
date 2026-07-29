package script

import (
	"bytes"
	"context"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"stressbot/protox"
)

// newWireRespState 构造带响应句柄的 Lua 环境：resp 全局变量是
// wrapRespMessage(解码消息, body 快照) 的产物，模拟 network.tcp_request_route 返回值。
func newWireRespState(t *testing.T) (*lua.LState, *protox.Factory, []byte) {
	t.Helper()
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	t.Cleanup(L.Close)

	f, msg := newFrozenTestFactoryMessage(t)
	GetContext(L).Factory = f

	raw, err := f.Serialize(msg)
	if err != nil {
		t.Fatalf("Serialize 失败: %v", err)
	}
	L.SetGlobal("resp", wrapRespMessage(L, msg, protox.WireSnapshot(raw)))
	return L, f, raw
}

// TestRespCleanStoreReuseRaw 未改写的响应 robot.set 后以 *WireValue 入库，
// 且直接复用响应携带的 body 快照（零重编码）；读路径惰性取值与旧展开语义一致。
func TestRespCleanStoreReuseRaw(t *testing.T) {
	L, _, raw := newWireRespState(t)

	if err := L.DoString(`
		local robot = require("robot")
		robot.set("playerData", resp)
		uid = robot.get_path("playerData.uid")
		mainId = robot.get_path("playerData.main.id")
		mainName = robot.get_path("playerData.main.name")
		secondItemName = robot.get_path("playerData.items[1].name")
		local whole = robot.get("playerData")
		wholeType = type(whole)
		wholeUID = whole.uid
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	wv, ok := GetContext(L).Store.Get("playerData").(*protox.WireValue)
	if !ok {
		t.Fatalf("playerData 应为 *protox.WireValue，实际 %T", GetContext(L).Store.Get("playerData"))
	}
	if !bytes.Equal(wv.Raw(), raw) {
		t.Fatal("干净响应入库应复用原始 body 字节（零重编码）")
	}

	assertGlobal := func(name string, want lua.LValue) {
		t.Helper()
		if got := L.GetGlobal(name); got != want {
			t.Fatalf("%s=%v (%T) want %v", name, got, got, want)
		}
	}
	assertGlobal("uid", lua.LNumber(42))
	assertGlobal("mainId", lua.LNumber(7))
	assertGlobal("mainName", lua.LString("")) // proto3 默认值在场
	assertGlobal("secondItemName", lua.LString("y"))
	assertGlobal("wholeType", lua.LString("table"))
	assertGlobal("wholeUID", lua.LNumber(42))
}

// TestRespDirtyStoreRemarshal proto.set_field 改写响应后 robot.set 不得复用旧快照：
// 重编码为当前内容，入库值反映改写。
func TestRespDirtyStoreRemarshal(t *testing.T) {
	L, _, raw := newWireRespState(t)

	if err := L.DoString(`
		local robot = require("robot")
		local proto = require("proto")
		proto.set_field(resp, "title", "rewritten")
		robot.set("playerData", resp)
		title = robot.get_path("playerData.title")
		uid = robot.get_path("playerData.uid")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	wv, ok := GetContext(L).Store.Get("playerData").(*protox.WireValue)
	if !ok {
		t.Fatalf("playerData 应为 *protox.WireValue，实际 %T", GetContext(L).Store.Get("playerData"))
	}
	if bytes.Equal(wv.Raw(), raw) {
		t.Fatal("已改写的响应不得复用原始快照（会丢失改写）")
	}
	if got := L.GetGlobal("title"); got != lua.LString("rewritten") {
		t.Fatalf("title=%v want rewritten", got)
	}
	if got := L.GetGlobal("uid"); got != lua.LNumber(42) {
		t.Fatalf("uid=%v want 42（改写不应丢其他字段）", got)
	}
}

// TestRespChildDirtyPropagates 经 list_get 取出的子消息被改写时，
// 同样置脏根句柄（同根共享脏标记），存储不得复用原始快照。
func TestRespChildDirtyPropagates(t *testing.T) {
	L, _, raw := newWireRespState(t)

	if err := L.DoString(`
		local robot = require("robot")
		local proto = require("proto")
		local item = proto.list_get(resp, "items", 1)
		proto.set_field(item, "name", "child-write")
		robot.set("playerData", resp)
		itemName = robot.get_path("playerData.items[0].name")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	wv, ok := GetContext(L).Store.Get("playerData").(*protox.WireValue)
	if !ok {
		t.Fatalf("playerData 应为 *protox.WireValue，实际 %T", GetContext(L).Store.Get("playerData"))
	}
	if bytes.Equal(wv.Raw(), raw) {
		t.Fatal("子消息被改写后不得复用原始快照")
	}
	if got := L.GetGlobal("itemName"); got != lua.LString("child-write") {
		t.Fatalf("items[0].name=%v want child-write", got)
	}
}

// TestRespStoreThenNestedWrite 整存响应后的嵌套路径写入走 Overlay COW：
// 写值可读回、兄弟字段保留、原始字节不受影响。
func TestRespStoreThenNestedWrite(t *testing.T) {
	L, _, _ := newWireRespState(t)

	if err := L.DoString(`
		local robot = require("robot")
		robot.set("playerData", resp)
		robot.set_path("playerData.main.id", 99)
		newId = robot.get_path("playerData.main.id")
		mainName = robot.get_path("playerData.main.name")
		uid = robot.get_path("playerData.uid")
		robot.set_path("playerData.main", nil)
		afterDelete = robot.get_path("playerData.main")
		uidAfter = robot.get_path("playerData.uid")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertGlobal := func(name string, want lua.LValue) {
		t.Helper()
		if got := L.GetGlobal(name); got != want {
			t.Fatalf("%s=%v (%T) want %v", name, got, got, want)
		}
	}
	assertGlobal("newId", lua.LNumber(99))
	assertGlobal("mainName", lua.LString(""))
	assertGlobal("uid", lua.LNumber(42))
	assertGlobal("afterDelete", lua.LNil) // 墓碑遮蔽 wire 旧值
	assertGlobal("uidAfter", lua.LNumber(42))
}
