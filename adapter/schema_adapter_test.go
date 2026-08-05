// Package adapter — SchemaAdapter wrapper 委托测试。
//
// 验证 adapter.NewSchemaAdapter 包装 *codec.SchemaCodec 后，9 方法逐个正确委托：
//   - 编译期断言 var _ Adapter = (*SchemaAdapter)(nil)（接口完整性，编译即验证）；
//   - HeaderSize / BodyLength / ExpectedRouteKey / EncodeTCP / EncodeUDP /
//     DecodeTCP / DecodeUDP / DescribeError / Close 行为与直接调 codec.SchemaCodec 一致。
package adapter

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"stressbot/codec"
)

// loadSchemaCodecForTest 加载 testdata schema + errorMap 构造 *codec.SchemaCodec。
// 路径相对仓库根（测试 CWD 在 adapter/，向上回溯一级到仓库根）。
func loadSchemaCodecForTest(t *testing.T) (*codec.CodecSchema, map[uint64]string) {
	t.Helper()
	// 从 adapter/ 向上找仓库根（含 codec/testdata 的目录）。
	root := findAdapterRepoRoot(t)
	schemaPath := filepath.Join(root, "codec", "testdata", "tcp_logic_codec.json")
	errorsPath := filepath.Join(root, "codec", "testdata", "errors.json")
	s, err := codec.LoadSchema(schemaPath)
	if err != nil {
		t.Fatalf("LoadSchema %s: %v", schemaPath, err)
	}
	em, err := codec.LoadErrorMap(errorsPath)
	if err != nil {
		t.Fatalf("LoadErrorMap %s: %v", errorsPath, err)
	}
	return s, em
}

// findAdapterRepoRoot 从测试 CWD（adapter/）向上找含 codec/testdata 的目录。
func findAdapterRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "codec", "testdata", "tcp_logic_codec.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("未找到含 codec/testdata/tcp_logic_codec.json 的仓库根")
	return ""
}

// TestSchemaAdapter_DelegatesAllMethods 验证 9 方法逐个委托正确。
func TestSchemaAdapter_DelegatesAllMethods(t *testing.T) {
	schema, em := loadSchemaCodecForTest(t)
	a, err := NewSchemaAdapter(schema, em)
	if err != nil {
		t.Fatalf("NewSchemaAdapter: %v", err)
	}
	t.Cleanup(a.Close)

	// 直接构造底层 *codec.SchemaCodec 作对照。
	sc, err := codec.NewSchemaCodec(schema, em)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}

	// HeaderSize
	if got, want := a.HeaderSize(), sc.HeaderSize(); got != want {
		t.Errorf("HeaderSize: got=%d want=%d", got, want)
	}

	// BodyLength
	hdr := make([]byte, 12)
	// le u32 = 1234
	hdr[0] = 0xD2
	hdr[1] = 0x04
	if got, want := a.BodyLength(hdr), sc.BodyLength(hdr); got != want {
		t.Errorf("BodyLength: got=%d want=%d", got, want)
	}

	// ExpectedRouteKey
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	if got, want := a.ExpectedRouteKey(route), sc.ExpectedRouteKey(route); got != want {
		t.Errorf("ExpectedRouteKey: got=%q want=%q", got, want)
	}

	// Encode/Decode（构造一个加密帧并双方互验）
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 1)
	}
	body := make([]byte, 64)
	for i := range body {
		body[i] = byte(i)
	}
	frame := a.EncodeTCP(route, body, key)
	frameDirect := sc.EncodeTCP(route, body, key)
	if !bytes.Equal(frame, frameDirect) {
		t.Errorf("EncodeTCP 委托不一致：wrapper≠direct")
	}

	// DecodeTCP
	r1, b1, e1 := a.DecodeTCP(frame, key)
	r2, b2, e2 := sc.DecodeTCP(frame, key)
	if r1 != r2 || !bytes.Equal(b1, b2) || e1 != e2 {
		t.Errorf("DecodeTCP 委托不一致：(%q,%v,%d) vs (%q,%v,%d)", r1, b1, e1, r2, b2, e2)
	}

	// DecodeUDP（同一 codec 单 transport；UDP schema variant 不在 wrapper 测试构造范围，
	// 这里仅验证 wrapper 调用不 panic 且与 direct 一致）。
	r3, b3, e3 := a.DecodeUDP(frame, key)
	r4, b4, e4 := sc.DecodeUDP(frame, key)
	if r3 != r4 || !bytes.Equal(b3, b4) || e3 != e4 {
		t.Errorf("DecodeUDP 委托不一致")
	}

	// EncodeUDP
	frameU := a.EncodeUDP(route, body, key)
	frameUDirect := sc.EncodeUDP(route, body, key)
	if !bytes.Equal(frameU, frameUDirect) {
		t.Errorf("EncodeUDP 委托不一致")
	}

	// DescribeError
	if got, want := a.DescribeError(0), sc.DescribeError(0); got != want {
		t.Errorf("DescribeError(0): got=%q want=%q", got, want)
	}
	if got := a.DescribeError(0); got != "成功" {
		t.Errorf("DescribeError(0)=%q want 成功", got)
	}
	if got := a.DescribeError(19); got != "消息解密失败" {
		t.Errorf("DescribeError(19)=%q want 消息解密失败", got)
	}

	// Close 幂等（多次调用不 panic）。
	a.Close()
	a.Close()
}
