package robot

import (
	"context"
	"fmt"
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
// （Lua await / 节点延迟 / wait 节点 / 请求-响应）都经本调度器的统一 pump（wait / awaitResponse /
// runIO）收敛——等待窗口内直接 select taskCh 就地处理 listen 回调，绝不裸阻塞，从而任何阻塞点
// 都不会饿死推送回调，也不会出现「pump goroutine 抢占业务 LState」的并发栈损坏。
//
// 线程模型：
//   - mailbox（taskCh）：网络 pump goroutine 投递异步 Lua 工作（listen 回调），只复制消息 + 投递，
//     绝不在 pump goroutine 内碰业务 LState；
//   - owner（执行器 goroutine）：唯一消费者，在等待窗口的 select 内就地处理 taskCh + 推进流程 +
//     resume 协程。owner 不在 select 上时（正跑业务 Lua / 推进流程），任务在 taskCh 排队等下个等待窗口。
//
// 终端条件纯语义化（就绪 / 响应 / done / 超时 / 取消）：无计数上界、无独立唤醒通道——taskCh 的
// channel 收发本身就是 owner 与 pump 之间的唤醒信号（向正 select 在 taskCh 上的 owner 投递即唤醒）。
// drain 只发生在空闲窗口（action 节点的 nodeDelay / wait 节点 / Lua await），flat-out 流程不 drain。
//
// 调度器只持有对 Robot 的回引用以读取 actor 的 I/O 句柄（ctx / client），自身不重复持有，
// 保证与 Robot 生命周期一致、并便于独立单测。
type robotScheduler struct {
	robot       *Robot
	taskCh      chan pendingTask
	taskDropped atomic.Int64
}

// newRobotScheduler 构造调度器（mailbox 容量 robotTaskQueueSize）。
func newRobotScheduler(r *Robot) *robotScheduler {
	return &robotScheduler{
		robot:  r,
		taskCh: make(chan pendingTask, robotTaskQueueSize),
	}
}

// enqueue 投递一个任务到 mailbox（线程安全，由网络 pump goroutine 调用）。
// 队列满时丢弃最新并计数，绝不阻塞 pump（保护 I/O 平面吞吐）。
// 投递成功后无需显式唤醒——向 taskCh 发送本身就会唤醒正 select 在其上的 owner。
// 丢弃走 Warn 级别（回调丢弃是压测保真度问题，不应藏在 debug 里）。
func (s *robotScheduler) enqueue(t pendingTask) {
	select {
	case s.taskCh <- t:
	default:
		n := s.taskDropped.Add(1)
		stresslog.Warn("[ROBOT] 任务队列已满，丢弃 listen 回调任务",
			zap.Int("id", s.robot.id),
			zap.String("account", s.robot.account),
			zap.String("task", t.name),
			zap.Int("queueLen", len(s.taskCh)),
			zap.Int("queueCap", cap(s.taskCh)),
			zap.Int64("dropped", n))
	}
}

// wait 是统一的协作式等待 pump，等待至 deadline：
//   - check==nil（sleep / 节点延迟 / wait 节点）：等到截止；
//   - check!=nil（listen）：命中即返回，超时返回 TimedOut。
//
// park 期间直接 select taskCh：来一个 listen 回调任务就地处理一个，每处理完回 loop 顶
// 重查 check / deadline——天然实现 per-callback 的就绪检查（取代旧的批 drain + boundary check）。
// wake 是容量 1 的边沿提示；ctx 或 Robot 生命周期取消时立即返回 Canceled。
func (s *robotScheduler) wait(ctx context.Context, deadline time.Time, wake <-chan struct{}, check func() *engine.NetExchange) script.WaitOutcome {
	var callerDone <-chan struct{}
	if ctx != nil {
		callerDone = ctx.Done()
	}
	var robotDone <-chan struct{}
	if s.robot.ctx != nil {
		robotDone = s.robot.ctx.Done()
	}

	// 监听等待的起点。终点取帧被内核收到的时刻，而不是 owner 被唤醒的时刻。
	waitStart := time.Now()
	var timer *time.Timer
	defer func() {
		if timer != nil {
			utils.PutTimer(timer)
		}
	}()
	for {
		if (ctx != nil && ctx.Err() != nil) || (s.robot.ctx != nil && s.robot.ctx.Err() != nil) {
			return script.WaitOutcome{Canceled: true}
		}
		if check != nil {
			if ex := check(); ex != nil {
				wait, kind := engine.ClassifyListenWait(waitStart, ex.RecvFrameAt)
				return script.WaitOutcome{Exchange: ex, ListenWait: wait, ListenWaitKind: kind}
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if check == nil {
				return script.WaitOutcome{} // sleep 到时
			}
			return script.WaitOutcome{TimedOut: true} // listen 超时
		}

		// timer 只覆盖整个剩余 deadline；listen 到达由 wake 唤醒。
		// t.exec() 前必 Stop：回调里嵌套 wait 会再从池取，互不干扰。
		if timer == nil {
			timer = utils.GetTimer(remaining)
		} else {
			timer.Reset(remaining)
		}
		select {
		case t := <-s.taskCh:
			timer.Stop()
			t.exec() // 就地处理 → loop 回顶重查队列 / deadline
		case <-wake:
			timer.Stop()
			// wake 只是提示，队列才是事实源；回顶后通过 check 消费。
		case <-callerDone:
			return script.WaitOutcome{Canceled: true}
		case <-robotDone:
			return script.WaitOutcome{Canceled: true}
		case <-timer.C:
			// deadline 边界回顶重查一次队列，避免到达与超时的边界竞态。
		}
	}
}

// awaitResponse 协作式请求-响应：发送 spec.Packet 并注册响应通道，select 通道 + taskCh。
// taskCh 与响应通道在同一个 select 里公平竞争：响应经通道即时命中（不被 drain 批次挡），
// WireRTT 使用底层消息时间戳计算。命中 / 超时 / 取消 / 发送失败分别返回带 Exchange / Err /
// Canceled 的 WaitOutcome，由 drive-loop 的 requestResultValues 转 Lua 值。
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

	// 单一池化 timer 计满整个超时窗口（每请求一只 NewTimer 的分配消除）。
	timer := utils.GetTimer(pr.Timeout())
	defer utils.PutTimer(timer)
	for {
		if ctx.Err() != nil {
			return script.WaitOutcome{Canceled: true}
		}
		select {
		case t := <-s.taskCh:
			t.exec() // 就地处理 listen 回调
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
		case <-ctx.Done():
			return script.WaitOutcome{Canceled: true}
		case <-timer.C:
			return script.WaitOutcome{Err: engine.NewActionError(errcode.ErrRecvTimeout,
				"service="+spec.Service+" route="+spec.RouteKey+" timeout="+pr.Timeout().String())}
		}
	}
}

// runIO 协作式执行一次 Class B 阻塞 I/O（share.* Redis / http / 拨号）：把 job 投递到协程池
// 后台执行真正的阻塞调用，调用方（执行器 goroutine）等待期间直接 select taskCh 就地处理
// listen 回调，故 I/O 往返不会饿死协作式工作，也不会出现「pump goroutine 抢占业务 LState」。
// job 的结果由其自身经闭包捕获，调用方在 runIO 返回后读取（happens-before 经 done 通道保证）。
//
// 这是 **Lua await_*（awaitIO）与声明式 http/connect 动作（ActionExecutor.coopIO）共用的唯一
// 原语**，保证两条路径同一协作式语义——「任何阻塞点都不裸阻塞」的统一约束覆盖声明式与脚本。
// job 内**绝不可访问业务 LState / state**（只能用线程安全句柄：ctx.Shared / httpClient / client）。
//
// 不内联跑阻塞调用（会卡死执行器自身），也不放进 wait 的事件/截止时间模型（Class B 的唤醒事件
// scheduler 观测不到）——这是 Class B 区别于 Class A 的核心。
//
// 为什么用协程池而非裸 go：项目规范要求统一 recover 与 goroutine 追踪。提交失败会立即返回错误，
// 由声明式动作或 Lua Waiter 向上游传播，不能在没有后台作业的情况下继续等待 done。
//
// 不监听 ctx.Done 提前返回：job 的阻塞调用均受 ctx/超时约束（share 用派生 opCtx；http client
// Timeout 10s + dial 5s；拨号受 ctx + OS connect 超时），ctx 取消时作业会很快返回。提前返回会
// 让后台作业的结果无人接收（虽 done 缓冲 1 不泄漏），且声明式路径拿不到 Go 结果，故统一等作业完成。
// job panic 由协程池 recover + 本地 recover 兜底，并始终 signal done，避免调用方永久阻塞。
func (s *robotScheduler) runIO(job func()) error {
	return s.runIOWithSubmit(job, utils.GetWorkPool().Submit)
}

func (s *robotScheduler) runIOWithSubmit(job func(), submit func(func()) error) error {
	done := make(chan struct{}, 1) // 缓冲 1：即使调用方已放弃等待，后台 signal 也不阻塞/泄漏
	if err := submit(func() {
		defer func() { done <- struct{}{} }() // 始终 signal（含 panic 路径，defer LIFO 在 recover 之后）
		defer func() {
			if r := recover(); r != nil {
				stresslog.DPanic("[ROBOT] Class B I/O 作业 panic",
					zap.Int("id", s.robot.id), zap.Any("error", r))
			}
		}()
		job()
	}); err != nil {
		stresslog.Error("[ROBOT] 提交 Class B I/O 作业失败",
			zap.Int("id", s.robot.id),
			zap.String("account", s.robot.account),
			zap.Error(err))
		return fmt.Errorf("提交 Class B I/O 作业失败: %w", err)
	}

	for {
		select {
		case t := <-s.taskCh:
			t.exec() // 就地处理 listen 回调
		case <-done:
			return nil
		}
	}
}

// awaitIO Lua await_*（share.* / http_request / connect_*）的 Class B 协作式 I/O：经 runIO
// 后台跑 spec.IOJob，返回其产出的 IORenderer（由 drive-loop 在执行器 goroutine 上调用产出 Lua
// 返回值）。job panic 时 renderer 为 nil → buildResumeVals 以空返回值 resume。
func (s *robotScheduler) awaitIO(spec *script.WaitSpec) script.WaitOutcome {
	var renderer script.IORenderer
	err := s.runIO(func() { renderer = spec.IOJob() })
	return script.WaitOutcome{IORender: renderer, Err: err}
}
