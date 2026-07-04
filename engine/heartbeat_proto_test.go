package engine

import (
	"os"
	"path/filepath"
	"testing"

	"stressbot/protox"
	"stressbot/state"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// ──────────────────────────────────────────────────────────────────────────
// BuildProtoBody：按 c2sProto + bindings 构造 proto body（复用 buildBody/bindFields）
// ──────────────────────────────────────────────────────────────────────────

// newHeartbeatProtoFactory 构造一个临时 proto registry：
//
//	package hbtest;
//	message HeartbeatC2S { int32 seq = 1; int64 value = 2; string token = 3; }
func newHeartbeatProtoFactory(t *testing.T) *protox.Factory {
	t.Helper()
	stresslog.ReplaceLogger(zap.NewNop())
	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package hbtest;

message HeartbeatC2S {
  int32 seq = 1;
  int64 value = 2;
  string token = 3;
}
`
	protoPath := filepath.Join(dir, "hbtest.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}
	loader := protox.NewLoader([]string{dir}, []string{"hbtest.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}
	return protox.NewFactory(protox.NewRegistry(files))
}

// TestBuildProtoBody_SimpleBindings 用真实 factory + simple bindings 构造 proto body，
// 再 Parse 回来验证字段值（证明 BuildProtoBody 复用 bindFields 路径正确）。
func TestBuildProtoBody_SimpleBindings(t *testing.T) {
	factory := newHeartbeatProtoFactory(t)
	st := state.NewStore()
	st.Set("sharedValue", int64(0x0102030405060708))

	bindings := []FieldBind{
		{Field: "seq", Type: BindFixed, Value: 42},
		{Field: "value", Type: BindState, Source: "sharedValue"},
		{Field: "token", Type: BindFixed, Value: "abc"},
	}

	body, skip, err := BuildProtoBody("hbtest.HeartbeatC2S", bindings, st, factory, "Heartbeat")
	if err != nil {
		t.Fatalf("BuildProtoBody err: %v", err)
	}
	if skip {
		t.Fatal("不应 skip")
	}
	if len(body) == 0 {
		t.Fatal("body 不应为空")
	}

	// 反序列化验证字段值（证明走的是真实 factory.Serialize）
	parsed, err := factory.Parse("hbtest.HeartbeatC2S", body)
	if err != nil {
		t.Fatalf("Parse err: %v", err)
	}
	seq, err := factory.GetField(parsed, "seq")
	if err != nil {
		t.Fatalf("GetField seq: %v", err)
	}
	if seq != int64(42) {
		t.Fatalf("seq=%v want=42", seq)
	}
	val, err := factory.GetField(parsed, "value")
	if err != nil {
		t.Fatalf("GetField value: %v", err)
	}
	if val != int64(0x0102030405060708) {
		t.Fatalf("value=%v want=0x0102030405060708", val)
	}
	tok, err := factory.GetField(parsed, "token")
	if err != nil {
		t.Fatalf("GetField token: %v", err)
	}
	if tok != "abc" {
		t.Fatalf("token=%v want=abc", tok)
	}
}

// TestBuildProtoBody_EmptyProto 返回空 body（与 buildBody 对齐：c2sProto="" → nil）。
func TestBuildProtoBody_EmptyProto(t *testing.T) {
	factory := newHeartbeatProtoFactory(t)
	body, skip, err := BuildProtoBody("", nil, state.NewStore(), factory, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skip {
		t.Fatal("空 proto 不应 skip")
	}
	if body != nil {
		t.Fatalf("空 c2sProto body 应为 nil，got=%x", body)
	}
}

// TestBuildProtoBody_UnknownProto 返回 err（不静默兜底）。
func TestBuildProtoBody_UnknownProto(t *testing.T) {
	factory := newHeartbeatProtoFactory(t)
	_, _, err := BuildProtoBody("hbtest.NoSuch", nil, state.NewStore(), factory, "")
	if err == nil {
		t.Fatal("未知 proto 应报错")
	}
}

// TestBuildProtoBody_NoBindings 空 bindings 也能正常序列化（proto3 全默认值 → 空 body，合法）。
func TestBuildProtoBody_NoBindings(t *testing.T) {
	factory := newHeartbeatProtoFactory(t)
	body, _, err := BuildProtoBody("hbtest.HeartbeatC2S", nil, state.NewStore(), factory, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// proto3 全默认值消息序列化为空 body（合法）；关键是 Create + Serialize 无 err。
	// 能 Parse 回来即证明 body 是合法 proto 序列化结果（即使为空）。
	if _, err := factory.Parse("hbtest.HeartbeatC2S", body); err != nil {
		t.Fatalf("Parse err: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// buildBody 重构无回归：buildBody 经 BuildProtoBody 走，仍能正确构造 proto body
// ──────────────────────────────────────────────────────────────────────────

// TestBuildBody_RefactoredViaBuildProtoBody 验证 buildBody 重构后行为零变化
// （复用已有的 map binding 集成测试同等构造路径）。
func TestBuildBody_RefactoredViaBuildProtoBody(t *testing.T) {
	factory := newHeartbeatProtoFactory(t)
	store := state.NewStore()
	ae := &ActionExecutor{store: store, factory: factory}

	def := &ActionDef{
		Name:     "Heartbeat",
		Pattern:  PatternTCPSend,
		C2SProto: "hbtest.HeartbeatC2S",
		Bindings: []FieldBind{
			{Field: "seq", Type: BindFixed, Value: 7},
			{Field: "token", Type: BindFixed, Value: "xyz"},
		},
	}
	body, err := ae.buildBody(def)
	if err != nil {
		t.Fatalf("buildBody err: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("buildBody 返回空 body")
	}
	parsed, err := factory.Parse("hbtest.HeartbeatC2S", body)
	if err != nil {
		t.Fatalf("Parse err: %v", err)
	}
	if seq, _ := factory.GetField(parsed, "seq"); seq != int64(7) {
		t.Fatalf("seq=%v want=7", seq)
	}
	if tok, _ := factory.GetField(parsed, "token"); tok != "xyz" {
		t.Fatalf("token=%v want=xyz", tok)
	}
}
