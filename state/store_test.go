package state

import (
	"reflect"
	"sync"
	"testing"
)

func TestSetPath_FlatKey(t *testing.T) {
	s := NewStore()
	s.SetPath("key", 42)
	if v := s.Get("key"); v != 42 {
		t.Errorf("SetPath flat key: got %v, want 42", v)
	}
}

func TestSetPath_SimpleNested(t *testing.T) {
	s := NewStore()
	s.SetPath("a.b", 1)
	if v := s.GetPath("a.b"); v != 1 {
		t.Errorf("SetPath a.b: got %v, want 1", v)
	}
	m := s.GetMap("a")
	if m == nil {
		t.Fatal("a should be a map")
	}
	if m["b"] != 1 {
		t.Errorf("a[b]: got %v, want 1", m["b"])
	}
}

func TestSetPath_DeepNested(t *testing.T) {
	s := NewStore()
	s.SetPath("a.b.c.d", "deep")
	if v := s.GetPath("a.b.c.d"); v != "deep" {
		t.Errorf("SetPath deep: got %v, want deep", v)
	}
}

func TestSetPath_PreserveSiblingFields(t *testing.T) {
	s := NewStore()
	s.SetPath("loginResp.id", 100)
	s.SetPath("loginResp.name", "test")
	if v := s.GetPath("loginResp.id"); v != 100 {
		t.Errorf("loginResp.id: got %v, want 100", v)
	}
	if v := s.GetPath("loginResp.name"); v != "test" {
		t.Errorf("loginResp.name: got %v, want test", v)
	}
}

func TestSetPath_TypeConflictOverwrite(t *testing.T) {
	s := NewStore()
	s.Set("x", "string")
	s.SetPath("x.y", 1)
	if v := s.GetPath("x.y"); v != 1 {
		t.Errorf("type conflict: got %v, want 1", v)
	}
	m := s.GetMap("x")
	if m == nil {
		t.Fatal("x should have been overwritten to map")
	}
}

func TestSetPath_ArrayIndexModify(t *testing.T) {
	s := NewStore()
	s.Set("list", []any{"a", "b", "c"})
	s.SetPath("list[1]", "replaced")
	if v := s.GetPath("list[1]"); v != "replaced" {
		t.Errorf("array index: got %v, want replaced", v)
	}
}

func TestSetPath_ArrayIndexOutOfRange(t *testing.T) {
	s := NewStore()
	s.Set("list", []any{1})
	s.SetPath("list[5]", "x")
	if v := s.GetPath("list[0]"); v != 1 {
		t.Errorf("out of range: got %v, want 1", v)
	}
}

func TestSetPath_ArrayIndexNonExistent(t *testing.T) {
	s := NewStore()
	s.SetPath("noexist[0].field", 1)
	// [0] 段无法导航（noexist 是空 map，不是数组），操作跳过
	// 但 noexist 作为中间 map 已被创建
	m := s.GetMap("noexist")
	if m == nil {
		t.Fatal("noexist should exist as map (created as intermediate)")
	}
	if len(m) != 0 {
		t.Errorf("noexist should be empty map, got %v", m)
	}
}

func TestSetPath_EmptyPath(t *testing.T) {
	s := NewStore()
	s.SetPath("", 1)
	if v := s.Get(""); v != nil {
		t.Errorf("empty path: got %v, want nil", v)
	}
}

func TestSetPath_SingleSegment(t *testing.T) {
	s := NewStore()
	s.SetPath("solo", true)
	if v := s.Get("solo"); v != true {
		t.Errorf("single segment: got %v, want true", v)
	}
}

func TestSetPath_MixedSetAndSetPath(t *testing.T) {
	s := NewStore()
	s.Set("m", map[string]any{"x": 1})
	s.SetPath("m.y", 2)
	if v := s.GetPath("m.x"); v != 1 {
		t.Errorf("m.x: got %v, want 1", v)
	}
	if v := s.GetPath("m.y"); v != 2 {
		t.Errorf("m.y: got %v, want 2", v)
	}
}

func TestSetPath_ArrayNestedField(t *testing.T) {
	s := NewStore()
	s.Set("items", []any{
		map[string]any{"id": 1, "name": "a"},
		map[string]any{"id": 2, "name": "b"},
	})
	s.SetPath("items[0].name", "updated")
	if v := s.GetPath("items[0].name"); v != "updated" {
		t.Errorf("array nested: got %v, want updated", v)
	}
	if v := s.GetPath("items[1].name"); v != "b" {
		t.Errorf("sibling: got %v, want b", v)
	}
}

func TestNavigatePath(t *testing.T) {
	data := map[string]any{
		"heroList": []any{
			map[string]any{"heroId": 100, "level": 5},
		},
	}
	tests := []struct {
		path    string
		want    any
		wantNil bool
	}{
		{"heroList[0].heroId", 100, false},
		{"heroList[0].level", 5, false},
		{"heroList[1]", nil, true},
		{"heroList[0].missing", nil, true},
	}
	for _, tt := range tests {
		got := NavigatePath(data, tt.path)
		if tt.wantNil {
			if got != nil {
				t.Errorf("NavigatePath(%q): got %v, want nil", tt.path, got)
			}
		} else if got != tt.want {
			t.Errorf("NavigatePath(%q): got %v, want %v", tt.path, got, tt.want)
		}
	}
	// 空路径返回原始值
	got := NavigatePath(data, "")
	if !reflect.DeepEqual(got, data) {
		t.Errorf("NavigatePath empty: got %v, want original data", got)
	}
}

func TestSetPath_Concurrent(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.SetPath("shared.field", i)
		}()
		go func() {
			defer wg.Done()
			s.SetPath("shared.other", i)
		}()
	}
	wg.Wait()
	m := s.GetMap("shared")
	if m == nil {
		t.Fatal("shared should exist as map")
	}
	if _, ok := m["field"]; !ok {
		t.Error("shared.field should exist")
	}
	if _, ok := m["other"]; !ok {
		t.Error("shared.other should exist")
	}
}
