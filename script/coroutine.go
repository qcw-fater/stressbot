package script

import (
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
	// WaitSleep 是 utils.sleep 的纯计时等待，无唤醒条件，到时即 resume。
	WaitSleep WaitKind = iota + 1
	// WaitListen 等待 network.tcp_listen / network.udp_listen 对应 routeKey 的推送消息，
	// Waiter 同时等待队列通知并处理 Robot mailbox，命中或超时后 resume。
	WaitListen
	// WaitResponse 等待 network.tcp_request / network.udp_request 的响应。
	// Waiter 发送 Packet 并注册响应通道，select 通道 + drain 任务，命中/超时/取消后 resume。
	// 响应到达后由通道直接唤醒，WireRTT 使用底层消息时间戳计算。
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
	Proto    string        // "tcp" / "udp"
	Service  string
	RouteKey string
	S2CProto string
	Packet   []byte // WaitResponse：已编码的请求包，由 Waiter 发送

	// WaitIO 专用：
	IOName string            // 作业名（日志/错误），如 "share.get" / "network.http_request"
	IOJob  func() IORenderer // 后台 goroutine 执行的阻塞 I/O；返回在执行器 goroutine 上产出 Lua 值的 renderer
}

// WaitOutcome 是 Waiter.Await 的等待结果，交回 drive-loop 转成 Lua 返回值。
type WaitOutcome struct {
	Exchange *engine.NetExchange // WaitListen/WaitResponse 命中的响应（nil = 未命中）
	TimedOut bool                // 超时（WaitListen 用；未命中且 ctx 未取消）
	Canceled bool                // ctx 取消导致提前返回
	Err      error               // WaitResponse 的请求错误（发送失败 / 超时 / 断连），listen 不用
	IORender IORenderer          // WaitIO：后台作业完成后返回的 renderer（nil = 作业被放弃/panic）

	// ListenWait / ListenWaitKind：WaitListen 命中时的等待时长与其可测性。
	// 由 Waiter 判定后带回——只有它知道等待的起点，脚本层拿不到。
	ListenWait     time.Duration
	ListenWaitKind engine.ListenWaitKind
}

// Waiter 协作式等待后端，由 Robot 实现。
// Await 必须在执行器 goroutine（业务 LState 唯一所有者）内被 drive-loop 同步调用：
// 它在等待窗口内 drain Robot 任务队列（跑 listen 回调等），故等待期间其他协作式工作得以推进。
type Waiter interface {
	Await(spec *WaitSpec) (WaitOutcome, error)
}

// coroutine 封装一次 Lua 脚本任务（action 脚本 execute 或 listen 回调 on_message）
// 在长驻蹦床 thread（P2，见 trampoline.go）上的执行。
type coroutine struct {
	parent   *lua.LState    // Robot 独占的根 LState，Resume 的接收者
	tramp    *trampThread   // 承载本任务的长驻蹦床 thread（缓存复用或新建）
	trampFn  *lua.LFunction // 蹦床主函数（bootstrap Resume 用）
	sentinel *lua.LUserData // DONE 哨兵（任务完成 yield 的首值，指针相等判定）
	ctx      *Context       // 收尾时归还 thread 缓存的目的地（nil = 直接弃用）
	fn       *lua.LFunction // 入口函数（execute / on_message）
	argc     uint8          // 任务实参数量；当前协议仅允许 1 或 2
	arg1     lua.LValue     // execute/on_message 的第一个实参
	arg2     lua.LValue     // on_message 的第二个实参；argc=1 时固定为 nil
	started  bool           // 本任务是否已投递（首次 Resume 已完成）
	doneOK   bool           // 以 DONE-yield 干净收尾（thread 停在蹦床顶部，可归还）
	name     string         // 脚本名（日志/错误）
}

// close 按收尾状态处置 thread：干净完成 → 归还缓存复用；
// 错误/裸 yield/await 中途放弃（栈停在半途）→ 弃用（下次惰性重建）。
func (co *coroutine) close() {
	if co == nil {
		return
	}
	if co.tramp != nil {
		if co.doneOK {
			releaseTrampThread(co.ctx, co.tramp)
		} else {
			co.tramp.stop()
		}
		co.tramp = nil
	}
	co.fn = nil
	co.arg1 = lua.LNil
	co.arg2 = lua.LNil
}

// startCoroutine 加载脚本指定入口函数并把任务绑定到一条蹦床 thread（尚未 Resume）。
// argc/arg1/arg2 为固定任务实参（execute(r) 为 1 个；on_message(r,msg) 为 2 个）。
func (rp *RuntimePool) startCoroutine(L *lua.LState, scriptName string, entry scriptEntry, argc uint8, arg1, arg2 lua.LValue) (*coroutine, error) {
	if argc != 1 && argc != 2 {
		return nil, fmt.Errorf("脚本 %s 的入口参数数量 %d 不受支持", scriptName, argc)
	}
	fnv, err := rp.loadScriptFn(L, scriptName, entry)
	if err != nil {
		return nil, err
	}
	fn, ok := fnv.(*lua.LFunction)
	if !ok {
		fnName, _ := entry.globalName()
		return nil, fmt.Errorf("脚本 %s 的 %s 不是函数", scriptName, fnName)
	}
	trampFn, err := rp.trampMainFn(L)
	if err != nil {
		return nil, err
	}
	sentinel, err := trampSentinel(L)
	if err != nil {
		return nil, err
	}
	ctx := GetContext(L)
	return &coroutine{
		parent:   L,
		tramp:    acquireTrampThread(L, ctx),
		trampFn:  trampFn,
		sentinel: sentinel,
		ctx:      ctx,
		fn:       fn,
		argc:     argc,
		arg1:     arg1,
		arg2:     arg2,
		name:     scriptName,
	}, nil
}

// startActionCoroutine 加载脚本 execute 入口（实参为 robot 句柄）并创建协程。
func (rp *RuntimePool) startActionCoroutine(L *lua.LState, scriptName string) (*coroutine, error) {
	return rp.startCoroutine(L, scriptName, scriptEntryExecute, 1, createRobotUserData(L), lua.LNil)
}

// resumeResult 描述一次 Resume 的结果。
type resumeResult struct {
	done    bool         // 协程是否已结束
	ret     lua.LValue   // done 时脚本首个返回值原始 LValue（action 脚本据此区分 nil / err table / 非法值）
	retVals []lua.LValue // done 时脚本的全部返回值（boolean 脚本据此取 LBool）
	wait    *WaitSpec    // 未结束时 yield 出的等待规格
}

// resumeCoroutine 推进一次任务：首次投递任务（bootstrap 喂 (sentinel, fn, args...)，
// 复用 thread 则 handoff 喂 (fn, args...)），后续喂入 await 返回值。
//
// 在 Resume 前把 ctx.topThread 置为本任务的蹦床线程，供 await_* 运行时校验顶层协程
// 身份。该字段每次 resume 前重设，故协作式嵌套（回调 await 期间 drain 出另一回调）
// 也能正确归位。
//
// ResumeYield 三分支判定（顺序即优先级）：
//  1. 首值 == DONE 哨兵 → 任务完成，thread 停在蹦床顶部（close() 归还复用）；
//  2. 含 WaitSpec → await_*，走 Waiter 协作式等待（协议与蹦床化前完全一致）；
//  3. 其他 → 脚本裸 coroutine.yield，fail-loud，thread 栈停在脚本内部（close() 弃用）。
func (rp *RuntimePool) resumeCoroutine(co *coroutine, ctx *Context, resumeVals []lua.LValue) (res resumeResult, retErr error) {
	if ctx != nil {
		ctx.topThread = co.tramp.thread
	}

	// panic 兜底：腐坏协程的 panic 会穿透 Resume 边界。典型场景是 await_* 被 pcall 吞掉——
	// gopher-lua 的 yield 被 pcall 的 Go 递归 mainLoop 拦截后，线程已腐坏（Parent 被清空、
	// WaitSpec 被推到父栈、执行却继续），随后蹦床的 DONE-yield 在腐坏线程上触发
	// "can not yield from outside of a coroutine" panic 并穿出 Resume（蹦床化前该腐坏
	// "恰好"表现为 ResumeOK 带出 WaitSpec，由下方 default 分支拦截；蹦床多了一次 yield，
	// 表现升级为 panic，故必须在此兜住）。恢复动作：按父栈残留判定场景还原精确错误、
	// 清理父栈，doneOK 保持 false → close() 弃用该线程。
	savedTop := co.parent.GetTop()
	defer func() {
		if rcv := recover(); rcv != nil {
			swallowed := false
			for i := savedTop + 1; i <= co.parent.GetTop(); i++ {
				if ud, ok := co.parent.Get(i).(*lua.LUserData); ok {
					if _, isSpec := ud.Value.(*WaitSpec); isSpec {
						swallowed = true
						break
					}
				}
			}
			co.parent.SetTop(savedTop) // 清掉吞 yield 在父栈上的残留，父 LState 可继续使用
			res = resumeResult{done: true}
			if swallowed {
				retErr = fmt.Errorf(
					"脚本 %s 的 await_* 被 pcall/coroutine 吞掉（yield 未到达调度器）；"+
						"await_* 只能在动作脚本顶层直接调用，不可置于 pcall/xpcall 或 coroutine.create 内", co.name)
			} else {
				retErr = fmt.Errorf("执行脚本 %s 时协程 panic: %v", co.name, rcv)
			}
		}
	}()

	var (
		st   lua.ResumeState
		err  error
		vals []lua.LValue
	)
	if !co.started {
		co.started = true
		if !co.tramp.booted {
			// bootstrap：蹦床主函数首次进入，实参 (sentinel, fn, argc, arg1, arg2)。
			co.tramp.booted = true
			st, err, vals = co.parent.Resume(
				co.tramp.thread,
				co.trampFn,
				co.sentinel,
				co.fn,
				lua.LNumber(co.argc),
				co.arg1,
				co.arg2,
			)
		} else {
			// handoff：thread 停在蹦床顶部的 yield 处，喂入 (fn, argc, arg1, arg2)。
			st, err, vals = co.parent.Resume(
				co.tramp.thread,
				nil,
				co.fn,
				lua.LNumber(co.argc),
				co.arg1,
				co.arg2,
			)
		}
	} else {
		st, err, vals = co.parent.Resume(co.tramp.thread, nil, resumeVals...)
	}

	switch st {
	case lua.ResumeError:
		// 蹦床内无 pcall，脚本错误直接穿出 → thread 已死，close() 走弃用路径。
		return resumeResult{done: true}, fmt.Errorf("执行脚本 %s 失败: %w", co.name, err)
	case lua.ResumeYield:
		// 分支 1：DONE 哨兵 → 任务完成，vals[1:] 为脚本返回值。
		if len(vals) > 0 {
			if ud, ok := vals[0].(*lua.LUserData); ok && ud == co.sentinel {
				rets := vals[1:]
				// 静默陷阱拦截：await_* 被 pcall 吞掉时 WaitSpec 会成为脚本"返回值"带出。
				if extractWaitSpec(rets) != nil {
					return resumeResult{done: true}, fmt.Errorf(
						"脚本 %s 的 await_* 被 pcall/coroutine 吞掉（yield 未到达调度器）；"+
							"await_* 只能在动作脚本顶层直接调用，不可置于 pcall/xpcall 或 coroutine.create 内", co.name)
				}
				co.doneOK = true
				var ret lua.LValue = lua.LNil
				if len(rets) > 0 {
					ret = rets[0]
				}
				return resumeResult{done: true, ret: ret, retVals: rets}, nil
			}
		}
		// 分支 2：await_*。
		if spec := extractWaitSpec(vals); spec != nil {
			return resumeResult{wait: spec}, nil
		}
		// 分支 3：裸 yield。
		return resumeResult{done: true}, fmt.Errorf(
			"脚本 %s 非法 yield：仅允许经 await_* API 让出（不可直接 coroutine.yield）", co.name)
	default: // ResumeOK：蹦床主函数退出——蹦床是无限循环，正常不可达，仅异常路径。
		// 静默陷阱拦截：await_* 被 pcall 吞掉且把整个协程终结时，WaitSpec 随返回值带出。
		if spec := extractWaitSpec(vals); spec != nil {
			_ = spec
			return resumeResult{done: true}, fmt.Errorf(
				"脚本 %s 的 await_* 被 pcall/coroutine 吞掉（yield 未到达调度器）；"+
					"await_* 只能在动作脚本顶层直接调用，不可置于 pcall/xpcall 或 coroutine.create 内", co.name)
		}
		return resumeResult{done: true}, fmt.Errorf("脚本 %s 蹦床协程异常退出（协程被外部终结）", co.name)
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
