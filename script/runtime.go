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
	"stressbot/sharedstate"
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
	Index   int
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

	// Shared 任务级共享状态后端（Redis）。多个 Robot 共享同一实例。
	// 未启用共享状态（无 Redis 配置 / 任务未使用 share）时为 nil，
	// share.* API 返回 ErrNotEnabled，不 panic。
	Shared sharedstate.Store

	// DefaultRequestTimeout 当 Lua 脚本调 network.tcp_request / udp_request 未显式传
	// timeout 参数时使用的默认值。来自 robotConfig.timeoutSec → ResolvedConfig.RequestTimeout。
	// 0 表示沿用 engine.DefaultRequestTimeoutSec 的硬编码兜底（保留旧行为兼容）。
	//
	// 历史背景：早期版本硬编码 10s，与声明式 tcpRequest 走的 60s（来自 c.requestTimeout）
	// 不一致。在高并发握手场景下 10s 太短，会把"服务端慢响应但最终能回"误判为 timeout。
	DefaultRequestTimeout time.Duration
	TimingLevel           int

	metricsMu        sync.Mutex
	currentTiming    engine.ActionTiming
	currentSendBytes int
	currentRecvBytes int
	lastActionError  *engine.ActionError
}

// resetMetrics 在每次 action/callback 脚本开始前清零累加器。
func (c *Context) resetMetrics() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming = engine.ActionTiming{}
	c.currentSendBytes = 0
	c.currentRecvBytes = 0
	c.lastActionError = nil
	c.metricsMu.Unlock()
}

// recordBytes 累加脚本内网络 API 实际发生的 WireBytes。供 api_network 调用。
func (c *Context) recordBytes(send, recv int) {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	if send > 0 {
		c.currentSendBytes += send
	}
	if recv > 0 {
		c.currentRecvBytes += recv
	}
	c.metricsMu.Unlock()
}

// recordRequest 累加一次真实的 request-response。供 api_network 调用。
func (c *Context) recordRequest(req engine.RequestTiming) {
	if c == nil || req.WireRTT <= 0 {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming.AddRequest(req)
	c.metricsMu.Unlock()
}

func (c *Context) recordClientTiming(timing engine.ClientTiming) {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming.Client.BuildCost += timing.BuildCost
	c.currentTiming.Client.EncodeCost += timing.EncodeCost
	c.currentTiming.Client.SendCost += timing.SendCost
	c.currentTiming.Client.DecodeWait += timing.DecodeWait
	c.currentTiming.Client.DecodeCost += timing.DecodeCost
	c.currentTiming.Client.DispatchWait += timing.DispatchWait
	c.currentTiming.Client.ParseStoreCost += timing.ParseStoreCost
	c.metricsMu.Unlock()
}

// metrics 取出当前累加结果。
func (c *Context) metrics() (send int, recv int, timing engine.ActionTiming) {
	if c == nil {
		return 0, 0, engine.ActionTiming{}
	}
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	out := c.currentTiming
	if len(c.currentTiming.Requests) > 0 {
		out.Requests = append([]engine.RequestTiming(nil), c.currentTiming.Requests...)
	}
	return c.currentSendBytes, c.currentRecvBytes, out
}

// SetLastActionError 记录 Lua 网络 API 最近一次结构化错误。
// Lua 脚本最终 return 非 0 时，robot 层优先使用它，避免只凭 code 猜 Kind/Detail。
func (c *Context) SetLastActionError(err *engine.ActionError) {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.lastActionError = err
	c.metricsMu.Unlock()
}

// LastActionError 返回最近一次 Lua 网络 API 记录的结构化错误。
func (c *Context) LastActionError() *engine.ActionError {
	if c == nil {
		return nil
	}
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	return c.lastActionError
}

// ClearLastActionError 清理最近一次结构化错误。
func (c *Context) ClearLastActionError() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.lastActionError = nil
	c.metricsMu.Unlock()
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
		// 默认 lua.NewState 会预分配 RegistrySize(5120) 的数据栈和 CallStackSize(256)
		// 的固定调用帧栈，×数千机器人时纯固定开销可达数百 MB。这里改为小初始值 +
		// 按需增长：数据栈从 1024 起步、上限 16384、每次扩 512；调用帧栈用分段自增长。
		// 栈不够会自动扩，对脚本完全透明，仅省下空闲预分配的峰值内存。
		L := lua.NewState(lua.Options{
			RegistrySize:        1024,
			RegistryMaxSize:     16384,
			RegistryGrowStep:    512,
			CallStackSize:       256,
			MinimizeStackMemory: true,
		})
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
	// 清理本次 Robot 在脚本运行期写入全局表的"运行时全局"，避免被池内下一个 Robot
	// 复用时读到上一个 Robot 的残留状态（脚本顶层定义的全局已并入 baseline 受保护）。
	resetRuntimeGlobals(L)
	// 清除绑定的 context.Context（Robot.Start 通过 L.SetContext 绑定了已 cancel 的 ctx）。
	// 不清理的话，该 LState 被池内下一个 Robot 复用时会因继承已取消的 ctx 而立即 abort。
	L.RemoveContext()
	rp.pool.Put(L)
}

// ── 脚本入口函数缓存 + 全局表卫生 ──────────────────────────────
//
// 历史实现每次执行动作都 NewFunctionFromProto + PCall(0,0) 重跑整块 chunk 来
// （重新）定义 execute/onMessage，纯属浪费。现在改为：每个 LState 每个脚本的 chunk
// 只跑一次，把入口函数捕获进 registry 缓存、并移出全局表；后续直接取缓存函数调用。
//
// 配套：chunk 顶层定义的全局（函数定义、常量表等）在首次加载后并入 baseline 集合，
// Release 时只清理 baseline 之外的"运行时全局"，从而既保留脚本静态定义、又隔离
// Robot 之间的运行时污染。
//
// 注意：脚本顶层副作用由"每次动作"变为"每个 LState 首次加载一次"。正常脚本顶层只有
// 函数定义 / require，无影响；把可变状态写在顶层裸全局当作每次重置的写法属反模式，
// 应改用 robot.set/get。
const (
	scriptFnCachePrefix = "__sbfn_"          // 入口函数缓存键前缀
	globalBaselineKey   = "__sb_gbaseline__" // 全局表 baseline 集合键
)

func scriptFnCacheKey(scriptName, fnName string) string {
	return scriptFnCachePrefix + scriptName + "#" + fnName
}

// loadScriptFn 惰性加载脚本入口函数并缓存到当前 LState 的 registry。
// 首次命中时运行一次 chunk 捕获入口函数（execute / onMessage），随后从全局表移除，
// 并把 chunk 顶层产生的全局并入 baseline 受保护。后续调用直接返回缓存函数。
func (rp *RuntimePool) loadScriptFn(L *lua.LState, scriptName, fnName string) (lua.LValue, error) {
	reg := L.Get(lua.RegistryIndex)
	cacheKey := scriptFnCacheKey(scriptName, fnName)
	if v := L.GetField(reg, cacheKey); v != lua.LNil {
		return v, nil
	}

	compiled, ok := rp.precompiled[scriptName]
	if !ok {
		return nil, fmt.Errorf("脚本未预编译: %s", scriptName)
	}

	savedTop := L.GetTop()
	fn := L.NewFunctionFromProto(compiled)
	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		L.SetTop(savedTop)
		return nil, fmt.Errorf("加载脚本 %s 失败: %w", scriptName, err)
	}
	L.SetTop(savedTop)

	target := L.GetGlobal(fnName)
	if target == lua.LNil {
		return nil, fmt.Errorf("脚本 %s 未定义 %s 函数", scriptName, fnName)
	}
	L.SetGlobal(fnName, lua.LNil) // 入口函数移出全局表，避免污染
	L.SetField(reg, cacheKey, target)
	rememberGlobalBaseline(L) // chunk 顶层全局并入 baseline，Release 时不清理
	return target, nil
}

// rememberGlobalBaseline 把当前全局表的所有键并入 baseline 集合（registry 持有）。
func rememberGlobalBaseline(L *lua.LState) {
	reg := L.Get(lua.RegistryIndex)
	base, ok := L.GetField(reg, globalBaselineKey).(*lua.LTable)
	if !ok {
		base = L.NewTable()
		L.SetField(reg, globalBaselineKey, base)
	}
	globals, ok := L.Get(lua.GlobalsIndex).(*lua.LTable)
	if !ok {
		return
	}
	globals.ForEach(func(k, _ lua.LValue) {
		if ks, ok := k.(lua.LString); ok {
			base.RawSetString(string(ks), lua.LTrue)
		}
	})
}

// resetRuntimeGlobals 删除全局表中不在 baseline 内的键（即脚本运行期动态写入的全局）。
// baseline 尚未建立（该 LState 还没跑过脚本）时全局表为纯净 stdlib，直接返回。
func resetRuntimeGlobals(L *lua.LState) {
	reg := L.Get(lua.RegistryIndex)
	base, ok := L.GetField(reg, globalBaselineKey).(*lua.LTable)
	if !ok {
		return
	}
	globals, ok := L.Get(lua.GlobalsIndex).(*lua.LTable)
	if !ok {
		return
	}
	var toDelete []string
	globals.ForEach(func(k, _ lua.LValue) {
		if ks, ok := k.(lua.LString); ok {
			if base.RawGetString(string(ks)) == lua.LNil {
				toDelete = append(toDelete, string(ks))
			}
		}
	})
	for _, k := range toDelete {
		L.SetGlobal(k, lua.LNil)
	}
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
// Lua 脚本应定义 `function execute(r)` 函数并只返回 code：
//
//	return code -- 0=成功，非 0=失败
//
// send/recv WireBytes 由脚本内 network API 自动累计到 Context；脚本额外返回的 send/recv
// 会被忽略，避免重复统计。
func (rp *RuntimePool) RunActionScript(L *lua.LState, scriptName string) (code, send, recv int, timing engine.ActionTiming, err error) {
	ctx := GetContext(L)
	if ctx != nil {
		ctx.resetMetrics()
		defer func() {
			send, recv, timing = ctx.metrics()
		}()
	}

	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	executeFn, lerr := rp.loadScriptFn(L, scriptName, "execute")
	if lerr != nil {
		return -1, 0, 0, engine.ActionTiming{}, lerr
	}

	robotUD := createRobotUserData(L)
	if err = L.CallByParam(lua.P{Fn: executeFn, NRet: 1, Protect: true}, robotUD); err != nil {
		return -1, 0, 0, engine.ActionTiming{}, fmt.Errorf("执行脚本 %s 失败: %w", scriptName, err)
	}

	if L.GetTop() >= savedTop+1 {
		code = int(lua.LVAsNumber(L.Get(savedTop + 1)))
	}

	return code, 0, 0, engine.ActionTiming{}, nil
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
	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	executeFn, err := rp.loadScriptFn(L, scriptName, "execute")
	if err != nil {
		return false, err
	}

	robotUD := createRobotUserData(L)
	if err := L.CallByParam(lua.P{Fn: executeFn, NRet: 1, Protect: true}, robotUD); err != nil {
		return false, fmt.Errorf("执行脚本 %s 失败: %w", scriptName, err)
	}

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
// msg 为 proto 消息对象（LUserData）。回调内部 network API 的 WireBytes 自动累计到 Context。
func (rp *RuntimePool) RunCallbackScript(L *lua.LState, scriptName string, msgData []byte, s2cProto string) (send, recv int, timing engine.ActionTiming, err error) {
	ctx := GetContext(L)
	if ctx != nil {
		ctx.resetMetrics()
		defer func() {
			send, recv, timing = ctx.metrics()
		}()
	}

	// 保存栈顶，确保退出时恢复
	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	onMsgFn, err := rp.loadScriptFn(L, scriptName, "onMessage")
	if err != nil {
		return 0, 0, engine.ActionTiming{}, err
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
		return 0, 0, engine.ActionTiming{}, fmt.Errorf("执行回调脚本 %s 失败: %w", scriptName, err)
	}

	return 0, 0, engine.ActionTiming{}, nil
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
	L.PreloadModule("share", loadShareModule)
}

// robotMetatableKey robot 对象共享元表在 registry 中的键。
const robotMetatableKey = "__stressbot_robot_mt__"

// robotMetatable 返回当前 LState 上共享的 robot 对象元表，惰性创建并缓存到 registry，
// 避免每次脚本执行都新建一张元表 + 一个 closure。
func robotMetatable(L *lua.LState) *lua.LTable {
	reg := L.Get(lua.RegistryIndex)
	if v := L.GetField(reg, robotMetatableKey); v != lua.LNil {
		if mt, ok := v.(*lua.LTable); ok {
			return mt
		}
	}
	mt := L.NewTable()
	L.SetField(mt, "__index", L.NewFunction(robotIndex))
	L.SetField(reg, robotMetatableKey, mt)
	return mt
}

// createRobotUserData 创建 robot 对象（LUserData + metatable）
func createRobotUserData(L *lua.LState) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = GetContext(L)
	L.SetMetatable(ud, robotMetatable(L))
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
