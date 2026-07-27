package protox

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Frozen 是解码后 proto 消息的不可变引用封装——state 整存映射（Field==""）的存储形态。
//
// 设计动机（P1a 状态表示重做）：
//   - 旧实现整存时 GetFieldMap 把消息递归展开成 map[string]any 装箱树常驻 state，
//     体积由 schema 全字段数决定（含 proto3 默认值字段），大消息（LoginPlayerS2C /
//     SystemShopDataS2C）× 数千机器人 = GB 级常驻膨胀；
//   - Frozen 只持有解码后的 proto 引用（与网络解码共享同一对象，无额外拷贝），
//     路径访问经 NavigateSegs 惰性取值，临时分配只与"实际被访问的字段"成正比；
//   - proto reflect 对未设置的标量/枚举天然返回默认值（0/""/false/枚举0），
//     与 GetFieldMap 保留 proto3 默认值的语义一致（如会长职位 position=0 不丢失），
//     无需为此预展开整树。
//
// 不可变契约：冻结后底层消息绝不可再修改。构造点必须保证消息此后无写方——
// factory.Parse 出的响应消息存入 state 后仅由 Frozen 持有，满足该条件。
// Message() 仅供边界只读消费（Lua table 化 / 序列化 / 字段导航），调用方不得调用任何
// setter（包括 factory.SetField / Lua proto.set_field，两者签名均不接受 *Frozen，
// 类型系统天然挡住）。不可变性使 Frozen 可被多个 goroutine 无锁并发读，
// state.GetPath 对其按引用直通（不可变值无需任何拷贝防护）。
type Frozen struct {
	msg proto.Message
}

// Freeze 将解码后的 proto 消息封装为不可变引用。msg 为 nil 时返回 nil。
func Freeze(msg proto.Message) *Frozen {
	if msg == nil {
		return nil
	}
	return &Frozen{msg: msg}
}

// Message 返回底层消息，仅供只读消费（Lua 转换 / 序列化），不得修改。
func (fz *Frozen) Message() proto.Message {
	if fz == nil {
		return nil
	}
	return fz.msg
}

// NavigateSegs 在冻结消息上按已拆分的路径段惰性取值，实现 state.PathNavigator。
// 路径存在性语义与 navigatePath(GetFieldMap(msg), segs) 逐字一致（复刻 messageToMap
// 的跳过规则：未设置的 message、空 repeated/map 视为不存在；类型不匹配返回未找到），
// 差别仅在产出的表示是惰性的：
//   - 终端标量/枚举 → Go 标量（与 fromScalarValue 同一套转换，含默认值语义）；
//   - 终端 message → *Frozen（不展开，可继续路径导航或在 Lua 边界转 table）；
//   - 终端 repeated/map → 新建 []any / map[string]any，元素按标量转换、
//     message 元素为 *Frozen（仅浅层物化，不递归展开子树）。
//
// 产出的容器均为现场新建、元素为标量或不可变引用，与底层消息无可变别名，
// 调用方可在锁外自由遍历。
func (fz *Frozen) NavigateSegs(segs []string) (any, bool) {
	if fz == nil || fz.msg == nil || len(segs) == 0 {
		return nil, false
	}
	return frozenNavigate(fz.msg.ProtoReflect(), segs)
}

// frozenNavigate 在 message 节点上消费一个字段名段并按需继续下探。
// 结构复刻 getFieldForStore，仅终端产出换成惰性表示（frozenFieldValue）。
func frozenNavigate(ref protoreflect.Message, parts []string) (any, bool) {
	seg := parts[0]
	if isIndexSeg(seg) {
		return nil, false
	}
	fd := ref.Descriptor().Fields().ByName(protoreflect.Name(seg))
	if fd == nil {
		return nil, false
	}
	// 复刻 messageToMap 的跳过规则：未设置的 message、空 repeated/map 视为不存在。
	if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() && !ref.Has(fd) {
		return nil, false
	}
	if (fd.IsList() || fd.IsMap()) && !ref.Has(fd) {
		return nil, false
	}
	if len(parts) == 1 {
		return frozenFieldValue(fd, ref.Get(fd)), true
	}
	return frozenDescend(fd, ref.Get(fd), parts[1:])
}

// frozenDescend 在字段值上继续下探剩余路径段，结构复刻 descendFieldValue。
func frozenDescend(fd protoreflect.FieldDescriptor, val protoreflect.Value, parts []string) (any, bool) {
	switch {
	case fd.IsList():
		idx, ok := parseIndexSeg(parts[0])
		if !ok {
			return nil, false
		}
		list := val.List()
		if idx < 0 || idx >= list.Len() {
			return nil, false
		}
		elem := list.Get(idx)
		if len(parts) == 1 {
			return frozenScalarValue(fd, elem), true
		}
		if fd.Kind() == protoreflect.MessageKind {
			return frozenNavigate(elem.Message(), parts[1:])
		}
		return nil, false

	case fd.IsMap():
		seg := parts[0]
		if isIndexSeg(seg) {
			return nil, false
		}
		protomap := val.Map()
		valFd := fd.MapValue()
		var found protoreflect.Value
		ok := false
		protomap.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			if k.String() == seg {
				found = v
				ok = true
				return false
			}
			return true
		})
		if !ok {
			return nil, false
		}
		if len(parts) == 1 {
			return frozenScalarValue(valFd, found), true
		}
		if valFd.Kind() == protoreflect.MessageKind {
			return frozenNavigate(found.Message(), parts[1:])
		}
		return nil, false

	case fd.Kind() == protoreflect.MessageKind:
		return frozenNavigate(val.Message(), parts)

	default:
		// 标量后仍有剩余路径 → 路径不存在。
		return nil, false
	}
}

// frozenFieldValue 产出终端字段值的惰性表示：
// repeated/map 仅浅层物化（元素标量转换、message 元素为 *Frozen），标量走 frozenScalarValue。
func frozenFieldValue(fd protoreflect.FieldDescriptor, val protoreflect.Value) any {
	if fd.IsList() {
		list := val.List()
		out := make([]any, 0, list.Len())
		for i := 0; i < list.Len(); i++ {
			out = append(out, frozenScalarValue(fd, list.Get(i)))
		}
		return out
	}
	if fd.IsMap() {
		m := val.Map()
		valFd := fd.MapValue()
		out := make(map[string]any, m.Len())
		m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			out[k.String()] = frozenScalarValue(valFd, v)
			return true
		})
		return out
	}
	return frozenScalarValue(fd, val)
}

// frozenScalarValue 与 fromScalarValue 同一套标量转换，唯一差别：
// 嵌套 message 返回 *Frozen 引用而非 messageToMap 展开树。
func frozenScalarValue(fd protoreflect.FieldDescriptor, val protoreflect.Value) any {
	if fd.Kind() == protoreflect.MessageKind {
		return Freeze(val.Message().Interface())
	}
	return fromScalarValue(fd, val)
}
