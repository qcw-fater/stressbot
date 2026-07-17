package protox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"stressbot/state"
	stresslog "stressbot/utils/log"
)

// newStoreTestFactory 构造含标量 / 嵌套 message / repeated message / repeated 标量 /
// map<string,message> / map<string,scalar> / 未设置 message 的测试工厂。
func newStoreTestFactory(t *testing.T) *Factory {
	t.Helper()
	stresslog.ReplaceLogger(zap.NewNop())

	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package storetest;

message Item {
  int32  id   = 1;
  string name = 2;
}

message Bag {
  int64             uid    = 1;
  string            title  = 2;
  Item              main   = 3;
  repeated Item     items  = 4;
  repeated int32    nums   = 5;
  map<string, Item> shelf  = 6;
  map<string, int32> counts = 7;
  Item              empty  = 8;
}
`
	protoPath := filepath.Join(dir, "store_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}
	loader := NewLoader([]string{dir}, []string{"store_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}
	return NewFactory(NewRegistry(files))
}

// buildStoreTestBag 构造一个填充了各类字段的 Bag，并经序列化→反序列化还原线上 wire 行为
// （取默认值的字段不上线）。
func buildStoreTestBag(t *testing.T, f *Factory) proto.Message {
	t.Helper()
	bag, err := f.Create("storetest.Bag")
	if err != nil {
		t.Fatalf("创建 Bag 失败: %v", err)
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("SetField 失败: %v", err)
		}
	}
	must(f.SetField(bag, "uid", int64(42)))
	must(f.SetField(bag, "title", "hello"))
	// main：设置 id=7，name 保持默认 ""（验证嵌套 message 的 proto3 默认标量保留）
	must(f.SetField(bag, "main.id", int64(7)))
	// items：两个元素
	must(f.SetField(bag, "items[0].id", int64(1)))
	must(f.SetField(bag, "items[0].name", "x"))
	must(f.SetField(bag, "items[1].id", int64(2)))
	must(f.SetField(bag, "items[1].name", "y"))
	// nums：repeated 标量
	must(f.SetField(bag, "nums", []any{10, 20, 30}))
	// counts：map<string,int32>
	must(f.SetField(bag, "counts", map[any]any{"gold": 100}))
	// empty：不设置（未设置 message）

	// shelf：map<string,Item> —— 经反射构造 message 值
	bagRef := bag.ProtoReflect()
	shelfFD := bagRef.Descriptor().Fields().ByName("shelf")
	shelfMap := bagRef.Mutable(shelfFD).Map()
	item, err := f.Create("storetest.Item")
	if err != nil {
		t.Fatalf("创建 Item 失败: %v", err)
	}
	itemRef := item.ProtoReflect()
	itemRef.Set(itemRef.Descriptor().Fields().ByName("id"), protoreflect.ValueOfInt32(9))
	itemRef.Set(itemRef.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("sword"))
	shelfMap.Set(protoreflect.ValueOfString("s1").MapKey(), protoreflect.ValueOfMessage(itemRef))

	raw, err := f.Serialize(bag)
	if err != nil {
		t.Fatalf("Serialize 失败: %v", err)
	}
	parsed, err := f.Parse("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	return parsed
}

// TestGetFieldForStoreEquivalence 校验 GetFieldForStore(msg, path) 与旧路径
// navigatePath(GetFieldMap(msg), path) 对各类路径逐字等价（P1 优化的正确性契约）。
func TestGetFieldForStoreEquivalence(t *testing.T) {
	f := newStoreTestFactory(t)
	bag := buildStoreTestBag(t, f)

	// 旧路径：整树展开后按 path 导航。
	fullMap := f.GetFieldMap(bag)

	paths := []string{
		"uid",
		"title",
		"main",
		"main.id",
		"main.name", // proto3 默认 ""，main 已设置故存在
		"items",
		"items[0]",
		"items[0].id",
		"items[1].name",
		"items[5].id", // 越界
		"nums",
		"nums[1]",
		"nums[2].x", // 标量后继续下探
		"shelf",
		"shelf.s1",
		"shelf.s1.id",
		"shelf.s1.name",
		"shelf.missing", // map 缺失 key
		"counts",
		"counts.gold",
		"counts.missing",
		"empty",    // 未设置 message
		"empty.id", // 未设置 message 下探
		"missing",  // 不存在字段
		"main[0]",  // message 节点用数组下标
		"uid.x",    // 标量下探
	}

	for _, p := range paths {
		oldVal := state.NavigatePath(fullMap, p)
		newVal, ok := f.GetFieldForStore(bag, p)

		// (ok==false) 必须与 (oldVal==nil) 一致
		if ok == (oldVal == nil) {
			t.Errorf("path %q: ok=%v 但 navigatePath=%#v（found/nil 不一致）", p, ok, oldVal)
			continue
		}
		if ok && !reflect.DeepEqual(oldVal, newVal) {
			t.Errorf("path %q: GetFieldForStore=%#v, navigatePath=%#v（值不一致）", p, newVal, oldVal)
		}
	}
}
