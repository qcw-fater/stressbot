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

// TestSetPath_StoreResponseSim_GuildInfo 模拟 storeResponse 写入 playerData.guildInfo 的完整流程：
//  1. 登录时 LoginPlayerDataS2C 不含 guildInfo（没战队），playerData 不含 guildInfo
//  2. CreateGuild 成功，GuildCreateS2C.data 字段写入 playerData.guildInfo
//  3. 条件 state:playerData.guildInfo != nil && playerData.guildInfo.guildId != 0 应为 true
//  4. 子字段 playerData.guildInfo.mydata.position 可正确导航
func TestSetPath_StoreResponseSim_GuildInfo(t *testing.T) {
	s := NewStore()

	// --- 阶段 1：登录，LoginPlayerDataS2C 不含 guildInfo（proto3 未设置的 message 字段被跳过） ---
	loginFieldMap := map[string]any{
		"itemData": []any{"sword", "shield"},
		"funcList": []any{map[string]any{"id": 1}},
		"heroData": "someHero",
		// 注意：没有 guildInfo 字段
	}
	s.Set("playerData", loginFieldMap)

	// 验证：此时 guildInfo 不存在
	if v := s.GetPath("playerData.guildInfo"); v != nil {
		t.Fatalf("阶段1: playerData.guildInfo 应为 nil, got %v", v)
	}

	// --- 阶段 2：模拟 storeResponse 将 GuildCreateS2C.data 写入 playerData.guildInfo ---
	// GuildCreateS2C.data 的类型是 GuildLoginInfo，结构如下：
	guildLoginInfo := map[string]any{
		"guildId":       int64(12345),
		"guildGamePlay": map[string]any{},
		"baseInfo":      map[string]any{"name": "testGuild"},
		"mydata":        map[string]any{"position": int32(0), "playerId": int32(100)},
		"baseSetting":   map[string]any{},
	}

	// 这是 storeResponse 实际调用的: ae.store.SetPath(m.Setter, val)
	// setter = "playerData.guildInfo", val = guildLoginInfo
	s.SetPath("playerData.guildInfo", guildLoginInfo)

	// --- 阶段 3：验证嵌套写入结果 ---

	// 3a. playerData.guildInfo 整体不为 nil
	got := s.GetPath("playerData.guildInfo")
	if got == nil {
		t.Fatal("阶段3a: playerData.guildInfo 不应为 nil")
	}

	// 3b. guildId 可正确读取
	gotGuildId := s.GetPath("playerData.guildInfo.guildId")
	if gotGuildId != int64(12345) {
		t.Fatalf("阶段3b: guildId = %v, want 12345", gotGuildId)
	}

	// 3c. 条件: playerData.guildInfo != nil → true
	gotGuildInfo := s.GetPath("playerData.guildInfo")
	if gotGuildInfo == nil {
		t.Fatal("阶段3c: playerData.guildInfo != nil 应为 true")
	}

	// 3d. 条件: playerData.guildInfo.guildId != 0 → true
	if gotGuildId == nil || gotGuildId == int64(0) {
		t.Fatal("阶段3d: playerData.guildInfo.guildId != 0 应为 true")
	}

	// 3e. 子字段 mydata.position 可正确导航
	gotPosition := s.GetPath("playerData.guildInfo.mydata.position")
	if gotPosition != int32(0) {
		t.Fatalf("阶段3e: mydata.position = %v, want 0", gotPosition)
	}

	// --- 阶段 4：验证 playerData 原有字段未被覆盖 ---
	gotItemData := s.GetPath("playerData.itemData")
	if gotItemData == nil {
		t.Fatal("阶段4: playerData.itemData 应保留")
	}
	gotHeroData := s.GetPath("playerData.heroData")
	if gotHeroData != "someHero" {
		t.Fatalf("阶段4: playerData.heroData = %v, want someHero", gotHeroData)
	}

	// --- 阶段 5：第二次写入（如 GetGuildInfo 更新战队信息）不应丢失其他 playerData 字段 ---
	updatedGuildInfo := map[string]any{
		"guildId":  int64(99999),
		"mydata":   map[string]any{"position": int32(1), "playerId": int32(100)},
		"baseInfo": map[string]any{"name": "updatedGuild"},
	}
	s.SetPath("playerData.guildInfo", updatedGuildInfo)

	// 原有字段仍在
	if s.GetPath("playerData.heroData") != "someHero" {
		t.Fatal("阶段5: 更新 guildInfo 后 playerData.heroData 应保留")
	}
	// guildId 已更新
	if s.GetPath("playerData.guildInfo.guildId") != int64(99999) {
		t.Fatal("阶段5: guildId 应已更新为 99999")
	}
	// position 已更新
	if s.GetPath("playerData.guildInfo.mydata.position") != int32(1) {
		t.Fatal("阶段5: mydata.position 应已更新为 1")
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

// TestGetPath_ConcurrentWithWriters 校验 GetPath 的锁内导航纪律：并发写方（就地改嵌套 map 的
// SetPath / 心跳式 Increment）与 GetPath 读方同时访问同一子树时，导航全程持读锁不崩溃、无 race。
// 注：P1b 单写方化后，生产环境嵌套容器写只发生在执行器 goroutine，GetPath 返回值为内部别名
// 仅供执行器消费；本测的跨 goroutine 写方保留，仍覆盖"导航与写并发"这一锁纪律（读方不遍历
// 返回值，只做锁内导航——与 pump 侧顶层标量访问 vs 执行器写的真实并发形态一致）。
func TestGetPath_ConcurrentWithWriters(t *testing.T) {
	s := NewStore()
	s.SetPath("root.branch.leaf", 0)

	stop := make(chan struct{})
	var writers sync.WaitGroup

	// 写方 1：listen 回调式，就地改嵌套 map 的兄弟键（污染 root.branch 这张 map）。
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.SetPath("root.branch.sibling", i)
			}
		}
	}()

	// 写方 2：心跳式 stateCounter 自增（写 Store）。
	writers.Add(1)
	go func() {
		defer writers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Increment("counter")
			}
		}
	}()

	// 读方：主流程条件求值式，深度遍历同一子树，固定圈数后结束。
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for i := 0; i < 100000; i++ {
			_ = s.GetPath("root.branch.leaf")
			_ = s.GetPath("root.branch")
			_ = s.GetPath("counter")
		}
	}()

	reader.Wait()  // 读方跑满固定圈数
	close(stop)    // 通知写方退出
	writers.Wait() // 回收写方，避免测试结束后残留 goroutine
}
