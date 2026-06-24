package script

import (
	"context"
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"

	"stressbot/engine"
)

// 协作式 Lua 调度：action 脚本运行在子线程协程上，遇到 await_* 时 yield 出 WaitSpec，
// 控制权交回 drive-loop（RunActionScript）；drive-loop 调 Context.Waiter.Await 让 Robot
// 在等待窗口内 drain 任务队列（跑 listen 回调等），条件满足/超时后再 resume 协程。
//
// 已由 spike 验证（gopher-lua v1.1.2）的关键性质：
//   - 子线程经 NewThread 共享 registry/globals，故 GetContext(thread) 与主 LState 同源；
//   - 主流程协程 park 在 yield 时，可安全 Resume 其他协程 / 在主 LState 上 CallByParam
//     跑 listen 回调，互不破坏栈；
//   - await_* 若被 pcall 吞掉，协程会以 ResumeOK「结束」并把 WaitSpec 当返回值带出——
//     据此 fail-loud（见 resumeCoroutine 的 ResumeOK 分支）。

// WaitKind 标识一次协作式等待的条件类型。
type WaitKind int

const (
	// WaitSleep 纯计时等待（await_sleep），无唤醒条件，到时即 resume。
	WaitSleep WaitKind = iota + 1
	// WaitListen 等待某 routeKey 的推送消息（await_tcp_listen / await_udp_listen），
	// 由 Waiter 轮询监听队列 + drain 任务，命中或超时后 resume。
	WaitListen
	// WaitResponse 等待一次请求的响应（await_tcp_request / await_udp_request）。
	// Waiter 发送 Packet 并注册响应通道，select 通道 + drain 任务，命中/超时/取消后 resume。
	// 用通道 select（非轮询）唤醒，保证 WireRTT 测量不被轮询间隔污染。
	WaitResponse
	// WaitIO 一次 Class B 主动阻塞 I/O（share.* Redis / http_request / connect_*）。
	// scheduler 观测不到其唤醒事件，故由 Waiter 把 WaitSpec.IOJob 投递到后台 goroutine 实际
	// 执行阻塞调用，执行器 goroutine 同步等待期间持续 drain 任务队列；IOJob 完成后返回 IORenderer，
	// 由 drive-loop 在执行器 goroutine 上调用以产出 Lua 返回值。
	WaitIO
)

// IORenderer 把后台 I/O 作业的 Go 结果转成 await_* 的 Lua 返回值。
//
// 关键约束：IORenderer **只在执行器 goroutine（业务 LState 唯一所有者）上被调用**（drive-loop
// 的 buildResumeVals 内），故可安全访问 L 与 Context；与之相对，产出它的 WaitSpec.IOJob 运行在
// 后台 goroutine，**绝不可触碰 L / 业务 state**。两者经 done 通道交接（happens-before，无竞态）。
type IORenderer func(L *lua.LState) []lua.LValue

// WaitSpec 是 action 协程在 await_* 处 yield 出的等待规格。
// 由 Context.Waiter（Robot 侧）解释执行；drive-loop 据 Kind 把 WaitOutcome 转回 Lua 返回值。
type WaitSpec struct {
	Kind     WaitKind
	Duration time.Duration // WaitSleep：休眠时长；WaitListen/WaitResponse：总超时
	PollMs   int           // WaitListen：轮询间隔（毫秒）
	Proto    string        // "tcp" / "udp"
	Service  string
	RouteKey string
	S2CProto string
	Packet   []byte // WaitResponse：已编码的请求包，由 Waiter 发送

	// WaitIO 专用：
	IOName string          // 作业名（日志/错误），如 "share.get" / "network.http_request"
	IOJob  func() IORenderer // 后台 goroutine 执行的阻塞 I/O；返回在执行器 goroutine 上产出 Lua 值的 renderer
}

// WaitOutcome 是 Waiter.Await 的等待结果，交回 drive-loop 转成 Lua 返回值。
type WaitOutcome struct {
	Exchange *engine.NetExchange // WaitListen/WaitResponse 命中的响应（nil = 未命中）
	TimedOut bool                // 超时（WaitListen 用；未命中且 ctx 未取消）
	Canceled bool                // ctx 取消导致提前返回
	Err      error               // WaitResponse 的请求错误（发送失败 / 超时 / 断连），listen 不用
	IORender IORenderer          // WaitIO：后台作业完成后返回的 renderer（nil = 作业被放弃/panic）
}

// Waiter 协作式等待后端，由 Robot 实现。
// Await 必须在执行器 goroutine（业务 LState 唯一所有者）内被 drive-loop 同步调用：
// 它在等待窗口内 drain Robot 任务队列（跑 listen 回调等），故等待期间其他协作式工作得以推进。
type Waiter interface {
	Await(spec *WaitSpec) (WaitOutcome, error)
}

// coroutine 封装一次 Lua 脚本执行的子线程协程（action 脚本 execute 或 listen 回调 on_message）。
type coroutine struct {
	parent   *lua.LState        // 创建该线程的父 LState（= Robot 独占 LState），Resume 的接收者
	thread   *lua.LState        // 子线程，脚本在其上运行
	cancel   context.CancelFunc // NewThread 派生的子 ctx 的取消函数，结束时必须调用以释放
	fn       *lua.LFunction     // 入口函数（execute / on_message）
	initArgs []lua.LValue       // 首次 Resume 喂入的入口实参（如 robot 句柄、msg）
	started  bool               // 是否已首次 Resume
	name     string             // 脚本名（日志/错误）
}

func (co *coroutine) close() {
	if co != nil && co.cancel != nil {
		co.cancel()
	}
}

// startCoroutine 加载脚本指定入口函数并创建子线程协程（尚未 Resume）。
// initArgs 为首次 Resume 喂入入口的实参（execute(r) → [r]；on_message(r,msg) → [r,msg]）。
func (rp *RuntimePool) startCoroutine(L *lua.LState, scriptName, entry string, initArgs ...lua.LValue) (*coroutine, error) {
	fnv, err := rp.loadScriptFn(L, scriptName, entry)
	if err != nil {
		return nil, err
	}
	fn, ok := fnv.(*lua.LFunction)
	if !ok {
		return nil, fmt.Errorf("脚本 %s 的 %s 不是函数", scriptName, entry)
	}
	thread, cancel := L.NewThread()
	return &coroutine{parent: L, thread: thread, cancel: cancel, fn: fn, initArgs: initArgs, name: scriptName}, nil
}

// startActionCoroutine 加载脚本 execute 入口（实参为 robot 句柄）并创建协程。
func (rp *RuntimePool) startActionCoroutine(L *lua.LState, scriptName string) (*coroutine, error) {
	return rp.startCoroutine(L, scriptName, "execute", createRobotUserData(L))
}

// resumeResult 描述一次 Resume 的结果。
type resumeResult struct {
	done    bool         // 协程是否已结束
	ret     lua.LValue   // done 时脚本首个返回值原始 LValue（action 脚本据此区分 nil / err table / 非法值）
	retVals []lua.LValue // done 时脚本的全部返回值（boolean 脚本据此取 LBool）
	wait    *WaitSpec    // 未结束时 yield 出的等待规格
}

// resumeCoroutine 推进一次协程：首次喂入 co.initArgs（如 execute(r) / on_message(r,msg)），
// 后续喂入 await 返回值。
//
// 在 Resume 前把 ctx.topThread 置为本协程线程，供 await_* 运行时校验顶层协程身份。
// 该字段每次 resume 前重设，故协作式嵌套（回调 await 期间 drain 出另一回调）也能正确归位。
func (rp *RuntimePool) resumeCoroutine(co *coroutine, ctx *Context, resumeVals []lua.LValue) (resumeResult, error) {
	if ctx != nil {
		ctx.topThread = co.thread
	}

	var (
		st   lua.ResumeState
		err  error
		vals []lua.LValue
	)
	if !co.started {
		co.started = true
		st, err, vals = co.parent.Resume(co.thread, co.fn, co.initArgs...)
	} else {
		st, err, vals = co.parent.Resume(co.thread, nil, resumeVals...)
	}

	switch st {
	case lua.ResumeError:
		return resumeResult{done: true}, fmt.Errorf("执行脚本 %s 失败: %w", co.name, err)
	case lua.ResumeYield:
		spec := extractWaitSpec(vals)
		if spec == nil {
			return resumeResult{done: true}, fmt.Errorf(
				"脚本 %s 非法 yield：仅允许经 await_* API 让出（不可直接 coroutine.yield）", co.name)
		}
		return resumeResult{wait: spec}, nil
	default: // ResumeOK：协程结束
		// 静默陷阱拦截：await_* 被 pcall 吞掉时，协程以 OK 结束并把 WaitSpec 当返回值带出。
		if spec := extractWaitSpec(vals); spec != nil {
			_ = spec
			return resumeResult{done: true}, fmt.Errorf(
				"脚本 %s 的 await_* 被 pcall/coroutine 吞掉（yield 未到达调度器）；"+
					"await_* 只能在动作脚本顶层直接调用，不可置于 pcall/xpcall 或 coroutine.create 内", co.name)
		}
		var ret lua.LValue = lua.LNil
		if len(vals) > 0 {
			ret = vals[0]
		}
		return resumeResult{done: true, ret: ret, retVals: vals}, nil
	}
}

// extractWaitSpec 从一组 Lua 值里找出 await_* yield 的 *WaitSpec（包裹在 userdata 内）。
func extractWaitSpec(vals []lua.LValue) *WaitSpec {
	for _, v := range vals {
		if ud, ok := v.(*lua.LUserData); ok {
			if spec, ok := ud.Value.(*WaitSpec); ok {
				return spec
			}
		}
	}
	return nil
}

// awaitYield 是 await_* Go 函数的统一让出辅助：
// 校验调用者处于调度器直接 resume 的顶层协程，否则 fail-loud；通过后把 WaitSpec 包成
// userdata yield 出去（返回值由调度器 resume 时喂回，本函数不会在 resume 后继续执行）。
func awaitYield(L *lua.LState, fnName string, spec *WaitSpec) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.topThread != L {
		L.RaiseError("%s 只能在动作脚本顶层（调度器协程）直接调用，"+
			"不可在 pcall/xpcall 或 coroutine.create 创建的协程内调用", fnName)
		return 0
	}
	if ctx.Waiter == nil {
		L.RaiseError("%s 不可用：当前运行时未接入协作式调度（Waiter 为 nil）", fnName)
		return 0
	}
	ud := L.NewUserData()
	ud.Value = spec
	return L.Yield(ud)
}

// awaitIO 是 Class B 阻塞 I/O（share.* / http_request / connect_*）的统一协作式让出辅助。
//
// 调用约定：调用方先在执行器 goroutine 上读完所有 Lua 入参（CheckString / luaToGoValue 等），
// 再传入一个 job 闭包——job 在后台 goroutine 上执行真正的阻塞调用（Redis 往返 / HTTP Do /
// 拨号），返回一个 IORenderer。job 内**绝不可访问 L 或业务 state**（只能用已捕获的 Go 值 +
// 线程安全句柄如 ctx.Shared / httpClient / client）。等待期间执行器 goroutine 由 Waiter 持续
// drain 任务队列，故 I/O 往返不会饿死 listen 回调等协作式工作。
//
// job 完成后，drive-loop 在执行器 goroutine 上调用 IORenderer 产出本 await_* 的 Lua 返回值
// （此时访问 L / goValueToLua / ctx.recordRequest 才安全）。
func awaitIO(L *lua.LState, fnName string, job func() IORenderer) int {
	return awaitYield(L, fnName, &WaitSpec{Kind: WaitIO, IOName: fnName, IOJob: job})
}
