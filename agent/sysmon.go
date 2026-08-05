package agent

import (
	"errors"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"stressbot/utils"
)

type systemProbe interface {
	hostCPUPercent() (float64, error)
	hostMemory() (totalBytes, availableBytes uint64, err error)
	processCPUPercent() (float64, error)
	processRSSBytes() (uint64, error)
	processThreads() (int32, error)
	processFDs() (int32, error)
	hostNetworkCounters() (sentBytes, recvBytes uint64, err error)
}

type gopsutilSystemProbe struct {
	self *process.Process
}

func (p *gopsutilSystemProbe) hostCPUPercent() (float64, error) {
	values, err := cpu.Percent(0, false)
	if err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, errors.New("system CPU metric unavailable")
	}
	return values[0], nil
}

func (p *gopsutilSystemProbe) hostMemory() (uint64, uint64, error) {
	value, err := mem.VirtualMemory()
	if err != nil {
		return 0, 0, err
	}
	return value.Total, value.Available, nil
}

func (p *gopsutilSystemProbe) processCPUPercent() (float64, error) {
	return p.self.Percent(0)
}

func (p *gopsutilSystemProbe) processRSSBytes() (uint64, error) {
	value, err := p.self.MemoryInfo()
	if err != nil {
		return 0, err
	}
	return value.RSS, nil
}

func (p *gopsutilSystemProbe) processThreads() (int32, error) {
	return p.self.NumThreads()
}

func (p *gopsutilSystemProbe) processFDs() (int32, error) {
	return p.self.NumFDs()
}

func (p *gopsutilSystemProbe) hostNetworkCounters() (uint64, uint64, error) {
	// pernic=false returns the host-wide sum across all network interfaces.
	values, err := net.IOCounters(false)
	if err != nil {
		return 0, 0, err
	}
	if len(values) == 0 {
		return 0, 0, errors.New("system network metric unavailable")
	}
	return values[0].BytesSent, values[0].BytesRecv, nil
}

// SystemMonitor collects immutable system snapshots at a fixed interval.
type SystemMonitor struct {
	interval    time.Duration
	static      StaticInfo
	probe       systemProbe
	logicalCPUs int
	now         func() time.Time

	mu     sync.RWMutex
	latest SystemSnapshot

	hostCPUInitialized    bool
	processCPUInitialized bool
	networkInitialized    bool
	prevNetSent           uint64
	prevNetRecv           uint64
	prevNetAt             time.Time
	sequence              uint64
}

func NewSystemMonitor(interval time.Duration, static StaticInfo) (*SystemMonitor, error) {
	self, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return nil, err
	}
	if value, err := mem.VirtualMemory(); err == nil {
		static.MemTotalBytes = value.Total
	}
	return &SystemMonitor{
		interval:    interval,
		static:      static,
		probe:       &gopsutilSystemProbe{self: self},
		logicalCPUs: runtime.NumCPU(),
		now:         time.Now,
	}, nil
}

func (m *SystemMonitor) Static() StaticInfo {
	return m.static
}

func (m *SystemMonitor) Snapshot() SystemSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest
}

func (m *SystemMonitor) Start(_ <-chan struct{}) {
	m.collect()
	utils.GetWorkPool().Go(func() { m.loop(utils.GetWorkPool().StopChan()) })
}

func (m *SystemMonitor) loop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			m.collect()
		}
	}
}

func (m *SystemMonitor) collect() {
	now := m.now()
	m.sequence++
	snapshot := SystemSnapshot{Timestamp: now, Sequence: m.sequence}

	if value, err := m.probe.hostCPUPercent(); err == nil {
		if m.hostCPUInitialized {
			snapshot.HostCPUPercent = boundedPercent(value)
		} else {
			m.hostCPUInitialized = true
		}
	}

	if total, available, err := m.probe.hostMemory(); err == nil && total > 0 && available <= total {
		used := total - available
		snapshot.HostMemTotalBytes = new(total)
		snapshot.HostMemUsedBytes = new(used)
		snapshot.HostMemPercent = boundedPercent(float64(used) / float64(total) * 100)
	}

	if value, err := m.probe.processCPUPercent(); err == nil {
		if m.processCPUInitialized && m.logicalCPUs > 0 {
			snapshot.ProcessCPUPercent = boundedPercent(value / float64(m.logicalCPUs))
		} else {
			m.processCPUInitialized = true
		}
	}
	if value, err := m.probe.processRSSBytes(); err == nil {
		snapshot.ProcessRSSBytes = new(value)
	}
	if value, err := m.probe.processThreads(); err == nil {
		snapshot.ProcessThreads = new(value)
	}
	if value, err := m.probe.processFDs(); err == nil {
		snapshot.ProcessFDs = new(value)
	}

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	snapshot.ProcessHeapBytes = stats.HeapAlloc
	snapshot.ProcessGoroutines = runtime.NumGoroutine()

	if sent, recv, err := m.probe.hostNetworkCounters(); err == nil {
		if m.networkInitialized {
			snapshot.HostNetSendBytesPerSec = counterRate(sent, m.prevNetSent, now.Sub(m.prevNetAt))
			snapshot.HostNetRecvBytesPerSec = counterRate(recv, m.prevNetRecv, now.Sub(m.prevNetAt))
		}
		m.prevNetSent = sent
		m.prevNetRecv = recv
		m.prevNetAt = now
		m.networkInitialized = true
	}

	m.mu.Lock()
	m.latest = snapshot
	m.mu.Unlock()
}

func boundedPercent(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	if value > 100 {
		value = 100
	}
	return &value
}

//go:fix inline
func uint64Pointer(value uint64) *uint64 { return new(value) }

//go:fix inline
func int32Pointer(value int32) *int32 { return new(value) }

func counterRate(current, previous uint64, elapsed time.Duration) *float64 {
	if elapsed <= 0 || current < previous {
		return nil
	}
	value := float64(current-previous) / elapsed.Seconds()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}
