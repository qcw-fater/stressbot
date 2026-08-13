package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"stressbot/binding"
	flowdef "stressbot/flow"
	"testing"

	"stressbot/protocol/protox"
	"stressbot/state"

	"go.uber.org/zap"
	"stressbot/internal/stresslog"
)

// TestActionExecutorBuildBodyWithMapBinding 端到端测试：
// 从 proto 定义 → buildBody → 序列化 → Parse → GetField 验证 map 字段。
func TestActionExecutorBuildBodyWithMapBinding(t *testing.T) {
	// 初始化日志（protox loader 内部依赖）
	stresslog.ReplaceLogger(zap.NewNop())

	// 1) 创建临时 proto 文件
	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package maptest;

message GuildEditInfoC2S {
  map<int32, int32> params = 1;
}
`
	protoPath := filepath.Join(dir, "guild_edit.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}

	// 2) 加载 proto 并创建 Factory
	loader := protox.NewLoader([]string{dir}, []string{"guild_edit.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}
	factory := protox.NewFactory(protox.NewRegistry(files))

	// 3) 构建 ActionExecutor（仅 buildBody 路径不需要 netSender/adp）
	store := state.NewStore()
	ae := &ActionExecutor{
		store:   store,
		factory: factory,
	}

	// 4) 定义 flowdef.ActionDef：map binding 使用 fixed key + randomInt（min==max 保证确定性）
	def := &flowdef.ActionDef{
		Name:     "GuildEditInfo",
		Pattern:  flowdef.PatternTCPSend,
		C2SProto: "maptest.GuildEditInfoC2S",
		Bindings: []binding.FieldBind{
			{
				Field: "params",
				Type:  binding.BindMap,
				Entries: []binding.MapEntryBind{
					{Key: 1, Value: binding.FieldBind{Type: binding.BindRandomInt, Min: 0, Max: 0}},
					{Key: 2, Value: binding.FieldBind{Type: binding.BindRandomInt, Min: 1, Max: 1}},
					{Key: 3, Value: binding.FieldBind{Type: binding.BindRandomInt, Min: 200, Max: 200}},
				},
			},
		},
	}

	// 5) 调用 buildBody 构建并序列化消息
	body, err := ae.buildBody(def)
	if err != nil {
		t.Fatalf("buildBody 失败: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("buildBody 返回空 body")
	}

	// 6) 反序列化并验证 map 内容
	parsed, err := factory.Parse("maptest.GuildEditInfoC2S", body)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}

	got, err := factory.GetField(parsed, "params")
	if err != nil {
		t.Fatalf("GetField params 失败: %v", err)
	}

	// GetField 对 map 字段返回 map[string]any，value 全部转为 int64
	want := map[string]any{
		"1": int64(0),
		"2": int64(1),
		"3": int64(200),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}
