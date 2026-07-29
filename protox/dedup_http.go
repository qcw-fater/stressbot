package protox

import (
	"encoding/json"
	"net/http"
	"sync"
)

// ── 去重缓存观测端点 ─────────────────────────────────────────
//
// 仿 expvar 的做法在 init 时把 /debug/dedup 挂到 http.DefaultServeMux：
// 凡启用了 pprof 调试服务（utils.StartPprofServer 用的就是 DefaultServeMux）
// 的进程自动获得该端点，无需各模式（standalone / agent / admin）分别接线。
//
// 输出 JSON 对象：wire = 留存去重（WireCache，字节共享），frozenDecoded = 消费
// 去重（FrozenCache，解码共享）。正常各一个（一个 Factory 各挂一个）。判读方法：
//   - hits/(hits+misses) 为命中率——广播为主的负载应显著大于 0；
//   - hits≈0 且 misses 高速增长 = 接入了逐机器人唯一的独占推送（污染）；
//   - evictions 高速增长 = 相异内容插入速率超出容量，正在挤掉可复用条目。

var (
	statsMu           sync.Mutex
	statsCaches       []*WireCache
	statsFrozenCaches []*FrozenCache
)

func registerCacheForStats(c *WireCache) {
	statsMu.Lock()
	statsCaches = append(statsCaches, c)
	statsMu.Unlock()
}

func registerFrozenCacheForStats(c *FrozenCache) {
	statsMu.Lock()
	statsFrozenCaches = append(statsFrozenCaches, c)
	statsMu.Unlock()
}

// unregisterCacheForStats 从统计列表按身份移除（Factory.Close 调用）。
// 不移除的话包级切片会钉住缓存及其全部条目，跨任务累积泄漏。
func unregisterCacheForStats(c *WireCache) {
	statsMu.Lock()
	for i, x := range statsCaches {
		if x == c {
			statsCaches = append(statsCaches[:i], statsCaches[i+1:]...)
			break
		}
	}
	statsMu.Unlock()
}

func unregisterFrozenCacheForStats(c *FrozenCache) {
	statsMu.Lock()
	for i, x := range statsFrozenCaches {
		if x == c {
			statsFrozenCaches = append(statsFrozenCaches[:i], statsFrozenCaches[i+1:]...)
			break
		}
	}
	statsMu.Unlock()
}

func init() {
	http.HandleFunc("/debug/dedup", func(w http.ResponseWriter, _ *http.Request) {
		statsMu.Lock()
		caches := make([]*WireCache, len(statsCaches))
		copy(caches, statsCaches)
		frozenCaches := make([]*FrozenCache, len(statsFrozenCaches))
		copy(frozenCaches, statsFrozenCaches)
		statsMu.Unlock()

		out := struct {
			Wire          []DedupStats       `json:"wire"`
			FrozenDecoded []FrozenDedupStats `json:"frozenDecoded"`
		}{
			Wire:          make([]DedupStats, 0, len(caches)),
			FrozenDecoded: make([]FrozenDedupStats, 0, len(frozenCaches)),
		}
		for _, c := range caches {
			out.Wire = append(out.Wire, c.Stats())
		}
		for _, c := range frozenCaches {
			out.FrozenDecoded = append(out.FrozenDecoded, c.Stats())
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})
}
