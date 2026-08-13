package protox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
	"stressbot/internal/stresslog"
)

func newMapTestFactory(t *testing.T) *Factory {
	t.Helper()

	// Suppress logging so loader.Load() does not panic on nil zap logger.
	stresslog.ReplaceLogger(zap.NewNop())

	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package maptest;

message MapHolder {
  map<int32, int32> params = 1;
  map<string, int32> scores = 2;
  map<int64, string> names = 3;
}
`
	protoPath := filepath.Join(dir, "map_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}

	loader := NewLoader([]string{dir}, []string{"map_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}

	return NewFactory(NewRegistry(files))
}

func TestSetFieldMapInt32Int32(t *testing.T) {
	factory := newMapTestFactory(t)
	msg, err := factory.Create("maptest.MapHolder")
	if err != nil {
		t.Fatalf("创建 MapHolder 失败: %v", err)
	}

	// Verify the field descriptor is a protobuf map.
	paramsFD := msg.ProtoReflect().Descriptor().Fields().ByName("params")
	if paramsFD == nil {
		t.Fatalf("未找到 params 字段描述")
	}
	if !paramsFD.IsMap() {
		t.Fatalf("params 字段不是 map 类型")
	}

	// SetField should write map entries.
	if err := factory.SetField(msg, "params", map[string]any{
		"1001": int32(11),
		"1002": int64(22),
	}); err != nil {
		t.Fatalf("SetField params 失败: %v", err)
	}

	// Verify through ProtoReflect directly.
	ref := msg.ProtoReflect()
	mapVal := ref.Get(paramsFD).Map()
	if mapVal.Len() != 2 {
		t.Fatalf("params map 长度 = %d, want 2", mapVal.Len())
	}
	key1001 := protoreflect.ValueOfInt32(1001).MapKey()
	if !mapVal.Has(key1001) {
		t.Fatalf("params map 缺少 key 1001")
	}
	if mapVal.Get(key1001).Int() != 11 {
		t.Fatalf("params[1001] = %d, want 11", mapVal.Get(key1001).Int())
	}
	key1002 := protoreflect.ValueOfInt32(1002).MapKey()
	if !mapVal.Has(key1002) {
		t.Fatalf("params map 缺少 key 1002")
	}
	if mapVal.Get(key1002).Int() != 22 {
		t.Fatalf("params[1002] = %d, want 22", mapVal.Get(key1002).Int())
	}

	// Also verify through GetField returns the expected shape.
	got, err := factory.GetField(msg, "params")
	if err != nil {
		t.Fatalf("GetField params 失败: %v", err)
	}
	want := map[string]any{
		"1001": int64(11),
		"1002": int64(22),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}

func TestSetFieldMapInt32Int32FromStringMap(t *testing.T) {
	factory := newMapTestFactory(t)
	msg, err := factory.Create("maptest.MapHolder")
	if err != nil {
		t.Fatalf("创建 MapHolder 失败: %v", err)
	}

	// Simulate JSON-like input where all keys are strings and all values
	// are untyped numbers (float64 from JSON deserialization).
	if err := factory.SetField(msg, "params", map[string]any{
		"5":  float64(10),
		"20": float64(30),
	}); err != nil {
		t.Fatalf("SetField params (string-map input) 失败: %v", err)
	}

	got, err := factory.GetField(msg, "params")
	if err != nil {
		t.Fatalf("GetField params 失败: %v", err)
	}
	want := map[string]any{
		"5":  int64(10),
		"20": int64(30),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}

func TestSetFieldMapStringInt32(t *testing.T) {
	factory := newMapTestFactory(t)
	msg, err := factory.Create("maptest.MapHolder")
	if err != nil {
		t.Fatalf("创建 MapHolder 失败: %v", err)
	}

	if err := factory.SetField(msg, "scores", map[string]any{
		"alice": int32(90),
		"bob":   int64(80),
	}); err != nil {
		t.Fatalf("SetField scores 失败: %v", err)
	}

	got, err := factory.GetField(msg, "scores")
	if err != nil {
		t.Fatalf("GetField scores 失败: %v", err)
	}
	want := map[string]any{
		"alice": int64(90),
		"bob":   int64(80),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scores = %#v, want %#v", got, want)
	}
}

func TestSetFieldMapInt64String(t *testing.T) {
	factory := newMapTestFactory(t)
	msg, err := factory.Create("maptest.MapHolder")
	if err != nil {
		t.Fatalf("创建 MapHolder 失败: %v", err)
	}

	if err := factory.SetField(msg, "names", map[any]any{
		int64(9000000001): "alpha",
		int64(9000000002): "beta",
	}); err != nil {
		t.Fatalf("SetField names 失败: %v", err)
	}

	got, err := factory.GetField(msg, "names")
	if err != nil {
		t.Fatalf("GetField names 失败: %v", err)
	}
	want := map[string]any{
		"9000000001": "alpha",
		"9000000002": "beta",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func TestSetFieldMapRejectsNonMapValue(t *testing.T) {
	factory := newMapTestFactory(t)
	msg, err := factory.Create("maptest.MapHolder")
	if err != nil {
		t.Fatalf("创建 MapHolder 失败: %v", err)
	}

	err = factory.SetField(msg, "params", []any{1, 2})
	if err == nil {
		t.Fatalf("SetField params 使用非 map 值时未返回错误")
	}
	msgText := err.Error()
	if !strings.Contains(msgText, "params") {
		t.Fatalf("错误信息应包含字段名 params，实际: %v", err)
	}
	// After map support lands, the error should mention "map".
	// Before map support, toFieldValue hits the MessageKind branch and
	// reports "proto.Message" — which is also acceptable for a map field
	// (protobuf maps are internally message-valued).
	if !strings.Contains(msgText, "map") && !strings.Contains(msgText, "proto.Message") {
		t.Fatalf("错误信息应包含 map 或 proto.Message 类型提示，实际: %v", err)
	}
}
