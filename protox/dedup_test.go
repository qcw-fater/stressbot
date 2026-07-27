package protox

import (
	"fmt"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
)

// marshalStoreBag ???????????????"????"?
// mutate ??????????????????????
func marshalStoreBag(t *testing.T, f *Factory, mutate func(msg proto.Message)) []byte {
	t.Helper()
	bag := buildStoreTestBag(t, f)
	if mutate != nil {
		mutate(bag)
	}
	raw, err := proto.Marshal(bag)
	if err != nil {
		t.Fatalf("?????: %v", err)
	}
	return raw
}

// TestDedupSharesIdenticalContent ?? (protoName, ??) ??????? *Frozen
// ???????????????????
func TestDedupSharesIdenticalContent(t *testing.T) {
	f := newStoreTestFactory(t)
	raw := marshalStoreBag(t, f, nil)

	fz1, err := f.ParseFrozenShared("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("????: %v", err)
	}
	// ???????????????????????????????????
	raw2 := append([]byte(nil), raw...)
	fz2, err := f.ParseFrozenShared("storetest.Bag", raw2)
	if err != nil {
		t.Fatalf("????: %v", err)
	}
	if fz1 != fz2 {
		t.Fatal("?????????? *Frozen ??")
	}

	hits, misses, entries, _ := f.frozenCache.Stats()
	if hits != 1 || misses != 1 || entries != 1 {
		t.Fatalf("hits=%d misses=%d entries=%d?want 1/1/1", hits, misses, entries)
	}

	// ????????????????????????
	direct, err := f.Parse("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("????: %v", err)
	}
	wantV, wantOK := Freeze(direct).NavigateSegs([]string{"title"})
	gotV, gotOK := fz1.NavigateSegs([]string{"title"})
	if gotOK != wantOK || gotV != wantV {
		t.Fatalf("?????? title=(%v,%v)?????=(%v,%v)", gotV, gotOK, wantV, wantOK)
	}
}

// TestDedupDistinctContent ???????????????????????????
func TestDedupDistinctContent(t *testing.T) {
	f := newStoreTestFactory(t)
	rawA := marshalStoreBag(t, f, nil)
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "??B"); err != nil {
			t.Fatalf("?????: %v", err)
		}
	})

	fzA, err := f.ParseFrozenShared("storetest.Bag", rawA)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	fzB, err := f.ParseFrozenShared("storetest.Bag", rawB)
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	if fzA == fzB {
		t.Fatal("??????????")
	}
	titleB, ok := fzB.NavigateSegs([]string{"title"})
	if !ok || titleB != "??B" {
		t.Fatalf("B ? title=(%v,%v)?want ??B", titleB, ok)
	}
}

// TestDedupEviction ???????? LRU ???????????????? miss
//??????????????????????
func TestDedupEviction(t *testing.T) {
	f := newStoreTestFactory(t)
	cache := NewFrozenCache(2, 1<<30)

	variant := func(title string) []byte {
		return marshalStoreBag(t, f, func(msg proto.Message) {
			if err := f.SetField(msg, "title", title); err != nil {
				t.Fatalf("?????: %v", err)
			}
		})
	}
	rawA, rawB, rawC := variant("A"), variant("B"), variant("C")

	fzA1, err := cache.getOrParse(f, "storetest.Bag", rawA)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	if _, err := cache.getOrParse(f, "storetest.Bag", rawB); err != nil {
		t.Fatalf("B: %v", err)
	}
	if _, err := cache.getOrParse(f, "storetest.Bag", rawC); err != nil {
		t.Fatalf("C: %v", err) // ?? C ??????? A
	}
	if _, _, entries, _ := cache.Stats(); entries != 2 {
		t.Fatalf("entries=%d?want 2", entries)
	}

	fzA2, err := cache.getOrParse(f, "storetest.Bag", rawA)
	if err != nil {
		t.Fatalf("A ??: %v", err)
	}
	if fzA1 == fzA2 {
		t.Fatal("A ???????????????")
	}
	// ??????????????????????????
	if v, ok := fzA1.NavigateSegs([]string{"title"}); !ok || v != "A" {
		t.Fatalf("??????????=(%v,%v)?want A", v, ok)
	}
}

// TestDedupByteBound ???????????
func TestDedupByteBound(t *testing.T) {
	f := newStoreTestFactory(t)
	raw := marshalStoreBag(t, f, nil)
	cache := NewFrozenCache(1024, len(raw)+1) // ??????

	if _, err := cache.getOrParse(f, "storetest.Bag", raw); err != nil {
		t.Fatalf("A: %v", err)
	}
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "??????"); err != nil {
			t.Fatalf("?????: %v", err)
		}
	})
	if _, err := cache.getOrParse(f, "storetest.Bag", rawB); err != nil {
		t.Fatalf("B: %v", err)
	}
	_, _, entries, rawBytes := cache.Stats()
	if entries != 1 {
		t.Fatalf("entries=%d?want 1????????", entries)
	}
	if rawBytes > len(raw)+len(rawB) {
		t.Fatalf("rawBytes=%d ????", rawBytes)
	}
}

// TestDedupConcurrent ? goroutine????? pump??????????
// ??????????????????????
func TestDedupConcurrent(t *testing.T) {
	f := newStoreTestFactory(t)
	rawA := marshalStoreBag(t, f, nil)
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "??B"); err != nil {
			t.Fatalf("?????: %v", err)
		}
	})
	cache := NewFrozenCache(dedupMaxEntries, dedupMaxBytes)

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
				fz, err := cache.getOrParse(f, "storetest.Bag", raw)
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
		t.Fatalf("??????????? 2 ?????? %d", len(distinct))
	}
	hits, misses, entries, _ := cache.Stats()
	if entries != 2 {
		t.Fatalf("entries=%d?want 2", entries)
	}
	if hits+misses != workers*rounds {
		t.Fatalf("hits+misses=%d?want %d", hits+misses, workers*rounds)
	}
}
