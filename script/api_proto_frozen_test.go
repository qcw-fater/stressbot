package script

import (
	"context"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"stressbot/protocol/protox"
)

// newFrozenProtoEnv 构造带 Factory 的测试 LState，并把共享只读消息
// （wrapFrozenMessage 包装的 *Frozen）注入全局 fmsg。
func newFrozenProtoEnv(t *testing.T) *lua.LState {
	t.Helper()
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	t.Cleanup(L.Close)
	f, msg := newFrozenTestFactoryMessage(t)
	GetContext(L).Factory = f
	L.SetGlobal("fmsg", wrapFrozenMessage(L, protox.Freeze(msg)))
	return L
}

// TestFrozenProtoAPI_ReadAccessors 校验共享只读消息的全部读访问器透传底层消息：
// get_field / get_path / list_size / list_get / iter_list / get_field_map / serialize
// 与普通可变消息行为一致（脚本无感知）。
func TestFrozenProtoAPI_ReadAccessors(t *testing.T) {
	L := newFrozenProtoEnv(t)

	if err := L.DoString(`
		local proto = require("proto")
		uid = proto.get_field(fmsg, "uid")
		mainId = proto.get_path(fmsg, "main.id")
		mainName = proto.get_path(fmsg, "main.name") -- proto3 默认值 ""
		itemCount = proto.list_size(fmsg, "items")
		local item2 = proto.list_get(fmsg, "items", 2)
		item2Name = proto.get_field(item2, "name")
		iterNames = ""
		-- iter_list 元素统一为 userdata（2026-07-30 起，≡ list_get）：继续用 proto 读访问器
		for _, it in proto.iter_list(fmsg, "items") do
			iterNames = iterNames .. tostring(proto.get_field(it, "name")) .. ";"
		end
		local fieldMap = proto.get_field_map(fmsg)
		mapUid = fieldMap.uid
		serLen = #proto.serialize(fmsg)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertGlobal := func(name string, want lua.LValue) {
		t.Helper()
		if got := L.GetGlobal(name); got != want {
			t.Fatalf("%s=%v (%T) want %v", name, got, got, want)
		}
	}
	assertGlobal("uid", lua.LNumber(42))
	assertGlobal("mainId", lua.LNumber(7))
	assertGlobal("mainName", lua.LString(""))
	assertGlobal("itemCount", lua.LNumber(2))
	assertGlobal("item2Name", lua.LString("y"))
	assertGlobal("iterNames", lua.LString(";y;"))
	assertGlobal("mapUid", lua.LNumber(42))
	if n := L.GetGlobal("serLen"); lua.LVAsNumber(n) <= 0 {
		t.Fatalf("serialize 应返回非空字节，serLen=%v", n)
	}
}

// TestFrozenProtoAPI_SetFieldRejected 校验共享只读消息的写防护（fail-loud）：
// set_field 以 Frozen 为目标或为 value 均 RaiseError，绝不静默改写共享解码结果。
func TestFrozenProtoAPI_SetFieldRejected(t *testing.T) {
	L := newFrozenProtoEnv(t)

	if err := L.DoString(`
		local proto = require("proto")
		okTarget, errTarget = pcall(function() proto.set_field(fmsg, "uid", 99) end)
		local writable = proto.create("frozentest.Bag")
		local sub = proto.list_get(fmsg, "items", 1) -- 冻结子消息
		okValue, errValue = pcall(function() proto.set_field(writable, "main", sub) end)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	if lua.LVAsBool(L.GetGlobal("okTarget")) {
		t.Fatal("set_field 以共享只读消息为目标应报错")
	}
	if msg := lua.LVAsString(L.GetGlobal("errTarget")); !strings.Contains(msg, "共享只读") {
		t.Fatalf("错误信息应说明共享只读，实际 %q", msg)
	}
	if lua.LVAsBool(L.GetGlobal("okValue")) {
		t.Fatal("set_field 以冻结子消息为 value 应报错")
	}
}

// TestFrozenProtoAPI_ChildReadonlyPropagation 校验只读性全树传播：
// list_get 从共享消息取出的子消息（userdata）同样只读，set_field 拒绝。
// （iter_list 元素为展开 table 值拷贝，与共享消息无别名，无需防护。）
func TestFrozenProtoAPI_ChildReadonlyPropagation(t *testing.T) {
	L := newFrozenProtoEnv(t)

	if err := L.DoString(`
		local proto = require("proto")
		local child = proto.list_get(fmsg, "items", 1)
		childId = proto.get_field(child, "id") -- 读正常
		okChild, errChild = pcall(function() proto.set_field(child, "id", 100) end)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	if got := L.GetGlobal("childId"); got != lua.LNumber(1) {
		t.Fatalf("childId=%v want 1", got)
	}
	if lua.LVAsBool(L.GetGlobal("okChild")) {
		t.Fatal("list_get 子消息 set_field 应报错（只读传播）")
	}
	if msg := lua.LVAsString(L.GetGlobal("errChild")); !strings.Contains(msg, "共享只读") {
		t.Fatalf("子消息写错误信息应说明共享只读，实际 %q", msg)
	}
}

// TestFrozenProtoAPI_RobotSetStoresFrozen 校验 robot.set 存共享只读消息时，
// state 落的是 *Frozen 引用本身（P1a 整存形态：免展开、跨机器人共享零拷贝），
// 且 robot.get 读回为真 table（边界语义与整存 Frozen 一致）。
func TestFrozenProtoAPI_RobotSetStoresFrozen(t *testing.T) {
	L := newFrozenProtoEnv(t)

	if err := L.DoString(`
		local robot = require("robot")
		robot.set("sharedMsg", fmsg)
		roundTripUid = robot.get("sharedMsg").uid
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	stored := GetContext(L).Store.Get("sharedMsg")
	if _, ok := stored.(*protox.Frozen); !ok {
		t.Fatalf("state 应存 *protox.Frozen 引用，实际 %T", stored)
	}
	if got := L.GetGlobal("roundTripUid"); got != lua.LNumber(42) {
		t.Fatalf("roundTripUid=%v want 42", got)
	}
}
