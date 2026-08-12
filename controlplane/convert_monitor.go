// Package controlplane 提供 Admin-Agent gRPC 控制面的共享 DTO 转换能力。
package controlplane

import (
	"time"

	"stressbot/controlplane/controlv1"
	"stressbot/monitor"
)

// ToProtoCollectorSnapshot converts the internal mergeable snapshot to its wire DTO.
func ToProtoCollectorSnapshot(src *monitor.CollectorSnapshot) *controlv1.CollectorSnapshot {
	if src == nil {
		return nil
	}
	out := &controlv1.CollectorSnapshot{
		TimestampUnixNano:    unixNano(src.Timestamp),
		CollectionEpoch:      src.CollectionEpoch,
		UptimeNanos:          int64(src.Uptime),
		UptimeSeconds:        src.UptimeSec,
		TotalActions:         src.TotalActions,
		ApdexTMillis:         int32(src.ApdexT),
		TimingDetail:         string(src.TimingDetail),
		Summary:              toProtoSummary(src.Summary),
		System:               toProtoMonitorSystem(src.System),
		Robots:               toProtoRobots(src.Robots),
		RampUp:               toProtoRampUp(src.RampUp),
		Connections:          toProtoConnections(src.Connections),
		Bandwidth:            toProtoBandwidth(src.Bandwidth),
		Actions:              toProtoActions(src.Actions),
		InvalidMetricSamples: src.InvalidMetricSamples,
		Window:               toProtoWindow(src.Window),
	}
	return out
}

// FromProtoCollectorSnapshot converts a wire DTO into an independently owned snapshot.
func FromProtoCollectorSnapshot(src *controlv1.CollectorSnapshot) *monitor.CollectorSnapshot {
	if src == nil {
		return nil
	}
	out := &monitor.CollectorSnapshot{
		Timestamp:            timeFromUnixNano(src.TimestampUnixNano),
		CollectionEpoch:      src.CollectionEpoch,
		Uptime:               time.Duration(src.UptimeNanos),
		UptimeSec:            src.UptimeSeconds,
		TotalActions:         src.TotalActions,
		ApdexT:               int(src.ApdexTMillis),
		TimingDetail:         monitor.NormalizeTimingDetail(src.TimingDetail),
		Summary:              fromProtoSummary(src.Summary),
		System:               fromProtoMonitorSystem(src.System),
		Robots:               fromProtoRobots(src.Robots),
		RampUp:               fromProtoRampUp(src.RampUp),
		Connections:          fromProtoConnections(src.Connections),
		Bandwidth:            fromProtoBandwidth(src.Bandwidth),
		Actions:              fromProtoActions(src.Actions),
		InvalidMetricSamples: src.InvalidMetricSamples,
		Window:               fromProtoWindow(src.Window),
	}
	if out.Actions == nil {
		out.Actions = []monitor.ActionSnapshot{}
	}
	return out
}

func toProtoHistogram(src monitor.HistogramSnapshot) *controlv1.HistogramSnapshot {
	return &controlv1.HistogramSnapshot{
		Count:         src.Count,
		MinMillis:     cloneFloat64(src.MinMs),
		MaxMillis:     cloneFloat64(src.MaxMs),
		AverageMillis: cloneFloat64(src.AvgMs),
		P50Millis:     cloneFloat64(src.P50Ms),
		P90Millis:     cloneFloat64(src.P90Ms),
		P95Millis:     cloneFloat64(src.P95Ms),
		P99Millis:     cloneFloat64(src.P99Ms),
		SumNanos:      src.SumNs,
		Sketch:        cloneBytes(src.Sketch),
	}
}

func fromProtoHistogram(src *controlv1.HistogramSnapshot) monitor.HistogramSnapshot {
	if src == nil {
		return monitor.HistogramSnapshot{}
	}
	return monitor.HistogramSnapshot{
		Count:  src.Count,
		MinMs:  cloneFloat64(src.MinMillis),
		MaxMs:  cloneFloat64(src.MaxMillis),
		AvgMs:  cloneFloat64(src.AverageMillis),
		P50Ms:  cloneFloat64(src.P50Millis),
		P90Ms:  cloneFloat64(src.P90Millis),
		P95Ms:  cloneFloat64(src.P95Millis),
		P99Ms:  cloneFloat64(src.P99Millis),
		SumNs:  src.SumNanos,
		Sketch: cloneBytes(src.Sketch),
	}
}

func toProtoActions(src []monitor.ActionSnapshot) []*controlv1.ActionSnapshot {
	if src == nil {
		return nil
	}
	out := make([]*controlv1.ActionSnapshot, len(src))
	for i := range src {
		out[i] = toProtoAction(src[i])
	}
	return out
}

func fromProtoActions(src []*controlv1.ActionSnapshot) []monitor.ActionSnapshot {
	if src == nil {
		return nil
	}
	out := make([]monitor.ActionSnapshot, len(src))
	for i := range src {
		out[i] = fromProtoAction(src[i])
	}
	return out
}

func toProtoAction(src monitor.ActionSnapshot) *controlv1.ActionSnapshot {
	out := &controlv1.ActionSnapshot{
		Name:                              src.Name,
		SampleCount:                       src.SampleCount,
		SuccessCount:                      src.SuccessCount,
		FailureCount:                      src.FailureCount,
		TimeoutCount:                      src.TimeoutCount,
		CanceledCount:                     src.CanceledCount,
		Executing:                         src.Executing,
		SuccessRate:                       src.SuccessRate,
		AverageSendBytes:                  src.AvgSendBytes,
		AverageReceiveBytes:               src.AvgRecvBytes,
		ByteSampleCount:                   src.ByteSampleCount,
		Kind:                              string(src.Kind),
		RttApdex:                          src.RTTApdex,
		Rtt:                               toProtoHistogram(src.RTT),
		ListenWait:                        toProtoHistogram(src.ListenWait),
		ListenReadyCount:                  src.ListenReadyCount,
		ListenTimeoutCount:                src.ListenTimeoutCount,
		ListenTimeoutRate:                 src.ListenTimeoutRate,
		TotalDuration:                     toProtoHistogram(src.TotalDuration),
		TimeoutAverageMillis:              src.TimeoutAvgMs,
		ClientAverageMillis:               src.ClientAvgMs,
		BuildAverageMillis:                src.BuildAvgMs,
		EncodeAverageMillis:               src.EncodeAvgMs,
		SendAverageMillis:                 src.SendAvgMs,
		DecodeWaitAverageMillis:           src.DecodeWaitAvgMs,
		DecodeAverageMillis:               src.DecodeAvgMs,
		DispatchToActionWaitAverageMillis: src.DispatchToActionWaitAvgMs,
		ParseStoreAverageMillis:           src.ParseStoreAvgMs,
		BuildSampleCount:                  src.BuildSampleCount,
		EncodeSampleCount:                 src.EncodeSampleCount,
		SendSampleCount:                   src.SendSampleCount,
		DecodeWaitSampleCount:             src.DecodeWaitSampleCount,
		DecodeSampleCount:                 src.DecodeSampleCount,
		DispatchWaitSampleCount:           src.DispatchWaitSampleCount,
		ParseStoreSampleCount:             src.ParseStoreSampleCount,
		RttSampleCount:                    src.RTTSampleCount,
		RttApdexSampleCount:               src.RTTApdexSampleCount,
		ListenWaitSampleCount:             src.ListenWaitSampleCount,
		TotalDurationSampleCount:          src.TotalDurationSampleCount,
		AverageQps:                        src.AvgQPS,
		PeriodQps:                         src.PeriodQPS,
		ApdexSatisfied:                    src.ApdexSatisfied,
		ApdexTolerating:                   src.ApdexTolerating,
		RttFailedCount:                    src.RTTFailedCount,
		TotalSendBytes:                    src.TotalSendBytes,
		TotalReceiveBytes:                 src.TotalRecvBytes,
		TimeoutTotalNanos:                 src.TimeoutTotalNs,
		ClientCostSumNanos:                src.ClientCostSumNs,
		ClientCostCount:                   src.ClientCostCount,
		BuildCostSumNanos:                 src.BuildCostSumNs,
		EncodeCostSumNanos:                src.EncodeCostSumNs,
		SendCostSumNanos:                  src.SendCostSumNs,
		DecodeWaitSumNanos:                src.DecodeWaitSumNs,
		DecodeCostSumNanos:                src.DecodeCostSumNs,
		DispatchWaitSumNanos:              src.DispatchWaitSumNs,
		ParseStoreSumNanos:                src.ParseStoreSumNs,
	}
	if src.Errors != nil {
		out.Errors = make([]*controlv1.ErrorEntry, len(src.Errors))
		for i := range src.Errors {
			errEntry := src.Errors[i]
			out.Errors[i] = &controlv1.ErrorEntry{
				Code:     errEntry.Code,
				CodeName: errEntry.CodeName,
				Messages: append([]string(nil), errEntry.Messages...),
				Count:    errEntry.Count,
			}
		}
	}
	return out
}

func fromProtoAction(src *controlv1.ActionSnapshot) monitor.ActionSnapshot {
	if src == nil {
		return monitor.ActionSnapshot{}
	}
	out := monitor.ActionSnapshot{
		Name:                      src.Name,
		SampleCount:               src.SampleCount,
		SuccessCount:              src.SuccessCount,
		FailureCount:              src.FailureCount,
		TimeoutCount:              src.TimeoutCount,
		CanceledCount:             src.CanceledCount,
		Executing:                 src.Executing,
		SuccessRate:               src.SuccessRate,
		AvgSendBytes:              src.AverageSendBytes,
		AvgRecvBytes:              src.AverageReceiveBytes,
		ByteSampleCount:           src.ByteSampleCount,
		Kind:                      monitor.ActionKind(src.Kind),
		RTTApdex:                  src.RttApdex,
		RTT:                       fromProtoHistogram(src.Rtt),
		ListenWait:                fromProtoHistogram(src.ListenWait),
		ListenReadyCount:          src.ListenReadyCount,
		ListenTimeoutCount:        src.ListenTimeoutCount,
		ListenTimeoutRate:         src.ListenTimeoutRate,
		TotalDuration:             fromProtoHistogram(src.TotalDuration),
		TimeoutAvgMs:              src.TimeoutAverageMillis,
		ClientAvgMs:               src.ClientAverageMillis,
		BuildAvgMs:                src.BuildAverageMillis,
		EncodeAvgMs:               src.EncodeAverageMillis,
		SendAvgMs:                 src.SendAverageMillis,
		DecodeWaitAvgMs:           src.DecodeWaitAverageMillis,
		DecodeAvgMs:               src.DecodeAverageMillis,
		DispatchToActionWaitAvgMs: src.DispatchToActionWaitAverageMillis,
		ParseStoreAvgMs:           src.ParseStoreAverageMillis,
		BuildSampleCount:          src.BuildSampleCount,
		EncodeSampleCount:         src.EncodeSampleCount,
		SendSampleCount:           src.SendSampleCount,
		DecodeWaitSampleCount:     src.DecodeWaitSampleCount,
		DecodeSampleCount:         src.DecodeSampleCount,
		DispatchWaitSampleCount:   src.DispatchWaitSampleCount,
		ParseStoreSampleCount:     src.ParseStoreSampleCount,
		RTTSampleCount:            src.RttSampleCount,
		RTTApdexSampleCount:       src.RttApdexSampleCount,
		ListenWaitSampleCount:     src.ListenWaitSampleCount,
		TotalDurationSampleCount:  src.TotalDurationSampleCount,
		AvgQPS:                    src.AverageQps,
		PeriodQPS:                 src.PeriodQps,
		ApdexSatisfied:            src.ApdexSatisfied,
		ApdexTolerating:           src.ApdexTolerating,
		RTTFailedCount:            src.RttFailedCount,
		TotalSendBytes:            src.TotalSendBytes,
		TotalRecvBytes:            src.TotalReceiveBytes,
		TimeoutTotalNs:            src.TimeoutTotalNanos,
		ClientCostSumNs:           src.ClientCostSumNanos,
		ClientCostCount:           src.ClientCostCount,
		BuildCostSumNs:            src.BuildCostSumNanos,
		EncodeCostSumNs:           src.EncodeCostSumNanos,
		SendCostSumNs:             src.SendCostSumNanos,
		DecodeWaitSumNs:           src.DecodeWaitSumNanos,
		DecodeCostSumNs:           src.DecodeCostSumNanos,
		DispatchWaitSumNs:         src.DispatchWaitSumNanos,
		ParseStoreSumNs:           src.ParseStoreSumNanos,
	}
	if src.Errors != nil {
		out.Errors = make([]monitor.ErrorEntry, len(src.Errors))
		for i := range src.Errors {
			if src.Errors[i] == nil {
				continue
			}
			out.Errors[i] = monitor.ErrorEntry{
				Code:     src.Errors[i].Code,
				CodeName: src.Errors[i].CodeName,
				Messages: append([]string(nil), src.Errors[i].Messages...),
				Count:    src.Errors[i].Count,
			}
		}
	}
	return out
}

func toProtoSummary(src monitor.SnapshotSummary) *controlv1.SnapshotSummary {
	return &controlv1.SnapshotSummary{
		SampleCount:                       src.SampleCount,
		SuccessCount:                      src.SuccessCount,
		FailureCount:                      src.FailureCount,
		TimeoutCount:                      src.TimeoutCount,
		CanceledCount:                     src.CanceledCount,
		Executing:                         src.Executing,
		SuccessRate:                       src.SuccessRate,
		RttApdex:                          src.RTTApdex,
		RttApdexSampleCount:               src.RTTApdexSampleCount,
		Rtt:                               toProtoHistogram(src.RTT),
		ListenWait:                        toProtoHistogram(src.ListenWait),
		TotalDuration:                     toProtoHistogram(src.TotalDuration),
		ClientAverageMillis:               src.ClientAvgMs,
		ClientCostCount:                   src.ClientCostCount,
		BuildAverageMillis:                src.BuildAvgMs,
		EncodeAverageMillis:               src.EncodeAvgMs,
		SendAverageMillis:                 src.SendAvgMs,
		DecodeWaitAverageMillis:           src.DecodeWaitAvgMs,
		DecodeAverageMillis:               src.DecodeAvgMs,
		DispatchToActionWaitAverageMillis: src.DispatchToActionWaitAvgMs,
		ParseStoreAverageMillis:           src.ParseStoreAvgMs,
		BuildSampleCount:                  src.BuildSampleCount,
		EncodeSampleCount:                 src.EncodeSampleCount,
		SendSampleCount:                   src.SendSampleCount,
		DecodeWaitSampleCount:             src.DecodeWaitSampleCount,
		DecodeSampleCount:                 src.DecodeSampleCount,
		DispatchWaitSampleCount:           src.DispatchWaitSampleCount,
		ParseStoreSampleCount:             src.ParseStoreSampleCount,
		AverageQps:                        src.AvgQPS,
	}
}

func fromProtoSummary(src *controlv1.SnapshotSummary) monitor.SnapshotSummary {
	if src == nil {
		return monitor.SnapshotSummary{}
	}
	return monitor.SnapshotSummary{
		SampleCount:               src.SampleCount,
		SuccessCount:              src.SuccessCount,
		FailureCount:              src.FailureCount,
		TimeoutCount:              src.TimeoutCount,
		CanceledCount:             src.CanceledCount,
		Executing:                 src.Executing,
		SuccessRate:               src.SuccessRate,
		RTTApdex:                  src.RttApdex,
		RTTApdexSampleCount:       src.RttApdexSampleCount,
		RTT:                       fromProtoHistogram(src.Rtt),
		ListenWait:                fromProtoHistogram(src.ListenWait),
		TotalDuration:             fromProtoHistogram(src.TotalDuration),
		ClientAvgMs:               src.ClientAverageMillis,
		ClientCostCount:           src.ClientCostCount,
		BuildAvgMs:                src.BuildAverageMillis,
		EncodeAvgMs:               src.EncodeAverageMillis,
		SendAvgMs:                 src.SendAverageMillis,
		DecodeWaitAvgMs:           src.DecodeWaitAverageMillis,
		DecodeAvgMs:               src.DecodeAverageMillis,
		DispatchToActionWaitAvgMs: src.DispatchToActionWaitAverageMillis,
		ParseStoreAvgMs:           src.ParseStoreAverageMillis,
		BuildSampleCount:          src.BuildSampleCount,
		EncodeSampleCount:         src.EncodeSampleCount,
		SendSampleCount:           src.SendSampleCount,
		DecodeWaitSampleCount:     src.DecodeWaitSampleCount,
		DecodeSampleCount:         src.DecodeSampleCount,
		DispatchWaitSampleCount:   src.DispatchWaitSampleCount,
		ParseStoreSampleCount:     src.ParseStoreSampleCount,
		AvgQPS:                    src.AverageQps,
	}
}

func toProtoWindow(src *monitor.ReportWindow) *controlv1.ReportWindow {
	if src == nil {
		return nil
	}
	return &controlv1.ReportWindow{
		Sequence:                src.Sequence,
		StartedAtUnixNano:       unixNano(src.StartedAt),
		EndedAtUnixNano:         unixNano(src.EndedAt),
		DurationSeconds:         src.DurationSeconds,
		ExpectedIntervalSeconds: src.ExpectedIntervalSeconds,
		Summary:                 toProtoSummary(src.Summary),
		Bandwidth: &controlv1.WindowBandwidthSnapshot{
			SendBytes:                 src.Bandwidth.SendBytes,
			ReceiveBytes:              src.Bandwidth.RecvBytes,
			SendMegabytesPerSecond:    src.Bandwidth.SendMBps,
			ReceiveMegabytesPerSecond: src.Bandwidth.RecvMBps,
		},
		Actions:              toProtoActions(src.Actions),
		InvalidMetricSamples: src.InvalidMetricSamples,
	}
}

func fromProtoWindow(src *controlv1.ReportWindow) *monitor.ReportWindow {
	if src == nil {
		return nil
	}
	out := &monitor.ReportWindow{
		Sequence:                src.Sequence,
		StartedAt:               timeFromUnixNano(src.StartedAtUnixNano),
		EndedAt:                 timeFromUnixNano(src.EndedAtUnixNano),
		DurationSeconds:         src.DurationSeconds,
		ExpectedIntervalSeconds: src.ExpectedIntervalSeconds,
		Summary:                 fromProtoSummary(src.Summary),
		Actions:                 fromProtoActions(src.Actions),
		InvalidMetricSamples:    src.InvalidMetricSamples,
	}
	if src.Bandwidth != nil {
		out.Bandwidth = monitor.WindowBandwidthSnapshot{
			SendBytes: src.Bandwidth.SendBytes,
			RecvBytes: src.Bandwidth.ReceiveBytes,
			SendMBps:  src.Bandwidth.SendMegabytesPerSecond,
			RecvMBps:  src.Bandwidth.ReceiveMegabytesPerSecond,
		}
	}
	return out
}

func toProtoMonitorSystem(src monitor.SystemSnapshot) *controlv1.MonitorSystemSnapshot {
	return &controlv1.MonitorSystemSnapshot{
		Goroutines:               int32(src.Goroutines),
		MemoryAllocatedMegabytes: src.MemAllocMB,
		MemorySystemMegabytes:    src.MemSysMB,
		GarbageCollectionCount:   src.GCCount,
	}
}

func fromProtoMonitorSystem(src *controlv1.MonitorSystemSnapshot) monitor.SystemSnapshot {
	if src == nil {
		return monitor.SystemSnapshot{}
	}
	return monitor.SystemSnapshot{
		Goroutines: int(src.Goroutines),
		MemAllocMB: src.MemoryAllocatedMegabytes,
		MemSysMB:   src.MemorySystemMegabytes,
		GCCount:    src.GarbageCollectionCount,
	}
}

func toProtoRobots(src monitor.RobotSnapshot) *controlv1.RobotSnapshot {
	return &controlv1.RobotSnapshot{Started: src.Started, Running: src.Running, Stopped: src.Stopped, Errored: src.Errored}
}

func fromProtoRobots(src *controlv1.RobotSnapshot) monitor.RobotSnapshot {
	if src == nil {
		return monitor.RobotSnapshot{}
	}
	return monitor.RobotSnapshot{Started: src.Started, Running: src.Running, Stopped: src.Stopped, Errored: src.Errored}
}

func toProtoRampUp(src monitor.RampUpSnapshot) *controlv1.RampUpSnapshot {
	return &controlv1.RampUpSnapshot{CurrentStage: int32(src.CurrentStage), TotalStages: int32(src.TotalStages)}
}

func fromProtoRampUp(src *controlv1.RampUpSnapshot) monitor.RampUpSnapshot {
	if src == nil {
		return monitor.RampUpSnapshot{}
	}
	return monitor.RampUpSnapshot{CurrentStage: int(src.CurrentStage), TotalStages: int(src.TotalStages)}
}

func toProtoConnections(src monitor.ConnectionSnapshot) *controlv1.ConnectionSnapshot {
	return &controlv1.ConnectionSnapshot{
		Established: src.Established,
		Active:      src.Active,
		Closed:      src.Closed,
		Failed:      src.Failed,
		Dropped:     src.Dropped,
	}
}

func fromProtoConnections(src *controlv1.ConnectionSnapshot) monitor.ConnectionSnapshot {
	if src == nil {
		return monitor.ConnectionSnapshot{}
	}
	return monitor.ConnectionSnapshot{
		Established: src.Established,
		Active:      src.Active,
		Closed:      src.Closed,
		Failed:      src.Failed,
		Dropped:     src.Dropped,
	}
}

func toProtoBandwidth(src monitor.BandwidthSnapshot) *controlv1.BandwidthSnapshot {
	return &controlv1.BandwidthSnapshot{
		TotalSendBytes:            src.TotalSendBytes,
		TotalReceiveBytes:         src.TotalRecvBytes,
		SendMegabytesPerSecond:    src.SendMBps,
		ReceiveMegabytesPerSecond: src.RecvMBps,
	}
}

func fromProtoBandwidth(src *controlv1.BandwidthSnapshot) monitor.BandwidthSnapshot {
	if src == nil {
		return monitor.BandwidthSnapshot{}
	}
	return monitor.BandwidthSnapshot{
		TotalSendBytes: src.TotalSendBytes,
		TotalRecvBytes: src.TotalReceiveBytes,
		SendMBps:       src.SendMegabytesPerSecond,
		RecvMBps:       src.ReceiveMegabytesPerSecond,
	}
}

func cloneFloat64(src *float64) *float64 {
	if src == nil {
		return nil
	}
	return new(*src)
}

func cloneBytes(src []byte) []byte {
	if src == nil {
		return nil
	}
	return append([]byte(nil), src...)
}

func unixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func timeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value)
}
