// Package codec_test — decode 引擎行为测试：短帧/纯头/headerErr 透传、失败语义、
// Params/KeyLen 修复、wrapper 委托、并发安全。
package codec_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"stressbot/protocol/codec"
)

// ---------------------------------------------------------------------------
// 短帧 / 纯头 / headerErr 透传
// ---------------------------------------------------------------------------

// 短帧：len < headerSize → 返回 ("", nil, 0)。
func TestDecodeTCP_ShortFrame(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()

	shorts := [][]byte{
		nil,
		{},
		{0x01},
		make([]byte, 11), // 差 1 字节
	}
	for i, f := range shorts {
		r, b, e := ut.DecodeTCP(f, key)
		if r != "" || b != nil || e != 0 {
			t.Errorf("short[%d] 短帧应返回空三件套，got=(%q,%v,%d)", i, r, b, e)
		}
	}
}

// 恰好 headerSize（无 body）：routeKey 仍按 cmd:act 拼，body 空。
func TestDecodeTCP_HeaderOnly(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()

	// 构造一个 cmd=5 act=9 flags=0 errorCode=0 bodyLen=0 的纯头帧。
	hdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hdr[0:], 0)
	hdr[6] = 5
	hdr[7] = 9
	hdr[10] = 0
	r, b, e := ut.DecodeTCP(hdr, key)
	if r != "5:9" {
		t.Errorf("HeaderOnly routeKey=%q want 5:9", r)
	}
	if len(b) != 0 {
		t.Errorf("HeaderOnly body 应为空，got len=%d", len(b))
	}
	if e != 0 {
		t.Errorf("HeaderOnly headerErr=%d want 0", e)
	}
}

// headerErr 非零：decode 透传 headerErr，不阻断路由解析。
func TestDecodeTCP_HeaderErrNonZero(t *testing.T) {
	ut := newSchemaCodecUT(t)
	key := genKey()
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	body := genBody(64)

	frame := ut.EncodeTCP(route, body, key)
	// 篡改 errorCode 字段（offset 4-5 le）为 0x0103 = 259。
	frame[4] = 0x03
	frame[5] = 0x01

	r, b, e := ut.DecodeTCP(frame, key)
	if e != 259 {
		t.Errorf("headerErr 透传: got=%d want 259", e)
	}
	if r != "100:7" {
		t.Errorf("headerErr 非零时 routeKey=%q want 100:7", r)
	}
	if !bytes.Equal(b, body) {
		t.Errorf("headerErr 非零时 body 应正常还原，got len=%d want len=%d", len(b), len(body))
	}
}

// ---------------------------------------------------------------------------
// 失败语义：onError=fail 返回空 routeKey+不外泄 body；keep 保留
// ---------------------------------------------------------------------------

// 坏 gzip：flags 标 compressed 但 body 不是合法 gzip 流。
// engine decode：gz 步解压失败 + onError=fail → 空路由 + 不外泄 body。
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
	frame := append(append([]byte(nil), hdr...), body...)

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
// engine decode：encrypt 步 produces bcc 在解密后校验，不一致 → onError=fail。
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
// engine decode：encrypted + 缺 key + onError=fail → 空路由。
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
	frame := append(append([]byte(nil), hdr...), body...)

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
// Params/KeyLen：非默认值 schema 证明读取了 schema 参数
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
	for range 8 {
		go func() {
			for range 50 {
				r, b, _ := ut.DecodeTCP(frame, key)
				_ = r
				_ = b
			}
			done <- struct{}{}
		}()
	}
	for range 8 {
		<-done
	}
}
