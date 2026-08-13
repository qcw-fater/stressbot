package protox

import (
	"reflect"
	"testing"

	"stressbot/state"
)

// materializeFrozen 把 Frozen 导航产物递归展开为 GetFieldMap 风格的装箱树，
// 供与旧路径 navigatePath(GetFieldMap(msg), path) 做逐字等价比对。
func materializeFrozen(v any) any {
	switch x := v.(type) {
	case *Frozen:
		return messageToMap(x.Message().ProtoReflect())
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = materializeFrozen(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = materializeFrozen(e)
		}
		return out
	default:
		return v
	}
}

// TestFrozenNavigateEquivalence 校验 Frozen.NavigateSegs 与旧路径
// navigatePath(GetFieldMap(msg), path) 在存在性与取值（物化后）上逐字等价
// （P1a 惰性状态表示的正确性契约，路径集合与 TestGetFieldForStoreEquivalence 一致）。
func TestFrozenNavigateEquivalence(t *testing.T) {
	f := newStoreTestFactory(t)
	bag := buildStoreTestBag(t, f)
	fz := Freeze(bag)

	fullMap := f.GetFieldMap(bag)

	paths := []string{
		"uid",
		"title",
		"main",
		"main.id",
		"main.name", // proto3 默认 ""，main 已设置故存在
		"items",
		"items[0]",
		"items[0].id",
		"items[1].name",
		"items[5].id", // 越界
		"nums",
		"nums[1]",
		"nums[2].x", // 标量后继续下探
		"shelf",
		"shelf.s1",
		"shelf.s1.id",
		"shelf.s1.name",
		"shelf.missing", // map 缺失 key
		"counts",
		"counts.gold",
		"counts.missing",
		"empty",    // 未设置 message
		"empty.id", // 未设置 message 下探
		"missing",  // 不存在字段
		"main[0]",  // message 节点用数组下标
		"uid.x",    // 标量下探
	}

	for _, p := range paths {
		oldVal := state.NavigatePath(fullMap, p)
		newVal, ok := fz.NavigateSegs(state.SplitPath(p))

		if ok == (oldVal == nil) {
			t.Errorf("path %q: ok=%v 但 navigatePath=%#v（found/nil 不一致）", p, ok, oldVal)
			continue
		}
		if ok && !reflect.DeepEqual(materializeFrozen(newVal), oldVal) {
			t.Errorf("path %q: NavigateSegs(物化)=%#v, navigatePath=%#v（值不一致）",
				p, materializeFrozen(newVal), oldVal)
		}
	}
}

// TestFrozenLazyRepresentation 校验导航产物的惰性形态：
// message 终端为 *Frozen、repeated message 元素为 *Frozen、标量/默认值直接取值。
func TestFrozenLazyRepresentation(t *testing.T) {
	f := newStoreTestFactory(t)
	bag := buildStoreTestBag(t, f)
	fz := Freeze(bag)

	// message 终端 → *Frozen（不展开）
	v, ok := fz.NavigateSegs([]string{"main"})
	if !ok {
		t.Fatal("main 未找到")
	}
	sub, isFrozen := v.(*Frozen)
	if !isFrozen {
		t.Fatalf("main 应为 *Frozen，实际 %T", v)
	}
	// 嵌套 Frozen 可继续导航；未设置的 proto3 标量返回默认值（在场，不丢失）
	name, ok := sub.NavigateSegs([]string{"name"})
	if !ok || name != "" {
		t.Fatalf("main.name 应为默认值 \"\"（found=true），实际 (%#v,%v)", name, ok)
	}

	// repeated message 终端 → []any，元素为 *Frozen（浅物化）
	v, ok = fz.NavigateSegs([]string{"items"})
	if !ok {
		t.Fatal("items 未找到")
	}
	list, isList := v.([]any)
	if !isList || len(list) != 2 {
		t.Fatalf("items 应为长度 2 的 []any，实际 %T %#v", v, v)
	}
	if _, isFrozen := list[0].(*Frozen); !isFrozen {
		t.Fatalf("items[0] 应为 *Frozen，实际 %T", list[0])
	}

	// 标量叶子直接取值
	v, ok = fz.NavigateSegs([]string{"uid"})
	if !ok || v != int64(42) {
		t.Fatalf("uid 应为 int64(42)，实际 (%#v,%v)", v, ok)
	}

	// 未设置 message → 不存在（与 messageToMap 跳过规则一致）
	if _, ok := fz.NavigateSegs([]string{"empty"}); ok {
		t.Fatal("未设置的 empty 应返回 found=false")
	}

	// nil 封装
	if Freeze(nil) != nil {
		t.Fatal("Freeze(nil) 应返回 nil")
	}
}

// TestFrozenStateIntegration 校验整存 Frozen 后 state 层的读路径：
// GetPath 穿透导航、Get 返回同一引用（不可变值按引用直通）、NavigatePath 等价。
func TestFrozenStateIntegration(t *testing.T) {
	f := newStoreTestFactory(t)
	bag := buildStoreTestBag(t, f)
	fz := Freeze(bag)

	st := state.NewStore()
	st.SetPath("loginResp", fz)

	// 标量路径
	if got := st.GetPath("loginResp.uid"); got != int64(42) {
		t.Fatalf("loginResp.uid=%#v want int64(42)", got)
	}
	// 嵌套默认值：main 已设置，name 未携带 → 默认 ""（在场，不为 nil）
	if got := st.GetPath("loginResp.main.name"); got != "" {
		t.Fatalf("loginResp.main.name=%#v want \"\"", got)
	}
	// 列表下标 + 下探
	if got := st.GetPath("loginResp.items[1].name"); got != "y" {
		t.Fatalf("loginResp.items[1].name=%#v want \"y\"", got)
	}
	// 不存在路径
	if got := st.GetPath("loginResp.empty.id"); got != nil {
		t.Fatalf("loginResp.empty.id=%#v want nil", got)
	}
	// 整键读取：返回同一 *Frozen 引用（不可变，按引用直通）
	if got := st.GetPath("loginResp"); got != any(fz) {
		t.Fatalf("GetPath(loginResp) 应返回同一 *Frozen 引用，实际 %#v", got)
	}
	if got := st.Get("loginResp"); got != any(fz) {
		t.Fatalf("Get(loginResp) 应返回同一 *Frozen 引用，实际 %#v", got)
	}
	// 公开 NavigatePath 同样穿透
	if got := state.NavigatePath(fz, "main.id"); got != int64(7) {
		t.Fatalf("NavigatePath(fz, main.id)=%#v want int64(7)", got)
	}
}

// TestFrozenMaterializeValue 校验全量物化：与 messageToMap 逐字一致，nil 安全。
func TestFrozenMaterializeValue(t *testing.T) {
	f := newStoreTestFactory(t)
	bag := buildStoreTestBag(t, f)
	fz := Freeze(bag)

	got, ok := fz.MaterializeValue().(map[string]any)
	if !ok {
		t.Fatalf("MaterializeValue 应返回 map[string]any，实际 %T", fz.MaterializeValue())
	}
	want := messageToMap(bag.ProtoReflect())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MaterializeValue=%#v want %#v", got, want)
	}
	if got := (*Frozen)(nil).MaterializeValue().(map[string]any); len(got) != 0 {
		t.Fatalf("nil Frozen 的 MaterializeValue 应为空 map，实际 %#v", got)
	}
}

// TestFrozenSetPathCOW 校验整存导航值（Frozen/WireValue 同一机制）的嵌套路径写入：
// SetPath 把导航值包进 Overlay（写时物化），只物化写路径书脊，
// 兄弟子树保留基座惰性形态，底层消息不被改写。
func TestFrozenSetPathCOW(t *testing.T) {
	f := newStoreTestFactory(t)
	bag := buildStoreTestBag(t, f)
	fz := Freeze(bag)

	st := state.NewStore()
	st.Set("playerData", fz)

	// 二级路径写入：顶层 Frozen 包 Overlay，main 子节点继续包 Overlay，id 写入覆盖层
	st.SetPath("playerData.main.id", int64(99))

	if got := st.GetPath("playerData.main.id"); got != int64(99) {
		t.Fatalf("main.id=%#v want int64(99)", got)
	}
	// 同级未写字段回落基座（main.name 默认 ""）
	if got := st.GetPath("playerData.main.name"); got != "" {
		t.Fatalf("main.name=%#v want \"\"", got)
	}
	// 兄弟子树保留且仍可导航
	if got := st.GetPath("playerData.uid"); got != int64(42) {
		t.Fatalf("uid=%#v want int64(42)（COW 不应清掉兄弟字段）", got)
	}
	if got := st.GetPath("playerData.items[1].name"); got != "y" {
		t.Fatalf("items[1].name=%#v want \"y\"", got)
	}
	// 底层共享消息未被改写（COW 隔离：可能被去重缓存共享给其他机器人）
	if v, err := f.GetField(bag, "main.id"); err != nil || v != int64(7) {
		t.Fatalf("底层消息 main.id=(%#v,%v) want (7,nil)——COW 不得改写共享消息", v, err)
	}

	// 顶层是 Overlay（基座 + 覆盖层），未触碰的兄弟字段不在覆盖层里（无级联展开）
	if _, isOverlay := st.Get("playerData").(*state.Overlay); !isOverlay {
		t.Fatalf("playerData 应为 *state.Overlay，实际 %T", st.Get("playerData"))
	}
	// 未触碰的 map 字段仍经基座惰性导航
	if got := st.GetPath("playerData.shelf.s1.id"); got != int64(9) {
		t.Fatalf("shelf.s1.id=%#v want int64(9)", got)
	}

	// 列表元素内的写入：[N] 段导航到 *Frozen 元素后同样包 Overlay
	st.SetPath("playerData.items[0].id", int64(100))
	if got := st.GetPath("playerData.items[0].id"); got != int64(100) {
		t.Fatalf("items[0].id=%#v want int64(100)", got)
	}
	if got := st.GetPath("playerData.items[0].name"); got != "x" {
		t.Fatalf("items[0].name=%#v want \"x\"（元素物化不应丢兄弟字段）", got)
	}

	// 终端整体覆盖：目标位置是导航值时直接替换（不物化终端）
	st.SetPath("playerData.main", "replaced")
	if got := st.GetPath("playerData.main"); got != "replaced" {
		t.Fatalf("main=%#v want \"replaced\"", got)
	}

	// 全量物化：基座 + 覆盖层合成纯 map 树
	m, ok := st.Get("playerData").(*state.Overlay).MaterializeValue().(map[string]any)
	if !ok {
		t.Fatal("Overlay.MaterializeValue 应返回 map[string]any")
	}
	if m["main"] != "replaced" {
		t.Fatalf("物化后 main=%#v want \"replaced\"", m["main"])
	}
	if m["uid"] != int64(42) {
		t.Fatalf("物化后 uid=%#v want int64(42)", m["uid"])
	}
	items, _ := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("物化后 items 长度=%d want 2", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != int64(100) || first["name"] != "x" {
		t.Fatalf("物化后 items[0]=%#v want id=100 name=x", first)
	}
}
