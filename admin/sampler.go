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
	tasks      *TaskStore

	mu      sync.Mutex
	current *samplerJob
}

type samplerJob struct {
	taskID    string
	startedAt time.Time
	cancel    context.CancelFunc
}

func NewSampler(interval time.Duration, agg *MetricsAggregator, hist *HistoryStore, reg *AgentRegistry, tasks *TaskStore) *Sampler {
	return &Sampler{
		interval:   interval,
		aggregator: agg,
		history:    hist,
		registry:   reg,
		tasks:      tasks,
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
			point.StageIndex = s.currentStageIndex(taskID)
			if err := s.history.AppendTimeseries(context.Background(), taskID, point); err != nil {
				stresslog.Warn("[SAMPLER] 时序趋势数据写入失败",
					zap.String("taskID", taskID), zap.Error(err))
				continue
			}
			lastSavedElapsed = elapsed
		}
	}
}

// currentStageIndex 返回采样时刻活跃任务所属的阶段段落号。
//   - 非 ramp-up 或无 reset：-1（不参与段落过滤；非 reset 阶段线由前端按配置近似绘制）。
//   - 有 reset：已观测到的不同 reset 上报数量 + 1（reset 前=段1，第 k 次 reset 后=段 k+1），
//     与归档段号映射一致。
func (s *Sampler) currentStageIndex(taskID string) int {
	if s.tasks == nil {
		return -1
	}
	task, ok := s.tasks.Get(taskID)
	if !ok || task == nil {
		return -1
	}
	plan := buildStagePlan(task.Config.RobotConfig.RampUp)
	if !plan.HasReset {
		return -1
	}
	seg := distinctResetCount(task.StageReports) + 1
	if seg > len(plan.Segments) {
		seg = len(plan.Segments)
	}
	return seg
}

// distinctResetCount 统计阶段段落报告中不同 StageIndex（>0）的数量。
func distinctResetCount(reports []TaskCompletionReport) int {
	seen := map[int]struct{}{}
	for _, r := range reports {
		if r.StageIndex > 0 {
			seen[r.StageIndex] = struct{}{}
		}
	}
	return len(seen)
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
	point.AvgMemPercent = sys.AvgMemPercent
	point.MaxMemPercent = sys.MaxMemPercent
	if stress == nil || stress.Snapshot == nil {
		return point
	}

	snap := stress.Snapshot
	point.BotsRunning = int(snap.Robots.Running)
	point.BotsErrored = int(snap.Robots.Errored)
	point.SendKBps = snap.Bandwidth.SendMBps * 1024
	point.RecvKBps = snap.Bandwidth.RecvMBps * 1024

	var rttWeight, totalDurationWeight float64
	var rttAvg, rttP95, rttP99, totalDurationAvg, totalDurationP95, totalDurationP99 float64
	var clientAvg, encodeAvg, decodeAvg float64
	var clientWeight float64
	for _, action := range snap.Actions {
		point.TotalQPS += action.AvgQPS
		if action.RTTSampleCount > 0 {
			weight := float64(action.RTTSampleCount)
			point.RTTApdex += action.RTTApdex * weight
			rttAvg += action.RTT.AvgMs * weight
			rttP95 += action.RTT.P95Ms * weight
			rttP99 += action.RTT.P99Ms * weight
			rttWeight += weight
		}
		if action.TotalDurationSampleCount > 0 {
			weight := float64(action.TotalDurationSampleCount)
			point.TotalDurationApdex += action.TotalDurationApdex * weight
			totalDurationAvg += action.TotalDuration.AvgMs * weight
			totalDurationP95 += action.TotalDuration.P95Ms * weight
			totalDurationP99 += action.TotalDuration.P99Ms * weight
			totalDurationWeight += weight
		}
		if action.SampleCount > 0 {
			weight := float64(action.SampleCount)
			clientAvg += action.ClientAvgMs * weight
			encodeAvg += action.EncodeAvgMs * weight
			decodeAvg += action.DecodeAvgMs * weight
			clientWeight += weight
		}
	}
	if rttWeight > 0 {
		point.RTTApdex = point.RTTApdex / rttWeight
		point.RTTAvgMs = rttAvg / rttWeight
		point.RTTP95Ms = rttP95 / rttWeight
		point.RTTP99Ms = rttP99 / rttWeight
	}
	if totalDurationWeight > 0 {
		point.TotalDurationApdex = point.TotalDurationApdex / totalDurationWeight
		point.TotalDurationAvgMs = totalDurationAvg / totalDurationWeight
		point.TotalDurationP95Ms = totalDurationP95 / totalDurationWeight
		point.TotalDurationP99Ms = totalDurationP99 / totalDurationWeight
	}
	if clientWeight > 0 {
		point.ClientAvgMs = clientAvg / clientWeight
		point.EncodeAvgMs = encodeAvg / clientWeight
		point.DecodeAvgMs = decodeAvg / clientWeight
	}
	point.TotalQPS = math.Round(point.TotalQPS*100) / 100
	point.RTTApdex = math.Round(point.RTTApdex*10000) / 10000
	point.TotalDurationApdex = math.Round(point.TotalDurationApdex*10000) / 10000
	point.SendKBps = math.Round(point.SendKBps*100) / 100
	point.RecvKBps = math.Round(point.RecvKBps*100) / 100
	point.AvgMemPercent = math.Round(point.AvgMemPercent*100) / 100
	point.MaxMemPercent = math.Round(point.MaxMemPercent*100) / 100
	return point
}
