// Package codec — cipher 算法实现。
//
// 每个 cipher 在外层做 offset 适配（前 offset 字节明文保留 + 对 data[offset:] 做核心变换），
// 核心变换（encryptXorCarryRol 等）为各算法的加密/解密本体。
//
// 注册的加密算法：xor、xor_carry_rol、rc4、aes_cbc、aes_ctr、aes_ecb、xxtea。
//
// 包内约定（与 brief §cipher 的 offset 语义一致）：
//   - Encrypt/Decrypt(data, key, offset, params) → out, err
//   - data[:offset] 必须原样保留到 out 中；只变换 data[offset:]；len(out)==len(data)。
//   - offset 越界（> len(data)）钳位到 len(data)；负值视为 0。
package codec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rc4"
	"encoding/binary"
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// 通用工具
// ---------------------------------------------------------------------------

// rol8 对单个字节做循环左移（rotate left）。
// 等价于 Lua 中的 bit.bor(bit.band(bit.lshift(x, n), 0xFF), bit.rshift(x, 8-n))。
func rol8(b byte, n uint) byte { return (b << n) | (b >> (8 - n)) }

// ror8 对单个字节做循环右移（rotate right），是 rol8 的逆运算。
func ror8(b byte, n uint) byte { return (b >> n) | (b << (8 - n)) }

// clampOffset 把 offset 限制到 [0, len(data)]；负值视为 0，越界钳到 len(data)。
func clampOffset(offset, dataLen int) int {
	if offset < 0 {
		return 0
	}
	if offset > dataLen {
		return dataLen
	}
	return offset
}

// ---------------------------------------------------------------------------
// 核心变换（不引入 offset）
// ---------------------------------------------------------------------------

// encryptXorCarryRol XOR + carry + ROL8 流加密。
//
// 对每个字节：x = data[i] ^ key[i%32]; x += carry; x = ROL8(x, rolBits); data[i]=x; carry=x。
// 注意：此函数假设 key 长度 == 32（由调用方 EncryptXorCarryRol 校验后保证）。
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
// 逆运算：x = ROR8(enc, rolBits); x -= carry; x ^= key[i%32]; data[i]=x; carry=enc。
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

// encryptXor 纯 XOR 流加密：data[i] ^= key[i % len(key)]（自反）。
func encryptXor(data, key []byte) {
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
}

// errInvalidPadding PKCS#7 填充校验失败时返回的错误。
var errInvalidPadding = errors.New("invalid pkcs7 padding")

// pkcs7Pad PKCS#7 填充。块对齐时额外填充一个完整块。
func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	pad := make([]byte, n)
	for i := range pad {
		pad[i] = byte(n)
	}
	return append(data, pad...)
}

// pkcs7Unpad 移除 PKCS#7 填充。
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

// xxteaDelta XXTEA 黄金比例增量常量。
const xxteaDelta uint32 = 0x9E3779B9

// xxteaKeyLen XXTEA 密钥要求的字节数（4 个小端 uint32）。
const xxteaKeyLen = 16

// encryptXXTEA XXTEA 加密。
func encryptXXTEA(data []byte, key [4]uint32) []byte {
	n := len(data) / 4
	if n < 2 {
		return data
	}
	v := bytesToUint32s(data, n)
	rounds := 6 + 52/n
	sum := uint32(0)
	for range rounds {
		sum += xxteaDelta
		e := (sum >> 2) & 3
		for p := range n {
			left := v[(p+n-1)%n]
			right := v[(p+1)%n]
			z := ((right>>5 ^ left<<2) + (left>>3 ^ right<<4)) ^ ((sum ^ right) + (key[uint32(p)&3^e] ^ left))
			v[p] += z
		}
	}
	return uint32sToBytes(v)
}

// decryptXXTEA XXTEA 解密。
func decryptXXTEA(data []byte, key [4]uint32) []byte {
	n := len(data) / 4
	if n < 2 {
		return data
	}
	v := bytesToUint32s(data, n)
	rounds := 6 + 52/n
	sum := uint32(rounds) * xxteaDelta
	for range rounds {
		e := (sum >> 2) & 3
		for p := n - 1; ; p-- {
			right := v[(p+1)%n]
			left := v[(p+n-1)%n]
			z := ((right>>5 ^ left<<2) + (left>>3 ^ right<<4)) ^ ((sum ^ right) + (key[uint32(p)&3^e] ^ left))
			v[p] -= z
			if p == 0 {
				break
			}
		}
		sum -= xxteaDelta
	}
	return uint32sToBytes(v)
}

func bytesToUint32s(data []byte, n int) []uint32 {
	v := make([]uint32, n)
	for i := range n {
		v[i] = binary.LittleEndian.Uint32(data[i*4:])
	}
	return v
}

func uint32sToBytes(v []uint32) []byte {
	out := make([]byte, len(v)*4)
	for i, u := range v {
		binary.LittleEndian.PutUint32(out[i*4:], u)
	}
	return out
}

// ---------------------------------------------------------------------------
// Cipher 接口实现
// ---------------------------------------------------------------------------

// noneCipher 直通：前缀明文 + data[offset:] 原样返回，len(out)==len(data)。
type noneCipher struct{}

func (noneCipher) Encrypt(data, _ []byte, _ int, _ map[string]any) ([]byte, error) {
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (noneCipher) Decrypt(data, _ []byte, _ int, _ map[string]any) ([]byte, error) {
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (noneCipher) DecryptInPlace(_ []byte, _ []byte, _ int, _ map[string]any) error {
	return nil // 直通：原地即恒等
}

// xorCipher 纯 XOR 流（key 任意长度，循环异或）。
type xorCipher struct{}

func (xorCipher) apply(data, key []byte, offset int) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	if len(key) == 0 || len(data) == 0 {
		return out
	}
	off := clampOffset(offset, len(data))
	encryptXor(out[off:], key)
	return out
}

func (c xorCipher) Encrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	return c.apply(data, key, offset), nil
}

func (c xorCipher) Decrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	return c.apply(data, key, offset), nil
}

func (c xorCipher) DecryptInPlace(data, key []byte, offset int, _ map[string]any) error {
	if len(key) == 0 || len(data) == 0 {
		return nil
	}
	off := clampOffset(offset, len(data))
	encryptXor(data[off:], key)
	return nil
}

// xorCarryRolCipher XOR+carry+ROL8（现协议）。key 必须 32 字节，否则返回原数据。
// params["rol"]（int）缺省 3；范围 [1,7]，越界钳位。
type xorCarryRolCipher struct{}

func (xorCarryRolCipher) rolFromParams(params map[string]any) uint {
	bits := uint(3)
	if v, ok := params["rol"]; ok {
		switch n := v.(type) {
		case int:
			bits = uint(n)
		case int64:
			bits = uint(n)
		case float64:
			bits = uint(n)
		case uint:
			bits = n
		}
	}
	if bits < 1 {
		bits = 1
	}
	if bits > 7 {
		bits = 7
	}
	return bits
}

func (c xorCarryRolCipher) Encrypt(data, key []byte, offset int, params map[string]any) ([]byte, error) {
	// key 非 32 字节或 data 为空时返回原数据副本。
	if len(key) != 32 || len(data) == 0 {
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	}
	off := clampOffset(offset, len(data))
	rolBits := c.rolFromParams(params)
	out := make([]byte, len(data))
	copy(out, data)
	encryptXorCarryRol(out[off:], key, rolBits)
	return out, nil
}

func (c xorCarryRolCipher) Decrypt(data, key []byte, offset int, params map[string]any) ([]byte, error) {
	if len(key) != 32 || len(data) == 0 {
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	}
	off := clampOffset(offset, len(data))
	rolBits := c.rolFromParams(params)
	out := make([]byte, len(data))
	copy(out, data)
	decryptXorCarryRol(out[off:], key, rolBits)
	return out, nil
}

func (c xorCarryRolCipher) DecryptInPlace(data, key []byte, offset int, params map[string]any) error {
	if len(key) != 32 || len(data) == 0 {
		return nil // 与 Decrypt 的"key 非 32 字节原样返回"一致：不动 data
	}
	off := clampOffset(offset, len(data))
	decryptXorCarryRol(data[off:], key, c.rolFromParams(params))
	return nil
}

// rc4Cipher RC4 流密码。RC4 自反，encrypt==decrypt，key 长度 1~256。
// RC4 仅用于兼容既有游戏协议，禁止用于新的安全设计。
type rc4Cipher struct{}

func (rc4Cipher) apply(data, key []byte, offset int) ([]byte, error) {
	out := make([]byte, len(data))
	copy(out, data)
	if len(key) == 0 || len(data) == 0 {
		return out, nil
	}
	stream, err := rc4.NewCipher(key)
	if err != nil {
		return out, err
	}
	off := clampOffset(offset, len(data))
	stream.XORKeyStream(out[off:], out[off:])
	return out, nil
}

func (c rc4Cipher) Encrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	return c.apply(data, key, offset)
}

func (c rc4Cipher) Decrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	return c.apply(data, key, offset)
}

func (c rc4Cipher) DecryptInPlace(data, key []byte, offset int, _ map[string]any) error {
	if len(key) == 0 || len(data) == 0 {
		return nil
	}
	stream, err := rc4.NewCipher(key)
	if err != nil {
		return err
	}
	off := clampOffset(offset, len(data))
	stream.XORKeyStream(data[off:], data[off:])
	return nil
}

// aesEcbCipher AES-ECB + PKCS#7。params 无；key 16/24/32。
//
// 注意：AES 是块密码，密文长度通常 > 明文（PKCS#7 填充）。
// 本接口要求 len(out)==len(data) 与块密码语义冲突；这里的处理是：
//   - Encrypt 对 data[offset:] 整段做 PKCS#7+ECB，输出长度 = padding 后的块对齐长度。
//   - 返回 out = data[:offset] + 加密后的尾部，因此 len(out) ≠ len(data)（这是块密码固有特性）。
//   - 与 cipher offset 语义的协调由 engine 处理（块密码步骤通常不与流密码混用 offset）。
//   - Decrypt 对 data[offset:] 做反向；len(out) ≤ len(data)（去填充后）。
type aesEcbCipher struct{}

func (aesEcbCipher) Encrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	off := clampOffset(offset, len(data))
	padded := pkcs7Pad(data[off:], aes.BlockSize)
	out := make([]byte, off+len(padded))
	copy(out, data[:off])
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[off+i:], padded[i:])
	}
	return out, nil
}

func (aesEcbCipher) Decrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	off := clampOffset(offset, len(data))
	ct := data[off:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a multiple of block size")
	}
	plain := make([]byte, len(ct))
	for i := 0; i < len(ct); i += aes.BlockSize {
		block.Decrypt(plain[i:], ct[i:])
	}
	unpadded, err := pkcs7Unpad(plain)
	if err != nil {
		return nil, err
	}
	out := make([]byte, off+len(unpadded))
	copy(out, data[:off])
	copy(out[off:], unpadded)
	return out, nil
}

// aesCbcCipher AES-CBC + PKCS#7。params["iv"]（[]byte 或 string）必需，16 字节。
type aesCbcCipher struct{}

func (aesCbcCipher) iv(params map[string]any) ([]byte, error) {
	v, ok := params["iv"]
	if !ok {
		return nil, errors.New("aes_cbc 缺少参数 iv")
	}
	var iv []byte
	switch t := v.(type) {
	case []byte:
		iv = t
	case string:
		iv = []byte(t)
	default:
		return nil, errors.New("aes_cbc 参数 iv 类型非法（需 bytes 或 string）")
	}
	// cipher.NewCBCEncrypter/Decrypter 对非 blockSize 的 IV 直接 panic，这里提前转成可控错误。
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("aes_cbc iv 长度非法：需 %d 字节，实际 %d", aes.BlockSize, len(iv))
	}
	return iv, nil
}

func (c aesCbcCipher) Encrypt(data, key []byte, offset int, params map[string]any) ([]byte, error) {
	iv, err := c.iv(params)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	off := clampOffset(offset, len(data))
	padded := pkcs7Pad(data[off:], aes.BlockSize)
	out := make([]byte, off+len(padded))
	copy(out, data[:off])
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(out[off:], padded)
	return out, nil
}

func (c aesCbcCipher) Decrypt(data, key []byte, offset int, params map[string]any) ([]byte, error) {
	iv, err := c.iv(params)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	off := clampOffset(offset, len(data))
	ct := data[off:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a multiple of block size")
	}
	plain := make([]byte, len(ct))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plain, ct)
	unpadded, err := pkcs7Unpad(plain)
	if err != nil {
		return nil, err
	}
	out := make([]byte, off+len(unpadded))
	copy(out, data[:off])
	copy(out[off:], unpadded)
	return out, nil
}

// aesCtrCipher AES-CTR（流模式，无填充）。params["iv"]（[]byte 或 string）必需，16 字节。
//
// CTR 是流密码语义：len(out)==len(data) 自然满足（仅 data[offset:] 变换）。
// 用 stdlib crypto/cipher.NewCTR 实现（不引第三方依赖）。
type aesCtrCipher struct{}

func (aesCtrCipher) iv(params map[string]any) ([]byte, error) {
	v, ok := params["iv"]
	if !ok {
		return nil, errors.New("aes_ctr 缺少参数 iv")
	}
	var iv []byte
	switch t := v.(type) {
	case []byte:
		iv = t
	case string:
		iv = []byte(t)
	default:
		return nil, errors.New("aes_ctr 参数 iv 类型非法（需 bytes 或 string）")
	}
	// cipher.NewCTR 对非 blockSize 的 IV 直接 panic，这里提前转成可控错误。
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("aes_ctr iv 长度非法：需 %d 字节，实际 %d", aes.BlockSize, len(iv))
	}
	return iv, nil
}

func (c aesCtrCipher) Encrypt(data, key []byte, offset int, params map[string]any) ([]byte, error) {
	iv, err := c.iv(params)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	off := clampOffset(offset, len(data))
	out := make([]byte, len(data))
	copy(out, data)
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(out[off:], data[off:])
	return out, nil
}

func (c aesCtrCipher) Decrypt(data, key []byte, offset int, params map[string]any) ([]byte, error) {
	// CTR 解密与加密完全对称（同一 keystream XOR）。
	return c.Encrypt(data, key, offset, params)
}

// xxteaCipher XXTEA 块密码。key 16 字节（4 个小端 uint32）。
//
// 与 AES 类似，XXTEA 要求数据 4 字节对齐且 ≥8 字节；不足时 encrypt 侧补零到 4 对齐，
// 输出长度 = 对齐后长度；解密侧原样返回（去对齐由上层处理）。
type xxteaCipher struct{}

func (xxteaCipher) keyWords(key []byte) [4]uint32 {
	var k [4]uint32
	for i := 0; i < 4 && i*4 < len(key); i++ {
		k[i] = binary.LittleEndian.Uint32(key[i*4:])
	}
	return k
}

func (c xxteaCipher) Encrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	// keyWords 对不足 16 字节的 key 会静默零填充 → 加解密结果错误却不报错。
	// 显式校验，把"静默错密钥"变成可定位的错误。
	if len(key) < xxteaKeyLen {
		return nil, fmt.Errorf("xxtea key 长度非法：需 %d 字节，实际 %d", xxteaKeyLen, len(key))
	}
	off := clampOffset(offset, len(data))
	body := data[off:]
	// 补齐到 4 字节对齐。
	if rem := len(body) % 4; rem != 0 {
		padded := make([]byte, len(body)+4-rem)
		copy(padded, body)
		body = padded
	}
	k := c.keyWords(key)
	enc := encryptXXTEA(body, k)
	out := make([]byte, off+len(enc))
	copy(out, data[:off])
	copy(out[off:], enc)
	return out, nil
}

func (c xxteaCipher) Decrypt(data, key []byte, offset int, _ map[string]any) ([]byte, error) {
	if len(key) < xxteaKeyLen {
		return nil, fmt.Errorf("xxtea key 长度非法：需 %d 字节，实际 %d", xxteaKeyLen, len(key))
	}
	off := clampOffset(offset, len(data))
	body := data[off:]
	if len(body)%4 != 0 || len(body) < 8 {
		return nil, errors.New("invalid xxtea data length")
	}
	k := c.keyWords(key)
	dec := decryptXXTEA(body, k)
	out := make([]byte, off+len(dec))
	copy(out, data[:off])
	copy(out[off:], dec)
	return out, nil
}

// ---------------------------------------------------------------------------
// 注册（包 init 阶段完成；运行期只读）
// ---------------------------------------------------------------------------

func init() {
	RegisterCipher("none", noneCipher{}, AlgoMeta{
		Description: "直通（不加密）；前 offset 字节明文 + data[offset:] 原样返回",
	})

	RegisterCipher("xor", xorCipher{}, AlgoMeta{
		Description: "循环 key XOR 流密码（自反，encrypt==decrypt）",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "密钥，任意长度，循环异或"},
		},
	})

	RegisterCipher("xor_carry_rol", xorCarryRolCipher{}, AlgoMeta{
		Description: "XOR+carry+ROL8 流密码（现协议 NetEncrypt）；key 必须 32 字节，否则返回原数据",
		Params: []AlgoParam{
			{Name: "rol", Type: "int", Default: 3, Description: "ROL8 旋转位数，范围 [1,7]，缺省 3，越界钳位"},
			{Name: "key", Type: "bytes", Description: "32 字节密钥"},
		},
	})

	RegisterCipher("rc4", rc4Cipher{}, AlgoMeta{
		Description: "RC4 流密码（自反，encrypt==decrypt）；key 长度 1~256",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "1~256 字节密钥"},
		},
	})

	RegisterCipher("aes_ecb", aesEcbCipher{}, AlgoMeta{
		Description: "AES-ECB + PKCS#7 自动填充；块密码，密文长度含填充",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "16/24/32 字节密钥（AES-128/192/256）"},
		},
	})

	RegisterCipher("aes_cbc", aesCbcCipher{}, AlgoMeta{
		Description: "AES-CBC + PKCS#7 自动填充；需 IV",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "16/24/32 字节密钥"},
			{Name: "iv", Type: "bytes", Description: "16 字节初始化向量"},
		},
	})

	RegisterCipher("aes_ctr", aesCtrCipher{}, AlgoMeta{
		Description: "AES-CTR 流模式（无填充，len(out)==len(data)）；需 IV（nonce）",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "16/24/32 字节密钥"},
			{Name: "iv", Type: "bytes", Description: "16 字节 nonce/计数器"},
		},
	})

	RegisterCipher("xxtea", xxteaCipher{}, AlgoMeta{
		Description: "XXTEA 轻量块密码；encrypt 自动补齐 4 字节对齐",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "16 字节密钥（4 个小端 uint32）"},
		},
	})
}
