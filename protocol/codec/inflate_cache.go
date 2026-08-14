// 解压去重缓存：内容寻址共享解压产物（内存换 CPU 的显式交易）。
//
// 动机：同一条大广播推给全部机器人，每条连接独立走一遍 gunzip——8000 接收方
// 就是 7999 次重复解压（gzip inflate 是收包管线里最贵的线性步骤）。解密后的
// 压缩字节在所有接收方之间逐字相同，按内容寻址即可把解压塌缩为一次。
//
// 防污染（018→019 教训的机制化）：请求-响应逐机器人唯一，进缓存只会白占位、
// 挤掉广播条目。本缓存采用**二见登记**：首见某内容只记 8 字节哈希标记（不存
// 字节），第二次见到才真正缓存。广播帧的"第二见"在微秒内到来，独占消息永远
// 停在标记层——零字节污染，无需知道路由类型。
//
// 共享安全前提（改动 decode 管线时必须维持）：
//   - 解压产物在 decode 返回后作为 Message.Data 只读流转（已审计：留存侧快照、
//     Lua 侧转字符串/视图，全链路无原地改写）；
//   - 只有 compress 步是 decode 反序管线的**最后执行步**时才可共享
//     （inflateShareSafe）——若其后还有原地解密等改写步骤，会污染共享字节；
//   - 碰撞防御与 WireCache 同款：哈希只做桶索引，命中判定必须全量 bytes.Equal。
//
// 生命周期：进程级全局（codec 无任务边界感知），双上界 LRU 自清洁；跨任务的
// 陈旧条目最多钉住 maxBytes，由后续驱逐自然换血。观测：/debug/inflate。
package codec

import (
	"bytes"
	"encoding/json"
	"hash/maphash"
	"net/http"
	"sync"

	"stressbot/internal/lru"
)

const (
	// inflateDedupMinBytes 参与去重的压缩字节下限：小帧解压本就便宜，
	// 不值得哈希 + 标记；大帧（广播配置类）才是重复解压的主体。
	inflateDedupMinBytes = 1024

	// inflateCacheMaxEntries / inflateCacheMaxBytes 真条目双上界
	// （字节按 压缩快照+解压产物 计，即真实钉住量）。
	inflateCacheMaxEntries = 1024
	inflateCacheMaxBytes   = 48 << 20

	// inflateMarkerCap 首见标记上限（每个 8 字节）。满后整体清空重来：
	// 最坏影响是某广播的"第二见"晚一个出现周期才登记，正确性无损。
	inflateMarkerCap = 8192
)

var inflateSeed = maphash.MakeSeed()

func inflateHash(data []byte) uint64 {
	var h maphash.Hash
	h.SetSeed(inflateSeed)
	_, _ = h.Write(data)
	return h.Sum64()
}

// inflateCache 二见登记 + 双上界 LRU。存储层由 lru.ContentLRU 承载，
// 二见登记（markers）是本缓存独有的接入层防污染逻辑。
type inflateCache struct {
	store   *lru.ContentLRU[[]byte, []byte] // K=压缩字节, V=解压产物（共享只读）
	mu      sync.Mutex                      // 保护 markers（二见登记）
	markers map[uint64]struct{}             // 首见哈希标记（二见登记的第一级）
}

func newInflateCache(maxEntries, maxBytes int) *inflateCache {
	return &inflateCache{
		store:   lru.New[[]byte, []byte](maxEntries, maxBytes, bytes.Equal),
		markers: make(map[uint64]struct{}),
	}
}

// sharedInflateCache 进程级共享实例（pump 并发调用）。
var sharedInflateCache = newInflateCache(inflateCacheMaxEntries, inflateCacheMaxBytes)

// get 命中返回共享解压产物（调用方只读），未命中返回 nil。
func (c *inflateCache) get(comp []byte) []byte {
	out, ok := c.store.Lookup(inflateHash(comp), comp)
	if !ok {
		return nil
	}
	return out
}

// put 二见登记：首见只记哈希标记；再见快照压缩字节并登记真条目。
// out 自登记起即为共享只读；并发双 miss 双 put 时 Insert 的 double-check 保证以先登记者为准。
func (c *inflateCache) put(comp, out []byte) {
	h := inflateHash(comp)
	// 二见登记（接入层防污染，不进存储引擎）
	c.mu.Lock()
	// 并发竞争先手已登记（store 里已有）则跳过 markers
	if _, seen := c.markers[h]; !seen {
		if len(c.markers) >= inflateMarkerCap {
			clear(c.markers)
		}
		c.markers[h] = struct{}{}
		c.mu.Unlock()
		return
	}
	delete(c.markers, h)
	c.mu.Unlock()
	// 二见确认，登记真条目。comp 做独立快照（decode 的 work 可能来自池化缓冲）。
	compSnap := append([]byte(nil), comp...)
	c.store.Insert(compSnap, h, out, len(compSnap)+len(out))
}

// InflateDedupStats 观测快照。判读：hits/(hits+misses) 为命中率；
// entries 长期为 0 且 misses 高速增长 = 负载里没有重复大帧（缓存空转，可下线）；
// evictions 高速增长 = 相异大帧插入速率超容量，考虑调大 maxBytes 或提高阈值。
type InflateDedupStats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
	Entries   int    `json:"entries"`
	Markers   int    `json:"markers"`
	Bytes     int    `json:"bytes"`
}

func (c *inflateCache) stats() InflateDedupStats {
	s := c.store.Stats()
	c.mu.Lock()
	markers := len(c.markers)
	c.mu.Unlock()
	return InflateDedupStats{
		Hits:      s.Hits,
		Misses:    s.Misses,
		Evictions: s.Evictions,
		Entries:   s.Entries,
		Markers:   markers,
		Bytes:     s.Cost,
	}
}

func init() {
	// 仿 /debug/dedup：挂 DefaultServeMux，启用 pprof 调试服务的进程自动获得。
	http.HandleFunc("/debug/inflate", func(w http.ResponseWriter, _ *http.Request) {
		out := struct {
			Inflate       InflateDedupStats `json:"inflate"`
			RouteKeys     int               `json:"routeKeyInterned"`
			WorkBufReuses uint64            `json:"workBufReuses"`
		}{
			Inflate:       sharedInflateCache.stats(),
			RouteKeys:     routeKeyInternSize(),
			WorkBufReuses: workBufReuses.Load(),
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})
}
