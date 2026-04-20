// Package robot 管理压测机器人实例。
// Robot 是单个压测客户端的完整上下文，持有网络连接、状态存储、Lua 运行时。
// Manager 负责批量创建、限速启动、销毁 Robot。
package robot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"stressbot/engine"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	"stressbot/state"
	stresslog "stressbot/utils/log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
)

// Robot 单个压测机器人实例。
// 包含网络连接、状态存储、Lua 运行时、流程执行器等完整运行时。
type Robot struct {
	id          int                 // 机器人编号（从 1 开始）
	account     string              // 账号名
	state       *state.Store        // 状态存储
	client      *network.Client     // 网络客户端
	factory     *protox.Factory     // 动态消息工厂
	executor    *engine.Executor    // 流程执行器
	luaPool     *script.RuntimePool // Lua 运行时池（共享）
	L           *lua.LState         // 独占的 Lua 状态机
	ctx         context.Context     // 上下文
	cancel      context.CancelFunc  // 取消函数
	running     atomic.Bool         // 是否正在运行
	dialer      *network.Dialer     // 网络拨号器（共享）
	authBaseURL string              // Auth 服务基础 URL（用于 HTTP POST）
	httpClient  *http.Client        // HTTP 客户端（连接池复用）
	luaMu       sync.Mutex          // 保护 LState 的并发访问（回调/心跳可能在其他 goroutine 触发）
}

// RobotConfig 单个机器人的配置
type RobotConfig struct {
	ID          int
	Account     string
	AuthBaseURL string
	Version     string
	Channel     string
	Platform    string
}

// NewRobot 创建机器人实例。
// flow、factory、luaPool 由 Manager 共享，每个 Robot 有独立的 state、client、LState、executor。
func NewRobot(cfg RobotConfig, flow *engine.TaskFlow, factory *protox.Factory,
	protocol *network.Protocol, dialer *network.Dialer, luaPool *script.RuntimePool) *Robot {

	ctx, cancel := context.WithCancel(context.Background())

	r := &Robot{
		id:          cfg.ID,
		account:     cfg.Account,
		state:       state.NewStore(),
		client:      network.NewClient(cfg.Account, protocol),
		factory:     factory,
		luaPool:     luaPool,
		ctx:         ctx,
		cancel:      cancel,
		dialer:      dialer,
		authBaseURL: cfg.AuthBaseURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}

	// 从池中获取独占 LState
	if luaPool != nil {
		r.L = luaPool.Acquire()
	}

	// 初始化状态
	r.state.Set("id", cfg.ID)
	r.state.Set("account", cfg.Account)
	r.state.Set("version", cfg.Version)
	r.state.Set("channel", cfg.Channel)
	r.state.Set("platform", cfg.Platform)

	// 创建执行器
	r.executor = engine.NewExecutor(flow, &robotActionHandler{robot: r, flow: flow})

	return r
}

// GetID 返回机器人编号
func (r *Robot) GetID() int {
	return r.id
}

// GetAccount 返回账号名
func (r *Robot) GetAccount() string {
	return r.account
}

// GetState 返回状态存储
func (r *Robot) GetState() *state.Store {
	return r.state
}

// GetClient 返回网络客户端
func (r *Robot) GetClient() *network.Client {
	return r.client
}

// GetFactory 返回动态消息工厂
func (r *Robot) GetFactory() *protox.Factory {
	return r.factory
}

// Start 启动机器人，执行流程
func (r *Robot) Start() {
	if !r.running.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer r.running.Store(false)

		// 绑定 Lua 脚本上下文
		if r.L != nil {
			r.luaMu.Lock()
			script.SetContext(r.L, &script.ScriptContext{
				RobotID:   r.id,
				Account:   r.account,
				Store:     r.state,
				Factory:   r.factory,
				Protocol:  r.client.GetProtocol(),
				NetSender: &netSenderAdapter{robot: r},
				Flow:      r.executor.GetFlow(),
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
	// 归还 LState 到池
	if r.L != nil && r.luaPool != nil {
		r.luaPool.Release(r.L)
		r.L = nil
	}
	r.state.Clear()
}

// ConnectTCP 建立 TCP 连接到指定服务
func (r *Robot) ConnectTCP(serviceName, address string) bool {
	ok := r.client.Connect(serviceName, address, 10*time.Second)
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

	return true
}

// ConnectUDP 建立 UDP 连接
func (r *Robot) ConnectUDP(address string) bool {
	ok := r.client.ConnectUDP(address)
	if !ok {
		return false
	}

	conn := r.client.GetUDPConn()
	if conn == nil {
		return false
	}

	_, err := r.dialer.DialUDP(address, conn)
	if err != nil {
		r.client.CloseUDP()
		return false
	}

	return true
}

// CloseTCP 关闭指定服务的 TCP 连接
func (r *Robot) CloseTCP(service string) {
	r.client.Close(service)
}

// CloseUDP 关闭 UDP 连接
func (r *Robot) CloseUDP() {
	r.client.CloseUDP()
}

// robotActionHandler Robot 对 ActionHandler 接口的实现
type robotActionHandler struct {
	robot *Robot
	flow  *engine.TaskFlow
}

// ExecuteAction 执行动作。
// 如果 pattern 为 "lua"，使用 Lua 运行时执行脚本；
// 否则使用声明式 ActionExecutor 执行。
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
	if actionDef.Pattern == "lua" {
		return h.executeLuaAction(actionDef)
	}

	// 声明式动作
	ae := engine.NewActionExecutor(
		h.robot.state,
		&netSenderAdapter{robot: h.robot},
		h.robot.factory,
		h.robot.client.GetProtocol(),
	)
	return ae.Execute(actionDef)
}

// executeLuaAction 执行 Lua 脚本动作
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

// ExecuteBoolean 执行条件判断。
// 如果表达式以 "lua:" 前缀开头，使用 Lua 脚本执行；
// 否则使用简单的内置条件解析。
func (h *robotActionHandler) ExecuteBoolean(expression string) bool {
	if len(expression) > 4 && expression[:4] == "lua:" {
		return h.executeLuaBoolean(expression[4:])
	}

	// TODO: 实现内置条件表达式解析（state 比较、数值范围等）
	return true
}

// executeLuaBoolean 使用 Lua 脚本执行条件判断
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

// RegisterListen 注册持久化监听。
// 根据回调定义（声明式 store 或 Lua 脚本）创建回调函数。
// 按 ref.Server 分组，在每个对应的连接上独立注册监听。
// 约定 server=="udp"/"battleUDP"/"battle_udp" 表示在 UDP 连接上注册监听，
// 其余在对应 TCP 连接上注册（对齐旧 Robot 的 agent 工作池）。
func (h *robotActionHandler) RegisterListen(refs []engine.ListenRef) error {
	protocol := h.robot.client.GetProtocol()

	// 按 server 分组
	groups := make(map[string]map[int]network.ListenCallBack)

	for _, ref := range refs {
		server := ref.Server
		if server == "" {
			server = "logic" // 默认 logic
		}
		if _, ok := groups[server]; !ok {
			groups[server] = make(map[int]network.ListenCallBack)
		}
		cmdAct := protocol.CmdAct(ref.Cmd, ref.Act)

		if ref.Callback == "" {
			groups[server][cmdAct] = nil
			continue
		}

		cbDef, ok := h.flow.GetCallback(ref.Callback)
		if !ok {
			stresslog.Warn("[ROBOT] 回调定义不存在，跳过监听注册", zap.String("callback", ref.Callback))
			continue
		}
		groups[server][cmdAct] = h.createListenCallback(cbDef)
	}

	// 在每个服务对应的连接上注册监听
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

// resolveListenConn 根据 server 名称解析对应的 Connection。
// UDP 约定: "udp" / "battleUDP" / "battle_udp"。
func (h *robotActionHandler) resolveListenConn(server string) *network.Connection {
	switch server {
	case "udp", "battleUDP", "battle_udp", "battleudp":
		return h.robot.client.GetUDPConn()
	default:
		return h.robot.client.GetTCPConn(server)
	}
}

// createListenCallback 根据回调定义创建 ListenCallBack 函数
func (h *robotActionHandler) createListenCallback(cbDef *engine.CallbackDef) network.ListenCallBack {
	if cbDef.Script != "" {
		// Lua 脚本回调：使用 luaMu 串行化对同一 LState 的访问
		return func(msg *network.Message) {
			if h.robot.L == nil || h.robot.luaPool == nil {
				stresslog.Error("[ROBOT] Lua 运行时未初始化，无法执行回调", zap.String("script", cbDef.Script))
				return
			}

			h.robot.luaMu.Lock()
			defer h.robot.luaMu.Unlock()

			script.SetContext(h.robot.L, &script.ScriptContext{
				RobotID:   h.robot.id,
				Account:   h.robot.account,
				Store:     h.robot.state,
				Factory:   h.robot.factory,
				Protocol:  h.robot.client.GetProtocol(),
				NetSender: &netSenderAdapter{robot: h.robot},
				Flow:      h.flow,
				Ctx:       h.robot.ctx,
				LuaMu:     &h.robot.luaMu,
			})

			if err := h.robot.luaPool.RunCallbackScript(h.robot.L, cbDef.Script, msg.Data, cbDef.S2CProto); err != nil {
				stresslog.Error("[ROBOT] Lua 回调执行失败",
					zap.Int("id", h.robot.id), zap.String("script", cbDef.Script), zap.Error(err))
			}
		}
	}

	// 声明式回调：无配置则返回 nil（消息缓存供 waitListen 轮询）
	if cbDef.S2CProto == "" || len(cbDef.Store) == 0 {
		return nil
	}

	return func(msg *network.Message) {
		if msg.Data == nil || len(msg.Data) == 0 {
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

// OnNodeError 节点执行出错回调
func (h *robotActionHandler) OnNodeError(node *engine.Node, err error) {
	stresslog.Error("[ROBOT] 节点执行错误",
		zap.Int("id", h.robot.id), zap.String("node", node.ID), zap.String("type", node.Type), zap.Error(err))
}

// netSenderAdapter NetSender 接口适配器
type netSenderAdapter struct {
	robot *Robot
}

func (ns *netSenderAdapter) TCPSend(service string, cmd, act uint8, headAndBody []byte) (bool, int) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return false, 0
	}
	return conn.Send(headAndBody)
}

func (ns *netSenderAdapter) TCPRequest(service string, cmd, act uint8, headAndBody []byte) ([]byte, bool) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil, false
	}
	protocol := ns.robot.client.GetProtocol()
	cmdAct := protocol.CmdAct(cmd, act)
	resp, _ := conn.RequestResponse(headAndBody, cmdAct)
	if resp == nil {
		return nil, false
	}
	// 日志记录消息头 error 字段（用于调试连接关闭问题）
	if resp.Head != nil {
		stresslog.Debug("[ACTION] TCPResponse head",
			zap.String("service", service), zap.Uint8("cmd", resp.Head.Cmd), zap.Uint8("act", resp.Head.Act), zap.Uint16("headError", resp.Head.Error), zap.Int("bodyLen", len(resp.Data)))
	}
	return resp.Data, true
}

// TCPRequestFor 发送请求并等待指定 cmd/act 的响应（响应 cmd/act 可以不同于请求的 cmd/act）。
// 用于 MainLoadOk(CMD=2,ACT=16) 等待 LoginPlayerDataS2C(CMD=1,ACT=2) 这类跨 CMD 响应场景。
func (ns *netSenderAdapter) TCPRequestFor(service string, sendCmd, sendAct uint8, headAndBody []byte, respCmd, respAct uint8) ([]byte, bool) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil, false
	}
	protocol := ns.robot.client.GetProtocol()
	respCmdAct := protocol.CmdAct(respCmd, respAct)
	resp, _ := conn.RequestResponse(headAndBody, respCmdAct)
	if resp == nil {
		return nil, false
	}
	if resp.Head != nil {
		stresslog.Debug("[ACTION] TCPResponse head",
			zap.String("service", service), zap.Uint8("cmd", resp.Head.Cmd), zap.Uint8("act", resp.Head.Act), zap.Uint16("headError", resp.Head.Error), zap.Int("bodyLen", len(resp.Data)))
	}
	return resp.Data, true
}

func (ns *netSenderAdapter) HTTPPost(path string, formData map[string]string) (int, []byte, error) {
	// 构建完整 URL
	baseURL := ns.robot.authBaseURL
	if baseURL == "" {
		return 0, nil, fmt.Errorf("Auth 服务地址未配置")
	}

	fullURL := baseURL
	if !strings.HasPrefix(path, "/") {
		fullURL += "/"
	}
	fullURL += path

	// 构建 form data
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

func (ns *netSenderAdapter) UDPSend(data []byte) bool {
	conn := ns.robot.client.GetUDPConn()
	if conn == nil {
		return false
	}
	ok, _ := conn.Send(data)
	return ok
}

func (ns *netSenderAdapter) UDPSendPacket(cmd, act uint8, body []byte) bool {
	conn := ns.robot.client.GetUDPConn()
	if conn == nil {
		return false
	}
	protocol := ns.robot.client.GetProtocol()
	// UDP 发送使用 offset 加密：body[0:offset] 保持明文（通常是 PacketIndex/BattleId/FighterIndex，
	// 供服务端通过 SecretKeyManager 根据 battleId+fighterIndex 查找解密密钥；剩余部分加密）。
	// 偏移量由 header.json 的 encrypt.udpOffset 配置，默认 11。
	encOffset := protocol.UDPEncryptOffset()
	secretKey := conn.GetSecretKey()
	// 当 body 小于偏移量时不足以加密；当未设置密钥时退化为明文，以兼容密钥交换前的首包。
	if len(body) <= encOffset || len(secretKey) == 0 {
		packet := protocol.BuildPacket(cmd, act, body, nil)
		ok, _ := conn.Send(packet)
		return ok
	}
	packet := protocol.BuildPacketWithOffset(cmd, act, body, secretKey, encOffset)
	ok, _ := conn.Send(packet)
	return ok
}

func (ns *netSenderAdapter) ConnectTCP(service, address string) bool {
	return ns.robot.ConnectTCP(service, address)
}

func (ns *netSenderAdapter) ConnectUDP(address string) bool {
	return ns.robot.ConnectUDP(address)
}

func (ns *netSenderAdapter) GetListenResp(service string, cmdAct int) []byte {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return nil
	}
	msg := conn.GetListenResp(cmdAct)
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

func (ns *netSenderAdapter) EnsureListener(service string, cmd, act uint8) {
	conn := ns.robot.client.GetTCPConn(service)
	if conn == nil {
		return
	}
	protocol := ns.robot.client.GetProtocol()
	cmdAct := protocol.CmdAct(cmd, act)
	conn.AddListener(cmdAct, nil)
}

func (ns *netSenderAdapter) CloseTCP(service string) {
	ns.robot.CloseTCP(service)
}

func (ns *netSenderAdapter) CloseUDP() {
	ns.robot.CloseUDP()
}

func (ns *netSenderAdapter) SetUDPSecretKey(key []byte) {
	conn := ns.robot.client.GetUDPConn()
	if conn != nil {
		conn.SetSecretKey(key)
	}
}

func (ns *netSenderAdapter) GetUDPSecretKey() []byte {
	conn := ns.robot.client.GetUDPConn()
	if conn == nil {
		return nil
	}
	return conn.GetSecretKey()
}

func (ns *netSenderAdapter) RegisterHeartbeat(target, service string, intervalMs int, builder func() []byte) {
	var conn *network.Connection
	if target == "udp" {
		conn = ns.robot.client.GetUDPConn()
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
