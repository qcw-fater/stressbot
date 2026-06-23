package script

import (
	"context"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func TestRobotError(t *testing.T) {
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	defer L.Close()
	if err := L.DoString(`
		local robot = require("robot")
		err = robot.error(54, "battleId 缺失")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	tb, ok := L.GetGlobal("err").(*lua.LTable)
	if !ok {
		t.Fatalf("err 不是 table: %T", L.GetGlobal("err"))
	}
	if got := tb.RawGetString("code"); got != lua.LNumber(54) {
		t.Fatalf("code=%v want 54", got)
	}
	if got := lua.LVAsString(tb.RawGetString("detail")); got != "battleId 缺失" {
		t.Fatalf("detail=%q", got)
	}
	L.SetGlobal("r", createRobotUserData(L))
	if err := L.DoString(`
		err2 = r.error(55, "房间不存在")
		err3 = r:error(56, "房间已满")
	`); err != nil {
		t.Fatalf("lua userdata error: %v", err)
	}
	tb2, ok := L.GetGlobal("err2").(*lua.LTable)
	if !ok {
		t.Fatalf("err2 不是 table: %T", L.GetGlobal("err2"))
	}
	if got := tb2.RawGetString("code"); got != lua.LNumber(55) {
		t.Fatalf("userdata dot code=%v want 55", got)
	}
	if got := lua.LVAsString(tb2.RawGetString("detail")); got != "房间不存在" {
		t.Fatalf("userdata dot detail=%q", got)
	}
	tb3, ok := L.GetGlobal("err3").(*lua.LTable)
	if !ok {
		t.Fatalf("err3 不是 table: %T", L.GetGlobal("err3"))
	}
	if got := tb3.RawGetString("code"); got != lua.LNumber(56) {
		t.Fatalf("userdata colon code=%v want 56", got)
	}
	if got := lua.LVAsString(tb3.RawGetString("detail")); got != "房间已满" {
		t.Fatalf("userdata colon detail=%q", got)
	}
}
