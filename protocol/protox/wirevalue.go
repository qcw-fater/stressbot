package protox

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// WireValue 是「原始 protobuf wire 字节 + 消息描述符」的不可变封装——state 整存
// 映射的新存储形态（取代解码态 Frozen）。
//
// 动机（wire-first 状态面重做，见 docs/superpowers/plans/2026-07-29-wire-first-state-plane.md）：
//   - 每机器人独有的大消息（LoginPlayerDataS2C ~74KB wire）任何**解码态**表示
//     （dynamicpb / Go map / Lua table）都 ~600KB/机器人；wire 字节本身小一个数量级。
//   - 留存改持字节，读取按需在字节上做 wire 扫描惰性取值（NavigateSegs），
//     临时分配只与「实际被访问的字段」成正比；全量物化（robot.get 整读）时才解码。
//
// 语义契约（与解码路径逐字一致，差分 fuzz + 线上影子验证共同守护）：
//
//	NavigateSegs(segs) 的产出（物化后）≡ Freeze(dynamicpb.Unmarshal(raw)).NavigateSegs(segs)。
//	为此 wire 扫描完整复刻 protobuf 合并语义：
//	- 单数标量/枚举：最后一次出现胜出（last-wins）；错误 wire type 的出现按未知字段忽略；
//	- 单数 message 多次出现：字节段按序拼接（wire 拼接 ≡ proto merge）；
//	- oneof：全组成员按出现序竞争，后出现的成员清掉前者（同成员连续出现按上一条 merge）；
//	- repeated：出现序即元素序，packed 与非 packed 可混排；
//	- map：entry 按出现序，同 key 后者整体替换；entry 内 key/value 自身按单数规则；
//	- 存在性：message 需至少一次出现；repeated/map 需至少一个元素；
//	  标量恒存在（未出现取 proto3 默认值）——与 messageToMap 的跳过规则一致。
//
// 不可变契约：raw 必须是调用方交出所有权的独立快照（网络缓冲区会被 pump 复用，
// 构造点必须先 WireSnapshot）。构造后 raw 绝不改写，可被多 goroutine 无锁并发读。
type WireValue struct {
	desc protoreflect.MessageDescriptor
	raw  []byte
}

// NewWireValue 用消息描述符与**已独立**的 wire 字节构造 WireValue。
// raw 的所有权移交给 WireValue（此后不得改写）；desc 为 nil 时返回 nil。
func NewWireValue(desc protoreflect.MessageDescriptor, raw []byte) *WireValue {
	if desc == nil {
		return nil
	}
	return &WireValue{desc: desc, raw: raw}
}

// WireSnapshot 复制一份独立的字节快照，供把网络缓冲区字节移交 NewWireValue 前调用。
func WireSnapshot(data []byte) []byte {
	return append([]byte(nil), data...)
}

// Desc 返回消息描述符。
func (wv *WireValue) Desc() protoreflect.MessageDescriptor {
	if wv == nil {
		return nil
	}
	return wv.desc
}

// Raw 返回底层 wire 字节（只读，不得改写）。
func (wv *WireValue) Raw() []byte {
	if wv == nil {
		return nil
	}
	return wv.raw
}

// ProtoName 返回消息全名（调试/统计用）。
func (wv *WireValue) ProtoName() string {
	if wv == nil || wv.desc == nil {
		return ""
	}
	return string(wv.desc.FullName())
}

// Message 把 wire 字节完整解码为 dynamicpb 消息（全量物化 / 影子验证 oracle 用）。
// 产物为现场新建的独占消息，调用方可自由持有；不缓存（避免解码树钉在 WireValue 上）。
func (wv *WireValue) Message() (proto.Message, error) {
	if wv == nil || wv.desc == nil {
		return nil, errors.New("WireValue 为空")
	}
	msg := dynamicpb.NewMessage(wv.desc)
	if err := proto.Unmarshal(wv.raw, msg); err != nil {
		return nil, fmt.Errorf("解码 %s 失败: %w", wv.desc.FullName(), err)
	}
	return msg, nil
}

// MaterializeValue 全量物化为 map[string]any（messageToMap 语义），实现 state.ValueMaterializer。
// 默认走 wire 单遍直转（WalkWire，零 dynamicpb 中间树）；schema 降级 / 影子采样
// 失配 / 直转失败时回落解码路径。解码也失败（构造点已结构校验，理论不可达）返回空 map。
func (wv *WireValue) MaterializeValue() any {
	if wv.MaterializeAllowed() {
		sink := newMapTreeSink()
		err := wv.Walk(sink)
		if err == nil {
			return sink.m
		}
		// ValidateWire 通过的字节上 Walk 失败 = 扫描器 bug：留证据日志并降级，
		// 绝不静默回退（否则 bug 无从排查）。
		wv.ReportWireFailure("materialize", err)
	}
	msg, err := wv.Message()
	if err != nil {
		return map[string]any{}
	}
	return messageToMap(msg.ProtoReflect())
}

// CopyDetached 返回底层字节独立复制的新 WireValue。
// 用于保留规划：把「共享大快照的子 span」复制为独立小缓冲，解除对父快照的钉扎。
func (wv *WireValue) CopyDetached() *WireValue {
	if wv == nil {
		return nil
	}
	return &WireValue{desc: wv.desc, raw: WireSnapshot(wv.raw)}
}

// navSegCache 缓存路径 → 分段（与 state.splitPathCached 同思路，路径集合由配置固定）。
var navSegCache sync.Map

func navSplitCached(path string) []string {
	if v, ok := navSegCache.Load(path); ok {
		return v.([]string)
	}
	segs := splitPath(path)
	navSegCache.Store(path, segs)
	return segs
}

// Navigate 按点分路径字符串取值（NavigateSegs 的便捷入口，分段结果有缓存）。
func (wv *WireValue) Navigate(path string) (any, bool) {
	return wv.NavigateSegs(navSplitCached(path))
}

// NavigateSegs 在 wire 字节上按已拆分的路径段惰性取值，实现 state.PathNavigator。
// 语义契约见类型注释。产出表示：
//   - 终端标量/枚举 → Go 标量（与 fromScalarValue 同一套装箱，含默认值语义）；
//   - 终端 message → *WireValue（子字节段，可继续导航或在边界全量物化）；
//   - 终端 repeated/map → 现场新建 []any / map[string]any，message 元素为 *WireValue；
//   - string/bytes 终端一律复制（不与底层快照共享，避免钉扎大缓冲）。
//
// 灰度期每次导航可能触发影子验证（wireshadow.go）：首 K 次按 (schema,路径) 全查，
// 之后按采样率抽查；失配以解码侧结果为准返回并把该 schema 降级。
func (wv *WireValue) NavigateSegs(segs []string) (any, bool) {
	if wv == nil || wv.desc == nil || len(segs) == 0 {
		return nil, false
	}
	fds, verify := navResolve(wv.desc, segs)
	got, found := wireNavigate(wv.desc, wv.raw, segs, fds)
	if verify {
		return shadowVerifyNavigate(wv, segs, got, found)
	}
	return got, found
}

// wireNavigate 在 message 层级的字节上消费一个字段名段并按需继续下探。
// 结构复刻 frozenNavigate（存在性/类型不匹配语义逐字一致），取值改为 wire 扫描。
// fds 为驻留表预解析的 fd 链（wirenav.go），与 parts 同步推进；槽位为 nil
// （非字段段/未驻留）时回退 Fields().ByName，行为不变。
func wireNavigate(md protoreflect.MessageDescriptor, b []byte, parts []string, fds []protoreflect.FieldDescriptor) (any, bool) {
	seg := parts[0]
	if isIndexSeg(seg) {
		return nil, false
	}
	var fd protoreflect.FieldDescriptor
	if len(fds) > 0 {
		fd = fds[0]
	}
	if fd == nil {
		fd = md.Fields().ByName(protoreflect.Name(seg))
	}
	if fd == nil {
		return nil, false
	}

	switch {
	case fd.IsMap():
		entries, keys, ok := wireCollectMap(b, fd)
		if !ok || len(entries) == 0 {
			return nil, false // 空 map 视为不存在（messageToMap 跳过规则）
		}
		valFd := fd.MapValue()
		if len(parts) == 1 {
			out := make(map[string]any, len(entries))
			for _, k := range keys {
				out[k] = entries[k].terminalValue(valFd)
			}
			return out, true
		}
		key := parts[1]
		if isIndexSeg(key) {
			return nil, false
		}
		e, hit := entries[key]
		if !hit {
			return nil, false
		}
		if len(parts) == 2 {
			return e.terminalValue(valFd), true
		}
		if valFd.Kind() == protoreflect.MessageKind {
			return wireNavigate(valFd.Message(), concatSpans(e.spans), parts[2:], fdsTail(fds, 2))
		}
		return nil, false

	case fd.IsList():
		if len(parts) == 1 {
			elems, ok := wireCollectList(b, fd, 0)
			if !ok || len(elems) == 0 {
				return nil, false // 空 repeated 视为不存在
			}
			out := make([]any, 0, len(elems))
			for _, e := range elems {
				out = append(out, e.terminalValue(fd))
			}
			return out, true
		}
		// 带下标访问：只扫到第 idx+1 个元素即停（repeated 元素按出现序追加，
		// 前缀即定值，早退不改变语义；层级结构在构造点已全量校验）。
		idx, iok := parseIndexSeg(parts[1])
		if !iok || idx < 0 {
			return nil, false
		}
		elems, ok := wireCollectList(b, fd, idx+1)
		if !ok || idx >= len(elems) {
			return nil, false // 含空 repeated 视为不存在
		}
		if len(parts) == 2 {
			return elems[idx].terminalValue(fd), true
		}
		if fd.Kind() == protoreflect.MessageKind {
			return wireNavigate(fd.Message(), elems[idx].span, parts[2:], fdsTail(fds, 2))
		}
		return nil, false

	case fd.Kind() == protoreflect.MessageKind:
		res, ok := wireCollectSingular(md, b, fd)
		if !ok || res.member != fd || len(res.spans) == 0 {
			// 无出现 / oneof 被其他成员清掉 → 未设置 message 视为不存在
			return nil, false
		}
		sub := concatSpans(res.spans)
		if len(parts) == 1 {
			return &WireValue{desc: fd.Message(), raw: sub}, true
		}
		return wireNavigate(fd.Message(), sub, parts[1:], fdsTail(fds, 1))

	case fd.Kind() == protoreflect.GroupKind:
		// proto3 无 group；不支持（差分 fuzz 的 schema 也不含 group）。
		return nil, false

	default:
		// 标量/枚举（含 oneof 成员）：恒存在（未出现 / 被其他 oneof 成员清掉 → 默认值）。
		res, ok := wireCollectSingular(md, b, fd)
		if !ok {
			return nil, false
		}
		if len(parts) > 1 {
			return nil, false // 标量后仍有剩余路径 → 路径不存在
		}
		if res.member == fd && res.hasScalar {
			return res.scalar, true
		}
		return fromScalarValue(fd, fd.Default()), true
	}
}

// concatSpans 拼接单数 message 的多个出现段（wire 拼接 ≡ proto merge）。
// 单段直通（零拷贝，共享父缓冲）；多段现场新建。
func concatSpans(spans [][]byte) []byte {
	if len(spans) == 1 {
		return spans[0]
	}
	total := 0
	for _, s := range spans {
		total += len(s)
	}
	out := make([]byte, 0, total)
	for _, s := range spans {
		out = append(out, s...)
	}
	return out
}

// ── 层级扫描 ────────────────────────────────────────────────────

// scanLevel 顺序遍历一个 message 层级的全部字段出现。
// visit 返回 false 表示提前终止（下标早退 / packed 解码失败），扫描返回 true。
// 返回 false 表示 wire 结构损坏（构造点已校验，导航中理论不可达）。
func scanLevel(b []byte, visit func(num protowire.Number, typ protowire.Type, u uint64, bs []byte) bool) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return false
			}
			if !visit(num, typ, v, nil) {
				return true
			}
			b = b[m:]
		case protowire.Fixed32Type:
			v, m := protowire.ConsumeFixed32(b)
			if m < 0 {
				return false
			}
			if !visit(num, typ, uint64(v), nil) {
				return true
			}
			b = b[m:]
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				return false
			}
			if !visit(num, typ, v, nil) {
				return true
			}
			b = b[m:]
		case protowire.BytesType:
			bs, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return false
			}
			if !visit(num, typ, 0, bs) {
				return true
			}
			b = b[m:]
		case protowire.StartGroupType:
			// 未知 group 字段：整组结构化跳过（合法 wire 必须可跳过）。
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return false
			}
			b = b[m:]
		default:
			return false
		}
	}
	return true
}

// singularResult 单数字段（含 oneof 成员）的收集结果。
// member 是「最后胜出的成员」：非 oneof 字段恒为目标字段自身；oneof 按出现序竞争。
type singularResult struct {
	member    protoreflect.FieldDescriptor
	hasScalar bool
	scalar    any
	spans     [][]byte
}

// wireCollectSingular 单遍扫描收集单数目标字段 fd 的最终取值状态。
// fd 属于真实 oneof 时观察全组成员（后出现的成员清掉前者的累计值——复刻解码器行为：
// 每个出现把该成员置为当前值；同成员 message 连续出现按 merge 拼接段）。
// 错误 wire type 的出现按未知字段忽略（不影响胜出状态）。
func wireCollectSingular(md protoreflect.MessageDescriptor, b []byte, fd protoreflect.FieldDescriptor) (singularResult, bool) {
	var res singularResult

	od := fd.ContainingOneof()
	watchOneof := od != nil && !od.IsSynthetic()

	structOK := scanLevel(b, func(num protowire.Number, typ protowire.Type, u uint64, bs []byte) bool {
		var f2 protoreflect.FieldDescriptor
		switch {
		case num == fd.Number():
			f2 = fd
		case watchOneof:
			cand := md.Fields().ByNumber(num)
			if cand == nil || cand.ContainingOneof() != od {
				return true
			}
			f2 = cand
		default:
			return true
		}
		if !wireTypeMatches(f2, typ) {
			return true // 错误 wire type → 未知字段，忽略
		}
		if f2 != res.member {
			res.member = f2
			res.spans = nil
			res.hasScalar = false
			res.scalar = nil
		}
		if f2.Kind() == protoreflect.MessageKind {
			res.spans = append(res.spans, bs)
		} else {
			res.scalar = decodeScalarWire(f2, u, bs)
			res.hasScalar = true
		}
		return true
	})
	return res, structOK
}

// wireElem repeated 字段的单个元素：标量已解码，message 保留字节段。
type wireElem struct {
	scalar any
	span   []byte
	isMsg  bool
}

// terminalValue 产出元素的终端表示（与 frozenScalarValue 对应：message → 惰性引用）。
func (e wireElem) terminalValue(fd protoreflect.FieldDescriptor) any {
	if e.isMsg {
		return &WireValue{desc: fd.Message(), raw: e.span}
	}
	return e.scalar
}

// wireCollectList 单遍扫描收集 repeated 字段元素（出现序，packed/非 packed 混排）。
// limit > 0 时收集到至少 limit 个元素即提前终止扫描（packed 块整块解出，可能
// 略多于 limit）——repeated 元素只追加不覆盖，前缀即定值，早退不改变取值语义；
// 层级结构在 WireValue 构造点已全量校验，跳过尾部不损失防线。limit <= 0 收全量。
func wireCollectList(b []byte, fd protoreflect.FieldDescriptor, limit int) ([]wireElem, bool) {
	var elems []wireElem
	kind := fd.Kind()
	native := nativeWireType(kind)
	packable := kindPackable(kind)
	num := fd.Number()

	decodeOK := true
	structOK := scanLevel(b, func(n protowire.Number, typ protowire.Type, u uint64, bs []byte) bool {
		if n != num {
			return true
		}
		switch {
		case kind == protoreflect.MessageKind && typ == protowire.BytesType:
			elems = append(elems, wireElem{span: bs, isMsg: true})
		case (kind == protoreflect.StringKind || kind == protoreflect.BytesKind) && typ == protowire.BytesType:
			elems = append(elems, wireElem{scalar: decodeScalarWire(fd, 0, bs)})
		case packable && typ == protowire.BytesType:
			// packed 序列：按 kind 逐个解出
			more, ok := decodePacked(fd, bs)
			if !ok {
				decodeOK = false
				return false
			}
			elems = append(elems, more...)
		case typ == native:
			elems = append(elems, wireElem{scalar: decodeScalarWire(fd, u, bs)})
		default:
			// 错误 wire type → 未知字段，忽略
		}
		return limit <= 0 || len(elems) < limit
	})
	return elems, structOK && decodeOK
}

// decodePacked 解出 packed 载荷中的连续标量元素。
func decodePacked(fd protoreflect.FieldDescriptor, bs []byte) ([]wireElem, bool) {
	var out []wireElem
	kind := fd.Kind()
	switch nativeWireType(kind) {
	case protowire.VarintType:
		for len(bs) > 0 {
			v, m := protowire.ConsumeVarint(bs)
			if m < 0 {
				return nil, false
			}
			out = append(out, wireElem{scalar: decodeScalarWire(fd, v, nil)})
			bs = bs[m:]
		}
	case protowire.Fixed32Type:
		for len(bs) > 0 {
			v, m := protowire.ConsumeFixed32(bs)
			if m < 0 {
				return nil, false
			}
			out = append(out, wireElem{scalar: decodeScalarWire(fd, uint64(v), nil)})
			bs = bs[m:]
		}
	case protowire.Fixed64Type:
		for len(bs) > 0 {
			v, m := protowire.ConsumeFixed64(bs)
			if m < 0 {
				return nil, false
			}
			out = append(out, wireElem{scalar: decodeScalarWire(fd, v, nil)})
			bs = bs[m:]
		}
	default:
		return nil, false
	}
	return out, true
}

// mapEntryState map 条目的最终取值状态（entry 内 key/value 自身按单数规则收集）。
type mapEntryState struct {
	hasScalar bool
	scalar    any
	spans     [][]byte
}

// terminalValue 产出 map 值的终端表示。
// 值为 message 且 entry 缺 value 字段时 → 空消息（解码器为该 key 建空 message）。
func (e mapEntryState) terminalValue(valFd protoreflect.FieldDescriptor) any {
	if valFd.Kind() == protoreflect.MessageKind {
		return &WireValue{desc: valFd.Message(), raw: concatSpans(e.spans)}
	}
	if e.hasScalar {
		return e.scalar
	}
	return fromScalarValue(valFd, valFd.Default())
}

// wireCollectMap 单遍扫描收集 map 字段全部条目。
// 同 key 的后一条 entry 整体替换前者（复刻 map.Set 语义）。
// keys 保留首次出现序（供物化时确定性遍历；map 本身无序，仅为实现稳定）。
func wireCollectMap(b []byte, fd protoreflect.FieldDescriptor) (map[string]mapEntryState, []string, bool) {
	entries := make(map[string]mapEntryState)
	var keys []string
	keyFd := fd.MapKey()
	valFd := fd.MapValue()
	num := fd.Number()

	entryOK := true
	structOK := scanLevel(b, func(n protowire.Number, typ protowire.Type, _ uint64, bs []byte) bool {
		if n != num || typ != protowire.BytesType {
			return true // 错误 wire type → 未知字段
		}
		key, st, ok := parseMapEntry(keyFd, valFd, bs)
		if !ok {
			entryOK = false
			return false
		}
		if _, seen := entries[key]; !seen {
			keys = append(keys, key)
		}
		entries[key] = st
		return true
	})
	return entries, keys, structOK && entryOK
}

// parseMapEntry 解析单条 map entry（key=1 / value=2，各自按单数规则）。
func parseMapEntry(keyFd, valFd protoreflect.FieldDescriptor, bs []byte) (string, mapEntryState, bool) {
	var st mapEntryState
	var keyScalar any
	keyNative := nativeWireType(keyFd.Kind())
	valNative := nativeWireType(valFd.Kind())

	ok := scanLevel(bs, func(n protowire.Number, typ protowire.Type, u uint64, b2 []byte) bool {
		switch {
		case n == 1 && typ == keyNative:
			keyScalar = decodeScalarWire(keyFd, u, b2)
		case n == 2 && valFd.Kind() == protoreflect.MessageKind && typ == protowire.BytesType:
			st.spans = append(st.spans, b2)
		case n == 2 && typ == valNative:
			st.scalar = decodeScalarWire(valFd, u, b2)
			st.hasScalar = true
		}
		return true
	})
	if !ok {
		return "", st, false
	}
	if keyScalar == nil {
		keyScalar = fromScalarValue(keyFd, keyFd.Default())
	}
	return mapKeyToString(keyScalar), st, true
}

// mapKeyToString 把已解码的 map key 标量转为字符串键，与 protoreflect.MapKey.String() 一致。
func mapKeyToString(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case string:
		return x
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ── 标量解码 ────────────────────────────────────────────────────

// nativeWireType 返回 kind 的原生 wire type（packed 之外的单元素编码）。
func nativeWireType(kind protoreflect.Kind) protowire.Type {
	switch kind {
	case protoreflect.BoolKind, protoreflect.EnumKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Uint32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Uint64Kind:
		return protowire.VarintType
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return protowire.Fixed32Type
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return protowire.Fixed64Type
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return protowire.BytesType
	case protoreflect.GroupKind:
		return protowire.StartGroupType
	default:
		return protowire.VarintType
	}
}

// kindPackable 判断 kind 是否可 packed 编码（数值/枚举/布尔）。
func kindPackable(kind protoreflect.Kind) bool {
	switch kind {
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind, protoreflect.GroupKind:
		return false
	default:
		return true
	}
}

// wireTypeMatches 判断单数字段的一次出现是否为合法 wire type（否则按未知字段忽略）。
func wireTypeMatches(fd protoreflect.FieldDescriptor, typ protowire.Type) bool {
	return typ == nativeWireType(fd.Kind())
}

// decodeScalarWire 把 wire 载荷解码为 Go 标量，装箱规则与 fromScalarValue 逐字一致。
// varint/fixed 载荷在 u，LEN 载荷在 bs。string/bytes 一律复制（不与底层快照共享）。
func decodeScalarWire(fd protoreflect.FieldDescriptor, u uint64, bs []byte) any {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return u != 0
	case protoreflect.Int32Kind:
		return int64(int32(u))
	case protoreflect.Sint32Kind:
		return int64(int32(protowire.DecodeZigZag(u & math.MaxUint32)))
	case protoreflect.Sfixed32Kind:
		return int64(int32(uint32(u)))
	case protoreflect.Int64Kind:
		return int64(u)
	case protoreflect.Sint64Kind:
		return protowire.DecodeZigZag(u)
	case protoreflect.Sfixed64Kind:
		return int64(u)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return uint64(uint32(u))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return u
	case protoreflect.FloatKind:
		return float64(math.Float32frombits(uint32(u)))
	case protoreflect.DoubleKind:
		return math.Float64frombits(u)
	case protoreflect.StringKind:
		return string(bs)
	case protoreflect.BytesKind:
		return slices.Clone(bs)
	case protoreflect.EnumKind:
		return int64(int32(u))
	default:
		return nil
	}
}

// ── 结构校验 ────────────────────────────────────────────────────

// ValidateWire 校验 raw 是否能被 proto.Unmarshal(desc) 成功解码——wire-first 存储
// 用它取代「解码即校验」：tag/长度结构、已知 message 字段递归、packed 载荷、
// proto3 string 的 UTF-8。未知字段只做结构跳过（与解码器一致，不校验载荷内容）。
// 零解码分配（纯索引游走）。
func ValidateWire(desc protoreflect.MessageDescriptor, raw []byte) error {
	return validateWireLevel(desc, raw, protowire.DefaultRecursionLimit)
}

func validateWireLevel(md protoreflect.MessageDescriptor, b []byte, depth int) error {
	if depth <= 0 {
		return errors.New("wire 嵌套超过递归上限")
	}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("非法 tag: %w", protowire.ParseError(n))
		}
		b = b[n:]
		var fd protoreflect.FieldDescriptor
		if md != nil {
			fd = md.Fields().ByNumber(num)
		}
		if typ == protowire.BytesType {
			bs, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return fmt.Errorf("字段 %d 非法 LEN 载荷: %w", num, protowire.ParseError(m))
			}
			b = b[m:]
			if fd == nil {
				continue
			}
			switch {
			case fd.IsMap():
				if err := validateMapEntry(fd, bs, depth-1); err != nil {
					return err
				}
			case fd.Kind() == protoreflect.MessageKind:
				if err := validateWireLevel(fd.Message(), bs, depth-1); err != nil {
					return err
				}
			case fd.Kind() == protoreflect.StringKind:
				if !utf8.Valid(bs) {
					return fmt.Errorf("字段 %s 含非法 UTF-8", fd.FullName())
				}
			case fd.IsList() && kindPackable(fd.Kind()):
				if err := validatePacked(fd, bs); err != nil {
					return err
				}
			case fd.Kind() == protoreflect.BytesKind:
				// 任意字节合法
			default:
				// 单数数值字段的 LEN 出现 → 解码器按未知字段保留，不校验载荷
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return fmt.Errorf("字段 %d 非法载荷(type=%d): %w", num, typ, protowire.ParseError(m))
		}
		b = b[m:]
	}
	return nil
}

// validateMapEntry 校验 map entry 载荷（string key/value 的 UTF-8、message value 递归）。
func validateMapEntry(fd protoreflect.FieldDescriptor, bs []byte, depth int) error {
	if depth <= 0 {
		return errors.New("wire 嵌套超过递归上限")
	}
	keyFd := fd.MapKey()
	valFd := fd.MapValue()
	b := bs
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("map %s entry 非法 tag: %w", fd.FullName(), protowire.ParseError(n))
		}
		b = b[n:]
		if typ == protowire.BytesType {
			payload, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return fmt.Errorf("map %s entry 非法 LEN 载荷: %w", fd.FullName(), protowire.ParseError(m))
			}
			b = b[m:]
			switch {
			case num == 1 && keyFd.Kind() == protoreflect.StringKind:
				if !utf8.Valid(payload) {
					return fmt.Errorf("map %s key 含非法 UTF-8", fd.FullName())
				}
			case num == 2 && valFd.Kind() == protoreflect.MessageKind:
				if err := validateWireLevel(valFd.Message(), payload, depth-1); err != nil {
					return err
				}
			case num == 2 && valFd.Kind() == protoreflect.StringKind:
				if !utf8.Valid(payload) {
					return fmt.Errorf("map %s value 含非法 UTF-8", fd.FullName())
				}
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return fmt.Errorf("map %s entry 非法载荷: %w", fd.FullName(), protowire.ParseError(m))
		}
		b = b[m:]
	}
	return nil
}

// validatePacked 校验 packed 载荷可被完整解出（尾部残缺 → 解码失败）。
func validatePacked(fd protoreflect.FieldDescriptor, bs []byte) error {
	switch nativeWireType(fd.Kind()) {
	case protowire.VarintType:
		for len(bs) > 0 {
			_, m := protowire.ConsumeVarint(bs)
			if m < 0 {
				return fmt.Errorf("字段 %s packed 载荷残缺", fd.FullName())
			}
			bs = bs[m:]
		}
	case protowire.Fixed32Type:
		if len(bs)%4 != 0 {
			return fmt.Errorf("字段 %s packed fixed32 载荷长度非 4 的倍数", fd.FullName())
		}
	case protowire.Fixed64Type:
		if len(bs)%8 != 0 {
			return fmt.Errorf("字段 %s packed fixed64 载荷长度非 8 的倍数", fd.FullName())
		}
	}
	return nil
}

// ── 保留规划（批量 span 复制决策）─────────────────────────────────

// collectWireRefs 深度收集导航产物里全部 *WireValue 引用的字节量。
func collectWireRefs(v any, sum *int) {
	switch x := v.(type) {
	case *WireValue:
		*sum += len(x.raw)
	case []any:
		for _, e := range x {
			collectWireRefs(e, sum)
		}
	case map[string]any:
		for _, e := range x {
			collectWireRefs(e, sum)
		}
	}
}

// detachWireRefs 深度把导航产物里的 *WireValue 替换为独立字节复制。
// 容器均为导航现场新建，就地改写安全。
func detachWireRefs(v any) any {
	switch x := v.(type) {
	case *WireValue:
		return x.CopyDetached()
	case []any:
		for i := range x {
			x[i] = detachWireRefs(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = detachWireRefs(x[k])
		}
		return x
	default:
		return v
	}
}

// PlanWireRetention 对一批将写入 state 的路径导航产物做保留规划：
//   - wholeRetained（同响应还有整存映射，快照必然常驻）→ 子 span 共享快照，零复制；
//   - 否则比较 Σ(子 span 字节) 与整包字节：复制更省则逐个复制为独立缓冲
//     （解除对整包快照的钉扎，快照随导航结束被 GC），共享更省（或引用近乎整包）则共享。
//
// 近似口径：Σ 按各 span 长度直加（嵌套重叠 span 少见，重叠只会高估 Σ → 偏向共享，安全）。
// results 元素会被就地改写（容器为导航现场新建）。
func PlanWireRetention(results []any, wholeLen int, wholeRetained bool) {
	if wholeRetained || wholeLen == 0 || len(results) == 0 {
		return
	}
	sum := 0
	for _, r := range results {
		collectWireRefs(r, &sum)
	}
	if sum == 0 || sum >= wholeLen {
		return
	}
	for i := range results {
		results[i] = detachWireRefs(results[i])
	}
}
