// 解压去重缓存 / routeKey 驻留 / work 缓冲池的内部测试（package codec：
// 需要直接访问 sharedInflateCache 与 internRouteKey）。
package codec

import (
	"bytes"
	"testing"
)

// TestInflateCacheSecondSight 二见登记语义：首见只记标记不存字节，
// 再见登记真条目，此后命中返回共享产物。
func TestInflateCacheSecondSight(t *testing.T) {
	c := newInflateCache(16, 1<<20)
	comp := bytes.Repeat([]byte{0xAB, 0xCD}, 800)
	out := []byte("decompressed-payload")

	if got := c.get(comp); got != nil {
		t.Fatal("空缓存不应命中")
	}
	c.put(comp, out)
	s := c.stats()
	if s.Entries != 0 || s.Markers != 1 {
		t.Fatalf("首见应只记标记：entries=%d markers=%d", s.Entries, s.Markers)
	}
	if got := c.get(comp); got != nil {
		t.Fatal("标记态不应命中")
	}
	c.put(comp, out)
	s = c.stats()
	if s.Entries != 1 || s.Markers != 0 {
		t.Fatalf("二见应登记真条目：entries=%d markers=%d", s.Entries, s.Markers)
	}
	got := c.get(comp)
	if got == nil || !bytes.Equal(got, out) {
		t.Fatal("登记后应命中并返回产物")
	}
	if &got[0] != &out[0] {
		t.Error("命中应返回共享底层数组（零拷贝）")
	}
	// 内容不同但前缀相同的 key 不得误命中。
	other := append(append([]byte{}, comp...), 0x01)
	if c.get(other) != nil {
		t.Error("相异内容不应命中")
	}
}

// TestInflateCacheEviction 双上界 LRU：超条目数从尾部驱逐,最旧先走。
func TestInflateCacheEviction(t *testing.T) {
	c := newInflateCache(2, 1<<20)
	mk := func(tag byte) []byte { return bytes.Repeat([]byte{tag}, 1200) }
	for _, tag := range []byte{1, 2, 3} {
		comp := mk(tag)
		c.put(comp, []byte{tag}) // 首见 → 标记
		c.put(comp, []byte{tag}) // 二见 → 真条目
	}
	s := c.stats()
	if s.Entries != 2 || s.Evictions != 1 {
		t.Fatalf("应驱逐 1 条保留 2 条：entries=%d evictions=%d", s.Entries, s.Evictions)
	}
	if c.get(mk(1)) != nil {
		t.Error("最旧条目(tag=1)应已被驱逐")
	}
	if c.get(mk(3)) == nil {
		t.Error("最新条目(tag=3)应保留")
	}
}

// TestInternRouteKey 驻留表返回规范实例：重复键返回同一底层字符串,条数不增。
func TestInternRouteKey(t *testing.T) {
	a := internRouteKey([]byte("9901-77"))
	sizeAfterFirst := routeKeyInternSize()
	b := internRouteKey([]byte("9901-77"))
	if a != b {
		t.Fatal("同内容应返回相等字符串")
	}
	if routeKeyInternSize() != sizeAfterFirst {
		t.Error("重复驻留不应增加条数")
	}
	if internRouteKey([]byte("9901-78")) == a {
		t.Error("不同内容不应混淆")
	}
}

// TestDecodeInflateDedupE2E 端到端：同一压缩帧反复 decode，第 3 次起命中共享
// 解压产物；所有 decode 结果与原始 body 一致（含并发段，-race 下验证锁与共享安全）。
func TestDecodeInflateDedupE2E(t *testing.T) {
	s, err := LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema 失败: %v", err)
	}
	c, err := NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 失败: %v", err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 1)
	}
	// 4KB 高熵块重复 16 次 = 64KB：可压缩（触发 compress 步），压缩产物 ≥4KB
	// （高熵块至少完整出现一次），稳过 inflateDedupMinBytes 门槛。
	block := make([]byte, 4096)
	st := uint32(0xBEEF1234)
	for i := range block {
		st ^= st << 13
		st ^= st >> 17
		st ^= st << 5
		block[i] = byte(st)
	}
	body := bytes.Repeat(block, 16)
	route := map[string]any{"cmd": float64(100), "act": float64(7)}
	frame := c.EncodeTCP(route, body, key)
	if frame == nil {
		t.Fatal("EncodeTCP 返回 nil")
	}

	before := sharedInflateCache.stats()
	for i := range 4 {
		_, got, _ := c.DecodeTCP(frame, key)
		if !bytes.Equal(got, body) {
			t.Fatalf("第 %d 次 decode body 不一致", i+1)
		}
	}
	after := sharedInflateCache.stats()
	if after.Entries-before.Entries < 1 {
		t.Errorf("二见后应登记真条目：entries %d→%d", before.Entries, after.Entries)
	}
	if after.Hits-before.Hits < 2 {
		t.Errorf("第 3、4 次 decode 应命中：hits %d→%d", before.Hits, after.Hits)
	}

	// 并发段：8 goroutine × 25 次同帧 decode，全部结果正确（共享产物只读契约）。
	done := make(chan error, 8)
	for range 8 {
		go func() {
			for range 25 {
				_, got, _ := c.DecodeTCP(frame, key)
				if !bytes.Equal(got, body) {
					done <- bytes.ErrTooLarge // 任意非 nil 哨兵
					return
				}
			}
			done <- nil
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal("并发 decode 结果不一致")
		}
	}
}
