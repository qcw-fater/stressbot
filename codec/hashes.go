// Package codec — hash/HMAC 算法实现（迁移自 adapter/lua_crypto.go + 标准库）。
//
// 接口语义（与 brief 逐字）：Hasher.Hash(data, key, params) []byte；
//   - key 为空 → 走原始哈希；
//   - key 非空 → 走 HMAC 变体（crypto/hmac）。
//
// 返回原始摘要字节（非 hex）——比 hex 字符串更通用；T1.4 engine 在写入 bytes 字段时
// 可按需 hex/base64 编码。lua_crypto.go 的 Lua 入口返回 hex 字符串，那是对 Lua 友好的
// 一层包装；本层是更底层的 Go 接口，返回原始摘要。
//
// 迁移来源行号：
//   - md5/sha1/sha256: lua_crypto.go:790/798/806
//   - hmac_md5/hmac_sha256: lua_crypto.go:818/828
//   - 通用化：所有 hash 都支持 key 非空走 HMAC（lua_crypto 只有 md5/sha256 的 HMAC
//     入口，本层补齐 sha1 的 HMAC，逻辑同构）。
package codec

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"hash"
)

// md5Hasher MD5（key 非空走 HMAC-MD5）。
type md5Hasher struct{}

func (md5Hasher) newHash(key []byte) hash.Hash {
	if len(key) > 0 {
		return hmac.New(md5.New, key)
	}
	return md5.New()
}

func (h md5Hasher) Hash(data, key []byte, params map[string]any) []byte {
	hh := h.newHash(key)
	hh.Write(data)
	return hh.Sum(nil)
}

// sha1Hasher SHA-1（key 非空走 HMAC-SHA1）。
type sha1Hasher struct{}

func (sha1Hasher) newHash(key []byte) hash.Hash {
	if len(key) > 0 {
		return hmac.New(sha1.New, key)
	}
	return sha1.New()
}

func (h sha1Hasher) Hash(data, key []byte, params map[string]any) []byte {
	hh := h.newHash(key)
	hh.Write(data)
	return hh.Sum(nil)
}

// sha256Hasher SHA-256（key 非空走 HMAC-SHA256）。
type sha256Hasher struct{}

func (sha256Hasher) newHash(key []byte) hash.Hash {
	if len(key) > 0 {
		return hmac.New(sha256.New, key)
	}
	return sha256.New()
}

func (h sha256Hasher) Hash(data, key []byte, params map[string]any) []byte {
	hh := h.newHash(key)
	hh.Write(data)
	return hh.Sum(nil)
}

func init() {
	RegisterHasher("md5", md5Hasher{}, AlgoMeta{
		Description: "MD5 哈希；key 非空走 HMAC-MD5；返回 16 字节原始摘要",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "可选；非空时走 HMAC"},
		},
	})
	RegisterHasher("sha1", sha1Hasher{}, AlgoMeta{
		Description: "SHA-1 哈希；key 非空走 HMAC-SHA1；返回 20 字节原始摘要",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "可选；非空时走 HMAC"},
		},
	})
	RegisterHasher("sha256", sha256Hasher{}, AlgoMeta{
		Description: "SHA-256 哈希；key 非空走 HMAC-SHA256；返回 32 字节原始摘要",
		Params: []AlgoParam{
			{Name: "key", Type: "bytes", Description: "可选；非空时走 HMAC"},
		},
	})
}
