// Package codec_test — encode 引擎对拍测试（外部测试包，避免 codec↔adapter 循环）。
//
// 验收核心：字节级对拍 conf/adapter/codec.lua 经旧 adapter.LuaAdapter 的 encode 输出。
// 测试在此 import adapter 包仅用于构造真值 oracle（gopher-lua 经 adapter 传递引入，
// 仅测试代码、不影响 codec 包生产依赖；go build ./codec 仍保持零 gopher-lua）。
//
// 本文件位于外部测试包 codec_test（而非内部 package codec）：因 adapter/schema_adapter.go
// 反向 import 了 codec，构成 codec(test)→adapter→codec(production) 的循环；外部测试包
// 编译为独立包，可同时 import codec 与 adapter 而不形成循环。codec 内部测试包
// （compile_test.go / registry_test.go / schema_test.go，需访问未导出符号）保留不变。
//
// 组合覆盖矩阵：
//   - TCP encode：offset 0、加密/不加密、压缩/不压缩、空 body、cmd=0
//   - UDP encode：offset 11（前 11 明文 + bcc 排除前缀）
//   - BodyLength / ExpectedRouteKey 行为与 helpers / lua_adapter 对齐
//   - 头部零初始化：checksumOut 未执行的帧对应字节为 0
package codec_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stressbot/adapter"
	"stressbot/codec"
)

// ---------------------------------------------------------------------------
// 真值 oracle：旧 LuaAdapter（codec.lua + error.lua）
// ---------------------------------------------------------------------------

// adapterPaths 返回 worktree 根下的 conf/adapter/codec.lua / error.lua 绝对路径。
// 测试可从 codec/ 目录运行（go test ./codec），故向上回溯一级。
func adapterPaths(t *testing.T) (codecPath, errorPath string) {
	t.Helper()
	root := findRepoRoot(t)
	return filepath.Join(root, "conf", "adapter", "codec.lua"),
		filepath.Join(root, "conf", "adapter", "error.lua")
}

// findRepoRoot 从测试 CWD 向上查找含 conf/adapter/codec.lua 的目录。
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "conf", "adapter", "codec.lua")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("未找到含 conf/adapter/codec.lua 的仓库根目录")
	return ""
}

// newLuaOracle 构造旧 LuaAdapter 作为 encode 真值 oracle。
//
// 池大小用 2：poolSize=1 时 NewLuaAdapter 在初始化期持有一个 LState，加载
// error.lua 时再次 acquire 会耗尽池并超时；这是旧 Lua oracle 的初始化限制，
// 与 codec 包无关。poolSize=2 验证可正常构造；codec 测试串行调用无需更大池。
func newLuaOracle(t *testing.T) *adapter.LuaAdapter {
	t.Helper()
	codecPath, errorPath := adapterPaths(t)
	a, err := adapter.NewLuaAdapter(2, codecPath, errorPath)
	if err != nil {
		t.Fatalf("NewLuaAdapter 失败: %v", err)
	}
	t.Cleanup(a.Close)
	return a
}

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
// udp:battle 语义 {encode:11, decode:0}，用于 UDP 对拍。
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

// genBody 生成长度 n 的可复现高熵 body（xorshift128 伪随机，gzip 难以压缩）。
func genBody(n int) []byte {
	b := make([]byte, n)
	// xorshift32 状态，确保高熵以便 gzip 压缩后变大（用于 onlySmaller 对拍分支）。
	state := uint32(0x12345678)
	for i := range b {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		b[i] = byte(state)
	}
	return b
}

// hexStr 用于失败诊断。
func hexStr(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2+2)
	out = append(out, '0', 'x')
	for _, c := range b {
		out = append(out, h[c>>4], h[c&0xF])
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// TCP 对拍矩阵
// ---------------------------------------------------------------------------

func TestEncodeTCP_Parity_LuaAdapter(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUT(t)
	key := genKey()

	// 路由：与 codec.lua math.floor 行为对齐（数值经 JSON 反序列化为 float64）。
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	cases := []struct {
		name  string
		route map[string]any
		body  []byte
		key   []byte // nil = 不传 key
	}{
		// 加密 + 不压缩（body < 2048）。
		{"small_encrypted", route, genBody(64), key},
		// 加密 + 不压缩，中等 body。
		{"medium_encrypted", route, genBody(1024), key},
		// 加密 + 压缩（body >= 2048，随机字节通常压缩后变大→不压缩；用低熵 body 保证压缩变小）。
		// 低熵 body：单字节重复，gzip 必然变小。
		{"large_compressible_encrypted", route, bytes.Repeat([]byte{0x41}, 4096), key},
		// 加密 + 高熵大 body（>= 2048，但压缩后变大→应丢弃压缩结果，flag 不置 compressed）。
		{"large_incompressible_encrypted", route, genBody(4096), key},
		// 不加密（无 key）：cmd!=0 但无合法 key → codec.lua 不加密。
		{"small_no_key", route, genBody(64), nil},
		// cmd=0：guard neq 0 不满足 → 不加密（即使有 key）。
		{"cmd0_with_key", map[string]any{"cmd": float64(0), "act": float64(7)}, genBody(64), key},
		// cmd=0 + 无 key。
		{"cmd0_no_key", map[string]any{"cmd": float64(0), "act": float64(7)}, genBody(64), nil},
		// 空 body：codec.lua #data==0 → 不压缩、不加密。
		{"empty_body_encrypted", route, nil, key},
		{"empty_body_no_key", route, nil, nil},
		// 单字节 body。
		{"one_byte_encrypted", route, []byte{0x42}, key},
		// route nil（cmd=act=0）。
		{"nil_route", nil, genBody(32), key},
		// route act=0 cmd!=0：仍加密（guard 只看 cmd）。
		{"act0_encrypted", map[string]any{"cmd": float64(50), "act": float64(0)}, genBody(100), key},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := tc.key
			var expected []byte
			if k == nil {
				expected = oracle.EncodeTCP(tc.route, tc.body, nil)
			} else {
				expected = oracle.EncodeTCP(tc.route, tc.body, k)
			}
			if expected == nil {
				t.Fatalf("oracle encode 返回 nil（脚本错误）")
			}
			got := ut.EncodeTCP(tc.route, tc.body, k)
			if !bytes.Equal(got, expected) {
				t.Fatalf("TCP encode 不一致\n name=%s\n got=%s\n want=%s",
					tc.name, hexStr(got), hexStr(expected))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UDP 对拍矩阵（offset 11）
// ---------------------------------------------------------------------------

func TestEncodeUDP_Parity_LuaAdapter(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUDP(t) // encOffset=11
	key := genKey()

	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	cases := []struct {
		name  string
		route map[string]any
		body  []byte
		key   []byte
	}{
		// body 必须大于 11 才能让 offset 11 的明文前缀真正出现。
		{"udp_small_encrypted_offset11", route, genBody(64), key},
		{"udp_medium_encrypted_offset11", route, genBody(256), key},
		{"udp_compressible_encrypted_offset11", route, bytes.Repeat([]byte{0x41}, 4096), key},
		{"udp_no_key", route, genBody(64), nil},
		{"udp_cmd0_with_key", map[string]any{"cmd": float64(0), "act": float64(7)}, genBody(64), key},
		// body 短于 offset 11：codec.lua net_encrypt 在 offset>=len 时处理空段，flag 仍置位、bcc=0。
		{"udp_body_shorter_than_offset", route, genBody(8), key},
		// 空 body：UDP offset 11 但 body 为空 → 不加密。
		{"udp_empty_body", route, nil, key},
		// 恰好 11 字节：处理区域为空。
		{"udp_body_equals_offset", route, genBody(11), key},
		// 12 字节：处理区域 1 字节。
		{"udp_body_one_byte_after_offset", route, genBody(12), key},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := tc.key
			var expected []byte
			if k == nil {
				expected = oracle.EncodeUDP(tc.route, tc.body, nil)
			} else {
				expected = oracle.EncodeUDP(tc.route, tc.body, k)
			}
			if expected == nil {
				t.Fatalf("oracle encode 返回 nil")
			}
			got := ut.EncodeUDP(tc.route, tc.body, k)
			if !bytes.Equal(got, expected) {
				t.Fatalf("UDP encode 不一致\n name=%s\n got=%s\n want=%s",
					tc.name, hexStr(got), hexStr(expected))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TCP 与 UDP 在 offset 0 / 11 上的对比（结构性断言，非对拍）
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

	// 与 LuaAdapter BodyLength 对齐。
	oracle := newLuaOracle(t)
	for _, n := range []int{0, 1, 255, 1024, 65535, 1 << 20} {
		h := make([]byte, 12)
		binary.LittleEndian.PutUint32(h[0:], uint32(n))
		got := ut.BodyLength(h)
		want := oracle.BodyLength(h)
		if got != want {
			t.Errorf("BodyLength(n=%d)：got=%d want(oracle)=%d", n, got, want)
		}
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
	oracle := newLuaOracle(t)

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
			// 与 oracle 对齐。
			wantOracle := oracle.ExpectedRouteKey(tc.route)
			if got != wantOracle {
				t.Errorf("ExpectedRouteKey 与 oracle 不一致：got=%q oracle=%q", got, wantOracle)
			}
		})
	}
}

// math.floor 对齐：codec.lua 用 math.floor(route.cmd)，本实现应一致（截断小数）。
func TestExpectedRouteKey_FloorAlignment(t *testing.T) {
	ut := newSchemaCodecUT(t)
	oracle := newLuaOracle(t)
	// 非整数 float64 → math.floor 截断。
	route := map[string]any{"cmd": float64(99.7), "act": float64(3.9)}
	got := ut.ExpectedRouteKey(route)
	want := oracle.ExpectedRouteKey(route)
	if got != want {
		t.Errorf("math.floor 对齐失败：got=%q want(oracle)=%q", got, want)
	}
	if got != "99:3" {
		t.Errorf("ExpectedRouteKey(99.7,3.9) = %q, want 99:3", got)
	}
}

// ---------------------------------------------------------------------------
// bcc 语义断言：xor8(plaintext body[encOffset:])
// ---------------------------------------------------------------------------
// 注：codec.lua 经 lua_crypto.go:227 在加密**之前**对明文区域计算 bcc
// （computeBcc(data[offset:])，data 为加密前明文）。故 bcc = xor8(plaintext[encOffset:])。

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

// bcc 对拍：与 oracle 在头部 bcc 字节一致（已被 EncodeTCP_Parity 覆盖，这里单独显式断言）。
func TestBCC_ParityWithOracle(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUT(t)
	key := genKey()
	body := genBody(128)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	gotFrame := ut.EncodeTCP(route, body, key)
	wantFrame := oracle.EncodeTCP(route, body, key)
	if gotFrame[11] != wantFrame[11] {
		t.Errorf("bcc 字节不一致：got=%d want=%d", gotFrame[11], wantFrame[11])
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

// 辅助：把失败诊断信息打印成可读形式（调试时手动启用）。
func TestDebug_PrintOneFrame(t *testing.T) {
	if !debugEnabled() {
		t.Skip()
	}
	ut := newSchemaCodecUT(t)
	oracle := newLuaOracle(t)
	key := genKey()
	body := genBody(16)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	got := ut.EncodeTCP(route, body, key)
	want := oracle.EncodeTCP(route, body, key)
	fmt.Printf("GOT : %s\nWANT: %s\n", hexStr(got), hexStr(want))
	if !bytes.Equal(got, want) {
		t.Errorf("不一致")
	}
}

func debugEnabled() bool {
	return strings.HasPrefix(os.Getenv("CODEC_DEBUG"), "1")
}
