package robot

import (
	"context"
	"sync/atomic"
	"time"

	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/network"
	"stressbot/script"
	"stressbot/utils"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// robotScheduler 是单个 Robot 的协作式调度核心（actor 运行时）。
//
// 对标 skynet 的 service 语义：执行器 goroutine 独占业务 LState，**所有「会等待」的点**
// （Lua await / 节点延迟 / wait 节点 / 请求-响应）都经本调度器的统一 pump（wait）收敛——
// 等待期间持续 drain mailbox（taskCh），绝不裸阻塞，从而任何阻塞点都不会饿死推送回调，
// 也不会出现「pump goroutine 抢占业务 LState」的并发栈损坏。
//
// 线程模型：
//   - mailbox（taskCh）：网络 pump goroutine 投递异步 Lua 工作（listen 回调），只复制消息 + 投递，
//     绝不在 pump goroutine 内碰业务 LState；
//   - owner（执行器 goroutine）：唯一消费者，串行 drain + 推进流程 + resume 协程；
//   - taskWake：投递唤醒信号（1 容量、非阻塞），令处于等待窗口的 owner 及时醒来 drain。
//
// 调度器只持有对 Robot 的回引用以读取 actor 的 I/O 句柄（ctx / client），自身不重复持有，
// 保证与 Robot 生命周期一致、并便于独立单测。
type robotScheduler struct {
	robot       *Robot
	taskCh      chan pendingTask
	taskWake    chan struct{}
	taskDropped atomic.Int64
}

// newRobotScheduler 构造调度器（mailbox 容量 robotTaskQueueSize，唤醒信号容量 1）。
func newRobotScheduler(r *Robot) *robotScheduler {
	return &robotScheduler{
		robot:    r,
		taskCh:   make(chan pendingTask, robotTaskQueueSize),
		taskWake: make(chan struct{}, 1),
	}
}

// enqueue 投递一个任务到 mailbox（线程安全，由网络 pump goroutine 调用）。
// 队列满时丢弃最新并计数，绝不阻塞 pump（保护 I/O 平面吞吐）；投递成功后 notify owner。
func (s *robotScheduler) enqueue(t pendingTask) {
	select {
	case s.taskCh <- t:
		// 唤醒可能正处于等待窗口的 owner，及时 drain。
		select {
		case s.taskWake <- struct{}{}:
		default:
		}
	default:
		n := s.taskDropped.Add(1)
		if stresslog.DebugEnabled() {
			stresslog.Debug("[ROBOT] 任务队列已满，丢弃 listen 回调任务",
				zap.Int("id", s.robot.id), zap.String("task", t.name), zap.Int64("dropped", n))
		}
	}
}

// dropped 返回累计丢弃任务数（指标用）。
func (s *robotScheduler) dropped() int64 { return s.taskDropped.Load() }

// drain 串行执行至多 max 个待处理任务（在 owner goroutine 内调用，业务 LState 唯一所有者）。
// 节点边界（RunPendingTasks）与等待窗口（wait）共用，确保两类安全点行为一致。ctx 取消立即停止。
func (s *robotScheduler) drain(ctx context.Context, max int) {
	for i := 0; i < max; i++ {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		select {
		case t := <-s.taskCh:
			t.exec()
		default:
			return
		}
	}
}

// wait 是统一的协作式等待 pump，等待至 deadline：
//   - check==nil（sleep / 节点延迟 / wait 节点）：等到截止；taskWake 唤醒时提前醒来 drain 后续等。
//   - check!=nil（listen）：每 pollMs 轮询一次 check，命中即返回；taskWake 也会触发提前 drain。
//
// 每轮都 drain mailbox（跑 listen 回调等）。ctx 取消立即返回 Canceled。
func (s *robotScheduler) wait(deadline time.Time, pollMs int, check func() *engine.NetExchange) script.WaitOutcome {
	ctx := s.robot.ctx
	for {
		if ctx.Err() != nil {
			return script.WaitOutcome{Canceled: true}
		}
		if check != nil {
			if ex := check(); ex != nil {
				return script.WaitOutcome{Exchange: ex}
			}
		}
		s.drain(ctx, maxDrainPerBoundary)
		if check != nil {
			if ex := check(); ex != nil {
				return script.WaitOutcome{Exchange: ex}
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			if check == nil {
				return script.WaitOutcome{} // sleep 到时
			}
			return script.WaitOutcome{TimedOut: true} // listen 超时
		}

		w := remaining
		if check != nil {
			tick := time.Duration(pollMs) * time.Millisecond
			if tick <= 0 {
				tick = defaultAwaitPollMs * time.Millisecond
			}
			if tick < w {
				w = tick
			}
		}

		timer := time.NewTimer(w)
		select {
		case <-ctx.Done():
			timer.Stop()
			return script.WaitOutcome{Canceled: true}
		case <-s.taskWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// awaitResponse 协作式请求-响应：发送 spec.Packet 并注册响应通道，select 通道 + drain mailbox。
// 用通道即时唤醒（非轮询），保证 WireRTT 测量不被轮询间隔污染。命中 / 超时 / 取消 / 发送失败
// 分别返回带 Exchange / Err / Canceled 的 WaitOutcome，由 drive-loop 的 requestResultValues 转 Lua 值。
func (s *robotScheduler) awaitResponse(spec *script.WaitSpec) script.WaitOutcome {
	ctx := s.robot.ctx
	var conn *network.Connection
	if spec.Proto == "udp" {
		conn = s.robot.client.GetUDPConn(spec.Service)
	} else {
		conn = s.robot.client.GetTCPConn(spec.Service)
	}
	if conn == nil {
		return script.WaitOutcome{Err: engine.NewActionError(errcode.ErrConnNotFound, "service="+spec.Service)}
	}

	pr, err := conn.SendRequest(spec.Packet, spec.RouteKey, spec.Duration)
	if err != nil {
		return script.WaitOutcome{Err: err}
	}
	defer pr.Close()

	// 单一 timer 计满整个超时窗口；taskWake 唤醒只 drain 不重置超时。
	timer := time.NewTimer(pr.Timeout())
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return script.WaitOutcome{Canceled: true}
		}
		s.drain(ctx, maxDrainPerBoundary)
		select {
		case resp := <-pr.C():
			if resp == nil {
				return script.WaitOutcome{Canceled: true}
			}
			return script.WaitOutcome{Exchange: &engine.NetExchange{
				Body:          resp.Data,
				HeaderErr:     resp.HeaderErr,
				SendWireBytes: len(spec.Packet),
				RecvWireBytes: resp.WireBytes,
				Timing:        engine.RequestTiming(pr.Timing(resp)),
			}}
		case <-s.taskWake:
			// 唤醒后回到循环顶部 drain，再继续 select。
		case <-ctx.Done():
			return script.WaitOutcome{Canceled: true}
		case <-timer.C:
			return script.WaitOutcome{Err: engine.NewActionError(errcode.ErrRecvTimeout,
				"service="+spec.Service+" route="+spec.RouteKey+" timeout="+pr.Timeout().String())}
		}
	}
}

// runIO 协作式执行一次 Class B 阻塞 I/O（share.* Redis / http / 拨号）：把 job 投递到协程池
// 后台执行真正的阻塞调用，调用方（执行器 goroutine）等待期间持续 drain mailbox（**串行**跑
// listen 回调等），故 I/O 往返不会饿死协作式工作，也不会出现「pump goroutine 抢占业务 LState」。
// job 的结果由其自身经闭包捕获，调用方在 runIO 返回后读取（happens-before 经 done 通道保证）。
//
// 这是 **Lua await_*（awaitIO）与声明式 http/connect 动作（ActionExecutor.coopIO）共用的唯一
// 原语**，保证两条路径同一协作式语义——「任何阻塞点都不裸阻塞」的统一约束覆盖声明式与脚本。
// job 内**绝不可访问业务 LState / state**（只能用线程安全句柄：ctx.Shared / httpClient / client）。
//
// 不内联跑阻塞调用（会卡死执行器自身），也不放进 wait 的 timer/poll 模型（Class B 的唤醒事件
// scheduler 观测不到）——这是 Class B 区别于 Class A 的核心。
//
// 为什么用协程池而非裸 go：① 项目规范禁止裸 go func（统一 recover + 追踪）；② 池容量为 0（无限），
// 且执行器本身常驻占用池槽位（数量 ≥ Robot 数），故池必然 ≥ Robot 数，不会出现「执行器全 park 在
// runIO 等作业、作业又抢不到池槽位」的死锁。
//
// 不监听 ctx.Done 提前返回：job 的阻塞调用均受 ctx/超时约束（share 用派生 opCtx；http client
// Timeout 10s + dial 5s；拨号受 ctx + OS connect 超时），ctx 取消时作业会很快返回。提前返回会
// 让后台作业的结果无人接收（虽 done 缓冲 1 不泄漏），且声明式路径拿不到 Go 结果，故统一等作业完成。
// job panic 由协程池 recover + 本地 recover 兜底，并始终 signal done，避免调用方永久阻塞。
func (s *robotScheduler) runIO(job func()) {
	done := make(chan struct{}, 1) // 缓冲 1：即使调用方已放弃等待，后台 signal 也不阻塞/泄漏
	utils.GetWorkPool().Go(func() {
		defer func() { done <- struct{}{} }() // 始终 signal（含 panic 路径，defer LIFO 在 recover 之后）
		defer func() {
			if r := recover(); r != nil {
				stresslog.DPanic("[ROBOT] Class B I/O 作业 panic",
					zap.Int("id", s.robot.id), zap.Any("error", r))
			}
		}()
		job()
	})

	for {
		s.drain(s.robot.ctx, maxDrainPerBoundary)
		select {
		case <-done:
			return
		case <-s.taskWake:
			// 唤醒后回到循环顶部 drain，再继续等作业完成。
		}
	}
}

// awaitIO Lua await_*（share.* / http_request / connect_*）的 Class B 协作式 I/O：经 runIO
// 后台跑 spec.IOJob，返回其产出的 IORenderer（由 drive-loop 在执行器 goroutine 上调用产出 Lua
// 返回值）。job panic 时 renderer 为 nil → buildResumeVals 以空返回值 resume。
func (s *robotScheduler) awaitIO(spec *script.WaitSpec) script.WaitOutcome {
	var renderer script.IORenderer
	s.runIO(func() { renderer = spec.IOJob() })
	return script.WaitOutcome{IORender: renderer}
}
