package admin

import (
	"context"
	"math"
	"sync"
	"time"

	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// Sampler 运行期定时采集时序数据。
type Sampler struct {
	interval   time.Duration
	aggregator *MetricsAggregator
	history    *HistoryStore
	registry   *AgentRegistry

	mu      sync.Mutex
	current *samplerJob
}

type samplerJob struct {
	taskID    string
	startedAt time.Time
	cancel    context.CancelFunc
}

func NewSampler(interval time.Duration, agg *MetricsAggregator, hist *HistoryStore, reg *AgentRegistry) *Sampler {
	return &Sampler{
		interval:   interval,
		aggregator: agg,
		history:    hist,
		registry:   reg,
	}
}

func (s *Sampler) Start(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current != nil {
		return ErrTaskConflict.WithMessage("sampler already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.current = &samplerJob{
		taskID:    taskID,
		startedAt: time.Now(),
		cancel:    cancel,
	}
	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) { s.loop(ctx, taskID, s.current.startedAt, stopCh) })
	return nil
}

func (s *Sampler) Stop(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current != nil && s.current.taskID == taskID {
		s.current.cancel()
		s.current = nil
	}
}

func (s *Sampler) loop(ctx context.Context, taskID string, startedAt time.Time, stopCh <-chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	lastSavedElapsed := -1
	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case t := <-ticker.C:
			elapsed := int(t.Sub(startedAt).Seconds())
			if lastSavedElapsed >= 0 && elapsed-lastSavedElapsed < historySaveIntervalSec(elapsed) {
				continue
			}

			stress := s.aggregator.AggregateStress(taskID)
			sys := s.aggregator.AggregateSystem()
			point := buildHistoryTrendPoint(t, elapsed, stress, sys)
			if err := s.history.AppendTimeseries(context.Background(), taskID, point); err != nil {
				stresslog.Warn("[SAMPLER] 时序趋势数据写入失败",
					zap.String("taskId", taskID), zap.Error(err))
				continue
			}
			lastSavedElapsed = elapsed
		}
	}
}

func historySaveIntervalSec(elapsed int) int {
	switch {
	case elapsed < 30*60:
		return 10
	case elapsed < 6*60*60:
		return 60
	default:
		return 5 * 60
	}
}

func buildHistoryTrendPoint(sampledAt time.Time, elapsed int, stress *StressAggregate, sys ClusterSystemSnapshot) HistoryTrendPoint {
	point := HistoryTrendPoint{
		SampledAt:     sampledAt,
		ElapsedSec:    elapsed,
		AvgCPUPercent: sys.AvgCPUPercent,
		MaxCPUPercent: sys.MaxCPUPercent,
		Goroutines:    sys.TotalGoroutines,
		Threads:       int(sys.TotalThreads),
		FDs:           int(sys.TotalFDs),
		OnlineCount:   sys.OnlineCount,
		OfflineCount:  sys.OfflineCount,
	}
	if sys.TotalMemMB > 0 {
		point.MemPercent = float64(sys.UsedMemMB) / float64(sys.TotalMemMB) * 100
	}
	if stress == nil || stress.Snapshot == nil {
		return point
	}

	snap := stress.Snapshot
	point.BotsRunning = int(snap.Robots.Running)
	point.BotsErrored = int(snap.Robots.Errored)
	point.SendKBps = snap.Bandwidth.SendMBps * 1024
	point.RecvKBps = snap.Bandwidth.RecvMBps * 1024

	var apdexWeight float64
	var rttAvg, rttP95, rttP99, clientAvg, encodeAvg, decodeAvg float64
	var clientWeight float64
	for _, action := range snap.Actions {
		point.TotalQPS += action.AvgQPS
		if action.RTTSampleCount > 0 {
			weight := float64(action.RTTSampleCount)
			point.Apdex += action.Apdex * weight
			rttAvg += action.RTT.AvgMs * weight
			rttP95 += action.RTT.P95Ms * weight
			rttP99 += action.RTT.P99Ms * weight
			apdexWeight += weight
		}
		if action.SampleCount > 0 {
			weight := float64(action.SampleCount)
			clientAvg += action.ClientAvgMs * weight
			encodeAvg += action.EncodeAvgMs * weight
			decodeAvg += action.DecodeAvgMs * weight
			clientWeight += weight
		}
	}
	if apdexWeight > 0 {
		point.Apdex = point.Apdex / apdexWeight
		point.RTTAvgMs = rttAvg / apdexWeight
		point.RTTP95Ms = rttP95 / apdexWeight
		point.RTTP99Ms = rttP99 / apdexWeight
	}
	if clientWeight > 0 {
		point.ClientAvgMs = clientAvg / clientWeight
		point.EncodeAvgMs = encodeAvg / clientWeight
		point.DecodeAvgMs = decodeAvg / clientWeight
	}
	point.TotalQPS = math.Round(point.TotalQPS*100) / 100
	point.Apdex = math.Round(point.Apdex*10000) / 10000
	point.SendKBps = math.Round(point.SendKBps*100) / 100
	point.RecvKBps = math.Round(point.RecvKBps*100) / 100
	point.MemPercent = math.Round(point.MemPercent*100) / 100
	return point
}
