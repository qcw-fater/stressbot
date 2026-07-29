package state

import (
	"reflect"
	"testing"
)

// fakeNav 模拟不可变导航基座（protox.WireValue / protox.Frozen 的 state 侧形态）：
// 用嵌套 map 树实现 PathNavigator + ValueMaterializer。
type fakeNav struct {
	tree map[string]any
}

func (f *fakeNav) NavigateSegs(segs []string) (any, bool) {
	var cur any = f.tree
	for i, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, hit := m[seg]
		if !hit {
			return nil, false
		}
		// 嵌套 message 语义：非终端 map 返回子导航器（模拟惰性子视图）。
		if sub, isMap := v.(map[string]any); isMap && i == len(segs)-1 {
			return &fakeNav{tree: sub}, true
		}
		cur = v
	}
	return cur, true
}

func (f *fakeNav) MaterializeValue() any {
	var deep func(m map[string]any) map[string]any
	deep = func(m map[string]any) map[string]any {
		out := make(map[string]any, len(m))
		for k, v := range m {
			if sub, ok := v.(map[string]any); ok {
				out[k] = deep(sub)
			} else {
				out[k] = v
			}
		}
		return out
	}
	return deep(f.tree)
}

// TestOverlayTombstone set_path(x, nil) 必须遮蔽基座旧值：
// 读回 nil、物化时删键、未删兄弟字段不受影响（listen_guild_kick_member.lua 的用法）。
func TestOverlayTombstone(t *testing.T) {
	base := &fakeNav{tree: map[string]any{
		"guild": map[string]any{
			"id":      int64(1),
			"members": map[string]any{"m1": "alice", "m2": "bob"},
		},
	}}

	st := NewStore()
	st.Set("playerData", base)

	// 删除嵌套键：基座值必须被墓碑遮蔽。
	st.SetPath("playerData.guild.members.m1", nil)

	if got := st.GetPath("playerData.guild.members.m1"); got != nil {
		t.Fatalf("被删键=%#v want nil", got)
	}
	if got := st.GetPath("playerData.guild.members.m2"); got != "bob" {
		t.Fatalf("兄弟键=%#v want \"bob\"", got)
	}
	if got := st.GetPath("playerData.guild.id"); got != int64(1) {
		t.Fatalf("guild.id=%#v want int64(1)", got)
	}

	// 物化：被删键不出现。
	ov, ok := st.Get("playerData").(*Overlay)
	if !ok {
		t.Fatalf("playerData 应为 *Overlay，实际 %T", st.Get("playerData"))
	}
	m := ov.MaterializeValue().(map[string]any)
	members := m["guild"].(map[string]any)["members"].(map[string]any)
	if _, hit := members["m1"]; hit {
		t.Fatalf("物化后被删键仍在场: %#v", members)
	}
	if !reflect.DeepEqual(members, map[string]any{"m2": "bob"}) {
		t.Fatalf("members=%#v want {m2:bob}", members)
	}

	// 删后重写：墓碑被新值覆盖。
	st.SetPath("playerData.guild.members.m1", "carol")
	if got := st.GetPath("playerData.guild.members.m1"); got != "carol" {
		t.Fatalf("重写后=%#v want \"carol\"", got)
	}

	// 删除后向被删子树继续写：墓碑转空 map 再下探（mkdir -p 语义）。
	st.SetPath("playerData.guild.stats", nil)
	st.SetPath("playerData.guild.stats.wins", int64(3))
	if got := st.GetPath("playerData.guild.stats.wins"); got != int64(3) {
		t.Fatalf("墓碑下探重建=%#v want int64(3)", got)
	}
}

// TestOverlayScopeMinimal SetPath 只在写路径书脊上创建 Overlay 节点，
// 未触碰的兄弟子树保持基座惰性形态（不物化、不产生默认值对象）。
func TestOverlayScopeMinimal(t *testing.T) {
	base := &fakeNav{tree: map[string]any{
		"a": map[string]any{"x": int64(1)},
		"b": map[string]any{"y": int64(2)},
	}}

	st := NewStore()
	st.Set("k", base)
	st.SetPath("k.a.x", int64(10))

	ov := st.Get("k").(*Overlay)
	if len(ov.overrides) != 1 {
		t.Fatalf("顶层覆盖键数=%d want 1（只有写路径上的 a）", len(ov.overrides))
	}
	if _, hit := ov.overrides["b"]; hit {
		t.Fatal("未写的兄弟 b 不应被物化进覆盖层")
	}
	// b 仍经基座读取。
	if got := st.GetPath("k.b.y"); got != int64(2) {
		t.Fatalf("k.b.y=%#v want int64(2)", got)
	}
	if got := st.GetPath("k.a.x"); got != int64(10) {
		t.Fatalf("k.a.x=%#v want int64(10)", got)
	}
}
