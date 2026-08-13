package history

import (
	"testing"

	"stressbot/monitor"
)

func TestProjectStressSnapshotPreservesSummaryAndActionDiagnostics(t *testing.T) {
	snapshot := monitor.CollectorSnapshot{
		TimingDetail: monitor.TimingCodecDetail,
		Summary: monitor.SnapshotSummary{
			RTTApdex:            0.505,
			RTTApdexSampleCount: 20,
		},
		Actions: []monitor.ActionSnapshot{{
			RTTApdexSampleCount:       10,
			BuildAvgMs:                4,
			SendAvgMs:                 5,
			DecodeWaitAvgMs:           6,
			DispatchToActionWaitAvgMs: 7,
		}},
	}

	projected := ProjectStressSnapshot(snapshot)
	if projected.TimingDetail != monitor.TimingCodecDetail || projected.Summary.RTTApdex != 0.505 {
		t.Fatalf("projected summary = timing %q apdex %v, want codec/0.505", projected.TimingDetail, projected.Summary.RTTApdex)
	}
	if len(projected.Actions) != 1 || projected.Actions[0].RTTApdexSampleCount != 10 {
		t.Fatalf("projected action denominator = %#v", projected.Actions)
	}
	if projected.Actions[0].BuildAvgMs != 4 || projected.Actions[0].SendAvgMs != 5 || projected.Actions[0].DecodeWaitAvgMs != 6 || projected.Actions[0].DispatchToActionWaitAvgMs != 7 {
		t.Fatalf("projected action phases = %#v", projected.Actions[0])
	}
}
