package metrics

import (
	"context"
	"sync"
	"time"

	"stressbot/controlplane"
	controlpb "stressbot/controlplane/pb"
	"stressbot/monitor"
)

type stressSlot struct {
	taskID     string
	reportedAt time.Time
	snapshot   *monitor.CollectorSnapshot
	dropped    uint64
}

type systemSlot struct {
	snapshot SystemSnapshot
	dropped  uint64
}

// LatestMetrics has exactly one stress slot and one system slot.
type LatestMetrics struct {
	mu           sync.Mutex
	stress       *stressSlot
	system       *systemSlot
	stressDrops  uint64
	stressTaskID string
	systemDrops  uint64
	notify       chan struct{}
}

// NewLatestMetrics 创建最新帧缓冲。
func NewLatestMetrics() *LatestMetrics { return &LatestMetrics{notify: make(chan struct{}, 1)} }

// OfferStress 覆盖压测帧槽位并唤醒发送循环；任务切换时丢弃旧任务帧并清零丢弃计数，
// 未发送的被覆盖帧计入 dropped，随下一帧带给 Admin 补账。
func (t *LatestMetrics) OfferStress(taskID string, reportedAt time.Time, snapshot *monitor.CollectorSnapshot) {
	t.mu.Lock()
	if t.stressTaskID != taskID {
		t.stressTaskID = taskID
		t.stressDrops = 0
		t.stress = nil
	}
	if t.stress != nil {
		t.stressDrops++
	}
	t.stress = &stressSlot{taskID: taskID, reportedAt: reportedAt, snapshot: snapshot, dropped: t.stressDrops}
	t.mu.Unlock()
	t.wake()
}

// OfferSystem 覆盖系统帧槽位并唤醒发送循环；未发送的被覆盖帧计入 dropped。
func (t *LatestMetrics) OfferSystem(snapshot SystemSnapshot) {
	t.mu.Lock()
	if t.system != nil {
		t.systemDrops++
	}
	t.system = &systemSlot{snapshot: snapshot, dropped: t.systemDrops}
	t.mu.Unlock()
	t.wake()
}

func (t *LatestMetrics) wake() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

func (t *LatestMetrics) take() (*stressSlot, *systemSlot) {
	t.mu.Lock()
	stress, system := t.stress, t.system
	t.stress, t.system = nil, nil
	t.mu.Unlock()
	return stress, system
}

func (t *LatestMetrics) restore(stress *stressSlot, system *systemSlot) {
	t.mu.Lock()
	if stress != nil {
		if t.stress == nil {
			t.stress = stress
		} else if t.stress.taskID == stress.taskID {
			t.stressDrops++
			t.stress.dropped = t.stressDrops
		}
	}
	if system != nil {
		if t.system == nil {
			t.system = system
		} else {
			t.systemDrops++
			t.system.dropped = t.systemDrops
		}
	}
	hasPending := t.stress != nil || t.system != nil
	t.mu.Unlock()
	if hasPending {
		t.wake()
	}
}

// SendLoop 将最新压力与系统指标通过 gRPC 流持续发送。
func (t *LatestMetrics) SendLoop(ctx context.Context, client controlpb.AgentMetricsServiceClient, agentID string, generation uint64) error {
	stream, err := client.Report(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			_, _ = stream.CloseAndRecv()
			return ctx.Err()
		case <-t.notify:
		}
		stress, system := t.take()
		if stress != nil {
			envelope := &controlpb.MetricsEnvelope{AgentId: agentID, Generation: generation, DroppedIntervals: stress.dropped,
				Payload: &controlpb.MetricsEnvelope_Stress{Stress: &controlpb.StressMetrics{TaskId: stress.taskID,
					ReportedAtUnixNano: stress.reportedAt.UnixNano(), Snapshot: controlplane.ToProtoCollectorSnapshot(stress.snapshot)}}}
			if err := stream.Send(envelope); err != nil {
				t.restore(stress, system)
				return err
			}
		}
		if system != nil {
			envelope := &controlpb.MetricsEnvelope{AgentId: agentID, Generation: generation, DroppedIntervals: system.dropped,
				Payload: &controlpb.MetricsEnvelope_System{System: &controlpb.SystemMetrics{Snapshot: SystemSnapshotToProto(system.snapshot)}}}
			if err := stream.Send(envelope); err != nil {
				t.restore(nil, system)
				return err
			}
		}
	}
}
