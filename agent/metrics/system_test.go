package metrics

import (
	"errors"
	"math"
	"testing"
	"time"
)

type fakeSystemProbe struct {
	hostCPUValues    []float64
	processCPUValues []float64
	hostCPUCalls     int
	processCPUCalls  int

	memoryTotal     uint64
	memoryAvailable uint64
	memoryErr       error
	processRSS      uint64
	processRSSErr   error
	threads         int32
	threadsErr      error
	fds             int32
	fdsErr          error

	netSentValues []uint64
	netRecvValues []uint64
	netCalls      int
	netErr        error
}

func (p *fakeSystemProbe) hostCPUPercent() (float64, error) {
	if p.hostCPUCalls >= len(p.hostCPUValues) {
		return 0, errors.New("host cpu unavailable")
	}
	v := p.hostCPUValues[p.hostCPUCalls]
	p.hostCPUCalls++
	return v, nil
}

func (p *fakeSystemProbe) hostMemory() (uint64, uint64, error) {
	return p.memoryTotal, p.memoryAvailable, p.memoryErr
}

func (p *fakeSystemProbe) processCPUPercent() (float64, error) {
	if p.processCPUCalls >= len(p.processCPUValues) {
		return 0, errors.New("process cpu unavailable")
	}
	v := p.processCPUValues[p.processCPUCalls]
	p.processCPUCalls++
	return v, nil
}

func (p *fakeSystemProbe) processRSSBytes() (uint64, error) {
	return p.processRSS, p.processRSSErr
}

func (p *fakeSystemProbe) processThreads() (int32, error) {
	return p.threads, p.threadsErr
}

func (p *fakeSystemProbe) processFDs() (int32, error) {
	return p.fds, p.fdsErr
}

func (p *fakeSystemProbe) hostNetworkCounters() (uint64, uint64, error) {
	if p.netErr != nil {
		return 0, 0, p.netErr
	}
	if p.netCalls >= len(p.netSentValues) || p.netCalls >= len(p.netRecvValues) {
		return 0, 0, errors.New("network counters unavailable")
	}
	sent := p.netSentValues[p.netCalls]
	recv := p.netRecvValues[p.netCalls]
	p.netCalls++
	return sent, recv, nil
}

func newTestSystemMonitor(probe systemProbe, logicalCPUs int, times ...time.Time) *SystemMonitor {
	idx := 0
	return &SystemMonitor{
		probe:       probe,
		logicalCPUs: logicalCPUs,
		now: func() time.Time {
			v := times[idx]
			idx++
			return v
		},
	}
}

func TestSystemMonitorUsesFirstDifferentialSamplesOnlyAsBaselines(t *testing.T) {
	t0 := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	probe := &fakeSystemProbe{
		hostCPUValues:    []float64{75, 80},
		processCPUValues: []float64{800, 640},
		memoryTotal:      1_000,
		memoryAvailable:  250,
		processRSS:       123,
		threads:          7,
		fds:              11,
		netSentValues:    []uint64{1_000, 3_000},
		netRecvValues:    []uint64{2_000, 5_000},
	}
	monitor := newTestSystemMonitor(probe, 16, t0, t0.Add(2*time.Second))

	monitor.collect()
	first := monitor.Snapshot()
	if first.Sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", first.Sequence)
	}
	if first.HostCPUPercent != nil || first.ProcessCPUPercent != nil {
		t.Fatalf("first CPU samples must be baselines: host=%v process=%v", first.HostCPUPercent, first.ProcessCPUPercent)
	}
	if first.HostNetSendBytesPerSec != nil || first.HostNetRecvBytesPerSec != nil {
		t.Fatalf("first network sample must be a baseline: send=%v recv=%v", first.HostNetSendBytesPerSec, first.HostNetRecvBytesPerSec)
	}

	monitor.collect()
	second := monitor.Snapshot()
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", second.Sequence)
	}
	assertFloatPointer(t, "host CPU", second.HostCPUPercent, 80)
	assertFloatPointer(t, "process CPU", second.ProcessCPUPercent, 40)
	assertFloatPointer(t, "host send rate", second.HostNetSendBytesPerSec, 1_000)
	assertFloatPointer(t, "host receive rate", second.HostNetRecvBytesPerSec, 1_500)
}

func TestSystemMonitorPublishesExactMemoryAndProcessValues(t *testing.T) {
	t0 := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	probe := &fakeSystemProbe{
		hostCPUValues:    []float64{10},
		processCPUValues: []float64{10},
		memoryTotal:      10_000,
		memoryAvailable:  2_500,
		processRSS:       1_234,
		threads:          17,
		fds:              29,
		netSentValues:    []uint64{1},
		netRecvValues:    []uint64{1},
	}
	monitor := newTestSystemMonitor(probe, 8, t0)

	monitor.collect()
	snapshot := monitor.Snapshot()
	assertUint64Pointer(t, "host total memory", snapshot.HostMemTotalBytes, 10_000)
	assertUint64Pointer(t, "host used memory", snapshot.HostMemUsedBytes, 7_500)
	assertFloatPointer(t, "host memory percent", snapshot.HostMemPercent, 75)
	assertUint64Pointer(t, "process RSS", snapshot.ProcessRSSBytes, 1_234)
	assertInt32Pointer(t, "process threads", snapshot.ProcessThreads, 17)
	assertInt32Pointer(t, "process handles", snapshot.ProcessFDs, 29)
	if snapshot.ProcessHeapBytes == 0 || snapshot.ProcessGoroutines == 0 {
		t.Fatalf("Go runtime metrics must remain available: heap=%d goroutines=%d", snapshot.ProcessHeapBytes, snapshot.ProcessGoroutines)
	}
}

func TestSystemMonitorKeepsFailedProbeFieldsUnknown(t *testing.T) {
	t0 := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	probe := &fakeSystemProbe{
		memoryErr:     errors.New("memory unavailable"),
		processRSSErr: errors.New("rss unavailable"),
		threadsErr:    errors.New("threads unavailable"),
		fdsErr:        errors.New("fds unavailable"),
		netErr:        errors.New("network unavailable"),
	}
	monitor := newTestSystemMonitor(probe, 8, t0)

	monitor.collect()
	snapshot := monitor.Snapshot()
	if snapshot.HostCPUPercent != nil || snapshot.HostMemTotalBytes != nil || snapshot.HostMemUsedBytes != nil || snapshot.HostMemPercent != nil {
		t.Fatalf("failed host probes must remain unknown: %+v", snapshot)
	}
	if snapshot.ProcessCPUPercent != nil || snapshot.ProcessRSSBytes != nil || snapshot.ProcessThreads != nil || snapshot.ProcessFDs != nil {
		t.Fatalf("failed process probes must remain unknown: %+v", snapshot)
	}
	if snapshot.HostNetSendBytesPerSec != nil || snapshot.HostNetRecvBytesPerSec != nil {
		t.Fatalf("failed network probe must remain unknown: %+v", snapshot)
	}
}

func assertFloatPointer(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertUint64Pointer(t *testing.T, name string, got *uint64, want uint64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", name, got, want)
	}
}

func assertInt32Pointer(t *testing.T, name string, got *int32, want int32) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", name, got, want)
	}
}

func TestSystemNetworkCounterRateRejectsReset(t *testing.T) {
	if got := counterRate(10, 20, time.Second); got != nil {
		t.Fatalf("计数器回退速率 = %v, want nil", *got)
	}
	if got := counterRate(20, 10, 0); got != nil {
		t.Fatalf("零时间跨度速率 = %v, want nil", *got)
	}
	got := counterRate(30, 10, 2*time.Second)
	if got == nil || *got != 10 {
		t.Fatalf("正常速率 = %v, want 10 bytes/s", got)
	}
}
