package adapter

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"math"

	lua "github.com/yuin/gopher-lua"
)

// ---------------------------------------------------------------------------
// 模块注册
// ---------------------------------------------------------------------------

// RegisterCryptoModule 向 LState 预加载 crypto Lua 模块。
// codec.lua 通过 local crypto = require("crypto") 加载。
//
// 设计原则：零耦合。每个算法是独立函数名，不做 mode 字符串分派，
// 未来不同服务器的 codec.lua 调用不同函数名即可，Go 侧无需改动分派逻辑。
//
// Lua 用法：
//
//	local crypto = require("crypto")
//
//	-- XOR 系列（自定义流密码，offset 参数控制部分加密）
//	data, bcc = crypto.encrypt_xor(data, key, offset)           → 纯 XOR
//	data      = crypto.decrypt_xor(data, key, offset)
//	data, bcc = crypto.encrypt_xor_carry(data, key, offset)     → XOR + carry 加法反馈
//	data      = crypto.decrypt_xor_carry(data, key, offset)
//	data, bcc = crypto.encrypt_xor_carry_rol(data, key, offset, bits)  → XOR + carry + ROL8
//	data      = crypto.decrypt_xor_carry_rol(data, key, offset, bits)
//
//	-- RC4（经典流密码，游戏服务器极常见）
//	data = crypto.encrypt_rc4(data, key)
//	data = crypto.decrypt_rc4(data, key)
//
//	-- AES（标准块加密，自动 PKCS#7 填充）
//	data = crypto.encrypt_aes_ecb(data, key)          → AES-ECB
//	data = crypto.decrypt_aes_ecb(data, key)
//	data = crypto.encrypt_aes_cbc(data, key, iv)      → AES-CBC
//	data = crypto.decrypt_aes_cbc(data, key, iv)
//
//	-- XXTEA（轻量块密码，手游常用）
//	data = crypto.encrypt_xxtea(data, key)
//	data = crypto.decrypt_xxtea(data, key)
//
//	-- 校验和
//	num = crypto.crc16(data)      → CRC-16/CCITT
//	num = crypto.crc32(data)      → CRC-32/IEEE
//	num = crypto.bcc(data)        → XOR all bytes
//
//	-- 哈希（返回 hex 字符串）
//	str = crypto.md5(data)        → 32 字符 hex
//	str = crypto.sha1(data)       → 40 字符 hex
//	str = crypto.sha256(data)     → 64 字符 hex
//
//	-- HMAC（返回 hex 字符串）
//	str = crypto.hmac_md5(data, key)       → 32 字符 hex
//	str = crypto.hmac_sha256(data, key)    → 64 字符 hex
func RegisterCryptoModule(L *lua.LState) {
	L.PreloadModule("crypto", func(L *lua.LState) int {
		mod := L.NewTable()
		// XOR 系列
		L.SetField(mod, "encrypt_xor", L.NewFunction(cryptoEncryptXor))
		L.SetField(mod, "decrypt_xor", L.NewFunction(cryptoDecryptXor))
		L.SetField(mod, "encrypt_xor_carry", L.NewFunction(cryptoEncryptXorCarry))
		L.SetField(mod, "decrypt_xor_carry", L.NewFunction(cryptoDecryptXorCarry))
		L.SetField(mod, "encrypt_xor_carry_rol", L.NewFunction(cryptoEncryptXorCarryRol))
		L.SetField(mod, "decrypt_xor_carry_rol", L.NewFunction(cryptoDecryptXorCarryRol))
		// RC4
		L.SetField(mod, "encrypt_rc4", L.NewFunction(cryptoEncryptRC4))
		L.SetField(mod, "decrypt_rc4", L.NewFunction(cryptoDecryptRC4))
		// AES
		L.SetField(mod, "encrypt_aes_ecb", L.NewFunction(cryptoEncryptAesEcb))
		L.SetField(mod, "decrypt_aes_ecb", L.NewFunction(cryptoDecryptAesEcb))
		L.SetField(mod, "encrypt_aes_cbc", L.NewFunction(cryptoEncryptAesCbc))
		L.SetField(mod, "decrypt_aes_cbc", L.NewFunction(cryptoDecryptAesCbc))
		// XXTEA
		L.SetField(mod, "encrypt_xxtea", L.NewFunction(cryptoEncryptXxtea))
		L.SetField(mod, "decrypt_xxtea", L.NewFunction(cryptoDecryptXxtea))
		// 校验和
		L.SetField(mod, "crc16", L.NewFunction(cryptoCrc16))
		L.SetField(mod, "crc32", L.NewFunction(cryptoCrc32))
		L.SetField(mod, "bcc", L.NewFunction(cryptoBcc))
		// 哈希
		L.SetField(mod, "md5", L.NewFunction(cryptoMd5))
		L.SetField(mod, "sha1", L.NewFunction(cryptoSha1))
		L.SetField(mod, "sha256", L.NewFunction(cryptoSha256))
		// HMAC
		L.SetField(mod, "hmac_md5", L.NewFunction(cryptoHmacMd5))
		L.SetField(mod, "hmac_sha256", L.NewFunction(cryptoHmacSha256))
		L.Push(mod)
		return 1
	})
}

// ---------------------------------------------------------------------------
// XOR 系列通用工具
// ---------------------------------------------------------------------------

// rol8 对单个字节做循环左移（rotate left）。
// 等价于 Lua 中的 bit.bor(bit.band(bit.lshift(x, n), 0xFF), bit.rshift(x, 8-n))。
func rol8(b byte, n uint) byte { return (b << n) | (b >> (8 - n)) }

// ror8 对单个字节做循环右移（rotate right），是 rol8 的逆运算。
func ror8(b byte, n uint) byte { return (b >> n) | (b << (8 - n)) }

// computeBcc 计算所有字节的异或校验和（XOR checksum）。
// 加密前对明文部分调用一次，结果写入协议头的 bcc 字段，供服务端校验。
func computeBcc(data []byte) byte {
	var bcc byte
	for _, b := range data {
		bcc ^= b
	}
	return bcc
}

// luaOffset 从 Lua 栈中读取可选的 offset 参数。
// 参数缺失时返回 0；负值钳位到 0（与 codec.lua 中的行为一致）。
func luaOffset(L *lua.LState, argIdx int) int {
	if L.GetTop() < argIdx {
		return 0
	}
	n := int(L.CheckNumber(argIdx))
	if n < 0 {
		return 0
	}
	return n
}

// xorEncryptWithOffset 是 XOR 系列 encrypt 的通用 Lua 包装。
//
// 统一处理 XOR 系列的公共逻辑：校验 key 长度（必须 32 字节）→ 切片保留
// data[:offset] 明文前缀 → 对 data[offset:] 计算 BCC → 调用 encryptFn
// 原地加密 → 拼回完整数据。key 不合法或 data 为空时返回原数据 + bcc=0。
//
// Lua 返回值：(encrypted_data, bcc)
func xorEncryptWithOffset(L *lua.LState, encryptFn func(data, key []byte)) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	offset := luaOffset(L, 3)

	if len(key) != 32 || len(data) == 0 {
		L.Push(lua.LString(string(data)))
		L.Push(lua.LNumber(0))
		return 2
	}
	if offset > len(data) {
		offset = len(data)
	}

	bcc := computeBcc(data[offset:])
	enc := make([]byte, len(data))
	copy(enc, data)
	encryptFn(enc[offset:], key)

	L.Push(lua.LString(string(enc)))
	L.Push(lua.LNumber(bcc))
	return 2
}

// xorDecryptWithOffset 是 XOR 系列 decrypt 的通用 Lua 包装。
//
// 与 xorEncryptWithOffset 对称，不含 BCC 计算（解密侧不需要校验 BCC）。
// key 不合法或 data 为空时返回原数据。
//
// Lua 返回值：decrypted_data
func xorDecryptWithOffset(L *lua.LState, decryptFn func(data, key []byte)) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	offset := luaOffset(L, 3)

	if len(key) != 32 || len(data) == 0 {
		L.Push(lua.LString(string(data)))
		return 1
	}
	if offset > len(data) {
		offset = len(data)
	}

	out := make([]byte, len(data))
	copy(out, data)
	decryptFn(out[offset:], key)

	L.Push(lua.LString(string(out)))
	return 1
}

// xorEncryptWithRolBits 带可配旋转位数的 XOR+carry+ROL encrypt Lua 包装。
//
// 与 xorEncryptWithOffset 类似，但额外读取第 4 个参数 bits（1~7，默认 3），
// 传给 encryptFn 控制每次 XOR+carry 后的循环左移位数。
//
// Lua 返回值：(encrypted_data, bcc)
func xorEncryptWithRolBits(L *lua.LState, encryptFn func(data, key []byte, bits uint)) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	offset := luaOffset(L, 3)
	bits := uint(3)
	if L.GetTop() >= 4 {
		bits = uint(L.CheckNumber(4))
	}
	if bits < 1 {
		bits = 1
	}
	if bits > 7 {
		bits = 7
	}

	if len(key) != 32 || len(data) == 0 {
		L.Push(lua.LString(string(data)))
		L.Push(lua.LNumber(0))
		return 2
	}
	if offset > len(data) {
		offset = len(data)
	}

	bcc := computeBcc(data[offset:])
	enc := make([]byte, len(data))
	copy(enc, data)
	encryptFn(enc[offset:], key, bits)

	L.Push(lua.LString(string(enc)))
	L.Push(lua.LNumber(bcc))
	return 2
}

// xorDecryptWithRolBits 带可配旋转位数的 XOR+carry+ROL decrypt Lua 包装。
//
// 与 xorEncryptWithRolBits 对称，读取相同的 bits 参数用于 ROR 逆运算。
//
// Lua 返回值：decrypted_data
func xorDecryptWithRolBits(L *lua.LState, decryptFn func(data, key []byte, bits uint)) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	offset := luaOffset(L, 3)
	bits := uint(3)
	if L.GetTop() >= 4 {
		bits = uint(L.CheckNumber(4))
	}
	if bits < 1 {
		bits = 1
	}
	if bits > 7 {
		bits = 7
	}

	if len(key) != 32 || len(data) == 0 {
		L.Push(lua.LString(string(data)))
		return 1
	}
	if offset > len(data) {
		offset = len(data)
	}

	out := make([]byte, len(data))
	copy(out, data)
	decryptFn(out[offset:], key, bits)

	L.Push(lua.LString(string(out)))
	return 1
}

// ---------------------------------------------------------------------------
// XOR 系列算法实现
// ---------------------------------------------------------------------------

// encryptXor 纯 XOR 流加密：data[i] ^= key[i % len(key)]。
// 因为 XOR 是自反的，加密和解密是同一个操作。
func encryptXor(data, key []byte) {
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
}

// decryptXor 纯 XOR 流解密，与 encryptXor 完全相同（XOR 对称性）。
func decryptXor(data, key []byte) { encryptXor(data, key) }

// encryptXorCarry XOR + carry 加法反馈流加密。
//
// 对每个字节：
//   x = data[i] ^ key[i % 32]
//   x = x + carry
//   data[i] = x
//   carry = x
//
// carry 形成链式依赖，每个字节的密文依赖前面所有明文，比纯 XOR 更难破译。
func encryptXorCarry(data, key []byte) {
	var carry byte
	for i := range data {
		x := data[i] ^ key[i%len(key)]
		x += carry
		data[i] = x
		carry = x
	}
}

// decryptXorCarry XOR + carry 加法反馈流解密。
//
// 逆运算：
//   enc = data[i]
//   x = enc - carry
//   x = x ^ key[i % 32]
//   data[i] = x
//   carry = enc
//
// 注意 carry 取的是加密后的密文字节 enc，不是解密后的明文字节。
func decryptXorCarry(data, key []byte) {
	var carry byte
	for i := range data {
		enc := data[i]
		x := enc - carry
		x ^= key[i%len(key)]
		data[i] = x
		carry = enc
	}
}

// encryptXorCarryRol XOR + carry + ROL8 流加密。
//
// 对每个字节：
//   x = data[i] ^ key[i % 32]
//   x = x + carry
//   x = ROL8(x, rolBits)
//   data[i] = x
//   carry = x
//
// 这是当前游戏服务器使用的 NetEncrypt 算法（rolBits=3）。
// ROL8 增加扩散性，使相邻明文字节的差异在密文中迅速扩散到所有位。
func encryptXorCarryRol(data, key []byte, rolBits uint) {
	var carry byte
	for i := range data {
		x := data[i] ^ key[i%len(key)]
		x += carry
		x = rol8(x, rolBits)
		data[i] = x
		carry = x
	}
}

// decryptXorCarryRol XOR + carry + ROL8 流解密。
//
// 逆运算：
//   enc = data[i]
//   x = ROR8(enc, rolBits)
//   x = x - carry
//   x = x ^ key[i % 32]
//   data[i] = x
//   carry = enc
//
// 解密顺序是加密的精确逆序：先反旋转，再减 carry，再 XOR key。
func decryptXorCarryRol(data, key []byte, rolBits uint) {
	var carry byte
	for i := range data {
		enc := data[i]
		x := ror8(enc, rolBits)
		x -= carry
		x ^= key[i%len(key)]
		data[i] = x
		carry = enc
	}
}

// ---------------------------------------------------------------------------
// XOR 系列 Lua 入口
// ---------------------------------------------------------------------------

// cryptoEncryptXor crypto.encrypt_xor(data, key, offset) → (encrypted_data, bcc) — 纯 XOR 流加密
func cryptoEncryptXor(L *lua.LState) int { return xorEncryptWithOffset(L, encryptXor) }

// cryptoDecryptXor crypto.decrypt_xor(data, key, offset) → decrypted_data — 纯 XOR 流解密
func cryptoDecryptXor(L *lua.LState) int { return xorDecryptWithOffset(L, decryptXor) }

// cryptoEncryptXorCarry crypto.encrypt_xor_carry(data, key, offset) → (encrypted_data, bcc) — XOR+carry 流加密
func cryptoEncryptXorCarry(L *lua.LState) int { return xorEncryptWithOffset(L, encryptXorCarry) }

// cryptoDecryptXorCarry crypto.decrypt_xor_carry(data, key, offset) → decrypted_data — XOR+carry 流解密
func cryptoDecryptXorCarry(L *lua.LState) int { return xorDecryptWithOffset(L, decryptXorCarry) }

// cryptoEncryptXorCarryRol crypto.encrypt_xor_carry_rol(data, key, offset, bits) → (encrypted_data, bcc) — XOR+carry+ROL 流加密
func cryptoEncryptXorCarryRol(L *lua.LState) int { return xorEncryptWithRolBits(L, encryptXorCarryRol) }

// cryptoDecryptXorCarryRol crypto.decrypt_xor_carry_rol(data, key, offset, bits) → decrypted_data — XOR+carry+ROL 流解密
func cryptoDecryptXorCarryRol(L *lua.LState) int { return xorDecryptWithRolBits(L, decryptXorCarryRol) }

// ---------------------------------------------------------------------------
// RC4
// ---------------------------------------------------------------------------

// applyRC4 对 data 做 RC4 流加密/解密（对称操作）。
//
// RC4 是对称流密码：加密和解密使用完全相同的操作。
// 密钥长度 1~256 字节，内部维护 256 字节置换表（S-box）。
// Go 标准库的 crypto/rc4 在 Go 1.24 被移除，此处自行实现。
//
// 算法分两阶段：
//  1. KSA（Key-Scheduling Algorithm）：用 key 初始化 S-box
//  2. PRGA（Pseudo-Random Generation Algorithm）：逐字节生成密钥流并 XOR
func applyRC4(data, key []byte) {
	// KSA：初始化 S-box
	s := [256]byte{}
	for i := range s {
		s[i] = byte(i)
	}
	var j byte
	for i := range s {
		j = j + s[i] + key[i%len(key)]
		s[i], s[j] = s[j], s[i]
	}
	// PRGA：生成密钥流并 XOR
	var si, sj byte
	for i := range data {
		si++
		sj = sj + s[si]
		s[si], s[sj] = s[sj], s[si]
		data[i] ^= s[(s[si]+s[sj])&0xFF]
	}
}

// cryptoEncryptRC4 crypto.encrypt_rc4(data, key) → encrypted_data — RC4 流加密
//
// 参数：data (string) — 原始数据；key (string) — 1~256 字节密钥。
// RC4 是对称的（encrypt = decrypt），保留两个名称仅为语义清晰。
func cryptoEncryptRC4(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	out := make([]byte, len(data))
	copy(out, data)
	applyRC4(out, key)
	L.Push(lua.LString(string(out)))
	return 1
}

// cryptoDecryptRC4 crypto.decrypt_rc4(data, key) → decrypted_data — RC4 流解密
//
// 参数同 encrypt_rc4。解密就是再次执行 RC4（对称性）。
func cryptoDecryptRC4(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	out := make([]byte, len(data))
	copy(out, data)
	applyRC4(out, key)
	L.Push(lua.LString(string(out)))
	return 1
}

// ---------------------------------------------------------------------------
// AES
// ---------------------------------------------------------------------------

// errInvalidPadding PKCS#7 填充校验失败时返回的错误。
var errInvalidPadding = errors.New("invalid pkcs7 padding")

// pkcs7Pad 对数据做 PKCS#7 填充，使其长度为 blockSize 的整数倍。
// 即使数据恰好块对齐，也会额外填充一个完整块（RFC 5652 规定）。
func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	pad := make([]byte, n)
	for i := range pad {
		pad[i] = byte(n)
	}
	return append(data, pad...)
}

// pkcs7Unpad 移除 PKCS#7 填充，验证填充字节的合法性。
// 填充不合法时返回 errInvalidPadding（数据损坏或密钥错误）。
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errInvalidPadding
	}
	n := int(data[len(data)-1])
	if n == 0 || n > len(data) {
		return nil, errInvalidPadding
	}
	for i := len(data) - n; i < len(data); i++ {
		if data[i] != byte(n) {
			return nil, errInvalidPadding
		}
	}
	return data[:len(data)-n], nil
}

// cryptoEncryptAesEcb crypto.encrypt_aes_ecb(data, key) → encrypted_data — AES-ECB 加密
//
// 参数：data (string) — 原始数据；key (string) — 16/24/32 字节（AES-128/192/256）。
// 自动 PKCS#7 填充。出错时返回 (nil, error_string)。
func cryptoEncryptAesEcb(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))

	block, err := aes.NewCipher(key)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	padded := pkcs7Pad(data, aes.BlockSize)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[i:], padded[i:])
	}

	L.Push(lua.LString(string(out)))
	return 1
}

// cryptoDecryptAesEcb crypto.decrypt_aes_ecb(data, key) → decrypted_data — AES-ECB 解密
//
// 参数同 encrypt_aes_ecb。自动去除 PKCS#7 填充。
// 密文不是块大小整数倍或填充不合法时返回 (nil, error_string)。
func cryptoDecryptAesEcb(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))

	block, err := aes.NewCipher(key)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	if len(data)%aes.BlockSize != 0 {
		L.Push(lua.LNil)
		L.Push(lua.LString("ciphertext is not a multiple of block size"))
		return 2
	}

	out := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(out[i:], data[i:])
	}

	unpadded, err := pkcs7Unpad(out)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString("pkcs7 unpad failed"))
		return 2
	}

	L.Push(lua.LString(string(unpadded)))
	return 1
}

// cryptoEncryptAesCbc crypto.encrypt_aes_cbc(data, key, iv) → encrypted_data — AES-CBC 加密
//
// 参数：data (string) — 原始数据；key (string) — 16/24/32 字节；
// iv (string) — 16 字节初始化向量。自动 PKCS#7 填充。
func cryptoEncryptAesCbc(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	iv := []byte(L.CheckString(3))

	block, err := aes.NewCipher(key)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	padded := pkcs7Pad(data, aes.BlockSize)
	out := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(out, padded)

	L.Push(lua.LString(string(out)))
	return 1
}

// cryptoDecryptAesCbc crypto.decrypt_aes_cbc(data, key, iv) → decrypted_data — AES-CBC 解密
//
// 参数同 encrypt_aes_cbc。自动去除 PKCS#7 填充。
func cryptoDecryptAesCbc(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	iv := []byte(L.CheckString(3))

	block, err := aes.NewCipher(key)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	if len(data)%aes.BlockSize != 0 {
		L.Push(lua.LNil)
		L.Push(lua.LString("ciphertext is not a multiple of block size"))
		return 2
	}

	out := make([]byte, len(data))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(out, data)

	unpadded, err := pkcs7Unpad(out)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString("pkcs7 unpad failed"))
		return 2
	}

	L.Push(lua.LString(string(unpadded)))
	return 1
}

// ---------------------------------------------------------------------------
// XXTEA
// ---------------------------------------------------------------------------

// xxteaDelta XXTEA 算法的黄金比例增量常量（⌊2³² / φ⌋）。
const xxteaDelta uint32 = 0x9E3779B9

// encryptXXTEA 对 data 做 XXTEA 加密。
//
// XXTEA 是Corrected Block TEA 的变体，对可变长度块进行加密。
// data 长度必须 ≥ 8 字节且 4 字节对齐（不足由调用方补齐）。
// key 是 4 个 uint32（16 字节），采用小端序。
// 轮数 = 6 + 52/n（n 为 uint32 个数），保证充分扩散。
func encryptXXTEA(data []byte, key [4]uint32) []byte {
	n := len(data) / 4
	if n < 2 {
		return data
	}
	v := bytesToUint32s(data, n)

	rounds := 6 + 52/n
	sum := uint32(0)
	for i := 0; i < rounds; i++ {
		sum += xxteaDelta
		e := (sum >> 2) & 3
		for p := 0; p < n; p++ {
			left := v[(p+n-1)%n]
			right := v[(p+1)%n]
			z := ((right >> 5 ^ left << 2) + (left >> 3 ^ right << 4)) ^ ((sum ^ right) + (key[uint32(p)&3^e] ^ left))
			v[p] += z
		}
	}
	return uint32sToBytes(v)
}

// decryptXXTEA 对 data 做 XXTEA 解密，是 encryptXXTEA 的逆运算。
// 从最大 sum 开始递减，按逆序处理每个 uint32 元素。
func decryptXXTEA(data []byte, key [4]uint32) []byte {
	n := len(data) / 4
	if n < 2 {
		return data
	}
	v := bytesToUint32s(data, n)

	rounds := 6 + 52/n
	sum := uint32(rounds) * xxteaDelta
	for i := 0; i < rounds; i++ {
		e := (sum >> 2) & 3
		for p := n - 1; ; p-- {
			right := v[(p+1)%n]
			left := v[(p+n-1)%n]
			z := ((right >> 5 ^ left << 2) + (left >> 3 ^ right << 4)) ^ ((sum ^ right) + (key[uint32(p)&3^e] ^ left))
			v[p] -= z
			if p == 0 {
				break
			}
		}
		sum -= xxteaDelta
	}
	return uint32sToBytes(v)
}

// bytesToUint32s 将字节切片转为小端序 uint32 切片。
func bytesToUint32s(data []byte, n int) []uint32 {
	v := make([]uint32, n)
	for i := 0; i < n; i++ {
		v[i] = binary.LittleEndian.Uint32(data[i*4:])
	}
	return v
}

// uint32sToBytes 将 uint32 切片转为小端序字节切片。
func uint32sToBytes(v []uint32) []byte {
	out := make([]byte, len(v)*4)
	for i, u := range v {
		binary.LittleEndian.PutUint32(out[i*4:], u)
	}
	return out
}

// cryptoEncryptXxtea crypto.encrypt_xxtea(data, key) → encrypted_data — XXTEA 加密
//
// 参数：data (string) — 原始数据（自动补齐到 4 字节对齐）；
// key (string) — 16 字节密钥（4 个小端序 uint32）。
func cryptoEncryptXxtea(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	keyBytes := []byte(L.CheckString(2))

	var key [4]uint32
	for i := 0; i < 4 && i*4 < len(keyBytes); i++ {
		key[i] = binary.LittleEndian.Uint32(keyBytes[i*4:])
	}

	// 补齐到 4 字节对齐
	if rem := len(data) % 4; rem != 0 {
		pad := make([]byte, 4-rem)
		data = append(data, pad...)
	}

	result := encryptXXTEA(data, key)
	L.Push(lua.LString(string(result)))
	return 1
}

// cryptoDecryptXxtea crypto.decrypt_xxtea(data, key) → decrypted_data — XXTEA 解密
//
// 参数同 encrypt_xxtea。data 必须是 4 字节对齐且 ≥ 8 字节，否则返回 (nil, error_string)。
func cryptoDecryptXxtea(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	keyBytes := []byte(L.CheckString(2))

	if len(data)%4 != 0 || len(data) < 8 {
		L.Push(lua.LNil)
		L.Push(lua.LString("invalid xxtea data length"))
		return 2
	}

	var key [4]uint32
	for i := 0; i < 4 && i*4 < len(keyBytes); i++ {
		key[i] = binary.LittleEndian.Uint32(keyBytes[i*4:])
	}

	result := decryptXXTEA(data, key)
	L.Push(lua.LString(string(result)))
	return 1
}

// ---------------------------------------------------------------------------
// 校验和
// ---------------------------------------------------------------------------

// crc16CCITT 计算 CRC-16/CCITT 校验和。
// 多项式 0x1021，初始值 0xFFFF，无输入/输出反转。
func crc16CCITT(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// cryptoCrc16 crypto.crc16(data) → number — CRC-16/CCITT 校验和
func cryptoCrc16(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	crc := crc16CCITT(data)
	L.Push(lua.LNumber(crc))
	return 1
}

// cryptoCrc32 crypto.crc32(data) → number — CRC-32/IEEE 校验和
func cryptoCrc32(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	L.Push(lua.LNumber(crc32.ChecksumIEEE(data)))
	return 1
}

// cryptoBcc crypto.bcc(data) → number — XOR 所有字节得到单字节校验和
func cryptoBcc(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	L.Push(lua.LNumber(computeBcc(data)))
	return 1
}

// ---------------------------------------------------------------------------
// 哈希
// ---------------------------------------------------------------------------

// cryptoMd5 crypto.md5(data) → string — MD5 哈希，返回 32 字符小写 hex
func cryptoMd5(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	h := md5.Sum(data)
	L.Push(lua.LString(hex.EncodeToString(h[:])))
	return 1
}

// cryptoSha1 crypto.sha1(data) → string — SHA-1 哈希，返回 40 字符小写 hex
func cryptoSha1(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	h := sha1.Sum(data)
	L.Push(lua.LString(hex.EncodeToString(h[:])))
	return 1
}

// cryptoSha256 crypto.sha256(data) → string — SHA-256 哈希，返回 64 字符小写 hex
func cryptoSha256(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	h := sha256.Sum256(data)
	L.Push(lua.LString(hex.EncodeToString(h[:])))
	return 1
}

// ---------------------------------------------------------------------------
// HMAC
// ---------------------------------------------------------------------------

// cryptoHmacMd5 crypto.hmac_md5(data, key) → string — HMAC-MD5，返回 32 字符小写 hex
func cryptoHmacMd5(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	mac := hmac.New(md5.New, key)
	mac.Write(data)
	L.Push(lua.LString(hex.EncodeToString(mac.Sum(nil))))
	return 1
}

// cryptoHmacSha256 crypto.hmac_sha256(data, key) → string — HMAC-SHA256，返回 64 字符小写 hex
func cryptoHmacSha256(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	key := []byte(L.CheckString(2))
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	L.Push(lua.LString(hex.EncodeToString(mac.Sum(nil))))
	return 1
}

// ---------------------------------------------------------------------------
// Go 公开接口（供其他 Go 包直接调用，不经 Lua）
// ---------------------------------------------------------------------------

// EncryptXorCarryRol Go 原生 XOR+carry+ROL 流加密。
//
// 对 data[offset:] 加密，返回加密后的完整字节切片和 BCC 校验字节。
// key 不合法（非 32 字节）或 data 为空时返回 (原数据, 0)。
// 供单元测试和声明式动作执行器直接调用，避免绕道 Lua。
func EncryptXorCarryRol(data, key []byte, offset int, rolBits uint) ([]byte, byte) {
	if len(key) != 32 || len(data) == 0 {
		return data, 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	bcc := computeBcc(data[offset:])
	out := make([]byte, len(data))
	copy(out, data)
	encryptXorCarryRol(out[offset:], key, rolBits)
	return out, bcc
}

// DecryptXorCarryRol Go 原生 XOR+carry+ROL 流解密。
//
// 对 data[offset:] 解密，返回解密后的完整字节切片。
// key 不合法或 data 为空时返回原数据。
func DecryptXorCarryRol(data, key []byte, offset int, rolBits uint) []byte {
	if len(key) != 32 || len(data) == 0 {
		return data
	}
	if offset > len(data) {
		offset = len(data)
	}
	out := make([]byte, len(data))
	copy(out, data)
	decryptXorCarryRol(out[offset:], key, rolBits)
	return out
}

// 确保 math 包被引用（文件内可能需要 math 常量时无需再改 import）。
var _ = math.Pi
