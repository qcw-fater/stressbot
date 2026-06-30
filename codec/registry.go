// Package codec — 算法注册表与元数据导出。
//
// 本文件（registry.go）只负责：
//   - 四张算法注册表（cipher / compressor / checksum / hasher）及其接口；
//   - Register* / Lookup* helper；
//   - 元数据导出（AlgoMeta / AlgoParam / Algorithms），供 HTTP 接口与前端下拉使用。
//
// 算法实现位于 ciphers.go / compressors.go / checksums.go / hashes.go，并在各自 init()
// 阶段注册到本文件的四张表。运行期注册表只读：所有注册在 init 完成，查找在热路径。
//
// 设计要点：
//   - 注册表用 map + sync.RWMutex（读多写极少；init 后只读，但 RWMutex 仍保留以允
//     许测试或未来扩展在不重启进程时增注册）。
//   - 查找缺失返回 (nil, false)——由编译层 fail loud，本层不 panic。
//   - 不 import gopher-lua；与 adapter/ 完全解耦。
package codec

import (
	"sort"
	"sync"
)

// ---------------------------------------------------------------------------
// 接口（与 brief 逐字一致）
// ---------------------------------------------------------------------------

// Cipher：offset 为明文前缀长度——前 offset 字节保持明文，仅处理 data[offset:]。
// 返回值长度必须等于 len(data)（前缀原样 + 处理后的尾部拼接）。
type Cipher interface {
	Encrypt(data, key []byte, offset int, params map[string]any) (out []byte, err error)
	Decrypt(data, key []byte, offset int, params map[string]any) (out []byte, err error)
}

// Compressor 无损压缩；Compress/Decompress 须可往返（round-trip）。
type Compressor interface {
	Compress(data []byte) ([]byte, error)
	Decompress(data []byte) ([]byte, error)
}

// Checksum：Sum 对给定区域算校验值（最多 8 字节语义；调用方按目标字段 size
// 截取/对齐）。
type Checksum interface {
	Sum(data []byte, params map[string]any) uint64
}

// Hasher：key 非空走 HMAC 变体。
type Hasher interface {
	Hash(data, key []byte, params map[string]any) []byte
}

// ---------------------------------------------------------------------------
// 注册表
// ---------------------------------------------------------------------------

type cipherEntry struct {
	impl Cipher
	meta AlgoMeta
}
type compressorEntry struct {
	impl Compressor
	meta AlgoMeta
}
type checksumEntry struct {
	impl Checksum
	meta AlgoMeta
}
type hasherEntry struct {
	impl Hasher
	meta AlgoMeta
}

var (
	registryMu  sync.RWMutex
	ciphers     = map[string]cipherEntry{}
	compressors = map[string]compressorEntry{}
	checksums   = map[string]checksumEntry{}
	hashers     = map[string]hasherEntry{}
)

// ---------------------------------------------------------------------------
// Register*
// ---------------------------------------------------------------------------

// RegisterCipher 把 cipher 注册到表，并登记元数据。同名重复注册覆盖。
func RegisterCipher(name string, c Cipher, meta AlgoMeta) {
	registryMu.Lock()
	defer registryMu.Unlock()
	meta.Name = name
	meta.Op = "cipher"
	ciphers[name] = cipherEntry{impl: c, meta: meta}
}

// RegisterCompressor 把 compressor 注册到表，并登记元数据。同名重复注册覆盖。
func RegisterCompressor(name string, c Compressor, meta AlgoMeta) {
	registryMu.Lock()
	defer registryMu.Unlock()
	meta.Name = name
	meta.Op = "compress"
	compressors[name] = compressorEntry{impl: c, meta: meta}
}

// RegisterChecksum 把 checksum 注册到表，并登记元数据。同名重复注册覆盖。
func RegisterChecksum(name string, c Checksum, meta AlgoMeta) {
	registryMu.Lock()
	defer registryMu.Unlock()
	meta.Name = name
	meta.Op = "checksum"
	checksums[name] = checksumEntry{impl: c, meta: meta}
}

// RegisterHasher 把 hasher 注册到表，并登记元数据。同名重复注册覆盖。
func RegisterHasher(name string, h Hasher, meta AlgoMeta) {
	registryMu.Lock()
	defer registryMu.Unlock()
	meta.Name = name
	meta.Op = "hash"
	hashers[name] = hasherEntry{impl: h, meta: meta}
}

// ---------------------------------------------------------------------------
// Lookup*
// ---------------------------------------------------------------------------

// LookupCipher 按名查 cipher。缺失返回 (nil, false)。
func LookupCipher(name string) (Cipher, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := ciphers[name]
	if !ok {
		return nil, false
	}
	return e.impl, true
}

// LookupCompressor 按名查 compressor。缺失返回 (nil, false)。
func LookupCompressor(name string) (Compressor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := compressors[name]
	if !ok {
		return nil, false
	}
	return e.impl, true
}

// LookupChecksum 按名查 checksum。缺失返回 (nil, false)。
func LookupChecksum(name string) (Checksum, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := checksums[name]
	if !ok {
		return nil, false
	}
	return e.impl, true
}

// LookupHasher 按名查 hasher。缺失返回 (nil, false)。
func LookupHasher(name string) (Hasher, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := hashers[name]
	if !ok {
		return nil, false
	}
	return e.impl, true
}

// ---------------------------------------------------------------------------
// 元数据导出
// ---------------------------------------------------------------------------

// AlgoParam 描述算法的一个可配参数（供前端编辑器表单与 T4 API）。
type AlgoParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // int|string|bool|bytes
	Default     any    `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// AlgoMeta 描述一个已注册算法的元数据。
type AlgoMeta struct {
	Name        string      `json:"name"`
	Op          string      `json:"op"` // cipher|compress|checksum|hash
	Description string      `json:"description,omitempty"`
	Params      []AlgoParam `json:"params,omitempty"`
}

// Algorithms 返回所有已注册算法的元数据，按 op 分组稳定排序（名字字母序）。
// 顺序：cipher → compress → checksum → hash（与 PipelineStep op 习惯序一致）。
func Algorithms() []AlgoMeta {
	registryMu.RLock()
	defer registryMu.RUnlock()

	out := make([]AlgoMeta, 0, len(ciphers)+len(compressors)+len(checksums)+len(hashers))

	// 按 op 收集，组内按 name 字母序，组间固定顺序。
	collect := func(m map[string]AlgoMeta) {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, m[n])
		}
	}

	cipherMetas := make(map[string]AlgoMeta, len(ciphers))
	for n, e := range ciphers {
		cipherMetas[n] = e.meta
	}
	compressorMetas := make(map[string]AlgoMeta, len(compressors))
	for n, e := range compressors {
		compressorMetas[n] = e.meta
	}
	checksumMetas := make(map[string]AlgoMeta, len(checksums))
	for n, e := range checksums {
		checksumMetas[n] = e.meta
	}
	hasherMetas := make(map[string]AlgoMeta, len(hashers))
	for n, e := range hashers {
		hasherMetas[n] = e.meta
	}

	collect(cipherMetas)
	collect(compressorMetas)
	collect(checksumMetas)
	collect(hasherMetas)
	return out
}
