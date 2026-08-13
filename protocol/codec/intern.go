// routeKey 字符串驻留：解码/编码路由键的规范实例共享。
//
// 每帧解码都要产一个路由键小字符串（剖面周期 2 亿量级的小分配），而相异的
// routeKey 全场只有几十种（cmd/act 组合有限）。驻留后热路径变为一次无分配的
// map 查找（编译器对 m[string(bytes)] 的查找不产生转换分配），返回共享的规范
// 字符串——Go 字符串不可变，跨 goroutine 共享绝对安全。
//
// 实现为 copy-on-write 表：读侧只做原子 Load + map 读（无锁、无读者计数竞争），
// 写侧整表复制 + CAS（表在预热后不再增长，写是一次性的）。容量上限防御损坏
// 帧产出无界垃圾键把表撑爆——超限后直接返回新分配串，不再驻留（正确性无损）。
package codec

import "maps"

import "sync/atomic"

// internMaxEntries routeKey 驻留表容量上限（正常负载几十条；上限只防损坏帧）。
const internMaxEntries = 4096

var routeKeyTable atomic.Pointer[map[string]string]

// internRouteKey 返回 b 对应的规范字符串；表满时退化为普通分配。
func internRouteKey(b []byte) string {
	if m := routeKeyTable.Load(); m != nil {
		if s, ok := (*m)[string(b)]; ok {
			return s
		}
	}
	s := string(b)
	for {
		old := routeKeyTable.Load()
		var oldM map[string]string
		if old != nil {
			oldM = *old
		}
		if len(oldM) >= internMaxEntries {
			return s
		}
		if canon, ok := oldM[s]; ok {
			return canon // 竞争者先插入
		}
		nm := make(map[string]string, len(oldM)+1)
		maps.Copy(nm, oldM)
		nm[s] = s
		if routeKeyTable.CompareAndSwap(old, &nm) {
			return s
		}
	}
}

// routeKeyInternSize 当前驻留条数（观测用）。
func routeKeyInternSize() int {
	if m := routeKeyTable.Load(); m != nil {
		return len(*m)
	}
	return 0
}
