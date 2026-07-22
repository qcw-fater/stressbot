package script

import (
	"strings"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// recordingWaiter 记录收到的 WaitSpec，并返回预置 outcome（默认空：sleep 到时 / listen 无命中）。
type recordingWaiter struct {
	specs   []*WaitSpec
	outcome WaitOutcome
	// onAwait 可选钩子：在返回前执行（用于模拟「等待窗口内 drain 任务」的副作用）。
	onAwait func(spec *WaitSpec)
}

func (w *recordingWaiter) Await(spec *WaitSpec) (WaitOutcome, error) {
	w.specs = append(w.specs, spec)
	if w.onAwait != nil {
		w.onAwait(spec)
	}
	// WaitIO：模拟真实调度器——同步跑作业（测试无需后台协程）并返回其 renderer。
	if spec.Kind == WaitIO && spec.IOJob != nil {
		return WaitOutcome{IORender: spec.IOJob()}, nil
	}
	return w.outcome, nil
}

// newTestPool 用内存中的 Lua 源码构造 RuntimePool（绕过磁盘 .lua 文件）。
func newTestPool(t *testing.T, scripts map[string]string) *RuntimePool {
	t.Helper()
	rp := NewRuntimePool("")
	compiler := lua.NewState()
	defer compiler.Close()
	for name, src := range scripts {
		fn, err := compiler.LoadString(src)
		if err != nil {
			t.Fatalf("编译脚本 %s 失败: %v", name, err)
		}
		rp.precompiled[name] = fn.Proto
	}
	return rp
}

func runScript(t *testing.T, rp *RuntimePool, ctx *Context, name string) error {
	t.Helper()
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, ctx)
	_, _, _, err := rp.RunActionScript(L, name)
	return err
}

// TestRunActionScript_NoYield 不含 await 的脚本应在协程首次 resume 即跑完并成功返回 nil
// （协程化后与旧 CallByParam 行为等价的回归）。err-table 契约下成功 = return nil。
func TestRunActionScript_NoYield(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"ret.lua": `function execute(r) return nil end`,
	})
	if err := runScript(t, rp, &Context{}, "ret.lua"); err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
}

// TestAwaitSleep_YieldsAndResumes 验证 await_sleep yield 出 WaitSleep、被 Waiter 接管、
// 再 resume 回脚本并跑到 return nil（成功）。
func TestAwaitSleep_YieldsAndResumes(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"sleep.lua": `function execute(r)
  local utils = require("utils")
  utils.sleep(25)
  return nil
end`,
	})
	w := &recordingWaiter{}
	if err := runScript(t, rp, &Context{Waiter: w}, "sleep.lua"); err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	if len(w.specs) != 1 {
		t.Fatalf("期望 1 次 Await，实际 %d", len(w.specs))
	}
	if w.specs[0].Kind != WaitSleep {
		t.Fatalf("Kind=%d，want WaitSleep(%d)", w.specs[0].Kind, WaitSleep)
	}
	if w.specs[0].Duration != 25*time.Millisecond {
		t.Fatalf("Duration=%v，want 25ms", w.specs[0].Duration)
	}
}

// TestAwaitSleep_MultipleInLoop 循环内多次 await 应逐次 yield/resume，最终正常结束（return nil）。
func TestAwaitSleep_MultipleInLoop(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"loop.lua": `function execute(r)
  local utils = require("utils")
  for i = 1, 3 do
    utils.sleep(1)
  end
  return nil
end`,
	})
	w := &recordingWaiter{}
	if err := runScript(t, rp, &Context{Waiter: w}, "loop.lua"); err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	if len(w.specs) != 3 {
		t.Fatalf("期望 3 次 Await，实际 %d", len(w.specs))
	}
}

// TestAwaitInsidePcall_FailLoud await 被 pcall 吞掉时（spike 实测的静默陷阱）必须 fail-loud，
// 不允许静默错乱。
func TestAwaitInsidePcall_FailLoud(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"pcall.lua": `function execute(r)
  local utils = require("utils")
  pcall(function() utils.sleep(5) end)
  return nil
end`,
	})
	w := &recordingWaiter{}
	err := runScript(t, rp, &Context{Waiter: w}, "pcall.lua")
	if err == nil {
		t.Fatalf("期望 fail-loud error，实际 nil")
	}
	if !strings.Contains(err.Error(), "pcall") {
		t.Fatalf("error %q 应提示 pcall/coroutine 吞掉 await", err.Error())
	}
}

// TestAwaitInsideUserCoroutine_FailLoud await 在 coroutine.create 协程内调用时，
// 顶层协程身份校验应 fail-loud。
func TestAwaitInsideUserCoroutine_FailLoud(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"userco.lua": `function execute(r)
  local utils = require("utils")
  local co = coroutine.create(function() utils.sleep(5) end)
  local ok, err = coroutine.resume(co)
  if not ok then error(err) end
  return nil
end`,
	})
	w := &recordingWaiter{}
	err := runScript(t, rp, &Context{Waiter: w}, "userco.lua")
	if err == nil {
		t.Fatalf("期望 fail-loud error，实际 nil")
	}
	if len(w.specs) != 0 {
		t.Fatalf("用户协程内的 await 不应到达 Waiter，实际收到 %d 次", len(w.specs))
	}
}

// TestRunListenScript_NoAwait 不含 await 的 on_message 回调应正常跑完（协程化回归）。
func TestRunListenScript_NoAwait(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"cb.lua": `function on_message(r, msg) return end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{})
	if err := rp.RunListenScript(L, "cb.lua", nil); err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
}

// TestRunListenScriptRaw_PassesPayload 验证未配置 s2cProto 的持久监听会把原始消息体
// 作为二进制安全的 Lua string 传给 on_message，而不是丢弃为 nil。
func TestRunListenScriptRaw_PassesPayload(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"raw.lua": `function on_message(r, msg) received = msg end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{})

	want := []byte{0x00, 0x7f, 0x80, 0xff}
	if err := rp.RunListenScriptRaw(L, "raw.lua", want); err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	got, ok := L.GetGlobal("received").(lua.LString)
	if !ok {
		t.Fatalf("received 类型 = %T，期望 lua.LString", L.GetGlobal("received"))
	}
	if string(got) != string(want) {
		t.Fatalf("received = %v，期望 %v", []byte(got), want)
	}
}

// TestRunListenScript_AwaitInCallback listen 回调改协程后，on_message 内可直接调用会等待的 API：
// 应 yield 一次 WaitSleep 交给 Waiter，再 resume 跑完。
func TestRunListenScript_AwaitInCallback(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"cb.lua": `function on_message(r, msg)
  local utils = require("utils")
  utils.sleep(5)
end`,
	})
	w := &recordingWaiter{}
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{Waiter: w})
	if err := rp.RunListenScript(L, "cb.lua", nil); err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	if len(w.specs) != 1 || w.specs[0].Kind != WaitSleep {
		t.Fatalf("回调内 sleep 应 yield 一次 WaitSleep，实际 %+v", w.specs)
	}
}

// TestAwaitWithoutWaiter_FailLoud 未接入协作式调度（Waiter nil）时调 await 应 fail-loud。
func TestAwaitWithoutWaiter_FailLoud(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"nowaiter.lua": `function execute(r)
  local utils = require("utils")
  utils.sleep(5)
  return nil
end`,
	})
	err := runScript(t, rp, &Context{}, "nowaiter.lua")
	if err == nil {
		t.Fatalf("期望 fail-loud error，实际 nil")
	}
}
