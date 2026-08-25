package lru

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

// simpleHash 简单字节哈希（仅测试用）
func simpleHash(b []byte) uint64 {
	var h uint64 = 14695981039346656037
	for _, x := range b {
		h ^= uint64(x)
		h *= 1099511628211
	}
	return h
}

// newByteCache 创建测试用的 []byte → int 缓存
func newByteCache(maxEntries, maxCost int) *ContentLRU[[]byte, int] {
	return New[[]byte, int](maxEntries, maxCost, bytes.Equal)
}

// 验证基本命中/未命中与 LRU 前移
func TestContentLRUHitMiss(t *testing.T) {
	c := newByteCache(4, 1<<20)

	if _, ok := c.Lookup(simpleHash([]byte("alpha")), []byte("alpha")); ok {
		t.Fatal("空缓存不应命中")
	}

	c.Insert([]byte("alpha"), simpleHash([]byte("alpha")), 65, 1)

	v, ok := c.Lookup(simpleHash([]byte("alpha")), []byte("alpha"))
	if !ok || v != 65 {
		t.Fatalf("应命中 alpha，got (%d,%v)", v, ok)
	}

	if _, ok := c.Lookup(simpleHash([]byte("beta")), []byte("beta")); ok {
		t.Fatal("不同 key 不应命中")
	}

	s := c.Stats()
	if s.Hits != 1 || s.Misses != 2 || s.Entries != 1 {
		t.Fatalf("stats 误差：hits=%d misses=%d entries=%d", s.Hits, s.Misses, s.Entries)
	}
}

// 验证条目数上界驱逐
func TestContentLRUEntryBound(t *testing.T) {
	c := newByteCache(2, 1<<20)
	for i := range 3 {
		key := []byte(fmt.Sprintf("k%d", i))
		c.Insert(key, simpleHash(key), i, 1)
	}
	s := c.Stats()
	if s.Entries != 2 {
		t.Fatalf("maxEntries=2 应驱逐到 2 条，got %d", s.Entries)
	}
	if s.Evictions != 1 {
		t.Fatalf("应驱逐 1 条，got %d", s.Evictions)
	}
	if _, ok := c.Lookup(simpleHash([]byte("k0")), []byte("k0")); ok {
		t.Fatal("k0 应已被驱逐")
	}
	if v, ok := c.Lookup(simpleHash([]byte("k2")), []byte("k2")); !ok || v != 2 {
		t.Fatalf("k2 应保留且值为 2，got (%d,%v)", v, ok)
	}
}

// 验证 cost 上界驱逐
func TestContentLRUCostBound(t *testing.T) {
	c := newByteCache(100, 10)
	c.Insert([]byte("a"), simpleHash([]byte("a")), 1, 6)
	c.Insert([]byte("b"), simpleHash([]byte("b")), 2, 6)
	s := c.Stats()
	if s.Entries != 1 {
		t.Fatalf("cost 超限应驱逐到 1 条，got %d", s.Entries)
	}
	if s.Cost != 6 {
		t.Fatalf("剩余 cost 应为 6，got %d", s.Cost)
	}
	if _, ok := c.Lookup(simpleHash([]byte("a")), []byte("a")); ok {
		t.Fatal("a 应被驱逐")
	}
}

// 验证至少保留 1 条
func TestContentLRUMinOneEntry(t *testing.T) {
	c := newByteCache(100, 5)
	c.Insert([]byte("big"), simpleHash([]byte("big")), 1, 100)
	s := c.Stats()
	if s.Entries != 1 {
		t.Fatalf("单条即使超 maxCost 也应保留 1 条，got %d", s.Entries)
	}
}

// 验证 double-check：相同 key 的 Insert 不重复插入
func TestContentLRUDoubleCheck(t *testing.T) {
	c := newByteCache(10, 1<<20)
	key := []byte("dup")
	h := simpleHash(key)
	c.Insert(key, h, 1, 1)
	v := c.Insert(key, h, 2, 1) // double-check 应返回已有值
	if v != 1 {
		t.Fatalf("double-check 应返回已有值 1，got %d", v)
	}
	s := c.Stats()
	if s.Entries != 1 {
		t.Fatalf("重复插入不应新增条目，got %d", s.Entries)
	}
}

// 验证 Purge 清空
func TestContentLRUPurge(t *testing.T) {
	c := newByteCache(10, 1<<20)
	c.Insert([]byte("x"), simpleHash([]byte("x")), 1, 1)
	c.Purge()
	s := c.Stats()
	if s.Entries != 0 || s.Cost != 0 {
		t.Fatalf("purge 后应清空，entries=%d cost=%d", s.Entries, s.Cost)
	}
}

// 验证碰撞防御：相同 hash 不同 key 不误命中
func TestContentLRUCollisionDefense(t *testing.T) {
	c := newByteCache(10, 1<<20)
	hash := uint64(42)
	c.Insert([]byte("foo"), hash, 1, 1)
	if v, ok := c.Lookup(hash, []byte("bar")); ok {
		t.Fatalf("相同 hash 不同 key 不应命中，got (%d,%v)", v, ok)
	}
	if v, ok := c.Lookup(hash, []byte("foo")); !ok || v != 1 {
		t.Fatalf("foo 应命中，got (%d,%v)", v, ok)
	}
}

// 验证并发安全
func TestContentLRUConcurrent(t *testing.T) {
	c := newByteCache(100, 1<<20)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range 100 {
				key := []byte(fmt.Sprintf("k%d", j%10))
				h := simpleHash(key)
				if _, ok := c.Lookup(h, key); !ok {
					c.Insert(key, h, id, 1)
				}
			}
		}(i)
	}
	wg.Wait()
	s := c.Stats()
	if s.Entries > 10 {
		t.Fatalf("最多 10 个不同 key，got %d entries", s.Entries)
	}
}

// ── 验证结构体 key 零分配（模拟 WireCache 的 wireKey 用法）─────────

type testKey struct {
	name string
	data []byte
}

func testKeyHash(k testKey) uint64 {
	return simpleHash([]byte(k.name)) ^ simpleHash(k.data)
}

func testKeyEqual(a, b testKey) bool {
	return a.name == b.name && bytes.Equal(a.data, b.data)
}

// 验证结构体 key 的 hit/miss/equal 语义正确
func TestContentLRUStructKey(t *testing.T) {
	c := New[testKey, int](10, 1<<20, testKeyEqual)
	data := []byte("payload")
	k := testKey{name: "Msg", data: data}
	h := testKeyHash(k)

	if _, ok := c.Lookup(h, k); ok {
		t.Fatal("空缓存不应命中")
	}
	c.Insert(k, h, 1, 1)

	// 相同 name + 相同 data 应命中
	if v, ok := c.Lookup(h, k); !ok || v != 1 {
		t.Fatalf("相同结构体 key 应命中，got (%d,%v)", v, ok)
	}

	// 相同 name + 不同 data 不应命中
	other := testKey{name: "Msg", data: []byte("other")}
	if _, ok := c.Lookup(testKeyHash(other), other); ok {
		t.Fatal("不同 data 不应命中")
	}

	// 不同 name + 相同 data 不应命中
	other2 := testKey{name: "Other", data: data}
	if _, ok := c.Lookup(testKeyHash(other2), other2); ok {
		t.Fatal("不同 name 不应命中")
	}
}
