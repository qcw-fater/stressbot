package admin

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/controlplane"
	"stressbot/controlplane/controlv1"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

type telemetryKind uint8

const (
	telemetryStress telemetryKind = iota + 1
	telemetrySystem
	telemetrySlotCapacity = 32768
)

type telemetryKey struct {
	agentID    string
	generation uint64
	kind       telemetryKind
}

type telemetrySlot struct {
	envelope *controlv1.TelemetryEnvelope
	queued   bool
	lastLog  time.Time
}

// TelemetryIngestor keeps at most one pending item per Agent and payload kind.
// gRPC Recv only validates identity and replaces that slot; expensive monitor
// conversion, DDSketch validation and state commit run on a bounded worker set.
type TelemetryIngestor struct {
	server   *AdminServer
	workers  int
	mu       sync.Mutex
	slots    map[telemetryKey]*telemetrySlot
	errors   map[telemetryKey]error
	ready    chan telemetryKey
	accepted atomic.Uint64
	rejected atomic.Uint64
	dropped  atomic.Uint64
}

func NewTelemetryIngestor(server *AdminServer) *TelemetryIngestor {
	workers := runtime.NumCPU()
	if workers < 4 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}
	return &TelemetryIngestor{server: server, workers: workers, slots: make(map[telemetryKey]*telemetrySlot), errors: make(map[telemetryKey]error), ready: make(chan telemetryKey, telemetrySlotCapacity)}
}

func (i *TelemetryIngestor) Start(ctx context.Context) error {
	for worker := 0; worker < i.workers; worker++ {
		if err := utils.GetWorkPool().Submit(func() { i.runWorker(ctx) }); err != nil {
			return err
		}
	}
	return nil
}

func (i *TelemetryIngestor) Offer(envelope *controlv1.TelemetryEnvelope) error {
	if envelope == nil || envelope.AgentId == "" || envelope.Generation == 0 {
		return fmt.Errorf("遥测帧身份无效")
	}
	kind := telemetryKind(0)
	switch envelope.GetPayload().(type) {
	case *controlv1.TelemetryEnvelope_Stress:
		kind = telemetryStress
	case *controlv1.TelemetryEnvelope_System:
		kind = telemetrySystem
	default:
		return fmt.Errorf("遥测帧缺少 payload")
	}
	key := telemetryKey{agentID: envelope.AgentId, generation: envelope.Generation, kind: kind}
	i.mu.Lock()
	if len(i.slots) >= telemetrySlotCapacity {
		if _, exists := i.slots[key]; !exists {
			i.mu.Unlock()
			return fmt.Errorf("遥测摄取槽位已满")
		}
	}
	slot := i.slots[key]
	if slot == nil {
		slot = &telemetrySlot{}
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
		return fmt.Errorf("遥测摄取队列已满")
	}
}

func (i *TelemetryIngestor) TakeError(agentID string, generation uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, kind := range []telemetryKind{telemetryStress, telemetrySystem} {
		key := telemetryKey{agentID: agentID, generation: generation, kind: kind}
		if err := i.errors[key]; err != nil {
			delete(i.errors, key)
			return err
		}
	}
	return nil
}

func (i *TelemetryIngestor) DropGeneration(agentID string, generation uint64) {
	i.mu.Lock()
	for _, kind := range []telemetryKind{telemetryStress, telemetrySystem} {
		key := telemetryKey{agentID: agentID, generation: generation, kind: kind}
		delete(i.errors, key)
		if slot := i.slots[key]; slot != nil && !slot.queued {
			delete(i.slots, key)
		}
	}
	i.mu.Unlock()
}

func (i *TelemetryIngestor) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-i.ready:
			i.processKey(ctx, key)
		}
	}
}

func (i *TelemetryIngestor) processKey(ctx context.Context, key telemetryKey) {
	i.mu.Lock()
	slot := i.slots[key]
	if slot == nil {
		i.mu.Unlock()
		return
	}
	envelope := slot.envelope
	slot.envelope = nil
	i.mu.Unlock()

	err := i.server.sessions.WithCurrent(key.agentID, key.generation, func() error {
		switch payload := envelope.GetPayload().(type) {
		case *controlv1.TelemetryEnvelope_Stress:
			return i.server.acceptStressReportWithDrops(StressReport{AgentID: key.agentID, TaskID: payload.Stress.TaskId,
				ReportedAt: time.Unix(0, payload.Stress.ReportedAtUnixNano), Snapshot: controlplane.FromProtoCollectorSnapshot(payload.Stress.Snapshot)}, envelope.DroppedIntervals)
		case *controlv1.TelemetryEnvelope_System:
			i.server.agents.Touch(key.agentID, "")
			i.server.agents.UpdateSystem(key.agentID, systemSnapshotFromProto(payload.System.Snapshot), time.Now())
			return nil
		default:
			return fmt.Errorf("遥测帧缺少 payload")
		}
	})
	if err != nil && err != context.Canceled {
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
			stresslog.Warn("[ADMIN] 拒绝节点遥测", zap.String("agentID", key.agentID), zap.Uint64("generation", key.generation), zap.Error(err))
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
