package protox

import (
	"fmt"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
)

// marshalStoreBag ???? Bag ????? wire ???
// mutate ? nil ???????????????????
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

// TestDedupSharesIdenticalContent ?? (protoName, ??) ????? *WireValue?
// ???????????????????
func TestDedupSharesIdenticalContent(t *testing.T) {
	f := newStoreTestFactory(t)
	raw := marshalStoreBag(t, f, nil)

	wv1, err := f.WireShared("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("??: %v", err)
	}
	// ?????????????????????????????????????
	raw2 := append([]byte(nil), raw...)
	wv2, err := f.WireShared("storetest.Bag", raw2)
	if err != nil {
		t.Fatalf("??: %v", err)
	}
	if wv1 != wv2 {
		t.Fatal("????????? *WireValue ??")
	}

	st := f.wireCache.Stats()
	if st.Hits != 1 || st.Misses != 1 || st.Entries != 1 {
		t.Fatalf("hits=%d misses=%d entries=%d?want 1/1/1", st.Hits, st.Misses, st.Entries)
	}
	if st.RawBytes != len(raw) {
		t.Fatalf("rawBytes=%d?want %d???????????", st.RawBytes, len(raw))
	}

	// ?????????????????
	direct, err := f.Parse("storetest.Bag", raw)
	if err != nil {
		t.Fatalf("????: %v", err)
	}
	wantV, wantOK := Freeze(direct).NavigateSegs([]string{"title"})
	gotV, gotOK := wv1.NavigateSegs([]string{"title"})
	if gotOK != wantOK || gotV != wantV {
		t.Fatalf("????? title=(%v,%v)?????=(%v,%v)", gotV, gotOK, wantV, wantOK)
	}
}

// TestDedupDistinctContent ???????????????????
func TestDedupDistinctContent(t *testing.T) {
	f := newStoreTestFactory(t)
	rawA := marshalStoreBag(t, f, nil)
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "??B"); err != nil {
			t.Fatalf("????: %v", err)
		}
	})

	wvA, err := f.WireShared("storetest.Bag", rawA)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	wvB, err := f.WireShared("storetest.Bag", rawB)
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	if wvA == wvB {
		t.Fatal("????????")
	}
	titleB, ok := wvB.NavigateSegs([]string{"title"})
	if !ok || titleB != "??B" {
		t.Fatalf("B ? title=(%v,%v)?want ??B", titleB, ok)
	}
}

// TestDedupInvalidBytes ?????????????????????????
func TestDedupInvalidBytes(t *testing.T) {
	f := newStoreTestFactory(t)
	if _, err := f.WireShared("storetest.Bag", []byte{0xFF, 0xFF, 0xFF}); err == nil {
		t.Fatal("???????")
	}
	if entries := f.wireCache.Stats().Entries; entries != 0 {
		t.Fatalf("entries=%d??????????", entries)
	}
}

// TestDedupEviction ?????? LRU ????????????? miss?
// ??????????????????
func TestDedupEviction(t *testing.T) {
	f := newStoreTestFactory(t)
	f.wireCache = NewWireCache(2, 1<<30)

	variant := func(title string) []byte {
		return marshalStoreBag(t, f, func(msg proto.Message) {
			if err := f.SetField(msg, "title", title); err != nil {
				t.Fatalf("????: %v", err)
			}
		})
	}
	rawA, rawB, rawC := variant("A"), variant("B"), variant("C")

	wvA1, err := f.WireShared("storetest.Bag", rawA)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	if _, err := f.WireShared("storetest.Bag", rawB); err != nil {
		t.Fatalf("B: %v", err)
	}
	if _, err := f.WireShared("storetest.Bag", rawC); err != nil {
		t.Fatalf("C: %v", err) // ?? C ?????? A
	}
	if entries := f.wireCache.Stats().Entries; entries != 2 {
		t.Fatalf("entries=%d?want 2", entries)
	}

	wvA2, err := f.WireShared("storetest.Bag", rawA)
	if err != nil {
		t.Fatalf("A ??: %v", err)
	}
	if wvA1 == wvA2 {
		t.Fatal("A ??????????????")
	}
	// ??????????????????????????
	if v, ok := wvA1.NavigateSegs([]string{"title"}); !ok || v != "A" {
		t.Fatalf("?????????=(%v,%v)?want A", v, ok)
	}
}

// TestDedupByteBound ???????curBytes ?? maxBytes ?? LRU ?????
func TestDedupByteBound(t *testing.T) {
	f := newStoreTestFactory(t)
	raw := marshalStoreBag(t, f, nil)

	// ???? 1.5 ??????? 1 ????? 2 ??
	f.wireCache = NewWireCache(1024, len(raw)+len(raw)/2)

	if _, err := f.WireShared("storetest.Bag", raw); err != nil {
		t.Fatalf("A: %v", err)
	}
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "another-title-content"); err != nil {
			t.Fatalf("set title: %v", err)
		}
	})
	if _, err := f.WireShared("storetest.Bag", rawB); err != nil {
		t.Fatalf("B: %v", err)
	}
	bst := f.wireCache.Stats()
	if bst.Entries != 1 {
		t.Fatalf("entries=%d?want 1???????????", bst.Entries)
	}
	if bst.RawBytes != len(rawB) {
		t.Fatalf("rawBytes=%d?want %d", bst.RawBytes, len(rawB))
	}
	if bst.Evictions != 1 {
		t.Fatalf("evictions=%d?want 1", bst.Evictions)
	}
}

// TestDedupConcurrent ? goroutine???? pump??????????
// ????????????????
func TestDedupConcurrent(t *testing.T) {
	f := newStoreTestFactory(t)
	rawA := marshalStoreBag(t, f, nil)
	rawB := marshalStoreBag(t, f, func(msg proto.Message) {
		if err := f.SetField(msg, "title", "??B"); err != nil {
			t.Fatalf("????: %v", err)
		}
	})

	const workers, rounds = 8, 200
	results := make([][]*WireValue, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			results[w] = make([]*WireValue, 0, rounds)
			for i := range rounds {
				raw := rawA
				if (w+i)%2 == 1 {
					raw = rawB
				}
				wv, err := f.WireShared("storetest.Bag", raw)
				if err != nil {
					panic(fmt.Sprintf("worker%d round%d: %v", w, i, err))
				}
				results[w] = append(results[w], wv)
			}
		}(w)
	}
	wg.Wait()

	distinct := map[*WireValue]bool{}
	for _, rs := range results {
		for _, wv := range rs {
			distinct[wv] = true
		}
	}
	// ?? miss ??????????????????? 2 ????
	if len(distinct) != 2 {
		t.Fatalf("??????????? 2??? %d", len(distinct))
	}
	cst := f.wireCache.Stats()
	if cst.Entries != 2 {
		t.Fatalf("entries=%d?want 2", cst.Entries)
	}
	if cst.Hits+cst.Misses != workers*rounds {
		t.Fatalf("hits+misses=%d?want %d", cst.Hits+cst.Misses, workers*rounds)
	}
}
