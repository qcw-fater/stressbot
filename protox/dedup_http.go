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
// 输出 JSON 数组：进程内每个 WireCache（正常一个 Factory 一个）一条快照。
// 判读方法：
//   - hits/(hits+misses) 为命中率——广播为主的负载应显著大于 0；
//   - hits≈0 且 misses 高速增长 = 接入了逐机器人唯一的独占推送（污染）；
//   - evictions 高速增长 = 相异内容插入速率超出容量，正在挤掉可复用条目。

var (
	statsMu     sync.Mutex
	statsCaches []*WireCache
)

func registerCacheForStats(c *WireCache) {
	statsMu.Lock()
	statsCaches = append(statsCaches, c)
	statsMu.Unlock()
}

func init() {
	http.HandleFunc("/debug/dedup", func(w http.ResponseWriter, _ *http.Request) {
		statsMu.Lock()
		caches := make([]*WireCache, len(statsCaches))
		copy(caches, statsCaches)
		statsMu.Unlock()

		snaps := make([]DedupStats, 0, len(caches))
		for _, c := range caches {
			snaps = append(snaps, c.Stats())
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snaps)
	})
}
