// Package codec_test — encode 引擎测试（纯 Go SchemaCodec，外部测试包）。
//
// 覆盖 TCP/UDP encode 行为：offset 0/11、加密/压缩组合、空 body、cmd=0、
// 头部零初始化、flags 语义、bcc 区域、BodyLength/ExpectedRouteKey 访问器、并发安全。
package codec_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"stressbot/protocol/codec"
)

// newSchemaCodecUT 加载 testdata/tcp_logic_codec.json 并构造被测 SchemaCodec。
func newSchemaCodecUT(t *testing.T) *codec.SchemaCodec {
	t.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema 失败: %v", err)
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 失败: %v", err)
	}
	return c
}

// newSchemaCodecUDP 与 newSchemaCodecUT 相同 schema，但把 enc 步的 offset 改为
// udp:battle 语义 {encode:11, decode:0}，用于 UDP 测试。
func newSchemaCodecUDP(t *testing.T) *codec.SchemaCodec {
	t.Helper()
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema 失败: %v", err)
	}
	for i := range s.Pipeline {
		if s.Pipeline[i].Name == "enc" {
			s.Pipeline[i].Offset = &codec.StepOffset{Encode: 11, Decode: 0}
		}
	}
	c, err := codec.NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 失败: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// genKey 生成长度 32 的可复现密钥。
func genKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7 + 1)
	}
	return k
}

// genBody 生成长度 n 的可复现高熵 body（xorshift32 伪随机，gzip 难以压缩）。
func genBody(n int) []byte {
	b := make([]byte, n)
	// xorshift32 状态，确保高熵以便 gzip 压缩后变大（用于 CompressionRejectedWhenLarger 分支）。
	state := uint32(0x12345678)
	for i := range b {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		b[i] = byte(state)
	}
	return b
}

// ---------------------------------------------------------------------------
// TCP/UDP encode 结构性断言
// ---------------------------------------------------------------------------

// UDP offset 11 时：前 11 字节明文必须与 body 前 11 字节相等。
func TestEncodeUDP_PlaintextPrefix_Preserved(t *testing.T) {
	ut := newSchemaCodecUDP(t)
	key := genKey()
	body := genBody(64)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeUDP(route, body, key)
	if len(frame) != 12+len(body) {
		t.Fatalf("frame len = %d, want %d", len(frame), 12+len(body))
	}
	ciphered := frame[12:]
	// 前 11 字节必须等于 body[:11]。
	if !bytes.Equal(ciphered[:11], body[:11]) {
		t.Errorf("UDP 明文前缀未保留：got %v want %v", ciphered[:11], body[:11])
	}
	// 第 12 字节起应与 body 不同（被加密）。
	if ciphered[11] == body[11] {
		t.Errorf("UDP 第 12 字节疑似未加密")
	}
}

// TCP offset 0 时：整 body 都应被加密（每字节都不同）。
func TestEncodeTCP_FullEncryption(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(64)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeTCP(route, body, key)
	ciphered := frame[12:]
	if bytes.Equal(ciphered, body) {
		t.Errorf("TCP offset 0：ciphered == plain，疑似未加密")
	}
}

// ---------------------------------------------------------------------------
// 头部零初始化：checksumOut 未执行的帧对应字节为 0
// ---------------------------------------------------------------------------

// cmd=0 + 有 key：guard 不满足 → enc 步不执行 → bcc 字段（offset 11）应为 0。
func TestEncode_HeaderZeroInit_ChecksumNotExecuted(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(64)
	route := map[string]any{"cmd": float64(0), "act": float64(7)}

	frame := ut.EncodeTCP(route, body, key)
	if len(frame) < 12 {
		t.Fatalf("frame len = %d", len(frame))
	}
	// bcc 字段在 offset 11。
	if frame[11] != 0 {
		t.Errorf("cmd=0 时 enc 步未执行，bcc 应为 0，实际 %d", frame[11])
	}
	// errorCode offset 4-5 应为 0。
	if frame[4] != 0 || frame[5] != 0 {
		t.Errorf("errorCode 应为 0，实际 %d/%d", frame[4], frame[5])
	}
	// index value offset 8-9 应为 0（schema const=0）。
	if frame[8] != 0 || frame[9] != 0 {
		t.Errorf("index value 应为 0，实际 %d/%d", frame[8], frame[9])
	}
	// flags offset 10 应为 0（既不加密也不压缩）。
	if frame[10] != 0 {
		t.Errorf("flags 应为 0（cmd=0 无 key 命中），实际 %d", frame[10])
	}
}

// ---------------------------------------------------------------------------
// flags 命名位语义断言
// ---------------------------------------------------------------------------

// 加密 + 压缩（低熵大 body）→ flags bit0(encrypted) + bit1(compressed) 都置位。
func TestEncode_Flags_EncryptedAndCompressed(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := bytes.Repeat([]byte{0x41}, 4096) // 低熵 → gzip 变小
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeTCP(route, body, key)
	flags := frame[10]
	if flags&0x01 == 0 {
		t.Errorf("期望 encrypted 位(bit0)置位，flags=%d", flags)
	}
	if flags&0x02 == 0 {
		t.Errorf("期望 compressed 位(bit1)置位，flags=%d", flags)
	}
	// 压缩后 body 应明显变小。
	bodyLen := int(binary.LittleEndian.Uint32(frame[:4]))
	if bodyLen >= len(body) {
		t.Errorf("低熵 body 应压缩后变小：bodyLen=%d orig=%d", bodyLen, len(body))
	}
}

// 高熵大 body：尝试压缩但变大 → 不采用，flags 仅 encrypted。
func TestEncode_Flags_CompressionRejectedWhenLarger(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(4096) // 高熵
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeTCP(route, body, key)
	flags := frame[10]
	if flags&0x01 == 0 {
		t.Errorf("期望 encrypted 位置位，flags=%d", flags)
	}
	if flags&0x02 != 0 {
		t.Errorf("高熵 body 压缩后变大，compressed 位不应置位，flags=%d", flags)
	}
	// body 长度不变。
	bodyLen := int(binary.LittleEndian.Uint32(frame[:4]))
	if bodyLen != len(body) {
		t.Errorf("未压缩时 bodyLen 应=%d，实际 %d", len(body), bodyLen)
	}
}

// ---------------------------------------------------------------------------
// BodyLength 访问器
// ---------------------------------------------------------------------------

func TestBodyLength(t *testing.T) {
	ut := newSchemaCodecUT(t)

	// 正常 header：bodyLen=1234，按 le 写在 offset 0。
	hdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hdr[0:], 1234)
	if got := ut.BodyLength(hdr); got != 1234 {
		t.Errorf("BodyLength = %d, want 1234", got)
	}

	// 0 长度。
	binary.LittleEndian.PutUint32(hdr[0:], 0)
	if got := ut.BodyLength(hdr); got != 0 {
		t.Errorf("BodyLength = %d, want 0", got)
	}

	// header 过短 → 返回 0。
	short := make([]byte, 2)
	if got := ut.BodyLength(short); got != 0 {
		t.Errorf("短 header BodyLength = %d, want 0", got)
	}
}

// BodyLength 与 encode 自洽：encode 后用 BodyLength 切帧应拿到正确 body 长。
func TestBodyLength_RoundtripWithEncode(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(200)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeTCP(route, body, key)
	gotBodyLen := ut.BodyLength(frame[:12])
	if gotBodyLen != len(frame)-12 {
		t.Errorf("BodyLength=%d 但 frame-body 实际长 %d", gotBodyLen, len(frame)-12)
	}
}

// ---------------------------------------------------------------------------
// ExpectedRouteKey
// ---------------------------------------------------------------------------

func TestExpectedRouteKey(t *testing.T) {
	ut := newSchemaCodecUT(t)

	cases := []struct {
		name  string
		route any
		want  string
	}{
		{"normal", map[string]any{"cmd": float64(100), "act": float64(7)}, "100:7"},
		{"zero", map[string]any{"cmd": float64(0), "act": float64(0)}, "0:0"},
		{"nil", nil, "0:0"},
		{"cmd0_act5", map[string]any{"cmd": float64(0), "act": float64(5)}, "0:5"},
		{"large_cmd", map[string]any{"cmd": float64(12345), "act": float64(255)}, "12345:255"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ut.ExpectedRouteKey(tc.route)
			if got != tc.want {
				t.Errorf("ExpectedRouteKey(%v) = %q, want %q", tc.route, got, tc.want)
			}
		})
	}
}

// route.cmd 经 math.floor 截断取整（数值经 JSON 反序列化为 float64）。
func TestExpectedRouteKey_FloorAlignment(t *testing.T) {
	ut := newSchemaCodecUT(t)
	// 非整数 float64 → math.floor 截断。
	route := map[string]any{"cmd": float64(99.7), "act": float64(3.9)}
	got := ut.ExpectedRouteKey(route)
	if got != "99:3" {
		t.Errorf("ExpectedRouteKey(99.7,3.9) = %q, want 99:3", got)
	}
}

// ---------------------------------------------------------------------------
// bcc 语义断言：xor8(plaintext body[encOffset:])
// ---------------------------------------------------------------------------
// 注：enc 步在加密之前对明文区域计算 bcc = xor8(plaintext[encOffset:])，
// 故 bcc 只取决于明文区域，与加密后字节无关。

// TCP offset 0：bcc = xor8(整 plaintext body)。
func TestBCC_TCP_XorOverPlaintextRegion(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(64)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeTCP(route, body, key)
	bccInHeader := frame[11]
	// 独立计算 xor8(plaintext body)。
	var want byte
	for _, b := range body {
		want ^= b
	}
	if bccInHeader != want {
		t.Errorf("TCP bcc=%d, want xor8(plaintext)=%d", bccInHeader, want)
	}
}

// UDP offset 11：bcc = xor8(plaintext[11:])，排除前 11 明文字节。
func TestBCC_UDP_ExcludesPlaintextPrefix(t *testing.T) {
	ut := newSchemaCodecUDP(t)
	key := genKey()
	body := genBody(64)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	frame := ut.EncodeUDP(route, body, key)
	bccInHeader := frame[11]
	// 仅对 plaintext[11:] 求 xor8。
	region := body[11:]
	var want byte
	for _, b := range region {
		want ^= b
	}
	if bccInHeader != want {
		t.Errorf("UDP bcc=%d, want xor8(plaintext[11:])=%d", bccInHeader, want)
	}
}

// ---------------------------------------------------------------------------
// HeaderSize 访问器回归
// ---------------------------------------------------------------------------

func TestHeaderSize(t *testing.T) {
	ut := newSchemaCodecUT(t)
	if got := ut.HeaderSize(); got != 12 {
		t.Errorf("HeaderSize = %d, want 12", got)
	}
}

// ---------------------------------------------------------------------------
// 并发安全（-race）：同一 SchemaCodec 多 goroutine 并发 encode 应无竞争。
// ---------------------------------------------------------------------------

func TestEncode_ConcurrentSafe(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(64)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	done := make(chan struct{}, 8)
	for range 8 {
		go func() {
			for range 50 {
				_ = ut.EncodeTCP(route, body, key)
			}
			done <- struct{}{}
		}()
	}
	for range 8 {
		<-done
	}
}
