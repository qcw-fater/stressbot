package admin

import (
	"context"
	"fmt"
	"sync"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// Sampler 运行期定时采集时序数据。
type Sampler struct {
	interval   time.Duration
	aggregator *MetricsAggregator
	history    historyWriter
	windows    *MetricsWindowStore
	registry   *AgentRegistry
	tasks      *TaskStore

	mu      sync.Mutex
	current *samplerJob
}

type historyWriter interface {
	AppendTimeseries(context.Context, string, HistoryTrendPoint) error
}

type samplerJob struct {
	taskID    string
	startedAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewSampler(interval time.Duration, agg *MetricsAggregator, hist historyWriter, reg *AgentRegistry, tasks *TaskStore, windows *MetricsWindowStore) *Sampler {
	return &Sampler{
		interval:   interval,
		aggregator: agg,
		history:    hist,
		windows:    windows,
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
	job := &samplerJob{
		taskID:    taskID,
		startedAt: time.Now(),
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	s.current = job
	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
		defer close(job.done)
		s.loop(ctx, taskID, job.startedAt, stopCh)
	})
	return nil
}

func (s *Sampler) Stop(taskID string) {
	s.mu.Lock()
	job := s.current
	if job == nil || job.taskID != taskID {
		s.mu.Unlock()
		return
	}
	job.cancel()
	s.current = nil
	s.mu.Unlock()

	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		stresslog.Warn("[SAMPLER] 等待采样循环退出超时", zap.String("taskID", taskID))
	}

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err := s.sampleOnce(ctx, taskID, now, int(now.Sub(job.startedAt).Seconds()))
	cancel()
	if err != nil {
		stresslog.Warn("[SAMPLER] 终态指标冲刷失败，进入后台重试", zap.String("taskID", taskID), zap.Error(err))
		s.retryFinalFlush(taskID, job.startedAt)
	}
}

func (s *Sampler) retryFinalFlush(taskID string, startedAt time.Time) {
	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
		policy := utils.NewExponentialBackOff(utils.RetryPolicy{
			Initial: time.Second,
			Max:     30 * time.Second,
			Factor:  2,
			Jitter:  0.5,
		})
		_ = utils.RetryWithStop(stopCh, func() error {
			now := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := s.sampleOnce(ctx, taskID, now, int(now.Sub(startedAt).Seconds()))
			cancel()
			return err
		}, func(err error, wait time.Duration) {
			stresslog.Warn("[SAMPLER] 终态指标后台冲刷失败",
				zap.String("taskID", taskID), zap.Duration("backoff", wait), zap.Error(err))
		}, policy)
	})
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

			saved, err := s.sampleOnce(context.Background(), taskID, t, elapsed)
			if err != nil {
				stresslog.Warn("[SAMPLER] 时序趋势数据写入失败",
					zap.String("taskID", taskID), zap.Error(err))
				continue
			}
			if saved {
				lastSavedElapsed = elapsed
			}
		}
	}
}

func (s *Sampler) sampleOnce(ctx context.Context, taskID string, sampledAt time.Time, elapsed int) (bool, error) {
	if s.windows == nil || s.history == nil {
		return false, nil
	}
	batch, ok := s.windows.PeekHistory(taskID)
	if !ok {
		return false, nil
	}
	stress, err := aggregateHistoryBatch(batch)
	if err != nil {
		return false, err
	}
	var system ClusterSystemSnapshot
	if s.aggregator != nil {
		live, err := s.aggregator.AggregateStress(taskID)
		if err != nil {
			return false, err
		}
		stress.AssignedAgents = live.AssignedAgents
		stress.TotalAgents = live.TotalAgents
		stress.OfflineAgents = live.OfflineAgents
		stress.CoverageRatio = 0
		if stress.AssignedAgents > 0 {
			stress.CoverageRatio = float64(stress.ReportingAgents) / float64(stress.AssignedAgents)
		}
		var systemAgentIDs []string
		if task, ok := s.tasks.Get(taskID); ok {
			systemAgentIDs = taskSystemAgentIDs(task)
		} else {
			systemAgentIDs = []string{}
		}
		system = s.aggregator.AggregateSystem(systemAgentIDs)
	}
	if stress.AssignedAgents == 0 {
		stress.AssignedAgents = s.taskAssignedAgentCount(taskID)
		stress.CoverageRatio = 0
		if stress.AssignedAgents > 0 {
			stress.CoverageRatio = float64(stress.ReportingAgents) / float64(stress.AssignedAgents)
		}
	}
	point := buildHistoryTrendPoint(sampledAt, elapsed, stress, system)
	point.StageIndex = s.currentStageIndex(taskID)
	point.HistoryBatchToken = append([]byte(nil), batch.Token[:]...)
	if err := s.history.AppendTimeseries(ctx, taskID, point); err != nil {
		return false, err
	}
	if !s.windows.AckHistory(taskID, batch.Token) {
		return false, fmt.Errorf("确认历史指标批次失败")
	}
	return true, nil
}

func (s *Sampler) taskAssignedAgentCount(taskID string) int {
	if s.tasks == nil {
		return 0
	}
	task, ok := s.tasks.Get(taskID)
	if !ok {
		return 0
	}
	if len(task.SucceededAgents) > 0 {
		return len(task.SucceededAgents)
	}
	return len(task.Assignments)
}

func aggregateHistoryBatch(batch MetricHistoryBatch) (*StressAggregate, error) {
	parts := make([]*monitor.CollectorSnapshot, 0, len(batch.Windows))
	type agentRate struct {
		samples              int64
		duration             float64
		sendBytes, recvBytes int64
		latestSequence       uint64
		robots               monitor.RobotSnapshot
		connections          monitor.ConnectionSnapshot
	}
	rates := make(map[string]*agentRate)
	var startedAt, endedAt time.Time
	for _, item := range batch.Windows {
		window := item.Window
		parts = append(parts, &monitor.CollectorSnapshot{
			ApdexT:               item.ApdexT,
			TimingDetail:         item.TimingDetail,
			UptimeSec:            window.DurationSeconds,
			Actions:              window.Actions,
			InvalidMetricSamples: window.InvalidMetricSamples,
		})
		rate := rates[item.AgentID]
		if rate == nil {
			rate = &agentRate{}
			rates[item.AgentID] = rate
		}
		for _, action := range window.Actions {
			rate.samples += action.SampleCount
		}
		rate.duration += window.DurationSeconds
		rate.sendBytes += window.Bandwidth.SendBytes
		rate.recvBytes += window.Bandwidth.RecvBytes
		if window.Sequence >= rate.latestSequence {
			rate.latestSequence = window.Sequence
			rate.robots = item.Robots
			rate.connections = item.Connections
		}
		if startedAt.IsZero() || window.StartedAt.Before(startedAt) {
			startedAt = window.StartedAt
		}
		if window.EndedAt.After(endedAt) {
			endedAt = window.EndedAt
		}
	}
	merged, err := monitor.MergeSnapshots(parts)
	if err != nil {
		return nil, err
	}
	var qps, sendMBps, recvMBps float64
	for _, rate := range rates {
		if rate.duration > 0 {
			qps += float64(rate.samples) / rate.duration
			sendMBps += float64(rate.sendBytes) / 1024 / 1024 / rate.duration
			recvMBps += float64(rate.recvBytes) / 1024 / 1024 / rate.duration
		}
		merged.Robots.Running += rate.robots.Running
		merged.Connections.Active += rate.connections.Active
		merged.Connections.Closed += rate.connections.Closed
		merged.Connections.Dropped += rate.connections.Dropped
	}
	merged.Summary.AvgQPS = qps
	merged.TotalActions = merged.Summary.SampleCount
	merged.Bandwidth.SendMBps = sendMBps
	merged.Bandwidth.RecvMBps = recvMBps
	merged.Window = &monitor.ReportWindow{
		StartedAt:       startedAt,
		EndedAt:         endedAt,
		DurationSeconds: endedAt.Sub(startedAt).Seconds(),
		Summary:         merged.Summary,
		Bandwidth: monitor.WindowBandwidthSnapshot{
			SendMBps: sendMBps,
			RecvMBps: recvMBps,
		},
		Actions: merged.Actions,
	}
	return &StressAggregate{Snapshot: merged, ReportingAgents: len(rates)}, nil
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
	seg := min(distinctResetCount(task.StageReports)+1, len(plan.Segments))
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
		AvgCPUPercent: float64Value(sys.AvgHostCPUPercent),
		MaxCPUPercent: float64Value(sys.MaxHostCPUPercent),
		Goroutines:    intValue(sys.TotalProcessGoroutines),
		Threads:       int(int32Value(sys.TotalProcessThreads)),
		FDs:           int(int32Value(sys.TotalProcessFDs)),
		OnlineCount:   sys.OnlineCount,
		OfflineCount:  sys.OfflineCount,
	}
	point.AvgMemPercent = float64Value(sys.AvgHostMemPercent)
	point.MaxMemPercent = float64Value(sys.MaxHostMemPercent)
	if stress == nil || stress.Snapshot == nil {
		return point
	}

	snap := stress.Snapshot
	point.BotsRunning = int(snap.Robots.Running)
	point.BotsErrored = int(snap.Robots.Errored)
	if snap.Window == nil {
		return point
	}
	window := snap.Window
	point.WindowFrom = window.StartedAt
	point.WindowTo = window.EndedAt
	point.SampleCount = window.Summary.SampleCount
	point.SendKBps = window.Bandwidth.SendMBps * 1024
	point.RecvKBps = window.Bandwidth.RecvMBps * 1024
	sendBytesPerSec := window.Bandwidth.SendMBps * 1024 * 1024
	recvBytesPerSec := window.Bandwidth.RecvMBps * 1024 * 1024
	point.NetSendBytesPerSec = &sendBytesPerSec
	point.NetRecvBytesPerSec = &recvBytesPerSec
	active := snap.Connections.Active
	closed := snap.Connections.Closed
	dropped := snap.Connections.Dropped
	point.ActiveConnections = &active
	point.ClosedConnections = &closed
	point.DroppedConnections = &dropped
	assigned := stress.AssignedAgents
	reporting := stress.ReportingAgents
	coverage := stress.CoverageRatio
	point.AssignedAgents = &assigned
	point.ReportingAgents = &reporting
	point.ReportingCoverage = &coverage

	summary := window.Summary
	point.TotalQPS = summary.AvgQPS
	if summary.RTTApdexSampleCount > 0 {
		rttApdex := summary.RTTApdex
		point.RTTApdex = &rttApdex
	}
	if summary.RTT.Count > 0 {
		point.RTTAvgMs = summary.RTT.AvgMs
		point.RTTP50Ms = summary.RTT.P50Ms
		point.RTTP90Ms = summary.RTT.P90Ms
		point.RTTP95Ms = summary.RTT.P95Ms
		point.RTTP99Ms = summary.RTT.P99Ms
	}
	if summary.ListenWait.Count > 0 {
		point.ListenWaitP99Ms = summary.ListenWait.P99Ms
	}
	if summary.TotalDuration.Count > 0 {
		point.TotalDurationAvgMs = summary.TotalDuration.AvgMs
		point.TotalDurationP95Ms = summary.TotalDuration.P95Ms
		point.TotalDurationP99Ms = summary.TotalDuration.P99Ms
	}
	if summary.ClientCostCount > 0 {
		clientAvg := summary.ClientAvgMs
		point.ClientAvgMs = &clientAvg
	}
	if summary.EncodeSampleCount > 0 {
		encodeAvg := summary.EncodeAvgMs
		point.EncodeAvgMs = &encodeAvg
	}
	if summary.DecodeSampleCount > 0 {
		decodeAvg := summary.DecodeAvgMs
		point.DecodeAvgMs = &decodeAvg
	}
	return point
}
