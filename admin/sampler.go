package admin

import (
	"context"
	"encoding/json"
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case t := <-ticker.C:
			elapsed := int(t.Sub(startedAt).Seconds())

			stress := s.aggregator.AggregateStress(taskID)
			if stressJSON, err := json.Marshal(stress); err == nil {
				if err := s.history.AppendTimeseries(context.Background(), taskID, TimeseriesPoint{
					TaskID: taskID, SampledAt: t, ElapsedSec: elapsed,
					DataType: "stress", Snapshot: stressJSON,
				}); err != nil {
					stresslog.Warn("[SAMPLER] stress 时序数据写入失败",
						zap.String("taskId", taskID), zap.Error(err))
				}
			}

			sys := s.aggregator.AggregateSystem()
			if sysJSON, err := json.Marshal(sys); err == nil {
				if err := s.history.AppendTimeseries(context.Background(), taskID, TimeseriesPoint{
					TaskID: taskID, SampledAt: t, ElapsedSec: elapsed,
					DataType: "system", Snapshot: sysJSON,
				}); err != nil {
					stresslog.Warn("[SAMPLER] system 时序数据写入失败",
						zap.String("taskId", taskID), zap.Error(err))
				}
			}
		}
	}
}
