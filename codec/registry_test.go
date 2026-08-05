// Package codec — 算法注册表与算法实现测试。
//
// 测试策略（TDD）：
//   - 每个 cipher 覆盖 Encrypt→Decrypt 往返（含 offset>0，断言前缀字节不变 + len(out)==len(data)
//     对流密码；块密码验证 len 约束与去填充后等价）；
//   - xor_carry_rol 已知向量往返（TestXorCarryRolEquivalence 的手动计算值）；
//   - xor8 已知向量 (0x55,0xAA)=0xFF；
//   - crc16("123456789")=0x29B1、crc32("123456789")=0xCBF43926 已知向量；
//   - md5(""/"abc")、sha1("abc")、sha256("") 已知向量 + HMAC 输出长度；
//   - gzip 往返；
//   - Algorithms() 完整性 + 顺序稳定性。
package codec

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash/crc32"
	"testing"
)

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// mustEncDec 对 cipher 做 Encrypt→Decrypt 往返，断言恢复原文（流密码 + offset 通用版）。
func cipherRoundTrip(t *testing.T, name string, c Cipher, data, key []byte, offset int, params map[string]any) {
	t.Helper()
	enc, err := c.Encrypt(data, key, offset, params)
	if err != nil {
		t.Fatalf("%s: Encrypt 失败: %v", name, err)
	}
	// 前缀明文字节必须不变。
	if offset > 0 && offset <= len(data) && len(enc) >= offset {
		if !bytes.Equal(enc[:offset], data[:offset]) {
			t.Errorf("%s: offset=%d 前缀被改动\n  enc前缀: %x\n  原前缀:   %x", name, offset, enc[:offset], data[:offset])
		}
	}
	dec, err := c.Decrypt(enc, key, offset, params)
	if err != nil {
		t.Fatalf("%s: Decrypt 失败: %v", name, err)
	}
	if !bytes.Equal(dec, data) {
		t.Errorf("%s: offset=%d 往返不恢复\n  原: %x\n  解: %x", name, offset, data, dec)
	}
}

// randBytes 用伪随机构造确定性数据（避免 crypto/rand 让测试不稳定）。
func randBytes(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + int(seed)*7)
	}
	return b
}

// key32 构造 32 字节确定性密钥（用于 xor_carry_rol，要求 32 字节）。
func key32(seed byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7) ^ seed
	}
	return k
}

// ---------------------------------------------------------------------------
// Algorithms() 完整性与顺序
// ---------------------------------------------------------------------------

func TestAlgorithmsCompleteness(t *testing.T) {
	all := Algorithms()

	want := map[string]string{
		// key "op.name" → op（值冗余但便于断言；name 已在 key 里）。
		"cipher.none": "cipher", "cipher.xor": "cipher", "cipher.xor_carry_rol": "cipher",
		"cipher.rc4": "cipher", "cipher.aes_ecb": "cipher", "cipher.aes_cbc": "cipher",
		"cipher.aes_ctr": "cipher", "cipher.xxtea": "cipher",
		"compress.none": "compress", "compress.gzip": "compress",
		"checksum.none": "checksum", "checksum.xor8": "checksum", "checksum.sum8": "checksum",
		"checksum.crc16": "checksum", "checksum.crc32": "checksum", "checksum.crc32c": "checksum",
		"hash.md5": "hash", "hash.sha1": "hash", "hash.sha256": "hash",
	}
	got := make(map[string]string, len(all))
	for _, m := range all {
		got[m.Op+"."+m.Name] = m.Op
	}
	for k, wop := range want {
		gop, ok := got[k]
		if !ok {
			t.Errorf("Algorithms() 缺少 %s", k)
			continue
		}
		if gop != wop {
			t.Errorf("Algorithms() %s 的 op 错误: got %q want %q", k, gop, wop)
		}
	}
	if len(got) != len(want) {
		t.Errorf("Algorithms() 数量不匹配: got %d want %d", len(got), len(want))
	}
}

func TestAlgorithmsOrderStable(t *testing.T) {
	// 调用两次，顺序必须稳定且按 op 分组、组内按 name 字母序。
	a := Algorithms()
	b := Algorithms()
	if len(a) != len(b) {
		t.Fatalf("两次 Algorithms() 长度不同: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Op != b[i].Op {
			t.Fatalf("Algorithms() 顺序不稳定 @%d: %s/%s vs %s/%s", i, a[i].Op, a[i].Name, b[i].Op, b[i].Name)
		}
	}
	// 验证 op 分组顺序：cipher → compress → checksum → hash。
	opOrder := map[string]int{"cipher": 0, "compress": 1, "checksum": 2, "hash": 3}
	lastGroup := -1
	lastName := ""
	for _, m := range a {
		rank := opOrder[m.Op]
		if rank < lastGroup {
			t.Errorf("op 分组顺序错误: %q 出现在更早的组之后 (rank=%d last=%d)", m.Op, rank, lastGroup)
		}
		if rank == lastGroup {
			// 同组内名字必须字母序非降。
			if m.Name < lastName {
				t.Errorf("组 %q 内 name 字母序错: %q 在 %q 之后", m.Op, lastName, m.Name)
			}
		}
		lastGroup = rank
		lastName = m.Name
	}
}

// ---------------------------------------------------------------------------
// Lookup*
// ---------------------------------------------------------------------------

func TestLookupMissing(t *testing.T) {
	if _, ok := LookupCipher("does-not-exist"); ok {
		t.Error("LookupCipher 对未知名应返回 false")
	}
	if _, ok := LookupCompressor("does-not-exist"); ok {
		t.Error("LookupCompressor 对未知名应返回 false")
	}
	if _, ok := LookupChecksum("does-not-exist"); ok {
		t.Error("LookupChecksum 对未知名应返回 false")
	}
	if _, ok := LookupHasher("does-not-exist"); ok {
		t.Error("LookupHasher 对未知名应返回 false")
	}
}

func TestLookupPresent(t *testing.T) {
	if _, ok := LookupCipher("xor_carry_rol"); !ok {
		t.Error("LookupCipher(xor_carry_rol) 应命中")
	}
	if _, ok := LookupCompressor("gzip"); !ok {
		t.Error("LookupCompressor(gzip) 应命中")
	}
	if _, ok := LookupChecksum("xor8"); !ok {
		t.Error("LookupChecksum(xor8) 应命中")
	}
	if _, ok := LookupHasher("sha256"); !ok {
		t.Error("LookupHasher(sha256) 应命中")
	}
}

// ---------------------------------------------------------------------------
// cipher: none / xor / xor_carry_rol / rc4
// ---------------------------------------------------------------------------

func TestNoneCipherPassThrough(t *testing.T) {
	c, _ := LookupCipher("none")
	data := randBytes(64, 1)
	enc, err := c.Encrypt(data, nil, 11, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, data) {
		t.Error("none cipher 应原样返回")
	}
	if len(enc) != len(data) {
		t.Errorf("none: len(out)=%d want %d", len(enc), len(data))
	}
}

func TestXorCipherRoundTrip(t *testing.T) {
	c, _ := LookupCipher("xor")
	data := randBytes(128, 2)
	key := []byte("short-key")
	for _, off := range []int{0, 1, 11, 64} {
		cipherRoundTrip(t, "xor", c, data, key, off, nil)
	}
}

func TestXorCarryRolRoundTrip(t *testing.T) {
	c, _ := LookupCipher("xor_carry_rol")
	data := randBytes(256, 3)
	key := key32('K')
	for _, off := range []int{0, 1, 11, 100} {
		cipherRoundTrip(t, "xor_carry_rol", c, data, key, off, nil)
	}
}

// 手动向量化值，对照本文件 TestXorCarryRolEquivalence。
func TestXorCarryRolKnownVector(t *testing.T) {
	c, _ := LookupCipher("xor_carry_rol")
	data := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}
	// 手动计算期望（rol=3 的 XOR+carry+ROL8 流加密）。
	expected := make([]byte, len(data))
	carry := byte(0)
	for i := range data {
		plain := data[i]
		mask := key[i%32]
		x := plain ^ mask
		x += carry
		x = (x << 3) | (x >> 5) // ROL8(x, 3)
		expected[i] = x
		carry = x
	}
	enc, err := c.Encrypt(data, key, 0, map[string]any{"rol": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, expected) {
		t.Errorf("xor_carry_rol 与已知向量不匹配:\n  got:      %x\n  expected: %x", enc, expected)
	}
}

// xor_carry_rol 默认 rol=3（params 缺省）。
func TestXorCarryRolDefaultRol(t *testing.T) {
	c, _ := LookupCipher("xor_carry_rol")
	data := randBytes(64, 4)
	key := key32('D')
	withDefault, err := c.Encrypt(data, key, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	withExplicit, err := c.Encrypt(data, key, 0, map[string]any{"rol": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withDefault, withExplicit) {
		t.Error("xor_carry_rol 默认 rol 应为 3")
	}
}

// xor_carry_rol 非法 key（非 32 字节）返回原数据。
func TestXorCarryRolInvalidKey(t *testing.T) {
	c, _ := LookupCipher("xor_carry_rol")
	data := []byte{1, 2, 3, 4}
	shortKey := make([]byte, 16)
	enc, err := c.Encrypt(data, shortKey, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, data) {
		t.Errorf("非法 key 应返回原数据，got %v", enc)
	}
}

// xor_carry_rol offset=11 时前缀明文不变。
func TestXorCarryRolOffsetPrefix(t *testing.T) {
	c, _ := LookupCipher("xor_carry_rol")
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}
	key := key32('O')
	off := 11
	enc, err := c.Encrypt(data, key, off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc[:off], data[:off]) {
		t.Error("offset=11 前缀应保持明文")
	}
	if len(enc) != len(data) {
		t.Errorf("len(out)=%d want %d", len(enc), len(data))
	}
	// 后半段必须已变化（极小概率全等，但 64 字节下几乎不可能）。
	if bytes.Equal(enc[off:], data[off:]) {
		t.Error("offset 后段未加密")
	}
}

func TestRc4RoundTrip(t *testing.T) {
	c, _ := LookupCipher("rc4")
	data := randBytes(256, 5)
	key := []byte("testkey1234567890")
	for _, off := range []int{0, 1, 11, 100} {
		cipherRoundTrip(t, "rc4", c, data, key, off, nil)
	}
}

// rc4 key 长度 1/16/256 均可往返。
func TestRc4KeyLengths(t *testing.T) {
	c, _ := LookupCipher("rc4")
	data := randBytes(64, 6)
	for _, kl := range []int{1, 16, 256} {
		key := make([]byte, kl)
		for i := range key {
			key[i] = byte(i + kl)
		}
		cipherRoundTrip(t, "rc4", c, data, key, 0, nil)
	}
}

// ---------------------------------------------------------------------------
// cipher: AES-ECB / CBC / CTR
// ---------------------------------------------------------------------------

func TestAesEcbRoundTrip(t *testing.T) {
	c, _ := LookupCipher("aes_ecb")
	data := []byte("hello world, this is AES test data!!") // 35 字节
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	enc, err := c.Encrypt(data, key, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// PKCS#7 填充到 48 字节。
	if len(enc) != 48 {
		t.Errorf("aes_ecb 密文长度=%d want 48", len(enc))
	}
	dec, err := c.Decrypt(enc, key, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, data) {
		t.Error("aes_ecb 往返失败")
	}
}

func TestAesCbcRoundTrip(t *testing.T) {
	c, _ := LookupCipher("aes_cbc")
	data := []byte("hello world, this is AES-CBC test!!") // 34 字节
	key := make([]byte, 32)                               // AES-256
	iv := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	for i := range iv {
		iv[i] = byte(i + 1)
	}
	params := map[string]any{"iv": iv}
	enc, err := c.Encrypt(data, key, 0, params)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := c.Decrypt(enc, key, 0, params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, data) {
		t.Error("aes_cbc 往返失败")
	}
}

func TestAesCbcMissingIV(t *testing.T) {
	c, _ := LookupCipher("aes_cbc")
	_, err := c.Encrypt([]byte("data"), make([]byte, 16), 0, nil)
	if err == nil {
		t.Error("aes_cbc 缺 iv 应报错")
	}
}

func TestAesCtrRoundTrip(t *testing.T) {
	c, _ := LookupCipher("aes_ctr")
	data := randBytes(100, 7) // 非块对齐长度，CTR 仍流式工作
	key := make([]byte, 16)
	iv := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	for i := range iv {
		iv[i] = byte(i + 2)
	}
	params := map[string]any{"iv": iv}
	enc, err := c.Encrypt(data, key, 0, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) != len(data) {
		t.Errorf("aes_ctr 流模式: len(out)=%d want %d", len(enc), len(data))
	}
	dec, err := c.Decrypt(enc, key, 0, params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, data) {
		t.Error("aes_ctr 往返失败")
	}
}

// aes_ctr offset 前缀明文保留 + 流密码 len(out)==len(data)。
func TestAesCtrOffset(t *testing.T) {
	c, _ := LookupCipher("aes_ctr")
	data := randBytes(50, 8)
	key := make([]byte, 16)
	iv := make([]byte, 16)
	params := map[string]any{"iv": iv}
	off := 7
	enc, err := c.Encrypt(data, key, off, params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc[:off], data[:off]) {
		t.Error("aes_ctr offset 前缀应保留明文")
	}
	if len(enc) != len(data) {
		t.Errorf("len(out)=%d want %d", len(enc), len(data))
	}
	dec, _ := c.Decrypt(enc, key, off, params)
	if !bytes.Equal(dec, data) {
		t.Error("aes_ctr offset 往返失败")
	}
}

// ---------------------------------------------------------------------------
// cipher: xxtea
// ---------------------------------------------------------------------------

func TestXxteaRoundTrip(t *testing.T) {
	c, _ := LookupCipher("xxtea")
	data := randBytes(32, 9)
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i + 1)
	}
	enc, err := c.Encrypt(data, key, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := c.Decrypt(enc, key, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// encrypt 自动补齐到 4 对齐（data 已 32 字节，无补齐）。
	if !bytes.Equal(dec, data) {
		t.Errorf("xxtea 往返失败\n  原: %x\n  解: %x", data, dec)
	}
}

// xxtea offset：前缀明文保留（块密码在 data[offset:] 段工作）。
func TestXxteaOffset(t *testing.T) {
	c, _ := LookupCipher("xxtea")
	data := randBytes(40, 10) // offset=8 后 32 字节（已对齐）
	key := make([]byte, 16)
	off := 8
	enc, err := c.Encrypt(data, key, off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc[:off], data[:off]) {
		t.Error("xxtea offset 前缀应保留明文")
	}
	dec, err := c.Decrypt(enc, key, off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec[:off], data[:off]) {
		t.Error("xxtea offset 解密后前缀应恢复")
	}
	if !bytes.Equal(dec[off:], data[off:]) {
		t.Error("xxtea offset 解密后尾部应恢复")
	}
}

// TestDecryptInPlaceParity 原地解密与复制版 Decrypt 逐字对拍：
// 对实现 CipherInPlace 的流密码，任意 key（含触发"原样返回"分支的非法长度）、
// 任意 offset 下，DecryptInPlace 后的 data 必须与 Decrypt 返回值一致。
func TestDecryptInPlaceParity(t *testing.T) {
	cases := []struct {
		name string
		keys [][]byte
	}{
		{"none", [][]byte{nil, []byte("k")}},
		{"xor", [][]byte{nil, []byte("k"), randBytes(7, 3)}},
		{"xor_carry_rol", [][]byte{nil, randBytes(31, 4), randBytes(32, 5)}},
		{"rc4", [][]byte{nil, randBytes(16, 6)}},
	}
	params := map[string]any{"rol": 3}
	for _, tc := range cases {
		ciph, ok := LookupCipher(tc.name)
		if !ok {
			t.Fatalf("LookupCipher(%s) 未命中", tc.name)
		}
		ipc, ok := ciph.(CipherInPlace)
		if !ok {
			t.Fatalf("%s 应实现 CipherInPlace", tc.name)
		}
		for _, key := range tc.keys {
			for _, off := range []int{0, 4, 999} {
				data := randBytes(64, 9)
				want, err := ciph.Decrypt(data, key, off, params)
				if err != nil {
					t.Fatalf("%s Decrypt 失败: %v", tc.name, err)
				}
				got := append([]byte{}, data...)
				if err := ipc.DecryptInPlace(got, key, off, params); err != nil {
					t.Fatalf("%s DecryptInPlace 失败: %v", tc.name, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s key=%d off=%d 原地解密与复制版不一致", tc.name, len(key), off)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// compressor: none / gzip
// ---------------------------------------------------------------------------

func TestNoneCompressor(t *testing.T) {
	c, _ := LookupCompressor("none")
	data := randBytes(32, 11)
	comp, err := c.Compress(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(comp, data) {
		t.Error("none 压缩应原样返回")
	}
	dec, err := c.Decompress(comp)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, data) {
		t.Error("none 解压应原样返回")
	}
}

func TestGzipRoundTrip(t *testing.T) {
	c, _ := LookupCompressor("gzip")
	// 高熵数据 gzip 压不下去但能往返；低熵数据压缩率高。
	for _, payload := range [][]byte{
		bytes.Repeat([]byte("hello world. "), 200),
		randBytes(1024, 12),
		[]byte(""),
	} {
		comp, err := c.Compress(payload)
		if err != nil {
			t.Fatalf("gzip Compress 失败: %v", err)
		}
		dec, err := c.Decompress(comp)
		if err != nil {
			t.Fatalf("gzip Decompress 失败: %v", err)
		}
		if !bytes.Equal(dec, payload) {
			t.Errorf("gzip 往返失败 (len=%d)", len(payload))
		}
	}
}

// gzip 解压无效数据应报错（不 panic）。
func TestGzipDecompressInvalid(t *testing.T) {
	c, _ := LookupCompressor("gzip")
	_, err := c.Decompress([]byte("not gzip"))
	if err == nil {
		t.Error("gzip 解压非法数据应报错")
	}
}

// TestGzipDecompressMultiMember 多成员拼接流：ISIZE trailer 只描述末一个成员，
// 定长读的尺寸提示必然偏短，覆盖 readAllSized 的探针追加路径；
// 语义须与 io.ReadAll 时代一致（gzip.Reader 多成员模式读全部成员）。
func TestGzipDecompressMultiMember(t *testing.T) {
	c, _ := LookupCompressor("gzip")
	a := bytes.Repeat([]byte("first-member. "), 500)
	b := bytes.Repeat([]byte("x"), 7)
	compA, err := c.Compress(a)
	if err != nil {
		t.Fatal(err)
	}
	compB, err := c.Compress(b)
	if err != nil {
		t.Fatal(err)
	}
	joined := append(append([]byte{}, compA...), compB...)
	dec, err := c.Decompress(joined)
	if err != nil {
		t.Fatalf("多成员解压失败: %v", err)
	}
	want := append(append([]byte{}, a...), b...)
	if !bytes.Equal(dec, want) {
		t.Errorf("多成员解压结果不符：got %d 字节 want %d 字节", len(dec), len(want))
	}
}

// ---------------------------------------------------------------------------
// checksum: none / xor8 / sum8 / crc16 / crc32 / crc32c
// ---------------------------------------------------------------------------

func TestNoneChecksum(t *testing.T) {
	c, _ := LookupChecksum("none")
	if got := c.Sum(randBytes(16, 1), nil); got != 0 {
		t.Errorf("none checksum 应返回 0，got %d", got)
	}
}

// xor8 已知向量：xor8(0x55,0xAA)=0xFF。
func TestXor8BccVector(t *testing.T) {
	c, _ := LookupChecksum("xor8")
	got := c.Sum([]byte{0x55, 0xAA}, nil)
	if got != 0xFF {
		t.Errorf("xor8(0x55,0xAA)=%#x want 0xFF", got)
	}
}

// xor8 全字节 XOR 校验：与手动循环一致。
func TestXor8Manual(t *testing.T) {
	c, _ := LookupChecksum("xor8")
	data := randBytes(100, 13)
	var want byte
	for _, b := range data {
		want ^= b
	}
	if got := c.Sum(data, nil); got != uint64(want) {
		t.Errorf("xor8 手动校验: got %#x want %#x", got, want)
	}
}

func TestSum8(t *testing.T) {
	c, _ := LookupChecksum("sum8")
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} // sum=55
	if got := c.Sum(data, nil); got != 55 {
		t.Errorf("sum8 = %d want 55", got)
	}
}

// crc16 已知向量：crc16("123456789")=0x29B1。
func TestCrc16KnownVector(t *testing.T) {
	c, _ := LookupChecksum("crc16")
	got := c.Sum([]byte("123456789"), nil)
	if got != 0x29B1 {
		t.Errorf("crc16(123456789)=%#x want 0x29B1", got)
	}
}

// crc32 已知向量：crc32("123456789")=0xCBF43926。
func TestCrc32KnownVector(t *testing.T) {
	c, _ := LookupChecksum("crc32")
	got := c.Sum([]byte("123456789"), nil)
	if got != 0xCBF43926 {
		t.Errorf("crc32(123456789)=%#x want 0xCBF43926", got)
	}
}

// crc32c 已知向量：crc32c("123456789")=0xE3069283（Castagnoli 标准 check 值）。
func TestCrc32cKnownVector(t *testing.T) {
	c, _ := LookupChecksum("crc32c")
	got := c.Sum([]byte("123456789"), nil)
	if got != 0xE3069283 {
		t.Errorf("crc32c(123456789)=%#x want 0xE3069283", got)
	}
	// 与标准库直接调用一致。
	want := uint64(crc32.Checksum([]byte("123456789"), crc32.MakeTable(crc32.Castagnoli)))
	if got != want {
		t.Errorf("crc32c 与标准库不一致: got %#x want %#x", got, want)
	}
}

// ---------------------------------------------------------------------------
// hash: md5 / sha1 / sha256（含 HMAC）
// ---------------------------------------------------------------------------

// md5 已知向量：md5("") 的 hex。
func TestMd5Vector(t *testing.T) {
	c, _ := LookupHasher("md5")
	got := c.Hash([]byte(""), nil, nil)
	wantHex := "d41d8cd98f00b204e9800998ecf8427e"
	if hex.EncodeToString(got) != wantHex {
		t.Errorf("md5(\"\")=%s want %s", hex.EncodeToString(got), wantHex)
	}
	if len(got) != 16 {
		t.Errorf("md5 摘要长度=%d want 16", len(got))
	}
}

// sha1 已知向量：sha1("abc") 的 hex。
func TestSha1Vector(t *testing.T) {
	c, _ := LookupHasher("sha1")
	got := c.Hash([]byte("abc"), nil, nil)
	wantHex := "a9993e364706816aba3e25717850c26c9cd0d89d"
	if hex.EncodeToString(got) != wantHex {
		t.Errorf("sha1(abc)=%s want %s", hex.EncodeToString(got), wantHex)
	}
	if len(got) != 20 {
		t.Errorf("sha1 摘要长度=%d want 20", len(got))
	}
}

// sha256 已知向量：sha256("") 的 hex。
func TestSha256Vector(t *testing.T) {
	c, _ := LookupHasher("sha256")
	got := c.Hash([]byte(""), nil, nil)
	wantHex := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hex.EncodeToString(got) != wantHex {
		t.Errorf("sha256(\"\")=%s want %s", hex.EncodeToString(got), wantHex)
	}
	if len(got) != 32 {
		t.Errorf("sha256 摘要长度=%d want 32", len(got))
	}
}

// HMAC：key 非空走 HMAC，输出与直接 crypto/hmac 一致。
func TestHmacConsistency(t *testing.T) {
	data := []byte("hello")
	key := []byte("key")

	// md5
	c, _ := LookupHasher("md5")
	mac := hmac.New(md5.New, key)
	mac.Write(data)
	want := mac.Sum(nil)
	if got := c.Hash(data, key, nil); !bytes.Equal(got, want) {
		t.Errorf("hmac-md5 不一致: %x want %x", got, want)
	}

	// sha1
	c1, _ := LookupHasher("sha1")
	mac1 := hmac.New(sha1.New, key)
	mac1.Write(data)
	want1 := mac1.Sum(nil)
	if got := c1.Hash(data, key, nil); !bytes.Equal(got, want1) {
		t.Errorf("hmac-sha1 不一致: %x want %x", got, want1)
	}

	// sha256
	c2, _ := LookupHasher("sha256")
	mac2 := hmac.New(sha256.New, key)
	mac2.Write(data)
	want2 := mac2.Sum(nil)
	if got := c2.Hash(data, key, nil); !bytes.Equal(got, want2) {
		t.Errorf("hmac-sha256 不一致: %x want %x", got, want2)
	}
}

// HMAC 与 plain hash 必须不同（key 非空时）。
func TestHmacDiffersFromPlain(t *testing.T) {
	c, _ := LookupHasher("sha256")
	data := []byte("payload")
	plain := c.Hash(data, nil, nil)
	withKey := c.Hash(data, []byte("secret"), nil)
	if bytes.Equal(plain, withKey) {
		t.Error("HMAC 与 plain hash 必须不同")
	}
}
