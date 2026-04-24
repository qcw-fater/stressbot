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

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const registryCtxKey = "__stressbot_ctx__"

// Context Lua 脚本执行上下文。
type Context struct {
	RobotID   int
	Account   string
	Store     *state.Store
	Factory   *protox.Factory
	Adapter   adapter.Adapter
	NetSender engine.NetSender
	Ctx       context.Context
	LuaMu     *sync.Mutex
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
	// 清除上下文
	L.SetField(L.Get(lua.RegistryIndex), registryCtxKey, lua.LNil)
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
// Lua 脚本应定义 `function execute(r)` 函数。
// r 为 robot 对象，返回值为整数错误码（0 表示成功）。
func (rp *RuntimePool) RunActionScript(L *lua.LState, scriptName string) (int, error) {
	compiled, ok := rp.precompiled[scriptName]
	if !ok {
		return -1, fmt.Errorf("脚本未预编译: %s", scriptName)
	}

	// 保存栈顶，确保退出时恢复（防止栈泄漏）
	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	// 加载脚本（定义 execute 函数）
	fn := L.NewFunctionFromProto(compiled)
	L.Push(fn)
	if err := L.PCall(0, 0, nil); err != nil {
		return -1, fmt.Errorf("加载脚本 %s 失败: %w", scriptName, err)
	}

	// 获取 execute 函数
	executeFn := L.GetGlobal("execute")
	if executeFn == lua.LNil {
		return -1, fmt.Errorf("脚本 %s 未定义 execute 函数", scriptName)
	}

	// 创建 robot 对象并调用
	robotUD := createRobotUserData(L)
	if err := L.CallByParam(lua.P{Fn: executeFn, NRet: 1, Protect: true}, robotUD); err != nil {
		return -1, fmt.Errorf("执行脚本 %s 失败: %w", scriptName, err)
	}

	// 获取返回值
	code := 0
	if L.GetTop() > savedTop {
		ret := L.Get(-1)
		code = int(lua.LVAsNumber(ret))
	}

	// 清理全局中的 execute 函数，避免下次执行冲突
	L.SetGlobal("execute", lua.LNil)

	return code, nil
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
