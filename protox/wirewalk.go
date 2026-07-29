package protox

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ── Wire 全量物化：单遍直转（D1 直转器）─────────────────────────
//
// WalkWire 在 wire 字节上单遍收集全部字段的最终取值状态，再按 descriptor 字段序
// 通过 WireTreeSink 回调产出——把「整读」从 `dynamicpb 全量解码 → 遍历解码树建表`
// 压成 `字节单遍扫描 → 直接建表`，dynamicpb 中间树（每次整读一棵、纯过路分配）
// 整个消失。产出语义与 `messageToMap(dynamicpb.Unmarshal(raw))` 逐字一致：
//   - 按 descriptor 字段序遍历全部字段（不仅限已设置）；
//   - 未设置的单数 message、空 repeated/map 跳过；
//   - 标量/枚举恒产出（未出现 / 被 oneof 其他成员清掉 → 默认值）；
//   - 合并语义同 wireNavigate（last-wins / message 段拼接 / oneof 竞争 /
//     packed 混排 / map 同 key 替换），共用同一批底层收集原语；
//   - string/bytes 一律复制，产物不与底层快照共享。
//
// 正确性防线与导航路径同构：L1 差分 fuzz（wirewalk_test.go），L2/L3 线上影子
// 采样（MaterializeAllowed 内嵌，伪路径 "*"，失配走同一套 schema 降级注册表）。

// WireTreeSink 接收一个 message 层级的字段产出事件。
// 实现方按事件构建目标表示（Go map / Lua table），字段按 descriptor 序到达。
type WireTreeSink interface {
	// Scalar 标量/枚举字段（含默认值语义），v 为 fromScalarValue 同款装箱。
	Scalar(fd protoreflect.FieldDescriptor, v any)
	// Message 单数 message 字段，返回子层级 sink（walker 随后递归填充）。
	Message(fd protoreflect.FieldDescriptor) WireTreeSink
	// List repeated 字段，n 为元素数，返回列表 sink。
	List(fd protoreflect.FieldDescriptor, n int) WireListSink
	// Map map 字段，n 为条目数，返回 map sink。
	Map(fd protoreflect.FieldDescriptor, n int) WireMapSink
}

// WireListSink 接收 repeated 字段的元素（按出现序）。
type WireListSink interface {
	ScalarElem(v any)
	// MessageElem 返回元素的子层级 sink。
	MessageElem() WireTreeSink
}

// WireMapSink 接收 map 字段的条目（按 key 首次出现序）。
type WireMapSink interface {
	ScalarEntry(key string, v any)
	// MessageEntry 返回 value 的子层级 sink。
	MessageEntry(key string) WireTreeSink
}

// wireFieldAcc 单遍扫描期一个字段的累计状态（按字段形态使用对应成员）。
type wireFieldAcc struct {
	hasScalar bool
	scalar    any
	spans     [][]byte
	elems     []wireElem
	entries   map[string]mapEntryState
	keys      []string
}

// wireWalkRecursionLimit 直转递归上限（与 ValidateWire 同源）。
const wireWalkRecursionLimit = protowire.DefaultRecursionLimit

// Walk 全量遍历并经 sink 产出。结构损坏返回 error（构造点已 ValidateWire，
// 理论不可达），调用方应回落解码路径。调用前应先 MaterializeAllowed。
func (wv *WireValue) Walk(sink WireTreeSink) error {
	if wv == nil || wv.desc == nil {
		return fmt.Errorf("WireValue 为空")
	}
	return walkWireLevel(wv.desc, wv.raw, sink, wireWalkRecursionLimit)
}

// walkWireLevel 单遍收集一个 message 层级并按字段序产出。
func walkWireLevel(md protoreflect.MessageDescriptor, b []byte, sink WireTreeSink, depth int) error {
	if depth <= 0 {
		return fmt.Errorf("wire 嵌套超过递归上限")
	}
	fields := md.Fields()
	accs := make([]wireFieldAcc, fields.Len())
	var oneofWinner map[protoreflect.OneofDescriptor]int // oneof → 当前胜出字段 index

	collectErr := error(nil)
	structOK := scanLevel(b, func(num protowire.Number, typ protowire.Type, u uint64, bs []byte) bool {
		fd := fields.ByNumber(num)
		if fd == nil {
			return true // 未知字段
		}
		acc := &accs[fd.Index()]
		switch {
		case fd.IsMap():
			if typ != protowire.BytesType {
				return true // 错误 wire type → 未知字段
			}
			key, st, ok := parseMapEntry(fd.MapKey(), fd.MapValue(), bs)
			if !ok {
				collectErr = fmt.Errorf("map %s entry 损坏", fd.FullName())
				return false
			}
			if acc.entries == nil {
				acc.entries = make(map[string]mapEntryState)
			}
			if _, seen := acc.entries[key]; !seen {
				acc.keys = append(acc.keys, key)
			}
			acc.entries[key] = st

		case fd.IsList():
			kind := fd.Kind()
			switch {
			case kind == protoreflect.MessageKind && typ == protowire.BytesType:
				acc.elems = append(acc.elems, wireElem{span: bs, isMsg: true})
			case (kind == protoreflect.StringKind || kind == protoreflect.BytesKind) && typ == protowire.BytesType:
				acc.elems = append(acc.elems, wireElem{scalar: decodeScalarWire(fd, 0, bs)})
			case kindPackable(kind) && typ == protowire.BytesType:
				more, ok := decodePacked(fd, bs)
				if !ok {
					collectErr = fmt.Errorf("字段 %s packed 载荷损坏", fd.FullName())
					return false
				}
				acc.elems = append(acc.elems, more...)
			case typ == nativeWireType(kind):
				acc.elems = append(acc.elems, wireElem{scalar: decodeScalarWire(fd, u, bs)})
			default:
				// 错误 wire type → 未知字段，忽略
			}

		default: // 单数字段（含 oneof 成员）
			if !wireTypeMatches(fd, typ) {
				return true
			}
			if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
				if oneofWinner == nil {
					oneofWinner = make(map[protoreflect.OneofDescriptor]int, 1)
				}
				if prev, ok := oneofWinner[od]; ok && prev != fd.Index() {
					// 成员切换：清掉前一胜出成员的累计（复刻解码器 oneof 竞争）
					accs[prev] = wireFieldAcc{}
				}
				oneofWinner[od] = fd.Index()
			}
			if fd.Kind() == protoreflect.MessageKind {
				acc.spans = append(acc.spans, bs)
			} else if fd.Kind() == protoreflect.GroupKind {
				// proto3 无 group；出现即回落解码路径（与 wireNavigate 拒绝一致）
				collectErr = fmt.Errorf("字段 %s 为 group 类型，不支持直转", fd.FullName())
				return false
			} else {
				acc.scalar = decodeScalarWire(fd, u, bs)
				acc.hasScalar = true
			}
		}
		return true
	})
	if collectErr != nil {
		return collectErr
	}
	if !structOK {
		return fmt.Errorf("%s wire 结构损坏", md.FullName())
	}

	// 按 descriptor 字段序产出（与 messageToMap / protoMessageToLuaTable 一致）。
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		acc := &accs[i]
		switch {
		case fd.IsMap():
			if len(acc.entries) == 0 {
				continue // 空 map 跳过
			}
			ms := sink.Map(fd, len(acc.entries))
			valFd := fd.MapValue()
			for _, k := range acc.keys {
				st := acc.entries[k]
				if valFd.Kind() == protoreflect.MessageKind {
					if err := walkWireLevel(valFd.Message(), concatSpans(st.spans), ms.MessageEntry(k), depth-1); err != nil {
						return err
					}
				} else if st.hasScalar {
					ms.ScalarEntry(k, st.scalar)
				} else {
					ms.ScalarEntry(k, fromScalarValue(valFd, valFd.Default()))
				}
			}

		case fd.IsList():
			if len(acc.elems) == 0 {
				continue // 空 repeated 跳过
			}
			ls := sink.List(fd, len(acc.elems))
			for _, e := range acc.elems {
				if e.isMsg {
					if err := walkWireLevel(fd.Message(), e.span, ls.MessageElem(), depth-1); err != nil {
						return err
					}
				} else {
					ls.ScalarElem(e.scalar)
				}
			}

		case fd.Kind() == protoreflect.MessageKind:
			if len(acc.spans) == 0 {
				continue // 未设置的单数 message 跳过
			}
			if err := walkWireLevel(fd.Message(), concatSpans(acc.spans), sink.Message(fd), depth-1); err != nil {
				return err
			}

		case fd.Kind() == protoreflect.GroupKind:
			return fmt.Errorf("字段 %s 为 group 类型，不支持直转", fd.FullName())

		default: // 标量/枚举：恒产出
			if acc.hasScalar {
				sink.Scalar(fd, acc.scalar)
			} else {
				sink.Scalar(fd, fromScalarValue(fd, fd.Default()))
			}
		}
	}
	return nil
}

// ── Go map 出口（messageToMap 同形产物）───────────────────────────

// mapTreeSink 把遍历事件物化为 map[string]any（messageToMap 语义）。
type mapTreeSink struct {
	m map[string]any
}

func newMapTreeSink() *mapTreeSink {
	return &mapTreeSink{m: make(map[string]any)}
}

func (s *mapTreeSink) Scalar(fd protoreflect.FieldDescriptor, v any) {
	s.m[string(fd.Name())] = v
}

func (s *mapTreeSink) Message(fd protoreflect.FieldDescriptor) WireTreeSink {
	child := newMapTreeSink()
	s.m[string(fd.Name())] = child.m
	return child
}

func (s *mapTreeSink) List(fd protoreflect.FieldDescriptor, n int) WireListSink {
	l := &mapListSink{parent: s.m, name: string(fd.Name()), elems: make([]any, 0, n)}
	l.parent[l.name] = l.elems
	return l
}

func (s *mapTreeSink) Map(fd protoreflect.FieldDescriptor, n int) WireMapSink {
	m := make(map[string]any, n)
	s.m[string(fd.Name())] = m
	return &mapMapSink{m: m}
}

// mapListSink 列表出口。容量按元素数精确预分配，append 不会搬迁底层数组，
// 但切片头长度会变，每次追加后写回父容器。
type mapListSink struct {
	parent map[string]any
	name   string
	elems  []any
}

func (l *mapListSink) ScalarElem(v any) {
	l.elems = append(l.elems, v)
	l.parent[l.name] = l.elems
}

func (l *mapListSink) MessageElem() WireTreeSink {
	child := newMapTreeSink()
	l.elems = append(l.elems, child.m)
	l.parent[l.name] = l.elems
	return child
}

type mapMapSink struct {
	m map[string]any
}

func (m *mapMapSink) ScalarEntry(key string, v any) {
	m.m[key] = v
}

func (m *mapMapSink) MessageEntry(key string) WireTreeSink {
	child := newMapTreeSink()
	m.m[key] = child.m
	return child
}
