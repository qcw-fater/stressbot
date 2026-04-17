// Package state 提供泛型 key-value 状态存储，用于 Robot 运行时状态管理。
// 所有字段通过字符串 key 访问，支持类型安全的 getter/setter。
// 替代原有 IRobot 接口的 80+ 个 typed getter/setter，使工具完全数据驱动。
package state

import (
	"fmt"
	"sync"
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

// GetBool 获取布尔值，不存在返回 false
func (s *Store) GetBool(key string) bool {
	s.mu.RLock()
	v, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
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
	return toInt64(v)
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

// GetBytes 获取字节切片
func (s *Store) GetBytes(key string) []byte {
	s.mu.RLock()
	v := s.data[key]
	s.mu.RUnlock()
	if v == nil {
		return nil
	}
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}

// GetList 获取列表（any 切片）
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

// GetMap 获取映射
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
	v := toInt64(s.data[key]) + 1
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
	default:
		return 0
	}
}

// toInt64 将 any 转换为 int64
func toInt64(v any) int64 {
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
	default:
		return 0
	}
}
