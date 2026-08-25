package metrics

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/controlplane"
	controlpb "stressbot/controlplane/pb"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
)

type metricsKind uint8

const (
	metricsStress metricsKind = iota + 1
	metricsSystem
	metricsSlotCapacity = 32768
)

type metricsKey struct {
	agentID    string
	generation uint64
	kind       metricsKind
}

type metricsSlot struct {
	envelope *controlpb.MetricsEnvelope
	queued   bool
	lastLog  time.Time
}

// Ingestor keeps at most one pending item per Agent and payload kind.
// gRPC Recv only validates identity and replaces that slot; expensive monitor
// conversion, DDSketch validation and state commit run on a bounded worker set.
type Ingestor struct {
	withCurrent  func(string, uint64, func() error) error
	acceptStress func(StressReport, uint64) error
	acceptSystem func(string, *controlpb.SystemMetricSnapshot)
	workers      int
	mu           sync.Mutex
	slots        map[metricsKey]*metricsSlot
	errors       map[metricsKey]error
	ready        chan metricsKey
	accepted     atomic.Uint64
	rejected     atomic.Uint64
	dropped      atomic.Uint64
}

// NewIngestor 创建指标摄取器；worker 数取 CPU 核数并夹在 [4,32]。
// 三个回调分别封装租约校验、压测帧受理与系统帧受理。
func NewIngestor(
	withCurrent func(string, uint64, func() error) error,
	acceptStress func(StressReport, uint64) error,
	acceptSystem func(string, *controlpb.SystemMetricSnapshot),
) *Ingestor {
	workers := runtime.NumCPU()
	if workers < 4 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}
	return &Ingestor{withCurrent: withCurrent, acceptStress: acceptStress, acceptSystem: acceptSystem, workers: workers, slots: make(map[metricsKey]*metricsSlot), errors: make(map[metricsKey]error), ready: make(chan metricsKey, metricsSlotCapacity)}
}

// Start 启动全部 worker 协程消费就绪队列，直到 ctx 取消。
func (i *Ingestor) Start(ctx context.Context) error {
	for range i.workers {
		if err := workpool.Default().Submit(func() { i.runWorker(ctx) }); err != nil {
			return err
		}
	}
	return nil
}

// Offer 接收一帧指标：只校验帧身份，然后覆盖该 (节点, 代次, 类型) 槽位；
// 仅未排队中的槽位入就绪队列，被覆盖的未处理帧计入 dropped 由新帧顺带携带。
// 槽位表或就绪队列满时返回错误，表示该帧被拒。
func (i *Ingestor) Offer(envelope *controlpb.MetricsEnvelope) error {
	if envelope == nil || envelope.AgentId == "" || envelope.Generation == 0 {
		return errors.New("指标帧身份无效")
	}
	var kind metricsKind
	switch envelope.GetPayload().(type) {
	case *controlpb.MetricsEnvelope_Stress:
		kind = metricsStress
	case *controlpb.MetricsEnvelope_System:
		kind = metricsSystem
	default:
		return errors.New("指标帧缺少 payload")
	}
	key := metricsKey{agentID: envelope.AgentId, generation: envelope.Generation, kind: kind}
	i.mu.Lock()
	if len(i.slots) >= metricsSlotCapacity {
		if _, exists := i.slots[key]; !exists {
			i.mu.Unlock()
			return errors.New("指标摄取槽位已满")
		}
	}
	slot := i.slots[key]
	if slot == nil {
		slot = &metricsSlot{}
		i.slots[key] = slot
	}
	if slot.envelope != nil {
		i.dropped.Add(1)
	}
	slot.envelope = envelope
	if slot.queued {
		i.mu.Unlock()
		return nil
	}
	slot.queued = true
	i.mu.Unlock()
	select {
	case i.ready <- key:
		return nil
	default:
		i.mu.Lock()
		slot.queued = false
		i.mu.Unlock()
		return errors.New("指标摄取队列已满")
	}
}

// TakeError 取出该节点当前代次（压测或系统帧）最近一次处理失败的错误，
// 取出即清除，供控制会话回传给 Agent。
func (i *Ingestor) TakeError(agentID string, generation uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, kind := range []metricsKind{metricsStress, metricsSystem} {
		key := metricsKey{agentID: agentID, generation: generation, kind: kind}
		if err := i.errors[key]; err != nil {
			delete(i.errors, key)
			return err
		}
	}
	return nil
}

// DropGeneration 清理指定节点代次的错误记录与空闲槽位；
// 仍在队列中排队处理的槽位保留，由 worker 处理完后自行移除。
func (i *Ingestor) DropGeneration(agentID string, generation uint64) {
	i.mu.Lock()
	for _, kind := range []metricsKind{metricsStress, metricsSystem} {
		key := metricsKey{agentID: agentID, generation: generation, kind: kind}
		delete(i.errors, key)
		if slot := i.slots[key]; slot != nil && !slot.queued {
			delete(i.slots, key)
		}
	}
	i.mu.Unlock()
}

func (i *Ingestor) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-i.ready:
			i.processKey(ctx, key)
		}
	}
}

func (i *Ingestor) processKey(ctx context.Context, key metricsKey) {
	i.mu.Lock()
	slot := i.slots[key]
	if slot == nil {
		i.mu.Unlock()
		return
	}
	envelope := slot.envelope
	slot.envelope = nil
	i.mu.Unlock()

	err := i.withCurrent(key.agentID, key.generation, func() error {
		switch payload := envelope.GetPayload().(type) {
		case *controlpb.MetricsEnvelope_Stress:
			return i.acceptStress(StressReport{AgentID: key.agentID, TaskID: payload.Stress.TaskId,
				ReportedAt: time.Unix(0, payload.Stress.ReportedAtUnixNano), Snapshot: controlplane.FromProtoCollectorSnapshot(payload.Stress.Snapshot)}, envelope.DroppedIntervals)
		case *controlpb.MetricsEnvelope_System:
			i.acceptSystem(key.agentID, payload.System.Snapshot)
			return nil
		default:
			return errors.New("指标帧缺少 payload")
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		i.rejected.Add(1)
		i.mu.Lock()
		i.errors[key] = err
		now := time.Now()
		shouldLog := now.Sub(slot.lastLog) >= time.Minute
		if shouldLog {
			slot.lastLog = now
		}
		i.mu.Unlock()
		if shouldLog {
			stresslog.Warn("[ADMIN] 拒绝节点指标", zap.String("agentID", key.agentID), zap.Uint64("generation", key.generation), zap.Error(err))
		}
	} else if err == nil {
		i.accepted.Add(1)
	}

	i.mu.Lock()
	if slot.envelope != nil {
		i.mu.Unlock()
		select {
		case i.ready <- key:
		case <-ctx.Done():
		}
		return
	}
	slot.queued = false
	delete(i.slots, key)
	i.mu.Unlock()
}
