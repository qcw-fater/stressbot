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

// ctxRegistrySlot Context 在 LState registry 表中的整数槽位。
//
// 用整数槽而非字符串键是热路径优化：registry 的整数索引直落 LTable 的 array 部分
// （一次切片索引），字符串键要走 strdict 的哈希查表。GetContext 是每一个 Lua→Go
// API 函数的第一行，这一次哈希摊在全部 API 调用上——BenchmarkGetContext 实测字符串
// 键约 25ns/次，而 BenchmarkLuaAPI_GetID 显示一次最简 API 调用总共才约 200ns。
//
// 槽位无冲突：gopher-lua 自身从不写 registry 的整数部分（只在 Get/Replace 里整表
// 存取），项目内其余 registry 访问（脚本入口缓存、robot 元表、全局 baseline）也全是
// 字符串键，两者分居 array 与 strdict，互不干扰。
const ctxRegistrySlot = 1

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
	// Resolver 按「server 串 <proto>:<service>」解析每条连接的 Go SchemaAdapter
	// 取代旧 Context.Adapter。业务 Lua API（buildPacket / doTCPRequest /
	// networkUDPSend / networkListen / headerErrDetail 等）通过 ctx.Resolver.Resolve
	// （"<proto>:<service>"）取该连接的 adapter 后调 Encode/ExpectedRouteKey/DescribeError；
	// Resolve nil 由调用方 fail loud（不静默兜底）。
	//
	// 与 r.resolver（Robot 持有的 CodecResolver）是同一对象，SetContext 注入；
	// 与 engine.ActionExecutor.resolver 共享同一份 codec 映射，encode/decode 双向一致。
	Resolver  adapter.CodecResolver
	NetSender engine.NetSender
	Ctx       context.Context

	// adapterCache Resolve 结果的每机器人缓存，见 ResolveAdapter。
	// 仅执行器 goroutine 访问（与 topThread / trampThreads 同一约束），无需加锁。
	adapterCache map[adapterCacheKey]adapter.Adapter

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

	// Waiter 协作式调度的等待后端（由 Robot 实现）：action 脚本协程在 await_* 处 yield 出
	// WaitSpec 后，drive-loop 调 Waiter.Await 让 Robot 在等待窗口内 drain 任务队列
	// （跑 listen 回调等），条件满足/超时后返回 WaitOutcome，再 resume 协程。
	// 为 nil 时脚本一旦 yield（调 await_*）即报错（未接入协作式调度）。
	Waiter Waiter
	// topThread 当前被调度器 Resume 的 action 协程线程。await_* 运行时据此校验
	// 「自己处于调度器直接 resume 的顶层协程」：L != topThread（用户 coroutine.create
	// 协程）即 fail-loud。仅执行器 goroutine 单线程读写，无需加锁。
	topThread *lua.LState

	// trampThreads 本 Robot 的长驻蹦床协程空闲缓存（P2，见 trampoline.go）：
	// 任务以 DONE-yield 干净收尾的 thread 停在栈顶回到这里复用，消除每次脚本执行
	// NewThread 的 registry/调用栈分配炒翻。仅执行器 goroutine 访问，无需加锁；
	// Robot 归还 LState（RuntimePool.Release）时统一关闭。
	trampThreads []*trampThread

	metricsMu        sync.Mutex
	currentTiming    engine.ActionTiming
	currentSendBytes int
	currentRecvBytes int
}

// adapterCacheKey Resolve 缓存键。用结构体而非拼好的字符串：结构体键查表不分配，
// 而拼接本身就是要消除的那次分配。
type adapterCacheKey struct {
	proto   string
	service string
}

// ResolveAdapter 取 <proto>:<service> 对应的 Go SchemaAdapter，结果按机器人缓存。
//
// CodecResolver.Resolve 的入参是拼好的 "<proto>:<service>" 串，这次拼接是一次堆分配，
// 摊在每次 udp_send / tcp_request / listen 上——CPU 剖面里 networkUDPSend 占 2.86%，
// 其中就有它。codec 映射在任务启动时定型、运行期不变，缓存安全。
//
// 未命中（Resolve 返回 nil）不入缓存：那是配置错误的 fail-loud 路径，本就罕见，
// 缓存它反而会让运行期补上的映射永远看不见。
func (c *Context) ResolveAdapter(proto, service string) adapter.Adapter {
	if c == nil || c.Resolver == nil {
		return nil
	}
	key := adapterCacheKey{proto: proto, service: service}
	if adp, ok := c.adapterCache[key]; ok {
		return adp
	}
	adp := c.Resolver.Resolve(proto + ":" + service)
	if adp == nil {
		return nil
	}
	if c.adapterCache == nil {
		c.adapterCache = make(map[adapterCacheKey]adapter.Adapter, 8)
	}
	c.adapterCache[key] = adp
	return adp
}

// resetMetrics 在每次 action 脚本开始前清零累加器。
func (c *Context) resetMetrics() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming = engine.ActionTiming{}
	c.currentSendBytes = 0
	c.currentRecvBytes = 0
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

// recordRequestFailure 记一次「发出去但没等回响应帧」的请求（超时 / 断连 / 发送失败）。
// 供 api_network 在请求层失败分支调用；客户端主动取消不算，服务端回了业务错误码也不算
// （那种有响应帧、WireRTT 有效，走 recordRequest）。
func (c *Context) recordRequestFailure() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming.AddFailedRequest()
	c.metricsMu.Unlock()
}

// recordListenHit 记一次监听命中的等待时长（可测性由 Waiter 判定，见 engine.ClassifyListenWait）。
func (c *Context) recordListenHit(wait time.Duration, kind engine.ListenWaitKind) {
	if c == nil || kind == engine.ListenWaitUnknown {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming.AddListenHit(wait, kind)
	c.metricsMu.Unlock()
}

// recordListenTimeout 记一次监听超时（不产等待时长样本，单独成率）。
func (c *Context) recordListenTimeout() {
	if c == nil {
		return
	}
	c.metricsMu.Lock()
	c.currentTiming.AddListenTimeout()
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
	// 切片字段必须深拷贝：currentTiming 下次 resetMetrics 后会被复用，
	// 直接共享底层数组会让上一次 action 的样本被下一次覆写。
	if len(c.currentTiming.Requests) > 0 {
		out.Requests = append([]engine.RequestTiming(nil), c.currentTiming.Requests...)
	}
	if len(c.currentTiming.ListenWaits) > 0 {
		out.ListenWaits = append([]time.Duration(nil), c.currentTiming.ListenWaits...)
	}
	return c.currentSendBytes, c.currentRecvBytes, out
}

// SetContext 将脚本上下文绑定到 LState 的 registry
func SetContext(L *lua.LState, ctx *Context) {
	ud := L.NewUserData()
	ud.Value = ctx
	if reg, ok := L.Get(lua.RegistryIndex).(*lua.LTable); ok {
		reg.RawSetInt(ctxRegistrySlot, ud)
	}
}

// clearContext 解除绑定（LState 归还池前调用），槽位置 LNil 后 GetContext 返回 nil。
func clearContext(L *lua.LState) {
	if reg, ok := L.Get(lua.RegistryIndex).(*lua.LTable); ok {
		reg.RawSetInt(ctxRegistrySlot, lua.LNil)
	}
}

// GetContext 从 LState 的 registry 获取脚本上下文
func GetContext(L *lua.LState) *Context {
	reg, ok := L.Get(lua.RegistryIndex).(*lua.LTable)
	if !ok {
		return nil
	}
	ud, ok := reg.RawGetInt(ctxRegistrySlot).(*lua.LUserData)
	if !ok {
		return nil
	}
	ctx, _ := ud.Value.(*Context)
	return ctx
}

// RuntimePool Lua 运行时池。
// 管理 LState 实例池和预编译的 FunctionProto。
// 每个 Robot 在生命周期内独占一个 LState，结束时归还。
type RuntimePool struct {
	pool        sync.Pool
	precompiled map[string]*lua.FunctionProto // scriptName -> 预编译函数
	scriptDir   string                        // 脚本根目录
	trampProto  *lua.FunctionProto            // 蹦床 chunk（P2，进程级编译一次）
}

// NewRuntimePool 创建 Lua 运行时池
func NewRuntimePool(scriptDir string) *RuntimePool {
	rp := &RuntimePool{
		precompiled: make(map[string]*lua.FunctionProto),
		scriptDir:   scriptDir,
		trampProto:  compileTrampoline(),
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
	// 关闭本 Robot 的蹦床协程缓存：缓存的 thread 持有从当前 Robot ctx 派生的子 ctx，
	// 不可带给池内下一个 Robot（thread 本体交给 GC）。
	if ctx := GetContext(L); ctx != nil {
		ctx.closeTrampThreads()
	}
	// 清除脚本上下文
	clearContext(L)
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
// （重新）定义 execute，纯属浪费。现在改为：每个 LState 每个脚本的 chunk
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
// 首次命中时运行一次 chunk 捕获指定入口函数（fnName），随后从全局表移除，
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
// Lua 脚本应定义 `function execute(r)` 并返回：
//
//	return nil            -- 成功
//	return robot.error(code, detail)  -- 失败（err table，code 由 robot.error 构造）
//	return <number>       -- 非法：旧式 return code 已废弃，fail loud
//
// send/recv WireBytes 由脚本内 network API 自动累计到 Context；脚本额外返回的 send/recv
// 会被忽略，避免重复统计。返回的 error 仅为脚本执行框架错误（加载/resume/Waiter）；
// 脚本业务失败经 err table 重建为 *engine.ActionError 返回。
func (rp *RuntimePool) RunActionScript(L *lua.LState, scriptName string) (send, recv int, timing engine.ActionTiming, err error) {
	ctx := GetContext(L)
	if ctx != nil {
		ctx.resetMetrics()
		defer func() {
			send, recv, timing = ctx.metrics()
		}()
	}

	// 协作式执行：脚本跑在子线程协程上，遇 await_* 时 yield 出 WaitSpec，
	// drive-loop 调 Waiter.Await 等待（窗口内 Robot drain 任务队列），再 resume。
	// 不含 await 的脚本首次 Resume 即 ResumeOK 跑完，行为与旧 CallByParam 等价。
	co, lerr := rp.startActionCoroutine(L, scriptName)
	if lerr != nil {
		return 0, 0, engine.ActionTiming{}, lerr
	}
	defer co.close()

	var resumeVals []lua.LValue
	for {
		res, rerr := rp.resumeCoroutine(co, ctx, resumeVals)
		if rerr != nil {
			return 0, 0, engine.ActionTiming{}, rerr
		}
		if res.done {
			// err-table 契约：nil 成功 / err table 失败 / 旧式 number fail loud。
			if code, detail, isErr := parseErrTable(res.ret); isErr {
				return 0, 0, engine.ActionTiming{}, buildActionError(code, detail, scriptName)
			}
			if res.ret != lua.LNil {
				return 0, 0, engine.ActionTiming{}, fmt.Errorf(
					"脚本 %s 返回非法值 %s：须返回 nil（成功）或 err table（失败），旧式 return code 已废弃",
					scriptName, res.ret.String())
			}
			return 0, 0, engine.ActionTiming{}, nil
		}

		// 协程在 await_* 处 yield：交给 Waiter 协作式等待，再把结果转回 Lua 返回值。
		if ctx == nil || ctx.Waiter == nil {
			return 0, 0, engine.ActionTiming{}, fmt.Errorf(
				"脚本 %s 调用 await_*，但运行时未接入协作式调度（Waiter 为 nil）", scriptName)
		}
		outcome, werr := ctx.Waiter.Await(res.wait)
		if werr != nil {
			return 0, 0, engine.ActionTiming{}, werr
		}
		resumeVals = rp.buildResumeVals(L, ctx, res.wait, outcome)
	}
}

// buildResumeVals 把 Waiter 的等待结果转成喂回协程的 Lua 返回值（成为 await_* 的返回值）。
func (rp *RuntimePool) buildResumeVals(L *lua.LState, ctx *Context, spec *WaitSpec, outcome WaitOutcome) []lua.LValue {
	switch spec.Kind {
	case WaitSleep:
		return nil // await_sleep 无返回值
	case WaitListen:
		return listenResultValues(L, ctx, spec, outcome)
	case WaitResponse:
		return requestResultValues(L, ctx, spec, outcome)
	case WaitIO:
		// 后台作业完成：在执行器 goroutine 上调用 renderer 产出 Lua 返回值。
		// renderer 为 nil（作业被放弃/panic）时返回空——脚本所有返回值取 nil，调用方据此当错误处理。
		if outcome.IORender != nil {
			return outcome.IORender(L)
		}
		return nil
	default:
		return nil
	}
}

// RunListenScript 执行 listen 脚本回调（协作式调度安全模型）。
//
// Lua 脚本应定义 `function on_message(r, msg)`：
//   - r   为 robot 句柄（与 execute(r) 同一句柄，可调用 robot/network/state 等 API）；
//   - msg 为按 ListenDef.s2cProto 解码后的字段表。
//
// 调用约束：本方法**必须**由 Robot 调度器在安全点（节点边界 / 等待窗口 drain，执行器
// goroutine = 业务 LState 唯一所有者）串行调用，绝不能在网络 pump goroutine 内执行——
// 否则会与主流程并发抢占同一 LState 导致栈损坏。返回值忽略；失败返回 error 供调用方记指标。
//
// 与 action 脚本一致跑在子线程协程上：on_message 内可直接调用 await_*（如 await_tcp_request
// 回应推送），遇 await 时 yield，由 Waiter 协作式等待后 resume。嵌套（回调 await 期间 drain
// 出另一回调）安全：resumeCoroutine 每次 resume 前重设 topThread。
func (rp *RuntimePool) RunListenScript(L *lua.LState, scriptName string, respMsg proto.Message) error {
	robotUD := createRobotUserData(L)
	var msgVal lua.LValue = lua.LNil
	if respMsg != nil {
		msgVal = protoMessageToLuaTable(L, respMsg)
	}
	return rp.runListenScriptValue(L, scriptName, robotUD, msgVal)
}

// RunListenScriptWire 以 wire 单遍直转表执行 listen 脚本回调（D2）：on_message 拿到
// 的 Lua table 与 RunListenScript（解码 + 整表 Lua 化）逐字一致，但 dynamicpb
// 解码树整个消失——脚本收表本就是每机器人一份，直转后广播去重缓存（FrozenCache）
// 在这条路上不再必要。返回 handled=false 表示直转被拒（影子采样失配 / 降级竞态 /
// 结构异常），调用方回落解码路径。
func (rp *RuntimePool) RunListenScriptWire(L *lua.LState, scriptName string, wv *protox.WireValue) (bool, error) {
	msgVal, ok := wireValueToLuaTable(L, wv)
	if !ok {
		return false, nil
	}
	return true, rp.runListenScriptValue(L, scriptName, createRobotUserData(L), msgVal)
}

// RunListenScriptRaw 执行未配置 s2cProto 的 listen 脚本回调，将原始消息体作为
// 二进制安全的 Lua string 传给 on_message(r, msg)。
func (rp *RuntimePool) RunListenScriptRaw(L *lua.LState, scriptName string, raw []byte) error {
	return rp.runListenScriptValue(L, scriptName, createRobotUserData(L), lua.LString(string(raw)))
}

func (rp *RuntimePool) runListenScriptValue(L *lua.LState, scriptName string, robotUD, msgVal lua.LValue) error {
	ctx := GetContext(L)

	co, lerr := rp.startCoroutine(L, scriptName, "on_message", robotUD, msgVal)
	if lerr != nil {
		return lerr
	}
	defer co.close()

	var resumeVals []lua.LValue
	for {
		res, rerr := rp.resumeCoroutine(co, ctx, resumeVals)
		if rerr != nil {
			return rerr
		}
		if res.done {
			return nil // on_message 返回值忽略
		}
		if ctx == nil || ctx.Waiter == nil {
			return fmt.Errorf("监听脚本 %s 调用 await_*，但运行时未接入协作式调度（Waiter 为 nil）", scriptName)
		}
		outcome, werr := ctx.Waiter.Await(res.wait)
		if werr != nil {
			return werr
		}
		resumeVals = rp.buildResumeVals(L, ctx, res.wait, outcome)
	}
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
	ctx := GetContext(L)

	// 与 action / listen 一致跑在子线程协程上：条件脚本同样可调用 await_*（如先 await 一次
	// 请求再据响应判定）。不含 await 时首次 Resume 即结束，行为与旧 CallByParam 等价。
	co, lerr := rp.startActionCoroutine(L, scriptName)
	if lerr != nil {
		return false, lerr
	}
	defer co.close()

	var resumeVals []lua.LValue
	for {
		res, rerr := rp.resumeCoroutine(co, ctx, resumeVals)
		if rerr != nil {
			return false, rerr
		}
		if res.done {
			if len(res.retVals) == 0 {
				return false, fmt.Errorf("布尔脚本 %s 未返回值，必须 return true/false", scriptName)
			}
			b, ok := res.retVals[0].(lua.LBool)
			if !ok {
				return false, fmt.Errorf("布尔脚本 %s 必须 return true/false，实际类型 %s", scriptName, res.retVals[0].Type().String())
			}
			return bool(b), nil
		}
		if ctx == nil || ctx.Waiter == nil {
			return false, fmt.Errorf("布尔脚本 %s 调用 await_*，但运行时未接入协作式调度（Waiter 为 nil）", scriptName)
		}
		outcome, werr := ctx.Waiter.Await(res.wait)
		if werr != nil {
			return false, werr
		}
		resumeVals = rp.buildResumeVals(L, ctx, res.wait, outcome)
	}
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

// registerAPIs 注册所有 Lua API 模块到 LState。
//
// 「adapter」Lua 模块已下线——业务 encode/decode 全走 Go CodecResolver
// （ctx.Resolver.Resolve），不再需要业务 LState 上的适配器脚本副本。conf/scripts 经 grep
// 确认零依赖 adapter 模块。
func registerAPIs(L *lua.LState) {
	L.PreloadModule("robot", loadRobotModule)
	L.PreloadModule("proto", loadProtoModule)
	L.PreloadModule("network", loadNetworkModule)
	L.PreloadModule("utils", loadUtilsModule)
	L.PreloadModule("log", loadLogModule)
	L.PreloadModule("json", loadJsonModule)
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
