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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	"stressbot/state"
	stresslog "stressbot/utils/log"
)

// Robot 单个压测机器人实例。
type Robot struct {
	id          int
	account     string
	state       *state.Store
	client      *network.Client
	factory     *protox.Factory
	executor    *engine.Executor
	luaPool     *script.RuntimePool
	L           *lua.LState
	actionExec  *engine.ActionExecutor
	ctx         context.Context
	cancel      context.CancelFunc
	running     atomic.Bool
	dialer      *network.Dialer
	httpClient  *http.Client
	luaMu       sync.Mutex
	adp         adapter.Adapter
	mainService string        // 主连接服务名，仅用于断开检测（断开时停止机器人）
	done        chan struct{} // 执行 goroutine 结束信号
}

// Config 单个机器人的配置
type Config struct {
	ID          int
	Account     string
	StateExtra  map[string]string
	HTTPTimeout time.Duration
}

// NewRobot 创建机器人实例。
func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
	adp adapter.Adapter, dialer *network.Dialer, luaPool *script.RuntimePool,
	requestTimeout time.Duration, mainService string) *Robot {

	ctx, cancel := context.WithCancel(context.Background())

	r := &Robot{
		id:          cfg.ID,
		account:     cfg.Account,
		state:       state.NewStore(),
		client:      network.NewClient(cfg.Account, requestTimeout),
		factory:     factory,
		luaPool:     luaPool,
		ctx:         ctx,
		cancel:      cancel,
		dialer:      dialer,
		httpClient:  &http.Client{Timeout: cfg.HTTPTimeout},
		adp:         adp,
		mainService: mainService,
		done:        make(chan struct{}),
	}

	if luaPool != nil {
		r.L = luaPool.Acquire()
	}

	r.state.Set("id", cfg.ID)
	r.state.Set("account", cfg.Account)
	for k, v := range cfg.StateExtra {
		r.state.Set(k, v)
	}

	r.actionExec = engine.NewActionExecutor(r.state, &netSenderAdapter{robot: r}, r.factory, r.adp, r.ctx)
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

	go func() {
		defer r.running.Store(false)
		defer close(r.done)

		if r.L != nil {
			r.luaMu.Lock()
			script.SetContext(r.L, &script.Context{
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
		stresslog.Info("[ROBOT] 已停止", zap.Int("id", r.id), zap.String("account", r.account))
	}()
}

// Stop 停止机器人
func (r *Robot) Stop() {
	r.cancel()
}

// Wait 等待机器人执行 goroutine 结束。
func (r *Robot) Wait() {
	<-r.done
}

// Close 停止机器人并释放资源。
// 先 Stop 取消 ctx，再同时关闭连接（释放阻塞中的 RequestResponse）并等待执行退出。
func (r *Robot) Close() {
	r.Stop()
	// 并行关闭连接和等待执行：关闭连接可以解除 RequestResponse 的阻塞，
	// 避免 Wait() 死锁（executor 阻塞在网络 I/O 上时，仅靠 cancel ctx 无法唤醒）。
	var waitDone chan struct{}
	if r.done != nil {
		waitDone = make(chan struct{})
		go func() {
			r.Wait()
			close(waitDone)
		}()
	}
	r.client.CloseAll()
	if waitDone != nil {
		<-waitDone
	}
	if r.L != nil && r.luaPool != nil {
		r.luaPool.Release(r.L)
		r.L = nil
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
			stresslog.Debug("[ROBOT] 连接断开",
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

// robotActionHandler Robot 对 ActionHandler 接口的实现
type robotActionHandler struct {
	robot *Robot
	flow  *engine.TaskFlow
}

// ExecuteAction 执行动作
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		mc.RecordActionStart(actionDef.Name)
	}

	start := time.Now()
	var sendBytes, recvBytes int
	var err error

	if actionDef.Pattern == engine.PatternLua {
		sendBytes, recvBytes, err = h.executeLuaAction(actionDef)
	} else {
		sendBytes, recvBytes, err = h.robot.actionExec.Execute(actionDef)
	}

	if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
		result := classifyResult(err)
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		mc.RecordAction(actionDef.Name, result, time.Since(start), sendBytes, recvBytes, errMsg)
	}

	return err
}

// classifyResult 将 error 映射为 monitor ActionResult。
func classifyResult(err error) monitor.ActionResult {
	if err == nil {
		return monitor.ResultSuccess
	}
	if errors.Is(err, engine.ErrActionSkip) {
		return monitor.ResultSkipped
	}
	if errors.Is(err, engine.ErrTimeout) {
		return monitor.ResultTimeout
	}
	return monitor.ResultFailure
}

// executeLuaAction 执行 lua 脚本动作，返回 (sendBytes, recvBytes, err)。
func (h *robotActionHandler) executeLuaAction(actionDef *engine.ActionDef) (int, int, error) {
	if h.robot.L == nil || h.robot.luaPool == nil {
		return 0, 0, fmt.Errorf("lua 运行时未初始化")
	}

	if actionDef.Script == "" {
		return 0, 0, fmt.Errorf("lua 动作缺少 script 配置")
	}

	h.robot.luaMu.Lock()
	defer h.robot.luaMu.Unlock()

	code, send, recv, err := h.robot.luaPool.RunActionScript(h.robot.L, actionDef.Script)
	if err != nil {
		return 0, 0, fmt.Errorf("执行 lua 脚本 %s 失败: %w", actionDef.Script, err)
	}

	if code != 0 {
		return send, recv, fmt.Errorf("lua 脚本 %s 返回错误码: %d", actionDef.Script, code)
	}

	return send, recv, nil
}

// ExecuteBoolean 执行条件判断
func (h *robotActionHandler) ExecuteBoolean(expression string) bool {
	if len(expression) > 4 && expression[:4] == "lua:" {
		return h.executeLuaBoolean(expression[4:])
	}

	return evalCondition(expression, h.robot.state)
}

// executeLuaBoolean 执行 Lua 条件脚本，脚本必须 return true/false。
func (h *robotActionHandler) executeLuaBoolean(scriptName string) bool {
	if h.robot.L == nil || h.robot.luaPool == nil {
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

	result, err := h.robot.luaPool.RunBooleanScript(h.robot.L, scriptName)
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
			continue
		}
		key := connKey{proto: proto, service: service}
		if _, ok := groups[key]; !ok {
			groups[key] = make(map[string]network.ListenCallBack)
		}

		respKey := h.robot.adp.ExpectedResponseKey(ref.Route)

		if ref.Callback == "" {
			groups[key][respKey] = nil
			continue
		}

		cbDef, ok := h.flow.GetCallback(ref.Callback)
		if !ok {
			stresslog.Warn("[ROBOT] 回调定义不存在", zap.String("callback", ref.Callback))
			continue
		}
		groups[key][respKey] = h.createListenCallback(ref.Callback, cbDef)
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
			stresslog.Warn("[ROBOT] 无连接可注册监听", zap.String("proto", key.proto), zap.String("service", key.service))
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
func (h *robotActionHandler) createListenCallback(cbName string, cbDef *engine.CallbackDef) network.ListenCallBack {
	if cbDef.Script != "" {
		return func(msg *network.Message) {
			monitor.Global().RecordCallback(cbName)
			if h.robot.L == nil || h.robot.luaPool == nil {
				stresslog.Error("[ROBOT] Lua 运行时未初始化", zap.String("script", cbDef.Script))
				return
			}

			h.robot.luaMu.Lock()
			defer h.robot.luaMu.Unlock()

			script.SetContext(h.robot.L, &script.Context{
				RobotID:   h.robot.id,
				Account:   h.robot.account,
				Store:     h.robot.state,
				Factory:   h.robot.factory,
				Adapter:   h.robot.adp,
				NetSender: &netSenderAdapter{robot: h.robot},
				Ctx:       h.robot.ctx,
				LuaMu:     &h.robot.luaMu,
			})

			if err := h.robot.luaPool.RunCallbackScript(h.robot.L, cbDef.Script, msg.Data, cbDef.S2CProto); err != nil {
				stresslog.Error("[ROBOT] Lua 回调执行失败",
					zap.Int("id", h.robot.id), zap.String("script", cbDef.Script), zap.Error(err))
			}
		}
	}

	if cbDef.S2CProto == "" || len(cbDef.Store) == 0 {
		return nil
	}

	return func(msg *network.Message) {
		monitor.Global().RecordCallback(cbName)
		if len(msg.Data) == 0 {
			return
		}

		respMsg, err := h.robot.factory.Parse(cbDef.S2CProto, msg.Data)
		if err != nil {
			stresslog.Error("[ROBOT] 解析推送消息失败",
				zap.Int("id", h.robot.id), zap.String("proto", cbDef.S2CProto), zap.Error(err))
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
	}
}

// evalCondition 求值 state: 前缀的条件表达式。
// 支持复合条件：&&、||、!、括号嵌套。
// 示例：state:hp > 0 && (state:alive || state:isAdmin)
func evalCondition(expr string, s *state.Store) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}

	if !strings.HasPrefix(expr, "state:") {
		stresslog.Warn("[ROBOT] 条件表达式格式错误，仅支持 state: 前缀",
			zap.String("expr", expr))
		return false
	}
	return parseExpr(expr[6:], s)
}

// parseRHS 尝试将条件右值解析为数值类型，保留字符串回退。
func parseRHS(s string) any {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return s
}

// netSenderAdapter NetSender 接口适配器
type netSenderAdapter struct {
	robot *Robot
}

// TCPSend 通过 TCP 发送数据包。
func (ns *netSenderAdapter) TCPSend(service string, packet []byte) (bool, int) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return false, 0
	}
	return conn.Send(packet)
}

// TCPRequest 发送 TCP 请求并等待响应。
func (ns *netSenderAdapter) TCPRequest(service string, packet []byte, responseKey string, timeout ...time.Duration) ([]byte, uint64, bool) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		stresslog.Warn("[ACTION] TCPRequest 连接不存在",
			zap.String("service", service), zap.String("responseKey", responseKey))
		return nil, 0, false
	}
	resp, _ := conn.RequestResponse(packet, responseKey, timeout...)
	if resp == nil {
		return nil, 0, false
	}
	stresslog.Debug("[ACTION] TCPResponse",
		zap.String("service", service), zap.String("responseKey", responseKey),
		zap.Int("bodyLen", len(resp.Data)), zap.Uint64("headerErr", resp.HeaderErr))
	return resp.Data, resp.HeaderErr, true
}

// UDPRequest 发送 UDP 请求并等待响应，与 TCPRequest 同样使用 channel 阻塞等待。
func (ns *netSenderAdapter) UDPRequest(service string, packet []byte, responseKey string, timeout ...time.Duration) ([]byte, uint64, bool) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		stresslog.Warn("[ACTION] UDPRequest 连接不存在",
			zap.String("service", service), zap.String("responseKey", responseKey))
		return nil, 0, false
	}
	resp, _ := conn.RequestResponse(packet, responseKey, timeout...)
	if resp == nil {
		return nil, 0, false
	}
	stresslog.Debug("[ACTION] UDPResponse",
		zap.String("service", service), zap.String("responseKey", responseKey),
		zap.Int("bodyLen", len(resp.Data)), zap.Uint64("headerErr", resp.HeaderErr))
	return resp.Data, resp.HeaderErr, true
}

// HTTPRequest 发送 HTTP 请求。
func (ns *netSenderAdapter) HTTPRequest(reqURL, method, contentType string, body []byte) (int, []byte, error) {
	if reqURL == "" {
		return 0, nil, fmt.Errorf("HTTP 请求 URL 为空")
	}
	if !strings.HasPrefix(reqURL, "http://") && !strings.HasPrefix(reqURL, "https://") {
		return 0, nil, fmt.Errorf("HTTP 请求 URL 必须以 http:// 或 https:// 开头: %s", reqURL)
	}

	var req *http.Request
	var err error

	if len(body) > 0 {
		switch contentType {
		case "json":
			req, err = http.NewRequest(method, reqURL, bytes.NewReader(body))
			if err != nil {
				return 0, nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
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
				return 0, nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		default:
			req, err = http.NewRequest(method, reqURL, bytes.NewReader(body))
			if err != nil {
				return 0, nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
			}
		}
	} else {
		req, err = http.NewRequest(method, reqURL, nil)
		if err != nil {
			return 0, nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
		}
	}

	resp, err := ns.robot.httpClient.Do(req)
	if err != nil {
		stresslog.Warn("[HTTP] 请求失败", zap.String("url", reqURL), zap.Error(err))
		return 0, nil, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	return resp.StatusCode, respBody, nil
}

// UDPSend 通过 UDP 发送数据包。
func (ns *netSenderAdapter) UDPSend(service string, data []byte) (bool, int) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return false, 0
	}
	return conn.Send(data)
}

// ConnectTCP 通过适配器建立 TCP 连接。
func (ns *netSenderAdapter) ConnectTCP(service, address string) bool {
	return ns.robot.ConnectTCP(service, address)
}

// ConnectUDP 通过适配器建立 UDP 连接。
func (ns *netSenderAdapter) ConnectUDP(service, address string) bool {
	return ns.robot.ConnectUDP(service, address)
}

// GetTCPListenResp 获取 TCP 连接的监听响应数据。
func (ns *netSenderAdapter) GetTCPListenResp(service string, responseKey string) ([]byte, uint64) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil, 0
	}
	msg := conn.GetListenResp(responseKey)
	if msg == nil {
		return nil, 0
	}
	return msg.Data, msg.HeaderErr
}

// GetUDPListenResp 获取 UDP 连接的监听响应数据。
func (ns *netSenderAdapter) GetUDPListenResp(service string, responseKey string) ([]byte, uint64) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil, 0
	}
	msg := conn.GetListenResp(responseKey)
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
func (ns *netSenderAdapter) EnsureTCPListener(service string, responseKey string) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return
	}
	conn.AddListener(responseKey, nil)
}

// EnsureUDPListener 为 UDP 连接注册监听器占位。
func (ns *netSenderAdapter) EnsureUDPListener(service string, responseKey string) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return
	}
	conn.AddListener(responseKey, nil)
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
