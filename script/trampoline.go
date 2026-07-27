package script

import (
	"context"
	"fmt"

	lua "github.com/yuin/gopher-lua"
)

// ── P2：长驻 trampoline 协程复用 ────────────────────────────────
//
// 旧实现每次脚本执行都 L.NewThread() 新建子线程协程，脚本 return 后协程死亡：
// 数千机器人 × 每秒数千次执行时，newLState/newRegistry/newGlobal 占进程总分配的
// 50%+（线上 profile 实测 54%），GC 追不上分配速率，余量把 RSS 顶到 live 的 2 倍。
//
// 现在协程主函数换成永不返回的蹦床循环：执行完一个任务就 yield(哨兵, 结果...)
// 停在栈顶等待下一个任务，thread 长驻复用，稳态创建速率 ≈ 0。
//
// 归还规则（一句话）：只有以 DONE-yield 干净收尾的 thread 才回缓存；脚本错误
// （thread 已死）、裸 yield / await 中途放弃（栈停在半途）一律弃用，下次惰性重建。
//
// 嵌套场景天然成立：主流程脚本 await 挂起时其 thread 不在空闲缓存里，等待窗口
// drain 出的 listen 回调会取到另一条（或新建），互不干扰；topThread 校验逻辑不变。

const (
	trampFnKey       = "__sb_tramp_fn__"   // 蹦床主函数值缓存键（per LState registry）
	trampSentinelKey = "__sb_tramp_done__" // DONE 哨兵 userdata 键（per LState registry）

	// maxIdleTrampThreads 每 Robot 缓存的空闲 thread 上限：主流程 1 条 + 嵌套
	//（主脚本 await 期间 drain 出的 listen 回调）常见 1-2 条，4 覆盖极端嵌套；
	// 超出直接弃用，防御性上界。
	maxIdleTrampThreads = 4

	// maxTasksPerTrampThread 单 thread 执行任务数上限，到期退休重建：
	// gopher-lua thread 的数据栈（registry）只增不缩，深调用脚本会把它撑大并长期
	// 钉住；按次退休把该常驻上界摊销为 1/N 的创建成本。
	maxTasksPerTrampThread = 512
)

// trampolineSource 蹦床 chunk：返回蹦床主函数。进程级编译一次（proto），
// 每个根 LState 实例化一次函数值，每条长驻 thread 以它为协程主函数。
//
// 协议：
//   - bootstrap（首次 Resume）：实参 (sentinel, fn, args...) → 立即执行首个任务；
//   - 任务完成：yield(sentinel, 结果...) 停在栈顶；
//   - handoff（后续 Resume）：喂入 (fn, args...) 作为 yield 返回值 → 执行下一个任务；
//   - 任务内 await_*：脚本经 awaitYield 直接 yield WaitSpec（不带哨兵），蹦床无感知；
//   - 脚本 error：蹦床内无 pcall（yield 不可穿越保护边界），错误直接穿出 →
//     ResumeError，thread 死亡由 Go 侧弃用。
//
// 注：nxt/rets 局部表会把上一任务的入参/结果钉到下一任务边界，属可接受的小残留
//（通常是句柄/标量）；Lua 5.1 无 table.pack，用 select("#") 手工 pack 保 nil 洞。
const trampolineSource = `return function(sentinel, fn, ...)
    local pack = function(...) return { n = select("#", ...), ... } end
    local yield, unpack = coroutine.yield, unpack
    local rets = pack(fn(...))
    while true do
        local nxt = pack(yield(sentinel, unpack(rets, 1, rets.n)))
        rets = pack(nxt[1](unpack(nxt, 2, nxt.n)))
    end
end`

// trampThread 一条长驻蹦床协程。
type trampThread struct {
	thread *lua.LState
	cancel context.CancelFunc // NewThread 派生 ctx 的取消函数（父 L 未绑 ctx 时为 nil）
	booted bool               // 蹦床主函数是否已进入循环（bootstrap Resume 已完成）
	tasks  int                // 已完成任务数（退休阈值用）
}

// stop 取消派生 ctx 并弃用（thread 本体交给 GC；必须 cancel，否则长期运行下
// 弃用的 thread 会以子 ctx 节点形式挂在 Robot ctx 树上不释放）。
func (t *trampThread) stop() {
	if t != nil && t.cancel != nil {
		t.cancel()
	}
}

// acquireTrampThread 取一条可用 thread：优先复用 Context 缓存，否则新建。
// 仅执行器 goroutine（业务 LState 唯一所有者）调用，无锁。
func acquireTrampThread(L *lua.LState, ctx *Context) *trampThread {
	if ctx != nil {
		if n := len(ctx.trampThreads); n > 0 {
			t := ctx.trampThreads[n-1]
			ctx.trampThreads[n-1] = nil
			ctx.trampThreads = ctx.trampThreads[:n-1]
			return t
		}
	}
	thread, cancel := L.NewThread()
	return &trampThread{thread: thread, cancel: cancel}
}

// releaseTrampThread 归还干净收尾（栈停在蹦床顶部）的 thread；
// 超过退休阈值或缓存已满则弃用。ctx 为 nil（测试/无上下文路径）直接弃用。
func releaseTrampThread(ctx *Context, t *trampThread) {
	if ctx == nil {
		t.stop()
		return
	}
	t.tasks++
	if t.tasks >= maxTasksPerTrampThread || len(ctx.trampThreads) >= maxIdleTrampThreads {
		t.stop()
		return
	}
	ctx.trampThreads = append(ctx.trampThreads, t)
}

// closeTrampThreads 关闭并清空缓存。Robot 归还 LState（RuntimePool.Release）时调用：
// 缓存的 thread 持有从当前 Robot ctx 派生的子 ctx，不可带给池内下一个 Robot。
func (c *Context) closeTrampThreads() {
	if c == nil {
		return
	}
	for i, t := range c.trampThreads {
		t.stop()
		c.trampThreads[i] = nil
	}
	c.trampThreads = c.trampThreads[:0]
}

// trampMainFn 取当前 LState 的蹦床主函数（首次实例化后缓存进 registry，
// 同一 L 的所有 thread 共享该函数值——执行状态在 thread 栈上，函数值无状态）。
func (rp *RuntimePool) trampMainFn(L *lua.LState) (*lua.LFunction, error) {
	reg := L.Get(lua.RegistryIndex)
	if fn, ok := L.GetField(reg, trampFnKey).(*lua.LFunction); ok {
		return fn, nil
	}
	savedTop := L.GetTop()
	L.Push(L.NewFunctionFromProto(rp.trampProto))
	if err := L.PCall(0, 1, nil); err != nil {
		L.SetTop(savedTop)
		return nil, fmt.Errorf("实例化蹦床函数失败: %w", err)
	}
	fn, ok := L.Get(-1).(*lua.LFunction)
	L.SetTop(savedTop)
	if !ok {
		return nil, fmt.Errorf("蹦床 chunk 未返回函数")
	}
	L.SetField(reg, trampFnKey, fn)
	return fn, nil
}

// trampSentinel 取当前 LState 的 DONE 哨兵：唯一 userdata，指针相等判定任务完成，
// 脚本无法伪造（userdata 不可在 Lua 侧构造出同一指针）。
func trampSentinel(L *lua.LState) *lua.LUserData {
	reg := L.Get(lua.RegistryIndex)
	if ud, ok := L.GetField(reg, trampSentinelKey).(*lua.LUserData); ok {
		return ud
	}
	ud := L.NewUserData()
	L.SetField(reg, trampSentinelKey, ud)
	return ud
}

// compileTrampoline 编译蹦床 chunk（进程级一次）。源为包内常量，编译失败属编程错误，
// 直接 panic（等价于 init 失败）。
func compileTrampoline() *lua.FunctionProto {
	L := lua.NewState()
	defer L.Close()
	fn, err := L.LoadString(trampolineSource)
	if err != nil {
		panic(fmt.Sprintf("编译蹦床 chunk 失败: %v", err))
	}
	return fn.Proto
}
