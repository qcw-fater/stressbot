package protox

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Factory 动态消息工厂。
// 提供基于消息全名创建、序列化、反序列化、字段读写的能力。
type Factory struct {
	registry *Registry
	// pathCache 缓存字段路径 → 分段结果（map[string][]string）。
	// 字段路径集合由配置固定、数量有界（写一次读多次），Factory 跨全部 Robot 共享，
	// 故用 sync.Map：热路径 SetField/GetField 不再每次 splitPath 分配 []string。
	pathCache sync.Map
	// wireCache 广播去重缓存（见 dedup.go）：WireShared 按内容寻址共享
	// wire 字节。Factory 跨全部 Robot 共享，缓存天然覆盖全进程。
	wireCache *WireCache
}

// NewFactory 创建动态消息工厂
func NewFactory(registry *Registry) *Factory {
	return &Factory{
		registry:  registry,
		wireCache: NewWireCache(dedupMaxEntries, dedupMaxBytes),
	}
}

// MessageDescriptor 按消息名（全名或短名）查描述符，供 wire-first 存储点构造 WireValue。
func (f *Factory) MessageDescriptor(name string) (protoreflect.MessageDescriptor, bool) {
	return f.registry.Lookup(name)
}

// splitPathCached 返回 path 的分段结果，命中缓存则复用同一 []string。
//
// 返回的切片由 set/get 路径按**只读**方式消费（仅索引与子切片，绝不改写元素），
// 因此在多 Robot 间共享同一底层切片是安全的。空路径返回 nil。
func (f *Factory) splitPathCached(path string) []string {
	if v, ok := f.pathCache.Load(path); ok {
		return v.([]string)
	}
	parts := splitPath(path)
	f.pathCache.Store(path, parts)
	return parts
}

// Create 创建指定名称的动态消息。
// name 可以是全名（如 "example.RequestC2S"）或短名（如 "PlayerLoginC2S"）。
func (f *Factory) Create(name string) (proto.Message, error) {
	md, ok := f.registry.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("未找到消息类型: %s", name)
	}
	return dynamicpb.NewMessage(md), nil
}

// SetField 设置消息字段值。
// 支持 bool、整数、浮点、字符串、字节切片、枚举、嵌套消息、repeated 字段等类型。
// fieldName 支持点分路径和数组索引（如 "heroData.heroList[0].heroId"）。
func (f *Factory) SetField(msg proto.Message, fieldPath string, value any) error {
	parts := f.splitPathCached(fieldPath)
	if len(parts) == 0 {
		return fmt.Errorf("fieldPath 为空")
	}
	return f.setNestedField(msg.ProtoReflect(), parts, value)
}

// SetFieldsFromMap 批量设置消息字段。
// 遍历 map[string]any 的每个键值对，调用 SetField。
// 对 message 类型的字段，若值为 map[string]any 则自动创建子消息并递归填充。
func (f *Factory) SetFieldsFromMap(msg proto.Message, fields map[string]any) error {
	ref := msg.ProtoReflect()
	desc := ref.Descriptor()

	for key, value := range fields {
		fd := desc.Fields().ByName(protoreflect.Name(key))
		if fd == nil {
			return fmt.Errorf("消息 %s 未找到字段 %s", string(desc.FullName()), key)
		}

		// 嵌套 message 且值为 map：自动创建子消息并递归
		if fd.Kind() == protoreflect.MessageKind && !fd.IsMap() && !fd.IsList() {
			if subMap, ok := value.(map[string]any); ok {
				subMsg, err := f.Create(string(fd.Message().FullName()))
				if err != nil {
					return fmt.Errorf("自动创建子消息 %s 失败: %w", string(fd.Message().FullName()), err)
				}
				if err := f.SetFieldsFromMap(subMsg, subMap); err != nil {
					return err
				}
				ref.Set(fd, protoreflect.ValueOfMessage(subMsg.ProtoReflect()))
				continue
			}
		}

		if err := f.SetField(msg, key, value); err != nil {
			return err
		}
	}
	return nil
}

// splitPath 将 "a.b[0].c" 拆分为 ["a", "b", "[0]", "c"]
func splitPath(path string) []string {
	var out []string
	var buf []byte
	flush := func() {
		if len(buf) > 0 {
			out = append(out, string(buf))
			buf = buf[:0]
		}
	}
	for i := 0; i < len(path); i++ {
		ch := path[i]
		switch ch {
		case '.':
			flush()
		case '[':
			flush()
			j := i + 1
			for j < len(path) && path[j] != ']' {
				j++
			}
			if j < len(path) {
				out = append(out, path[i:j+1])
				i = j
			} else {
				buf = append(buf, ch)
			}
		default:
			buf = append(buf, ch)
		}
	}
	flush()
	return out
}

func (f *Factory) setNestedField(ref protoreflect.Message, parts []string, value any) error {
	desc := ref.Descriptor()
	part := parts[0]

	// 处理数组索引
	fieldName := part
	idx := -1
	if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
		return fmt.Errorf("路径不能以数组索引开头: %s", part)
	}

	// 查找字段（字段名必须与 proto 定义精确一致，大小写敏感）
	field := desc.Fields().ByName(protoreflect.Name(fieldName))
	if field == nil {
		return fmt.Errorf("消息 %s 未找到字段 %s", string(desc.FullName()), fieldName)
	}

	// 如果是最后一部分，直接赋值
	if len(parts) == 1 {
		if field.IsMap() {
			return setMapField(ref, field, value)
		}
		if field.IsList() {
			return setRepeatedField(ref, field, value)
		}
		val, err := toFieldValue(field, value)
		if err != nil {
			return err
		}
		ref.Set(field, val)
		return nil
	}

	// 如果不是最后一部分，检查下一部分是否是数组索引
	nextPart := parts[1]
	if strings.HasPrefix(nextPart, "[") && strings.HasSuffix(nextPart, "]") {
		if !field.IsList() {
			return fmt.Errorf("字段 %s 不是 repeated，但路径包含数组索引 %s", fieldName, nextPart)
		}
		idxStr := nextPart[1 : len(nextPart)-1]
		if idxStr != "" {
			fmt.Sscanf(idxStr, "%d", &idx)
		} else {
			idx = 0
		}

		list := ref.Mutable(field).List()
		for list.Len() <= idx {
			list.Append(list.NewElement())
		}
		elem := list.Get(idx)

		if len(parts) == 2 {
			val, err := toFieldValue(field, value)
			if err != nil {
				return err
			}
			list.Set(idx, val)
			return nil
		}

		if field.Kind() == protoreflect.MessageKind {
			msg := elem.Message()
			if err := f.setNestedField(msg, parts[2:], value); err != nil {
				return err
			}
			list.Set(idx, protoreflect.ValueOfMessage(msg))
			return nil
		}
		return fmt.Errorf("字段 %s 不是 message 类型，无法嵌套", fieldName)
	}

	// 下一部分不是数组索引，说明是普通的 message 嵌套
	if field.Kind() == protoreflect.MessageKind && !field.IsList() && !field.IsMap() {
		msg := ref.Mutable(field).Message()
		if err := f.setNestedField(msg, parts[1:], value); err != nil {
			return err
		}
		return nil
	}

	return fmt.Errorf("字段 %s 不是 message 类型，无法嵌套", fieldName)
}

// GetFieldForStore 按点分路径取单个字段值，语义与 navigatePath(GetFieldMap(msg), path)
// 完全一致，但**不展开整条消息**：只沿路径下探到目标字段，避免为无关的大字段
// （如未被引用的 repeated 列表）递归分配 map/slice。这是 storeResponse 热路径的关键优化。
//
// 返回 (nil,false) 表示该路径在 GetFieldMap 语义下不产出值，等价于旧路径
// navigatePath 命中 nil，具体包括：
//   - 字段不存在（按 proto 字段名**精确**匹配，与 GetFieldMap 用 fd.Name() 建 key 一致，
//     不做大小写兜底）；
//   - 中间 / 终端为未设置的 message（messageToMap 跳过未设置 message）；
//   - 终端为空的 repeated / map（messageToMap 跳过空 list/map）；
//   - 路径类型不匹配（如在标量上继续下探、在 message 节点用数组下标）。
//
// 返回值与内部消息共享底层（同 GetFieldMap 的浅拷贝语义），调用方只读使用。
func (f *Factory) GetFieldForStore(msg proto.Message, path string) (any, bool) {
	parts := f.splitPathCached(path)
	if len(parts) == 0 {
		return nil, false
	}
	return getFieldForStore(msg.ProtoReflect(), parts)
}

// getFieldForStore 在 message 节点上消费一个字段名段并按需继续下探。
// message 节点在 GetFieldMap 展开树里对应 map[string]any，故首段必须是字段名而非数组下标。
func getFieldForStore(ref protoreflect.Message, parts []string) (any, bool) {
	seg := parts[0]
	if isIndexSeg(seg) {
		// navigatePath 在 map 节点上遇到数组下标段 → navigateValue 返回 nil。
		return nil, false
	}
	fd := ref.Descriptor().Fields().ByName(protoreflect.Name(seg))
	if fd == nil {
		return nil, false
	}
	// 复刻 messageToMap 的跳过规则：未设置的 message、空 repeated/map 不进展开树。
	if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
		if !ref.Has(fd) {
			return nil, false
		}
	}
	if (fd.IsList() || fd.IsMap()) && !ref.Has(fd) {
		return nil, false
	}
	if len(parts) == 1 {
		// 终端：产出的值与 GetFieldMap 在该 key 上存的值逐字一致。
		return fromFieldValue(fd, ref.Get(fd)), true
	}
	return descendFieldValue(fd, ref.Get(fd), parts[1:])
}

// descendFieldValue 在「字段值」的展开表示上继续下探剩余路径段。
// fd/val 为父 message 上一个已确认存在的字段及其取值，parts 为字段名之后的剩余段。
func descendFieldValue(fd protoreflect.FieldDescriptor, val protoreflect.Value, parts []string) (any, bool) {
	switch {
	case fd.IsList():
		// 展开表示为 []any：只能用数组下标继续（navigateValue 在 []any 上按下标取元素）。
		seg := parts[0]
		idx, ok := parseIndexSeg(seg)
		if !ok {
			return nil, false
		}
		list := val.List()
		if idx < 0 || idx >= list.Len() {
			return nil, false
		}
		elem := list.Get(idx)
		if len(parts) == 1 {
			return fromScalarValue(fd, elem), true
		}
		if fd.Kind() == protoreflect.MessageKind {
			return getFieldForStore(elem.Message(), parts[1:])
		}
		return nil, false

	case fd.IsMap():
		// 展开表示为 map[string]any（key 为 protoreflect.MapKey.String()）。
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
			return fromScalarValue(valFd, found), true
		}
		if valFd.Kind() == protoreflect.MessageKind {
			return getFieldForStore(found.Message(), parts[1:])
		}
		return nil, false

	case fd.Kind() == protoreflect.MessageKind:
		// 单个 message：展开表示为 map[string]any，继续在子 message 上下探。
		return getFieldForStore(val.Message(), parts)

	default:
		// 标量后仍有剩余路径 → navigatePath 在标量上返回 nil。
		return nil, false
	}
}

// isIndexSeg 判断路径段是否为数组下标（如 "[0]"）。
func isIndexSeg(seg string) bool {
	return len(seg) >= 2 && seg[0] == '[' && seg[len(seg)-1] == ']'
}

// parseIndexSeg 解析数组下标段为整数；非下标段或空下标 "[]" 返回 (0,false)，
// 与 state.navigateValue（strconv.Atoi，空串报错）行为一致。
func parseIndexSeg(seg string) (int, bool) {
	if !isIndexSeg(seg) {
		return 0, false
	}
	idx, err := strconv.Atoi(seg[1 : len(seg)-1])
	if err != nil {
		return 0, false
	}
	return idx, true
}

// setRepeatedField 设置 repeated 字段值。
// value 应为 []any 切片，逐个转换并追加到 proto List。
func setRepeatedField(ref protoreflect.Message, field protoreflect.FieldDescriptor, value any) error {
	slice, ok := value.([]any)
	if !ok {
		return fmt.Errorf("repeated 字段 %s 需要 []any 类型, 实际: %T", field.Name(), value)
	}

	list := ref.Mutable(field).List()
	// 清空已有元素
	list.Truncate(0)

	for _, elem := range slice {
		val, err := toFieldValue(field, elem)
		if err != nil {
			return fmt.Errorf("repeated 字段 %s 元素转换失败: %w", field.Name(), err)
		}
		list.Append(val)
	}
	return nil
}

// mapEntryValue 表示一个 map 条目的 key/value 对。
type mapEntryValue struct {
	key   any
	value any
}

// setMapField 设置 map 字段值。
// value 接受 map[any]any 或 map[string]any。
// 清空已有 map，遍历条目并逐一写入 proto Map。
func setMapField(ref protoreflect.Message, field protoreflect.FieldDescriptor, value any) error {
	entries, err := normalizeMapEntries(value)
	if err != nil {
		return fmt.Errorf("字段 %s 是 map，绑定值需要 map 类型", field.Name())
	}

	protomap := ref.Mutable(field).Map()
	// 清空已有条目
	protomap.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		protomap.Clear(k)
		return true
	})

	valueField := field.MapValue()
	for _, entry := range entries {
		mapKey, err := toMapKey(entry.key, field.MapKey())
		if err != nil {
			return fmt.Errorf("字段 %s 的 map key=%v 无法转换为 %s: %w", field.Name(), entry.key, field.MapKey().Kind(), err)
		}
		val, err := toFieldValue(valueField, entry.value)
		if err != nil {
			return fmt.Errorf("字段 %s 的 map value(key=%v) 转换失败: %w", field.Name(), entry.key, err)
		}
		protomap.Set(mapKey, val)
	}
	return nil
}

// normalizeMapEntries 将 map[any]any 或 map[string]any 转为 []mapEntryValue。
func normalizeMapEntries(value any) ([]mapEntryValue, error) {
	switch m := value.(type) {
	case map[any]any:
		entries := make([]mapEntryValue, 0, len(m))
		for k, v := range m {
			entries = append(entries, mapEntryValue{key: k, value: v})
		}
		return entries, nil
	case map[string]any:
		entries := make([]mapEntryValue, 0, len(m))
		for k, v := range m {
			entries = append(entries, mapEntryValue{key: k, value: v})
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("需要 map 类型，实际: %T", value)
	}
}

// toMapKey 将 Go 值转换为 protoreflect.MapKey。
func toMapKey(key any, field protoreflect.FieldDescriptor) (protoreflect.MapKey, error) {
	switch field.Kind() {
	case protoreflect.BoolKind:
		v, ok := key.(bool)
		if !ok {
			return protoreflect.MapKey{}, fmt.Errorf("需要 bool, 实际: %T", key)
		}
		return protoreflect.ValueOfBool(v).MapKey(), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(toInt64Value(key))).MapKey(), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(toInt64Value(key)).MapKey(), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(uint32(toUint64Value(key))).MapKey(), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(toUint64Value(key)).MapKey(), nil
	case protoreflect.StringKind:
		v, ok := key.(string)
		if !ok {
			return protoreflect.MapKey{}, fmt.Errorf("需要 string, 实际: %T", key)
		}
		return protoreflect.ValueOfString(v).MapKey(), nil
	default:
		return protoreflect.MapKey{}, fmt.Errorf("不支持的 map key 类型: %v", field.Kind())
	}
}

// GetField 获取消息字段值。
// 返回 Go 原生类型（bool、int64、float64、string、[]byte 等）。
// repeated 字段返回 []any，map 字段返回 map[string]any。
// 嵌套消息返回 map[string]any（递归展开）。
// fieldName 支持点分路径和数组索引。
func (f *Factory) GetField(msg proto.Message, fieldPath string) (any, error) {
	parts := f.splitPathCached(fieldPath)
	if len(parts) == 0 {
		return nil, fmt.Errorf("fieldPath 为空")
	}
	return f.getNestedField(msg.ProtoReflect(), parts)
}

func (f *Factory) getNestedField(ref protoreflect.Message, parts []string) (any, error) {
	desc := ref.Descriptor()
	part := parts[0]

	if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
		return nil, fmt.Errorf("路径不能以数组索引开头: %s", part)
	}

	field := desc.Fields().ByName(protoreflect.Name(part))
	if field == nil {
		return nil, fmt.Errorf("消息 %s 未找到字段 %s", string(desc.FullName()), part)
	}

	if len(parts) == 1 {
		return fromFieldValue(field, ref.Get(field)), nil
	}

	nextPart := parts[1]
	if strings.HasPrefix(nextPart, "[") && strings.HasSuffix(nextPart, "]") {
		if !field.IsList() {
			return nil, fmt.Errorf("字段 %s 不是 repeated，但路径包含数组索引 %s", part, nextPart)
		}
		idxStr := nextPart[1 : len(nextPart)-1]
		idx := 0
		if idxStr != "" {
			fmt.Sscanf(idxStr, "%d", &idx)
		}

		list := ref.Get(field).List()
		if idx < 0 || idx >= list.Len() {
			return nil, fmt.Errorf("数组索引越界: %s", nextPart)
		}
		elem := list.Get(idx)

		if len(parts) == 2 {
			return fromScalarValue(field, elem), nil
		}

		if field.Kind() == protoreflect.MessageKind {
			return f.getNestedField(elem.Message(), parts[2:])
		}
		return nil, fmt.Errorf("字段 %s 不是 message 类型，无法嵌套", part)
	}

	if field.Kind() == protoreflect.MessageKind && !field.IsList() && !field.IsMap() {
		return f.getNestedField(ref.Get(field).Message(), parts[1:])
	}

	return nil, fmt.Errorf("字段 %s 不是 message 类型，无法嵌套", part)
}

// GetListLen 获取 repeated 字段长度，不展开列表内容。
func (f *Factory) GetListLen(msg proto.Message, fieldPath string) (int, error) {
	field, list, err := f.getListField(msg.ProtoReflect(), f.splitPathCached(fieldPath))
	if err != nil {
		return 0, err
	}
	if !field.IsList() {
		return 0, fmt.Errorf("字段 %s 不是 repeated", field.Name())
	}
	return list.Len(), nil
}

// GetListItem 获取 repeated 字段指定元素，message 元素保留为 proto.Message。
func (f *Factory) GetListItem(msg proto.Message, fieldPath string, idx int) (any, error) {
	field, list, err := f.getListField(msg.ProtoReflect(), f.splitPathCached(fieldPath))
	if err != nil {
		return nil, err
	}
	if !field.IsList() {
		return nil, fmt.Errorf("字段 %s 不是 repeated", field.Name())
	}
	if idx < 0 || idx >= list.Len() {
		return nil, fmt.Errorf("数组索引越界: %d", idx)
	}
	elem := list.Get(idx)
	if field.Kind() == protoreflect.MessageKind {
		if pm, ok := elem.Message().Interface().(proto.Message); ok {
			return pm, nil
		}
		return nil, fmt.Errorf("字段 %s 元素不是 proto.Message", field.Name())
	}
	return fromScalarValue(field, elem), nil
}

func (f *Factory) getListField(ref protoreflect.Message, parts []string) (protoreflect.FieldDescriptor, protoreflect.List, error) {
	if len(parts) == 0 {
		return nil, nil, fmt.Errorf("fieldPath 为空")
	}
	for _, part := range parts[:len(parts)-1] {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			return nil, nil, fmt.Errorf("列表路径 %s 不能以数组索引作为中间段", strings.Join(parts, "."))
		}
		field := ref.Descriptor().Fields().ByName(protoreflect.Name(part))
		if field == nil {
			return nil, nil, fmt.Errorf("消息 %s 未找到字段 %s", string(ref.Descriptor().FullName()), part)
		}
		if field.Kind() != protoreflect.MessageKind || field.IsList() || field.IsMap() {
			return nil, nil, fmt.Errorf("字段 %s 不是单个 message，无法继续读取列表", field.Name())
		}
		ref = ref.Get(field).Message()
	}

	last := parts[len(parts)-1]
	field := ref.Descriptor().Fields().ByName(protoreflect.Name(last))
	if field == nil {
		return nil, nil, fmt.Errorf("消息 %s 未找到字段 %s", string(ref.Descriptor().FullName()), last)
	}
	if !field.IsList() {
		return field, nil, nil
	}
	return field, ref.Get(field).List(), nil
}

// GetFieldMap 获取消息的所有字段值（map 形式）。
// 遍历所有字段描述符，包含 proto3 默认值字段（如 int64=0、bool=false、string=""、枚举=0）。
// 未设置的 message 类型字段和空的 repeated/map 字段会被跳过。
// 嵌套 message 递归走 messageToMap，保证嵌套结构同样保留 proto3 默认值字段
// （否则取默认值的零值字段——如会长职位 position=GPT_Leader=0——会在嵌套层丢失，
// 导致客户端把会长判成非会长）。
func (f *Factory) GetFieldMap(msg proto.Message) map[string]any {
	return messageToMap(msg.ProtoReflect())
}

// messageToMap 将 protoreflect.Message 展开为 map[string]any。
// 与 GetFieldMap 语义一致：遍历全部字段描述符，保留 proto3 标量/枚举默认值
// （int64=0、bool=false、string=""、枚举=0），仅跳过未设置的 message 字段与空的 repeated/map。
// 供 GetFieldMap 顶层与 fromScalarValue 的 MessageKind 递归共用——二者必须用同一套规则：
// 嵌套 message 若改用 msg.Range（只产出非默认值字段），会丢掉零值字段。
//
// 注意：Has(fd) 仅对 message/repeated/map 字段调用（这些类型支持 presence），
// 绝不对 proto3 标量/枚举字段调用 Has（它们无 presence，Has 会 panic）——标量/枚举一律用
// ref.Get(fd) 取值（未设置即返回默认值 0/""/false）。
func messageToMap(ref protoreflect.Message) map[string]any {
	result := make(map[string]any)

	// 遍历所有字段描述符（不仅限于已设置的字段）
	fields := ref.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// 跳过未设置的非 repeated message 字段
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() && !ref.Has(fd) {
			continue
		}
		// 跳过空的 repeated/map 字段
		if (fd.IsList() || fd.IsMap()) && !ref.Has(fd) {
			continue
		}

		result[string(fd.Name())] = fromFieldValue(fd, ref.Get(fd))
	}

	return result
}

// Serialize 序列化消息为字节切片
func (f *Factory) Serialize(msg proto.Message) ([]byte, error) {
	return proto.Marshal(msg)
}

// Parse 反序列化字节切片为指定类型的动态消息
func (f *Factory) Parse(name string, data []byte) (proto.Message, error) {
	msg, err := f.Create(name)
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("反序列化 %s 失败: %w", name, err)
	}
	return msg, nil
}

// toFieldValue 将 Go 值转换为 protoreflect.Value
func toFieldValue(field protoreflect.FieldDescriptor, value any) (protoreflect.Value, error) {
	switch field.Kind() {
	case protoreflect.BoolKind:
		v, ok := value.(bool)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("字段 %s 需要 bool 类型", field.Name())
		}
		return protoreflect.ValueOfBool(v), nil

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		v := toInt64Value(value)
		return protoreflect.ValueOfInt32(int32(v)), nil

	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		v := toInt64Value(value)
		return protoreflect.ValueOfInt64(v), nil

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		v := toUint64Value(value)
		return protoreflect.ValueOfUint32(uint32(v)), nil

	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		v := toUint64Value(value)
		return protoreflect.ValueOfUint64(v), nil

	case protoreflect.FloatKind:
		v := toFloat64Value(value)
		return protoreflect.ValueOfFloat32(float32(v)), nil

	case protoreflect.DoubleKind:
		v := toFloat64Value(value)
		return protoreflect.ValueOfFloat64(v), nil

	case protoreflect.StringKind:
		v, ok := value.(string)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("字段 %s 需要 string 类型", field.Name())
		}
		return protoreflect.ValueOfString(v), nil

	case protoreflect.BytesKind:
		v, ok := value.([]byte)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("字段 %s 需要 []byte 类型", field.Name())
		}
		return protoreflect.ValueOfBytes(v), nil

	case protoreflect.EnumKind:
		v := toInt64Value(value)
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(v)), nil

	case protoreflect.MessageKind:
		// 嵌套消息，value 应该已经是 proto.Message
		if sub, ok := value.(proto.Message); ok {
			return protoreflect.ValueOfMessage(sub.ProtoReflect()), nil
		}
		return protoreflect.Value{}, fmt.Errorf("字段 %s 需要 proto.Message 类型", field.Name())

	default:
		return protoreflect.Value{}, fmt.Errorf("不支持的字段类型: %s (kind=%v)", field.Name(), field.Kind())
	}
}

// fromFieldValue 将 protoreflect.Value 转换为 Go 值
func fromFieldValue(field protoreflect.FieldDescriptor, val protoreflect.Value) any {
	// 处理 repeated 字段（列表）
	if field.IsList() {
		list := val.List()
		result := make([]any, 0, list.Len())
		for i := 0; i < list.Len(); i++ {
			result = append(result, fromScalarValue(field, list.Get(i)))
		}
		return result
	}

	// 处理 map 字段
	if field.IsMap() {
		m := val.Map()
		result := make(map[string]any)
		m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			result[k.String()] = fromScalarValue(field.MapValue(), v)
			return true
		})
		return result
	}

	return fromScalarValue(field, val)
}

// fromScalarValue 将标量 protoreflect.Value 转为 Go 值
func fromScalarValue(field protoreflect.FieldDescriptor, val protoreflect.Value) any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return val.Bool()
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return int64(val.Int())
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return val.Int()
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return uint64(val.Uint())
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return val.Uint()
	case protoreflect.FloatKind:
		return float64(val.Float())
	case protoreflect.DoubleKind:
		return val.Float()
	case protoreflect.StringKind:
		return val.String()
	case protoreflect.BytesKind:
		return val.Bytes()
	case protoreflect.EnumKind:
		return int64(val.Enum())
	case protoreflect.MessageKind:
		// 嵌套 message：与 GetFieldMap 同源走 messageToMap，保留 proto3 默认值字段。
		// 不能用 msg.Range——它只产出非默认值字段，会丢掉零值字段（如会长职位 position=0）。
		return messageToMap(val.Message())
	default:
		return val.Interface()
	}
}

// toInt64Value 从任意数值类型转为 int64
func toInt64Value(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	case bool:
		if n {
			return 1
		}
		return 0
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
		return 0
	default:
		return 0
	}
}

// toUint64Value 从任意数值类型转为 uint64
func toUint64Value(v any) uint64 {
	switch n := v.(type) {
	case int:
		return uint64(n)
	case int32:
		return uint64(n)
	case int64:
		return uint64(n)
	case uint:
		return uint64(n)
	case uint32:
		return uint64(n)
	case uint64:
		return n
	case float64:
		return uint64(n)
	case string:
		if u, err := strconv.ParseUint(n, 10, 64); err == nil {
			return u
		}
		return 0
	default:
		return 0
	}
}

// toFloat64Value 从任意数值类型转为 float64
func toFloat64Value(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case float32:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}
