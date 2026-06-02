// Package script 提供 Lua 脚本运行时，集成 gopher-lua。
// RuntimePool 管理 LState 池和预编译脚本，Context 为每个 Robot 绑定执行上下文。
// Lua API 分为四组命名空间：robot / proto / network / utils。
package script

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/protox"
	"stressbot/state"
	stresslog "stressbot/utils/log"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const registryCtxKey = "__stressbot_ctx__"

// Context Lua 脚本执行上下文。
//
// timing 在每次 RunActionScript 入口清零，脚本内每次 request 追加独立 RequestTiming，
// 避免 Lua 多次 request 被合并成一个 RTT 直方图样本。
type Context struct {
	RobotID int
	Account string
	Store   *state.Store
	Factory *protox.Factory
	// Adapter 是 Robot 私有的 codec 适配器（RobotLocalAdapter 重构后类型从
	// adapter.Adapter 接口收窄到具体类型 *adapter.RobotAdapter）。这样业务 Lua API
	// 内部可以直接调 *Locked 版本（已持 luaMu），避免与自动加锁版本互相自锁。
	// 自动加锁版本则留给 decodeLoop / 声明式动作执行器等"未持 luaMu"的路径。
	Adapter   *adapter.RobotAdapter
	NetSender engine.NetSender
	Ctx       context.Context
	LuaMu     *sync.Mutex

	// DefaultRequestTimeout 当 Lua 脚本调 network.tcp_request / udp_request 未显式传
	// timeout 参数时使用的默认值。来自 robotConfig.timeoutSec → ResolvedConfig.RequestTimeout。
	// 0 表示沿用 engine.DefaultRequestTimeoutSec 的硬编码兜底（保留旧行为兼容）。
	//
	// 历史背景：早期版本硬编码 10s，与声明式 tcpRequest 走的 60s（来自 c.requestTimeout）
	// 不一致。在高并发握手场景下 10s 太短，会把"服务端慢响应但最终能回"误判为 timeout。
	DefaultRequestTimeout time.Duration
	TimingLevel           int

	timingMu      sync.Mutex
	currentTiming engine.ActionTiming
}

// resetTiming 在每次 RunActionScript 开始前清零累加器。
func (c *Context) resetTiming() {
	if c == nil {
		return
	}
	c.timingMu.Lock()
	c.currentTiming = engine.ActionTiming{}
	c.timingMu.Unlock()
}

// recordRequest 累加一次真实的 request-response。供 api_network 调用。
func (c *Context) recordRequest(req engine.RequestTiming) {
	if c == nil || req.WireRTT <= 0 {
		return
	}
	c.timingMu.Lock()
	c.currentTiming.AddRequest(req)
	c.timingMu.Unlock()
}

func (c *Context) recordClientTiming(timing engine.ClientTiming) {
	if c == nil {
		return
	}
	c.timingMu.Lock()
	c.currentTiming.Client.BuildCost += timing.BuildCost
	c.currentTiming.Client.EncodeCost += timing.EncodeCost
	c.currentTiming.Client.SendCost += timing.SendCost
	c.currentTiming.Client.DecodeWait += timing.DecodeWait
	c.currentTiming.Client.DecodeCost += timing.DecodeCost
	c.currentTiming.Client.DispatchWait += timing.DispatchWait
	c.currentTiming.Client.ParseStoreCost += timing.ParseStoreCost
	c.timingMu.Unlock()
}

// timing 取出当前累加结果，构造 ActionTiming。
func (c *Context) timing() engine.ActionTiming {
	if c == nil {
		return engine.ActionTiming{}
	}
	c.timingMu.Lock()
	defer c.timingMu.Unlock()
	out := c.currentTiming
	if len(c.currentTiming.Requests) > 0 {
		out.Requests = append([]engine.RequestTiming(nil), c.currentTiming.Requests...)
	}
	return out
}

// SetContext 将脚本上下文绑定到 LState 的 registry
func SetContext(L *lua.LState, ctx *Context) {
	ud := L.NewUserData()
	ud.Value = ctx
	L.SetField(L.Get(lua.RegistryIndex), registryCtxKey, ud)
}

// GetContext 从 LState 的 registry 获取脚本上下文
func GetContext(L *lua.LState) *Context {
	reg := L.Get(lua.RegistryIndex)
	if reg == lua.LNil {
		return nil
	}
	val := L.GetField(reg, registryCtxKey)
	if ud, ok := val.(*lua.LUserData); ok {
		return ud.Value.(*Context)
	}
	return nil
}

// RuntimePool Lua 运行时池。
// 管理 LState 实例池和预编译的 FunctionProto。
// 每个 Robot 在生命周期内独占一个 LState，结束时归还。
type RuntimePool struct {
	pool        sync.Pool
	precompiled map[string]*lua.FunctionProto // scriptName -> 预编译函数
	scriptDir   string                        // 脚本根目录
}

// NewRuntimePool 创建 Lua 运行时池
func NewRuntimePool(scriptDir string) *RuntimePool {
	rp := &RuntimePool{
		precompiled: make(map[string]*lua.FunctionProto),
		scriptDir:   scriptDir,
	}
	rp.pool.New = func() any {
		L := lua.NewState()
		registerAPIs(L)
		return L
	}
	return rp
}

// Acquire 从池中获取一个 LState。
// LState 已注册所有 API 模块，可直接使用。
func (rp *RuntimePool) Acquire() *lua.LState {
	return rp.pool.Get().(*lua.LState)
}

// Release 将 LState 归还到池中。
// 调用前应清除绑定的 Context。
func (rp *RuntimePool) Release(L *lua.LState) {
	// 清除脚本上下文
	L.SetField(L.Get(lua.RegistryIndex), registryCtxKey, lua.LNil)
	// 清除绑定的 context.Context（Robot.Start 通过 L.SetContext 绑定了已 cancel 的 ctx）。
	// 不清理的话，该 LState 被池内下一个 Robot 复用时会因继承已取消的 ctx 而立即 abort。
	L.RemoveContext()
	rp.pool.Put(L)
}

// PrecompileScripts 预编译指定目录下的所有 .lua 脚本。
// 使用临时 LState 加载并编译，提取 FunctionProto 供后续复用。
func (rp *RuntimePool) PrecompileScripts(dirs []string) error {
	L := lua.NewState()
	defer L.Close()

	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".lua") {
				return nil
			}

			// 编译脚本文件
			fn, err := L.LoadFile(path)
			if err != nil {
				return fmt.Errorf("编译 Lua 脚本失败 %s: %w", path, err)
			}

			// 提取相对路径作为脚本名称
			rel, err := filepath.Rel(rp.scriptDir, path)
			if err != nil {
				rel = path
			}
			scriptName := filepath.ToSlash(rel)
			rp.precompiled[scriptName] = fn.Proto

			return nil
		})
		if err != nil {
			return err
		}
	}

	stresslog.Info("[SCRIPT] 已预编译 Lua 脚本", zap.Int("count", len(rp.precompiled)))
	return nil
}

// RunActionScript 执行动作脚本。
//
// Lua 脚本应定义 `function execute(r)` 函数，统一返回约定（按位置可省略后两个）：
//
//	return code             -- 仅 code，send/recv 视为 0（旧脚本兼容）
//	return code, send       -- 只有发送字节
//	return code, send, recv -- 完整三元组（推荐）
//
// code 仍为整数错误码（0=成功，非 0=失败）。send/recv 是本次 action 在
// lua 内部累计的"线缆字节数"（含 header / 加密后的真实包长，由 lua API 返回值给出），
// 调用方应当把它们透传给 monitor.RecordAction，从而和声明式动作走同一条 per-action
// 字节统计路径，使 ActionsTab 的 ↑avg / ↓avg 列对 lua 动作也能反映真实流量。
//
// timing 由 Context 中保存的每次 RequestTiming 汇总：
//   - 纯客户端脚本（仅 set_secret_key / connect 等）：Requests 为空，不进 RTT 直方图
//   - 含 N 次 request 的脚本：Requests 有 N 个独立 WireRTT 样本
//   - 出错中断的脚本：timing 仍反映已发生的网络调用
func (rp *RuntimePool) RunActionScript(L *lua.LState, scriptName string) (code, send, recv int, timing engine.ActionTiming, err error) {
	compiled, ok := rp.precompiled[scriptName]
	if !ok {
		return -1, 0, 0, engine.ActionTiming{}, fmt.Errorf("脚本未预编译: %s", scriptName)
	}

	// 进入脚本前清零累加器；即使 PCall 报错也要把"已发生"的网络耗时上抛
	if ctx := GetContext(L); ctx != nil {
		ctx.resetTiming()
		defer func() {
			timing = ctx.timing()
		}()
	}

	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	fn := L.NewFunctionFromProto(compiled)
	L.Push(fn)
	if err = L.PCall(0, 0, nil); err != nil {
		return -1, 0, 0, engine.ActionTiming{}, fmt.Errorf("加载脚本 %s 失败: %w", scriptName, err)
	}

	executeFn := L.GetGlobal("execute")
	if executeFn == lua.LNil {
		return -1, 0, 0, engine.ActionTiming{}, fmt.Errorf("脚本 %s 未定义 execute 函数", scriptName)
	}

	// NRet=3 总是申请 3 个返回值占位；脚本只 return 1~2 个时，Lua 会用 nil 补齐。
	robotUD := createRobotUserData(L)
	if err = L.CallByParam(lua.P{Fn: executeFn, NRet: 3, Protect: true}, robotUD); err != nil {
		return -1, 0, 0, engine.ActionTiming{}, fmt.Errorf("执行脚本 %s 失败: %w", scriptName, err)
	}

	// L.Get(savedTop+1..savedTop+3) 依次是 code / send / recv（缺省为 nil → 0）
	if L.GetTop() >= savedTop+1 {
		code = int(lua.LVAsNumber(L.Get(savedTop + 1)))
	}
	if L.GetTop() >= savedTop+2 {
		if n := int(lua.LVAsNumber(L.Get(savedTop + 2))); n > 0 {
			send = n
		}
	}
	if L.GetTop() >= savedTop+3 {
		if n := int(lua.LVAsNumber(L.Get(savedTop + 3))); n > 0 {
			recv = n
		}
	}

	L.SetGlobal("execute", lua.LNil)
	return code, send, recv, engine.ActionTiming{}, nil // timing 由上方 defer 从 ctx 累加器覆盖
}

// RunBooleanScript 执行布尔判断脚本（条件节点 / loop breakCondition）。
//
// Lua 脚本应定义 `function execute(r)` 函数，**必须** return 一个 boolean：
//
//	return true   -- 条件成立
//	return false  -- 条件不成立
//
// 返回 number / nil / 其他类型一律视作错误（不再兼容旧版 0/1 约定）：
// 调用方收到 error 后会判定条件为 false 并打 error 日志，引导脚本作者修正。
func (rp *RuntimePool) RunBooleanScript(L *lua.LState, scriptName string) (bool, error) {
	compiled, ok := rp.precompiled[scriptName]
	if !ok {
		return false, fmt.Errorf("脚本未预编译: %s", scriptName)
	}

	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	fn := L.NewFunctionFromProto(compiled)
	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		return false, fmt.Errorf("加载脚本 %s 失败: %w", scriptName, err)
	}

	executeFn := L.GetGlobal("execute")
	if executeFn == lua.LNil {
		return false, fmt.Errorf("脚本 %s 未定义 execute 函数", scriptName)
	}

	robotUD := createRobotUserData(L)
	if err := L.CallByParam(lua.P{Fn: executeFn, NRet: 1, Protect: true}, robotUD); err != nil {
		return false, fmt.Errorf("执行脚本 %s 失败: %w", scriptName, err)
	}

	defer L.SetGlobal("execute", lua.LNil)

	if L.GetTop() <= savedTop {
		return false, fmt.Errorf("布尔脚本 %s 未返回值，必须 return true/false", scriptName)
	}
	ret := L.Get(-1)
	b, ok := ret.(lua.LBool)
	if !ok {
		return false, fmt.Errorf("布尔脚本 %s 必须 return true/false，实际类型 %s", scriptName, ret.Type().String())
	}
	return bool(b), nil
}

// RunCallbackScript 执行回调脚本。
// Lua 脚本应定义 `function onMessage(r, msg)` 函数。
// msg 为 proto 消息对象（LUserData）。
func (rp *RuntimePool) RunCallbackScript(L *lua.LState, scriptName string, msgData []byte, s2cProto string) error {
	compiled, ok := rp.precompiled[scriptName]
	if !ok {
		return fmt.Errorf("回调脚本未预编译: %s", scriptName)
	}

	// 保存栈顶，确保退出时恢复
	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	// 加载脚本
	fn := L.NewFunctionFromProto(compiled)
	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		return fmt.Errorf("加载回调脚本 %s 失败: %w", scriptName, err)
	}

	// 获取 onMessage 函数
	onMsgFn := L.GetGlobal("onMessage")
	if onMsgFn == lua.LNil {
		return fmt.Errorf("回调脚本 %s 未定义 onMessage 函数", scriptName)
	}

	// 创建 robot 和 msg 对象
	// - 有 s2cProto 配置时：msg 为 proto.Message UserData，通过 proto.get_field_map 解析
	// - 无 s2cProto 且有原始字节时：msg 为原始二进制字符串（UDP 帧数据等场景）
	// - 其他：msg = nil
	robotUD := createRobotUserData(L)
	var msgArg lua.LValue
	switch {
	case s2cProto != "" && len(msgData) > 0:
		msgArg = createProtoMessageUserData(L, msgData, s2cProto)
	case len(msgData) > 0:
		msgArg = lua.LString(msgData)
	default:
		msgArg = lua.LNil
	}

	if err := L.CallByParam(lua.P{Fn: onMsgFn, NRet: 0, Protect: true}, robotUD, msgArg); err != nil {
		return fmt.Errorf("执行回调脚本 %s 失败: %w", scriptName, err)
	}

	// 清理
	L.SetGlobal("onMessage", lua.LNil)

	return nil
}

// HasScript 检查脚本是否已预编译
func (rp *RuntimePool) HasScript(name string) bool {
	_, ok := rp.precompiled[name]
	return ok
}

// ListScripts 列出所有已预编译的脚本
func (rp *RuntimePool) ListScripts() []string {
	names := make([]string, 0, len(rp.precompiled))
	for name := range rp.precompiled {
		names = append(names, name)
	}
	return names
}

// registerAPIs 注册所有 Lua API 模块到 LState
func registerAPIs(L *lua.LState) {
	L.PreloadModule("robot", loadRobotModule)
	L.PreloadModule("proto", loadProtoModule)
	L.PreloadModule("network", loadNetworkModule)
	L.PreloadModule("utils", loadUtilsModule)
	L.PreloadModule("log", loadLogModule)
	L.PreloadModule("json", loadJsonModule)
	L.PreloadModule("adapter", loadAdapterModule)
}

// createRobotUserData 创建 robot 对象（LUserData + metatable）
func createRobotUserData(L *lua.LState) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = GetContext(L)

	mt := L.NewTable()
	L.SetField(mt, "__index", L.NewFunction(robotIndex))
	L.SetMetatable(ud, mt)

	return ud
}

// createProtoMessageUserData 创建 proto 消息对象
func createProtoMessageUserData(L *lua.LState, data []byte, protoName string) *lua.LUserData {
	ctx := GetContext(L)
	var msg any
	if ctx != nil && ctx.Factory != nil && protoName != "" {
		parsed, err := ctx.Factory.Parse(protoName, data)
		if err == nil {
			msg = parsed
		}
	}

	if pm, ok := msg.(proto.Message); ok {
		return wrapProtoMessage(L, pm)
	}
	ud := L.NewUserData()
	ud.Value = msg
	return ud
}
