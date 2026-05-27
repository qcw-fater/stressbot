package adapter

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

// newTestLState 创建注册了 crypto 模块的测试用 LState。
func newTestLState() *lua.LState {
	L := lua.NewState()
	RegisterCryptoModule(L)
	return L
}

func callCrypto1(L *lua.LState, fn, code string) string {
	L.Push(L.GetGlobal("crypto"))
	tbl := L.Get(-1).(*lua.LTable)
	f := L.GetField(tbl, fn)
	L.Pop(1)
	if err := L.CallByParam(lua.P{Fn: f, NRet: 1, Protect: true}, lua.LString(code)); err != nil {
		L.Pop(1)
		return ""
	}
	result := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	return result
}

// ─── XOR 系列 ─────────────────────────────────────────────────────────────────

func TestXorRoundTrip(t *testing.T) {
	data := make([]byte, 256)
	rand.Read(data)
	key := make([]byte, 32)
	rand.Read(key)

	enc, bcc := EncryptXorCarryRol(data, key, 0, 3)
	if bcc == 0 && len(data) > 0 {
		t.Error("bcc should not be 0 for random data")
	}
	dec := DecryptXorCarryRol(enc, key, 0, 3)
	if !bytes.Equal(dec, data) {
		t.Error("round-trip failed for xor_carry_rol")
	}
}

func TestXorCarryRoundTrip(t *testing.T) {
	data := make([]byte, 128)
	rand.Read(data)
	key := make([]byte, 32)
	rand.Read(key)

	enc, _ := EncryptXorCarryRol(data, key, 0, 0) // bits=0 → no rotation, pure xor_carry
	dec := DecryptXorCarryRol(enc, key, 0, 0)
	if !bytes.Equal(dec, data) {
		t.Error("round-trip failed for xor_carry (bits=0)")
	}
}

func TestXorBccCorrectness(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	expectedBcc := byte(0)
	for _, b := range data {
		expectedBcc ^= b
	}

	_, bcc := EncryptXorCarryRol(data, key, 0, 3)
	if byte(bcc) != expectedBcc {
		t.Errorf("bcc mismatch: got %02x, want %02x", bcc, expectedBcc)
	}
}

func TestXorOffset(t *testing.T) {
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}
	key := make([]byte, 32)
	rand.Read(key)

	offset := 11
	enc, _ := EncryptXorCarryRol(data, key, offset, 3)
	if !bytes.Equal(enc[:offset], data[:offset]) {
		t.Error("prefix should be unchanged with offset=11")
	}
	dec := DecryptXorCarryRol(enc, key, offset, 3)
	if !bytes.Equal(dec, data) {
		t.Error("round-trip with offset failed")
	}
}

func TestXorRolBits(t *testing.T) {
	data := make([]byte, 256)
	rand.Read(data)
	key := make([]byte, 32)
	rand.Read(key)

	for _, bits := range []uint{1, 3, 5, 7} {
		enc, _ := EncryptXorCarryRol(data, key, 0, bits)
		dec := DecryptXorCarryRol(enc, key, 0, bits)
		if !bytes.Equal(dec, data) {
			t.Errorf("round-trip failed for bits=%d", bits)
		}
	}
}

func TestXorInvalidKey(t *testing.T) {
	data := []byte{1, 2, 3, 4}
	key := make([]byte, 16) // wrong size

	enc, bcc := EncryptXorCarryRol(data, key, 0, 3)
	if !bytes.Equal(enc, data) {
		t.Errorf("invalid key should return original data, got %v", enc)
	}
	if bcc != 0 {
		t.Error("invalid key should return bcc=0")
	}
}

func TestXorEmptyData(t *testing.T) {
	key := make([]byte, 32)
	data := []byte{}
	enc, bcc := EncryptXorCarryRol(data, key, 0, 3)
	if len(enc) != 0 {
		t.Error("empty data should return empty data")
	}
	if bcc != 0 {
		t.Error("empty data should return bcc=0")
	}
}

// ─── XOR Lua 接口 ─────────────────────────────────────────────────────────────

func TestLuaXorCarryRolRoundTrip(t *testing.T) {
	L := newTestLState()
	defer L.Close()

	// require("crypto")
	if err := L.DoString(`local c = require("crypto"); _G.crypto = c`); err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 100)
	rand.Read(data)
	key := make([]byte, 32)
	rand.Read(key)

	// encrypt
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	encFn := L.GetField(tbl, "encrypt_xor_carry_rol")
	if err := L.CallByParam(lua.P{Fn: encFn, NRet: 2, Protect: true},
		lua.LString(string(data)), lua.LString(string(key)), lua.LNumber(0), lua.LNumber(3)); err != nil {
		t.Fatal(err)
	}
	encData := L.Get(-2)
	bcc := L.Get(-1)
	L.Pop(2)

	// decrypt
	decFn := L.GetField(tbl, "decrypt_xor_carry_rol")
	if err := L.CallByParam(lua.P{Fn: decFn, NRet: 1, Protect: true},
		encData, lua.LString(string(key)), lua.LNumber(0), lua.LNumber(3)); err != nil {
		t.Fatal(err)
	}
	decData := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)

	if !bytes.Equal(decData, data) {
		t.Error("Lua round-trip failed")
	}
	if lua.LVAsNumber(bcc) == 0 {
		t.Error("bcc should not be 0")
	}
}

// ─── RC4 ──────────────────────────────────────────────────────────────────────

func TestRC4RoundTrip(t *testing.T) {
	data := make([]byte, 256)
	rand.Read(data)
	key := []byte("testkey1234567890")

	enc := make([]byte, len(data))
	copy(enc, data)
	applyRC4(enc, key)

	dec := make([]byte, len(enc))
	copy(dec, enc)
	applyRC4(dec, key)

	if !bytes.Equal(dec, data) {
		t.Error("RC4 round-trip failed")
	}
}

func TestRC4Symmetry(t *testing.T) {
	data := []byte("hello world")
	key := []byte("secret")

	enc1 := make([]byte, len(data))
	copy(enc1, data)
	applyRC4(enc1, key)

	enc2 := make([]byte, len(data))
	copy(enc2, data)
	applyRC4(enc2, key)

	if !bytes.Equal(enc1, enc2) {
		t.Error("RC4 should be deterministic")
	}
}

func TestRC4KeyLengths(t *testing.T) {
	data := make([]byte, 64)
	rand.Read(data)

	for _, keyLen := range []int{1, 16, 256} {
		key := make([]byte, keyLen)
		rand.Read(key)
		enc := make([]byte, len(data))
		copy(enc, data)
		applyRC4(enc, key)
		dec := make([]byte, len(enc))
		copy(dec, enc)
		applyRC4(dec, key)
		if !bytes.Equal(dec, data) {
			t.Errorf("RC4 round-trip failed for key length %d", keyLen)
		}
	}
}

// ─── AES ──────────────────────────────────────────────────────────────────────

func TestAesEcbRoundTrip(t *testing.T) {
	data := []byte("hello world, this is AES test data!!")
	key := make([]byte, 16) // AES-128
	rand.Read(key)

	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)

	// encrypt
	encFn := L.GetField(tbl, "encrypt_aes_ecb")
	if err := L.CallByParam(lua.P{Fn: encFn, NRet: 1, Protect: true},
		lua.LString(string(data)), lua.LString(string(key))); err != nil {
		t.Fatal(err)
	}
	encData := L.Get(-1)
	L.Pop(1)

	// decrypt
	decFn := L.GetField(tbl, "decrypt_aes_ecb")
	if err := L.CallByParam(lua.P{Fn: decFn, NRet: 1, Protect: true},
		encData, lua.LString(string(key))); err != nil {
		t.Fatal(err)
	}
	decData := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)

	if !bytes.Equal(decData, data) {
		t.Error("AES-ECB round-trip failed")
	}
}

func TestAesCbcRoundTrip(t *testing.T) {
	data := []byte("hello world, this is AES-CBC test!!")
	key := make([]byte, 32) // AES-256
	rand.Read(key)
	iv := make([]byte, 16)
	rand.Read(iv)

	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)

	// encrypt
	encFn := L.GetField(tbl, "encrypt_aes_cbc")
	if err := L.CallByParam(lua.P{Fn: encFn, NRet: 1, Protect: true},
		lua.LString(string(data)), lua.LString(string(key)), lua.LString(string(iv))); err != nil {
		t.Fatal(err)
	}
	encData := L.Get(-1)
	L.Pop(1)

	// decrypt
	decFn := L.GetField(tbl, "decrypt_aes_cbc")
	if err := L.CallByParam(lua.P{Fn: decFn, NRet: 1, Protect: true},
		encData, lua.LString(string(key)), lua.LString(string(iv))); err != nil {
		t.Fatal(err)
	}
	decData := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)

	if !bytes.Equal(decData, data) {
		t.Error("AES-CBC round-trip failed")
	}
}

func TestAesKeySizes(t *testing.T) {
	data := []byte("16-byte padded!!!")
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)

	for _, keySize := range []int{16, 24, 32} {
		key := make([]byte, keySize)
		rand.Read(key)
		encFn := L.GetField(tbl, "encrypt_aes_ecb")
		L.CallByParam(lua.P{Fn: encFn, NRet: 1, Protect: true},
			lua.LString(string(data)), lua.LString(string(key)))
		encData := L.Get(-1)
		L.Pop(1)

		decFn := L.GetField(tbl, "decrypt_aes_ecb")
		L.CallByParam(lua.P{Fn: decFn, NRet: 1, Protect: true},
			encData, lua.LString(string(key)))
		decData := []byte(lua.LVAsString(L.Get(-1)))
		L.Pop(1)

		if !bytes.Equal(decData, data) {
			t.Errorf("AES-ECB round-trip failed for key size %d", keySize)
		}
	}
}

func TestPkcs7Padding(t *testing.T) {
	// 数据恰好块对齐时应额外填充一个完整块
	data := make([]byte, 16)
	padded := pkcs7Pad(data, 16)
	if len(padded) != 32 {
		t.Errorf("expected 32 bytes, got %d", len(padded))
	}
	for _, b := range padded[16:] {
		if b != 16 {
			t.Error("padding byte should be 16")
		}
	}
}

// ─── XXTEA ────────────────────────────────────────────────────────────────────

func TestXxteaRoundTrip(t *testing.T) {
	data := make([]byte, 32)
	rand.Read(data)
	var key [4]uint32
	for i := range key {
		key[i] = binary.LittleEndian.Uint32(data[i*4 : i*4+4])
	}

	enc := encryptXXTEA(data, key)
	dec := decryptXXTEA(enc, key)
	if !bytes.Equal(dec, data) {
		t.Error("XXTEA round-trip failed")
	}
}

func TestXxteaLuaRoundTrip(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)

	data := make([]byte, 24)
	rand.Read(data)
	key := make([]byte, 16)
	rand.Read(key)

	encFn := L.GetField(tbl, "encrypt_xxtea")
	L.CallByParam(lua.P{Fn: encFn, NRet: 1, Protect: true},
		lua.LString(string(data)), lua.LString(string(key)))
	encData := L.Get(-1)
	L.Pop(1)

	decFn := L.GetField(tbl, "decrypt_xxtea")
	L.CallByParam(lua.P{Fn: decFn, NRet: 1, Protect: true},
		encData, lua.LString(string(key)))
	decData := []byte(lua.LVAsString(L.Get(-1)))
	L.Pop(1)

	if !bytes.Equal(decData, data) {
		t.Error("XXTEA Lua round-trip failed")
	}
}

// ─── 校验和 ───────────────────────────────────────────────────────────────────

func TestCrc16(t *testing.T) {
	// CRC-16/CCITT 已知值：crc16("123456789") = 0x29B1
	crc := crc16CCITT([]byte("123456789"))
	if crc != 0x29B1 {
		t.Errorf("crc16 mismatch: got %04x, want 29B1", crc)
	}
}

func TestCrc32(t *testing.T) {
	// CRC-32/IEEE 已知值：crc32("123456789") = 0xCBF43926
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "crc32")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LString("123456789"))
	result := uint32(lua.LVAsNumber(L.Get(-1)))
	L.Pop(1)
	if result != 0xCBF43926 {
		t.Errorf("crc32 mismatch: got %08x, want CBF43926", result)
	}
}

func TestBcc(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "bcc")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LString(string([]byte{0x55, 0xAA})))
	result := byte(lua.LVAsNumber(L.Get(-1)))
	L.Pop(1)
	if result != 0xFF {
		t.Errorf("bcc mismatch: got %02x, want FF", result)
	}
}

// ─── 哈希 ─────────────────────────────────────────────────────────────────────

func TestMd5(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "md5")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LString(""))
	result := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	// MD5("") = d41d8cd98f00b204e9800998ecf8427e
	if result != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("md5 mismatch: got %s", result)
	}
}

func TestSha1(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "sha1")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LString("abc"))
	result := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	// SHA1("abc") = a9993e364706816aba3e25717850c26c9cd0d89d
	if result != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Errorf("sha1 mismatch: got %s", result)
	}
}

func TestSha256(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "sha256")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LString(""))
	result := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	// SHA256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	if result != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("sha256 mismatch: got %s", result)
	}
}

// ─── HMAC ─────────────────────────────────────────────────────────────────────

func TestHmacMd5(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "hmac_md5")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true},
		lua.LString("hello"), lua.LString("key"))
	result := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	if len(result) != 32 {
		t.Errorf("hmac_md5 should return 32 hex chars, got %d", len(result))
	}
}

func TestHmacSha256(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)
	tbl := L.GetGlobal("crypto").(*lua.LTable)
	fn := L.GetField(tbl, "hmac_sha256")
	L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true},
		lua.LString("hello"), lua.LString("key"))
	result := lua.LVAsString(L.Get(-1))
	L.Pop(1)
	if len(result) != 64 {
		t.Errorf("hmac_sha256 should return 64 hex chars, got %d", len(result))
	}
}

// ─── 等价性测试：Go 与原始 Lua ────────────────────────────────────────────────

func TestXorCarryRolEquivalence(t *testing.T) {
	// 用简单数据验证 Go 实现与原始 Lua 算法字节级一致
	data := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}

	// 手动计算期望值（模拟原始 Lua net_encrypt 逻辑）
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

	enc, _ := EncryptXorCarryRol(data, key, 0, 3)
	if !bytes.Equal(enc, expected) {
		t.Errorf("Go encrypt mismatch:\n  got:      %x\n  expected: %x", enc, expected)
	}
}

// ─── 基准测试 ─────────────────────────────────────────────────────────────────

func BenchmarkXorCarryRol_Go(b *testing.B) {
	data := make([]byte, 1024)
	rand.Read(data)
	key := make([]byte, 32)
	rand.Read(key)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncryptXorCarryRol(data, key, 0, 3)
	}
}

func BenchmarkXorCarryRol_Lua(b *testing.B) {
	L := newTestLState()
	defer L.Close()
	L.DoString(`local c = require("crypto"); _G.crypto = c`)

	data := make([]byte, 1024)
	rand.Read(data)
	key := make([]byte, 32)
	rand.Read(key)
	dataStr := lua.LString(string(data))
	keyStr := lua.LString(string(key))

	tbl := L.GetGlobal("crypto").(*lua.LTable)
	encFn := L.GetField(tbl, "encrypt_xor_carry_rol")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L.CallByParam(lua.P{Fn: encFn, NRet: 2, Protect: true},
			dataStr, keyStr, lua.LNumber(0), lua.LNumber(3))
		L.Pop(2)
	}
}

func TestCryptoModuleFunctions(t *testing.T) {
	L := newTestLState()
	defer L.Close()
	if err := L.DoString(`
		local c = require("crypto")
		assert(c.encrypt_xor ~= nil, "missing encrypt_xor")
		assert(c.decrypt_xor ~= nil, "missing decrypt_xor")
		assert(c.encrypt_xor_carry ~= nil, "missing encrypt_xor_carry")
		assert(c.decrypt_xor_carry ~= nil, "missing decrypt_xor_carry")
		assert(c.encrypt_xor_carry_rol ~= nil, "missing encrypt_xor_carry_rol")
		assert(c.decrypt_xor_carry_rol ~= nil, "missing decrypt_xor_carry_rol")
		assert(c.encrypt_rc4 ~= nil, "missing encrypt_rc4")
		assert(c.decrypt_rc4 ~= nil, "missing decrypt_rc4")
		assert(c.encrypt_aes_ecb ~= nil, "missing encrypt_aes_ecb")
		assert(c.decrypt_aes_ecb ~= nil, "missing decrypt_aes_ecb")
		assert(c.encrypt_aes_cbc ~= nil, "missing encrypt_aes_cbc")
		assert(c.decrypt_aes_cbc ~= nil, "missing decrypt_aes_cbc")
		assert(c.encrypt_xxtea ~= nil, "missing encrypt_xxtea")
		assert(c.decrypt_xxtea ~= nil, "missing decrypt_xxtea")
		assert(c.crc16 ~= nil, "missing crc16")
		assert(c.crc32 ~= nil, "missing crc32")
		assert(c.bcc ~= nil, "missing bcc")
		assert(c.md5 ~= nil, "missing md5")
		assert(c.sha1 ~= nil, "missing sha1")
		assert(c.sha256 ~= nil, "missing sha256")
		assert(c.hmac_md5 ~= nil, "missing hmac_md5")
		assert(c.hmac_sha256 ~= nil, "missing hmac_sha256")
	`); err != nil {
		t.Fatal(err)
	}
}
