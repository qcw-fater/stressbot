package protox

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

// buildRetentionBag 构造「大整包 + 小子消息」的测试字节：
// title 填充大字符串，main 是小 message——路径存储只要 main 时复制显著更省。
func buildRetentionBag(t *testing.T, f *Factory) []byte {
	t.Helper()
	bag := buildStoreTestBag(t, f)
	if err := f.SetField(bag, "title", strings.Repeat("x", 4096)); err != nil {
		t.Fatalf("填充 title 失败: %v", err)
	}
	raw, err := proto.Marshal(bag)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

// TestPlanWireRetentionCopiesSmallSpans 只保留小子 span 时逐个复制为独立缓冲，
// 解除对整包快照的钉扎；复制后内容逐字节一致。
func TestPlanWireRetentionCopiesSmallSpans(t *testing.T) {
	f := newStoreTestFactory(t)
	disableShadow(t)
	raw := buildRetentionBag(t, f)
	wv := wireOf(t, f, "storetest.Bag", raw)

	sub, ok := wv.Navigate("main")
	if !ok {
		t.Fatal("main 未找到")
	}
	subWV, ok := sub.(*WireValue)
	if !ok {
		t.Fatalf("main 应为 *WireValue，实际 %T", sub)
	}
	before := subWV.Raw()

	results := []any{sub}
	PlanWireRetention(results, len(raw), false)

	after, ok := results[0].(*WireValue)
	if !ok {
		t.Fatalf("规划后应仍为 *WireValue，实际 %T", results[0])
	}
	if after == subWV {
		t.Fatal("小 span 应被复制为新实例（解除整包钉扎）")
	}
	if !bytes.Equal(after.Raw(), before) {
		t.Fatal("复制后字节内容必须逐字一致")
	}
}

// TestPlanWireRetentionSharesWhenWholeRetained 整包同时被整存时子 span 直接共享，
// 零复制（快照反正常驻）。
func TestPlanWireRetentionSharesWhenWholeRetained(t *testing.T) {
	f := newStoreTestFactory(t)
	disableShadow(t)
	raw := buildRetentionBag(t, f)
	wv := wireOf(t, f, "storetest.Bag", raw)

	sub, _ := wv.Navigate("main")
	subWV := sub.(*WireValue)

	results := []any{sub}
	PlanWireRetention(results, len(raw), true)

	if results[0].(*WireValue) != subWV {
		t.Fatal("整存共存时子 span 不应复制（共享整包快照）")
	}
}

// TestPlanWireRetentionSharesNearWhole 子 span 合计接近整包时共享更省，不复制。
func TestPlanWireRetentionSharesNearWhole(t *testing.T) {
	f := newStoreTestFactory(t)
	disableShadow(t)
	raw := buildRetentionBag(t, f)
	wv := wireOf(t, f, "storetest.Bag", raw)

	// 自引用近整包：整消息子视图（title 占绝对多数）→ Σ ≈ wholeLen → 共享。
	subTitle, _ := wv.Navigate("title")
	subMain, _ := wv.Navigate("main")
	mainWV := subMain.(*WireValue)

	// title 是复制出的字符串（标量终端不共享底层），只有 main 计入 wire 引用；
	// 用 wholeLen = len(main.Raw()) 模拟「引用近乎整包」的决策边界。
	results := []any{subTitle, subMain}
	PlanWireRetention(results, len(mainWV.Raw()), false)

	if results[1].(*WireValue) != mainWV {
		t.Fatal("Σ span ≥ 整包时应共享，不应复制")
	}
}

// TestPlanWireRetentionNestedContainers 列表/映射容器里的嵌套 *WireValue 同样被规划。
func TestPlanWireRetentionNestedContainers(t *testing.T) {
	f := newStoreTestFactory(t)
	disableShadow(t)
	raw := buildRetentionBag(t, f)
	wv := wireOf(t, f, "storetest.Bag", raw)

	items, ok := wv.Navigate("items")
	if !ok {
		t.Fatal("items 未找到")
	}
	list, ok := items.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("items 应为长度 2 的 []any，实际 %T", items)
	}
	firstBefore := list[0].(*WireValue)

	results := []any{items}
	PlanWireRetention(results, len(raw), false)

	afterList := results[0].([]any)
	firstAfter, ok := afterList[0].(*WireValue)
	if !ok {
		t.Fatalf("元素应仍为 *WireValue，实际 %T", afterList[0])
	}
	if firstAfter == firstBefore {
		t.Fatal("容器内嵌套 span 也应被复制")
	}
	if !bytes.Equal(firstAfter.Raw(), firstBefore.Raw()) {
		t.Fatal("复制后元素字节必须一致")
	}
}
