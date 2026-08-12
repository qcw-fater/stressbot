package monitor

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/DataDog/sketches-go/ddsketch"
	"github.com/DataDog/sketches-go/ddsketch/store"
)

// DDSketch 默认精度参数。CollectorConfig.Sketch 为零值时使用。
const (
	defaultSketchRelativeAccuracy = 0.01
	defaultSketchMaxBins          = 2048
)

var ErrInvalidMetricSample = errors.New("无效监控耗时样本")

// LatencyHistogram 使用 DDSketch 保存可合并的延迟分布，同时保存精确的
// Count/Sum/Min/Max。DDSketch 内部统一使用纳秒，展示快照再换算为毫秒。
type LatencyHistogram struct {
	mu     sync.Mutex
	sketch *ddsketch.DDSketch
	count  int64
	sumNs  int64
	minNs  int64
	maxNs  int64
}

// newLatencyHistogram 用默认精度参数创建直方图（供测试和回退使用）。
func newLatencyHistogram() *LatencyHistogram {
	return newLatencyHistogramWith(defaultSketchRelativeAccuracy, defaultSketchMaxBins)
}

// newLatencyHistogramWith 用指定 DDSketch 精度参数创建直方图。
// relativeAccuracy 越小越精确（默认 0.01 = 1%），maxBins 越大覆盖范围越广（默认 2048）。
func newLatencyHistogramWith(relativeAccuracy float64, maxBins int) *LatencyHistogram {
	if relativeAccuracy <= 0 {
		relativeAccuracy = defaultSketchRelativeAccuracy
	}
	if maxBins <= 0 {
		maxBins = defaultSketchMaxBins
	}
	sketch, err := ddsketch.LogCollapsingLowestDenseDDSketch(relativeAccuracy, maxBins)
	if err != nil {
		panic(fmt.Sprintf("初始化 DDSketch 失败: %v", err))
	}
	return &LatencyHistogram{sketch: sketch, minNs: math.MaxInt64}
}

// Record 记录一次延迟采样。负耗时不会进入分布，由调用方统计无效样本。
func (h *LatencyHistogram) Record(d time.Duration) error {
	if d < 0 {
		return ErrInvalidMetricSample
	}
	ns := d.Nanoseconds()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.sketch.Add(float64(ns)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMetricSample, err)
	}
	h.count++
	h.sumNs += ns
	if ns < h.minNs {
		h.minNs = ns
	}
	if ns > h.maxNs {
		h.maxNs = ns
	}
	return nil
}

// Reset 清空数据但保留 DDSketch store 的已分配容量。
func (h *LatencyHistogram) Reset() {
	h.mu.Lock()
	h.sketch.Clear()
	h.count = 0
	h.sumNs = 0
	h.minNs = math.MaxInt64
	h.maxNs = 0
	h.mu.Unlock()
}

// HistogramSnapshot 是 Agent/Admin 内部可合并、外部可展示的延迟快照。
// Sketch 只用于内部传输，公共 API 通过 PublicCopy 移除。
type HistogramSnapshot struct {
	Count  int64    `json:"count"`
	MinMs  *float64 `json:"minMs"`
	MaxMs  *float64 `json:"maxMs"`
	AvgMs  *float64 `json:"avgMs"`
	P50Ms  *float64 `json:"p50Ms"`
	P90Ms  *float64 `json:"p90Ms"`
	P95Ms  *float64 `json:"p95Ms"`
	P99Ms  *float64 `json:"p99Ms"`
	SumNs  int64    `json:"sumNs,omitempty"`
	Sketch []byte   `json:"sketch,omitempty"`
}

func (h *LatencyHistogram) Snapshot() HistogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return HistogramSnapshot{}
	}
	encoded := make([]byte, 0, 256)
	h.sketch.Encode(&encoded, false)
	snapshot, err := histogramSnapshotFromSketch(h.sketch, h.count, h.sumNs, h.minNs, h.maxNs, encoded)
	if err != nil {
		panic(fmt.Sprintf("生成 DDSketch 快照失败: %v", err))
	}
	return snapshot
}

func histogramSnapshotFromSketch(
	sketch *ddsketch.DDSketch,
	count, sumNs, minNs, maxNs int64,
	encoded []byte,
) (HistogramSnapshot, error) {
	if count == 0 {
		return HistogramSnapshot{}, nil
	}
	values, err := sketch.GetValuesAtQuantiles([]float64{0.50, 0.90, 0.95, 0.99})
	if err != nil {
		return HistogramSnapshot{}, fmt.Errorf("读取 DDSketch 分位数失败: %w", err)
	}
	minMs := float64(minNs) / float64(time.Millisecond)
	maxMs := float64(maxNs) / float64(time.Millisecond)
	clamp := func(ns float64) float64 {
		ms := ns / float64(time.Millisecond)
		return math.Max(minMs, math.Min(maxMs, ms))
	}
	return HistogramSnapshot{
		Count:  count,
		MinMs:  new(minMs),
		MaxMs:  new(maxMs),
		AvgMs:  new(float64(sumNs) / float64(count) / float64(time.Millisecond)),
		P50Ms:  new(clamp(values[0])),
		P90Ms:  new(clamp(values[1])),
		P95Ms:  new(clamp(values[2])),
		P99Ms:  new(clamp(values[3])),
		SumNs:  sumNs,
		Sketch: append([]byte(nil), encoded...),
	}, nil
}

// MergeHistograms 严格合并 DDSketch。任何非空快照缺少或损坏 sketch 都返回错误，
// 禁止退回固定桶或对分位数做二次平均。
func MergeHistograms(snaps []HistogramSnapshot) (HistogramSnapshot, error) {
	var mergedSketch *ddsketch.DDSketch
	var count, sumNs int64
	var minNs, maxNs int64
	minNs = math.MaxInt64
	for _, snap := range snaps {
		if snap.Count == 0 {
			continue
		}
		if snap.MinMs == nil || snap.MaxMs == nil || snap.AvgMs == nil || snap.P50Ms == nil ||
			snap.P90Ms == nil || snap.P95Ms == nil || snap.P99Ms == nil {
			return HistogramSnapshot{}, errors.New("非空延迟分布缺少展示值")
		}
		if len(snap.Sketch) == 0 {
			return HistogramSnapshot{}, errors.New("非空延迟分布缺少 DDSketch 数据")
		}
		decoded, err := ddsketch.DecodeDDSketch(snap.Sketch, store.BufferedPaginatedStoreConstructor, nil)
		if err != nil {
			return HistogramSnapshot{}, fmt.Errorf("解码 DDSketch 失败: %w", err)
		}
		if math.Round(decoded.GetCount()) != float64(snap.Count) {
			return HistogramSnapshot{}, fmt.Errorf("DDSketch 样本数不一致: sketch=%g snapshot=%d", decoded.GetCount(), snap.Count)
		}
		if mergedSketch == nil {
			mergedSketch = decoded
		} else if err := mergedSketch.MergeWith(decoded); err != nil {
			return HistogramSnapshot{}, fmt.Errorf("合并 DDSketch 失败: %w", err)
		}
		count += snap.Count
		sumNs += snap.SumNs
		candidateMinNs := int64(math.Round(*snap.MinMs * float64(time.Millisecond)))
		candidateMaxNs := int64(math.Round(*snap.MaxMs * float64(time.Millisecond)))
		if candidateMinNs < minNs {
			minNs = candidateMinNs
		}
		if candidateMaxNs > maxNs {
			maxNs = candidateMaxNs
		}
	}
	if count == 0 {
		return HistogramSnapshot{}, nil
	}
	encoded := make([]byte, 0, 256)
	mergedSketch.Encode(&encoded, false)
	return histogramSnapshotFromSketch(mergedSketch, count, sumNs, minNs, maxNs, encoded)
}

//go:fix inline
func float64Pointer(value float64) *float64 { return new(value) }

func histogramValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
