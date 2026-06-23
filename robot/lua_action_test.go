package robot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/script"

	lua "github.com/yuin/gopher-lua"
)

func newLuaActionTestPool(t *testing.T, name, body string) (*script.RuntimePool, *lua.LState) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := script.NewRuntimePool(dir)
	if err := rp.PrecompileScripts([]string{dir}); err != nil {
		t.Fatal(err)
	}
	L := rp.Acquire()
	return rp, L
}

func TestExecuteLuaActionWrapsNonActionError(t *testing.T) {
	rp, L := newLuaActionTestPool(t, "legacy.lua", `
		function execute(r)
			return 0
		end
	`)
	defer L.Close()

	h := &robotActionHandler{robot: &Robot{luaPool: rp, l: L}}
	_, _, _, _, err := h.executeLuaAction(&engine.ActionDef{
		Name:   "enterBattle",
		Script: "legacy.lua",
	})
	if err == nil {
		t.Fatal("期望旧式 return code 普通 error 被包装为 ActionError")
	}

	var ae *engine.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err 类型 = %T，期望 *engine.ActionError: %v", err, err)
	}
	if ae.Code != errcode.ErrLuaExecFailed {
		t.Fatalf("code = %v，期望 %v", ae.Code, errcode.ErrLuaExecFailed)
	}
	if !strings.Contains(ae.Detail, "script=legacy.lua") {
		t.Fatalf("detail = %q，期望包含 script=legacy.lua", ae.Detail)
	}
	if !strings.Contains(ae.Detail, "action=enterBattle") {
		t.Fatalf("detail = %q，期望包含 action=enterBattle", ae.Detail)
	}
}

// TestExecuteLuaActionErrTableAugmentsActionDetail 验证 lua 返回 err table 时，
// executeLuaAction 透传 ActionError 并在 detail 补 action= 前缀，同时保留 script= 与原 detail 文案。
func TestExecuteLuaActionErrTableAugmentsActionDetail(t *testing.T) {
	rp, L := newLuaActionTestPool(t, "fail.lua", `
		local robot = require("robot")
		function execute(r)
			return robot.error(54, "battleId 缺失")
		end
	`)
	defer L.Close()

	h := &robotActionHandler{robot: &Robot{luaPool: rp, l: L}}
	_, _, _, _, err := h.executeLuaAction(&engine.ActionDef{
		Name:   "enterBattle",
		Script: "fail.lua",
	})
	if err == nil {
		t.Fatal("期望 err table 转为 ActionError")
	}

	var ae *engine.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err 类型 = %T，期望 *engine.ActionError: %v", err, err)
	}
	// code 54 是框架码（<100），由 buildActionError 透传为 errcode.ErrorCode(54)。
	if ae.Code != errcode.ErrorCode(54) {
		t.Fatalf("code = %v，期望 54", ae.Code)
	}
	// detail 应同时包含 action= 前缀、script=、原 battleId 缺失 文案。
	for _, want := range []string{"action=enterBattle", "script=fail.lua", "battleId 缺失"} {
		if !strings.Contains(ae.Detail, want) {
			t.Fatalf("detail = %q，期望包含 %q", ae.Detail, want)
		}
	}
}
