package protox

import (
	"math/rand"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ── D2 wire 惰性视图兼容层差分：*Compat ≡ Factory 反射读 ─────────────────

// assertGetFieldCompat 双侧 GetField 一次并比对（错误性 + 产物逐字相等）。
func assertGetFieldCompat(t *testing.T, f *Factory, wv *WireValue, oracle proto.Message, path string) {
	t.Helper()
	got, gerr := wv.GetFieldCompat(path)
	want, werr := f.GetField(oracle, path)
	if (gerr != nil) != (werr != nil) {
		t.Fatalf("path %q: 错误性不一致 wire=%v oracle=%v", path, gerr, werr)
	}
	if gerr == nil && !plainEqual(got, want) {
		t.Fatalf("path %q:\n wire  =%#v\n oracle=%#v", path, got, want)
	}
}

// TestWireViewCompatSemantics 兼容层的历史语义定向用例：
// 缺席单数 message 按默认值实例下钻/物化、oneof 落选成员取默认、越界下标报错、
// 未知字段报错、map 字段不可下钻。
func TestWireViewCompatSemantics(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	msg := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "str", "compat")
		setF(t, f, m, "rints", []any{int64(1), int64(2)})
		setF(t, f, m, "choice_str", "winner")
	})
	raw := mustMarshal(t, msg)
	wv := wireOf(t, f, "wiretest.Everything", raw)
	oracle, err := f.Parse("wiretest.Everything", raw)
	if err != nil {
		t.Fatalf("oracle 解码失败: %v", err)
	}

	// 缺席 message 的默认值语义（GetField ≠ Navigate 的关键差异）。
	for _, p := range []string{
		"node",              // 缺席 message → 默认值整表
		"node.name",         // 缺席 message 下钻标量 → ""
		"node.leaf.id",      // 两层缺席下钻 → 0
		"choice_num",        // oneof 落选标量 → 默认 0
		"choice_node",       // oneof 落选 message → 默认值整表
		"opt_i",             // 未设置 optional → 默认 0
		"str", "rints",      // 已设置字段
		"rints[0]", "mstr",  // 下标 / 空 map
		"rints[9]",          // 越界 → error
		"no_such_field",     // 未知字段 → error
		"mstr.k",            // map 不可下钻 → error
		"str.x",             // 标量不可嵌套 → error
		"[0]",               // 下标开头 → error
	} {
		assertGetFieldCompat(t, f, wv, oracle, p)
	}

	// 列表兼容读。
	n, err := wv.ListLenCompat("rints")
	if err != nil || n != 2 {
		t.Fatalf("ListLenCompat(rints)=(%d,%v) want (2,nil)", n, err)
	}
	if _, err := wv.ListLenCompat("str"); err == nil {
		t.Fatal("非 repeated 字段 ListLenCompat 应报错")
	}
	if _, err := wv.ListItemCompat("rints", 5); err == nil {
		t.Fatal("越界 ListItemCompat 应报错")
	}
	item, err := wv.ListItemCompat("rints", 1)
	if err != nil || item != int64(2) {
		t.Fatalf("ListItemCompat(rints,1)=(%v,%v) want (2,nil)", item, err)
	}
	// 缺席 message 中间段 → 空列表语义（长度 0），与解码路径一致。
	nGot, gerr := wv.ListLenCompat("node.leaves")
	nWant, werr := f.GetListLen(oracle, "node.leaves")
	if (gerr != nil) != (werr != nil) || nGot != nWant {
		t.Fatalf("node.leaves 长度不一致: wire=(%d,%v) oracle=(%d,%v)", nGot, gerr, nWant, werr)
	}
}

// TestWireViewCompatDifferentialFuzz 随机消息（含 wire 拼接变异）+ 路径语料的
// GetFieldCompat / ListLenCompat / ListItemCompat 双侧比对。
func TestWireViewCompatDifferentialFuzz(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)
	md, _ := f.MessageDescriptor("wiretest.Everything")

	const iterations = 120
	rnd := rand.New(rand.NewSource(20260730))

	for it := 0; it < iterations; it++ {
		msg := dynamicpb.NewMessage(md)
		randFill(rnd, msg.ProtoReflect(), 3)
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatalf("iter %d: 序列化失败: %v", it, err)
		}
		if rnd.Intn(2) == 0 {
			msg2 := dynamicpb.NewMessage(md)
			randFill(rnd, msg2.ProtoReflect(), 2)
			raw2, err := proto.Marshal(msg2)
			if err != nil {
				t.Fatalf("iter %d: 序列化失败: %v", it, err)
			}
			raw = append(raw, raw2...)
		}

		oracle := dynamicpb.NewMessage(md)
		if err := proto.Unmarshal(raw, oracle); err != nil {
			t.Fatalf("iter %d: oracle 解码失败: %v", it, err)
		}
		wv := NewWireValue(md, WireSnapshot(raw))
		tree := messageToMap(oracle.ProtoReflect())

		for _, p := range collectPathCorpus(md, tree) {
			got, gerr := wv.GetFieldCompat(p)
			want, werr := f.GetField(oracle, p)
			if (gerr != nil) != (werr != nil) {
				t.Fatalf("iter %d path %q: 错误性不一致 wire=%v oracle=%v", it, p, gerr, werr)
			}
			if gerr == nil && !plainEqual(got, want) {
				t.Fatalf("iter %d path %q:\n wire  =%#v\n oracle=%#v", it, p, got, want)
			}
		}

		// 列表兼容读：对全部顶层 repeated 字段比对长度与逐元素产物。
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if !fd.IsList() {
				continue
			}
			name := string(fd.Name())
			nGot, gerr := wv.ListLenCompat(name)
			nWant, werr := f.GetListLen(oracle, name)
			if (gerr != nil) != (werr != nil) || nGot != nWant {
				t.Fatalf("iter %d %s: 长度不一致 wire=(%d,%v) oracle=(%d,%v)", it, name, nGot, gerr, nWant, werr)
			}
			// 游标与逐下标读语义逐项一致（游标是 list_get 循环的 O(n) 等价物）。
			cur, cerr := wv.ListCursorCompat(name)
			if cerr != nil {
				t.Fatalf("iter %d %s: ListCursorCompat 失败: %v", it, name, cerr)
			}
			if cur.Len() != nGot {
				t.Fatalf("iter %d %s: 游标长度不一致 cursor=%d len=%d", it, name, cur.Len(), nGot)
			}
			for j := 0; j < nGot; j++ {
				gi, gerr := wv.ListItemCompat(name, j)
				wi, werr := f.GetListItem(oracle, name, j)
				ci := cur.Item(j)
				if gerr == nil && !wireItemEqual(ci, gi) {
					t.Fatalf("iter %d %s[%d]: 游标产物与 ListItemCompat 不一致\n cursor=%#v\n item  =%#v",
						it, name, j, ci, gi)
				}
				if (gerr != nil) != (werr != nil) {
					t.Fatalf("iter %d %s[%d]: 错误性不一致 wire=%v oracle=%v", it, name, j, gerr, werr)
				}
				if gerr != nil {
					continue
				}
				// message 元素：wire 侧为子视图，oracle 侧为 proto.Message——物化后比对。
				if sub, ok := gi.(*WireValue); ok {
					wm, ok2 := wi.(proto.Message)
					if !ok2 {
						t.Fatalf("iter %d %s[%d]: 形态不一致 wire=%T oracle=%T", it, name, j, gi, wi)
					}
					if !plainEqual(sub.MaterializeValue(), messageToMap(wm.ProtoReflect())) {
						t.Fatalf("iter %d %s[%d]: 子消息不一致", it, name, j)
					}
				} else if !plainEqual(gi, wi) {
					t.Fatalf("iter %d %s[%d]:\n wire  =%#v\n oracle=%#v", it, name, j, gi, wi)
				}
			}
		}
	}
}

// wireItemEqual 游标元素与 ListItemCompat 元素的等价判定：
// message 元素双方都是 *WireValue 子视图，比 desc + 物化产物；标量直接比。
func wireItemEqual(a, b any) bool {
	av, aok := a.(*WireValue)
	bv, bok := b.(*WireValue)
	if aok != bok {
		return false
	}
	if aok {
		return av.Desc() == bv.Desc() && plainEqual(av.MaterializeValue(), bv.MaterializeValue())
	}
	return plainEqual(a, b)
}

// TestWireListCursorSemantics 游标的定向语义用例：
// 标量列表逐项产出、message 列表产出子视图、非 repeated 字段空迭代（对齐
// iter_list 经 GetField 的历史行为）、未知字段报错、缺席中间段空列表、越界 nil。
func TestWireListCursorSemantics(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	msg := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "str", "cursor")
		setF(t, f, m, "rints", []any{int64(7), int64(8), int64(9)})
	})
	wv := wireOf(t, f, "wiretest.Everything", mustMarshal(t, msg))

	cur, err := wv.ListCursorCompat("rints")
	if err != nil || cur.Len() != 3 {
		t.Fatalf("ListCursorCompat(rints)=(len %d, %v) want (3, nil)", cur.Len(), err)
	}
	for i, want := range []int64{7, 8, 9} {
		if got := cur.Item(i); got != want {
			t.Fatalf("Item(%d)=%v want %d", i, got, want)
		}
	}
	if cur.Item(3) != nil || cur.Item(-1) != nil {
		t.Fatal("越界 Item 应返回 nil")
	}

	// 非 repeated → 空游标（历史上 iter_list 对标量字段就是空迭代，不报错）。
	cur, err = wv.ListCursorCompat("str")
	if err != nil || cur.Len() != 0 {
		t.Fatalf("非 repeated 字段应得空游标: (len %d, %v)", cur.Len(), err)
	}

	// 未知字段 → error。
	if _, err := wv.ListCursorCompat("no_such_field"); err == nil {
		t.Fatal("未知字段 ListCursorCompat 应报错")
	}

	// 缺席单数 message 中间段 → 空列表语义。
	cur, err = wv.ListCursorCompat("node.leaves")
	if err != nil || cur.Len() != 0 {
		t.Fatalf("缺席中间段应得空游标: (len %d, %v)", cur.Len(), err)
	}
}

// TestWireViewCompatShadowSampling GetFieldCompat 的影子采样：首 K 次触发校验、
// 正确实现零失配。
func TestWireViewCompatShadowSampling(t *testing.T) {
	f := newWireTestFactory(t)
	resetWireShadowForTest()
	t.Cleanup(resetWireShadowForTest)

	msg := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "str", "shadow") })
	wv := wireOf(t, f, "wiretest.Everything", mustMarshal(t, msg))

	before := SnapshotWireShadowStats()
	for i := 0; i < shadowFirstK+2; i++ {
		if _, err := wv.GetFieldCompat("str"); err != nil {
			t.Fatalf("GetFieldCompat 失败: %v", err)
		}
	}
	after := SnapshotWireShadowStats()
	if after.Checks-before.Checks != shadowFirstK {
		t.Fatalf("首 K 次应各触发一次校验: got %d want %d", after.Checks-before.Checks, shadowFirstK)
	}
	if after.Mismatches != before.Mismatches {
		t.Fatalf("正确实现不应失配: %d → %d", before.Mismatches, after.Mismatches)
	}
}
