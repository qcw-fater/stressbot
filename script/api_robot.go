package script

import (
	"fmt"
	"strconv"

	"stressbot/protox"
	"stressbot/state"

	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/proto"
)

const maxSafeInt = 1 << 53 // 9007199254740992，Lua double 可精确表示的最大整数

// loadRobotModule 加载 robot 命名空间模块。
// Lua 用法：
//
//	local r = require("robot")
//	r.get("key")           → 读取状态值
//	r.set("key", value)    → 写入状态值
//	r.has("key")           → 检查 key 是否存在
//	r.delete("key")        → 删除单个 key
//	r.clear("key")         → 删除指定 key（无参时清空全部）
//	r.increment("key")     → 原子递增并返回新值
//	r.keys()               → 返回所有 key 列表
//	r.get_path("a.b[0].c") → 按路径读取嵌套值
//	r.set_path("a.b[0].c", v) → 按路径写入嵌套值（自动创建中间 map）
//	r.get_id()             → 返回机器人编号
//	r.get_index()          → 返回任务全局序号（0-based，不含 startNumber 偏移）
//	r.get_account()        → 返回账号名
//	r.get_context()        → 检查 context 是否已取消
//	r.error(code, detail)  → 构造 err table
func loadRobotModule(L *lua.LState) int {
	mod := L.NewTable()

	// 读写
	L.SetField(mod, "get", L.NewFunction(robotGet))
	L.SetField(mod, "get_view", L.NewFunction(robotGetView))
	L.SetField(mod, "set", L.NewFunction(robotSet))
	L.SetField(mod, "has", L.NewFunction(robotHas))
	L.SetField(mod, "delete", L.NewFunction(robotDelete))
	L.SetField(mod, "clear", L.NewFunction(robotClear))
	L.SetField(mod, "increment", L.NewFunction(robotIncrement))
	L.SetField(mod, "keys", L.NewFunction(robotKeys))
	L.SetField(mod, "get_path", L.NewFunction(robotGetPath))
	L.SetField(mod, "set_path", L.NewFunction(robotSetPath))
	// 元信息
	L.SetField(mod, "get_id", L.NewFunction(robotGetID))
	L.SetField(mod, "get_index", L.NewFunction(robotGetIndex))
	L.SetField(mod, "get_account", L.NewFunction(robotGetAccount))
	L.SetField(mod, "get_context", L.NewFunction(robotGetContext))
	L.SetField(mod, "error", L.NewFunction(robotError))

	L.Push(mod)
	return 1
}

// robotIndex robot 对象的 __index 元方法。
// 支持 r:get("key")、r:set("key", val) 等方法调用。
func robotIndex(L *lua.LState) int {
	method := L.CheckString(2)
	switch method {
	case "get":
		L.Push(L.NewFunction(robotGet))
	case "get_view":
		L.Push(L.NewFunction(robotGetView))
	case "set":
		L.Push(L.NewFunction(robotSet))
	case "has":
		L.Push(L.NewFunction(robotHas))
	case "delete":
		L.Push(L.NewFunction(robotDelete))
	case "clear":
		L.Push(L.NewFunction(robotClear))
	case "increment":
		L.Push(L.NewFunction(robotIncrement))
	case "keys":
		L.Push(L.NewFunction(robotKeys))
	case "get_path":
		L.Push(L.NewFunction(robotGetPath))
	case "set_path":
		L.Push(L.NewFunction(robotSetPath))
	case "get_id":
		L.Push(L.NewFunction(robotGetID))
	case "get_index":
		L.Push(L.NewFunction(robotGetIndex))
	case "get_account":
		L.Push(L.NewFunction(robotGetAccount))
	case "get_context":
		L.Push(L.NewFunction(robotGetContext))
	case "error":
		L.Push(L.NewFunction(robotError))
	default:
		L.RaiseError("unknown robot method: %s", method)
	}
	return 1
}

// ---------------------------------------------------------------------------
// 读写
// ---------------------------------------------------------------------------

// robotGet robot.get(key) — 获取状态值
func robotGet(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNil)
		return 1
	}

	key := L.CheckString(L.GetTop())
	val := ctx.Store.Get(key)
	recordStateKeyGet("get", key, val)
	L.Push(goValueToLua(L, val))
	return 1
}

// robotGetView robot.get_view(key) — 以只读惰性视图"借阅"消息形态的状态值。
//
// 与 robot.get 的分工（也是给脚本作者的使用边界）：
//   - get：给你一棵独立 Lua 表，任何 Lua 语法随便用，可加工后 set 回——
//     成本是整树物化（∝ 树大小），适合"整份拿来加工/修改"；
//   - get_view：返回 wire 惰性视图 userdata（与 await_listen 给脚本的同一种
//     东西），proto.get_path/list_size/list_get/iter_list/serialize 按需扫描，
//     零物化零分配——适合"大消息只读挑着看"。视图永远只读，是当时那份不可变
//     字节的快照引用，key 被覆盖不影响已借出的视图。
//
// 形态不符（标量、脚本 set 的 Lua 表、被 set_path 改写出 Overlay 的 key）
// 一律大声报错指路，不静默给错形态。key 不存在返回 nil（与 get 一致，可判空）。
func robotGetView(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNil)
		return 1
	}

	key := L.CheckString(L.GetTop())
	val := ctx.Store.Get(key)
	recordStateKeyView(key)
	switch v := val.(type) {
	case nil:
		L.Push(lua.LNil)
	case *protox.WireValue:
		// schema 降级时视图仍可用：各访问器经 findWireView 检测降级后
		// 自动落到解码路径（现有机制），脚本无感知。
		L.Push(wrapWireView(L, v))
	case *protox.Frozen:
		L.Push(wrapFrozenMessage(L, v))
	default:
		L.RaiseError(
			"robot.get_view: key %q 的存储形态是 %T,不是可借阅的消息字节。"+
				"整表加工请用 robot.get(key);被 set_path 改写过(存在写覆盖层)的 key 同样请用 robot.get",
			key, val)
	}
	return 1
}

// robotSet robot.set(key, value) — 设置状态值
func robotSet(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}

	base := firstRobotArg(L)
	if L.GetTop() < base+1 {
		L.RaiseError("robot.set requires (key, value)")
		return 0
	}

	key := lua.LVAsString(L.Get(base))
	value := luaToGoStoreValue(L.Get(base + 1))
	ctx.Store.Set(key, value)
	return 0
}

// robotHas robot.has(key) — 检查 key 是否存在
func robotHas(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LBool(false))
		return 1
	}

	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.RaiseError("robot.has requires (key)")
		return 0
	}

	key := lua.LVAsString(L.Get(base))
	L.Push(lua.LBool(ctx.Store.Has(key)))
	return 1
}

// robotDelete robot.delete(key) — 删除单个 key（必须传 key）
func robotDelete(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.RaiseError("robot.delete requires (key)")
		return 0
	}
	key := lua.LVAsString(L.Get(base))
	ctx.Store.Delete(key)
	return 0
}

// robotClear robot.clear(key?) — 删除指定 key 或清空全部
func robotClear(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	base := firstRobotArg(L)
	if L.GetTop() < base {
		ctx.Store.Clear()
		return 0
	}
	key := lua.LVAsString(L.Get(base))
	ctx.Store.Delete(key)
	return 0
}

// robotIncrement robot.increment(key) — 原子递增
func robotIncrement(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNumber(0))
		return 1
	}

	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.RaiseError("robot.increment requires (key)")
		return 0
	}

	key := lua.LVAsString(L.Get(base))
	v := ctx.Store.Increment(key)
	L.Push(lua.LNumber(v))
	return 1
}

// robotKeys robot.keys() — 返回所有 key 列表
func robotKeys(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(L.CreateTable(0, 0))
		return 1
	}
	keys := ctx.Store.Keys()
	tb := L.CreateTable(len(keys), 0)
	for i, k := range keys {
		tb.RawSetInt(i+1, lua.LString(k))
	}
	L.Push(tb)
	return 1
}

// robotError 构造 err table 供脚本 return。
// Lua: robot.error(code, detail) → {code=number, detail=string}
func robotError(L *lua.LState) int {
	base := firstRobotArg(L)
	code := L.CheckInt(base)
	detail := L.CheckString(base + 1)
	L.Push(newErrTable(L, code, detail))
	return 1
}

// robotGetPath robot.get_path("a.b[0].c") — 按路径读取嵌套值
func robotGetPath(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		L.Push(lua.LNil)
		return 1
	}
	base := firstRobotArg(L)
	if L.GetTop() < base {
		L.Push(lua.LNil)
		return 1
	}
	path := lua.LVAsString(L.Get(base))
	val := ctx.Store.GetPath(path)
	recordStateKeyGet("get_path", path, val)
	L.Push(goValueToLua(L, val))
	return 1
}

// robotSetPath robot.set_path(path, value) — 按路径写入嵌套 map/list（自动创建中间节点）
func robotSetPath(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Store == nil {
		return 0
	}
	base := firstRobotArg(L)
	if L.GetTop() < base+1 {
		L.RaiseError("robot.set_path requires (path, value)")
		return 0
	}
	path := lua.LVAsString(L.Get(base))
	value := luaToGoStoreValue(L.Get(base + 1))
	ctx.Store.SetPath(path, value)
	return 0
}

// ---------------------------------------------------------------------------
// 元信息
// ---------------------------------------------------------------------------

// robotGetID robot.get_id() — 获取机器人编号
func robotGetID(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(ctx.RobotID))
	return 1
}

// robotGetIndex robot.get_index() — 获取任务全局序号（0-based，不含 startNumber 偏移）
func robotGetIndex(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(ctx.Index))
	return 1
}

// robotGetAccount robot.get_account() — 获取账号名
func robotGetAccount(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil {
		L.Push(lua.LString(""))
		return 1
	}
	L.Push(lua.LString(ctx.Account))
	return 1
}

// robotGetContext robot.get_context() — 检查 context 是否已取消
func robotGetContext(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Ctx == nil {
		L.Push(lua.LBool(false))
		return 1
	}
	L.Push(lua.LBool(ctx.Ctx.Err() != nil))
	return 1
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// firstRobotArg 返回首个业务参数的栈索引，避免每次调用都分配参数切片。
// 以方法形式调用（r:set(...)）时栈位 1 是 robot 的 LUserData（self），需跳过返回 2；
// 以模块形式调用（robot.set(...)）时首参即在栈位 1。
func firstRobotArg(L *lua.LState) int {
	if _, ok := L.Get(1).(*lua.LUserData); ok {
		return 2
	}
	return 1
}

// goValueToLua 将 Go 值转换为 Lua 值
func goValueToLua(L *lua.LState, val any) lua.LValue {
	if val == nil {
		return lua.LNil
	}
	switch v := val.(type) {
	case bool:
		return lua.LBool(v)
	case int:
		return lua.LNumber(v)
	case int32:
		return lua.LNumber(v)
	case int64:
		if v > maxSafeInt || v < -maxSafeInt {
			return lua.LString(strconv.FormatInt(v, 10))
		}
		return lua.LNumber(v)
	case uint:
		return lua.LNumber(v)
	case uint32:
		return lua.LNumber(v)
	case uint64:
		if v > maxSafeInt {
			return lua.LString(strconv.FormatUint(v, 10))
		}
		return lua.LNumber(v)
	case float64:
		return lua.LNumber(v)
	case float32:
		return lua.LNumber(v)
	case string:
		return lua.LString(v)
	case []byte:
		return lua.LString(string(v))
	case []any:
		// 精确预分配数组容量，避免 L.NewTable() 默认 32 槽 array + 32 槽 hash 的双重浪费
		tb := L.CreateTable(len(v), 0)
		for i, elem := range v {
			tb.RawSetInt(i+1, goValueToLua(L, elem))
		}
		return tb
	case map[string]any:
		// 仅预分配 hash 容量，省掉默认 32 槽 array 的 512 字节空转
		tb := L.CreateTable(0, len(v))
		for k, elem := range v {
			tb.RawSetString(k, goValueToLua(L, elem))
		}
		return tb
	case *protox.Frozen:
		// 整存 proto 引用（P1a）：现场转真 Lua table。protoMessageToLuaTable 与
		// GetFieldMap 同一套跳过/默认值规则，脚本看到的 table 与旧的展开 map 版逐字一致
		// （type(v)=="table"、proto3 默认值字段在场、可自由遍历/改写副本）。
		// 转换产物是临时 Lua 对象，Go 侧不再为整存消息常驻装箱树。
		return protoMessageToLuaTable(L, v.Message())
	case *protox.WireValue:
		// wire-first 整存值：默认 wire 单遍直转 Lua table（WalkWire，零 dynamicpb
		// 中间树）；schema 降级 / 影子采样失配 / 结构损坏回落解码路径。
		// 解码也失败理论不可达（存储点已结构校验），防御性返回 nil。
		if lv, ok := wireValueToLuaTable(L, v); ok {
			return lv
		}
		msg, err := v.Message()
		if err != nil {
			return lua.LNil
		}
		return protoMessageToLuaTable(L, msg)
	case state.ValueMaterializer:
		// Overlay（wire 基座 + 脚本覆盖写）等惰性容器：全量物化为纯 Go 树后转 table。
		return goValueToLua(L, v.MaterializeValue())
	default:
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

// luaToGoStoreValue 是 robot.set / robot.set_path 专用的入口转换：与 luaToGoValue
// 的差别是 proto 消息 userdata 存为 state 可导航的紧凑形态而非裸消息：
//
//   - 响应句柄（*respHandle，网络层解码产物）→ *protox.WireValue（wire-first 核心收益）：
//     未改写（dirty=false）时直接复用响应的原始 body 快照，零重编码零解码树常驻；
//     已被 set_field 改写时 proto.Marshal 重编码为当前内容的 wire 字节。
//     每机器人独有的大消息（如 playerData）常驻由 ~600KB 解码树塌缩为 ~74KB 字节。
//   - 影子验证降级的 schema / 编码失败 → 回落 *protox.Frozen（解码引用）。
//   - 共享只读消息（*protox.Frozen）→ 原样存引用。
//   - 其它裸 proto.Message（proto.create/parse 产物）→ Frozen 引用（行为不变）。
//
// 语义注意：存表是快照（深转换），存消息按「存入时内容」固化——WireValue 是字节快照，
// 脚本此后对同一 userdata 调 proto.set_field 不会反映到 state（现有脚本无此用法；
// 旧 Frozen 引用形态在此用法下行为本就未定义）。
func luaToGoStoreValue(v lua.LValue) any {
	if ud, ok := v.(*lua.LUserData); ok {
		switch m := ud.Value.(type) {
		case *respHandle:
			return respHandleStoreValue(m)
		case *protox.Frozen:
			return m
		case *protox.WireValue:
			// wire 惰性视图（listen 消费）→ 原样存引用：字节不可变、可安全共享，
			// 留存形态即 wire-first 目标形态（零转换零复制）。
			return m
		case proto.Message:
			return protox.Freeze(m)
		}
	}
	return luaToGoValue(v)
}

// respHandleStoreValue 把响应句柄转为 state 存储形态（见 luaToGoStoreValue）。
func respHandleStoreValue(h *respHandle) any {
	if h.msg == nil {
		return nil
	}
	desc := h.msg.ProtoReflect().Descriptor()
	if protox.WireDegraded(desc) {
		return protox.Freeze(h.msg)
	}
	if h.raw != nil && h.dirty != nil && !*h.dirty {
		return protox.NewWireValue(desc, h.raw)
	}
	data, err := proto.Marshal(h.msg)
	if err != nil {
		return protox.Freeze(h.msg)
	}
	return protox.NewWireValue(desc, data)
}

// luaToGoValue 将 Lua 值转换为 Go 值
func luaToGoValue(v lua.LValue) any {
	switch v.Type() {
	case lua.LTNil:
		return nil
	case lua.LTBool:
		return bool(v.(lua.LBool))
	case lua.LTNumber:
		return float64(v.(lua.LNumber))
	case lua.LTString:
		return string(v.(lua.LString))
	case lua.LTTable:
		tb := v.(*lua.LTable)
		isArray := true
		maxIdx := 0
		count := 0
		tb.ForEach(func(k, val lua.LValue) {
			count++
			if k.Type() == lua.LTNumber {
				idx := int(lua.LVAsNumber(k))
				if idx > maxIdx {
					maxIdx = idx
				}
			} else {
				isArray = false
			}
		})

		// 仅"稠密"表按 maxIdx 分配：isArray 只保证键全为数字，maxIdx 可能远大于实际元素数。
		// 稀疏表（如 {[1e9]=1}：count=1 而 maxIdx=1e9）若按 maxIdx 分配会申请十亿级切片 → OOM。
		// maxIdx == count 表示下标连续 1..N，分配量恒等于元素数；否则退化为 map（键转字符串）。
		if isArray && maxIdx > 0 && maxIdx == count {
			arr := make([]any, maxIdx)
			tb.ForEach(func(k, val lua.LValue) {
				idx := int(lua.LVAsNumber(k)) - 1
				if idx >= 0 && idx < maxIdx {
					arr[idx] = luaToGoValue(val)
				}
			})
			return arr
		}

		// 按实际键数预分配 map，避免边写边扩容的多次 rehash
		m := make(map[string]any, count)
		tb.ForEach(func(k, val lua.LValue) {
			m[lua.LVAsString(k)] = luaToGoValue(val)
		})
		return m
	case lua.LTUserData:
		if ud, ok := v.(*lua.LUserData); ok {
			switch m := ud.Value.(type) {
			case *protox.Frozen:
				// 共享只读消息：robot.set 存 Frozen 引用本身（与 P1a 整存形态一致，
				// state 免展开、跨机器人共享零拷贝，路径读取走 PathNavigator 惰性取值）。
				return m
			case *respHandle:
				// 响应句柄：通用值语义透传底层消息（proto.set_field 的嵌套赋值等）。
				return m.msg
			case proto.Message:
				return m
			}
		}
		return nil
	default:
		return nil
	}
}
