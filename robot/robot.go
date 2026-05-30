package robot

import (
	"bytes"
	"context"
	"encoding/json"
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
	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	"stressbot/state"
	"stressbot/utils"
	stresslog "stressbot/utils/log"
)

// Robot 单个压测机器人实例。
// 每个 Robot 拥有独立的状态存储、网络客户端、Lua 运行时和流程执行器。
type Robot struct {
	id         int                    // 机器人唯一编号
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
	luaMu      sync.Mutex             // Lua 访问互斥锁（回调/心跳/encode/decode 共抢）
	// adp 是该 Robot 私有的 codec 适配器（RobotLocalAdapter 重构）。
	// 所有 encode/decode 都在 r.l 上执行，与其他 Robot 不再共享 LState 池。
	adp            *adapter.RobotAdapter
	mainService    string        // 主连接服务名，意外断开时停止机器人
	requestTimeout time.Duration // robotConfig.timeoutSec 注入；用作 Lua tcp/udp_request 默认 timeout
	timingLevel    int           // monitor.timingDetail 映射后的 engine 计时级别
	done           chan struct{} // 执行 goroutine 结束信号，Close 时等待
	onDone         func()        // 执行 goroutine 结束后回调（由 Manager 设置）
}

// Config 单个机器人的配置。
type Config struct {
	ID             int               // 机器人唯一编号
	Account        string            // 账号名
	StateExtra     map[string]string // 初始状态额外键值对
	HTTPTimeout    time.Duration     // HTTP 请求超时
	RequestTimeout time.Duration     // 网络请求超时（TCP/UDP RequestResponse）
	MainService    string            // 主连接服务名，意外断开时停止机器人
}

// NewRobot 创建机器人实例。
//
// globalAdp 是进程级共享的 LuaAdapter（持有 codec.lua 字节码 + 元信息 + 错误描述缓存）。
// NewRobot 内部调 globalAdp.NewRobotAdapter(r.l, &r.luaMu) 在该 Robot 私有 LState 上
// 注册一份 codec 函数副本，后续编解码不再跨 Robot 抢全局 LState 池。
//
// 返回 error 的场景：codec.lua 在 r.l 上加载失败（脚本错误 / 缺少必需函数）。
// 这种情况说明 codec 配置有问题，重试无意义，调用方应跳过该 Robot 并打 error 日志。
// 正常运行下这里不会失败（启动期已通过 LuaAdapter 验证过 codec.lua 完整性）。
func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
	globalAdp *adapter.LuaAdapter, dialer *network.Dialer, luaPool *script.RuntimePool) (*Robot, error) {

	if globalAdp == nil {
		return nil, fmt.Errorf("NewRobot: globalAdp 不能为 nil")
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
		account:        cfg.Account,
		state:          state.NewStore(),
		client:         network.NewClient(cfg.Account, cfg.RequestTimeout, timingDetail),
		factory:        factory,
		luaPool:        luaPool,
		ctx:            ctx,
		cancel:         cancel,
		dialer:         dialer,
		httpClient:     &http.Client{Timeout: cfg.HTTPTimeout},
		mainService:    cfg.MainService,
		requestTimeout: cfg.RequestTimeout,
		timingLevel:    engineTimingLevel,
		done:           make(chan struct{}),
	}

	if luaPool != nil {
		r.l = luaPool.Acquire()
	}

	// 派生 Robot 私有 codec 适配器（必须在 r.l 准备好之后、Start() 之前完成）。
	// 此时 r.l 刚从池中拿出，没有其它 goroutine 在用，NewRobotAdapter 不需要持锁也安全。
	if r.l == nil {
		cancel()
		return nil, fmt.Errorf("NewRobot: Lua 运行时池未提供 LState")
	}
	robotAdp, err := globalAdp.NewRobotAdapter(r.l, &r.luaMu)
	if err != nil {
		// 归还 LState 避免资源泄漏；其它字段会被 GC 回收
		if luaPool != nil {
			luaPool.Release(r.l)
		}
		cancel()
		return nil, fmt.Errorf("NewRobot: 创建 RobotAdapter 失败 (account=%s): %w", cfg.Account, err)
	}
	r.adp = robotAdp

	r.state.Set("id", cfg.ID)
	r.state.Set("account", cfg.Account)
	for k, v := range cfg.StateExtra {
		r.state.Set(k, v)
	}

	r.actionExec = engine.NewActionExecutor(r.state, &netSenderAdapter{robot: r}, r.factory, r.adp, engineTimingLevel)
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
			r.luaMu.Lock()
			script.SetContext(r.l, &script.Context{
				RobotID:               r.id,
				Account:               r.account,
				Store:                 r.state,
				Factory:               r.factory,
				Adapter:               r.adp,
				NetSender:             &netSenderAdapter{robot: r},
				Ctx:                   r.ctx,
				LuaMu:                 &r.luaMu,
				DefaultRequestTimeout: r.requestTimeout,
				TimingLevel:           r.timingLevel,
			})
			r.luaMu.Unlock()
		}

		stresslog.Info("[ROBOT] 启动", zap.Int("id", r.id), zap.String("account", r.account))
		monitor.Global().RobotStarted()
		monitor.Global().RobotRunning()

		done := make(chan struct{})
		utils.GetWorkPool().Go(func() {
			defer close(done)
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
		case <-done:
		case <-stopCh:
			r.cancel()
			<-done
		}

		stresslog.Info("[ROBOT] 已停止", zap.Int("id", r.id), zap.String("account", r.account))
		if r.onDone != nil {
			r.onDone()
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
//  2. r.client.CloseAll() 内含 WaitListenDone：等待 listenLoop 退出
//     （回调里的 lua 脚本等 luaMu 时可能死锁）
//
// 任一阻塞超过 robotCloseTimeout 即放弃等待，强制返回让上层推进。
// 代价：LState 不归还到池、state 不清空（避免与卡死的 goroutine 并发访问），
// 进程退出时由 OS 回收，轻微资源泄漏可接受。
const robotCloseTimeout = 10 * time.Second

// Close 停止机器人并释放资源。
// 先 Stop 取消 ctx，再并行关闭连接（释放阻塞中的 RequestResponse）并等待执行退出。
// 关键修复：r.client.CloseAll() 本身也可能因 listenLoop 回调死锁而长时间阻塞，
// 必须放进 goroutine 与 r.Wait() 一起在同一个 select 内统一受超时保护。
func (r *Robot) Close() {
	r.Stop()

	var waitDone chan struct{}
	if r.done != nil {
		waitDone = make(chan struct{})
		utils.GetWorkPool().Go(func() {
			r.Wait()
			close(waitDone)
		})
	}
	closeDone := make(chan struct{})
	utils.GetWorkPool().Go(func() {
		r.client.CloseAll()
		close(closeDone)
	})

	timeout := time.NewTimer(robotCloseTimeout)
	defer timeout.Stop()

	// 等待 waitDone（如有）与 closeDone 双双完成，或整体超时。
	// 已完成的 channel 置 nil，nil channel 上的接收永远阻塞，自然退出循环。
	pending := 1
	if waitDone != nil {
		pending = 2
	}
	for pending > 0 {
		select {
		case <-waitDone:
			waitDone = nil
			pending--
		case <-closeDone:
			closeDone = nil
			pending--
		case <-timeout.C:
			stresslog.Error("[ROBOT] 关闭等待超时，跳过资源归还（可能存在死锁，进程退出时回收）",
				zap.Int("id", r.id),
				zap.String("account", r.account),
				zap.Duration("timeout", robotCloseTimeout),
				zap.Bool("waitDone", waitDone == nil),
				zap.Bool("closeDone", closeDone == nil))
			return
		}
	}

	if r.l != nil && r.luaPool != nil {
		r.luaPool.Release(r.l)
		r.l = nil
	}
	r.state.Clear()
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

	_, err := r.dialer.DialTCP(r.ctx, address, conn, r.adp)
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
			r.client.CloseAll()
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

	_, err := r.dialer.DialUDP(address, conn, r.adp)
	if err != nil {
		stresslog.Warn("[ROBOT] UDP 拨号失败",
			zap.Int("id", r.id), zap.String("account", r.account),
			zap.String("service", serviceName), zap.String("address", address), zap.Error(err))
		r.client.CloseUDP(serviceName)
		monitor.Global().ConnFailed()
		return false
	}

	monitor.Global().ConnEstablished()

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

// ExecuteAction 执行动作
//
// 计时拆解原则：
//   - wallClock = time.Since(start)：含 proto 构建 / 序列化 / 反序列化 / state 写入等全部开销。
//   - timing.Requests：每次 request-response 的独立 WireRTT 样本。
//   - clientCost 由 monitor 用 wallClock - sum(WireRTT) 计算。
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		mc.RecordActionStart(actionDef.Name)
	}

	start := time.Now()
	var sendBytes, recvBytes int
	var timing engine.ActionTiming
	var err error

	if actionDef.Pattern == engine.PatternLua {
		sendBytes, recvBytes, timing, err = h.executeLuaAction(actionDef)
	} else {
		sendBytes, recvBytes, timing, err = h.robot.actionExec.Execute(h.robot.ctx, actionDef)
	}

	// 任务取消时的"副作用错误"覆写：
	// stop 阶段，Lua 脚本（如 match_succeed.lua / connect_battle_tcp.lua）会因
	// 底层 ctx 取消而拿到 nil/false，但脚本通常硬编码 return 31/1 等具体错误码，
	// 经 luaCodeToActionErr 映射后变成 LISTEN_TIMEOUT/CONN_NOT_FOUND；
	// 实际原因是 ACTION_CANCELED。在这里统一矫正，避免 monitor 把 cancel 流量
	// 误归类为 Timeout/Failure，也避免 executor 重复刷 error 日志。
	if err != nil && h.robot.ctx.Err() != nil {
		var actionErr *engine.ActionError
		if errors.As(err, &actionErr) && !isCanceledCode(actionErr.Code) {
			err = engine.NewActionError(errcode.ErrActionCanceled,
				"stopping: "+actionErr.Detail)
		}
	}

	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		result := classifyResult(err)
		wallClock := time.Since(start)
		mc.RecordAction(actionDef.Name, result, toMonitorTiming(timing), wallClock, sendBytes, recvBytes, err)
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

// executeLuaAction 执行 lua 脚本动作，返回 (sendBytes, recvBytes, timing, err)。
// timing 由脚本内的网络 API 累加；纯客户端逻辑（如仅 set_secret_key）timing 为零值。
func (h *robotActionHandler) executeLuaAction(actionDef *engine.ActionDef) (int, int, engine.ActionTiming, error) {
	if h.robot.l == nil || h.robot.luaPool == nil {
		stresslog.Error("[ROBOT] Lua 运行时未初始化，无法执行脚本",
			zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("script", actionDef.Script))
		return 0, 0, engine.ActionTiming{}, engine.NewActionError(errcode.ErrLuaNotInit, "")
	}

	if actionDef.Script == "" {
		stresslog.Error("[ROBOT] 脚本名为空，无法执行",
			zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("action", actionDef.Name))
		return 0, 0, engine.ActionTiming{}, engine.NewActionError(errcode.ErrLuaNoScript, "")
	}

	h.robot.luaMu.Lock()
	defer h.robot.luaMu.Unlock()

	// RunActionScript 内部通过 script.Context 累积每次 request 的独立 RequestTiming，
	// 这里把结构化 timing 上抛给 RecordAction。
	code, send, recv, timing, err := h.robot.luaPool.RunActionScript(h.robot.l, actionDef.Script)
	if err != nil {
		return 0, 0, timing, engine.NewActionError(errcode.ErrLuaExecFailed, "script="+actionDef.Script, err)
	}

	if code != 0 {
		return send, recv, timing, luaCodeToActionErr(code, actionDef.Script)
	}

	return send, recv, timing, nil
}

// luaCodeToActionErr 将 Lua 脚本退出码映射为结构化 ActionError。
//
// Lua 网络层 API（tcp_request / udp_send 等）已统一使用 errcode 体系返回错误码，
// 如果 exit code 命中已知 errcode，直接构造对应的 ActionError 透传给 monitor，
// 使前端 error map 能展示真实错误分类（如 ErrRecvTimeout / ErrConnClosed），
// 而非全部塌缩为 ErrLuaExitCode。
//
// 映射规则：
//   - code ∈ 已知框架 errcode     → NewActionError，由 classifyResult 按 Code 分流 ResultTimeout/ResultFailure
//   - code ≥ 100                  → NewServerError，走 ResultFailure + error map (Kind=server)
//   - 其他                         → 兜底 ErrLuaExitCode
func luaCodeToActionErr(code int, script string) error {
	detail := fmt.Sprintf("script=%s", script)

	ec := errcode.ErrorCode(code)

	switch ec {
	case errcode.ErrConnNotFound, errcode.ErrConnClosed, errcode.ErrSendFailed,
		errcode.ErrRecvTimeout, errcode.ErrConnDropped, errcode.ErrActionCanceled,
		errcode.ErrEncodeFailed, errcode.ErrParseFailed,
		errcode.ErrListenTimeout:
		return engine.NewActionError(ec, detail)
	}

	if code >= 100 {
		return engine.NewServerError(uint64(code), detail)
	}

	return engine.NewActionError(errcode.ErrLuaExitCode, fmt.Sprintf("%s code=%d", detail, code))
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
			zap.String("script", scriptName))
		return false
	}

	if !h.robot.luaPool.HasScript(scriptName) {
		stresslog.Error("[ROBOT] 条件脚本不存在，条件判断默认拒绝",
			zap.String("script", scriptName))
		return false
	}

	h.robot.luaMu.Lock()
	defer h.robot.luaMu.Unlock()

	result, err := h.robot.luaPool.RunBooleanScript(h.robot.l, scriptName)
	if err != nil {
		stresslog.Error("[ROBOT] 条件脚本执行失败，条件判断默认拒绝",
			zap.String("script", scriptName), zap.Error(err))
		return false
	}

	return result
}

// RegisterListen 注册持久化监听
func (h *robotActionHandler) RegisterListen(refs []engine.ListenRef) error {
	type connKey struct {
		proto   string
		service string
	}
	groups := make(map[connKey]map[string]network.ListenCallBack)

	for _, ref := range refs {
		proto, service, ok := parseServer(ref.Server)
		if !ok {
			stresslog.Warn("[ROBOT] 监听引用的 server 解析失败，跳过注册",
				zap.String("server", ref.Server), zap.String("listen", ref.Listen))
			continue
		}
		key := connKey{proto: proto, service: service}
		if _, ok := groups[key]; !ok {
			groups[key] = make(map[string]network.ListenCallBack)
		}

		routeKey := h.robot.adp.ExpectedRouteKey(ref.Route)

		if ref.Listen == "" {
			groups[key][routeKey] = nil
			continue
		}

		cbDef, ok := h.flow.Listen(ref.Listen)
		if !ok {
			stresslog.Error("[ROBOT] 回调定义不存在", zap.String("listen", ref.Listen))
			continue
		}
		groups[key][routeKey] = h.createListenCallback(ref.Listen, cbDef)
	}

	for key, listenMap := range groups {
		var conn *network.Connection
		switch key.proto {
		case "udp":
			conn = h.robot.client.GetUDPConn(key.service)
		case "tcp":
			conn = h.robot.client.GetTCPConn(key.service)
		}
		if conn == nil {
			stresslog.Debug("[ROBOT] 无连接可注册监听", zap.String("proto", key.proto), zap.String("service", key.service))
			continue
		}
		conn.ListenResponse(listenMap)
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
func (h *robotActionHandler) createListenCallback(cbName string, cbDef *engine.ListenDef) network.ListenCallBack {
	if cbDef.Script != "" {
		return func(msg *network.Message) {
			if h.robot.l == nil || h.robot.luaPool == nil {
				stresslog.Error("[ROBOT] Lua 运行时未初始化", zap.String("script", cbDef.Script))
				monitor.Global().RecordCallbackError(cbName, engine.NewActionError(errcode.ErrCallbackLua, "script="+cbDef.Script))
				return
			}

			h.robot.luaMu.Lock()
			defer h.robot.luaMu.Unlock()

			script.SetContext(h.robot.l, &script.Context{
				RobotID:               h.robot.id,
				Account:               h.robot.account,
				Store:                 h.robot.state,
				Factory:               h.robot.factory,
				Adapter:               h.robot.adp,
				NetSender:             &netSenderAdapter{robot: h.robot},
				Ctx:                   h.robot.ctx,
				LuaMu:                 &h.robot.luaMu,
				DefaultRequestTimeout: h.robot.requestTimeout,
				TimingLevel:           h.robot.timingLevel,
			})

			if err := h.robot.luaPool.RunCallbackScript(h.robot.l, cbDef.Script, msg.Data, cbDef.S2CProto); err != nil {
				stresslog.Error("[ROBOT] Lua 回调执行失败",
					zap.Int("id", h.robot.id), zap.String("script", cbDef.Script), zap.Error(err))
				monitor.Global().RecordCallbackError(cbName, engine.NewActionError(errcode.ErrCallbackLua, "script="+cbDef.Script, err))
				return
			}
			monitor.Global().RecordCallbackSuccess(cbName)
		}
	}

	if cbDef.S2CProto == "" || len(cbDef.Store) == 0 {
		return nil
	}

	return func(msg *network.Message) {
		if len(msg.Data) == 0 {
			return
		}

		respMsg, err := h.robot.factory.Parse(cbDef.S2CProto, msg.Data)
		if err != nil {
			stresslog.Error("[ROBOT] 解析推送消息失败",
				zap.Int("id", h.robot.id), zap.String("proto", cbDef.S2CProto), zap.Error(err))
			monitor.Global().RecordCallbackError(cbName, engine.NewActionError(errcode.ErrCallbackParse, "proto="+cbDef.S2CProto, err))
			return
		}

		fieldMap := h.robot.factory.GetFieldMap(respMsg)
		for _, m := range cbDef.Store {
			if m.Field == "" {
				h.robot.state.Set(m.Setter, fieldMap)
			} else if val, ok := fieldMap[m.Field]; ok {
				h.robot.state.Set(m.Setter, val)
			}
		}
		monitor.Global().RecordCallbackSuccess(cbName)
	}
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

// TCPRequest 发送 TCP 请求并等待响应。
// 返回 netLatency 是 Connection.RequestResponse 测量的"Send 完成 → 收到响应"窗口，
// 不含本函数中的连接查找等微秒级开销。
func (ns *netSenderAdapter) TCPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) ([]byte, uint64, engine.RequestTiming, error) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil, 0, engine.RequestTiming{}, engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
	}
	resp, netTiming, err := conn.RequestResponse(packet, routeKey, timeout...)
	timing := engine.RequestTiming(netTiming)
	if err != nil {
		return nil, 0, timing, err
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[ACTION] TCPResponse",
			zap.String("service", service), zap.String("routeKey", routeKey),
			zap.Int("bodyLen", len(resp.Data)), zap.Uint64("headerErr", resp.HeaderErr))
	}
	return resp.Data, resp.HeaderErr, timing, nil
}

// UDPRequest 发送 UDP 请求并等待响应，与 TCPRequest 同样使用 channel 阻塞等待。
func (ns *netSenderAdapter) UDPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) ([]byte, uint64, engine.RequestTiming, error) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil, 0, engine.RequestTiming{}, engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
	}
	resp, netTiming, err := conn.RequestResponse(packet, routeKey, timeout...)
	timing := engine.RequestTiming(netTiming)
	if err != nil {
		return nil, 0, timing, err
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[ACTION] UDPResponse",
			zap.String("service", service), zap.String("routeKey", routeKey),
			zap.Int("bodyLen", len(resp.Data)), zap.Uint64("headerErr", resp.HeaderErr))
	}
	return resp.Data, resp.HeaderErr, timing, nil
}

// HTTPRequest 发送 HTTP 请求。
//
// 返回 netLatency 覆盖：http.Client.Do 调用 + 读完 response.Body。
// http.NewRequest 构造、URL 解析等纯客户端开销不计入。
func (ns *netSenderAdapter) HTTPRequest(reqURL, method, contentType string, body []byte) (int, []byte, time.Duration, error) {
	if reqURL == "" {
		return 0, nil, 0, engine.NewActionError(errcode.ErrURLEmpty, "")
	}
	if !strings.HasPrefix(reqURL, "http://") && !strings.HasPrefix(reqURL, "https://") {
		return 0, nil, 0, engine.NewActionError(errcode.ErrURLScheme, "url="+reqURL)
	}

	var req *http.Request
	var err error

	if len(body) > 0 {
		switch contentType {
		case "json":
			req, err = http.NewRequest(method, reqURL, bytes.NewReader(body))
			if err != nil {
				return 0, nil, 0, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
			}
			req.Header.Set("Content-Type", "application/json")
		case "form":
			values := make(url.Values)
			if json.Unmarshal(body, &values) == nil {
				body = []byte(values.Encode())
			}
			req, err = http.NewRequest(method, reqURL, strings.NewReader(string(body)))
			if err != nil {
				return 0, nil, 0, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		default:
			req, err = http.NewRequest(method, reqURL, bytes.NewReader(body))
			if err != nil {
				return 0, nil, 0, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
			}
		}
	} else {
		req, err = http.NewRequest(method, reqURL, nil)
		if err != nil {
			return 0, nil, 0, engine.NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)
		}
	}

	netStart := time.Now()
	resp, err := ns.robot.httpClient.Do(req)
	if err != nil {
		netLatency := time.Since(netStart)
		stresslog.Warn("[HTTP] 请求失败", zap.String("url", reqURL), zap.Error(err))
		return 0, nil, netLatency, engine.NewActionError(errcode.ErrSendFailed, "url="+reqURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	netLatency := time.Since(netStart)
	if err != nil {
		return resp.StatusCode, nil, netLatency, engine.NewActionError(errcode.ErrHTTPReadBody, "url="+reqURL, err)
	}

	return resp.StatusCode, respBody, netLatency, nil
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
func (ns *netSenderAdapter) GetTCPListenResp(service string, routeKey string) ([]byte, uint64) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil, 0
	}
	msg := conn.GetListenResp(routeKey)
	if msg == nil {
		return nil, 0
	}
	return msg.Data, msg.HeaderErr
}

// GetUDPListenResp 获取 UDP 连接的监听响应数据。
func (ns *netSenderAdapter) GetUDPListenResp(service string, routeKey string) ([]byte, uint64) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil, 0
	}
	msg := conn.GetListenResp(routeKey)
	if msg == nil {
		return nil, 0
	}
	return msg.Data, msg.HeaderErr
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
	}
}

// EnsureTCPListener 为 TCP 连接注册监听器占位。
func (ns *netSenderAdapter) EnsureTCPListener(service string, routeKey string) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return
	}
	conn.AddListener(routeKey, nil)
}

// EnsureUDPListener 为 UDP 连接注册监听器占位。
func (ns *netSenderAdapter) EnsureUDPListener(service string, routeKey string) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return
	}
	conn.AddListener(routeKey, nil)
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

// RegisterTCPHeartbeat 注册 TCP 心跳。
func (ns *netSenderAdapter) RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte) error {
	if ns.robot.ctx.Err() != nil {
		return engine.NewActionError(errcode.ErrActionCanceled, "service="+service)
	}
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		err := engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
		stresslog.Warn("[ROBOT] RegisterTCPHeartbeat 连接不存在", zap.String("service", service))
		return err
	}
	conn.RegisterHeartbeat(network.HeartbeatConfig{
		Interval: time.Duration(intervalMs) * time.Millisecond,
		Builder:  builder,
	})
	return nil
}

// RegisterUDPHeartbeat 注册 UDP 心跳。
func (ns *netSenderAdapter) RegisterUDPHeartbeat(service string, intervalMs int, builder func() []byte) error {
	if ns.robot.ctx.Err() != nil {
		return engine.NewActionError(errcode.ErrActionCanceled, "service="+service)
	}
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		err := engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
		stresslog.Warn("[ROBOT] RegisterUDPHeartbeat 连接不存在", zap.String("service", service))
		return err
	}
	conn.RegisterHeartbeat(network.HeartbeatConfig{
		Interval: time.Duration(intervalMs) * time.Millisecond,
		Builder:  builder,
	})
	return nil
}

// 编译时接口断言
var (
	_ engine.NetSender     = (*netSenderAdapter)(nil)
	_ engine.ActionHandler = (*robotActionHandler)(nil)
)
