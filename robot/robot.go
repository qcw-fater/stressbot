package robot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"

	"stressbot/adapter"
	"stressbot/codec"
	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	"stressbot/sharedstate"
	"stressbot/state"
	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"
)

// Robot 单个压测机器人实例。
// 每个 Robot 拥有独立的状态存储、网络客户端、Lua 运行时和流程执行器。
type Robot struct {
	id         int                    // 机器人唯一编号（= startNumber + index）
	index      int                    // 批次内 0-based 序号
	account    string                 // 账号名
	state      *state.Store           // 线程安全的键值状态存储
	client     *network.Client        // 多服务网络客户端（管理 TCP/UDP 连接池）
	factory    *protox.Factory        // protobuf 消息工厂（动态创建/解析）
	executor   *engine.Executor       // 流程图执行器
	luaPool    *script.RuntimePool    // Lua 运行时池（Robot 持有独占 LState）
	l          *lua.LState            // 当前 Robot 独占的 Lua 状态（从 luaPool 获取）
	actionExec *engine.ActionExecutor // 声明式动作执行器
	ctx        context.Context        // 机器人生命周期上下文
	cancel     context.CancelFunc     // 取消函数（Stop 时调用）
	running    atomic.Bool            // 是否正在运行
	dialer     *network.Dialer        // 网络拨号器（封装 gnet 事件循环）
	httpClient *http.Client           // HTTP 客户端（声明式 HTTP 动作用）
	// 全 codec 路径（dial/decode/encode/心跳/listen/业务 Lua）共享同一份 codec 映射：
	//   - dial/decode：ConnectTCP/UDP 拨号前 Resolve，nil → fail loud；非 nil 注入 Connection；
	//   - encode/心跳/listen：engine.ActionExecutor / robotActionHandler / netSenderAdapter 各自 Resolve；
	//   - 业务 Lua：通过 script.Context.Resolver（= r.resolver）在 api_network.go 内 Resolve。
	resolver       adapter.CodecResolver
	shared         sharedstate.Store // 任务级共享状态后端（可为 nil）
	mainService    string            // 主连接服务名，意外断开时停止机器人
	requestTimeout time.Duration     // robotConfig.timeoutSec 注入；用作 Lua tcp/udp_request 默认 timeout
	timingLevel    int               // monitor.timingDetail 映射后的 engine 计时级别
	execDone       chan struct{}     // executor goroutine 结束信号，cleanup 等待它安全退出
	done           chan struct{}     // Robot 生命周期结束信号，Close 时等待
	// sched 是 Robot 的协作式调度核心（actor 运行时）：mailbox + 统一等待 pump。
	// 网络 pump goroutine 经 sched.enqueue 投递异步 Lua 工作（listen 回调），
	// 由执行器 goroutine 在等待窗口的 select 内就地串行消费（节点入口不再单独 drain）。详见 scheduler.go。
	sched              *robotScheduler
	onDone             func(*Robot, CleanupStatus) // 执行 goroutine 结束后回调（由 Manager 设置）
	cleanupOnce        sync.Once
	cleanupMu          sync.Mutex
	cleanupResult      CleanupStatus
	pendingHeartbeatMu sync.Mutex
	pendingHeartbeats  map[string]engine.HeartbeatConfig
}

// Config 单个机器人的配置。
type Config struct {
	ID             int               // 机器人唯一编号（= startNumber + batchOffset）
	Index          int               // 批次内 0-based 序号
	Account        string            // 账号名
	StateExtra     map[string]string // 初始状态额外键值对
	HTTPTimeout    time.Duration     // HTTP 请求超时
	RequestTimeout time.Duration     // 网络请求超时（TCP/UDP RequestResponse）
	MainService    string            // 主连接服务名，意外断开时停止机器人
	Shared         sharedstate.Store // 任务级共享状态后端（可为 nil，表示未启用）
}

// NewRobot 创建机器人实例。
//
// resolver 按「server 串 <proto>:<service>」解析每条连接的 Go SchemaAdapter，
// 全 codec 路径共享同一份 codec 映射：
//   - dial/decode：ConnectTCP/UDP 拨号前 Resolve，nil → fail loud；非 nil 注入 Connection；
//   - encode/心跳/listen：engine.ActionExecutor / robotActionHandler / netSenderAdapter 各自 Resolve；
//   - 业务 Lua：经 script.Context.Resolver（= resolver）在 api_network.go 内 Resolve。
//
// 返回 error 的场景：Lua 运行时池未提供 LState（pool 未初始化）。
// codec 配置错误（Resolve nil）不在 NewRobot 暴露——拨号 / 首次 encode 时按 fail-loud 上报，
// 便于定位到具体连接（service 串）而非整 Robot 创建失败。
func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
	resolver adapter.CodecResolver,
	dialer *network.Dialer, luaPool *script.RuntimePool) (*Robot, error) {

	if resolver == nil {
		return nil, fmt.Errorf("NewRobot: resolver 不能为 nil（codec 未配置）")
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 读取 monitor 的 timingDetail 配置，传递给 network 和 engine 层。
	var timingDetail monitor.TimingDetailLevel
	var engineTimingLevel int
	if mc := monitor.Global(); mc != nil {
		timingDetail = mc.TimingDetail()
		switch timingDetail {
		case monitor.TimingFullDetail:
			engineTimingLevel = engine.TimingLevelFull
		case monitor.TimingCodecDetail:
			engineTimingLevel = engine.TimingLevelCodec
		default:
			engineTimingLevel = engine.TimingLevelRTTOnly
		}
	}

	r := &Robot{
		id:             cfg.ID,
		index:          cfg.Index,
		account:        cfg.Account,
		state:          state.NewStore(),
		client:         network.NewClient(cfg.Account, cfg.RequestTimeout, timingDetail),
		factory:        factory,
		luaPool:        luaPool,
		ctx:            ctx,
		cancel:         cancel,
		dialer:         dialer,
		httpClient:     newRobotHTTPClient(cfg.HTTPTimeout),
		mainService:    cfg.MainService,
		shared:         cfg.Shared,
		requestTimeout: cfg.RequestTimeout,
		timingLevel:    engineTimingLevel,
		resolver:       resolver,
		execDone:       make(chan struct{}),
		done:           make(chan struct{}),
	}
	r.sched = newRobotScheduler(r)

	if luaPool != nil {
		r.l = luaPool.Acquire()
	}

	// 仅校验 LState 可用——业务脚本执行需要 LState；codec 路径完全在 Go 侧（resolver），
	// 不再依赖 r.l 上的适配器脚本副本。
	if r.l == nil {
		cancel()
		return nil, fmt.Errorf("NewRobot: Lua 运行时池未提供 LState")
	}

	r.state.Set("id", cfg.ID)
	r.state.Set("index", cfg.Index)
	r.state.Set("account", cfg.Account)
	for k, v := range cfg.StateExtra {
		r.state.Set(k, v)
	}

	r.actionExec = engine.NewActionExecutor(r.state, &netSenderAdapter{robot: r}, r.factory, r.resolver, engineTimingLevel)
	// 注入协作式休眠：声明式 listen 轮询间隔走调度器（drain mailbox），与 Lua await / 节点延迟同源。
	r.actionExec.SetCooperativeSleeper(r.cooperativeSleep)
	// 注入协作式阻塞 I/O：声明式 httpRequest / tcpConnect / udpConnect 走调度器 runIO（后台跑 +
	// drain mailbox），与 Lua await_*（share/http/connect）同一原语，两条路径协作式语义统一。
	r.actionExec.SetCooperativeIO(r.sched.runIO)
	r.executor = engine.NewExecutor(flow, &robotActionHandler{robot: r, flow: flow}, r.account)

	return r, nil
}

// GetID 返回机器人 ID。
func (r *Robot) GetID() int { return r.id }

// GetAccount 返回机器人账号名。
func (r *Robot) GetAccount() string { return r.account }

// GetState 返回机器人状态存储。
func (r *Robot) GetState() *state.Store { return r.state }

// GetClient 返回网络客户端。
func (r *Robot) GetClient() *network.Client { return r.client }

// GetFactory 返回 proto 消息工厂。
func (r *Robot) GetFactory() *protox.Factory { return r.factory }

// Start 启动机器人
func (r *Robot) Start() {
	if !r.running.CompareAndSwap(false, true) {
		return
	}

	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
		defer r.running.Store(false)
		defer close(r.done)

		if r.l != nil {
			// 绑定生命周期 ctx 到 LState：cancel 时正在执行的 Lua 脚本（含死循环/超长循环）
			// 会在下一个指令检查点返回 context canceled 错误并退出，使 cleanup 不再因
			// 不可中断的 Lua 而永久卡死、隔离 LState 永不回收。
			// 归还到池前由 RuntimePool.Release 调 RemoveContext 清理，避免复用时被旧 ctx 污染。
			r.l.SetContext(r.ctx)
			script.SetContext(r.l, &script.Context{
				RobotID:               r.id,
				Index:                 r.index,
				Account:               r.account,
				Store:                 r.state,
				Factory:               r.factory,
				Resolver:              r.resolver,
				NetSender:             &netSenderAdapter{robot: r},
				Ctx:                   r.ctx,
				Shared:                r.shared,
				DefaultRequestTimeout: r.requestTimeout,
				TimingLevel:           r.timingLevel,
				Waiter:                &robotWaiter{robot: r, netSender: &netSenderAdapter{robot: r}},
			})
		}

		stresslog.Info("[ROBOT] 启动", zap.Int("id", r.id), zap.String("account", r.account))
		monitor.Global().RobotStarted()
		monitor.Global().RobotRunning()

		utils.GetWorkPool().Go(func() {
			defer close(r.execDone)
			if err := r.executor.Run(r.ctx); err != nil {
				if r.ctx.Err() == nil {
					stresslog.Error("[ROBOT] 流程异常退出", zap.Int("id", r.id), zap.Error(err))
					monitor.Global().RobotErrored()
				} else {
					monitor.Global().RobotStopped()
				}
			} else {
				monitor.Global().RobotStopped()
			}
		})

		select {
		case <-r.execDone:
		case <-stopCh:
			r.cancel()
			<-r.execDone
		}

		cleanup := r.cleanup(CleanupReasonNatural, true)
		stresslog.Info("[ROBOT] 已停止", zap.Int("id", r.id), zap.String("account", r.account), zap.String("cleanup", string(cleanup.Status)))
		if r.onDone != nil {
			r.onDone(r, cleanup)
		}
	})
}

// Stop 停止机器人
func (r *Robot) Stop() {
	r.cancel()
}

// Wait 等待机器人执行 goroutine 结束。
func (r *Robot) Wait() {
	<-r.done
}

// robotCloseTimeout Robot.Close 总体超时时间。
// 阻塞点有两处：
//  1. r.Wait()：等待 executor goroutine 退出（lua 死循环、嵌套调用可能卡死）
//  2. r.client.CloseAll()：等待 connectionPump / listen queue 清理退出
//
// 任一阻塞超过 robotCloseTimeout 即放弃等待，强制返回让上层推进。
// 代价：LState 不归还到池、state 不清空（避免与卡死的 goroutine 并发访问），
// 进程退出时由 OS 回收，轻微资源泄漏可接受。
const robotCloseTimeout = 10 * time.Second

// defaultListenQueueSize 监听缓存队列默认容量，缺省等价于旧单槽语义。
// 与 network.defaultListenQueueSize 保持一致（同值，独立定义避免跨包依赖未导出符号）。
const defaultListenQueueSize = 1

const (
	// robotTaskQueueSize Robot 协作式调度任务队列容量。满则丢弃最新任务并计入 taskDropped，
	// 不阻塞投递方（网络 pump goroutine）。listen 脚本回调属推送热路径，丢弃语义与 listen
	// 缓存队列「保最新」一致：宁可丢回调也不阻塞 I/O 平面。
	robotTaskQueueSize = 256
)

// pendingTask 投递到 Robot 任务队列的一个工作单元。
// exec 在执行器 goroutine（业务 LState 唯一所有者）的等待窗口 select 内就地串行调用，
// 因此 exec 内可安全访问业务 Lua（r.l）。
type pendingTask struct {
	name string // 任务名（= listen 名），用于日志与指标
	exec func() // 实际工作，在执行器 goroutine 内执行
}

// defaultAwaitPollMs await 监听轮询的兜底间隔（spec.PollMs<=0 时用）。
const defaultAwaitPollMs = 50

// robotWaiter 实现 script.Waiter：action 协程在 await_* 处 yield 后，由 drive-loop 调本类型
// 在执行器 goroutine 内协作式等待。等待窗口内 drain Robot 任务队列（跑 listen 回调等），
// 故长 sleep / listen 期间其他协作式工作不被饿死（计划 §9 的解法）。
type robotWaiter struct {
	robot     *Robot
	netSender *netSenderAdapter
}

// Await 解释一条 WaitSpec：WaitSleep 纯计时等待；WaitListen 轮询监听队列。
// 两者等待期间都 drain 任务队列。ctx 取消经 WaitOutcome.Canceled 表达（不返回 error）。
func (w *robotWaiter) Await(spec *script.WaitSpec) (script.WaitOutcome, error) {
	deadline := time.Now().Add(spec.Duration)
	switch spec.Kind {
	case script.WaitSleep:
		return w.robot.sched.wait(deadline, 0, nil), nil
	case script.WaitListen:
		check := func() *engine.NetExchange {
			if spec.Proto == "udp" {
				return w.netSender.GetUDPListenResp(spec.Service, spec.RouteKey)
			}
			return w.netSender.GetTCPListenResp(spec.Service, spec.RouteKey)
		}
		return w.robot.sched.wait(deadline, spec.PollMs, check), nil
	case script.WaitResponse:
		return w.robot.sched.awaitResponse(spec), nil
	case script.WaitIO:
		return w.robot.sched.awaitIO(spec), nil
	default:
		return script.WaitOutcome{}, fmt.Errorf("未知的 WaitSpec.Kind=%d", spec.Kind)
	}
}

// Close 停止机器人并释放资源。
// 正常路径会归还 Lua LState；超时路径会隔离 LState，避免复用可能仍在使用的运行时。
func (r *Robot) Close() CleanupStatus {
	return r.cleanup(CleanupReasonAdminStop, false)
}

func (r *Robot) cleanup(reason CleanupReason, executorDone bool) CleanupStatus {
	var result CleanupStatus
	r.cleanupOnce.Do(func() {
		start := time.Now()
		r.Stop()

		waitDone := executorDone
		closeDone := false
		closeResult := network.CloseAllResult{Done: true}

		closeCh := make(chan network.CloseAllResult, 1)
		utils.GetWorkPool().Go(func() {
			closeCh <- r.client.CloseAllWithTimeout(robotCloseTimeout)
		})

		waitCh := make(chan struct{}, 1)
		if !executorDone && r.execDone != nil {
			utils.GetWorkPool().Go(func() {
				<-r.execDone
				waitCh <- struct{}{}
			})
		} else {
			waitDone = true
		}

		timer := time.NewTimer(robotCloseTimeout)
		defer timer.Stop()
		for !(waitDone && closeDone) {
			select {
			case <-waitCh:
				waitDone = true
			case cr := <-closeCh:
				closeDone = true
				closeResult = cr
			case <-timer.C:
				phase := "both"
				if waitDone && !closeDone {
					phase = "close_all"
				} else if !waitDone && closeDone {
					phase = "executor"
				}
				issue := CleanupIssue{
					RobotID:      r.id,
					Account:      r.account,
					Phase:        phase,
					WaitDone:     waitDone,
					CloseAllDone: closeDone,
					Message:      "机器人清理超时，Lua 运行时未归还",
				}
				result = cleanupTimeout(reason, time.Since(start), issue)
				stresslog.Error("[ROBOT] 清理超时，已隔离 Lua 运行时",
					zap.Int("id", r.id),
					zap.String("account", r.account),
					zap.String("reason", string(reason)),
					zap.String("phase", phase),
					zap.Duration("timeout", robotCloseTimeout),
					zap.Bool("waitDone", waitDone),
					zap.Bool("closeAllDone", closeDone),
					zap.Int("decodeTimeouts", closeResult.DecodeTimeouts),
					zap.Int("listenTimeouts", closeResult.ListenTimeouts))
				return
			}
		}

		if !closeResult.Done {
			issue := CleanupIssue{
				RobotID:      r.id,
				Account:      r.account,
				Phase:        "close_all",
				WaitDone:     true,
				CloseAllDone: false,
				Message:      closeResult.Message,
			}
			result = cleanupTimeout(reason, time.Since(start), issue)
			stresslog.Error("[ROBOT] 连接清理超时，已隔离 Lua 运行时",
				zap.Int("id", r.id),
				zap.String("account", r.account),
				zap.Int("decodeTimeouts", closeResult.DecodeTimeouts),
				zap.Int("listenTimeouts", closeResult.ListenTimeouts))
			return
		}

		if r.httpClient != nil {
			if tr, ok := r.httpClient.Transport.(*http.Transport); ok {
				tr.CloseIdleConnections()
			}
		}
		if r.l != nil && r.luaPool != nil {
			r.luaPool.Release(r.l)
			r.l = nil
		}
		r.state.Clear()
		result = cleanupOK(reason, time.Since(start))
	})

	r.cleanupMu.Lock()
	if result.Status != "" {
		r.cleanupResult = result
	} else {
		result = r.cleanupResult
	}
	r.cleanupMu.Unlock()
	return result
}

// ConnectTCP 建立 TCP 连接
func (r *Robot) ConnectTCP(serviceName, address string) bool {
	ok := r.client.ConnectTCP(serviceName)
	if !ok {
		return false
	}

	conn := r.client.GetTCPConn(serviceName)
	if conn == nil {
		return false
	}

	// 按连接的 server 串 "tcp:<service>" 从 resolver 解析 codec；未映射 → fail loud（不静默回退默认 codec）。
	server := "tcp:" + serviceName
	adp := r.resolver.Resolve(server)
	if adp == nil {
		stresslog.Error("[ROBOT] TCP 连接无 codec 配置（resolver 未映射），拨号中止",
			zap.Int("id", r.id), zap.String("account", r.account),
			zap.String("server", server), zap.String("service", serviceName), zap.String("addr", address))
		monitor.Global().ConnFailed()
		r.client.CloseTCP(serviceName)
		return false
	}

	_, err := r.dialer.DialTCP(r.ctx, address, conn, adp)
	if err != nil {
		if r.ctx.Err() != nil {
			stresslog.Debug("[ROBOT] TCP 连接建立已取消",
				zap.Int("id", r.id), zap.String("service", serviceName), zap.String("addr", address), zap.Error(err))
		} else {
			stresslog.Warn("[ROBOT] TCP 连接建立失败",
				zap.Int("id", r.id), zap.String("service", serviceName), zap.String("addr", address), zap.Error(err))
			monitor.Global().ConnFailed()
		}
		r.client.CloseTCP(serviceName)
		return false
	}

	monitor.Global().ConnEstablished()
	if err := (&netSenderAdapter{robot: r}).RegisterCodecHeartbeat("tcp", serviceName); err != nil {
		stresslog.Warn("[ROBOT] TCP 连接级心跳注册失败",
			zap.Int("id", r.id), zap.String("account", r.account), zap.String("service", serviceName), zap.Error(err))
	}

	// onClosed：主动/被动关闭都触发，仅用于监控 -1（与 ConnEstablished 配对，保证当前连接数准确）
	conn.SetOnClosed(func() {
		monitor.Global().ConnDropped()
	})
	// onDisconnect：仅意外断开触发，用于"主连接挂了就停 robot"业务判定
	conn.SetOnDisconnect(func() {
		if serviceName == r.mainService {
			stresslog.Warn("[ROBOT] 主连接意外断开，停止机器人",
				zap.Int("id", r.id), zap.String("account", r.account), zap.String("service", serviceName))
			r.Stop()
		} else {
			stresslog.Debug("[ROBOT] TCP 连接断开",
				zap.Int("id", r.id), zap.String("account", r.account), zap.String("service", serviceName))
		}
	})

	return true
}

// ConnectUDP 建立指定服务的 UDP 连接
func (r *Robot) ConnectUDP(serviceName, address string) bool {
	ok := r.client.ConnectUDP(serviceName)
	if !ok {
		return false
	}

	conn := r.client.GetUDPConn(serviceName)
	if conn == nil {
		return false
	}

	// 按连接的 server 串 "udp:<service>" 从 resolver 解析 codec；未映射 → fail loud（不静默回退默认 codec）。
	server := "udp:" + serviceName
	adp := r.resolver.Resolve(server)
	if adp == nil {
		stresslog.Error("[ROBOT] UDP 连接无 codec 配置（resolver 未映射），拨号中止",
			zap.Int("id", r.id), zap.String("account", r.account),
			zap.String("server", server), zap.String("service", serviceName), zap.String("address", address))
		monitor.Global().ConnFailed()
		r.client.CloseUDP(serviceName)
		return false
	}

	_, err := r.dialer.DialUDP(r.ctx, address, conn, adp)
	if err != nil {
		if r.ctx.Err() != nil {
			stresslog.Debug("[ROBOT] UDP 连接建立已取消",
				zap.Int("id", r.id), zap.String("service", serviceName), zap.String("addr", address), zap.Error(err))
		} else {
			stresslog.Warn("[ROBOT] UDP 拨号失败",
				zap.Int("id", r.id), zap.String("account", r.account),
				zap.String("service", serviceName), zap.String("address", address), zap.Error(err))
			monitor.Global().ConnFailed()
		}
		r.client.CloseUDP(serviceName)
		return false
	}

	monitor.Global().ConnEstablished()
	if err := (&netSenderAdapter{robot: r}).RegisterCodecHeartbeat("udp", serviceName); err != nil {
		stresslog.Warn("[ROBOT] UDP 连接级心跳注册失败",
			zap.Int("id", r.id), zap.String("account", r.account), zap.String("service", serviceName), zap.Error(err))
	}

	conn.SetOnClosed(func() {
		monitor.Global().ConnDropped()
	})
	conn.SetOnDisconnect(func() {
		stresslog.Debug("[ROBOT] UDP 连接断开",
			zap.Int("id", r.id), zap.String("account", r.account), zap.String("service", serviceName))
	})

	return true
}

// CloseTCP 关闭指定服务的 TCP 连接
func (r *Robot) CloseTCP(service string) {
	r.client.CloseTCP(service)
}

// CloseUDP 关闭指定服务的 UDP 连接
func (r *Robot) CloseUDP(service string) {
	r.client.CloseUDP(service)
}

// robotActionHandler 实现 engine.ActionHandler 接口，将流程引擎的动作委托给 Robot 执行。
type robotActionHandler struct {
	robot *Robot           // 关联的机器人实例
	flow  *engine.TaskFlow // 流程配置（用于查找回调定义）
}

// actionRunResult 是声明式 / Lua 动作内核返回给机器人层外壳的统一结果。
type actionRunResult struct {
	sendBytes int
	recvBytes int
	timing    engine.ActionTiming
	wallClock time.Duration
	err       error
}

// ExecuteAction 是动作执行的机器人层外壳：
//   - 统一声明式 / Lua 动作的 monitor start/record 配对。
//   - 统一 wallClock、bytes、ActionTiming、错误分类。
//   - 统一取消期副作用错误归一化。
//
// 真正的内层差异只保留在 runDeclarativeAction（Go executor）和 runLuaAction（Lua runtime）。
//
// 计时拆解原则：
//   - 声明式动作 wallClock 含 proto 构建 / 序列化 / 反序列化 / state 写入等全部开销。
//   - Lua 动作 wallClock 覆盖主流程同步执行脚本的总耗时。
//   - timing.Requests：每次 request-response 的独立 WireRTT 样本。
//   - clientCost 由 monitor 用 wallClock - sum(WireRTT) 计算。
func (h *robotActionHandler) ExecuteAction(ctx context.Context, actionDef *engine.ActionDef) error {
	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		mc.RecordActionStart(actionDef.Name)
	}

	result := h.runActionBody(ctx, actionDef)
	result.err = h.normalizeActionCancel(ctx, result.err)

	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		mc.RecordAction(actionDef.Name, classifyResult(result.err), toMonitorTiming(result.timing),
			result.wallClock, result.sendBytes, result.recvBytes, result.err)
	}

	return result.err
}

func (h *robotActionHandler) runActionBody(ctx context.Context, actionDef *engine.ActionDef) actionRunResult {
	if actionDef.Pattern == engine.PatternLua {
		return h.runLuaAction(actionDef)
	}
	return h.runDeclarativeAction(ctx, actionDef)
}

func (h *robotActionHandler) runDeclarativeAction(ctx context.Context, actionDef *engine.ActionDef) actionRunResult {
	start := time.Now()
	sendBytes, recvBytes, timing, err := h.robot.actionExec.Execute(ctx, actionDef)
	return actionRunResult{
		sendBytes: sendBytes,
		recvBytes: recvBytes,
		timing:    timing,
		wallClock: time.Since(start),
		err:       err,
	}
}

// normalizeActionCancel 统一修正任务取消期的"副作用错误"。
// stop 阶段，Lua 脚本（如 match_succeed.lua / connect_battle_tcp.lua）会因
// 底层 ctx 取消而让网络层返回 ErrActionCanceled，或脚本经 err table 重建出
// LISTEN_TIMEOUT/CONN_NOT_FOUND 等 ActionError；实际根因是 ACTION_CANCELED。
// 在这里统一矫正，避免 monitor 把 cancel 流量误归类为 Timeout/Failure，
// 也避免 executor 重复刷 error 日志。
func (h *robotActionHandler) normalizeActionCancel(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	canceled := ctx != nil && ctx.Err() != nil
	if !canceled && h.robot != nil && h.robot.ctx != nil {
		canceled = h.robot.ctx.Err() != nil
	}
	if !canceled {
		return err
	}

	if actionErr, ok := errors.AsType[*engine.ActionError](err); ok && !isCanceledCode(actionErr.Code) {
		return engine.NewActionError(errcode.ErrActionCanceled, "stopping: "+actionErr.Detail)
	}
	return err
}

// classifyResult 将 error 映射为 monitor ActionResult。
//
// 分类优先级（高到低）：
//  1. context.Canceled / context.DeadlineExceeded → Canceled
//     （任务停止或 robot.Close 直接命中 ctx，未经过网络层）
//  2. ActionError.Code == ErrActionCanceled → Canceled
//     （网络层等待响应时本地主动 Close 触发 ctx.Done，包成 ActionError 上抛）
//  3. ActionError.Code ∈ 超时码 → Timeout
//  4. 其它（含框架/服务端错误码）→ Failure
//
// 历史教训：早期未识别 ErrActionCanceled，导致任务停止时 inflight 的 BattleEnd /
// GameOver / RequestGameModeList 等动作被误归为 Failure，监控面板和历史详情里
// 堆出大量"假失败"，掩盖了真实的服务端业务错误。
func toMonitorTiming(t engine.ActionTiming) monitor.ActionTiming {
	out := monitor.ActionTiming{
		Client: monitor.ClientTiming{
			BuildCost:      t.Client.BuildCost,
			EncodeCost:     t.Client.EncodeCost,
			SendCost:       t.Client.SendCost,
			DecodeWait:     t.Client.DecodeWait,
			DecodeCost:     t.Client.DecodeCost,
			DispatchWait:   t.Client.DispatchWait,
			ParseStoreCost: t.Client.ParseStoreCost,
		},
	}
	if len(t.Requests) > 0 {
		out.Requests = make([]monitor.RequestTiming, 0, len(t.Requests))
		for _, req := range t.Requests {
			out.Requests = append(out.Requests, monitor.RequestTiming{
				SendCost:             req.SendCost,
				WireRTT:              req.WireRTT,
				DecodeWait:           req.DecodeWait,
				DecodeCost:           req.DecodeCost,
				DispatchToActionWait: req.DispatchToActionWait,
			})
		}
	}
	return out
}

func classifyResult(err error) monitor.ActionResult {
	if err == nil {
		return monitor.ResultSuccess
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return monitor.ResultCanceled
	}
	if actionErr, ok := errors.AsType[*engine.ActionError](err); ok {
		if isCanceledCode(actionErr.Code) {
			return monitor.ResultCanceled
		}
		if isTimeoutCode(actionErr.Code) {
			return monitor.ResultTimeout
		}
	}
	return monitor.ResultFailure
}

// isTimeoutCode 判断错误码是否为超时类（用于 classifyResult 分流 ResultTimeout）。
func isTimeoutCode(code errcode.ErrorCode) bool {
	return code == errcode.ErrRecvTimeout || code == errcode.ErrListenTimeout
}

// isCanceledCode 判断错误码是否表示"本地主动取消"（用于 classifyResult 分流 ResultCanceled）。
func isCanceledCode(code errcode.ErrorCode) bool {
	return code == errcode.ErrActionCanceled
}

// runLuaAction 执行 lua 脚本动作，并返回统一的 actionRunResult。
// timing 由脚本内的网络 API 累加；纯客户端逻辑（如仅 set_secret_key）timing 为零值。
// wallClock 覆盖主流程同步执行脚本的总耗时。
//
// err-table 契约：RunActionScript 返回 nil（脚本成功）/ *engine.ActionError（脚本 return
// err table 经 buildActionError 重建）/ 其它 error（脚本框架错误，如加载失败/await 非法）。
// - nil → 成功；
// - *engine.ActionError → 透传，补 action=<actionDef.Name> 上下文（若 detail 未含）；
// - 非 ActionError → 包装 ErrLuaExecFailed（detail 带 script=）。
func (h *robotActionHandler) runLuaAction(actionDef *engine.ActionDef) actionRunResult {
	if h.robot.l == nil || h.robot.luaPool == nil {
		stresslog.Error("[ROBOT] Lua 运行时未初始化，无法执行脚本",
			zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("script", actionDef.Script))
		return actionRunResult{err: engine.NewActionError(errcode.ErrLuaNotInit, "")}
	}

	if actionDef.Script == "" {
		stresslog.Error("[ROBOT] 脚本名为空，无法执行",
			zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("action", actionDef.Name))
		return actionRunResult{err: engine.NewActionError(errcode.ErrLuaNoScript, "")}
	}

	start := time.Now()

	// RunActionScript 内部通过 script.Context 累积每次 request 的独立 RequestTiming，
	// 这里把结构化 timing 上抛给 RecordAction。
	send, recv, timing, err := h.robot.luaPool.RunActionScript(h.robot.l, actionDef.Script)
	result := actionRunResult{
		sendBytes: send,
		recvBytes: recv,
		timing:    timing,
		wallClock: time.Since(start),
		err:       err,
	}
	if err == nil {
		return result
	}

	if actionErr, ok := errors.AsType[*engine.ActionError](err); ok {
		// 脚本 return err table 重建的 ActionError：透传，补 action= 上下文。
		actionErr.Detail = appendActionDetail(actionErr.Detail, actionDef.Name)
		result.err = actionErr
		return result
	}
	// 框架错误（脚本加载失败 / await 非法 / 返回非法值 等）：包装 ErrLuaExecFailed。
	result.sendBytes = 0
	result.recvBytes = 0
	result.err = engine.NewActionError(errcode.ErrLuaExecFailed, "script="+actionDef.Script, err)
	return result
}

// appendActionDetail 在 detail 末尾补 action=<name>（若 detail 未含 action=）。
// 便于前端/日志定位到具体 action 配置项，而非仅脚本名。
func appendActionDetail(detail, actionName string) string {
	if strings.Contains(detail, "action=") {
		return detail
	}
	if detail != "" {
		detail += " "
	}
	return detail + "action=" + actionName
}

// ExecuteBoolean 执行条件判断
func (h *robotActionHandler) ExecuteBoolean(expression string) bool {
	if strings.HasPrefix(expression, engine.PrefixLua) {
		return h.executeLuaBoolean(expression[len(engine.PrefixLua):])
	}

	return engine.EvalCondition(expression, h.robot.state)
}

// executeLuaBoolean 执行 Lua 条件脚本，脚本必须 return true/false。
func (h *robotActionHandler) executeLuaBoolean(scriptName string) bool {
	if h.robot.l == nil || h.robot.luaPool == nil {
		stresslog.Error("[ROBOT] Lua 运行时未初始化，条件判断默认拒绝",
			zap.Int("id", h.robot.id),
			zap.String("account", h.robot.account),
			zap.String("script", scriptName))
		return false
	}

	if !h.robot.luaPool.HasScript(scriptName) {
		stresslog.Error("[ROBOT] 条件脚本不存在，条件判断默认拒绝",
			zap.Int("id", h.robot.id),
			zap.String("account", h.robot.account),
			zap.String("script", scriptName))
		return false
	}

	result, err := h.robot.luaPool.RunBooleanScript(h.robot.l, scriptName)
	if err != nil {
		stresslog.Error("[ROBOT] 条件脚本执行失败，条件判断默认拒绝",
			zap.Int("id", h.robot.id),
			zap.String("account", h.robot.account),
			zap.String("script", scriptName), zap.Error(err))
		return false
	}

	return result
}

// CooperativeSleep 实现 engine.ActionHandler：节点延迟 / wait 节点的协作式休眠。
func (h *robotActionHandler) CooperativeSleep(ctx context.Context, d time.Duration) error {
	return h.robot.cooperativeSleep(ctx, d)
}

// cooperativeSleep 协作式休眠 d：复用调度器统一等待 pump（sched.wait，check==nil 即纯计时），
// 休眠期间持续 drain 任务队列。ctx 取消时返回 ctx.Err()。
//
// 三处共用：节点延迟 / wait 节点（engine.ActionHandler.CooperativeSleep）、声明式 listen 轮询
// 间隔（注入 ActionExecutor.coopSleep）—— 统一约束「任何阻塞点都不裸阻塞」。
func (r *Robot) cooperativeSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	if r.sched.wait(time.Now().Add(d), 0, nil).Canceled {
		return ctx.Err()
	}
	return nil
}

// RegisterListen 注册持久化监听
func (h *robotActionHandler) RegisterListen(refs []engine.ListenRef) error {
	type connKey struct {
		proto   string
		service string
	}
	// 每个 (proto, service) 分组收集 (routeKey, cb, queueSize) 三元组，注册阶段逐条下发给 Connection。
	type entry struct {
		routeKey  string
		cb        network.ListenCallBack
		queueSize int
	}
	groups := make(map[connKey][]entry)

	for _, ref := range refs {
		// 有效 queueSize 计算：缺省 1、显式 <=0 报错（fail loud，不静默 clamp）。
		queueSize, err := effectiveListenQueueSize(ref)
		if err != nil {
			return err
		}

		proto, service, ok := parseServer(ref.Server)
		if !ok {
			stresslog.Warn("[ROBOT] 监听引用的 server 解析失败，跳过注册",
				zap.Int("id", h.robot.id),
				zap.String("account", h.robot.account),
				zap.String("server", ref.Server), zap.String("listen", ref.Listen))
			continue
		}
		key := connKey{proto: proto, service: service}

		// T2-C2：listen routeKey 按 "<proto>:<service>" Resolve 出该连接的 Go SchemaAdapter
		// 后计算（server=ref.Server 即 proto:service）。Resolve nil → fail loud（中文 error，
		// 带上下文），不静默兜底——与 dial/decode 侧的 fail-loud 一致（缺 codec 配置即报错）。
		serverAdp := h.robot.resolver.Resolve(ref.Server)
		if serverAdp == nil {
			return fmt.Errorf("监听注册失败：server=%q routeKey 解析失败，连接未配置 codec（resolver.Resolve(%q) nil）",
				ref.Server, ref.Server)
		}
		routeKey := serverAdp.ExpectedRouteKey(ref.Route)

		var cb network.ListenCallBack
		if ref.Listen == "" {
			cb = nil
		} else {
			cbDef, ok := h.flow.Listen(ref.Listen)
			if !ok {
				stresslog.Error("[ROBOT] 回调定义不存在",
					zap.Int("id", h.robot.id),
					zap.String("account", h.robot.account),
					zap.String("server", ref.Server),
					zap.String("listen", ref.Listen))
				continue
			}
			if err := validateListenDef(ref.Listen, cbDef); err != nil {
				return err
			}
			cb = h.createListenCallback(ref.Listen, cbDef)
		}
		groups[key] = append(groups[key], entry{routeKey: routeKey, cb: cb, queueSize: queueSize})
	}

	for key, entries := range groups {
		var conn *network.Connection
		switch key.proto {
		case "udp":
			conn = h.robot.client.GetUDPConn(key.service)
		case "tcp":
			conn = h.robot.client.GetTCPConn(key.service)
		}
		if conn == nil {
			stresslog.Debug("[ROBOT] 无连接可注册监听",
				zap.Int("id", h.robot.id),
				zap.String("account", h.robot.account),
				zap.String("proto", key.proto), zap.String("service", key.service))
			continue
		}
		// 逐条注册：每条 RegisterListen 预创建队列 + 冲突 fail-loud + 幂等。
		for _, e := range entries {
			if err := conn.RegisterListen(e.routeKey, e.cb, e.queueSize); err != nil {
				return fmt.Errorf("监听注册失败：proto=%s service=%q routeKey=%q：%w",
					key.proto, key.service, e.routeKey, err)
			}
		}
	}

	return nil
}

// effectiveListenQueueSize 计算 ListenRef.QueueSize 的有效队列容量。
//
// 语义：
//   - QueueSize == nil（未写）→ 默认 1（与历史单槽逐字节等价）；
//   - QueueSize 显式 > 0 → 取该值；
//   - QueueSize 显式 <= 0 → 配置错误，返回带 server+listen 上下文的中文 error。
//
// 抽成纯函数便于单测；由 RegisterListen 在注册阶段（运行时）调用。
// 这里不做静默 clamp（遵循「禁止兼容性兜底」），显式 0/负数一律报错暴露配置错误。
func effectiveListenQueueSize(ref engine.ListenRef) (int, error) {
	if ref.QueueSize == nil {
		return defaultListenQueueSize, nil
	}
	q := *ref.QueueSize
	if q <= 0 {
		return 0, fmt.Errorf("监听注册失败：server=%q listen=%q 的 queueSize=%d 非法，须 >= 1",
			ref.Server, ref.Listen, q)
	}
	return q, nil
}

// validateListenDef 校验 ListenDef 的形态约束。
//
// 协作式调度下 listen 脚本回调（cbDef.Script）已恢复为正式能力：connectionPump 命中推送时
// 只复制消息 + 投递 Robot 任务队列，Lua on_message 由执行器 goroutine 在节点边界串行执行，
// 不再在 pump goroutine 内触碰业务 LState（消除旧版的跨 goroutine LState 抢占）。
//
// 唯一约束：store 与 script 互斥——同一条推送不允许既走 Go 声明式 store、又走 Lua handler
// 改写 state，避免双写与顺序语义混乱。需要兼得时把 store 逻辑写进 Lua handler。
// 抽成纯函数便于单测（不依赖 robot/network 状态）。
func validateListenDef(listenName string, cbDef *engine.ListenDef) error {
	if cbDef == nil {
		return nil
	}
	if cbDef.Script != "" && len(cbDef.Store) > 0 {
		return fmt.Errorf("监听 %q 同时配置了 store 与 script：二者互斥（同一推送不可既 Go-store 又 Lua 改 state）；"+
			"需要兼得请把 store 逻辑写进 Lua handler", listenName)
	}
	return nil
}

// parseServer 解析 "tcp:logic" 或 "udp:udp" 格式的 server 字段。
func parseServer(server string) (proto, service string, ok bool) {
	if server == "" {
		stresslog.Warn("[ROBOT] 监听引用缺少 server 字段")
		return "", "", false
	}
	parts := strings.SplitN(server, ":", 2)
	if len(parts) != 2 || (parts[0] != "tcp" && parts[0] != "udp") {
		stresslog.Error("[ROBOT] server 字段格式错误，需为 tcp:xxx 或 udp:xxx", zap.String("server", server))
		return "", "", false
	}
	return parts[0], parts[1], true
}

// createListenCallback 根据回调定义创建监听回调函数。
//
// 三种形态：
//   - cbDef.Script 配置：协作式 listen 脚本回调。返回的闭包在 pump goroutine 内只复制消息
//   - 投递 Robot 任务队列；Lua on_message 由执行器 goroutine 在节点边界串行执行（见
//     runListenScript），绝不在 pump 内碰业务 LState。
//   - cbDef.S2CProto 与 cbDef.Store 均配置：返回 Go 闭包，按 proto 解析后写 state（纯 Go，
//     pump goroutine 内安全执行）。
//   - 否则（纯缓存 listen，如 frameData）：返回 nil，消息仅入 listen queue 供主流程消费。
//
// store 与 script 互斥由 validateListenDef 在注册前 fail-loud 保证，故此处分支无歧义。
func (h *robotActionHandler) createListenCallback(cbName string, cbDef *engine.ListenDef) network.ListenCallBack {
	if cbDef.Script != "" {
		return func(msg *network.Message) {
			if h.robot.ctx.Err() != nil {
				return
			}
			// pump 可能复用 msg.Data 底层数组，入队前深拷贝快照；on_message 推迟执行。
			snapshot := msg.Copy()
			h.robot.sched.enqueue(pendingTask{
				name: cbName,
				exec: func() { h.runListenScript(cbName, cbDef, snapshot) },
			})
		}
	}

	if cbDef.S2CProto == "" || len(cbDef.Store) == 0 {
		return nil
	}

	return func(msg *network.Message) {
		if h.robot.ctx.Err() != nil {
			stresslog.Debug("[ROBOT] 停止阶段跳过状态回调",
				zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("callback", cbName))
			return
		}
		start := time.Now()
		if len(msg.Data) == 0 {
			monitor.Global().RecordCallback(cbName, monitor.ResultSuccess, monitor.ActionTiming{}, time.Since(start), 0, msg.WireBytes, nil)
			return
		}

		respMsg, err := h.robot.factory.Parse(cbDef.S2CProto, msg.Data)
		if err != nil {
			callbackErr := engine.NewActionError(errcode.ErrCallbackParse, "proto="+cbDef.S2CProto, err)
			stresslog.Error("[ROBOT] 解析推送消息失败",
				zap.Int("id", h.robot.id),
				zap.String("account", h.robot.account),
				zap.String("callback", cbName),
				zap.String("proto", cbDef.S2CProto),
				zap.String("routeKey", msg.RouteKey),
				zap.Int("bodyLen", len(msg.Data)),
				zap.Int("wireBytes", msg.WireBytes),
				zap.Uint64("headerErr", msg.HeaderErr),
				zap.Error(err))
			monitor.Global().RecordCallback(cbName, monitor.ResultFailure, monitor.ActionTiming{}, time.Since(start), 0, msg.WireBytes, callbackErr)
			return
		}

		fieldMap := h.robot.factory.GetFieldMap(respMsg)
		for _, m := range cbDef.Store {
			if m.Field == "" {
				h.robot.state.SetPath(m.Setter, fieldMap)
			} else {
				val := state.NavigatePath(fieldMap, m.Field)
				if val != nil {
					h.robot.state.SetPath(m.Setter, val)
				}
			}
		}
		monitor.Global().RecordCallback(cbName, monitor.ResultSuccess, monitor.ActionTiming{}, time.Since(start), 0, msg.WireBytes, nil)
	}
}

// runListenScript 在执行器 goroutine（业务 LState 唯一所有者）内执行一条 listen 脚本回调。
// 在等待窗口的 select 内就地串行调用（由 listen 回调的 exec 闭包触发），因此可安全访问 r.l。
//
// 解码失败 / 脚本失败均记 callback 失败指标，不向上传播——listen 回调是旁路推送，
// 失败不应中断主流程。配置了 s2cProto 时解码后以字段表传给 on_message(r, msg)，
// 否则传 nil（脚本可自行用 proto API 处理原始消息）。
func (h *robotActionHandler) runListenScript(cbName string, cbDef *engine.ListenDef, msg *network.Message) {
	if h.robot.ctx.Err() != nil {
		return
	}
	start := time.Now()

	var runErr error
	if cbDef.S2CProto != "" && len(msg.Data) > 0 {
		respMsg, perr := h.robot.factory.Parse(cbDef.S2CProto, msg.Data)
		if perr != nil {
			cbErr := engine.NewActionError(errcode.ErrCallbackParse, "proto="+cbDef.S2CProto, perr)
			stresslog.Error("[ROBOT] 解析监听推送失败",
				zap.Int("id", h.robot.id),
				zap.String("account", h.robot.account),
				zap.String("listen", cbName),
				zap.String("script", cbDef.Script),
				zap.String("proto", cbDef.S2CProto),
				zap.String("routeKey", msg.RouteKey),
				zap.Int("bodyLen", len(msg.Data)),
				zap.Int("wireBytes", msg.WireBytes),
				zap.Uint64("headerErr", msg.HeaderErr),
				zap.Error(perr))
			monitor.Global().RecordCallback(cbName, monitor.ResultFailure, monitor.ActionTiming{}, time.Since(start), 0, msg.WireBytes, cbErr)
			return
		}
		runErr = h.robot.luaPool.RunListenScript(h.robot.l, cbDef.Script, respMsg)
	} else {
		runErr = h.robot.luaPool.RunListenScript(h.robot.l, cbDef.Script, nil)
	}

	if runErr != nil {
		cbErr := engine.NewActionError(errcode.ErrCallbackLua, "script="+cbDef.Script, runErr)
		stresslog.Error("[ROBOT] 监听脚本执行失败",
			zap.Int("id", h.robot.id),
			zap.String("account", h.robot.account),
			zap.String("listen", cbName),
			zap.String("script", cbDef.Script),
			zap.String("proto", cbDef.S2CProto),
			zap.String("routeKey", msg.RouteKey),
			zap.Int("bodyLen", len(msg.Data)),
			zap.Int("wireBytes", msg.WireBytes),
			zap.Uint64("headerErr", msg.HeaderErr),
			zap.Error(runErr))
		monitor.Global().RecordCallback(cbName, monitor.ResultFailure, monitor.ActionTiming{}, time.Since(start), 0, msg.WireBytes, cbErr)
		return
	}
	monitor.Global().RecordCallback(cbName, monitor.ResultSuccess, monitor.ActionTiming{}, time.Since(start), 0, msg.WireBytes, nil)
}

// netSenderAdapter 将 Robot 适配为 engine.NetSender 接口，
// 桥接流程引擎与网络层（TCP/UDP/HTTP 收发、连接管理、心跳、密钥）。
type netSenderAdapter struct {
	robot *Robot // 关联的机器人实例
}

// TCPSend 通过 TCP 发送数据包。
func (ns *netSenderAdapter) TCPSend(service string, packet []byte) (int, error) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return 0, engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
	}
	return conn.Send(packet)
}

// TCPRequest 发送 TCP 请求并协作式等待响应（声明式 tcpRequest 动作用）。
// 经 sched.awaitResponse 与 Lua await_tcp_request 同源：发送 + 等待窗口内 drain mailbox，
// 故声明式请求期间 listen 回调不被饿死。返回 SendWireBytes=已编码请求包长，RecvWireBytes=入站帧长。
//
// 唯一调用方是 engine.ActionExecutor.execRequest（执行器 goroutine = 业务 LState 唯一所有者），
// 故在此 drain mailbox 安全。
func (ns *netSenderAdapter) TCPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) (*engine.NetExchange, error) {
	return ns.cooperativeRequest("tcp", service, packet, routeKey, timeout...)
}

// UDPRequest 发送 UDP 请求并协作式等待响应（与 TCPRequest 同源，经 sched.awaitResponse）。
func (ns *netSenderAdapter) UDPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) (*engine.NetExchange, error) {
	return ns.cooperativeRequest("udp", service, packet, routeKey, timeout...)
}

// cooperativeRequest 声明式请求-响应的协作式实现：构建 WaitSpec → sched.awaitResponse
// （发送 + select 响应通道 + drain mailbox）。与 Lua await_*_request 完全同一路径，保证
// 声明式与脚本两条请求路径行为/计时一致。命中 / 超时 / 取消 / 发送失败经 WaitOutcome 映射回
// (*NetExchange, error)，与旧 RequestResponse 返回契约对齐（execRequest 下游 HeaderErr/parse 不变）。
func (ns *netSenderAdapter) cooperativeRequest(proto, service string, packet []byte, routeKey string, timeout ...time.Duration) (*engine.NetExchange, error) {
	var d time.Duration
	if len(timeout) > 0 {
		d = timeout[0]
	}
	outcome := ns.robot.sched.awaitResponse(&script.WaitSpec{
		Kind:     script.WaitResponse,
		Proto:    proto,
		Service:  service,
		RouteKey: routeKey,
		Packet:   packet,
		Duration: d,
	})
	switch {
	case outcome.Err != nil:
		ex := outcome.Exchange
		if ex == nil {
			ex = &engine.NetExchange{SendWireBytes: len(packet)}
		}
		return ex, outcome.Err
	case outcome.Canceled:
		return &engine.NetExchange{SendWireBytes: len(packet)},
			engine.NewActionError(errcode.ErrActionCanceled, "service="+service+" route="+routeKey)
	default:
		if stresslog.DebugEnabled() && outcome.Exchange != nil {
			stresslog.Debug("[ACTION] 协作请求收到响应",
				zap.String("transport", proto),
				zap.String("service", service), zap.String("routeKey", routeKey),
				zap.Int("bodyLen", len(outcome.Exchange.Body)),
				zap.Int("wireBytes", outcome.Exchange.RecvWireBytes),
				zap.Uint64("headerErr", outcome.Exchange.HeaderErr))
		}
		return outcome.Exchange, nil
	}
}

// HTTPRequest 发送 HTTP 请求。
//
// NetLatency 覆盖：http.Client.Do 调用 + 读完 response.Body。
// HTTP WireBytes 统计 HTTP message bytes，不含 TCP/IP/TLS record 开销。
func (ns *netSenderAdapter) HTTPRequest(reqURL, method, contentType string, body []byte) (*engine.HTTPExchange, error) {
	exchange := &engine.HTTPExchange{}
	if reqURL == "" {
		return exchange, engine.NewActionError(errcode.ErrURLEmpty, "")
	}
	if !strings.HasPrefix(reqURL, "http://") && !strings.HasPrefix(reqURL, "https://") {
		return exchange, engine.NewActionError(errcode.ErrURLScheme, "url="+reqURL)
	}

	var req *http.Request
	var err error

	if len(body) > 0 {
		switch contentType {
		case "json":
			req, err = http.NewRequestWithContext(ns.robot.ctx, method, reqURL, bytes.NewReader(body))
			if err != nil {
				return exchange, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
			}
			req.Header.Set("Content-Type", "application/json")
		case "form":
			values := make(url.Values)
			if json.Unmarshal(body, &values) == nil {
				body = []byte(values.Encode())
			}
			req, err = http.NewRequestWithContext(ns.robot.ctx, method, reqURL, strings.NewReader(string(body)))
			if err != nil {
				return exchange, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		default:
			req, err = http.NewRequestWithContext(ns.robot.ctx, method, reqURL, bytes.NewReader(body))
			if err != nil {
				return exchange, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
			}
		}
	} else {
		req, err = http.NewRequestWithContext(ns.robot.ctx, method, reqURL, nil)
		if err != nil {
			return exchange, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
		}
	}
	exchange.SendWireBytes = httpRequestBytes(req, body)

	netStart := time.Now()
	resp, err := ns.robot.httpClient.Do(req)
	if err != nil {
		exchange.NetLatency = time.Since(netStart)
		if ns.robot.ctx.Err() != nil {
			stresslog.Debug("[HTTP] 请求已取消", zap.String("url", reqURL), zap.Error(err))
			return exchange, engine.NewActionError(errcode.ErrActionCanceled, "url="+reqURL, err)
		}
		stresslog.Warn("[HTTP] 请求失败", zap.String("url", reqURL), zap.Error(err))
		return exchange, engine.NewActionError(errcode.ErrSendFailed, "url="+reqURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	exchange.NetLatency = time.Since(netStart)
	exchange.StatusCode = resp.StatusCode
	exchange.Body = respBody
	exchange.RecvWireBytes = httpResponseBytes(resp, respBody)
	if err != nil {
		return exchange, engine.NewActionError(errcode.ErrHTTPReadBody, "url="+reqURL, err)
	}

	return exchange, nil
}

func httpRequestBytes(req *http.Request, body []byte) int {
	if req == nil {
		return 0
	}
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	total := len(req.Method) + 1 + len(path) + 1 + len("HTTP/1.1") + 2
	if req.Host != "" {
		total += len("Host") + 2 + len(req.Host) + 2
	} else if req.URL.Host != "" {
		total += len("Host") + 2 + len(req.URL.Host) + 2
	}
	total += httpHeaderBytes(req.Header)
	if len(body) > 0 {
		total += len("Content-Length") + 2 + len(fmt.Sprintf("%d", len(body))) + 2
	}
	total += 2 + len(body)
	return total
}

func httpResponseBytes(resp *http.Response, body []byte) int {
	if resp == nil {
		return 0
	}
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d", resp.StatusCode)
	}
	total := len("HTTP/1.1") + 1 + len(status) + 2
	total += httpHeaderBytes(resp.Header)
	if resp.ContentLength >= 0 && resp.Header.Get("Content-Length") == "" {
		total += len("Content-Length") + 2 + len(fmt.Sprintf("%d", resp.ContentLength)) + 2
	}
	total += 2 + len(body)
	return total
}

func httpHeaderBytes(header http.Header) int {
	total := 0
	for k, values := range header {
		for _, value := range values {
			total += len(k) + 2 + len(value) + 2
		}
	}
	return total
}

// UDPSend 通过 UDP 发送数据包。
func (ns *netSenderAdapter) UDPSend(service string, data []byte) (int, error) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return 0, engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
	}
	return conn.Send(data)
}

// ConnectTCP 通过适配器建立 TCP 连接。
func (ns *netSenderAdapter) ConnectTCP(service, address string) error {
	ok := ns.robot.ConnectTCP(service, address)
	if !ok {
		if ns.robot.ctx.Err() != nil {
			return engine.NewActionError(errcode.ErrActionCanceled, "service="+service+" address="+address)
		}
		return engine.NewActionError(errcode.ErrConnClosed, "service="+service+" address="+address)
	}
	return nil
}

// ConnectUDP 通过适配器建立 UDP 连接。
func (ns *netSenderAdapter) ConnectUDP(service, address string) error {
	ok := ns.robot.ConnectUDP(service, address)
	if !ok {
		if ns.robot.ctx.Err() != nil {
			return engine.NewActionError(errcode.ErrActionCanceled, "service="+service+" address="+address)
		}
		return engine.NewActionError(errcode.ErrConnClosed, "service="+service+" address="+address)
	}
	return nil
}

// GetTCPListenResp 获取 TCP 连接的监听响应数据。
func (ns *netSenderAdapter) GetTCPListenResp(service string, routeKey string) *engine.NetExchange {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil
	}
	msg := conn.GetListenResp(routeKey)
	if msg == nil {
		return nil
	}
	return &engine.NetExchange{Body: msg.Data, HeaderErr: msg.HeaderErr, RecvWireBytes: msg.WireBytes}
}

// GetUDPListenResp 获取 UDP 连接的监听响应数据。
func (ns *netSenderAdapter) GetUDPListenResp(service string, routeKey string) *engine.NetExchange {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil
	}
	msg := conn.GetListenResp(routeKey)
	if msg == nil {
		return nil
	}
	return &engine.NetExchange{Body: msg.Data, HeaderErr: msg.HeaderErr, RecvWireBytes: msg.WireBytes}
}

// GetTCPSecretKey 获取 TCP 连接的加密密钥。
func (ns *netSenderAdapter) GetTCPSecretKey(service string) []byte {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil
	}
	return conn.GetSecretKey()
}

// SetTCPSecretKey 设置 TCP 连接的加密密钥。
func (ns *netSenderAdapter) SetTCPSecretKey(service string, key []byte) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn != nil {
		conn.SetSecretKey(key)
		ns.startPendingHeartbeatAfterSecretKey("tcp", service)
	}
}

// EnsureTCPListener 为 TCP 连接注册监听器占位（callback=nil，缓存模式）。
// 由 Lua ensure_tcp_listener 调用；queueSize 由调用方传入（Lua 固定 1，flow listenRefs 由 robot.RegisterListen 走另一条路径）。
// 冲突时记 Error 日志暴露配置异常，不向上 panic（ensure 路径本就是占位注册）。
func (ns *netSenderAdapter) EnsureTCPListener(service string, routeKey string, queueSize int) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return
	}
	if err := conn.RegisterListen(routeKey, nil, queueSize); err != nil {
		stresslog.Error("[ROBOT] TCP 监听占位注册失败",
			zap.String("service", service), zap.String("routeKey", routeKey), zap.Int("queueSize", queueSize), zap.Error(err))
	}
}

// EnsureUDPListener 为 UDP 连接注册监听器占位（callback=nil，缓存模式）。
// 由 Lua ensure_udp_listener 调用；queueSize 由调用方传入（Lua 固定 1，flow listenRefs 由 robot.RegisterListen 走另一条路径）。
// 冲突时记 Error 日志暴露配置异常，不向上 panic（ensure 路径本就是占位注册）。
func (ns *netSenderAdapter) EnsureUDPListener(service string, routeKey string, queueSize int) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return
	}
	if err := conn.RegisterListen(routeKey, nil, queueSize); err != nil {
		stresslog.Error("[ROBOT] UDP 监听占位注册失败",
			zap.String("service", service), zap.String("routeKey", routeKey), zap.Int("queueSize", queueSize), zap.Error(err))
	}
}

// CloseTCP 关闭 TCP 连接。
func (ns *netSenderAdapter) CloseTCP(service string) {
	ns.robot.CloseTCP(service)
}

// CloseUDP 关闭 UDP 连接。
func (ns *netSenderAdapter) CloseUDP(service string) {
	ns.robot.CloseUDP(service)
}

// SetUDPSecretKey 设置 UDP 连接的加密密钥。
func (ns *netSenderAdapter) SetUDPSecretKey(service string, key []byte) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn != nil {
		conn.SetSecretKey(key)
		ns.startPendingHeartbeatAfterSecretKey("udp", service)
	}
}

// GetUDPSecretKey 获取 UDP 连接的加密密钥。
func (ns *netSenderAdapter) GetUDPSecretKey(service string) []byte {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil
	}
	return conn.GetSecretKey()
}

// RegisterCodecHeartbeat 按 codec 连接配置注册可选心跳。
func (ns *netSenderAdapter) RegisterCodecHeartbeat(transport, service string) error {
	if ns.robot == nil || ns.robot.resolver == nil {
		return nil
	}
	hb := adapter.Heartbeat(ns.robot.resolver, transport+":"+service)
	if hb == nil {
		return nil
	}
	cfg := engine.HeartbeatConfig{
		Transport:        transport,
		Service:          service,
		IntervalMs:       hb.IntervalMs,
		Route:            hb.Route,
		C2SProto:         hb.C2SProto,
		Bindings:         codecFieldBindsToEngine(hb.Bindings),
		Fields:           codecHeartbeatFieldsToEngine(hb.HeartbeatFields),
		SkipWhenMissing:  hb.SkipWhenMissing,
		RequireSecretKey: hb.RequireSecretKey,
	}
	if cfg.RequireSecretKey {
		ns.robot.deferHeartbeatUntilSecretKey(cfg)
		return nil
	}
	return ns.installHeartbeat(cfg)
}

func heartbeatKey(transport, service string) string {
	return transport + ":" + service
}

func (r *Robot) deferHeartbeatUntilSecretKey(cfg engine.HeartbeatConfig) {
	r.pendingHeartbeatMu.Lock()
	if r.pendingHeartbeats == nil {
		r.pendingHeartbeats = make(map[string]engine.HeartbeatConfig)
	}
	r.pendingHeartbeats[heartbeatKey(cfg.Transport, cfg.Service)] = cfg
	r.pendingHeartbeatMu.Unlock()
	stresslog.Debug("[ROBOT] 连接级心跳等待密钥后启动",
		zap.String("transport", cfg.Transport),
		zap.String("service", cfg.Service),
		zap.Int("intervalMs", cfg.IntervalMs))
}

func (ns *netSenderAdapter) startPendingHeartbeatAfterSecretKey(transport, service string) {
	if ns.robot == nil {
		return
	}
	key := heartbeatKey(transport, service)
	ns.robot.pendingHeartbeatMu.Lock()
	cfg, ok := ns.robot.pendingHeartbeats[key]
	if ok {
		delete(ns.robot.pendingHeartbeats, key)
	}
	ns.robot.pendingHeartbeatMu.Unlock()
	if ok {
		if err := ns.installHeartbeat(cfg); err != nil {
			stresslog.Warn("[ROBOT] 密钥就绪后启动连接级心跳失败",
				zap.String("transport", transport), zap.String("service", service), zap.Error(err))
		}
	}
}

// installHeartbeat 安装连接级心跳（双模式 body 构造）。
//
// 心跳 body 不由 Lua 构造，而是由 Go builder 闭包按配置模式分派：
//   - proto 模式（C2SProto != ""）：engine.BuildProtoBody（factory + bindings，Go-only）；
//   - raw-binary 模式（len(Fields) > 0）：engine.BuildHeartbeatBody（小端打包 + 私有计数器/时间/随机）；
//   - 空 body（两者皆无）：静态心跳。
//
// body 构造完成后走 resolver.Resolve("<transport>:<service>") 解析出的 Go SchemaAdapter 做 encode
// 因此 Go builder / pump 心跳路径不触碰业务 LState。
//
// 闭包内每次 tick：
//  1. 按模式分派构造 body（skip=true 跳过本 tick 返回 nil；err 记 Warn 返回 nil）；
//  2. 取 conn 当前 secretKey；
//  3. adp.EncodeTCP/UDP(route, body, key) → packet；
//  4. 仅 raw-binary 模式递增 privateCounters（counter 源按 Step 推进；proto 模式无私有计数器）。
func (ns *netSenderAdapter) installHeartbeat(cfg engine.HeartbeatConfig) error {
	if ns.robot.ctx.Err() != nil {
		return engine.NewActionError(errcode.ErrActionCanceled, "service="+cfg.Service)
	}
	var conn *network.Connection
	if cfg.Transport == "udp" {
		conn = ns.robot.client.GetUDPConn(cfg.Service)
	} else {
		conn = ns.robot.client.GetTCPConn(cfg.Service)
	}
	if conn == nil {
		stresslog.Warn("[ROBOT] 连接级心跳安装失败：连接不存在",
			zap.String("transport", cfg.Transport), zap.String("service", cfg.Service))
		return engine.NewActionError(errcode.ErrConnNotFound,
			"transport="+cfg.Transport+" service="+cfg.Service)
	}

	// 私有计数器初值：按字段下标 + Start（缺省 0）初始化（raw-binary 模式专用）。
	privateCounters := make(map[int]int64, len(cfg.Fields))
	for i, f := range cfg.Fields {
		if f.Source == engine.HeartbeatSourceCounter {
			if f.Start != nil {
				privateCounters[i] = *f.Start
			}
		}
	}

	// 闭包持有：双模式入参、state、私有计数器、resolver/factory/route、transport、conn（取 secretKey）、skip 标志。
	// 字段布局 / bindings 拷贝一份避免共享 cfg 切片头被外部修改（ActionDef 生命周期内不可变，防御性拷贝）。
	//
	// T2-C2 起 encode 走 resolver：每 tick 按 cfg.Transport+":"+cfg.Service Resolve 出该连接的 Go
	// SchemaAdapter 后 Encode。Resolve nil（codec 未映射）→ Warn+skip 本 tick（不 fail 流程，
	// 单 tick encode 失败不应终止整条心跳 goroutine。
	fields := append([]engine.HeartbeatField(nil), cfg.Fields...)
	bindings := append([]engine.FieldBind(nil), cfg.Bindings...)
	c2sProto := cfg.C2SProto
	st := ns.robot.state
	resolver := ns.robot.resolver
	factory := ns.robot.factory
	route := cfg.Route
	skipWhenMissing := cfg.SkipWhenMissing
	transport := cfg.Transport

	goBuilder := func() []byte {
		// 双模式 body 分派（与 execHeartbeat 互斥校验一致：c2sProto 与 fields 不会同时非空）：
		//   proto 模式 → BuildProtoBody（factory + bindings，Go-only）；
		//   raw-binary 模式 → BuildHeartbeatBody（小端打包 + 私有计数器成功后递增）；
		//   两者皆无 → 空 body（静态心跳）。
		var body []byte
		var skip bool
		if c2sProto != "" {
			b, skipB, err := engine.BuildProtoBody(c2sProto, bindings, st, factory, "heartbeat:"+cfg.Service)
			if err != nil {
				stresslog.Warn("[ROBOT] 心跳 proto body 构建失败",
					zap.String("transport", transport),
					zap.String("service", cfg.Service),
					zap.String("c2sProto", c2sProto),
					zap.Error(err))
				return nil
			}
			body, skip = b, skipB
		} else if len(fields) > 0 {
			b, skipB, err := engine.BuildHeartbeatBody(fields, st, privateCounters, skipWhenMissing)
			if err != nil {
				stresslog.Warn("[ROBOT] 心跳 body 构建失败",
					zap.String("transport", transport),
					zap.String("service", cfg.Service),
					zap.Error(err))
				return nil
			}
			body, skip = b, skipB
		}
		// else: 空 body（静态心跳，body=nil）
		if skip {
			return nil
		}
		key := conn.GetSecretKey()
		// T2-C2：按 "<transport>:<service>" Resolve 出该连接的 Go SchemaAdapter 后 encode。
		// 单 tick encode 失败不应终止整条心跳 goroutine。
		adp := resolver.Resolve(transport + ":" + cfg.Service)
		if adp == nil {
			stresslog.Warn("[ROBOT] 心跳 encode 失败：codec 未映射（resolver nil），跳过本 tick",
				zap.String("transport", transport),
				zap.String("service", cfg.Service))
			return nil
		}
		var packet []byte
		if transport == "udp" {
			packet = adp.EncodeUDP(route, body, key)
		} else {
			packet = adp.EncodeTCP(route, body, key)
		}
		// 构建成功 → 递增私有计数器（仅 raw-binary 模式的 counter 源按 Step 推进；proto 模式无计数器概念）。
		for i := range fields {
			f := &fields[i]
			if f.Source == engine.HeartbeatSourceCounter {
				step := int64(1)
				if f.Step != nil {
					step = *f.Step
				}
				privateCounters[i] += step
			}
		}
		return packet
	}

	conn.RegisterHeartbeat(network.HeartbeatConfig{
		Interval: time.Duration(cfg.IntervalMs) * time.Millisecond,
		Builder:  goBuilder,
	})
	stresslog.Debug("[ROBOT] 连接级心跳已安装",
		zap.String("transport", cfg.Transport),
		zap.String("service", cfg.Service),
		zap.Int("intervalMs", cfg.IntervalMs),
		zap.Int("fieldCount", len(cfg.Fields)))
	return nil
}

func codecFieldBindsToEngine(in []codec.FieldBind) []engine.FieldBind {
	if len(in) == 0 {
		return nil
	}
	out := make([]engine.FieldBind, len(in))
	for i := range in {
		out[i] = engine.FieldBind{
			Field:         in[i].Field,
			Type:          in[i].Type,
			Value:         in[i].Value,
			Source:        in[i].Source,
			Path:          in[i].Path,
			Values:        append([]any(nil), in[i].Values...),
			Entries:       codecMapEntryBindsToEngine(in[i].Entries),
			Required:      in[i].Required,
			Filters:       codecFilterDefsToEngine(in[i].Filters),
			Min:           in[i].Min,
			Max:           in[i].Max,
			Precision:     in[i].Precision,
			Length:        in[i].Length,
			Count:         in[i].Count,
			Charset:       in[i].Charset,
			ExcludeSource: in[i].ExcludeSource,
			Optional:      in[i].Optional,
			Wrap:          in[i].Wrap,
			StoreAs:       in[i].StoreAs,
			KeySource:     in[i].KeySource,
			Condition:     in[i].Condition,
		}
	}
	return out
}

func codecMapEntryBindsToEngine(in []codec.MapEntryBind) []engine.MapEntryBind {
	if len(in) == 0 {
		return nil
	}
	out := make([]engine.MapEntryBind, len(in))
	for i := range in {
		out[i] = engine.MapEntryBind{Key: in[i].Key}
		converted := codecFieldBindsToEngine([]codec.FieldBind{in[i].Value})
		out[i].Value = converted[0]
	}
	return out
}

func codecFilterDefsToEngine(in []codec.FilterDef) []engine.FilterDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]engine.FilterDef, len(in))
	for i := range in {
		out[i] = engine.FilterDef{
			Path:   in[i].Path,
			Op:     in[i].Op,
			Value:  in[i].Value,
			Source: in[i].Source,
			Mode:   in[i].Mode,
		}
	}
	return out
}

func codecHeartbeatFieldsToEngine(in []codec.HeartbeatField) []engine.HeartbeatField {
	if len(in) == 0 {
		return nil
	}
	out := make([]engine.HeartbeatField, len(in))
	for i := range in {
		out[i] = engine.HeartbeatField{
			Type:       in[i].Type,
			Source:     in[i].Source,
			Value:      cloneInt64Ptr(in[i].Value),
			FloatValue: cloneFloat64Ptr(in[i].FloatValue),
			Key:        in[i].Key,
			Min:        cloneInt64Ptr(in[i].Min),
			Max:        cloneInt64Ptr(in[i].Max),
			Start:      cloneInt64Ptr(in[i].Start),
			Step:       cloneInt64Ptr(in[i].Step),
			Unit:       in[i].Unit,
		}
	}
	return out
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	vv := *v
	return &vv
}

func cloneFloat64Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	vv := *v
	return &vv
}

// 编译时接口断言
var (
	_ engine.NetSender     = (*netSenderAdapter)(nil)
	_ engine.ActionHandler = (*robotActionHandler)(nil)
)
