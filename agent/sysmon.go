package agent

import (
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"stressbot/utils"
)

// SystemMonitor 系统指标采集器。
// 每 interval 采集一次，缓存最新结果。Snapshot() 只做读锁返回。
type SystemMonitor struct {
	interval time.Duration
	static   StaticInfo

	mu     sync.RWMutex
	latest SystemSnapshot

	// 网络速率差分基线
	prevNetSent uint64
	prevNetRecv uint64
	prevAt      time.Time
	initialized bool

	// 进程句柄
	pid  int32
	self *process.Process
}

// NewSystemMonitor 创建系统监控采集器。
func NewSystemMonitor(interval time.Duration, static StaticInfo) (*SystemMonitor, error) {
	pid := int32(os.Getpid())
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil, err
	}

	// 用 gopsutil 补充 StaticInfo 中的内存信息
	if vm, err := mem.VirtualMemory(); err == nil {
		static.MemTotalMB = vm.Total / 1024 / 1024
	}

	return &SystemMonitor{
		interval: interval,
		static:   static,
		pid:      pid,
		self:     p,
	}, nil
}

// Static 返回启动时采集的静态信息。
func (m *SystemMonitor) Static() StaticInfo {
	return m.static
}

// Snapshot 返回最新一次采集的系统指标快照（只读）。
func (m *SystemMonitor) Snapshot() SystemSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest
}

// Start 启动后台采集循环（非阻塞）。
func (m *SystemMonitor) Start(_ <-chan struct{}) {
	// 首次采集（建立基线，CPU/网络第一次无差分值）
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
	now := time.Now()
	snap := SystemSnapshot{Timestamp: now}

	// CPU
	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		snap.CPUPercent = percents[0]
	}
	if cores, err := cpu.Percent(0, true); err == nil {
		snap.CPUPerCore = cores
	}
	if avg, err := load.Avg(); err == nil {
		snap.LoadAvg1 = avg.Load1
		snap.LoadAvg5 = avg.Load5
		snap.LoadAvg15 = avg.Load15
	}

	// 内存
	if vm, err := mem.VirtualMemory(); err == nil {
		snap.MemTotalMB = vm.Total / 1024 / 1024
		snap.MemUsedMB = vm.Used / 1024 / 1024
		snap.MemPercent = vm.UsedPercent
		if swap, err := mem.SwapMemory(); err == nil {
			snap.SwapUsedMB = swap.Used / 1024 / 1024
		}
	}

	// 进程资源
	if mi, err := m.self.MemoryInfo(); err == nil {
		snap.ProcessRssMB = mi.RSS / 1024 / 1024
	}
	if threads, err := m.self.NumThreads(); err == nil {
		snap.NumThread = threads
	}
	if fds, err := m.self.NumFDs(); err == nil {
		snap.NumFD = fds
	}

	// Go 运行时
	var mstats runtime.MemStats
	runtime.ReadMemStats(&mstats)
	snap.ProcessHeapMB = mstats.HeapAlloc / 1024 / 1024
	snap.ProcessSysMB = mstats.Sys / 1024 / 1024
	snap.NumGoroutine = runtime.NumGoroutine()
	snap.GCCount = mstats.NumGC
	if mstats.NumGC > 0 && len(mstats.PauseNs) > 0 {
		var totalPause uint64
		n := int(mstats.NumGC)
		if n > 256 {
			n = 256
		}
		for i := 0; i < n; i++ {
			idx := (int(mstats.NumGC) - n + i) % 256
			totalPause += mstats.PauseNs[idx]
		}
		snap.GCPauseAvgMs = float64(totalPause) / float64(n) / 1e6
	}

	// 网络速率（差分）
	if counters, err := net.IOCounters(false); err == nil && len(counters) > 0 {
		c := counters[0]
		if m.initialized {
			dt := now.Sub(m.prevAt).Seconds()
			if dt > 0 {
				snap.NetSendKBps = float64(c.BytesSent-m.prevNetSent) / 1024 / dt
				snap.NetRecvKBps = float64(c.BytesRecv-m.prevNetRecv) / 1024 / dt
			}
		}
		m.prevNetSent = c.BytesSent
		m.prevNetRecv = c.BytesRecv
		m.prevAt = now
		m.initialized = true
	}

	m.mu.Lock()
	m.latest = snap
	m.mu.Unlock()
}
