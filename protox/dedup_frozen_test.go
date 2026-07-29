package protox

import (
	"fmt"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestFrozenDedupSharesIdenticalContent 相同 (protoName, 字节) 命中同一个 *Frozen，
// 且共享值的导航结果与独占解码路径一致。
func TestFrozenDedupSharesIdenticalContent(t *testing.T) {
	f := newStoreTestFactory(t)
	raw := marshalStoreBag(t, f, nil)

	fz1, err := f.ParseFrozenShared("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("首次: %v", err)
	}
	// 第二次用独立复制的字节（模拟另一台机器人的缓冲区），仍应命中同一共享实例。
	raw2 := append([]byte(nil), raw...)
	fz2, err := f.ParseFrozenShared("storetest.Bag", raw2)
	if err != nil {
		t.Fatalf("二次: %v", err)
	}
	if fz1 != fz2 {
		t.Fatal("相同内容应共享同一 *Frozen 实例")
	}

	st := f.frozenCache.Stats()
	if st.Hits != 1 || st.Misses != 1 || st.Entries != 1 {
		t.Fatalf("hits=%d misses=%d entries=%d，want 1/1/1", st.Hits, st.Misses, st.Entries)
	}
	if st.CostBytes <= 0 {
		t.Fatalf("costBytes=%d，应为正的解码体积估算", st.CostBytes)
	}

	// 共享实例的导航结果与独占解码等价。
	direct, err := f.Parse("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	wantV, wantOK := Freeze(direct).NavigateSegs([]string{"title"})
	gotV, gotOK := fz1.NavigateSegs([]string{"title"})
	if gotOK != wantOK || gotV != wantV {
		t.Fatalf("共享值导航 title=(%v,%v)，独占解码=(%v,%v)", gotV, gotOK, wantV, wantOK)
	}
}

// TestFrozenDedupCostBound 体积上界按解码体积估算驱逐：装得下 1 条、装不下 2 条时
// 插入第二条驱逐第一条。
func TestFrozenDedupCostBound(t *testing.T) {
	f := newStoreTestFactory(t)
	raw := marshalStoreBag(t, f, nil)

	probe, err := f.Parse("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("probe parse: %v", err)
	}
	oneCost := estimateDecodedCost(probe)
	if oneCost <= 0 {
		t.Fatalf("estimateDecodedCost=%d，应为正数", oneCost)
	}
	f.frozenCache = NewFrozenCache(1024, oneCost+oneCost/2)

	if _, err := f.ParseFrozenShared("storetest.Bag", raw); err != nil {
		t.Fatalf("A: %v", err)
	}
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "another-title-content"); err != nil {
			t.Fatalf("set title: %v", err)
		}
	})
	if _, err := f.ParseFrozenShared("storetest.Bag", rawB); err != nil {
		t.Fatalf("B: %v", err)
	}
	bst := f.frozenCache.Stats()
	if bst.Entries != 1 {
		t.Fatalf("entries=%d，want 1（体积上界应触发驱逐）", bst.Entries)
	}
	if bst.Evictions != 1 {
		t.Fatalf("evictions=%d，want 1", bst.Evictions)
	}
}

// TestFrozenDedupConcurrent 多 goroutine（模拟多 pump）并发获取共享实例：
// 无竞态、全程只产生两个相异实例。
func TestFrozenDedupConcurrent(t *testing.T) {
	f := newStoreTestFactory(t)
	rawA := marshalStoreBag(t, f, nil)
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "标题B"); err != nil {
			t.Fatalf("改写失败: %v", err)
		}
	})

	const workers, rounds = 8, 200
	results := make([][]*Frozen, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			results[w] = make([]*Frozen, 0, rounds)
			for i := 0; i < rounds; i++ {
				raw := rawA
				if (w+i)%2 == 1 {
					raw = rawB
				}
				fz, err := f.ParseFrozenShared("storetest.Bag", raw)
				if err != nil {
					panic(fmt.Sprintf("worker%d round%d: %v", w, i, err))
				}
				results[w] = append(results[w], fz)
			}
		}(w)
	}
	wg.Wait()

	distinct := map[*Frozen]bool{}
	for _, rs := range results {
		for _, fz := range rs {
			distinct[fz] = true
		}
	}
	if len(distinct) != 2 {
		t.Fatalf("并发去重后相异实例应为 2，实际 %d", len(distinct))
	}
	cst := f.frozenCache.Stats()
	if cst.Entries != 2 {
		t.Fatalf("entries=%d，want 2", cst.Entries)
	}
	if cst.Hits+cst.Misses != workers*rounds {
		t.Fatalf("hits+misses=%d，want %d", cst.Hits+cst.Misses, workers*rounds)
	}
}
