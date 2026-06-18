// Package codec_test — T1.5 decode 引擎对拍 + 失败语义 + Params/KeyLen 修复测试。
//
// 验收核心（与 T1.5 brief 逐条对应）：
//  1. decode 字节级对拍 conf/adapter/codec.lua 经旧 LuaAdapter.DecodeTCP/DecodeUDP。
//     覆盖矩阵：加密/压缩/cmd=0/空 body/UDP decode offset 0/headerErr 非零。
//  2. 失败语义：坏 gzip / 篡改 bcc / 缺 key 解密 → onError=fail 返回空 routeKey+不外泄 body；
//     onError=keep 保留原字节。
//  3. Params/KeyLen 修复：非默认值（rol=5、keyLen=16 用 aes_ecb）schema 的 encode/decode
//     读取了 schema 的 params/keyLen（已知向量断言 + 与 cipher 直调一致）。
//  4. wrapper 委托：adapter.NewSchemaAdapter 9 方法逐个委托 *codec.SchemaCodec。
package codec_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"stressbot/codec"
)

// ---------------------------------------------------------------------------
// TCP decode 对拍矩阵
// ---------------------------------------------------------------------------

func TestDecodeTCP_Parity_LuaAdapter(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUT(t)
	key := genKey()

	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	cases := []struct {
		name  string
		route map[string]any
		body  []byte
		key   []byte // nil = 不传 key（仅用于构造帧；decode 对拍统一传 key）
	}{
		{"small_encrypted", route, genBody(64), key},
		{"medium_encrypted", route, genBody(1024), key},
		// 低熵大 body：压缩 + 加密。
		{"large_compressible_encrypted", route, bytes.Repeat([]byte{0x41}, 4096), key},
		// 高熵大 body：尝试压缩但变大 → 不压缩、仅加密。
		{"large_incompressible_encrypted", route, genBody(4096), key},
		// cmd=0：不加密。
		{"cmd0_with_key", map[string]any{"cmd": float64(0), "act": float64(7)}, genBody(64), key},
		// 空 body。
		{"empty_body_encrypted", route, nil, key},
		// 单字节 body。
		{"one_byte_encrypted", route, []byte{0x42}, key},
		// route nil（cmd=act=0）。
		{"nil_route", nil, genBody(32), key},
		// act=0 cmd!=0：仍加密。
		{"act0_encrypted", map[string]any{"cmd": float64(50), "act": float64(0)}, genBody(100), key},
		// 不加密（无 key 构造）。
		{"small_no_key", route, genBody(64), nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 用 oracle 自己 encode 构造真值帧（保证 codec.lua encode/decode 自洽）。
			encKey := tc.key
			frame := oracle.EncodeTCP(tc.route, tc.body, encKey)
			if frame == nil {
				t.Fatalf("oracle encode 返回 nil")
			}
			// decode 对拍：双方都传 key（现网握手后 key 恒在）。
			decKey := key
			wantRoute, wantBody, wantErr := oracle.DecodeTCP(frame, decKey)
			gotRoute, gotBody, gotErr := ut.DecodeTCP(frame, decKey)
			if gotRoute != wantRoute {
				t.Errorf("routeKey 不一致: got=%q want=%q", gotRoute, wantRoute)
			}
			if !bytes.Equal(gotBody, wantBody) {
				t.Errorf("body 不一致\n name=%s\n got=%s\n want=%s",
					tc.name, hexStr(gotBody), hexStr(wantBody))
			}
			if gotErr != wantErr {
				t.Errorf("headerErr 不一致: got=%d want=%d", gotErr, wantErr)
			}
		})
	}
}

// 短帧：len < headerSize → 双方均返回 ("", nil, 0)。
func TestDecodeTCP_ShortFrame(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUT(t)
	key := genKey()

	shorts := [][]byte{
		nil,
		{},
		{0x01},
		make([]byte, 11), // 差 1 字节
	}
	for i, f := range shorts {
		or, ob, oe := oracle.DecodeTCP(f, key)
		gr, gb, ge := ut.DecodeTCP(f, key)
		if or != gr || !bytes.Equal(ob, gb) || oe != ge {
			t.Errorf("short[%d] 不一致: oracle=(%q,%v,%d) got=(%q,%v,%d)",
				i, or, ob, oe, gr, gb, ge)
		}
		if gr != "" || gb != nil || ge != 0 {
			t.Errorf("short[%d] 短帧应返回空三件套，got=(%q,%v,%d)", i, gr, gb, ge)
		}
	}
}

// 恰好 headerSize（无 body）：routeKey 仍按 cmd:act 拼，body 空。
func TestDecodeTCP_HeaderOnly(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUT(t)
	key := genKey()

	// 构造一个 cmd=5 act=9 flags=0 errorCode=0 bodyLen=0 的纯头帧。
	hdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hdr[0:], 0)
	hdr[6] = 5
	hdr[7] = 9
	hdr[10] = 0
	or, ob, oe := oracle.DecodeTCP(hdr, key)
	gr, gb, ge := ut.DecodeTCP(hdr, key)
	if gr != or || !bytes.Equal(gb, ob) || ge != oe {
		t.Errorf("HeaderOnly 不一致: oracle=(%q,%v,%d) got=(%q,%v,%d)", or, ob, oe, gr, gb, ge)
	}
	if gr != "5:9" {
		t.Errorf("HeaderOnly routeKey=%q want 5:9", gr)
	}
	if gb != nil && len(gb) != 0 {
		t.Errorf("HeaderOnly body 应为空，got len=%d", len(gb))
	}
}

// headerErr 非零：decode 透传，不阻断路由（与 codec.lua 一致）。
func TestDecodeTCP_HeaderErrNonZero(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUT(t)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := oracle.EncodeTCP(route, body, key)
	// 篡改 errorCode 字段（offset 4-5 le）为 0x0103 = 259。
	frame[4] = 0x03
	frame[5] = 0x01

	or, ob, oe := oracle.DecodeTCP(frame, key)
	gr, gb, ge := ut.DecodeTCP(frame, key)
	if ge != oe || ge != 259 {
		t.Errorf("headerErr 透传: oracle=%d got=%d (want 259)", oe, ge)
	}
	if gr != or || !bytes.Equal(gb, ob) {
		t.Errorf("headerErr 非零时 body/route 不一致: oracle=(%q,%v) got=(%q,%v)", or, ob, gr, gb)
	}
}

// ---------------------------------------------------------------------------
// UDP decode 对拍矩阵（encOffset=11 构造、decode offset 0）
// ---------------------------------------------------------------------------

func TestDecodeUDP_Parity_LuaAdapter(t *testing.T) {
	oracle := newLuaOracle(t)
	ut := newSchemaCodecUDP(t) // 默认 onError=fail 变体
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	cases := []struct {
		name string
		body []byte
	}{
		{"udp_small_encrypted_offset11", genBody(64)},
		{"udp_medium_encrypted_offset11", genBody(256)},
		// UDP decode 恒 offset 0：即便 encode 留了 11 明文前缀，decode 把整 body 全解。
		{"udp_body_shorter_than_offset", genBody(8)},
		{"udp_body_equals_offset", genBody(11)},
		{"udp_body_one_byte_after_offset", genBody(12)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := oracle.EncodeUDP(route, tc.body, key)
			if frame == nil {
				t.Fatalf("oracle encode 返回 nil")
			}
			wantRoute, wantBody, wantErr := oracle.DecodeUDP(frame, key)
			gotRoute, gotBody, gotErr := ut.DecodeUDP(frame, key)
			if gotRoute != wantRoute {
				t.Errorf("routeKey 不一致: got=%q want=%q", gotRoute, wantRoute)
			}
			if !bytes.Equal(gotBody, wantBody) {
				t.Errorf("body 不一致\n name=%s\n got=%s\n want=%s",
					tc.name, hexStr(gotBody), hexStr(wantBody))
			}
			if gotErr != wantErr {
				t.Errorf("headerErr 不一致: got=%d want=%d", gotErr, wantErr)
			}
		})
	}
}

// TestDecodeUDP_Parity_CompressibleEncrypted_Offset11 对拍 UDP 压缩+加密 这一
// T1.5 review 遗留缺口（carry-over a）。
//
// codec.lua 行为核对（conf/adapter/codec.lua:150-189）：
//   - encode_udp 用 offset 11（前 11 明文，body[12:] 加密）；
//   - decode_udp = decode_tcp，net_decrypt 用 offset 0 解密整 body——offset 不对称
//     导致前 11 字节被「再加密」（实际是 keystream 错位 XOR 出乱码），随后 pcall
//     zlib.decompress 乱码失败被吞 → 返回乱码 body（lenient，不阻断 routeKey）。
//
// 本 engine 默认 onError=fail 会严格返回空 routeKey（与 codec.lua 分歧）；改用 gz 步
// onError=keep 变体（newSchemaCodecUDPKeepGzip）后，解压失败保留原乱码字节、routeKey
// 正常——与 codec.lua 字节级一致。即「codec.lua 行为是 keep，用 keep 变体对拍」。
//
// 这不改变 engine 默认行为（生产 codec.json 仍 fail，更严）；仅证明 keep 变体可复刻
// codec.lua 的 lenient 行为，闭环该对拍缺口。
func TestDecodeUDP_Parity_CompressibleEncrypted_Offset11(t *testing.T) {
	oracle := newLuaOracle(t)
	utKeep := newSchemaCodecUDPKeepGzip(t) // gz onError=keep 变体
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}

	cases := []struct {
		name string
		body []byte
	}{
		// 低熵大 body：encode 阶段 gzip 压缩变小 → flags compressed+encrypted 都置位。
		{"udp_compressible_encrypted_offset11", bytes.Repeat([]byte{0x41}, 4096)},
		// 中等低熵 body（>=2048 阈值）。
		{"udp_compressible_encrypted_offset11_2k", bytes.Repeat([]byte{0x42}, 2048)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := oracle.EncodeUDP(route, tc.body, key)
			if frame == nil {
				t.Fatalf("oracle encode 返回 nil")
			}
			// 先确认 flags 同时置了 encrypted + compressed（否则不算覆盖该组合）。
			if frame[10]&0x01 == 0 || frame[10]&0x02 == 0 {
				t.Fatalf("oracle 帧未同时置 encrypted+compressed，flags=%d", frame[10])
			}
			wantRoute, wantBody, wantErr := oracle.DecodeUDP(frame, key)
			gotRoute, gotBody, gotErr := utKeep.DecodeUDP(frame, key)
			if gotRoute != wantRoute {
				t.Errorf("routeKey 不一致: got=%q want=%q", gotRoute, wantRoute)
			}
			if !bytes.Equal(gotBody, wantBody) {
				t.Errorf("body 不一致（keep 变体应字节级复刻 codec.lua lenient）\n name=%s\n got=%s\n want=%s",
					tc.name, hexStr(gotBody), hexStr(wantBody))
			}
			if gotErr != wantErr {
				t.Errorf("headerErr 不一致: got=%d want=%d", gotErr, wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 失败语义：onError=fail 返回空 routeKey+不外泄 body；keep 保留
// ---------------------------------------------------------------------------

// 坏 gzip：flags 标 compressed 但 body 不是合法 gzip 流。
// codec.lua 用 pcall 吞错 → body 保留原（乱码）字节。
// 我的 decode 按 brief：onError=fail → 空路由 + 不外泄 body。
func TestDecodeTCP_BadGzip_OnErrorFail(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()

	// 构造一个 flags=compressed、errorCode=0、cmd!=0 的帧，body 是非法 gzip。
	hdr := make([]byte, 12)
	body := []byte{0xDE, 0xAD, 0xBE, 0xEF} // 非 gzip magic
	binary.LittleEndian.PutUint32(hdr[0:], uint32(len(body)))
	hdr[6] = 7  // cmd
	hdr[7] = 1  // act
	hdr[10] = 2 // flags: compressed only（bit1）
	// bcc=0（未加密）
	frame := append(hdr, body...)

	route, gotBody, errCode := ut.DecodeTCP(frame, key)
	// enc 步 flag 未置位 → 不走 bcc 校验；gz 步 onError=fail → 解压失败 → 空路由。
	if route != "" {
		t.Errorf("bad gzip + onError=fail 应返回空 routeKey，got=%q", route)
	}
	if gotBody != nil {
		t.Errorf("bad gzip + onError=fail 不应外泄 body，got len=%d", len(gotBody))
	}
	// headerErr 仍透传（这里为 0）。
	if errCode != 0 {
		t.Errorf("headerErr 应透传为 0，got=%d", errCode)
	}
}

// 篡改 bcc：encode 正常加密置 bcc，然后改 bcc 字节。
// codec.lua decode 不校验 bcc → 返回正常解密 body。
// 我的 decode 按 brief：encrypt 步 produces bcc 在解密后校验，不一致 → onError=fail。
func TestDecodeTCP_TamperedBCC_OnErrorFail(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	// 篡改 bcc（offset 11）。
	origBcc := frame[11]
	frame[11] = origBcc ^ 0xFF

	gotRoute, gotBody, errCode := ut.DecodeTCP(frame, key)
	if gotRoute != "" {
		t.Errorf("篡改 bcc + onError=fail 应返回空 routeKey，got=%q", gotRoute)
	}
	if gotBody != nil {
		t.Errorf("篡改 bcc + onError=fail 不应外泄 body，got len=%d", len(gotBody))
	}
	_ = errCode // headerErr 透传
}

// 缺 key 解密：flags 标 encrypted 但 decode 未传 key。
// codec.lua decode 静默跳过解密（body 保留密文）—— 与我的 fail 分歧（Layer 1 决策 #2）。
// 这里仅断言我的行为：onError=fail → 空路由。
func TestDecodeTCP_MissingKey_OnErrorFail(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	// decode 不传 key。
	gotRoute, gotBody, _ := ut.DecodeTCP(frame, nil)
	if gotRoute != "" {
		t.Errorf("encrypted + 缺 key + onError=fail 应返回空 routeKey，got=%q", gotRoute)
	}
	if gotBody != nil {
		t.Errorf("encrypted + 缺 key + onError=fail 不应外泄 body，got len=%d", len(gotBody))
	}
}

// onError=keep：坏 gzip 保留原字节。
func TestDecodeTCP_BadGzip_OnErrorKeep(t *testing.T) {
	ut := newSchemaCodecKeepGzip(t)
	key := genKey()

	hdr := make([]byte, 12)
	body := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	binary.LittleEndian.PutUint32(hdr[0:], uint32(len(body)))
	hdr[6] = 7
	hdr[7] = 1
	hdr[10] = 2 // compressed
	frame := append(hdr, body...)

	route, gotBody, _ := ut.DecodeTCP(frame, key)
	// keep：routeKey 仍按 cmd:act 拼，body 保留原字节。
	if route != "7:1" {
		t.Errorf("onError=keep 应返回正常 routeKey 7:1，got=%q", route)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("onError=keep 应保留原 body，got=%v want=%v", gotBody, body)
	}
}

// onError=keep：篡改 bcc 仍解密成功并返回 body（bcc 校验失败但 keep）。
func TestDecodeTCP_TamperedBCC_OnErrorKeep(t *testing.T) {
	ut := newSchemaCodecKeepEnc(t)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	origBcc := frame[11]
	frame[11] = origBcc ^ 0xFF

	gotRoute, gotBody, _ := ut.DecodeTCP(frame, key)
	if gotRoute != "100:7" {
		t.Errorf("onError=keep 应返回正常 routeKey 100:7，got=%q", gotRoute)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("onError=keep 应解出原 body len=%d，got len=%d", len(body), len(gotBody))
	}
}

// ---------------------------------------------------------------------------
// Params/KeyLen 修复：非默认值 schema 证明读取了 schema 参数
// ---------------------------------------------------------------------------

// rol=5：构造 xor_carry_rol + params{rol:5} schema，encode 后用 rol=5 直调 cipher 解密能拿回原文，
// 证明 step.params 已透传（旧实现在 cipher 内默认 rol=3，会用错 rol）。
func TestParams_RolNonDefault(t *testing.T) {
	ut := newSchemaCodecRol(t, 5)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	// 取加密后 body（frame[12:]）。
	ciphered := frame[12:]

	// 用 rol=5 直调 cipher Decrypt（offset 0），应解回原 body。
	ciph, ok := codec.LookupCipher("xor_carry_rol")
	if !ok {
		t.Fatalf("xor_carry_rol 未注册")
	}
	dec, err := ciph.Decrypt(ciphered, key, 0, map[string]any{"rol": 5})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(dec, body) {
		t.Errorf("rol=5 encode 后用 rol=5 解密未还原：got=%v want=%v", dec, body)
	}

	// 用 rol=3（默认/旧值）解密会失败——证明 encode 用的是 rol=5。
	decWrong, _ := ciph.Decrypt(ciphered, key, 0, map[string]any{"rol": 3})
	if bytes.Equal(decWrong, body) {
		t.Errorf("rol=5 帧用 rol=3 不应解出原文（证明 step.params 生效）")
	}
}

// rol=5 的 decode 自洽：encode 后用自己的 decode 应还原 body。
func TestParams_RolNonDefault_DecodeRoundtrip(t *testing.T) {
	ut := newSchemaCodecRol(t, 5)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	routeKey, gotBody, errCode := ut.DecodeTCP(frame, key)
	if routeKey != "100:7" {
		t.Errorf("routeKey=%q want 100:7", routeKey)
	}
	if errCode != 0 {
		t.Errorf("headerErr=%d want 0", errCode)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("rol=5 decode 未还原 body：got=%v want=%v", gotBody, body)
	}
}

// keyLen=16 + aes_ecb：构造 aes_ecb schema + keyLen=16，证明 step.keyLen 生效
// （旧实现硬编码 ==32，会因 key 不达 32 而走"不加密"分支）。
func TestParams_KeyLen16_AesEcb(t *testing.T) {
	ut := newSchemaCodecAesEcb(t)
	// 16 字节 key。
	key := bytes.Repeat([]byte{0xAB}, 16)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	flags := frame[10]
	if flags&0x01 == 0 {
		t.Fatalf("keyLen=16 schema 应触发加密（flag bit0 置位），flags=%d", flags)
	}
	// decode 自洽。
	routeKey, gotBody, errCode := ut.DecodeTCP(frame, key)
	if routeKey != "100:7" {
		t.Errorf("routeKey=%q want 100:7", routeKey)
	}
	if errCode != 0 {
		t.Errorf("headerErr=%d want 0", errCode)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("aes_ecb keyLen=16 decode 未还原 body：got len=%d want len=%d", len(gotBody), len(body))
	}
}

// ---------------------------------------------------------------------------
// DescribeError 方法（委托 c.errorMap）
// ---------------------------------------------------------------------------

func TestSchemaCodec_DescribeError(t *testing.T) {
	s, err := codec.LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	em := map[uint64]string{
		0:  "成功",
		19: "消息解密失败",
	}
	c, err := codec.NewSchemaCodec(s, em)
	if err != nil {
		t.Fatalf("NewSchemaCodec: %v", err)
	}
	if got := c.DescribeError(0); got != "成功" {
		t.Errorf("DescribeError(0)=%q want 成功", got)
	}
	if got := c.DescribeError(19); got != "消息解密失败" {
		t.Errorf("DescribeError(19)=%q want 消息解密失败", got)
	}
	if got := c.DescribeError(9999); got != "" {
		t.Errorf("DescribeError(未知)=%q want 空串", got)
	}
	// nil errorMap → 永远空串。
	c2, _ := codec.NewSchemaCodec(s, nil)
	if got := c2.DescribeError(0); got != "" {
		t.Errorf("nil errorMap DescribeError(0)=%q want 空串", got)
	}
}

// ---------------------------------------------------------------------------
// decode 并发安全（-race）
// ---------------------------------------------------------------------------

func TestDecode_ConcurrentSafe(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)
	frame := ut.EncodeTCP(route, body, key)

	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				r, b, _ := ut.DecodeTCP(frame, key)
				_ = r
				_ = b
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
