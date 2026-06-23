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

	for i := 0; i < 5; i++ {
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

func TestRunActionScript_ErrTableBecomesActionError(t *testing.T) {
	rp := newPoolWithScript(t, "fail.lua", `
		local robot = require("robot")
		function execute(r)
			return robot.error(54, "battleId 缺失")
		end
	`)
	L := rp.Acquire()
	defer L.Close()

	_, _, _, err := rp.RunActionScript(L, "fail.lua")
	if err == nil {
		t.Fatal("期望 err table 转为 ActionError")
	}
	var ae *engine.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err 类型 = %T，期望 *engine.ActionError: %v", err, err)
	}
	if ae.Code != errcode.ErrorCode(54) {
		t.Fatalf("code = %d，期望 54", ae.Code)
	}
	if !strings.Contains(ae.Detail, "battleId 缺失") || !strings.Contains(ae.Detail, "script=fail.lua") {
		t.Fatalf("detail = %q，期望包含 battleId 缺失 和 script=fail.lua", ae.Detail)
	}
}

func TestRunActionScript_NilIsSuccess(t *testing.T) {
	rp := newPoolWithScript(t, "ok.lua", `
		function execute(r)
			return nil
		end
	`)
	L := rp.Acquire()
	defer L.Close()

	_, _, _, err := rp.RunActionScript(L, "ok.lua")
	if err != nil {
		t.Fatalf("期望 nil 返回成功，实际 err=%v", err)
	}
}

func TestRunActionScript_LegacyReturnCodeFailsLoud(t *testing.T) {
	rp := newPoolWithScript(t, "legacy.lua", `
		function execute(r)
			return 0
		end
	`)
	L := rp.Acquire()
	defer L.Close()

	_, _, _, err := rp.RunActionScript(L, "legacy.lua")
	if err == nil {
		t.Fatal("期望旧式 return code 报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "旧式 return code") && !strings.Contains(msg, "返回非法值") {
		t.Fatalf("错误文案 = %q，期望包含旧式 return code 或 返回非法值", msg)
	}
}

func TestRunActionScript_MalformedErrTableFailsLoud(t *testing.T) {
	rp := newPoolWithScript(t, "malformed.lua", `
		function execute(r)
			return {detail="x"}
		end
	`)
	L := rp.Acquire()
	defer L.Close()

	_, _, _, err := rp.RunActionScript(L, "malformed.lua")
	if err == nil {
		t.Fatal("期望 malformed err table 报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "返回非法值") && !strings.Contains(msg, "err table") {
		t.Fatalf("错误文案 = %q，期望包含 返回非法值 或 err table", msg)
	}
}
