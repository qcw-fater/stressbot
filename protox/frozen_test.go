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
// GetPath 穿透导航、Get 返回同一引用（零拷贝直通 deepCopyValue）、NavigatePath 等价。
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
	// 整键读取：返回同一 *Frozen 引用（不可变，deepCopyValue 直通）
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
