package monitor

import (
	"math"
	"sync/atomic"
	"time"
)

const numBuckets = 17

// bucketBoundsMs 定义桶上边界（毫秒），覆盖 0 ~ 60s+。
// buckets[i] 记录落在 (boundsMs[i-1], boundsMs[i]] 区间的样本数。
// buckets[0] 记录 == 0ms 的样本（极少见，占位）。
// 最后一个桶（index 16）为 >60000ms 的溢出桶。
var bucketBoundsMs = [numBuckets - 1]float64{
	1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000,
}
// 区间：[0,1) [1,2) [2,5) [5,10) [10,20) [20,50) [50,100) [100,200) [200,500)
//
//	[500,1000) [1000,2000) [2000,5000) [5000,10000) [10000,30000) [30000,60000) [60000,+∞)

// LatencyHistogram 全局累积固定桶直方图。
// 所有字段纯原子操作，无 mutex。
type LatencyHistogram struct {
	count   atomic.Int64
	sumNs   atomic.Int64
	minMs   atomic.Int64
	maxMs   atomic.Int64
	buckets [numBuckets]atomic.Int64
}

func newLatencyHistogram() *LatencyHistogram {
	h := &LatencyHistogram{}
	h.minMs.Store(math.MaxInt64)
	return h
}

// Record 记录一次延迟采样。
func (h *LatencyHistogram) Record(d time.Duration) {
	ms := d.Milliseconds()

	h.count.Add(1)
	h.sumNs.Add(d.Nanoseconds())

	// 原子 CAS 更新全局最小值
	for {
		cur := h.minMs.Load()
		if ms >= cur {
			break
		}
		if h.minMs.CompareAndSwap(cur, ms) {
			break
		}
	}
	// 原子 CAS 更新全局最大值
	for {
		cur := h.maxMs.Load()
		if ms <= cur {
			break
		}
		if h.maxMs.CompareAndSwap(cur, ms) {
			break
		}
	}

	// 找到归属桶
	idx := numBuckets - 1
	for i, bound := range bucketBoundsMs {
		if float64(ms) < bound {
			idx = i
			break
		}
	}
	h.buckets[idx].Add(1)
}

// HistogramSnapshot 直方图快照（只读）。
type HistogramSnapshot struct {
	Count int64   `json:"count"`
	MinMs float64 `json:"minMs"`
	MaxMs float64 `json:"maxMs"`
	AvgMs float64 `json:"avgMs"`
	P50Ms float64 `json:"p50Ms"`
	P90Ms float64 `json:"p90Ms"`
	P95Ms float64 `json:"p95Ms"`
	P99Ms float64 `json:"p99Ms"`
}

// Snapshot 计算当前快照。
func (h *LatencyHistogram) Snapshot() HistogramSnapshot {
	count := h.count.Load()
	if count == 0 {
		return HistogramSnapshot{}
	}
	minMs := h.minMs.Load()
	if minMs == math.MaxInt64 {
		minMs = 0
	}

	var bucketCounts [numBuckets]int64
	for i := range bucketCounts {
		bucketCounts[i] = h.buckets[i].Load()
	}

	return HistogramSnapshot{
		Count: count,
		MinMs: float64(minMs),
		MaxMs: float64(h.maxMs.Load()),
		AvgMs: float64(h.sumNs.Load()) / float64(count) / 1e6,
		P50Ms: percentileFromBuckets(bucketCounts, count, 0.50),
		P90Ms: percentileFromBuckets(bucketCounts, count, 0.90),
		P95Ms: percentileFromBuckets(bucketCounts, count, 0.95),
		P99Ms: percentileFromBuckets(bucketCounts, count, 0.99),
	}
}

// percentileFromBuckets 用桶计数前缀和 + 线性插值估算百分位（毫秒）。
func percentileFromBuckets(counts [numBuckets]int64, total int64, p float64) float64 {
	rank := int64(math.Ceil(p * float64(total)))
	var cumulative int64
	lo := 0.0
	for i := 0; i < numBuckets; i++ {
		hi := 60001.0
		if i < len(bucketBoundsMs) {
			hi = bucketBoundsMs[i]
		}
		cumulative += counts[i]
		if cumulative >= rank {
			inBucket := counts[i]
			if inBucket == 0 {
				return lo
			}
			prevSum := cumulative - inBucket
			fraction := float64(rank-prevSum) / float64(inBucket)
			return lo + fraction*(hi-lo)
		}
		lo = hi
	}
	return lo
}
