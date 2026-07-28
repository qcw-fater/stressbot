package protox

import (
	"bytes"
	"container/list"
	"hash/maphash"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ── 广播去重：内容寻址的共享 Frozen 缓存 ─────────────────────────
//
// 动机：同一条服务端广播（如商城配置 SystemShopDataS2C，~400KB）会推给全部机器人，
// 旧路径每个机器人各自解码并经 Frozen 常驻一份——数千机器人 × 相同内容 = GB 级
// 重复留存（线上 2000 人实测 dynamicpb 留存 ~1.2GB，绝大部分内容彼此相同）。
//
// 方案：按 (protoName, 消息字节) 内容寻址。命中返回同一个 *Frozen——不仅留存塌缩
// 为单份，连 dynamicpb 解码本身都跳过（解码 churn 同步消失）；未命中解码后登记。
// Frozen 的不可变契约（见 frozen.go）保证共享实例可被全部机器人无锁并发读；
// 机器人只持引用，退出即释放；缓存本身有界（条目数 + 原始字节数双上界，LRU 驱逐），
// 驱逐只影响后续命中率，不影响已持有引用的机器人（GC 按引用计存活）。
//
// 碰撞防御：哈希只用作桶索引，命中判定必须 protoName 相等 + 全量 bytes.Equal，
// 结构上不存在按哈希误共享的可能。
//
// 非广播（每机器人内容不同）的消息会一直 miss：代价是每条一次哈希 + 一份原始字节
// 快照（随 LRU 很快驱逐）。接入点用 DedupMinBytes 门槛把小消息挡在缓存外。
// 接入点：Go-store 监听（robot.createListenCallback）、监听脚本解码（runListenScript）、
// Lua await_listen 结果（script.listenResultValues，共享实例以 *Frozen 包 userdata，
// proto API 只读防护见 script/api_proto.go）。大而独占的推送若未来出现，
// Stats 的命中率会直接暴露（hits≈0 且 misses 高速增长）。

const (
	// DedupMinBytes 接入方参与去重的消息体下限：小消息即使重复，留存也可忽略，
	// 不值得哈希 + 快照；大消息（广播配置类）才是重复留存的主体。
	DedupMinBytes = 1024

	// dedupMaxEntries / dedupMaxCost 缓存双上界。
	//
	// 体积上界按**解码后体积估算**计（estimateDecodedCost），不按原始字节：
	// map/repeated 重度消息（MatchSucceedS2C 携带的 PlayerGameInfo 等）的 dynamicpb
	// 解码树实测可达原始字节的 ~50 倍——按原始字节设界（旧 64MB）时缓存满载可钉住
	// GB 级解码树，其中大半是已被全部接收方消费完、纯等 LRU 驱逐的对局广播（线上
	// 020→022 剖面证实）。改按解码估算后钉住量直接有界于 dedupMaxCost。
	//
	// 命中周期长的条目（商城配置：每机器人登录都命中）被 LRU 天然保护；
	// 被挤出的主要是一次性对局广播（6 个接收方毫秒级消费完毕，无需长驻）。
	//
	// 条数上界放宽到 4096：线上 5000 人实测 256 条被独占大推送疯狂换血
	// （evictions≈misses，命中率 38%，共享广播在队友消费前就被挤出），
	// 而彼时解码体积仅 64MB——内存安全已由 dedupMaxCost 兜底，条数不应再是瓶颈。
	dedupMaxEntries = 4096
	dedupMaxCost    = 256 << 20
)

var dedupSeed = maphash.MakeSeed()

// dedupEntry 一条缓存记录。raw 是消息体的独立快照（pump 会复用网络缓冲区底层数组，
// 不能直接留存入参切片），同时充当碰撞防御的比对基准。
// cost 是解码树的常驻体积估算（estimateDecodedCost），缓存体积上界按它累计。
type dedupEntry struct {
	protoName string
	raw       []byte
	frozen    *Frozen
	cost      int
	elem      *list.Element // 在 lru 中的位置（Value 指回本 entry）
}

// FrozenCache 内容寻址的共享 Frozen 缓存。全 goroutine 安全（pump 并发调用）。
type FrozenCache struct {
	mu         sync.Mutex
	buckets    map[uint64][]*dedupEntry // hash → 碰撞链（几乎恒为单元素）
	lru        *list.List               // Front = 最近使用
	curCost    int                      // Σ entry.cost（解码体积估算）
	maxEntries int
	maxCost    int
	hits       uint64
	misses     uint64
	evictions  uint64
}

// NewFrozenCache 创建缓存。maxEntries/maxCost（解码体积估算）任一超限即从 LRU 尾部驱逐。
func NewFrozenCache(maxEntries, maxCost int) *FrozenCache {
	c := &FrozenCache{
		buckets:    make(map[uint64][]*dedupEntry),
		lru:        list.New(),
		maxEntries: maxEntries,
		maxCost:    maxCost,
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
func (c *FrozenCache) lookup(hash uint64, protoName string, data []byte) *dedupEntry {
	for _, e := range c.buckets[hash] {
		if e.protoName == protoName && bytes.Equal(e.raw, data) {
			c.lru.MoveToFront(e.elem)
			return e
		}
	}
	return nil
}

// getOrParse 命中返回共享 Frozen（跳过解码）；未命中经 f.Parse 解码后登记。
//
// 解码在锁外进行（大广播解码需数百 µs，不能阻塞其他 pump 的命中路径）。
// 两个 pump 同时 miss 同一内容时会各解码一次，插入前二次检查、以先登记者为准——
// 后到者返回已登记的共享实例，自己的解码产物弃给 GC（一次性小代价，换命中路径无等待）。
func (c *FrozenCache) getOrParse(f *Factory, protoName string, data []byte) (*Frozen, error) {
	hash := dedupHash(protoName, data)

	c.mu.Lock()
	if e := c.lookup(hash, protoName, data); e != nil {
		c.hits++
		c.mu.Unlock()
		return e.frozen, nil
	}
	c.mu.Unlock()

	msg, err := f.Parse(protoName, data)
	if err != nil {
		return nil, err
	}
	entry := &dedupEntry{
		protoName: protoName,
		raw:       append([]byte(nil), data...),
		frozen:    Freeze(msg),
		cost:      estimateDecodedCost(msg),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.lookup(hash, protoName, data); e != nil {
		c.hits++
		return e.frozen, nil
	}
	c.misses++
	entry.elem = c.lru.PushFront(entry)
	c.buckets[hash] = append(c.buckets[hash], entry)
	c.curCost += entry.cost
	for (c.lru.Len() > c.maxEntries || c.curCost > c.maxCost) && c.lru.Len() > 1 {
		c.evictOldest()
	}
	return entry.frozen, nil
}

// evictOldest 移除 LRU 尾部条目（锁内调用）。
func (c *FrozenCache) evictOldest() {
	back := c.lru.Back()
	if back == nil {
		return
	}
	victim := back.Value.(*dedupEntry)
	c.lru.Remove(back)
	c.evictions++
	c.curCost -= victim.cost
	hash := dedupHash(victim.protoName, victim.raw)
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
// CostBytes 为缓存当前钉住的解码树体积估算（estimateDecodedCost 累计）。
type DedupStats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
	Entries   int    `json:"entries"`
	CostBytes int    `json:"costBytes"`
}

// Stats 返回命中/未命中/驱逐计数与当前占用。
func (c *FrozenCache) Stats() DedupStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return DedupStats{
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
		Entries:   c.lru.Len(),
		CostBytes: c.curCost,
	}
}

// estimateDecodedCost 粗估解码后消息树的常驻字节数（数量级精度，供缓存体积上界用）。
// 只遍历已设置字段（Range），与 dynamicpb 的实际留存形态一致：每消息一个对象 +
// 已设字段槽位，string/bytes 计入内容长度，list/map/子消息递归累计。
// 常数为经验值，不追求精确——目标是让缓存上界与真实钉住量同数量级。
func estimateDecodedCost(msg proto.Message) int {
	if msg == nil {
		return 0
	}
	return estimateMessageCost(msg.ProtoReflect())
}

func estimateMessageCost(ref protoreflect.Message) int {
	cost := 128 // dynamicpb.Message 本体 + known 字段表基础开销
	ref.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		cost += 48 // 字段槽（表条目 + Value 装箱）
		switch {
		case fd.IsList():
			list := val.List()
			cost += 24 + list.Len()*16
			switch fd.Kind() {
			case protoreflect.MessageKind, protoreflect.GroupKind:
				for i := 0; i < list.Len(); i++ {
					cost += estimateMessageCost(list.Get(i).Message())
				}
			case protoreflect.StringKind:
				for i := 0; i < list.Len(); i++ {
					cost += len(list.Get(i).String())
				}
			case protoreflect.BytesKind:
				for i := 0; i < list.Len(); i++ {
					cost += len(list.Get(i).Bytes())
				}
			}
		case fd.IsMap():
			m := val.Map()
			cost += 48 + m.Len()*64
			valFd := fd.MapValue()
			if valFd.Kind() == protoreflect.MessageKind {
				m.Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
					cost += estimateMessageCost(v.Message())
					return true
				})
			}
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			cost += estimateMessageCost(val.Message())
		case fd.Kind() == protoreflect.StringKind:
			cost += len(val.String())
		case fd.Kind() == protoreflect.BytesKind:
			cost += len(val.Bytes())
		}
		return true
	})
	return cost
}

// ParseFrozenShared 内容寻址解析：与 Parse 语义等价，但相同 (name, data) 跨调用方
// 返回同一个不可变 *Frozen（见 FrozenCache 包注释）。仅适用于「解析后只读消费」的
// 场景（listen Go-store 整存/路径取值）；需要改写消息的调用方必须走 Parse。
func (f *Factory) ParseFrozenShared(name string, data []byte) (*Frozen, error) {
	return f.frozenCache.getOrParse(f, name, data)
}
