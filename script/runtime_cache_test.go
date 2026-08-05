package script

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stressbot/engine"
	"stressbot/errcode"
	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
)

// TestMain 初始化全局日志（PrecompileScripts 等会写日志，未初始化时 logger 为 nil）。
func TestMain(m *testing.M) {
	stresslog.InitLog(filepath.Join(os.TempDir(), "stressbot_script_test.log"), "test",
		&stresslog.Config{PrintConsole: false, LogLevel: "error"}, "")
	os.Exit(m.Run())
}

// newPoolWithScript 在临时目录写入一个脚本并预编译，返回 pool。
func newPoolWithScript(t *testing.T, name, body string) *RuntimePool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := NewRuntimePool(dir)
	if err := rp.PrecompileScripts([]string{dir}); err != nil {
		t.Fatal(err)
	}
	return rp
}

// TestChunkCachedAcrossCalls chunk 顶层只应执行一次，多次动作调用复用缓存的 execute。
func TestChunkCachedAcrossCalls(t *testing.T) {
	rp := newPoolWithScript(t, "a.lua", `
		_G.__load_count = (_G.__load_count or 0) + 1   -- 顶层副作用：每次跑 chunk +1
		function execute(r)
			return nil
		end
	`)
	L := rp.Acquire()
	defer L.Close()

	for i := range 5 {
		if _, _, _, err := rp.RunActionScript(L, "a.lua"); err != nil {
			t.Fatalf("run %d: err=%v", i, err)
		}
	}

	// 顶层只应执行一次（chunk 缓存生效），__load_count 是顶层全局应被 baseline 保护
	if got := L.GetGlobal("__load_count"); got != lua.LNumber(1) {
		t.Errorf("__load_count = %v, 期望 1（chunk 只跑一次）", got)
	}
	// execute 入口函数已移出全局表
	if got := L.GetGlobal("execute"); got != lua.LNil {
		t.Errorf("execute 仍在全局表: %v", got)
	}
}

// TestRuntimeGlobalsResetOnRelease execute 内写入的运行时全局应在 Release 时清理，
// 但脚本顶层定义的全局应保留。
func TestRuntimeGlobalsResetOnRelease(t *testing.T) {
	rp := newPoolWithScript(t, "b.lua", `
		TOP_CONST = 42                  -- 顶层全局：受保护
		function execute(r)
			runtime_leak = 7            -- 运行时全局：应被清理
			return nil
		end
	`)
	L := rp.Acquire()
	defer L.Close()

	if _, _, _, err := rp.RunActionScript(L, "b.lua"); err != nil {
		t.Fatal(err)
	}
	if L.GetGlobal("runtime_leak") != lua.LNumber(7) {
		t.Fatalf("运行时全局未写入")
	}

	rp.Release(L)

	if got := L.GetGlobal("runtime_leak"); got != lua.LNil {
		t.Errorf("运行时全局 runtime_leak 未被清理: %v", got)
	}
	if got := L.GetGlobal("TOP_CONST"); got != lua.LNumber(42) {
		t.Errorf("顶层全局 TOP_CONST 被误删: %v", got)
	}
}

// TestMissingExecuteFn 未定义 execute 应报错。
func TestMissingExecuteFn(t *testing.T) {
	rp := newPoolWithScript(t, "c.lua", `local x = 1`)
	L := rp.Acquire()
	defer L.Close()
	if _, _, _, err := rp.RunActionScript(L, "c.lua"); err == nil {
		t.Errorf("期望未定义 execute 报错")
	}
}

// TestRunActionScript_ReturnNil 成功路径：脚本 return nil → 无 error。
func TestRunActionScript_ReturnNil(t *testing.T) {
	rp := newPoolWithScript(t, "ok.lua", `function execute(r) return nil end`)
	L := rp.Acquire()
	defer L.Close()
	_, _, _, err := rp.RunActionScript(L, "ok.lua")
	if err != nil {
		t.Fatalf("return nil 应为成功，实际 err=%v", err)
	}
}

// TestRunActionScript_ReturnErrTable 脚本 return robot.error(code, detail) →
// 重建 *engine.ActionError，code/detail 透传，detail 补 script=。
func TestRunActionScript_ReturnErrTable(t *testing.T) {
	rp := newPoolWithScript(t, "err.lua", `
		function execute(r)
			return r.error(54, "bad-field")
		end
	`)
	L := rp.Acquire()
	defer L.Close()
	_, _, _, err := rp.RunActionScript(L, "err.lua")
	if err == nil {
		t.Fatalf("return err table 应产生 error")
	}
	ae, ok := errors.AsType[*engine.ActionError](err)
	if !ok {
		t.Fatalf("error 应为 *engine.ActionError，实际 %T: %v", err, err)
	}
	if ae.Code != errcode.ErrLuaScriptCheck {
		t.Fatalf("code=%d，want ErrLuaScriptCheck(%d)", ae.Code, errcode.ErrLuaScriptCheck)
	}
	if !strings.Contains(ae.Detail, "bad-field") {
		t.Fatalf("detail %q 应含原始 detail \"bad-field\"", ae.Detail)
	}
	if !strings.Contains(ae.Detail, "script=") {
		t.Fatalf("detail %q 应补 script= 上下文", ae.Detail)
	}
}

// TestRunActionScript_ReturnNumber_FailLoud 旧式 return code 必须 fail loud。
func TestRunActionScript_ReturnNumber_FailLoud(t *testing.T) {
	rp := newPoolWithScript(t, "num.lua", `function execute(r) return 0 end`)
	L := rp.Acquire()
	defer L.Close()
	_, _, _, err := rp.RunActionScript(L, "num.lua")
	if err == nil {
		t.Fatalf("return number 应 fail loud，实际 nil")
	}
	// 应为框架错误（ErrLuaExecFailed 包装），非 ActionError 透传的脚本业务错误。
	if ae, ok := errors.AsType[*engine.ActionError](err); ok {
		t.Fatalf("旧式 return number 应包装为框架错误，不应透传 *ActionError（code=%d）", ae.Code)
	}
}
