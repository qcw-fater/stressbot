package monitor

import (
	"math"
	"sync/atomic"
	"time"
)

const NumBuckets = 16

// 兼容内部引用
const numBuckets = NumBuckets

// bucketBoundsMs 定义桶上边界（毫秒），覆盖 0 ~ 60s+。
// buckets[i] 记录落在 (boundsMs[i-1], boundsMs[i]] 区间的样本数。
// buckets[0] 记录 == 0ms 的样本（极少见，占位）。
// 最后一个桶（index 15）为 >60000ms 的溢出桶。
var BucketBoundsMs = [NumBuckets - 1]float64{
	1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000,
}

// 兼容内部引用
var bucketBoundsMs = BucketBoundsMs

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

	// 跨节点聚合所需原始数据（omitempty 向后兼容单机模式）
	SumNs        int64   `json:"sumNs,omitempty"`
	BucketCounts []int64 `json:"bucketCounts,omitempty"`
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

	bc := make([]int64, numBuckets)
	copy(bc, bucketCounts[:])

	return HistogramSnapshot{
		Count:        count,
		MinMs:        float64(minMs),
		MaxMs:        float64(h.maxMs.Load()),
		AvgMs:        float64(h.sumNs.Load()) / float64(count) / 1e6,
		P50Ms:        percentileFromBuckets(bucketCounts, count, 0.50),
		P90Ms:        percentileFromBuckets(bucketCounts, count, 0.90),
		P95Ms:        percentileFromBuckets(bucketCounts, count, 0.95),
		P99Ms:        percentileFromBuckets(bucketCounts, count, 0.99),
		SumNs:        h.sumNs.Load(),
		BucketCounts: bc,
	}
}

// MergeHistograms 合并多个直方图快照，重建百分位值。
// 用于分布式场景下合并多 Agent 的延迟数据。
func MergeHistograms(snaps []HistogramSnapshot) HistogramSnapshot {
	var merged HistogramSnapshot
	merged.MinMs = math.MaxFloat64
	buckets := make([]int64, NumBuckets)

	for _, s := range snaps {
		if s.Count == 0 {
			continue
		}
		merged.Count += s.Count
		merged.SumNs += s.SumNs
		if s.MinMs < merged.MinMs {
			merged.MinMs = s.MinMs
		}
		if s.MaxMs > merged.MaxMs {
			merged.MaxMs = s.MaxMs
		}
		for i, c := range s.BucketCounts {
			if i < NumBuckets {
				buckets[i] += c
			}
		}
	}
	if merged.Count == 0 {
		return HistogramSnapshot{}
	}
	merged.AvgMs = float64(merged.SumNs) / float64(merged.Count) / 1e6

	var bc [NumBuckets]int64
	copy(bc[:], buckets)
	merged.P50Ms = percentileFromBuckets(bc, merged.Count, 0.50)
	merged.P90Ms = percentileFromBuckets(bc, merged.Count, 0.90)
	merged.P95Ms = percentileFromBuckets(bc, merged.Count, 0.95)
	merged.P99Ms = percentileFromBuckets(bc, merged.Count, 0.99)
	merged.BucketCounts = buckets
	return merged
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
