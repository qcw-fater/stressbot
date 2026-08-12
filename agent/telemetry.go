package agent

import (
	"context"
	"sync"
	"time"

	"stressbot/controlplane"
	"stressbot/controlplane/controlv1"
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

// LatestTelemetry has exactly one stress slot and one system slot.
type LatestTelemetry struct {
	mu           sync.Mutex
	stress       *stressSlot
	system       *systemSlot
	stressDrops  uint64
	stressTaskID string
	systemDrops  uint64
	notify       chan struct{}
}

func NewLatestTelemetry() *LatestTelemetry { return &LatestTelemetry{notify: make(chan struct{}, 1)} }

func (t *LatestTelemetry) OfferStress(taskID string, reportedAt time.Time, snapshot *monitor.CollectorSnapshot) {
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

func (t *LatestTelemetry) OfferSystem(snapshot SystemSnapshot) {
	t.mu.Lock()
	if t.system != nil {
		t.systemDrops++
	}
	t.system = &systemSlot{snapshot: snapshot, dropped: t.systemDrops}
	t.mu.Unlock()
	t.wake()
}

func (t *LatestTelemetry) wake() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

func (t *LatestTelemetry) take() (*stressSlot, *systemSlot) {
	t.mu.Lock()
	stress, system := t.stress, t.system
	t.stress, t.system = nil, nil
	t.mu.Unlock()
	return stress, system
}

func (t *LatestTelemetry) restore(stress *stressSlot, system *systemSlot) {
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

func (t *LatestTelemetry) sendLoop(ctx context.Context, client controlv1.AgentTelemetryServiceClient, agentID string, generation uint64) error {
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
			envelope := &controlv1.TelemetryEnvelope{AgentId: agentID, Generation: generation, DroppedIntervals: stress.dropped,
				Payload: &controlv1.TelemetryEnvelope_Stress{Stress: &controlv1.StressTelemetry{TaskId: stress.taskID,
					ReportedAtUnixNano: stress.reportedAt.UnixNano(), Snapshot: controlplane.ToProtoCollectorSnapshot(stress.snapshot)}}}
			if err := stream.Send(envelope); err != nil {
				t.restore(stress, system)
				return err
			}
			stress = nil
		}
		if system != nil {
			envelope := &controlv1.TelemetryEnvelope{AgentId: agentID, Generation: generation, DroppedIntervals: system.dropped,
				Payload: &controlv1.TelemetryEnvelope_System{System: &controlv1.SystemTelemetry{Snapshot: systemSnapshotToProto(system.snapshot)}}}
			if err := stream.Send(envelope); err != nil {
				t.restore(nil, system)
				return err
			}
		}
	}
}
