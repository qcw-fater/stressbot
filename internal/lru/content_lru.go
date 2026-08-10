// Package lru 提供内容寻址的双上界 LRU 存储引擎，供 codec/protox 三个去重缓存共享。
//
// 本包只封装"存储 + LRU 驱逐 + 哈希桶查找"的机械逻辑，不关心 key 的语义：
//   - K 是 key 的完整类型（[]byte / struct{name,data} 等），equal 由调用方通过闭包注入；
//   - hash 由调用方算好传入（各缓存的 hash 函数不同，引擎不内置）；
//   - cost（字节数或解码体积估算）由调用方算好传入。
//
// 并发安全：单 mutex，与原三个缓存的各自手写实现一致。
// 锁外重活（校验/解码）由调用方在 Lookup miss 与 Insert 之间自行负责，
// 引擎提供两个锁内原语，double-check 由 Insert 内部的 equalFn "key 已存在则跳过"保证。
//
// 热路径零分配：K 作为值类型参数传递（栈拷贝），不构造拼接 key、不 make。
package lru

import (
	"container/list"
	"sync"
)

// ContentLRU 内容寻址的双上界 LRU。Front = 最近使用。
type ContentLRU[K any, V any] struct {
	mu         sync.Mutex
	buckets    map[uint64][]*entry[K, V] // hash → 碰撞链（几乎恒为单元素）
	lru        *list.List                // Front = 最近使用；Value = *entry[K,V]
	curCost    int                       // Σ entry.cost
	maxEntries int
	maxCost    int
	hits       uint64
	misses     uint64
	evictions  uint64
	equalFn    func(K, K) bool // 构造时注入一次，热路径只调用
}

// entry 内部记录。
type entry[K any, V any] struct {
	key   K
	hash  uint64
	value V
	cost  int
	elem  *list.Element // 在 lru 中的位置（Value 指回本 entry）
}

// New 创建双上界内容寻址 LRU。equalFn 由调用方注入（如 bytes.Equal）。
func New[K any, V any](maxEntries, maxCost int, equalFn func(K, K) bool) *ContentLRU[K, V] {
	return &ContentLRU[K, V]{
		buckets:    make(map[uint64][]*entry[K, V]),
		lru:        list.New(),
		maxEntries: maxEntries,
		maxCost:    maxCost,
		equalFn:    equalFn,
	}
}

// Lookup 锁内查找：命中返回 value 并前移 LRU、计一次 hit；未命中计一次 miss 并返回零值。
// hash 由调用方算好传入（引擎不内置 hash 函数，Lookup 和 Insert 复用同一 hash 避免重复计算）。
// 一次锁完成查找 + 计数，避免 miss 路径的二次锁开销。
func (c *ContentLRU[K, V]) Lookup(hash uint64, key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.buckets[hash] {
		if c.equalFn(e.key, key) {
			c.lru.MoveToFront(e.elem)
			c.hits++
			return e.value, true
		}
	}
	c.misses++
	return zero[V](), false
}

// Insert 锁内插入一条记录。若 key 已存在则跳过（double-check 语义）并返回已有值。
// double-check 用 equalFn 比对（不计 hit/miss——miss 已在之前的 Lookup 计过）。
// hash 由调用方预算（与 Lookup 复用同一 hash）；插入后按双上界驱逐尾部，保证至少留 1 条。
func (c *ContentLRU[K, V]) Insert(key K, hash uint64, value V, cost int) V {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.buckets[hash] {
		if c.equalFn(e.key, key) {
			return e.value
		}
	}
	e := &entry[K, V]{
		key:   key,
		hash:  hash,
		value: value,
		cost:  cost,
	}
	e.elem = c.lru.PushFront(e)
	c.buckets[hash] = append(c.buckets[hash], e)
	c.curCost += cost
	for (c.lru.Len() > c.maxEntries || c.curCost > c.maxCost) && c.lru.Len() > 1 {
		c.evictOldest()
	}
	return value
}

// evictOldest 移除 LRU 尾部条目（锁内调用）。
// entry 自带 hash 字段，直接定位桶，不需要反推 hash。
func (c *ContentLRU[K, V]) evictOldest() {
	back := c.lru.Back()
	if back == nil {
		return
	}
	victim := back.Value.(*entry[K, V])
	c.lru.Remove(back)
	c.evictions++
	c.curCost -= victim.cost
	bucket := c.buckets[victim.hash]
	for i, e := range bucket {
		if e == victim {
			bucket[i] = bucket[len(bucket)-1]
			bucket = bucket[:len(bucket)-1]
			break
		}
	}
	if len(bucket) == 0 {
		delete(c.buckets, victim.hash)
	} else {
		c.buckets[victim.hash] = bucket
	}
}

// Stats 计数快照。
type Stats struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
	Entries   int
	Cost      int // 当前占用累计（字节数或解码体积估算）
}

// Stats 返回计数快照。
func (c *ContentLRU[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
		Entries:   c.lru.Len(),
		Cost:      c.curCost,
	}
}

// Purge 清空全部条目。已被外部持有的共享 value 引用不受影响（GC 按引用计存活）。
func (c *ContentLRU[K, V]) Purge() {
	c.mu.Lock()
	c.buckets = make(map[uint64][]*entry[K, V])
	c.lru = list.New()
	c.curCost = 0
	c.mu.Unlock()
}

// zero 返回 V 的零值。
func zero[V any]() V {
	var v V
	return v
}
