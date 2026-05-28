// Package state 提供泛型 key-value 状态存储，用于 Robot 运行时状态管理。
// 所有字段通过字符串 key 访问，支持类型安全的 getter/setter。
// 替代原有 IRobot 接口的 80+ 个 typed getter/setter，使工具完全数据驱动。
package state

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Store 泛型状态存储。
// 并发安全，用于 Robot 运行时存储登录响应、战斗状态、好友列表等中间数据。
// 每秒可能被多个协程访问（主流程 + listen 回调），因此使用 RWMutex。
type Store struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewStore 创建新的状态存储
func NewStore() *Store {
	return &Store{data: make(map[string]any)}
}

// Set 设置状态值
func (s *Store) Set(key string, value any) {
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
}

// Get 获取状态值
func (s *Store) Get(key string) any {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	return v
}

// GetInt 获取整数值，不存在返回 0
func (s *Store) GetInt(key string) int {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	return toInt(v)
}

// GetInt32 获取 int32 值
func (s *Store) GetInt32(key string) int32 {
	return int32(s.GetInt(key))
}

// GetInt64 获取 int64 值
func (s *Store) GetInt64(key string) int64 {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	return ToInt64(v)
}

// GetString 获取字符串值，不存在返回 ""
func (s *Store) GetString(key string) string {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// GetList 获取列表（any 切片）。返回内部引用，调用方不应修改返回的切片。
// 所有调用方（engine/action.go 的 binding 解析）均为只读访问，无需拷贝。
func (s *Store) GetList(key string) []any {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	if v == nil {
		return nil
	}
	if list, ok := v.([]any); ok {
		return list
	}
	return nil
}

// GetMap 获取映射。返回内部引用，调用方不应修改返回的 map。
// 所有调用方（engine/action.go 的 binding 解析）均为只读访问，无需拷贝。
func (s *Store) GetMap(key string) map[string]any {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// Delete 删除指定 key
func (s *Store) Delete(key string) {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
}

// Clear 清空所有状态
func (s *Store) Clear() {
	s.mu.Lock()
	s.data = make(map[string]any)
	s.mu.Unlock()
}

// Increment 原子递增整数值，返回递增后的值
func (s *Store) Increment(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := toInt(s.data[key]) + 1
	s.data[key] = v
	return v
}

// IncrementInt64 原子递增 int64 值
func (s *Store) IncrementInt64(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := ToInt64(s.data[key]) + 1
	s.data[key] = v
	return v
}

// Keys 返回所有 key 的列表（调试用）
func (s *Store) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	return keys
}

// Has 检查 key 是否存在
func (s *Store) Has(key string) bool {
	s.mu.RLock()
	_, ok := s.data[key]
	s.mu.RUnlock()
	return ok
}

// toInt 将 any 转换为 int
func toInt(v any) int {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return 0
	}
}

// ToInt64 将 any 转换为 int64（公开版本，供其他包使用）
func ToInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case bool:
		if n {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// ToFloat64 将 any 转换为 float64（公开版本，供其他包使用）
func ToFloat64(v any) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	default:
		return 0
	}
}

// SplitPath 将 "a.b[0].c" 拆为 ["a","b","[0]","c"]（公开版本，供其他包使用）
func SplitPath(path string) []string {
	var out []string
	var buf []byte
	flush := func() {
		if len(buf) > 0 {
			out = append(out, string(buf))
			buf = buf[:0]
		}
	}
	for i := 0; i < len(path); i++ {
		ch := path[i]
		switch ch {
		case '.':
			flush()
		case '[':
			flush()
			j := i + 1
			for j < len(path) && path[j] != ']' {
				j++
			}
			if j < len(path) {
				out = append(out, path[i:j+1])
				i = j
			}
		default:
			buf = append(buf, ch)
		}
	}
	flush()
	return out
}

// ToFloat64Safe 尝试将 any 值转换为 float64。
// 第二个返回值指示原始值是否为数字类型。
func ToFloat64Safe(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	r := ToFloat64(v)
	switch v.(type) {
	case int, int32, int64, uint, uint32, uint64, float64, float32:
		return r, true
	default:
		return 0, false
	}
}

// CompareValues 按操作符比较两个值。
// 支持：eq/==, neq/!=, gt/>, gte/>=, lt/<, lte/<=, contains, in, timeWindow, dailyTimeWindow, notNil, isNil
func CompareValues(a, b any, op string) bool {
	aNum, aIsNum := ToFloat64Safe(a)
	bNum, bIsNum := ToFloat64Safe(b)

	if a == nil && bIsNum {
		aNum, aIsNum = 0, true
	}
	if b == nil && aIsNum {
		bNum, bIsNum = 0, true
	}

	switch op {
	case "eq", "==", "":
		if aIsNum && bIsNum {
			return aNum == bNum
		}
		if a == nil && b == nil {
			return true
		}
		if a == nil || b == nil {
			return false
		}
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)

	case "neq", "!=":
		if aIsNum && bIsNum {
			return aNum != bNum
		}
		if a == nil && b == nil {
			return false
		}
		if a == nil || b == nil {
			return true
		}
		return fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b)

	case "gt", ">":
		if aIsNum && bIsNum {
			return aNum > bNum
		}
		return false

	case "gte", ">=":
		if aIsNum && bIsNum {
			return aNum >= bNum
		}
		return false

	case "lt", "<":
		if aIsNum && bIsNum {
			return aNum < bNum
		}
		return false

	case "lte", "<=":
		if aIsNum && bIsNum {
			return aNum <= bNum
		}
		return false

	case "contains":
		aStr := fmt.Sprintf("%v", a)
		bStr := fmt.Sprintf("%v", b)
		return strings.Contains(aStr, bStr)

	case "in":
		list, ok := b.([]any)
		if !ok {
			return false
		}
		for _, it := range list {
			if DeepEqual(a, it) {
				return true
			}
		}
		return false

	case "timeWindow":
		var now int
		if bIsNum {
			now = int(bNum)
		} else {
			t := time.Now()
			now = t.Hour()*60 + t.Minute()
		}
		m, ok := a.(map[string]any)
		if !ok {
			return true
		}
		start, _ := ToFloat64Safe(m["startTime"])
		end, _ := ToFloat64Safe(m["endTime"])
		return float64(now) >= start && float64(now) <= end

	case "dailyTimeWindow":
		if a == nil {
			return true
		}
		items, ok := a.([]any)
		if !ok {
			if single, isMap := a.(map[string]any); isMap {
				items = []any{single}
			} else {
				return true
			}
		}
		if len(items) == 0 {
			return true
		}
		t := time.Now()
		nowMin := t.Hour()*60 + t.Minute()
		for _, it := range items {
			entry, ok := it.(map[string]any)
			if !ok {
				continue
			}
			sh, _ := ToFloat64Safe(firstNonNil(entry["StartHour"], entry["startHour"]))
			sm, _ := ToFloat64Safe(firstNonNil(entry["StartMinute"], entry["startMinute"]))
			eh, _ := ToFloat64Safe(firstNonNil(entry["EndHour"], entry["endHour"]))
			em, _ := ToFloat64Safe(firstNonNil(entry["EndMinute"], entry["endMinute"]))
			startMin := int(sh)*60 + int(sm)
			endMin := int(eh)*60 + int(em)
			if nowMin >= startMin && nowMin <= endMin {
				return true
			}
		}
		return false

	case "notNil":
		return a != nil

	case "isNil":
		return a == nil
	}

	return false
}

// firstNonNil 返回第一个非 nil 的值。
func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// DeepEqual 简单的深度相等判断。
func DeepEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	aNum, aOk := ToFloat64Safe(a)
	bNum, bOk := ToFloat64Safe(b)
	if aOk && bOk {
		return aNum == bNum
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
