package script

import (
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

// TestTrampThreadReuse 同一 Robot 连续执行多个脚本应复用同一条蹦床 thread（稳态零新建）。
func TestTrampThreadReuse(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"a.lua": `function execute(r) return nil end`,
		"b.lua": `function execute(r) return nil end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	ctx := &Context{}
	SetContext(L, ctx)

	if _, _, _, err := rp.RunActionScript(L, "a.lua"); err != nil {
		t.Fatalf("a.lua: %v", err)
	}
	if len(ctx.trampThreads) != 1 {
		t.Fatalf("首个任务后应缓存 1 条 thread，实际 %d", len(ctx.trampThreads))
	}
	first := ctx.trampThreads[0].thread

	for i := 0; i < 10; i++ {
		if _, _, _, err := rp.RunActionScript(L, "b.lua"); err != nil {
			t.Fatalf("b.lua 第 %d 次: %v", i, err)
		}
	}
	if len(ctx.trampThreads) != 1 {
		t.Fatalf("复用后缓存应仍为 1 条，实际 %d", len(ctx.trampThreads))
	}
	if ctx.trampThreads[0].thread != first {
		t.Fatal("后续任务未复用同一条蹦床 thread")
	}
	if got := ctx.trampThreads[0].tasks; got != 11 {
		t.Fatalf("tasks=%d want 11", got)
	}
}

// TestTrampReturnValues 复用 thread 上任务返回值经 DONE-yield 正确带出：
// 布尔脚本 true/false、err-table 契约均不受蹦床影响。
func TestTrampReturnValues(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"yes.lua": `function execute(r) return true end`,
		"no.lua":  `function execute(r) return false end`,
		"err.lua": `function execute(r)
  local robot = require("robot")
  return robot.error(54, "业务失败")
end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{})

	got, err := rp.RunBooleanScript(L, "yes.lua")
	if err != nil || got != true {
		t.Fatalf("yes.lua=(%v,%v) want (true,nil)", got, err)
	}
	got, err = rp.RunBooleanScript(L, "no.lua")
	if err != nil || got != false {
		t.Fatalf("no.lua=(%v,%v) want (false,nil)", got, err)
	}
	// err-table 契约：脚本业务失败重建为 ActionError 返回。
	if _, _, _, err := rp.RunActionScript(L, "err.lua"); err == nil ||
		!strings.Contains(err.Error(), "业务失败") {
		t.Fatalf("err.lua 应返回含 detail 的业务错误，实际 %v", err)
	}
}

// TestTrampAwaitThenReuse 含 await 的脚本在复用 thread 上多轮挂起/恢复后干净收尾并归还。
func TestTrampAwaitThenReuse(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"sleep.lua": `function execute(r)
  local utils = require("utils")
  utils.sleep(5)
  utils.sleep(5)
  return nil
end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	w := &recordingWaiter{}
	ctx := &Context{Waiter: w}
	SetContext(L, ctx)

	for i := 0; i < 3; i++ {
		if _, _, _, err := rp.RunActionScript(L, "sleep.lua"); err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
	}
	if len(w.specs) != 6 {
		t.Fatalf("期望 6 次 Await，实际 %d", len(w.specs))
	}
	if len(ctx.trampThreads) != 1 {
		t.Fatalf("应复用同一条 thread（缓存 1 条），实际 %d", len(ctx.trampThreads))
	}
}

// TestTrampErrorDiscardsThread 脚本 error 后 thread 必须弃用（不回缓存），
// 后续任务惰性重建且不受污染。
func TestTrampErrorDiscardsThread(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"ok.lua":   `function execute(r) return nil end`,
		"boom.lua": `function execute(r) error("boom") end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	ctx := &Context{}
	SetContext(L, ctx)

	if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
		t.Fatalf("ok.lua: %v", err)
	}
	first := ctx.trampThreads[0].thread

	if _, _, _, err := rp.RunActionScript(L, "boom.lua"); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("boom.lua 应报错，实际 %v", err)
	}
	if len(ctx.trampThreads) != 0 {
		t.Fatalf("错误收尾的 thread 应弃用，缓存应为空，实际 %d", len(ctx.trampThreads))
	}

	if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
		t.Fatalf("错误后再执行 ok.lua: %v", err)
	}
	if len(ctx.trampThreads) != 1 || ctx.trampThreads[0].thread == first {
		t.Fatal("错误后应新建 thread 并归还缓存")
	}
}

// TestTrampRogueYieldDiscards 脚本裸 coroutine.yield 必须 fail-loud 且弃用 thread
//（栈停在脚本内部，不可复用）。
func TestTrampRogueYieldDiscards(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"rogue.lua": `function execute(r) coroutine.yield(1) return nil end`,
		"ok.lua":    `function execute(r) return nil end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	ctx := &Context{}
	SetContext(L, ctx)

	if _, _, _, err := rp.RunActionScript(L, "rogue.lua"); err == nil ||
		!strings.Contains(err.Error(), "非法 yield") {
		t.Fatalf("裸 yield 应 fail-loud，实际 %v", err)
	}
	if len(ctx.trampThreads) != 0 {
		t.Fatalf("裸 yield 的 thread 应弃用，缓存应为空，实际 %d", len(ctx.trampThreads))
	}
	if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
		t.Fatalf("裸 yield 后再执行 ok.lua: %v", err)
	}
}

// TestTrampNestedListenDuringAwait 主脚本 await 挂起期间（模拟等待窗口 drain）执行
// listen 回调：主 thread 不在空闲缓存（挂起中），回调取第二条 thread，两者各自干净
// 收尾归还——对应真实调度器的协作式嵌套。
func TestTrampNestedListenDuringAwait(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"main.lua": `function execute(r)
  local utils = require("utils")
  utils.sleep(5)
  return nil
end`,
		"cb.lua": `function on_message(r, msg) return nil end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)

	var ctx *Context
	w := &recordingWaiter{}
	w.onAwait = func(spec *WaitSpec) {
		if err := rp.RunListenScriptRaw(L, "cb.lua", []byte("x")); err != nil {
			t.Errorf("嵌套 listen 回调失败: %v", err)
		}
		if n := len(ctx.trampThreads); n != 1 {
			t.Errorf("回调收尾后空闲缓存应为 1 条（主 thread 挂起中），实际 %d", n)
		}
	}
	ctx = &Context{Waiter: w}
	SetContext(L, ctx)

	if _, _, _, err := rp.RunActionScript(L, "main.lua"); err != nil {
		t.Fatalf("main.lua: %v", err)
	}
	if len(ctx.trampThreads) != 2 {
		t.Fatalf("主流程+回调各归还一条，缓存应为 2 条，实际 %d", len(ctx.trampThreads))
	}
}

// TestTrampThreadRetirement thread 执行满 maxTasksPerTrampThread 次后退休重建，
// 兜住 gopher-lua registry 只增不缩的常驻上界。
func TestTrampThreadRetirement(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"ok.lua": `function execute(r) return nil end`,
	})
	L := rp.Acquire()
	defer rp.Release(L)
	ctx := &Context{}
	SetContext(L, ctx)

	for i := 0; i < maxTasksPerTrampThread; i++ {
		if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
	}
	if len(ctx.trampThreads) != 0 {
		t.Fatalf("满 %d 次任务后 thread 应退休，缓存应为空，实际 %d",
			maxTasksPerTrampThread, len(ctx.trampThreads))
	}
	if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
		t.Fatalf("退休后再执行: %v", err)
	}
	if len(ctx.trampThreads) != 1 || ctx.trampThreads[0].tasks != 1 {
		t.Fatal("退休后应新建 thread 并从 tasks=1 重新计数")
	}
}

// TestTrampReleaseClosesCache Robot 归还 LState 时缓存必须清空（thread 持有
// 当前 Robot 的派生 ctx，不可带给下一个 Robot）。
func TestTrampReleaseClosesCache(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"ok.lua": `function execute(r) return nil end`,
	})
	L := rp.Acquire()
	ctx := &Context{}
	SetContext(L, ctx)
	if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
		t.Fatalf("ok.lua: %v", err)
	}
	if len(ctx.trampThreads) != 1 {
		t.Fatalf("缓存应为 1 条，实际 %d", len(ctx.trampThreads))
	}
	rp.Release(L)
	if len(ctx.trampThreads) != 0 {
		t.Fatalf("Release 后缓存应清空，实际 %d", len(ctx.trampThreads))
	}
}

// BenchmarkNewThreadPerTask 对照基准：复刻蹦床化前的旧路径（每任务 L.NewThread +
// 直接 Resume 入口函数），量化 P2 消除的每任务分配。
func BenchmarkNewThreadPerTask(b *testing.B) {
	rp := NewRuntimePool("")
	compiler := lua.NewState()
	fn, err := compiler.LoadString(`function execute(r) return nil end`)
	compiler.Close()
	if err != nil {
		b.Fatalf("编译失败: %v", err)
	}
	rp.precompiled["bench.lua"] = fn.Proto

	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{})
	fnv, err := rp.loadScriptFn(L, "bench.lua", "execute")
	if err != nil {
		b.Fatalf("加载入口失败: %v", err)
	}
	entry := fnv.(*lua.LFunction)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		thread, cancel := L.NewThread()
		st, rerr, _ := L.Resume(thread, entry, lua.LNil)
		if st != lua.ResumeOK || rerr != nil {
			b.Fatalf("resume 失败: st=%v err=%v", st, rerr)
		}
		if cancel != nil {
			cancel()
		}
	}
}

// BenchmarkRunActionScript 稳态脚本执行的分配基准：蹦床复用后不应再出现
// NewThread 级别（registry ~16KB/次）的分配。
func BenchmarkRunActionScript(b *testing.B) {
	rp := NewRuntimePool("")
	compiler := lua.NewState()
	fn, err := compiler.LoadString(`function execute(r) return nil end`)
	compiler.Close()
	if err != nil {
		b.Fatalf("编译失败: %v", err)
	}
	rp.precompiled["bench.lua"] = fn.Proto

	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := rp.RunActionScript(L, "bench.lua"); err != nil {
			b.Fatalf("执行失败: %v", err)
		}
	}
}
