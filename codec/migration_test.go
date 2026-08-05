// Package codec_test 校验声明式 codec 生产产物。
//
// 验收对象：conf/adapter/ 下三份生产 *_codec.json + 共享 errors.json。
// 直接 LoadSchema/NewSchemaCodec 生产位置的 conf/adapter/*_codec.json，证明实际加载的
// 文件经引擎编译/验证通过。
//
// 关于 encOffset/decOffset 断言：compiledStep.encOffset/decOffset 为未导出字段，改为
// **行为级**断言——UDP encOffset=11 在 EncodeUDP 输出上必然表现为「body 前 11 字节明文
// 保留」，decOffset=0 表现为 decode 能正确还原 UDP 加密帧。TCP 两份 encOffset=0/decOffset=0
// 表现为「整 body 加密 + decode 还原」。
package codec_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"stressbot/codec"
)

// findConfAdapterDir 从测试 CWD 向上回溯找 conf/adapter 目录。
func findConfAdapterDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "conf", "adapter", "tcp_logic_codec.json")); err == nil {
			return filepath.Join(dir, "conf", "adapter")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("未找到含 conf/adapter/tcp_logic_codec.json 的仓库根目录")
	return ""
}

// loadProdCodec 加载生产 codec.json + 共享 errors.json，返回编译产物与 errorMap。
func loadProdCodec(t *testing.T, name string) (*codec.SchemaCodec, map[uint64]string) {
	t.Helper()
	dir := findConfAdapterDir(t)
	schemaPath := filepath.Join(dir, name)
	errorsPath := filepath.Join(dir, "errors.json")

	s, err := codec.LoadSchema(schemaPath)
	if err != nil {
		t.Fatalf("LoadSchema(%q) 失败: %v", name, err)
	}
	em, err := codec.LoadErrorMap(errorsPath)
	if err != nil {
		t.Fatalf("LoadErrorMap(%q) 失败: %v", errorsPath, err)
	}
	c, err := codec.NewSchemaCodec(s, em)
	if err != nil {
		t.Fatalf("NewSchemaCodec(%q) 失败: %v", name, err)
	}
	return c, em
}

// TestMigration_AllCodecsCompile 验证 3 份生产 codec.json 经
// LoadSchema + Validate + NewSchemaCodec 全部成功（含算法注册表命中）。
func TestMigration_AllCodecsCompile(t *testing.T) {
	dir := findConfAdapterDir(t)
	// 先验 errors.json 可加载（共享）。
	em, err := codec.LoadErrorMap(filepath.Join(dir, "errors.json"))
	if err != nil {
		t.Fatalf("LoadErrorMap(errors.json) 失败: %v", err)
	}
	if len(em) == 0 {
		t.Fatalf("errors.json 为空")
	}

	for _, name := range []string{
		"tcp_logic_codec.json",
		"tcp_battle_codec.json",
		"udp_battle_codec.json",
	} {
		t.Run(name, func(t *testing.T) {
			s, err := codec.LoadSchema(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("LoadSchema 失败: %v", err)
			}
			if _, err := codec.NewSchemaCodec(s, em); err != nil {
				t.Fatalf("NewSchemaCodec 失败: %v", err)
			}
		})
	}
}

// TestMigration_TCP_OffsetZero_TCPLogicAndBattleIdentical 验证 TCP 两份
// encOffset=0/decOffset=0 的行为一致：
//   - 同 route/body/key 下 EncodeTCP 字节完全相同（两份 codec 内容本应一致）；
//   - TCP 加密时整 body 均被加密（offset 0 → 无明文前缀）。
func TestMigration_TCP_OffsetZero_TCPLogicAndBattleIdentical(t *testing.T) {
	logic, _ := loadProdCodec(t, "tcp_logic_codec.json")
	battle, _ := loadProdCodec(t, "tcp_battle_codec.json")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 1)
	}
	body := make([]byte, 64)
	for i := range body {
		body[i] = byte(i)
	}
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	logicFrame := logic.EncodeTCP(route, body, key)
	battleFrame := battle.EncodeTCP(route, body, key)
	if !bytes.Equal(logicFrame, battleFrame) {
		t.Fatalf("tcp_logic 与 tcp_battle 编码不一致（应内容相同）\n logic=%x\n battle=%x",
			logicFrame, battleFrame)
	}

	// encOffset=0 → 整 body 加密（ciphered != plain）。
	ciphered := logicFrame[12:]
	if bytes.Equal(ciphered, body) {
		t.Errorf("TCP encOffset=0：ciphered == plain，疑似未加密")
	}

	// decOffset=0 → decode 能还原。
	rk, decBody, herr := logic.DecodeTCP(logicFrame, key)
	if rk != "100:7" {
		t.Errorf("decode routeKey=%q want 100:7", rk)
	}
	if herr != 0 {
		t.Errorf("decode headerErr=%d want 0", herr)
	}
	if !bytes.Equal(decBody, body) {
		t.Errorf("decode body 还原失败")
	}
}

// TestMigration_UDP_EncOffset11_DecOffset0 验证 udp_battle_codec.json 的
// encOffset=11/decOffset=0 行为。
//
// 重要语义说明：UDP 的 encode/decode **故意非对称**——encode 用 offset 11（前 11 明文供
// 服务端查密钥表），decode 恒用 offset 0。配置见 udp_battle_codec.json 的 encrypt.offset。
// 这意味着**客户端用 UDP encode 出去的帧，客户端自己用 decode 是无法还原的**
// （流密码 keystream 位置不对齐），服务端用其专属 decode 路径处理。故 UDP 不做
// encode→decode 自环；只验证 encode 行为（encOffset=11）+ decode 对 offset-0
// 加密帧（即服务端回包形态）能正确还原。
func TestMigration_UDP_EncOffset11_DecOffset0(t *testing.T) {
	udp, _ := loadProdCodec(t, "udp_battle_codec.json")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 1)
	}
	body := make([]byte, 64)
	for i := range body {
		body[i] = byte(i)
	}
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	// ---- encOffset=11：encode 行为 ----
	frame := udp.EncodeUDP(route, body, key)
	if len(frame) != 12+len(body) {
		t.Fatalf("UDP frame len=%d want %d", len(frame), 12+len(body))
	}
	ciphered := frame[12:]
	// 前 11 字节明文必须保留。
	if !bytes.Equal(ciphered[:11], body[:11]) {
		t.Errorf("UDP encOffset=11：前 11 字节明文未保留\n got=%v\n want=%v",
			ciphered[:11], body[:11])
	}
	// 第 12 字节起应被加密（与明文不同）。
	if ciphered[11] == body[11] {
		t.Errorf("UDP encOffset=11：第 12 字节疑似未加密")
	}
	// bcc（header offset 11）= xor8(plaintext[11:])，排除前 11 明文字节。
	var wantBcc byte
	for _, b := range body[11:] {
		wantBcc ^= b
	}
	if frame[11] != wantBcc {
		t.Errorf("UDP bcc=%d, want xor8(plaintext[11:])=%d", frame[11], wantBcc)
	}

	// ---- decOffset=0：decode 对 offset-0 加密帧（服务端回包形态）能还原 ----
	// 用 tcp_logic codec（encOffset=0）编码一个「服务端风格」offset-0 加密帧，
	// 交 udp codec（decOffset=0）解码——decOffset 相同故应正确还原。
	tcp, _ := loadProdCodec(t, "tcp_logic_codec.json")
	serverLikeFrame := tcp.EncodeTCP(route, body, key) // offset 0 加密
	rk, decBody, herr := udp.DecodeUDP(serverLikeFrame, key)
	if rk != "100:7" {
		t.Errorf("UDP decode routeKey=%q want 100:7", rk)
	}
	if herr != 0 {
		t.Errorf("UDP decode headerErr=%d want 0", herr)
	}
	if !bytes.Equal(decBody, body) {
		t.Errorf("UDP decode（decOffset=0）还原 offset-0 加密帧失败\n got=%v\n want=%v",
			decBody, body)
	}
}
