// Package codec — hash/HMAC 算法实现。
//
// 接口语义：Hasher.Hash(data, key, params) []byte；
//   - key 为空 → 走原始哈希；
//   - key 非空 → 走 HMAC 变体（crypto/hmac）。
//
// 返回原始摘要字节（非 hex）——比 hex 字符串更通用；engine 在写入 bytes 字段时
// 可按需 hex/base64 编码。
//
// 注册的哈希算法：md5、sha1、sha256（key 非空时自动走对应 HMAC）。
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

func (h md5Hasher) Hash(data, key []byte, _ map[string]any) []byte {
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

func (h sha1Hasher) Hash(data, key []byte, _ map[string]any) []byte {
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

func (h sha256Hasher) Hash(data, key []byte, _ map[string]any) []byte {
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
