package robot

import (
	"context"
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
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	"stressbot/state"
	stresslog "stressbot/utils/log"
)

// Robot 单个压测机器人实例。
type Robot struct {
	id                  int
	account             string
	state               *state.Store
	client              *network.Client
	factory             *protox.Factory
	executor            *engine.Executor
	luaPool             *script.RuntimePool
	L                   *lua.LState
	ctx                 context.Context
	cancel              context.CancelFunc
	running             atomic.Bool
	dialer              *network.Dialer
	authBaseURL         string
	httpClient          *http.Client
	luaMu               sync.Mutex
	adp                 adapter.Adapter
	udpServices         map[string]bool
	mainService         string // 主连接服务名，仅用于断开检测（断开时停止机器人）
}

// Config 单个机器人的配置
type Config struct {
	ID          int
	Account     string
	AuthBaseURL string
	AuthExtra   map[string]string
}

// NewRobot 创建机器人实例。
func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
	adp adapter.Adapter, dialer *network.Dialer, luaPool *script.RuntimePool,
	requestTimeout time.Duration, udpServices map[string]bool, mainService string) *Robot {

	ctx, cancel := context.WithCancel(context.Background())

	r := &Robot{
		id:                  cfg.ID,
		account:             cfg.Account,
		state:               state.NewStore(),
		client:              network.NewClient(cfg.Account, requestTimeout),
		factory:             factory,
		luaPool:             luaPool,
		ctx:                 ctx,
		cancel:              cancel,
		dialer:              dialer,
		authBaseURL:         cfg.AuthBaseURL,
		httpClient:          &http.Client{Timeout: 10 * time.Second},
		adp:                 adp,
		udpServices:         udpServices,
		mainService: mainService,
	}

	if luaPool != nil {
		r.L = luaPool.Acquire()
	}

	r.state.Set("id", cfg.ID)
	r.state.Set("account", cfg.Account)
	for k, v := range cfg.AuthExtra {
		r.state.Set(k, v)
	}

	r.executor = engine.NewExecutor(flow, &robotActionHandler{robot: r, flow: flow}, r.account)

	return r
}

func (r *Robot) GetID() int                  { return r.id }
func (r *Robot) GetAccount() string          { return r.account }
func (r *Robot) GetState() *state.Store      { return r.state }
func (r *Robot) GetClient() *network.Client  { return r.client }
func (r *Robot) GetFactory() *protox.Factory { return r.factory }

// Start 启动机器人
func (r *Robot) Start() {
	if !r.running.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer r.running.Store(false)

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
		if err := r.executor.Run(r.ctx); err != nil {
			if r.ctx.Err() == nil {
				stresslog.Error("[ROBOT] 流程异常退出", zap.Int("id", r.id), zap.Error(err))
			}
		}
		stresslog.Info("[ROBOT] 已停止", zap.Int("id", r.id), zap.String("account", r.account))
	}()
}

// Stop 停止机器人
func (r *Robot) Stop() {
	r.cancel()
}

// Close 释放机器人资源
func (r *Robot) Close() {
	r.Stop()
	r.client.CloseAll()
	if r.L != nil && r.luaPool != nil {
		r.luaPool.Release(r.L)
		r.L = nil
	}
	r.state.Clear()
}

// ConnectTCP 建立 TCP 连接
func (r *Robot) ConnectTCP(serviceName, address string) bool {
	ok := r.client.Connect(serviceName)
	if !ok {
		return false
	}

	conn := r.client.GetTCPConn(serviceName)
	if conn == nil {
		return false
	}

	_, err := r.dialer.DialTCP(r.ctx, address, conn)
	if err != nil {
		r.client.Close(serviceName)
		return false
	}

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
		return false
	}

	conn.SetOnDisconnect(func() {
		stresslog.Debug("[ROBOT] UDP 连接断开",
			zap.Int("id", r.id), zap.String("account", r.account), zap.String("service", serviceName))
	})

	return true
}

// CloseTCP 关闭指定服务的 TCP 连接
func (r *Robot) CloseTCP(service string) {
	r.client.Close(service)
}

// CloseUDP 关闭指定服务的 UDP 连接
func (r *Robot) CloseUDP(service string) {
	r.client.CloseUDP(service)
}

// resolveListenConnByName 按服务名解析连接，区分 UDP / TCP。
func (r *Robot) resolveListenConnByName(name string) *network.Connection {
	if r.udpServices[name] {
		return r.client.GetUDPConn(name)
	}
	return r.client.GetTCPConn(name)
}

// robotActionHandler Robot 对 ActionHandler 接口的实现
type robotActionHandler struct {
	robot *Robot
	flow  *engine.TaskFlow
}

// ExecuteAction 执行动作
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
	if actionDef.Pattern == "lua" {
		return h.executeLuaAction(actionDef)
	}

	ae := engine.NewActionExecutor(
		h.robot.state,
		&netSenderAdapter{robot: h.robot},
		h.robot.factory,
		h.robot.adp,
	)
	return ae.Execute(actionDef)
}

func (h *robotActionHandler) executeLuaAction(actionDef *engine.ActionDef) error {
	if h.robot.L == nil || h.robot.luaPool == nil {
		return fmt.Errorf("Lua 运行时未初始化")
	}

	if actionDef.Script == "" {
		return fmt.Errorf("Lua 动作缺少 script 配置")
	}

	h.robot.luaMu.Lock()
	defer h.robot.luaMu.Unlock()

	code, err := h.robot.luaPool.RunActionScript(h.robot.L, actionDef.Script)
	if err != nil {
		return fmt.Errorf("执行 Lua 脚本 %s 失败: %w", actionDef.Script, err)
	}

	if code != 0 {
		return fmt.Errorf("Lua 脚本 %s 返回错误码: %d", actionDef.Script, code)
	}

	return nil
}

// ExecuteBoolean 执行条件判断
func (h *robotActionHandler) ExecuteBoolean(expression string) bool {
	if len(expression) > 4 && expression[:4] == "lua:" {
		return h.executeLuaBoolean(expression[4:])
	}

	return evalCondition(expression, h.robot.state)
}

func (h *robotActionHandler) executeLuaBoolean(scriptName string) bool {
	if h.robot.L == nil || h.robot.luaPool == nil {
		return true
	}

	if !h.robot.luaPool.HasScript(scriptName) {
		stresslog.Warn("[ROBOT] 条件脚本不存在", zap.String("script", scriptName))
		return true
	}

	h.robot.luaMu.Lock()
	defer h.robot.luaMu.Unlock()

	code, err := h.robot.luaPool.RunActionScript(h.robot.L, scriptName)
	if err != nil {
		stresslog.Warn("[ROBOT] 条件脚本执行失败", zap.String("script", scriptName), zap.Error(err))
		return true
	}

	return code == 0
}

// RegisterListen 注册持久化监听
func (h *robotActionHandler) RegisterListen(refs []engine.ListenRef) error {
	groups := make(map[string]map[string]network.ListenCallBack)

	for _, ref := range refs {
		server := ref.Server
		if server == "" {
			stresslog.Warn("[ROBOT] 监听引用缺少 server 字段")
			continue
		}
		if _, ok := groups[server]; !ok {
			groups[server] = make(map[string]network.ListenCallBack)
		}

		respKey := h.robot.adp.ExpectedResponseKey(ref.Route)

		if ref.Callback == "" {
			groups[server][respKey] = nil
			continue
		}

		cbDef, ok := h.flow.GetCallback(ref.Callback)
		if !ok {
			stresslog.Warn("[ROBOT] 回调定义不存在", zap.String("callback", ref.Callback))
			continue
		}
		groups[server][respKey] = h.createListenCallback(cbDef)
	}

	for server, listenMap := range groups {
		conn := h.resolveListenConn(server)
		if conn == nil {
			stresslog.Warn("[ROBOT] 无连接可注册监听", zap.String("server", server))
			continue
		}
		conn.ListenResponse(listenMap)
	}

	return nil
}

func (h *robotActionHandler) resolveListenConn(server string) *network.Connection {
	if h.robot.udpServices[server] {
		return h.robot.client.GetUDPConn(server)
	}
	return h.robot.client.GetTCPConn(server)
}

func (h *robotActionHandler) createListenCallback(cbDef *engine.CallbackDef) network.ListenCallBack {
	if cbDef.Script != "" {
		return func(msg *network.Message) {
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

func evalCondition(expr string, s *state.Store) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}

	if !strings.HasPrefix(expr, "state:") {
		return true
	}
	rest := expr[6:]

	for _, op := range []string{">=", "<=", "!=", "==", ">", "<"} {
		if idx := strings.Index(rest, op); idx > 0 {
			key := strings.TrimSpace(rest[:idx])
			rhs := strings.TrimSpace(rest[idx+len(op):])
			lhs := s.Get(key)
			if lhs == nil {
				return false
			}
			return compareValues(lhs, rhs, op)
		}
	}

	val := s.Get(rest)
	if val == nil {
		return false
	}
	switch v := val.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	default:
		return true
	}
}

func compareValues(lhs any, rhs string, op string) bool {
	lhsStr := fmt.Sprintf("%v", lhs)
	if op == "==" {
		return lhsStr == rhs
	}
	if op == "!=" {
		return lhsStr != rhs
	}

	lhsNum, err1 := strconv.ParseFloat(lhsStr, 64)
	rhsNum, err2 := strconv.ParseFloat(rhs, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	switch op {
	case ">":
		return lhsNum > rhsNum
	case ">=":
		return lhsNum >= rhsNum
	case "<":
		return lhsNum < rhsNum
	case "<=":
		return lhsNum <= rhsNum
	}
	return false
}

// netSenderAdapter NetSender 接口适配器
type netSenderAdapter struct {
	robot *Robot
}

func (ns *netSenderAdapter) TCPSend(service string, packet []byte) (bool, int) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return false, 0
	}
	return conn.Send(packet)
}

func (ns *netSenderAdapter) TCPRequest(service string, packet []byte, responseKey string) ([]byte, bool) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		stresslog.Warn("[ACTION] TCPRequest 连接不存在",
			zap.String("service", service), zap.String("responseKey", responseKey))
		return nil, false
	}
	resp, _ := conn.RequestResponse(packet, responseKey)
	if resp == nil {
		return nil, false
	}
	stresslog.Debug("[ACTION] TCPResponse",
		zap.String("service", service), zap.String("responseKey", responseKey),
		zap.Int("bodyLen", len(resp.Data)))
	return resp.Data, true
}

func (ns *netSenderAdapter) HTTPPost(path string, formData map[string]string) (int, []byte, error) {
	baseURL := ns.robot.authBaseURL
	if baseURL == "" {
		return 0, nil, fmt.Errorf("Auth 服务地址未配置")
	}

	fullURL := baseURL
	if !strings.HasPrefix(path, "/") {
		fullURL += "/"
	}
	fullURL += path

	values := make(url.Values)
	for k, v := range formData {
		values.Set(k, v)
	}

	resp, err := ns.robot.httpClient.PostForm(fullURL, values)
	if err != nil {
		return 0, nil, fmt.Errorf("HTTP POST 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	return resp.StatusCode, body, nil
}

func (ns *netSenderAdapter) UDPSend(service string, data []byte) bool {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return false
	}
	ok, _ := conn.Send(data)
	return ok
}

func (ns *netSenderAdapter) ConnectTCP(service, address string) bool {
	return ns.robot.ConnectTCP(service, address)
}

func (ns *netSenderAdapter) ConnectUDP(service, address string) bool {
	return ns.robot.ConnectUDP(service, address)
}

func (ns *netSenderAdapter) GetListenResp(service string, responseKey string) []byte {
	conn := ns.robot.resolveListenConnByName(service)
	if conn == nil {
		return nil
	}
	msg := conn.GetListenResp(responseKey)
	if msg == nil {
		return nil
	}
	return msg.Data
}

func (ns *netSenderAdapter) GetSecretKey(service string) []byte {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil
	}
	return conn.GetSecretKey()
}

func (ns *netSenderAdapter) SetSecretKey(service string, key []byte) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn != nil {
		conn.SetSecretKey(key)
	}
}

func (ns *netSenderAdapter) EnsureListener(service string, responseKey string) {
	conn := ns.robot.resolveListenConnByName(service)
	if conn == nil {
		return
	}
	conn.AddListener(responseKey, nil)
}

func (ns *netSenderAdapter) CloseTCP(service string) {
	ns.robot.CloseTCP(service)
}

func (ns *netSenderAdapter) CloseUDP(service string) {
	ns.robot.CloseUDP(service)
}

func (ns *netSenderAdapter) SetUDPSecretKey(service string, key []byte) {
	conn := ns.robot.client.GetUDPConn(service)
	if conn != nil {
		conn.SetSecretKey(key)
	}
}

func (ns *netSenderAdapter) GetUDPSecretKey(service string) []byte {
	conn := ns.robot.client.GetUDPConn(service)
	if conn == nil {
		return nil
	}
	return conn.GetSecretKey()
}

func (ns *netSenderAdapter) RegisterHeartbeat(target, service string, intervalMs int, builder func() []byte) {
	var conn *network.Connection
	if ns.robot.udpServices[target] {
		conn = ns.robot.client.GetUDPConn(target)
	} else if target == "udp" {
		conn = ns.robot.client.GetUDPConn(service)
	} else {
		conn = ns.robot.client.GetTCPConn(service)
	}
	if conn == nil {
		stresslog.Warn("[ROBOT] RegisterHeartbeat 连接不存在", zap.String("target", target), zap.String("service", service))
		return
	}
	conn.RegisterHeartbeat(network.HeartbeatConfig{
		Interval: time.Duration(intervalMs) * time.Millisecond,
		Builder:  builder,
	})
}
