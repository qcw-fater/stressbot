package protox

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
	"stressbot/internal/stresslog"
)

func newNestedTestFactory(t *testing.T) *Factory {
	t.Helper()

	// Suppress logging so loader.Load() does not panic on nil zap logger.
	stresslog.ReplaceLogger(zap.NewNop())

	dir := t.TempDir()
	// 嵌套 message：Outer.inner 是 message，Inner 含 int32 标量与 enum。
	// 枚举首值 LEADER=0 是 proto3 默认值——线上 wire 不会携带取默认值的枚举字段。
	protoContent := `syntax = "proto3";

package nesttest;

message Outer {
  Inner inner = 1;
}

message Inner {
  int32 count = 1;
  Role  role  = 2;
}

enum Role {
  LEADER = 0;
  MEMBER = 1;
}
`
	protoPath := filepath.Join(dir, "nested_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}

	loader := NewLoader([]string{dir}, []string{"nested_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}

	return NewFactory(NewRegistry(files))
}

// TestGetFieldMapNestedIncludesProto3DefaultEnum 验证 GetFieldMap 展开嵌套 message 时，
// 不得丢失 proto3 取默认值的标量/枚举字段（如会长职位 position=GPT_Leader=0）。
//
// 复现线上真实路径：服务端下发 inner（Has=true），其中 role 取枚举默认值 0 → proto3 序列化
// 不携带该字段 → 反序列化后 role 仍为默认 0。GetFieldMap 展开嵌套 message 时必须保留 role=0，
// 否则下游所有 "position == 0 判断会长" 的条件永远不成立，导致会长身份丢失。
func TestGetFieldMapNestedIncludesProto3DefaultEnum(t *testing.T) {
	factory := newNestedTestFactory(t)
	outer, err := factory.Create("nesttest.Outer")
	if err != nil {
		t.Fatalf("创建 Outer 失败: %v", err)
	}

	// 构造 inner 存在、count=5（非默认）、role 保持默认 LEADER=0 的场景。
	ref := outer.ProtoReflect()
	innerFD := ref.Descriptor().Fields().ByName("inner")
	innerRef := ref.Mutable(innerFD).Message()
	innerRef.Set(innerRef.Descriptor().Fields().ByName("count"), protoreflect.ValueOfInt32(5))
	// role 不显式设置，保持默认 LEADER=0（与线上会长职位一致）

	// 经 序列化→反序列化 还原线上 wire 行为（取默认值的枚举字段不上线）。
	raw, err := factory.Serialize(outer)
	if err != nil {
		t.Fatalf("Serialize 失败: %v", err)
	}
	parsed, err := factory.Parse("nesttest.Outer", raw)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}

	m := factory.GetFieldMap(parsed)
	inner, ok := m["inner"].(map[string]any)
	if !ok {
		t.Fatalf("GetFieldMap 缺少 inner 嵌套消息: %#v", m)
	}
	// count=5 非默认，转换前后都应保留（健全性校验，确保嵌套结构本身可用）
	if got := inner["count"]; got != int64(5) {
		t.Fatalf("inner.count = %#v, want int64(5)", got)
	}
	// role 取默认 0（会长职位）—— proto3 嵌套转换不得丢失零值字段
	role, ok := inner["role"]
	if !ok {
		t.Fatalf("inner.role 缺失：嵌套消息的 proto3 默认值枚举被丢弃（会长职位 position=0 丢失，身份判不出）")
	}
	if role != int64(0) {
		t.Fatalf("inner.role = %#v, want int64(0)", role)
	}
}
