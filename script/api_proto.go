package script

import (
	"strconv"

	"stressbot/protocol/protox"

	lua "github.com/yuin/gopher-lua"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// loadProtoModule 加载 proto 命名空间模块。
// Lua 用法：
//
//	local proto = require("proto")
//	local msg = proto.create("example.RequestC2S")
//	proto.set_field(msg, "PlayerId", 10001)
//	local playerId = proto.get_field(msg, "PlayerId")
//	local bytes = proto.serialize(msg)
//	local parsed = proto.parse("example.ResponseS2C", bytes)
//	local fields = proto.get_field_map(msg)
//	for i, item in proto.iter_list(msg, "items") do ... end
//	local n = proto.list_size(msg, "items")
//	local item = proto.list_get(msg, "items", 1)
func loadProtoModule(L *lua.LState) int {
	mod := L.NewTable()

	L.SetField(mod, "create", L.NewFunction(protoCreate))
	L.SetField(mod, "set_field", L.NewFunction(protoSetField))
	L.SetField(mod, "get_field", L.NewFunction(protoGetField))
	L.SetField(mod, "get_path", L.NewFunction(protoGetField))
	L.SetField(mod, "serialize", L.NewFunction(protoSerialize))
	L.SetField(mod, "parse", L.NewFunction(protoParse))
	L.SetField(mod, "get_field_map", L.NewFunction(protoGetFieldMap))
	L.SetField(mod, "iter_list", L.NewFunction(protoIterList))
	L.SetField(mod, "list_size", L.NewFunction(protoListSize))
	L.SetField(mod, "list_get", L.NewFunction(protoListGet))

	L.Push(mod)
	return 1
}

// protoMsgIndex proto 消息对象的 __index 元方法
func protoMsgIndex(L *lua.LState) int {
	method := L.CheckString(2)
	switch method {
	case "set_field":
		L.Push(L.NewFunction(protoSetField))
	case "get_field":
		L.Push(L.NewFunction(protoGetField))
	case "get_path":
		L.Push(L.NewFunction(protoGetField))
	case "serialize":
		L.Push(L.NewFunction(protoSerialize))
	case "get_field_map":
		L.Push(L.NewFunction(protoGetFieldMap))
	default:
		// 教学报错：误把消息/视图当 Lua 表点字段（view.foo）时指路，而不是
		// 只报"未知方法"。pairs/ipairs/# 等表原语对 userdata 同样不可用。
		L.RaiseError(
			"proto 消息/wire 视图不支持表语法访问 %q。只读挑字段请用 proto.get_path(msg, \"a.b\")、"+
				"列表请用 proto.list_size/list_get/iter_list;需要整表加工请改用 robot.get(key)",
			method)
	}
	return 1
}

// protoMsgNewIndex __newindex 元方法：消息/视图 userdata 禁止赋值语法。
func protoMsgNewIndex(L *lua.LState) int {
	L.RaiseError(
		"proto 消息/wire 视图不支持赋值语法(msg.field = v)。可写消息请用 proto.set_field;" +
			"wire 视图只读——修改状态请 robot.get 取表加工后 robot.set 回,或 robot.set_path 精确改点")
	return 0
}

// respHandle 网络响应消息的 Lua 包装载荷（wire-first）：解码消息 + 原始 wire 快照 + 脏标记。
//
// raw 是响应 body 的独立快照：脚本对未改写的响应做 robot.set(resp) 时，
// luaToGoStoreValue 直接用 raw 构造 *protox.WireValue 存入 state（零重编码）。
// dirty 由全部同根包装（含 list_get/iter_list 取出的子消息包装）共享：任何一处
// set_field 都会置脏，置脏后 raw 不再可信，存储回落 proto.Marshal 重编码。
type respHandle struct {
	msg   proto.Message
	raw   []byte // 原始 body 独立快照；nil 表示不可用（子包装 / 无 body）
	dirty *bool  // 同根共享；nil 等价恒脏（防御）
}

// unwrapProtoUD 解出 userdata 里的 proto 消息形态。
// 返回 (底层消息, 只读性, 响应句柄)；句柄仅 respHandle 包装非 nil。
func unwrapProtoUD(v lua.LValue) (proto.Message, bool, *respHandle) {
	ud, ok := v.(*lua.LUserData)
	if !ok {
		return nil, false, nil
	}
	switch m := ud.Value.(type) {
	case *respHandle:
		return m.msg, false, m
	case *protox.Frozen:
		return m.Message(), true, nil
	case *protox.WireValue:
		// wire 视图的解码兜底：只在无 wire 分支的访问器（或 schema 降级）走到，
		// 现场解码为只读消息（视图语义等价 Frozen）。
		msg, err := m.Message()
		if err != nil {
			return nil, false, nil
		}
		return msg, true, nil
	case proto.Message:
		return m, false, nil
	}
	return nil, false, nil
}

// checkProtoMsg 从栈中获取 proto.Message（跳过 self 参数）。
// 共享只读消息（*protox.Frozen）与响应句柄（*respHandle）透传底层消息。
func checkProtoMsg(L *lua.LState) proto.Message {
	msg, _, _ := checkProtoMsgFull(L)
	return msg
}

// checkProtoMsgRO 从栈中获取 proto 消息及其只读性（跳过 self 参数）。
// readonly=true 表示消息来自广播去重的共享 *Frozen（多机器人共享同一份解码结果），
// 调用方返回其子消息时必须保持只读传播（继续包 Frozen），绝不能交出可变包装。
func checkProtoMsgRO(L *lua.LState) (proto.Message, bool) {
	msg, readonly, _ := checkProtoMsgFull(L)
	return msg, readonly
}

// checkProtoMsgFull 从栈中获取 proto 消息、只读性与响应句柄（跳过 self 参数）。
func checkProtoMsgFull(L *lua.LState) (proto.Message, bool, *respHandle) {
	top := L.GetTop()
	for i := 1; i <= top; i++ {
		if msg, readonly, h := unwrapProtoUD(L.Get(i)); msg != nil {
			return msg, readonly, h
		}
	}
	L.RaiseError("expected proto message (userdata)")
	return nil, false, nil
}

// protoMsgMetatableKey proto 消息共享元表在 registry 中的键。
const protoMsgMetatableKey = "__stressbot_proto_msg_mt__"

// protoMsgMetatable 返回当前 LState 上共享的 proto 消息元表，
// 惰性创建并缓存到 registry。所有 proto 消息共用同一张元表（__index 固定指向
// protoMsgIndex），避免每次收包都新建一张表 + 一个 closure。
func protoMsgMetatable(L *lua.LState) *lua.LTable {
	reg := L.Get(lua.RegistryIndex)
	if v := L.GetField(reg, protoMsgMetatableKey); v != lua.LNil {
		if mt, ok := v.(*lua.LTable); ok {
			return mt
		}
	}
	mt := L.NewTable()
	L.SetField(mt, "__index", L.NewFunction(protoMsgIndex))
	L.SetField(mt, "__newindex", L.NewFunction(protoMsgNewIndex))
	L.SetField(reg, protoMsgMetatableKey, mt)
	return mt
}

// wrapProtoMessage 将 proto.Message 包装为带 __index 元方法的 LUserData。
func wrapProtoMessage(L *lua.LState, msg proto.Message) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = msg
	L.SetMetatable(ud, protoMsgMetatable(L))
	return ud
}

// wrapRespMessage 将网络响应消息连同原始 body 快照包装为 LUserData（wire-first）。
// raw 必须是独立快照（调用方负责复制）；后续 robot.set(resp) 未改写时直接以 raw
// 存 WireValue。dirty 标记现场新建，同根子包装共享。
func wrapRespMessage(L *lua.LState, msg proto.Message, raw []byte) *lua.LUserData {
	ud := L.NewUserData()
	dirty := false
	ud.Value = &respHandle{msg: msg, raw: raw, dirty: &dirty}
	L.SetMetatable(ud, protoMsgMetatable(L))
	return ud
}

// wrapFrozenMessage 将共享只读消息（*protox.Frozen，广播去重产物）包装为与普通
// proto 消息同元表的 LUserData：读访问器（get_field/get_path/list_*/serialize/
// get_field_map）经 checkProtoMsgRO 透传底层消息；set_field 检测到 Frozen 直接
// RaiseError（fail-loud，防止改写污染其他机器人共享的同一份解码结果）；
// list_get/iter_list 返回的子消息继续包 Frozen，只读性全树传播。
func wrapFrozenMessage(L *lua.LState, fz *protox.Frozen) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = fz
	L.SetMetatable(ud, protoMsgMetatable(L))
	return ud
}

// wrapWireView 将 wire 惰性视图（*protox.WireValue）包装为与普通 proto 消息
// 同元表的 LUserData（D2）：listen 消费不再整包解码，读访问器按需 wire 扫描
// （get_field/get_path → GetFieldCompat，list_* → List*Compat，get_field_map →
// 直转器，serialize → 原始字节）；set_field 与 Frozen 同款 fail-loud。
// list_get/iter_list 的 message 元素继续包视图，只读性全树传播。
// schema 降级后视图自动回落：unwrapProtoUD 现场解码为只读消息走原反射路径。
func wrapWireView(L *lua.LState, wv *protox.WireValue) *lua.LUserData {
	ud := L.NewUserData()
	ud.Value = wv
	L.SetMetatable(ud, protoMsgMetatable(L))
	return ud
}

// findWireView 在栈上找未降级的 wire 视图（各读访问器的 wire 分支入口）。
// 降级 schema 的视图返回 nil——调用方落到 checkProtoMsg 路径，由 unwrapProtoUD
// 现场解码走原反射实现（正确性优先于形态）。
func findWireView(L *lua.LState) *protox.WireValue {
	top := L.GetTop()
	for i := 1; i <= top; i++ {
		if ud, ok := L.Get(i).(*lua.LUserData); ok {
			if wv, ok2 := ud.Value.(*protox.WireValue); ok2 {
				if protox.WireDegraded(wv.Desc()) {
					return nil
				}
				return wv
			}
		}
	}
	return nil
}

// protoCreate proto.create(name) — 创建动态 proto 消息
func protoCreate(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.RaiseError("proto factory not available")
		return 0
	}

	name := L.CheckString(1)
	msg, err := ctx.Factory.Create(name)
	if err != nil {
		L.RaiseError("create proto message failed: %v", err)
		return 0
	}

	ud := wrapProtoMessage(L, msg)

	L.Push(ud)
	return 1
}

// protoSetField proto.set_field(msg, field, value) — 设置 proto 字段
// value 支持标量、表、以及嵌套 proto 消息（LUserData）
func protoSetField(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.RaiseError("proto factory not available")
		return 0
	}

	// 共享只读消息（广播去重的 *Frozen / wire 惰性视图 *WireValue）禁止参与
	// set_field：既不能作为改写目标（污染共享数据 / 破坏视图不可变契约），也不能
	// 作为嵌套赋值的 value（会让共享消息获得可变父级，间接失去只读保障）。
	// fail-loud，需要可写副本请用 proto.parse。
	for i := 1; i <= L.GetTop(); i++ {
		if ud, ok := L.Get(i).(*lua.LUserData); ok {
			switch ud.Value.(type) {
			case *protox.Frozen, *protox.WireValue:
				L.RaiseError("proto.set_field: 消息为共享只读（广播去重/wire 视图），禁止修改；需要可写副本请用 proto.parse 重新解析")
				return 0
			}
		}
	}

	msg, _, handle := checkProtoMsgFull(L)
	if msg == nil {
		return 0
	}

	// 找到 msg 在栈中的位置，然后按相对位置取参数
	msgIdx := 0
	for i := 1; i <= L.GetTop(); i++ {
		if m, _, _ := unwrapProtoUD(L.Get(i)); m != nil {
			msgIdx = i
			break
		}
	}

	fieldName := L.CheckString(msgIdx + 1)
	fieldValue := L.CheckAny(msgIdx + 2)

	// 如果 value 是 LUserData 且包含 proto 消息，直接传递（用于嵌套消息）
	var goVal any
	if subMsg, _, _ := unwrapProtoUD(fieldValue); subMsg != nil {
		goVal = subMsg
	} else {
		goVal = luaToGoValue(fieldValue)
	}

	if err := ctx.Factory.SetField(msg, fieldName, goVal); err != nil {
		L.RaiseError("set field %s failed: %v", fieldName, err)
	}
	// 响应句柄被改写：置脏（同根共享），此后存储不得再复用原始 raw 快照。
	if handle != nil && handle.dirty != nil {
		*handle.dirty = true
	}

	return 0
}

// protoGetField proto.get_field(msg, field) — 获取 proto 字段值
func protoGetField(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}

	// wire 视图：按需扫描（GetFieldCompat 语义 ≡ Factory.GetField，含错误行为）。
	if wv := findWireView(L); wv != nil {
		fieldName := findFirstStringArg(L)
		if fieldName == "" {
			L.RaiseError("proto.get_field requires (msg, field)")
			return 0
		}
		val, err := wv.GetFieldCompat(fieldName)
		if err != nil {
			L.RaiseError("get field %s failed: %v", fieldName, err)
			return 0
		}
		L.Push(goValueToLua(L, val))
		return 1
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}

	// 提取字段名参数
	var fieldName string
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if fieldName == "" {
			fieldName = lua.LVAsString(v)
		}
	}

	if fieldName == "" {
		L.RaiseError("proto.get_field requires (msg, field)")
		return 0
	}

	val, err := ctx.Factory.GetField(msg, fieldName)
	if err != nil {
		L.RaiseError("get field %s failed: %v", fieldName, err)
		return 0
	}

	L.Push(goValueToLua(L, val))
	return 1
}

// protoSerialize proto.serialize(msg) — 序列化 proto 消息为字节
func protoSerialize(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}

	// wire 视图：直接返回原始字节（免解码免重编码；解码后重编码与原始字节
	// 语义等价——两者 Unmarshal 结果一致）。
	if wv := findWireView(L); wv != nil {
		L.Push(lua.LString(string(wv.Raw())))
		return 1
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}

	data, err := ctx.Factory.Serialize(msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err)
		return 0
	}

	L.Push(lua.LString(string(data)))
	return 1
}

// protoParse proto.parse(name, data) — 反序列化字节为 proto 消息
func protoParse(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.RaiseError("proto factory not available")
		return 0
	}

	// 提取 name 和 data 参数
	var name string
	var data []byte

	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if name == "" {
			name = lua.LVAsString(v)
		} else if data == nil {
			data = []byte(lua.LVAsString(v))
		}
	}

	if name == "" || data == nil {
		L.RaiseError("proto.parse requires (name, data)")
		return 0
	}

	msg, err := ctx.Factory.Parse(name, data)
	if err != nil {
		L.RaiseError("parse proto %s failed: %v", name, err)
		return 0
	}

	ud := wrapProtoMessage(L, msg)

	L.Push(ud)
	return 1
}

// protoGetFieldMap proto.get_field_map(msg) — 获取所有字段（返回 table）
// 单遍展开：proto → Lua table，跳过中间 map[string]any 层。
func protoGetFieldMap(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}

	// wire 视图：直转器整树建表（零 dynamicpb 中间树）；被拒（采样失配/损坏）
	// 落到 checkProtoMsg 的解码兜底。
	if wv := findWireView(L); wv != nil {
		if result, ok := wireValueToLuaTable(L, wv); ok {
			L.Push(result)
			return 1
		}
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}

	result := protoMessageToLuaTable(L, msg)

	L.Push(result)
	return 1
}

// protoIterList proto.iter_list(msg, field) → iterator
// for idx, item in proto.iter_list(msg, "items") do ... end
// item 为子 proto 消息（LUserData）。
func protoIterList(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}
	if wv := findWireView(L); wv != nil {
		// wire 视图：列表游标——一遍收集元素跨度，message 元素惰性产出子视图
		//（零解码，脚本读哪个字段才扫哪个字段），与 list_get 的子视图语义对齐。
		// 出错推 nil、非 list 字段空迭代，行为与解码分支一致。
		fieldName := findFirstStringArg(L)
		if fieldName == "" {
			L.RaiseError("proto.iter_list requires (msg, field)")
			return 0
		}
		cur, err := wv.ListCursorCompat(fieldName)
		if err != nil {
			L.Push(lua.LNil)
			return 1
		}
		idx := 0
		iter := L.NewFunction(func(L *lua.LState) int {
			if idx >= cur.Len() {
				L.Push(lua.LNil)
				return 1
			}
			item := cur.Item(idx)
			idx++
			L.Push(lua.LNumber(idx))
			if sub, ok := item.(*protox.WireValue); ok {
				L.Push(wrapWireView(L, sub))
			} else {
				L.Push(goValueToLua(L, item))
			}
			return 2
		})
		L.Push(iter)
		return 1
	}

	msg, readonly, handle := checkProtoMsgFull(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}
	fieldName := findFirstStringArg(L)
	if fieldName == "" {
		L.RaiseError("proto.iter_list requires (msg, field)")
		return 0
	}
	// 解码路径与 wire 游标语义逐字对齐（含 schema 降级回落时，脚本可见形态
	// 不得漂移）：message 元素产出 userdata（≡ list_get），只读性随父传播；
	// 未知字段/非法路径 → nil；非 repeated（标量/map）→ 空迭代。
	n, err := ctx.Factory.GetListLen(msg, fieldName)
	if err != nil {
		if _, ferr := ctx.Factory.GetField(msg, fieldName); ferr != nil {
			L.Push(lua.LNil)
			return 1
		}
		n = 0 // 字段可读但非 repeated → 空迭代（iter_list 历史行为）
	}
	idx := 0
	iter := L.NewFunction(func(L *lua.LState) int {
		if idx >= n {
			L.Push(lua.LNil)
			return 1
		}
		item, ierr := ctx.Factory.GetListItem(msg, fieldName, idx)
		idx++
		if ierr != nil {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LNumber(idx))
		// 嵌套 proto 消息保留为 userdata；共享只读消息的子消息保持只读传播
		if pm, ok := item.(proto.Message); ok {
			L.Push(wrapChildMessage(L, pm, readonly, handle))
		} else {
			L.Push(goValueToLua(L, item))
		}
		return 2
	})
	L.Push(iter)
	return 1
}

// protoListSize proto.list_size(msg, field) → 返回 list 字段长度
func protoListSize(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	// wire 视图：单遍扫描计数，不展开元素。
	if wv := findWireView(L); wv != nil {
		n, err := wv.ListLenCompat(findFirstStringArg(L))
		if err != nil {
			L.Push(lua.LNumber(0))
			return 1
		}
		L.Push(lua.LNumber(n))
		return 1
	}

	msg := checkProtoMsg(L)
	if msg == nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	fieldName := findFirstStringArg(L)
	n, err := ctx.Factory.GetListLen(msg, fieldName)
	if err != nil {
		L.Push(lua.LNumber(0))
		return 1
	}
	L.Push(lua.LNumber(n))
	return 1
}

// protoListGet proto.list_get(msg, field, idx) → 返回 list 字段第 idx 项 (1-based)
func protoListGet(L *lua.LState) int {
	ctx := GetContext(L)
	if ctx == nil || ctx.Factory == nil {
		L.Push(lua.LNil)
		return 1
	}
	// wire 视图：message 元素返回子视图（字节段共享父快照，只读性全树传播）。
	if wv := findWireView(L); wv != nil {
		fieldName, idx := findStringAndIndexArgs(L)
		item, err := wv.ListItemCompat(fieldName, idx-1)
		if err != nil {
			L.Push(lua.LNil)
			return 1
		}
		if sub, ok := item.(*protox.WireValue); ok {
			L.Push(wrapWireView(L, sub))
		} else {
			L.Push(goValueToLua(L, item))
		}
		return 1
	}

	msg, readonly, handle := checkProtoMsgFull(L)
	if msg == nil {
		L.Push(lua.LNil)
		return 1
	}
	fieldName, idx := findStringAndIndexArgs(L)
	i := idx - 1
	item, err := ctx.Factory.GetListItem(msg, fieldName, i)
	if err != nil {
		L.Push(lua.LNil)
		return 1
	}
	if pm, ok := item.(proto.Message); ok {
		L.Push(wrapChildMessage(L, pm, readonly, handle))
	} else {
		L.Push(goValueToLua(L, item))
	}
	return 1
}

// findStringAndIndexArgs 收集非 userdata 参数里的 (字段名, 下标)（list_get 用）。
func findStringAndIndexArgs(L *lua.LState) (string, int) {
	var fieldName string
	idx := -1
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if fieldName == "" {
			fieldName = lua.LVAsString(v)
		} else if idx < 0 {
			idx = int(lua.LVAsNumber(v))
		}
	}
	return fieldName, idx
}

// wrapChildMessage 包装从父消息取出的子消息：父为共享只读（Frozen）时子消息
// 继续包 Frozen（只读性全树传播，堵住"经子消息 set_field 改写共享树"的旁路）；
// 父为响应句柄时子消息共享父的脏标记（子消息 set_field 会改写同一底层树，
// 必须让父句柄的 raw 快照同步失效），且不携带 raw（子消息无独立字节段）；
// 否则按普通可变消息包装。
func wrapChildMessage(L *lua.LState, pm proto.Message, readonly bool, parent *respHandle) *lua.LUserData {
	if readonly {
		return wrapFrozenMessage(L, protox.Freeze(pm))
	}
	if parent != nil {
		ud := L.NewUserData()
		ud.Value = &respHandle{msg: pm, raw: nil, dirty: parent.dirty}
		L.SetMetatable(ud, protoMsgMetatable(L))
		return ud
	}
	return wrapProtoMessage(L, pm)
}

// findFirstStringArg 从参数列表中找第一个字符串参数（跳过 userdata）
func findFirstStringArg(L *lua.LState) string {
	for i := 1; i <= L.GetTop(); i++ {
		v := L.Get(i)
		if _, ok := v.(*lua.LUserData); ok {
			continue
		}
		if v.Type() == lua.LTString {
			return string(v.(lua.LString))
		}
	}
	return ""
}

// protoMessageToLuaTable 将 proto.Message 直接转换为 Lua LTable，
// 跳过中间 map[string]any 层，省掉一次完整的 Go 对象树分配。
func protoMessageToLuaTable(L *lua.LState, msg proto.Message) *lua.LTable {
	ref := msg.ProtoReflect()

	fields := ref.Descriptor().Fields()
	// 用字段总数作为 hash 容量上限（实际会跳过未设置字段，略有富余但远优于默认 32 槽，
	// 且 message 表无数组部分，省掉默认 array 的 512 字节）
	result := L.CreateTable(0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// 跳过未设置的非 repeated message 字段（与 factory.GetFieldMap 一致）
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() && !ref.Has(fd) {
			continue
		}
		// 跳过空的 repeated/map 字段
		if (fd.IsList() || fd.IsMap()) && !ref.Has(fd) {
			continue
		}

		val := ref.Get(fd)
		lv := protoFieldToLua(L, fd, val)
		result.RawSetString(string(fd.Name()), lv)
	}

	return result
}

// protoFieldToLua 将单个 proto 字段值转换为 lua.LValue。
func protoFieldToLua(L *lua.LState, field protoreflect.FieldDescriptor, val protoreflect.Value) lua.LValue {
	// repeated 字段
	if field.IsList() {
		list := val.List()
		tb := L.CreateTable(list.Len(), 0)
		for i := 0; i < list.Len(); i++ {
			tb.RawSetInt(i+1, protoScalarToLua(L, field, list.Get(i)))
		}
		return tb
	}

	// map 字段
	if field.IsMap() {
		m := val.Map()
		tb := L.CreateTable(0, m.Len())
		m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			tb.RawSetString(k.String(), protoScalarToLua(L, field.MapValue(), v))
			return true
		})
		return tb
	}

	return protoScalarToLua(L, field, val)
}

// protoScalarToLua 将标量 proto 值转换为 lua.LValue。
func protoScalarToLua(L *lua.LState, field protoreflect.FieldDescriptor, val protoreflect.Value) lua.LValue {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return lua.LBool(val.Bool())

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		v := val.Int()
		if v > maxSafeInt || v < -maxSafeInt {
			return lua.LString(strconv.FormatInt(v, 10))
		}
		return lua.LNumber(v)

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		v := val.Uint()
		if v > maxSafeInt {
			return lua.LString(strconv.FormatUint(v, 10))
		}
		return lua.LNumber(v)

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return lua.LNumber(val.Float())

	case protoreflect.StringKind:
		return lua.LString(val.String())

	case protoreflect.BytesKind:
		return lua.LString(string(val.Bytes()))

	case protoreflect.EnumKind:
		v := int64(val.Enum())
		if v > maxSafeInt || v < -maxSafeInt {
			return lua.LString(strconv.FormatInt(v, 10))
		}
		return lua.LNumber(v)

	case protoreflect.MessageKind:
		return protoMessageToLuaTable(L, val.Message().Interface())

	default:
		return lua.LString(val.String())
	}
}
