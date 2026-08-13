package protox

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ── Wire 惰性视图：proto.* 读 API 的 wire 兼容层（D2）─────────────────
//
// listen 消费（await_*_listen 结果 / listen 脚本消息）此前必须整包解码——脚本
// 实际只经 proto.get_field/get_path 窄读两三个字段，为此解码整棵 dynamicpb 树
// 是设计错配（8000 人剖面：帧解码 6.9% CPU + FrozenCache 268MB 常驻，单人局
// flow 命中率仅 6%）。本文件把 Factory 反射读 API 的语义逐字复刻到 wire 扫描上，
// 让脚本拿到的消息 userdata 直接持字节（*WireValue），读多少扫多少。
//
// 语义契约（与解码路径逐字一致）：
//   GetFieldCompat(p)   ≡ Factory.GetField(decode(raw), p)
//   ListLenCompat(p)    ≡ Factory.GetListLen(decode(raw), p)
//   ListItemCompat(p,i) ≡ Factory.GetListItem(decode(raw), p, i)（message 元素 → 子视图）
//
// 与 NavigateSegs 的关键差别（这是 GetField 的历史语义，必须保真）：
//   - 缺席的单数 message 按「默认值实例」继续下钻（≡ 在空字节上继续扫描），
//     不是 not-found——proto.get_path(resp,"record.address") 在 record 缺席时
//     返回 ""，不是 nil；
//   - 终端 message 全量物化为 map 树（walkWireLevel 直转，零 dynamicpb）；
//   - 未知字段 / 越界下标 / 非法嵌套是 error（脚本侧 RaiseError），不是 nil。
//
// 正确性防线与导航/直转同构：L1 差分（wireview_test.go），L2/L3 线上影子采样
// （GetFieldCompat 内嵌，oracle 为 getNestedFieldValue，失配走同一套 schema 降级）。

// GetFieldCompat 按 GetField 语义读取字段（含影子采样）。
func (wv *WireValue) GetFieldCompat(path string) (any, error) {
	if wv == nil || wv.desc == nil {
		return nil, fmt.Errorf("WireValue 为空")
	}
	parts := navSplitCached(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("fieldPath 为空")
	}
	got, gerr := wireGetNested(wv.desc, wv.raw, parts)
	// fd 链暂未接入 wireGetNested（其层级推进含「缺席按默认值实例下钻」的
	// GetField 历史语义，与 wireNavigate 不同轨）；这里只复用影子采样判定。
	if _, verify := navResolve(wv.desc, parts); verify {
		return shadowVerifyGetField(wv, parts, got, gerr)
	}
	return got, gerr
}

// shadowVerifyGetField GetFieldCompat 的双读比对；失配时以 oracle 结果返回并降级。
func shadowVerifyGetField(wv *WireValue, parts []string, got any, gerr error) (any, error) {
	shadowChecks.Add(1)
	msg, err := wv.Message()
	if err != nil {
		recordWireMismatch(wv, parts, "oracle 解码失败: "+err.Error())
		return nil, err
	}
	want, werr := getNestedFieldValue(msg.ProtoReflect(), parts)
	if (gerr != nil) != (werr != nil) {
		recordWireMismatch(wv, parts, fmt.Sprintf("错误性不一致: wire=%v oracle=%v", gerr, werr))
		return want, werr
	}
	if gerr == nil && !plainEqual(got, want) {
		recordWireMismatch(wv, parts, fmt.Sprintf("wire=%v oracle=%v",
			summarizeValue(got), summarizeValue(want)))
		return want, werr
	}
	return got, gerr
}

// wireGetNested getNestedFieldValue 的 wire 版：语义逐字对齐（含错误行为）。
func wireGetNested(md protoreflect.MessageDescriptor, b []byte, parts []string) (any, error) {
	part := parts[0]
	if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
		return nil, fmt.Errorf("路径不能以数组索引开头: %s", part)
	}
	fd := md.Fields().ByName(protoreflect.Name(part))
	if fd == nil {
		return nil, fmt.Errorf("消息 %s 未找到字段 %s", string(md.FullName()), part)
	}

	if len(parts) == 1 {
		return wireFieldTerminal(md, b, fd)
	}

	nextPart := parts[1]
	if strings.HasPrefix(nextPart, "[") && strings.HasSuffix(nextPart, "]") {
		if !fd.IsList() {
			return nil, fmt.Errorf("字段 %s 不是 repeated，但路径包含数组索引 %s", part, nextPart)
		}
		idx := sscanfIndex(nextPart)
		if idx < 0 {
			return nil, fmt.Errorf("数组索引越界: %s", nextPart)
		}
		// 下标访问只扫到第 idx+1 个元素即停（前缀即定值，见 wireCollectList）。
		elems, ok := wireCollectList(b, fd, idx+1)
		if !ok {
			return nil, fmt.Errorf("字段 %s wire 结构损坏", fd.FullName())
		}
		if idx >= len(elems) {
			return nil, fmt.Errorf("数组索引越界: %s", nextPart)
		}
		e := elems[idx]
		if len(parts) == 2 {
			if e.isMsg {
				return wireToMapTree(fd.Message(), e.span)
			}
			return e.scalar, nil
		}
		if fd.Kind() == protoreflect.MessageKind {
			return wireGetNested(fd.Message(), e.span, parts[2:])
		}
		return nil, fmt.Errorf("字段 %s 不是 message 类型，无法嵌套", part)
	}

	if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
		sub, err := wireSingularSpan(md, b, fd)
		if err != nil {
			return nil, err
		}
		// 缺席 → sub 为空字节 ≡ 默认值实例，继续下钻（GetField 历史语义）。
		return wireGetNested(fd.Message(), sub, parts[1:])
	}
	return nil, fmt.Errorf("字段 %s 不是 message 类型，无法嵌套", part)
}

// wireFieldTerminal fromFieldValue 的 wire 版（终端物化）。
func wireFieldTerminal(md protoreflect.MessageDescriptor, b []byte, fd protoreflect.FieldDescriptor) (any, error) {
	switch {
	case fd.IsMap():
		entries, keys, ok := wireCollectMap(b, fd)
		if !ok {
			return nil, fmt.Errorf("map %s wire 结构损坏", fd.FullName())
		}
		valFd := fd.MapValue()
		out := make(map[string]any, len(entries))
		for _, k := range keys {
			st := entries[k]
			switch {
			case valFd.Kind() == protoreflect.MessageKind:
				m, err := wireToMapTree(valFd.Message(), concatSpans(st.spans))
				if err != nil {
					return nil, err
				}
				out[k] = m
			case st.hasScalar:
				out[k] = st.scalar
			default:
				out[k] = fromScalarValue(valFd, valFd.Default())
			}
		}
		return out, nil

	case fd.IsList():
		elems, ok := wireCollectList(b, fd, 0)
		if !ok {
			return nil, fmt.Errorf("字段 %s wire 结构损坏", fd.FullName())
		}
		out := make([]any, 0, len(elems))
		for _, e := range elems {
			if e.isMsg {
				m, err := wireToMapTree(fd.Message(), e.span)
				if err != nil {
					return nil, err
				}
				out = append(out, m)
			} else {
				out = append(out, e.scalar)
			}
		}
		return out, nil

	case fd.Kind() == protoreflect.MessageKind:
		// 缺席 → 空字节 ≡ 默认值实例的整表（含标量默认值）。
		sub, err := wireSingularSpan(md, b, fd)
		if err != nil {
			return nil, err
		}
		return wireToMapTree(fd.Message(), sub)

	case fd.Kind() == protoreflect.GroupKind:
		return nil, fmt.Errorf("字段 %s 为 group 类型，不支持", fd.FullName())

	default: // 标量/枚举：last-wins 或默认值（oneof 落选成员同默认值）
		res, ok := wireCollectSingular(md, b, fd)
		if !ok {
			return nil, fmt.Errorf("字段 %s wire 结构损坏", fd.FullName())
		}
		if res.member == fd && res.hasScalar {
			return res.scalar, nil
		}
		return fromScalarValue(fd, fd.Default()), nil
	}
}

// wireSingularSpan 收集单数 message 字段的合并字节段；缺席返回空字节。
func wireSingularSpan(md protoreflect.MessageDescriptor, b []byte, fd protoreflect.FieldDescriptor) ([]byte, error) {
	res, ok := wireCollectSingular(md, b, fd)
	if !ok {
		return nil, fmt.Errorf("字段 %s wire 结构损坏", fd.FullName())
	}
	if res.member != fd || len(res.spans) == 0 {
		return nil, nil
	}
	return concatSpans(res.spans), nil
}

// wireToMapTree 整树直转为 map（messageToMap 同形）。
func wireToMapTree(md protoreflect.MessageDescriptor, b []byte) (map[string]any, error) {
	sink := newMapTreeSink()
	if err := walkWireLevel(md, b, sink, wireWalkRecursionLimit); err != nil {
		return nil, err
	}
	return sink.m, nil
}

// sscanfIndex 复刻 getNestedFieldValue 的 fmt.Sscanf("%d") 下标解析：解析失败取 0。
func sscanfIndex(seg string) int {
	s := seg[1 : len(seg)-1]
	if s == "" {
		return 0
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return 0
}

// ── 列表兼容读（GetListLen / GetListItem 的 wire 版）───────────────────

// wireListField 复刻 getListField 的路径行为：中间段必须是单数 message
// （缺席 ≡ 空字节继续），终端返回字段与其元素。
// limit 语义同 wireCollectList：>0 时收集到至少 limit 个元素即停，<=0 收全量。
func wireListField(md protoreflect.MessageDescriptor, b []byte, parts []string, limit int) (protoreflect.FieldDescriptor, []wireElem, error) {
	if len(parts) == 0 {
		return nil, nil, fmt.Errorf("fieldPath 为空")
	}
	for _, part := range parts[:len(parts)-1] {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			return nil, nil, fmt.Errorf("列表路径 %s 不能以数组索引作为中间段", strings.Join(parts, "."))
		}
		fd := md.Fields().ByName(protoreflect.Name(part))
		if fd == nil {
			return nil, nil, fmt.Errorf("消息 %s 未找到字段 %s", string(md.FullName()), part)
		}
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
			return nil, nil, fmt.Errorf("字段 %s 不是单个 message，无法继续读取列表", fd.Name())
		}
		sub, err := wireSingularSpan(md, b, fd)
		if err != nil {
			return nil, nil, err
		}
		md, b = fd.Message(), sub
	}

	last := parts[len(parts)-1]
	fd := md.Fields().ByName(protoreflect.Name(last))
	if fd == nil {
		return nil, nil, fmt.Errorf("消息 %s 未找到字段 %s", string(md.FullName()), last)
	}
	if !fd.IsList() {
		return fd, nil, nil
	}
	elems, ok := wireCollectList(b, fd, limit)
	if !ok {
		return nil, nil, fmt.Errorf("字段 %s wire 结构损坏", fd.FullName())
	}
	return fd, elems, nil
}

// WireListCursor 列表游标：一遍收集全部元素跨度后逐个产出。
//
// 动机：脚本顺序遍历列表若用 ListItemCompat 逐下标取，每次调用都要
// wireCollectList 重扫父层级定位全部元素，整链 O(n²)；游标把收集压成一遍
// （message 元素只存字节跨度引用，零解码零复制），Item 按需产出，整链 O(n)。
// 元素语义与 ListItemCompat(path, i) 逐项一致（message → *WireValue 子视图）。
//
// elems 的跨度引用父快照字节；WireValue 不可变，游标跨协程挂起、跨 key 覆盖
// 均安全，无失效协议。
type WireListCursor struct {
	msgDesc protoreflect.MessageDescriptor // 元素为 message 时非 nil
	elems   []wireElem
}

// Len 元素个数。
func (c *WireListCursor) Len() int {
	if c == nil {
		return 0
	}
	return len(c.elems)
}

// Item 取第 i 个元素（0-based）：message 元素返回 *WireValue 子视图（只读性
// 全树传播），标量元素返回收集时装箱的值。越界返回 nil（调用方按 Len 迭代）。
func (c *WireListCursor) Item(i int) any {
	if c == nil || i < 0 || i >= len(c.elems) {
		return nil
	}
	e := c.elems[i]
	if e.isMsg {
		return &WireValue{desc: c.msgDesc, raw: e.span}
	}
	return e.scalar
}

// ListCursorCompat 构造 path 所指列表的游标。
// fd 非 repeated 时返回空游标（nil error）——对齐 iter_list 经 GetField 读标量
// 字段时"空迭代"的历史行为；结构损坏 / 路径非法返回 error。
func (wv *WireValue) ListCursorCompat(path string) (*WireListCursor, error) {
	if wv == nil || wv.desc == nil {
		return nil, fmt.Errorf("WireValue 为空")
	}
	fd, elems, err := wireListField(wv.desc, wv.raw, navSplitCached(path), 0)
	if err != nil {
		return nil, err
	}
	if !fd.IsList() {
		return &WireListCursor{}, nil
	}
	c := &WireListCursor{elems: elems}
	if fd.Kind() == protoreflect.MessageKind {
		c.msgDesc = fd.Message()
	}
	return c, nil
}

// ListLenCompat GetListLen 的 wire 版。
func (wv *WireValue) ListLenCompat(path string) (int, error) {
	if wv == nil || wv.desc == nil {
		return 0, fmt.Errorf("WireValue 为空")
	}
	fd, elems, err := wireListField(wv.desc, wv.raw, navSplitCached(path), 0)
	if err != nil {
		return 0, err
	}
	if !fd.IsList() {
		return 0, fmt.Errorf("字段 %s 不是 repeated", fd.Name())
	}
	return len(elems), nil
}

// ListItemCompat GetListItem 的 wire 版；message 元素返回 *WireValue 子视图
// （调用方以只读视图包装，只读性全树传播）。
func (wv *WireValue) ListItemCompat(path string, idx int) (any, error) {
	if wv == nil || wv.desc == nil {
		return nil, fmt.Errorf("WireValue 为空")
	}
	if idx < 0 {
		return nil, fmt.Errorf("数组索引越界: %d", idx)
	}
	// 只扫到第 idx+1 个元素即停（前缀即定值，见 wireCollectList）。
	fd, elems, err := wireListField(wv.desc, wv.raw, navSplitCached(path), idx+1)
	if err != nil {
		return nil, err
	}
	if !fd.IsList() {
		return nil, fmt.Errorf("字段 %s 不是 repeated", fd.Name())
	}
	if idx >= len(elems) {
		return nil, fmt.Errorf("数组索引越界: %d", idx)
	}
	e := elems[idx]
	if e.isMsg {
		return &WireValue{desc: fd.Message(), raw: e.span}, nil
	}
	return e.scalar, nil
}
