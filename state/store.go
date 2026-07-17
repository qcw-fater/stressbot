// Package state 提供泛型 key-value 状态存储，用于 Robot 运行时状态管理。
// 所有字段通过字符串 key 访问，支持类型安全的 getter/setter。
// 替代原有 IRobot 接口的 80+ 个 typed getter/setter，使工具完全数据驱动。
package state

import (
	"fmt"
	"math"
	"strconv"
	"strings"
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

// GetList 获取列表（any 切片）的顶层浅拷贝副本。
//
// 返回副本而非内部引用：Go-store 型 listen 回调在 connectionPump goroutine 内调用
// SetPath 就地改写状态容器，与执行器 goroutine 通过本方法读取同一切片并发，若返回内部
// 引用会触发 "concurrent map/slice 读写" 致命崩溃。拷贝在读锁内完成，保证与写入互斥、
// 快照自洽。元素为浅拷贝（嵌套容器仍共享底层），调用方对返回值只做只读遍历/随机选取。
func (s *Store) GetList(key string) []any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if list, ok := s.data[key].([]any); ok {
		out := make([]any, len(list))
		copy(out, list)
		return out
	}
	return nil
}

// GetMap 获取映射的顶层浅拷贝副本。
//
// 返回副本而非内部引用，原因同 GetList：避免与 SetPath 的并发就地写导致致命崩溃。
// 拷贝在读锁内完成；值为浅拷贝（嵌套容器仍共享），调用方只做只读访问。
func (s *Store) GetMap(key string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.data[key].(map[string]any); ok {
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return nil
}

// ListLen 返回 key 对应 []any 的长度；非列表或不存在返回 0。
// 相比 len(GetList(key)) 省去整表拷贝，供 listSize / 随机选取的长度判断使用。
func (s *Store) ListLen(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if list, ok := s.data[key].([]any); ok {
		return len(list)
	}
	return 0
}

// PickFromList 在读锁内对 key 对应的 []any 用 pick(len) 选出下标并返回该元素。
//
// 零拷贝：只读取选中的单个元素，避免 GetList 为「取一个」场景全表拷贝（stateFirst /
// 无过滤器的 stateRandom）。pick 在读锁内调用（下标计算不得再访问本 Store，防止重入死锁）；
// 列表为空或 pick 返回越界下标时返回 (nil,false)。返回的元素与内部共享底层（浅拷贝语义，
// 同 GetList），调用方只读使用。
func (s *Store) PickFromList(key string, pick func(n int) int) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list, ok := s.data[key].([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	idx := pick(len(list))
	if idx < 0 || idx >= len(list) {
		return nil, false
	}
	return list[idx], true
}

// PickMapKey 在读锁内用 pick(len) 选出 key 对应 map 的第 idx 个键（迭代序）并返回。
// 零拷贝：不构造 keys 切片。map 迭代序本身随机，配合 pick(len) 仍是均匀随机。
// map 为空或下标越界返回 ("",false)。
func (s *Store) PickMapKey(key string, pick func(n int) int) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.data[key].(map[string]any)
	if !ok || len(m) == 0 {
		return "", false
	}
	target := pick(len(m))
	if target < 0 || target >= len(m) {
		return "", false
	}
	i := 0
	for k := range m {
		if i == target {
			return k, true
		}
		i++
	}
	return "", false
}

// PickMapValue 在读锁内用 pick(len) 选出 key 对应 map 的第 idx 个值（迭代序）并返回。
// 语义/约束同 PickMapKey，返回的值与内部共享底层（浅拷贝语义），调用方只读使用。
func (s *Store) PickMapValue(key string, pick func(n int) int) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.data[key].(map[string]any)
	if !ok || len(m) == 0 {
		return nil, false
	}
	target := pick(len(m))
	if target < 0 || target >= len(m) {
		return nil, false
	}
	i := 0
	for _, v := range m {
		if i == target {
			return v, true
		}
		i++
	}
	return nil, false
}

// SetPath 按点分路径设置嵌套值，与 GetPath 对称。
// 路径格式同 SplitPath："key.sub.field" 或 "list[0].field"。
// 中间 map 不存在时自动创建（类似 mkdir -p）；已存在但非 map 时覆盖为新 map。
// 遇到 [N] 数组索引段时要求对应位置已是 []any 且长度足够，否则跳过不设置。
// 单段路径等价于 Set，空路径不操作。
func (s *Store) SetPath(path string, value any) {
	segments := splitPathCached(path)
	if len(segments) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 单段路径等价于 Set
	if len(segments) == 1 {
		s.data[segments[0]] = value
		return
	}

	// 第一段：从 data 取值或创建；非 map/list 时覆盖为新 map
	cur, ok := s.data[segments[0]]
	if !ok {
		cur = make(map[string]any)
		s.data[segments[0]] = cur
	} else {
		switch cur.(type) {
		case map[string]any, []any:
		default:
			cur = make(map[string]any)
			s.data[segments[0]] = cur
		}
	}

	// 中间段 [1..n-2]：导航或创建
	for i := 1; i < len(segments)-1; i++ {
		seg := segments[i]
		next := navigateValue(cur, seg)
		if next == nil {
			if isArrayIndex(seg) {
				return // 不自动创建数组元素
			}
			next = make(map[string]any)
			setInValue(cur, seg, next)
		} else {
			switch next.(type) {
			case map[string]any, []any:
				// 可继续导航
			default:
				if isArrayIndex(seg) {
					return
				}
				next = make(map[string]any)
				setInValue(cur, seg, next)
			}
		}
		cur = next
	}

	// 最后一段：写入值
	setInValue(cur, segments[len(segments)-1], value)
}

// NavigatePath 从任意值中按点分路径提取子值（公开版本，供 engine/robot 包复用）。
// 路径格式同 SplitPath，从 v 开始逐段导航 map/list。
// v 为 nil 或路径不匹配时返回 nil。
func NavigatePath(v any, path string) any {
	cur := v
	for _, seg := range splitPathCached(path) {
		if cur == nil {
			return nil
		}
		cur = navigateValue(cur, seg)
	}
	return cur
}

// isArrayIndex 判断路径段是否为数组索引（如 "[0]"）。
func isArrayIndex(seg string) bool {
	return len(seg) >= 2 && seg[0] == '[' && seg[len(seg)-1] == ']'
}

// setInValue 在 cur 的指定段写入 val。
// cur 必须为 map[string]any 或 []any；数组索引越界时跳过。
func setInValue(cur any, seg string, val any) {
	if isArrayIndex(seg) {
		idx, err := strconv.Atoi(seg[1 : len(seg)-1])
		if err != nil {
			return
		}
		list, ok := cur.([]any)
		if !ok || idx < 0 || idx >= len(list) {
			return
		}
		list[idx] = val
		return
	}
	if m, ok := cur.(map[string]any); ok {
		m[seg] = val
	}
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

// ToInt64 将 any 转换为 int64（公开版本，供其他包使用）。
// 字符串仅接受十进制整数字面量，用于承接 Lua 层为避免大整数精度丢失而保存的 int64 文本。
func ToInt64(v any) int64 {
	got, ok := ToInt64Safe(v)
	if !ok {
		return 0
	}
	return got
}

// ToInt64Safe 将 any 转换为 int64，并返回转换是否成功。
func ToInt64Safe(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > uint64(math.MaxInt64) {
			return 0, false
		}
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > uint64(math.MaxInt64) {
			return 0, false
		}
		return int64(n), true
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case string:
		got, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0, false
		}
		return got, true
	default:
		return 0, false
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

// GetPath 按点分路径从嵌套 map/list 中提取值。
// 路径格式同 SplitPath：key.subfield / list[0].field。
// 单段路径等价于 Get，无路径或路径不匹配返回 nil。
func (s *Store) GetPath(path string) any {
	segments := splitPathCached(path)
	if len(segments) == 0 {
		return nil
	}
	cur := s.Get(segments[0])
	for i := 1; i < len(segments); i++ {
		if cur == nil {
			return nil
		}
		cur = navigateValue(cur, segments[i])
	}
	return cur
}

// navigateValue 从单个值中按一段路径提取子值。
func navigateValue(cur any, seg string) any {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return nil
	}
	// 数组索引 [N]
	if len(seg) >= 2 && seg[0] == '[' && seg[len(seg)-1] == ']' {
		idxStr := seg[1 : len(seg)-1]
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			return nil
		}
		list, ok := cur.([]any)
		if !ok || idx < 0 || idx >= len(list) {
			return nil
		}
		return list[idx]
	}
	// map key
	m, ok := cur.(map[string]any)
	if !ok {
		return nil
	}
	return m[seg]
}

// pathSegmentCache 缓存路径字符串 → 分段结果（map[string][]string）。
// 路径集合由配置固定、数量有界（写一次读多次），全 Robot 共享同一 Store 类型的热路径
// （SetPath/GetPath/NavigatePath），用 sync.Map 避免每次调用都 SplitPath 分配 []string。
// 缓存值仅被上述方法按**只读**方式消费（索引 / range，绝不改写元素），跨协程共享安全。
var pathSegmentCache sync.Map

// splitPathCached 返回 path 的分段结果，命中缓存则复用同一 []string（只读消费）。
// 供 state 内部热路径使用；导出的 SplitPath 保持每次新分配，供需要可变结果的外部调用方。
func splitPathCached(path string) []string {
	if v, ok := pathSegmentCache.Load(path); ok {
		return v.([]string)
	}
	segs := SplitPath(path)
	pathSegmentCache.Store(path, segs)
	return segs
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
// 支持：eq/==, neq/!=, gt/>, gte/>=, lt/<, lte/<=, contains, notContains, in, notIn, notNil, isNil
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
		return containsValue(a, b)

	case "notContains":
		return !containsValue(a, b)

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

	case "notIn":
		return !CompareValues(a, b, "in")

	case "notNil":
		return a != nil

	case "isNil":
		return a == nil
	}

	return false
}

// containsValue 判断 a 是否包含 b：数组使用 DeepEqual，其他类型按字符串包含。
func containsValue(a, b any) bool {
	if a == nil {
		return false
	}
	if list, ok := a.([]any); ok {
		for _, it := range list {
			if DeepEqual(it, b) {
				return true
			}
		}
		return false
	}
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	return strings.Contains(aStr, bStr)
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
