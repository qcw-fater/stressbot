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
	id          int                    // 机器人唯一编号
	account     string                 // 账号名
	state       *state.Store           // 线程安全的键值状态存储
	client      *network.Client        // 多服务网络客户端（管理 TCP/UDP 连接池）
	factory     *protox.Factory        // protobuf 消息工厂（动态创建/解析）
	executor    *engine.Executor       // 流程图执行器
	luaPool     *script.RuntimePool    // Lua 运行时池（Robot 持有独占 LState）
	l           *lua.LState            // 当前 Robot 独占的 Lua 状态（从 luaPool 获取）
	actionExec  *engine.ActionExecutor // 声明式动作执行器
	ctx         context.Context        // 机器人生命周期上下文
	cancel      context.CancelFunc     // 取消函数（Stop 时调用）
	running     atomic.Bool            // 是否正在运行
	dialer      *network.Dialer        // 网络拨号器（封装 gnet 事件循环）
	httpClient  *http.Client           // HTTP 客户端（声明式 HTTP 动作用）
	luaMu       sync.Mutex             // Lua 访问互斥锁（回调/心跳可能在其他 goroutine 触发）
	adp         adapter.Adapter        // 协议适配器（编解码 + 帧解析）
	mainService string                 // 主连接服务名，意外断开时停止机器人
	done        chan struct{}          // 执行 goroutine 结束信号，Close 时等待
	onDone      func()                 // 执行 goroutine 结束后回调（由 Manager 设置）
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
func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
	adp adapter.Adapter, dialer *network.Dialer, luaPool *script.RuntimePool) *Robot {

	ctx, cancel := context.WithCancel(context.Background())

	r := &Robot{
		id:          cfg.ID,
		account:     cfg.Account,
		state:       state.NewStore(),
		client:      network.NewClient(cfg.Account, cfg.RequestTimeout),
		factory:     factory,
		luaPool:     luaPool,
		ctx:         ctx,
		cancel:      cancel,
		dialer:      dialer,
		httpClient:  &http.Client{Timeout: cfg.HTTPTimeout},
		adp:         adp,
		mainService: cfg.MainService,
		done:        make(chan struct{}),
	}

	if luaPool != nil {
		r.l = luaPool.Acquire()
	}

	r.state.Set("id", cfg.ID)
	r.state.Set("account", cfg.Account)
	for k, v := range cfg.StateExtra {
		r.state.Set(k, v)
	}

	r.actionExec = engine.NewActionExecutor(r.state, &netSenderAdapter{robot: r}, r.factory, r.adp)
	r.executor = engine.NewExecutor(flow, &robotActionHandler{robot: r, flow: flow}, r.account)

	return r
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
				RobotID:   r.id,
				Account:   r.account,
				Store:     r.state,
				Factory:   r.factory,
				Adapter:   r.adp,
				NetSender: &netSenderAdapter{robot: r},
				Ctx:       r.ctx,
				LuaMu:     &r.luaMu,
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

	_, err := r.dialer.DialTCP(r.ctx, address, conn)
	if err != nil {
		stresslog.Warn("[ROBOT] TCP 连接建立失败",
			zap.Int("id", r.id), zap.String("service", serviceName), zap.String("addr", address), zap.Error(err))
		r.client.CloseTCP(serviceName)
		monitor.Global().ConnFailed()
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

	_, err := r.dialer.DialUDP(address, conn)
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
// 计时拆解原则（参见 plans/latency-net-only-redesign.md）：
//   - wallClock = time.Since(start)：含 proto 构建 / 序列化 / 反序列化 / state 写入等全部开销。
//   - timing.NetLatency：纯网络往返（由 ActionExecutor / Lua API 累加）。
//   - clientCost = wallClock - NetLatency：客户端 CPU 部分，作为独立列 ClientAvgMs 上报。
//   - clientCost 可能因测量精度抖动出现极小负值，做下界保护。
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

	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		result := classifyResult(err)
		wallClock := time.Since(start)
		clientCost := wallClock - timing.NetLatency
		if clientCost < 0 {
			clientCost = 0
		}
		mc.RecordAction(actionDef.Name, result,
			timing.NetLatency, clientCost, timing.SamplesNet,
			sendBytes, recvBytes, err)
	}

	return err
}

// classifyResult 将 error 映射为 monitor ActionResult。
func classifyResult(err error) monitor.ActionResult {
	if err == nil {
		return monitor.ResultSuccess
	}
	// 任务取消优先级最高
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return monitor.ResultCanceled
	}
	if errors.Is(err, engine.ErrTimeout) {
		return monitor.ResultTimeout
	}
	return monitor.ResultFailure
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

	// RunActionScript 内部为脚本绑定一个新的 script.Context，
	// 脚本里的 tcp_request / udp_request / http_request 等会原子累加 NetLatencyNs / NetSamples，
	// 这里把累加结果作为 timing 上抛给 RecordAction。
	code, send, recv, timing, err := h.robot.luaPool.RunActionScript(h.robot.l, actionDef.Script)
	if err != nil {
		return 0, 0, timing, engine.NewActionError(errcode.ErrLuaExecFailed, "script="+actionDef.Script, err)
	}

	if code != 0 {
		return send, recv, timing, engine.NewActionError(errcode.ErrLuaExitCode, fmt.Sprintf("script=%s code=%d", actionDef.Script, code))
	}

	return send, recv, timing, nil
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
				RobotID:   h.robot.id,
				Account:   h.robot.account,
				Store:     h.robot.state,
				Factory:   h.robot.factory,
				Adapter:   h.robot.adp,
				NetSender: &netSenderAdapter{robot: h.robot},
				Ctx:       h.robot.ctx,
				LuaMu:     &h.robot.luaMu,
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
func (ns *netSenderAdapter) TCPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) ([]byte, uint64, time.Duration, error) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil, 0, 0, engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
	}
	resp, netLatency, err := conn.RequestResponse(packet, routeKey, timeout...)
	if err != nil {
		return nil, 0, netLatency, err
	}
	stresslog.Debug("[ACTION] TCPResponse",
		zap.String("service", service), zap.String("routeKey", routeKey),
		zap.Int("bodyLen", len(resp.Data)), zap.Uint64("headerErr", resp.HeaderErr))
	return resp.Data, resp.HeaderErr, netLatency, nil
}

// UDPRequest 发送 UDP 请求并等待响应，与 TCPRequest 同样使用 channel 阻塞等待。
func (ns *netSenderAdapter) UDPRequest(service string, packet []byte, routeKey string, timeout ...time.Duration) ([]byte, uint64, time.Duration, error) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil, 0, 0, engine.NewActionError(errcode.ErrConnNotFound, "service="+service)
	}
	resp, netLatency, err := conn.RequestResponse(packet, routeKey, timeout...)
	if err != nil {
		return nil, 0, netLatency, err
	}
	stresslog.Debug("[ACTION] UDPResponse",
		zap.String("service", service), zap.String("routeKey", routeKey),
		zap.Int("bodyLen", len(resp.Data)), zap.Uint64("headerErr", resp.HeaderErr))
	return resp.Data, resp.HeaderErr, netLatency, nil
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
			if err := json.Unmarshal(body, &values); err == nil {
				req, err = http.NewRequest(method, reqURL, strings.NewReader(values.Encode()))
			} else {
				req, err = http.NewRequest(method, reqURL, strings.NewReader(string(body)))
			}
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
		return engine.NewActionError(errcode.ErrConnClosed, "service="+service+" address="+address)
	}
	return nil
}

// ConnectUDP 通过适配器建立 UDP 连接。
func (ns *netSenderAdapter) ConnectUDP(service, address string) error {
	ok := ns.robot.ConnectUDP(service, address)
	if !ok {
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
func (ns *netSenderAdapter) RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		stresslog.Warn("[ROBOT] RegisterTCPHeartbeat 连接不存在", zap.String("service", service))
		return
	}
	conn.RegisterHeartbeat(network.HeartbeatConfig{
		Interval: time.Duration(intervalMs) * time.Millisecond,
		Builder:  builder,
	})
}

// RegisterUDPHeartbeat 注册 UDP 心跳。
func (ns *netSenderAdapter) RegisterUDPHeartbeat(service string, intervalMs int, builder func() []byte) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		stresslog.Warn("[ROBOT] RegisterUDPHeartbeat 连接不存在", zap.String("service", service))
		return
	}
	conn.RegisterHeartbeat(network.HeartbeatConfig{
		Interval: time.Duration(intervalMs) * time.Millisecond,
		Builder:  builder,
	})
}

// 编译时接口断言
var (
	_ engine.NetSender     = (*netSenderAdapter)(nil)
	_ engine.ActionHandler = (*robotActionHandler)(nil)
)
