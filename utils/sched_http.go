package utils

import (
	"encoding/json"
	"net/http"
	"runtime/metrics"
	"sync"
)

// ── 调度延迟观测端点 ─────────────────────────────────────────
//
// /debug/sched 输出 Go 运行时调度延迟直方图（goroutine 从就绪到实际上 CPU 的
// 等待时间）的分位数。用途：量化施压机自身负载对延迟测量（Apdex/P99）的污染
// ——响应到达后从 netpoller 唤醒到打时间戳之间的排队延迟会被计入"响应耗时"，
// 这里的 p99 就是施压机给每个样本加的水分上界。
//
// 与 /debug/dedup 同思路：init 挂 DefaultServeMux，启用 pprof 调试服务的进程
// 自动获得。输出两组：sinceStart（进程累计）与 sinceLast（距上次抓取的增量，
// 压测中定期抓取时看这组——累计值会被启动阶段/空闲期稀释）。

const schedLatencyMetric = "/sched/latencies:seconds"

var (
	schedMu   sync.Mutex
	schedLast *metrics.Float64Histogram
)

// SchedLatencySummary 一段窗口内的调度延迟分位数（毫秒）。
type SchedLatencySummary struct {
	Count  uint64  `json:"count"`
	P50Ms  float64 `json:"p50Ms"`
	P90Ms  float64 `json:"p90Ms"`
	P99Ms  float64 `json:"p99Ms"`
	P999Ms float64 `json:"p999Ms"`
	MaxMs  float64 `json:"maxMs"`
}

func readSchedHistogram() *metrics.Float64Histogram {
	samples := []metrics.Sample{{Name: schedLatencyMetric}}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindFloat64Histogram {
		return nil
	}
	// Float64Histogram 的底层切片会被下次 Read 复用，做独立拷贝。
	h := samples[0].Value.Float64Histogram()
	cp := &metrics.Float64Histogram{
		Counts:  append([]uint64(nil), h.Counts...),
		Buckets: append([]float64(nil), h.Buckets...),
	}
	return cp
}

// summarizeSchedHistogram 从直方图桶计算分位数。分位值取桶上界（悲观），
// 精度受运行时桶粒度限制（对数桶，同数量级内误差可忽略）。
func summarizeSchedHistogram(counts []uint64, buckets []float64) SchedLatencySummary {
	var total uint64
	for _, c := range counts {
		total += c
	}
	out := SchedLatencySummary{Count: total}
	if total == 0 {
		return out
	}
	// buckets 长度 = len(counts)+1；counts[i] 覆盖 [buckets[i], buckets[i+1])。
	upperMs := func(i int) float64 {
		hi := buckets[i+1]
		if hi > 1e18 { // +Inf 桶取下界
			hi = buckets[i]
		}
		return hi * 1000
	}
	quantile := func(q float64) float64 {
		target := uint64(float64(total) * q)
		var cum uint64
		for i, c := range counts {
			cum += c
			if cum > target {
				return upperMs(i)
			}
		}
		return upperMs(len(counts) - 1)
	}
	out.P50Ms = quantile(0.50)
	out.P90Ms = quantile(0.90)
	out.P99Ms = quantile(0.99)
	out.P999Ms = quantile(0.999)
	for i := len(counts) - 1; i >= 0; i-- {
		if counts[i] > 0 {
			out.MaxMs = upperMs(i)
			break
		}
	}
	return out
}

func init() {
	http.HandleFunc("/debug/sched", func(w http.ResponseWriter, _ *http.Request) {
		cur := readSchedHistogram()
		if cur == nil {
			http.Error(w, "sched latency metric unavailable", http.StatusInternalServerError)
			return
		}

		schedMu.Lock()
		last := schedLast
		schedLast = cur
		schedMu.Unlock()

		deltaCounts := cur.Counts
		if last != nil && len(last.Counts) == len(cur.Counts) {
			deltaCounts = make([]uint64, len(cur.Counts))
			for i := range cur.Counts {
				deltaCounts[i] = cur.Counts[i] - last.Counts[i]
			}
		}

		out := struct {
			SinceStart SchedLatencySummary `json:"sinceStart"`
			SinceLast  SchedLatencySummary `json:"sinceLast"`
		}{
			SinceStart: summarizeSchedHistogram(cur.Counts, cur.Buckets),
			SinceLast:  summarizeSchedHistogram(deltaCounts, cur.Buckets),
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})
}
