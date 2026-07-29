package protox

import (
	"bytes"
	"container/list"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ── 广播消费去重：内容寻址的共享解码（*Frozen）缓存 ─────────────────
//
// 与 WireCache（dedup.go，服务「留存」：state 里存的是共享字节）互补，本缓存服务
// 「脚本消费」：listen 脚本 / await_tcp_listen 拿到的消息要经 proto API 读字段，
// 需要解码态。同一条广播（如同场战斗 60 人的帧数据、全服商城配置）逐机器人独占
// 解码曾被线上剖面证实是双重灾难，wire-first 首版把这里改成独占瞬态解码后复发
// （029→031 剖面：区间总分配 3.1TB 其中 ~1.3TB 是 dynamicpb churn；live 净增
// 1.15GB 全部来自本路径）：
//   - churn：60 接收方 × 每帧解码 = 分配速率放大 60 倍，GC 压力与浮动垃圾暴涨；
//   - 钉扎：帧循环脚本挂起在 await_tcp_listen 时，协程局部变量钉着「自己那份」
//     解码树——5000 机器人陆续进战斗，每人钉一棵 = 单调增长。共享后 60 人钉同
//     一棵，摊薄 60 倍。
//
// 命中返回同一个不可变 *Frozen（见 frozen.go 的不可变契约），脚本侧以只读
// userdata 包装（wrapFrozenMessage，set_field fail-loud）。
//
// 体积上界按**解码后体积估算**计（estimateDecodedCost）：本缓存钉住的是解码树，
// map/repeated 重度消息的 dynamicpb 树可达原始字节 ~50 倍，按原始字节设界会失真
// 钉住 GB 级（线上 020→022 剖面证实）。这与 WireCache 按原始字节设界不矛盾——
// 两个缓存钉的东西不同。
//
// 碰撞防御：哈希只用作桶索引，命中判定必须 protoName 相等 + 全量 bytes.Equal。

const (
	// frozenMaxEntries / frozenMaxCost 解码缓存双上界。
	// 条数 4096：线上 5000 人实测 256 条被高频推送换血（evictions≈misses）；
	// 体积按解码树估算累计，256MB 是真实钉住上界。
	frozenMaxEntries = 4096
	frozenMaxCost    = 256 << 20
)

// frozenEntry 一条解码缓存记录。raw 是消息体的独立快照（pump 复用网络缓冲区
// 底层数组，不能留存入参切片），同时充当碰撞防御的比对基准。
type frozenEntry struct {
	protoName string
	raw       []byte
	frozen    *Frozen
	cost      int
	elem      *list.Element
}

// FrozenCache 内容寻址的共享解码缓存。全 goroutine 安全（pump 并发调用）。
type FrozenCache struct {
	mu         sync.Mutex
	buckets    map[uint64][]*frozenEntry
	lru        *list.List // Front = 最近使用
	curCost    int        // Σ entry.cost（解码体积估算）
	maxEntries int
	maxCost    int
	hits       uint64
	misses     uint64
	evictions  uint64
}

// NewFrozenCache 创建缓存。maxEntries/maxCost（解码体积估算）任一超限即从 LRU 尾部驱逐。
func NewFrozenCache(maxEntries, maxCost int) *FrozenCache {
	c := &FrozenCache{
		buckets:    make(map[uint64][]*frozenEntry),
		lru:        list.New(),
		maxEntries: maxEntries,
		maxCost:    maxCost,
	}
	registerFrozenCacheForStats(c)
	return c
}

// lookup 在锁内查找命中项并前移 LRU。
func (c *FrozenCache) lookup(hash uint64, protoName string, data []byte) *frozenEntry {
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
// 解码在锁外进行（大广播解码需数百 μs，不能阻塞其他 pump 的命中路径）。
// 两个 pump 同时 miss 同一内容时会各解码一次，插入前二次检查、以先登记者为准。
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
	entry := &frozenEntry{
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
// 驱逐只影响后续命中率，不影响已持有引用的机器人（GC 按引用计存活）。
func (c *FrozenCache) evictOldest() {
	back := c.lru.Back()
	if back == nil {
		return
	}
	victim := back.Value.(*frozenEntry)
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

// FrozenDedupStats 解码缓存一次快照。CostBytes 为当前钉住的解码树体积估算累计。
type FrozenDedupStats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
	Entries   int    `json:"entries"`
	CostBytes int    `json:"costBytes"`
}

// Stats 返回命中/未命中/驱逐计数与当前占用。
func (c *FrozenCache) Stats() FrozenDedupStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return FrozenDedupStats{
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
			l := val.List()
			cost += 24 + l.Len()*16
			switch fd.Kind() {
			case protoreflect.MessageKind, protoreflect.GroupKind:
				for i := 0; i < l.Len(); i++ {
					cost += estimateMessageCost(l.Get(i).Message())
				}
			case protoreflect.StringKind:
				for i := 0; i < l.Len(); i++ {
					cost += len(l.Get(i).String())
				}
			case protoreflect.BytesKind:
				for i := 0; i < l.Len(); i++ {
					cost += len(l.Get(i).Bytes())
				}
			}
		case fd.IsMap():
			m := val.Map()
			cost += 48 + m.Len()*64
			if fd.MapValue().Kind() == protoreflect.MessageKind {
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
// 场景（listen 脚本 / await_listen 结果）；需要改写消息的调用方必须走 Parse。
func (f *Factory) ParseFrozenShared(name string, data []byte) (*Frozen, error) {
	return f.frozenCache.getOrParse(f, name, data)
}
