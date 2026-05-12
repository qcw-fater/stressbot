package protox

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Factory 动态消息工厂。
// 提供基于消息全名创建、序列化、反序列化、字段读写的能力。
type Factory struct {
	registry *Registry
}

// NewFactory 创建动态消息工厂
func NewFactory(registry *Registry) *Factory {
	return &Factory{registry: registry}
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
// fieldName 大小写不敏感，会自动匹配 proto 定义中的字段名。
func (f *Factory) SetField(msg proto.Message, fieldName string, value any) error {
	ref := msg.ProtoReflect()
	desc := ref.Descriptor()
	field := desc.Fields().ByName(protoreflect.Name(fieldName))
	if field == nil {
		field = findFieldCaseInsensitive(desc, fieldName)
	}
	if field == nil {
		return fmt.Errorf("消息 %s 未找到字段 %s", string(desc.FullName()), fieldName)
	}

	// 处理 repeated 字段（列表）
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

// GetField 获取消息字段值。
// 返回 Go 原生类型（bool、int64、float64、string、[]byte 等）。
// repeated 字段返回 []any，map 字段返回 map[string]any。
// 嵌套消息返回 map[string]any（递归展开）。
// fieldName 大小写不敏感。
func (f *Factory) GetField(msg proto.Message, fieldName string) (any, error) {
	ref := msg.ProtoReflect()
	desc := ref.Descriptor()
	field := desc.Fields().ByName(protoreflect.Name(fieldName))
	if field == nil {
		field = findFieldCaseInsensitive(desc, fieldName)
	}
	if field == nil {
		return nil, fmt.Errorf("消息 %s 未找到字段 %s", string(desc.FullName()), fieldName)
	}

	return fromFieldValue(field, ref.Get(field)), nil
}

// GetFieldMap 获取消息的所有字段值（map 形式）。
// 遍历所有字段描述符，包含 proto3 默认值字段（如 int64=0、bool=false、string=""）。
// 未设置的 message 类型字段和空的 repeated/map 字段会被跳过。
func (f *Factory) GetFieldMap(msg proto.Message) map[string]any {
	ref := msg.ProtoReflect()
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

		val := ref.Get(fd)
		result[string(fd.Name())] = fromFieldValue(fd, val)
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
		// 返回原始 map 表示
		msg := val.Message()
		result := make(map[string]any)
		msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			result[string(fd.Name())] = fromFieldValue(fd, v)
			return true
		})
		return result
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

// findFieldCaseInsensitive 大小写不敏感查找 proto 字段
func findFieldCaseInsensitive(desc protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	lower := strings.ToLower(name)
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if strings.ToLower(string(fd.Name())) == lower {
			return fd
		}
	}
	return nil
}
