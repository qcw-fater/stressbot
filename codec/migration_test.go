// Package codec_test — T1.6 迁移产物自验。
//
// 验收对象：conf/adapter/ 下三份生产 codec.json + 共享 errors.json。
// 不再使用 codec/testdata/ 下的 fixture（那是 T1.4/T1.5 的对拍 fixture），
// 而是直接 LoadSchema/NewSchemaCodec 生产位置的 conf/adapter/*_codec.json，
// 证明 T4 切换时实际加载的文件经引擎编译/验证通过。
//
// 关于 encOffset/decOffset 断言：compiledStep.encOffset/decOffset 为未导出字段，
// 本任务约束「不改 codec/ 或 adapter/ 代码」，故不为此加访问器。改为**行为级**断言——
// UDP encOffset=11 在 EncodeUDP 输出上必然表现为「body 前 11 字节明文保留」
// （见 engine_test.go TestEncodeUDP_PlaintextPrefix_Preserved 同款断言），decOffset=0
// 表现为 decode 能正确还原 UDP 加密帧。这比读字段值更强，直接证字节级行为。
// TCP 两份 encOffset=0/decOffset=0 表现为「整 body 加密 + decode 还原」。
package codec_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"stressbot/adapter"
	"stressbot/codec"
)

// findConfAdapterDir 从测试 CWD 向上回溯找 conf/adapter 目录。
func findConfAdapterDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
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
// 重要语义说明（已逐行核对 codec.lua:189 `decode_udp = decode_tcp`）：
// UDP 的 encode/decode **故意非对称**——encode 用 offset 11（前 11 明文供服务端
// 查密钥表），decode 恒用 offset 0（codec.lua decode_tcp 的 net_decrypt 偏移）。
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

// TestMigration_ErrorMap_Coverage 验证 errors.json 覆盖 error.lua 的全部 code→desc 对。
//
// 不使用 LuaAdapter.DescribeError 作 oracle：该路径在未初始化 zap logger 的测试环境
// 下会触发 nil panic（utils/log 包的 logger 为 nil，callDescribeError 失败时调
// stresslog.Error），与 errors.json 迁移正确性无关，属 oracle 自身限制。改为：
//  1. 结构校验：所有 key 为合法数字、所有 value 非空（不漏描述）；
//  2. 计数对齐：errors.json 条目数 == error.lua 中 `[N] =` 形式条目数（不漏不重，
//     LoadErrorMap 已保证 key 唯一，Go map 重复 key 覆盖即重会被计数差异暴露）；
//  3. 抽样核对：curated 一批跨区段 code→desc，与 error.lua 真值逐字一致。
func TestMigration_ErrorMap_Coverage(t *testing.T) {
	dir := findConfAdapterDir(t)
	em, err := codec.LoadErrorMap(filepath.Join(dir, "errors.json"))
	if err != nil {
		t.Fatalf("LoadErrorMap 失败: %v", err)
	}

	// 1. 结构校验。
	for code, desc := range em {
		if desc == "" {
			t.Errorf("code=%d 描述为空", code)
		}
	}

	// 2. 计数对齐：grep error.lua 中 `[N] =` 条目数。
	errorLuaBytes, err := os.ReadFile(filepath.Join(dir, "error.lua"))
	if err != nil {
		t.Fatalf("读 error.lua 失败: %v", err)
	}
	luaEntryRe := regexp.MustCompile(`(?m)^\s*\[\d+\]\s*=`)
	luaCount := len(luaEntryRe.FindAll(errorLuaBytes, -1))
	if len(em) != luaCount {
		t.Fatalf("errors.json 条目数=%d 与 error.lua 条目数=%d 不一致（漏或重）",
			len(em), luaCount)
	}

	// 3. 抽样核对（跨区段：登录/战队/充值/搜打撤/快捷消息 等；< 100 的服务端系统码
	//    不作为业务结果返回，已从 errors.json/error.lua 清理，故不在此断言）。
	curated := map[uint64]string{
		256:  "区服没有找到",
		700:  "已申请过当前战队，不可重复申请",
		707:  "您当前是会长，需传位后才能离开战队",
		1080: "订单内数据未找到data数据",
		1800: "搜打撤模式未开放",
		2016: "快捷消息重复装备",
	}
	for code, want := range curated {
		got, ok := em[code]
		if !ok {
			t.Errorf("抽样 code=%d 在 errors.json 中缺失", code)
			continue
		}
		if got != want {
			t.Errorf("抽样 code=%d 描述不一致：got=%q want=%q", code, got, want)
		}
	}
}

// TestMigration_TCPLogic_ParityWithLuaAdapter 对生产 tcp_logic_codec.json
// 跑一次 encode 字节级对拍（T1.4 的 fixture 已对拍过，这里证明**生产文件**同样字节一致）。
func TestMigration_TCPLogic_ParityWithLuaAdapter(t *testing.T) {
	dir := findConfAdapterDir(t)
	ut, _ := loadProdCodec(t, "tcp_logic_codec.json")

	codecLua := filepath.Join(dir, "codec.lua")
	errorLua := filepath.Join(dir, "error.lua")
	oracle, err := adapter.NewLuaAdapter(2, codecLua, errorLua)
	if err != nil {
		t.Fatalf("NewLuaAdapter 失败: %v", err)
	}
	t.Cleanup(oracle.Close)

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 1)
	}

	cases := []struct {
		name  string
		route map[string]any
		body  []byte
	}{
		{"encrypted_small", map[string]any{"cmd": float64(100), "act": float64(7)}, genBodyMig(64)},
		{"encrypted_medium", map[string]any{"cmd": float64(100), "act": float64(7)}, genBodyMig(1024)},
		{"encrypted_compressible",
			map[string]any{"cmd": float64(100), "act": float64(7)}, bytes.Repeat([]byte{0x41}, 4096)},
		{"no_key", map[string]any{"cmd": float64(100), "act": float64(7)}, genBodyMig(64)},
		{"cmd0_with_key", map[string]any{"cmd": float64(0), "act": float64(7)}, genBodyMig(64)},
		{"empty_body", map[string]any{"cmd": float64(100), "act": float64(7)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := key
			if tc.name == "no_key" {
				k = nil
			}
			want := oracle.EncodeTCP(tc.route, tc.body, k)
			got := ut.EncodeTCP(tc.route, tc.body, k)
			if !bytes.Equal(got, want) {
				t.Fatalf("TCP 对拍失败 name=%s\n got=%x\n want=%x", tc.name, got, want)
			}
		})
	}
}

// genBodyMig 生成长度 n 的可复现伪随机 body（xorshift32）。
func genBodyMig(n int) []byte {
	b := make([]byte, n)
	state := uint32(0x12345678)
	for i := range b {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		b[i] = byte(state)
	}
	return b
}

// TestMigration_ErrorMap_FullVerbatimVsErrorLua 对 errors.json 全部 639 对 code→desc
// 与 error.lua 真值做 verbatim 比对（T1.7 carry-over c，闭环 T1.6 仅抽样的缺口）。
//
// 不依赖 LuaAdapter（其 DescribeError 路径在未初始化 zap logger 时会 nil panic，与迁移
// 正确性无关）；改用纯文本解析 error.lua 的 `errors = {...}` 表——按行匹配
// `^\s*\[(\d+)\]\s*=\s*"(.*)"\s*,?$`，提取 (code, desc) 对。error.lua 已确认全部 639
// 条目均符合该格式（无转义引号、无多行字符串），解析无歧义。
//
// 断言：
//  1. 双方条目数相等（639），不漏不重（LoadErrorMap 已保证 key 唯一）；
//  2. 每对 code→desc 在 errors.json 与 error.lua 间 verbatim 一致（string==）。
func TestMigration_ErrorMap_FullVerbatimVsErrorLua(t *testing.T) {
	dir := findConfAdapterDir(t)

	// 1. 加载 errors.json。
	em, err := codec.LoadErrorMap(filepath.Join(dir, "errors.json"))
	if err != nil {
		t.Fatalf("LoadErrorMap 失败: %v", err)
	}

	// 2. 纯文本解析 error.lua 的 errors 表。
	luaBytes, err := os.ReadFile(filepath.Join(dir, "error.lua"))
	if err != nil {
		t.Fatalf("读 error.lua 失败: %v", err)
	}
	// 按行匹配 `[code] = "desc"`。error.lua 全部条目均符合此格式（已人工核对）。
	// desc 不含转义引号，正则贪婪到行尾引号即可。
	entryRe := regexp.MustCompile(`(?m)^\s*\[(\d+)\]\s*=\s*"(.*)"\s*,?\s*$`)
	matches := entryRe.FindAllSubmatch(luaBytes, -1)
	if len(matches) == 0 {
		t.Fatalf("error.lua 未解析出任何条目（正则失配？）")
	}
	lua := make(map[uint64]string, len(matches))
	for _, m := range matches {
		code, parseErr := strconv.ParseUint(string(m[1]), 10, 64)
		if parseErr != nil {
			t.Fatalf("error.lua code %q 解析失败: %v", string(m[1]), parseErr)
		}
		lua[code] = string(m[2])
	}

	// 3. 条目数对齐。
	if len(em) != len(lua) {
		t.Fatalf("条目数不一致：errors.json=%d error.lua=%d", len(em), len(lua))
	}
	if len(em) != 639 {
		t.Errorf("条目数=%d，期望 639（若 error.lua 故意增减，请同步更新此断言）", len(em))
	}

	// 4. 逐对 verbatim 比对。
	var mismatch, missingInJSON, missingInLua int
	for code, wantDesc := range lua {
		gotDesc, ok := em[code]
		if !ok {
			missingInJSON++
			t.Errorf("code=%d 在 errors.json 中缺失（error.lua 有）", code)
			continue
		}
		if gotDesc != wantDesc {
			mismatch++
			t.Errorf("code=%d 描述不一致\n errors.json=%q\n error.lua =%q", code, gotDesc, wantDesc)
		}
	}
	for code := range em {
		if _, ok := lua[code]; !ok {
			missingInLua++
			t.Errorf("code=%d 在 error.lua 中缺失（errors.json 多出）", code)
		}
	}
	if mismatch != 0 || missingInJSON != 0 || missingInLua != 0 {
		t.Fatalf("全量比对失败：mismatch=%d missingInJSON=%d missingInLua=%d",
			mismatch, missingInJSON, missingInLua)
	}
}
