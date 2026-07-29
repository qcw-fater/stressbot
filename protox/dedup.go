package protox

import (
	"bytes"
	"container/list"
	"fmt"
	"hash/maphash"
	"sync"
)

// ── 广播去重：内容寻址的共享 WireValue 缓存 ─────────────────────
//
// 动机：同一条服务端广播（如商城配置、对局广播）会推给多个机器人，逐机器人各留
// 一份字节快照仍是重复留存。按 (protoName, 消息字节) 内容寻址：命中返回同一个
// *WireValue——留存塌缩为单份字节，结构校验也只做一次。
//
// wire-first 改造（M3）：缓存条目从解码态 *Frozen 换成 *WireValue（原始字节本体）。
// 旧解码态缓存的两大痛点就地消失：
//   - 体积失真：解码树实测可达原始字节 ~50 倍，按解码估算设界（estimateDecodedCost）
//     本质是给失真打补丁；wire 条目的占用就是字节数本身，上界即真实钉住量；
//   - 解码 churn：miss 时不再解码整树，只做零分配结构校验（ValidateWire）。
//
// WireValue 的不可变契约保证共享实例可被全部机器人无锁并发读；机器人只持引用，
// 退出即释放；缓存有界（条目数 + 原始字节双上界，LRU 驱逐），驱逐只影响后续命中率，
// 不影响已持有引用的机器人（GC 按引用计存活）。
//
// 碰撞防御：哈希只用作桶索引，命中判定必须 protoName 相等 + 全量 bytes.Equal，
// 结构上不存在按哈希误共享的可能。
//
// 接入点：Go-store 监听整存（robot.createListenCallback）。动作响应（请求-响应）
// 逐机器人唯一，不进缓存（历史 018→019 实测污染教训）；listen 脚本 / await_listen
// 的脚本消费改为独占瞬态解码（wire-first 后消费与留存解耦，缓存只服务留存）。

const (
	// DedupMinBytes 接入方参与去重的消息体下限：小消息即使重复，留存也可忽略，
	// 不值得哈希 + 快照；大消息（广播配置类）才是重复留存的主体。
	DedupMinBytes = 1024

	// dedupMaxEntries / dedupMaxBytes 缓存双上界。
	// wire 条目占用 = 原始字节本身（无解码放大），64MB 即真实钉住上界；
	// 条数 4096：线上 5000 人实测 256 条会被高频推送换血（evictions≈misses）。
	dedupMaxEntries = 4096
	dedupMaxBytes   = 64 << 20
)

var dedupSeed = maphash.MakeSeed()

// dedupEntry 一条缓存记录。wire.Raw() 是消息体的独立快照（pump 会复用网络缓冲区
// 底层数组，不能直接留存入参切片），同时充当碰撞防御的比对基准。
type dedupEntry struct {
	protoName string
	wire      *WireValue
	elem      *list.Element // 在 lru 中的位置（Value 指回本 entry）
}

// WireCache 内容寻址的共享 WireValue 缓存。全 goroutine 安全（pump 并发调用）。
type WireCache struct {
	mu         sync.Mutex
	buckets    map[uint64][]*dedupEntry // hash → 碰撞链（几乎恒为单元素）
	lru        *list.List               // Front = 最近使用
	curBytes   int                      // Σ len(entry.wire.Raw())
	maxEntries int
	maxBytes   int
	hits       uint64
	misses     uint64
	evictions  uint64
}

// NewWireCache 创建缓存。maxEntries/maxBytes 任一超限即从 LRU 尾部驱逐。
func NewWireCache(maxEntries, maxBytes int) *WireCache {
	c := &WireCache{
		buckets:    make(map[uint64][]*dedupEntry),
		lru:        list.New(),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
	}
	registerCacheForStats(c)
	return c
}

func dedupHash(protoName string, data []byte) uint64 {
	var h maphash.Hash
	h.SetSeed(dedupSeed)
	_, _ = h.WriteString(protoName)
	_, _ = h.Write(data)
	return h.Sum64()
}

// lookup 在锁内查找命中项并前移 LRU。
func (c *WireCache) lookup(hash uint64, protoName string, data []byte) *dedupEntry {
	for _, e := range c.buckets[hash] {
		if e.protoName == protoName && bytes.Equal(e.wire.Raw(), data) {
			c.lru.MoveToFront(e.elem)
			return e
		}
	}
	return nil
}

// getShared 命中返回共享 WireValue（跳过校验与快照）；未命中结构校验 + 快照后登记。
//
// 校验/快照在锁外进行（不阻塞其他 pump 的命中路径）。两个 pump 同时 miss 同一内容
// 时会各校验一次，插入前二次检查、以先登记者为准。
func (f *Factory) wireShared(protoName string, data []byte) (*WireValue, error) {
	md, ok := f.registry.Lookup(protoName)
	if !ok {
		return nil, fmt.Errorf("未找到消息类型: %s", protoName)
	}
	c := f.wireCache
	hash := dedupHash(protoName, data)

	c.mu.Lock()
	if e := c.lookup(hash, protoName, data); e != nil {
		c.hits++
		c.mu.Unlock()
		return e.wire, nil
	}
	c.mu.Unlock()

	snapshot := WireSnapshot(data)
	if err := ValidateWire(md, snapshot); err != nil {
		return nil, fmt.Errorf("校验 %s wire 失败: %w", protoName, err)
	}
	entry := &dedupEntry{
		protoName: protoName,
		wire:      NewWireValue(md, snapshot),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.lookup(hash, protoName, data); e != nil {
		c.hits++
		return e.wire, nil
	}
	c.misses++
	entry.elem = c.lru.PushFront(entry)
	c.buckets[hash] = append(c.buckets[hash], entry)
	c.curBytes += len(snapshot)
	for (c.lru.Len() > c.maxEntries || c.curBytes > c.maxBytes) && c.lru.Len() > 1 {
		c.evictOldest()
	}
	return entry.wire, nil
}

// evictOldest 移除 LRU 尾部条目（锁内调用）。
func (c *WireCache) evictOldest() {
	back := c.lru.Back()
	if back == nil {
		return
	}
	victim := back.Value.(*dedupEntry)
	c.lru.Remove(back)
	c.evictions++
	c.curBytes -= len(victim.wire.Raw())
	hash := dedupHash(victim.protoName, victim.wire.Raw())
	bucket := c.buckets[hash]
	for i, e := range bucket {
		if e == victim {
			bucket[i] = bucket[len(bucket)-1]
			bucket = bucket[:len(bucket)-1]
			break
		}
	}
	if len(bucket) == 0 {
		delete(c.buckets, hash)
	} else {
		c.buckets[hash] = bucket
	}
}

// DedupStats 一次快照（观测去重是否生效、是否被独占推送污染）。
// RawBytes 为缓存当前钉住的原始字节总量（即真实内存占用，wire 条目无解码放大）。
type DedupStats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
	Entries   int    `json:"entries"`
	RawBytes  int    `json:"rawBytes"`
}

// purge 清空全部条目（Factory.Close 调用）。已被机器人持有的 *WireValue 引用
// 不受影响（GC 按引用计存活）；purge 后缓存仍可安全使用，只是从零开始。
func (c *WireCache) purge() {
	c.mu.Lock()
	c.buckets = make(map[uint64][]*dedupEntry)
	c.lru = list.New()
	c.curBytes = 0
	c.mu.Unlock()
}

// Stats 返回命中/未命中/驱逐计数与当前占用。
func (c *WireCache) Stats() DedupStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return DedupStats{
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
		Entries:   c.lru.Len(),
		RawBytes:  c.curBytes,
	}
}

// WireShared 内容寻址的共享 wire 值：相同 (name, data) 跨调用方返回同一个不可变
// *WireValue（见 WireCache 包注释）。data 会被独立快照，调用方无需复制。
// 仅适用于「留存只读」的场景（listen Go-store 整存/路径取值）。
func (f *Factory) WireShared(name string, data []byte) (*WireValue, error) {
	return f.wireShared(name, data)
}
