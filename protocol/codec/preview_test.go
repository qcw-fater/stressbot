// Package codec_test — T1.7 第 4 项 preview helper 测试。
//
// 覆盖：
//   - 合法 encode → FrameHex + Fields（逐字段值正确）。
//   - encode 后 decode 往返：BodyHex 还原、RouteKey、HeaderErr=0、Fields 一致。
//   - 畸形 schema → Error 填中文，不 panic。
//   - 坏 hex（bodyHex/frameHex/keyHex 含非 hex 字符）→ Error 填中文。
//   - 未知 mode/transport → Error 填中文。
//   - nil schema → Error。
//   - route 字段 string 值 → 数值化（与 int 值结果一致）。
package codec_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stressbot/protocol/codec"
)

// loadSchemaForPreview 加载 testdata/tcp_logic_codec.json（preview 不接 errorMap）。
func loadSchemaForPreview(t *testing.T) *codec.Schema {
	t.Helper()
	// 兼容从 codec/ 或仓库根运行。
	candidates := []string{
		"testdata/tcp_logic_codec.json",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			s, err := codec.LoadSchema(c)
			if err != nil {
				t.Fatalf("LoadSchema %s: %v", c, err)
			}
			return s
		}
	}
	// 回溯找仓库根。
	dir, _ := os.Getwd()
	for range 8 {
		p := filepath.Join(dir, "codec", "testdata", "tcp_logic_codec.json")
		if _, err := os.Stat(p); err == nil {
			s, err := codec.LoadSchema(p)
			if err != nil {
				t.Fatalf("LoadSchema %s: %v", p, err)
			}
			return s
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("未找到 tcp_logic_codec.json")
	return nil
}

// findField 在 Fields 中按名查；缺失 fatal。
func findField(t *testing.T, fields []codec.PreviewField, name string) codec.PreviewField {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("字段 %q 未在 Fields 中找到", name)
	return codec.PreviewField{}
}

// genKeyHex 32B key 的 hex（与 engine_test genKey 同种子）。
func genKeyHex() string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7 + 1)
	}
	return hex.EncodeToString(k)
}

func TestPreview_Encode_Valid(t *testing.T) {
	s := loadSchemaForPreview(t)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	bodyHex := hex.EncodeToString([]byte("hello world payload"))

	res := codec.Preview(s, "encode", "tcp", route, bodyHex, genKeyHex(), "")
	if res.Error != "" {
		t.Fatalf("encode 不应有错：%q", res.Error)
	}
	if res.Mode != "encode" {
		t.Errorf("Mode=%q want encode", res.Mode)
	}
	if res.FrameHex == "" {
		t.Fatal("FrameHex 为空")
	}
	// FrameHex 解码后长度 = header(12) + 加密 body（与明文等长，xor_carry_rol 定长）。
	frameBytes, err := hex.DecodeString(res.FrameHex)
	if err != nil {
		t.Fatalf("FrameHex 非 hex：%v", err)
	}
	if len(frameBytes) != 12+len("hello world payload") {
		t.Errorf("frame len=%d want %d", len(frameBytes), 12+len("hello world payload"))
	}
	// Fields 应包含全部 7 个 header 字段。
	if len(res.Fields) != 7 {
		t.Errorf("Fields 数=%d want 7", len(res.Fields))
	}
	// cmd=100，act=7。
	if got := findField(t, res.Fields, "cmd"); got.Value != 100 {
		t.Errorf("cmd Value=%d want 100", got.Value)
	}
	if got := findField(t, res.Fields, "act"); got.Value != 7 {
		t.Errorf("act Value=%d want 7", got.Value)
	}
	// flags：加密置位 bit0（cmd!=0 且有 key）。
	if got := findField(t, res.Fields, "flags"); got.Value&0x01 == 0 {
		t.Errorf("flags=%d 应置 encrypted 位(bit0)", got.Value)
	}
	// bcc 字段应非零（已加密）。
	if got := findField(t, res.Fields, "bcc"); got.Value == 0 {
		t.Errorf("bcc=0，加密后应非零")
	}
}

func TestPreview_EncodeDecode_Roundtrip(t *testing.T) {
	s := loadSchemaForPreview(t)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	origBody := "round-trip-body-12345"
	bodyHex := hex.EncodeToString([]byte(origBody))
	keyHex := genKeyHex()

	enc := codec.Preview(s, "encode", "tcp", route, bodyHex, keyHex, "")
	if enc.Error != "" {
		t.Fatalf("encode：%q", enc.Error)
	}
	// 用 encode 出的 frameHex 做 decode 入参。
	dec := codec.Preview(s, "decode", "tcp", nil, "", keyHex, enc.FrameHex)
	if dec.Error != "" {
		t.Fatalf("decode：%q", dec.Error)
	}
	if dec.RouteKey != "100:7" {
		t.Errorf("RouteKey=%q want 100:7", dec.RouteKey)
	}
	if dec.HeaderErr != 0 {
		t.Errorf("HeaderErr=%d want 0", dec.HeaderErr)
	}
	// BodyHex 应还原 origBody 的 hex。
	if dec.BodyHex != bodyHex {
		t.Errorf("BodyHex 往还原失败\n got=%s\n want=%s", dec.BodyHex, bodyHex)
	}
	// decode 侧 Fields 也应解析出 cmd=100/act=7。
	if got := findField(t, dec.Fields, "cmd"); got.Value != 100 {
		t.Errorf("decode cmd Value=%d want 100", got.Value)
	}
}

func TestPreview_Encode_RouteStringValues(t *testing.T) {
	s := loadSchemaForPreview(t)
	// route 用 string 值——应被数值化为与 int 一致。
	routeStr := map[string]any{"cmd": "100", "act": "7"}
	routeInt := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := []byte("payload for route-string test")
	bodyHex := hex.EncodeToString(body)
	keyHex := genKeyHex()

	resStr := codec.Preview(s, "encode", "tcp", routeStr, bodyHex, keyHex, "")
	if resStr.Error != "" {
		t.Fatalf("string route encode：%q", resStr.Error)
	}
	resInt := codec.Preview(s, "encode", "tcp", routeInt, bodyHex, keyHex, "")
	if resInt.Error != "" {
		t.Fatalf("int route encode：%q", resInt.Error)
	}
	if resStr.FrameHex != resInt.FrameHex {
		t.Errorf("string route 与 int route 应产生同一帧\n str=%s\n int=%s",
			resStr.FrameHex, resInt.FrameHex)
	}
}

func TestPreview_Decode_ShortFrame(t *testing.T) {
	s := loadSchemaForPreview(t)
	// 短帧（< headerSize）→ decode 返回空三件套；Preview 仍填 Fields（前 12 字节不足时各自零）。
	shortHex := hex.EncodeToString([]byte{0x01, 0x02, 0x03})
	res := codec.Preview(s, "decode", "tcp", nil, "", genKeyHex(), shortHex)
	if res.Error != "" {
		t.Fatalf("短帧 decode 不应报错：%q", res.Error)
	}
	if res.RouteKey != "" {
		t.Errorf("短帧 RouteKey=%q want 空串", res.RouteKey)
	}
}

func TestPreview_NilSchema_Error(t *testing.T) {
	res := codec.Preview(nil, "encode", "tcp", nil, "00", "", "")
	if res.Error == "" {
		t.Fatal("nil schema 应填 Error")
	}
	if !strings.Contains(res.Error, "schema") {
		t.Errorf("nil schema Error 应含 schema 字样，got %q", res.Error)
	}
}

func TestPreview_BadSchema_Error(t *testing.T) {
	// 缺 role:"length" 字段的非法 schema。
	bad := &codec.Schema{
		Version:       1,
		EndianDefault: "le",
		Frame:         codec.FrameSpec{HeaderSize: 12},
		Header: []codec.Field{
			{Name: "cmd", Offset: 0, Size: 1, Type: "u8", Role: "route"},
		},
		RouteKeyTmpl: "{cmd}",
	}
	res := codec.Preview(bad, "encode", "tcp", map[string]any{"cmd": float64(1)}, "00", "", "")
	if res.Error == "" {
		t.Fatal("畸形 schema 应填 Error")
	}
	// 错误信息应为中文（含「校验失败」或「缺」等）。
	if !containsHan(res.Error) {
		t.Errorf("畸形 schema Error 应为中文，got %q", res.Error)
	}
}

func TestPreview_BadBodyHex_Error(t *testing.T) {
	s := loadSchemaForPreview(t)
	res := codec.Preview(s, "encode", "tcp", map[string]any{"cmd": float64(1)}, "xyz非hex", "", "")
	if res.Error == "" {
		t.Fatal("坏 bodyHex 应填 Error")
	}
	if !strings.Contains(res.Error, "bodyHex") {
		t.Errorf("坏 bodyHex Error 应含 bodyHex 字样，got %q", res.Error)
	}
}

func TestPreview_BadKeyHex_Error(t *testing.T) {
	s := loadSchemaForPreview(t)
	res := codec.Preview(s, "encode", "tcp", map[string]any{"cmd": float64(1)}, "00", "nothex", "")
	if res.Error == "" {
		t.Fatal("坏 keyHex 应填 Error")
	}
	if !strings.Contains(res.Error, "keyHex") {
		t.Errorf("坏 keyHex Error 应含 keyHex 字样，got %q", res.Error)
	}
}

func TestPreview_BadFrameHex_Error(t *testing.T) {
	s := loadSchemaForPreview(t)
	res := codec.Preview(s, "decode", "tcp", nil, "", "", "ZZ")
	if res.Error == "" {
		t.Fatal("坏 frameHex 应填 Error")
	}
	if !strings.Contains(res.Error, "frameHex") {
		t.Errorf("坏 frameHex Error 应含 frameHex 字样，got %q", res.Error)
	}
}

func TestPreview_UnknownMode_Error(t *testing.T) {
	s := loadSchemaForPreview(t)
	res := codec.Preview(s, "frobnicate", "tcp", nil, "00", "", "")
	if res.Error == "" {
		t.Fatal("未知 mode 应填 Error")
	}
	if !strings.Contains(res.Error, "mode") {
		t.Errorf("未知 mode Error 应含 mode 字样，got %q", res.Error)
	}
}

func TestPreview_UnknownTransport_Error(t *testing.T) {
	s := loadSchemaForPreview(t)
	res := codec.Preview(s, "encode", "icmp", nil, "00", "", "")
	if res.Error == "" {
		t.Fatal("未知 transport 应填 Error")
	}
	if !strings.Contains(res.Error, "协议方向") {
		t.Errorf("未知 transport Error 应含「协议方向」，got %q", res.Error)
	}
}

func TestPreview_NoKey_EncodesWithoutEncryption(t *testing.T) {
	s := loadSchemaForPreview(t)
	// 无 key → encode 不加密（cmd!=0 但 key 空）。
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	bodyHex := hex.EncodeToString([]byte("plain body"))
	res := codec.Preview(s, "encode", "tcp", route, bodyHex, "", "")
	if res.Error != "" {
		t.Fatalf("无 key encode：%q", res.Error)
	}
	flags := findField(t, res.Fields, "flags")
	if flags.Value != 0 {
		t.Errorf("无 key flags=%d 应为 0（不加密不压缩）", flags.Value)
	}
}

// containsHan 粗判字符串是否含汉字（Unicode Han）。
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
